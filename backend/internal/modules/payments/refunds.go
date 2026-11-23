package payments

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

type refundRow struct {
	Ref, Op, Merchant, Order, Appt, Clinic, Link, PaymentID, Rule, PolicyVersion string
	Amount                                                                       int64
	Currency, Status                                                             string
	ProviderID                                                                   *string
	FailureCode                                                                  string
	Attempts                                                                     int
	LastCall                                                                     *time.Time
	Next                                                                         time.Time
	ShortfallSince                                                               *time.Time
	UpdatedAt                                                                    time.Time
}

const refundCols = `refund_ref::text, operation_key, merchant_ref, order_ref::text, appointment_ref::text, clinic_ref::text, link_ref::text,
	payment_id, rule_id, policy_version, amount_minor, currency, status, provider_refund_id, failure_code, attempts, last_call_at,
	next_attempt_at, shortfall_since, updated_at`

func scanRefund(r pgx.Row) (refundRow, error) {
	var f refundRow
	err := r.Scan(&f.Ref, &f.Op, &f.Merchant, &f.Order, &f.Appt, &f.Clinic, &f.Link, &f.PaymentID, &f.Rule, &f.PolicyVersion, &f.Amount,
		&f.Currency, &f.Status, &f.ProviderID, &f.FailureCode, &f.Attempts, &f.LastCall, &f.Next, &f.ShortfallSince, &f.UpdatedAt)
	return f, err
}

func outboxEmit(ctx context.Context, tx pgx.Tx, typ, subject string, payload map[string]string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: typ, SubjectRef: subject, Payload: payload})
}

// refundPayload is the common refund event payload; the amount lets the patient be told
// exactly what is coming back (S-157).
func refundPayload(r refundRow, extra map[string]string) map[string]string {
	p := map[string]string{"refundRef": r.Ref, "appointmentRef": r.Appt, "ruleId": r.Rule, "amountMinor": strconv.FormatInt(r.Amount, 10), "currency": r.Currency}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// refundOpKey is the durable operation key: one refund per appointment and rule (API
// standards section 6, TM-12). A duplicate payment is the one case with more than one refund
// for the same rule, so its key also names the payment.
func refundOpKey(appt, rule, paymentID string, duplicate bool) string {
	if duplicate {
		return "refund:" + appt + ":" + rule + ":" + paymentID
	}
	return "refund:" + appt + ":" + rule
}

// insertRefundTx records a refund decision as a `requested` row, inside the caller's
// transaction. It never exceeds what the payment received, and a repeat of the same
// operation key changes nothing.
func (m *Module) insertRefundTx(ctx context.Context, tx pgx.Tx, fx *effects, o orderRow, paymentID string, d refundpolicy.Decision, policyVersion, opKey string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM refund WHERE operation_key=$1)`, opKey).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor),0) FROM refund WHERE order_ref=$1 AND payment_id=$2 AND status <> 'failed'`, o.Ref, paymentID).Scan(&reserved); err != nil {
		return err
	}
	if reserved+d.RefundMinor > o.Amount {
		return m.ticket(ctx, tx, fx, "refund_exceeds:"+opKey, "refund_exceeds_remaining", o.Clinic, o.Appt, AlertRefundNotPossible)
	}
	ref := ids.New()
	now := m.now()
	tag, err := tx.Exec(ctx, `INSERT INTO refund (refund_ref, operation_key, merchant_ref, order_ref, appointment_ref, clinic_ref, link_ref, payment_id,
		rule_id, policy_version, amount_minor, currency, status, next_attempt_at, created_at, updated_at)
		VALUES ($1::uuid,$2,$1::text,$3,$4,$5,$6,$7,$8,$9,$10,$11,'requested',$12,$12,$12) ON CONFLICT (operation_key) DO NOTHING`,
		ref, opKey, o.Ref, o.Appt, o.Clinic, o.Link, paymentID, d.RuleID, policyVersion, d.RefundMinor, o.Currency, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		fx.refunds = append(fx.refunds, ref)
		trig := ""
		if pol, err := m.policyFor(policyVersion); err == nil {
			if t, ok := pol.RuleTrigger(d.RuleID); ok {
				trig = string(t)
			}
		}
		return outboxEmit(ctx, tx, rp.EventRefundRequested, o.Appt, map[string]string{"refundRef": ref, "appointmentRef": o.Appt,
			"ruleId": d.RuleID, "trigger": trig, "pct": strconv.Itoa(d.Pct), "amountMinor": strconv.FormatInt(d.RefundMinor, 10), "currency": o.Currency})
	}
	return nil
}

func (m *Module) backoff(attempts int) time.Duration {
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(m.cfg.RefundBackoff) {
		i = len(m.cfg.RefundBackoff) - 1
	}
	return m.cfg.RefundBackoff[i]
}

// dispatchRefund claims a due `requested` (or shortfall-`escalated`) refund and calls the
// vendor ONCE, with the stored operation key and merchant reference. The claim moves it to
// `processing` first, so a crash is resolved with findRefund, never by a blind second refund.
func (m *Module) dispatchRefund(ctx context.Context, refundRef string) error {
	now := m.now()
	r, err := scanRefund(m.pool.QueryRow(ctx, `UPDATE refund SET status='processing', attempts=attempts+1, last_call_at=$2, next_attempt_at=$3, updated_at=$2
		WHERE refund_ref=$1 AND status IN ('requested','escalated') AND next_attempt_at<=$2 AND provider_refund_id IS NULL RETURNING `+refundCols,
		refundRef, now, now.Add(m.cfg.CallLease)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	res, callErr := m.port.Refund(ctx, adp.RefundRequest{ClinicLinkRef: r.Link, PaymentID: r.PaymentID, Amount: adp.Money{Minor: r.Amount, Currency: r.Currency},
		IdempotencyKey: r.Op, MerchantRefundRef: r.Merchant, ReasonCode: r.Rule})
	return m.afterRefundCall(ctx, r, res, callErr)
}

func (m *Module) afterRefundCall(ctx context.Context, r refundRow, res adp.Refund, callErr error) error {
	now := m.now()
	fx := &effects{}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx = &effects{}
		switch adapters.KindOf(callErr) {
		case "":
			if (res.MerchantRefundRef != "" && res.MerchantRefundRef != r.Merchant) || res.Amount.Minor != r.Amount || res.Amount.Currency != r.Currency || res.ProviderRefundID == "" {
				return m.failRefund(ctx, tx, fx, r, "VENDOR_MISMATCH")
			}
			// The vendor accepted it. It is NOT succeeded until a verified webhook or a status poll says so.
			_, err := tx.Exec(ctx, `UPDATE refund SET status='processing', provider_refund_id=$2, failure_code='', next_attempt_at=$3, updated_at=$3
				WHERE refund_ref=$1 AND status IN ('processing','unknown')`, r.Ref, res.ProviderRefundID, now)
			return err
		case adapters.RateLimited:
			_, err := tx.Exec(ctx, `UPDATE refund SET status='requested', next_attempt_at=$2, updated_at=$3 WHERE refund_ref=$1 AND status='processing'`,
				r.Ref, now.Add(m.cfg.CallBackoff), now)
			return err
		case adapters.AuthFailed:
			if _, err := tx.Exec(ctx, `UPDATE refund SET status='requested', next_attempt_at=$2, failure_code='AUTH_FAILED', updated_at=$3 WHERE refund_ref=$1 AND status='processing'`,
				r.Ref, now.Add(time.Hour), now); err != nil {
				return err
			}
			return m.alertOnly(ctx, tx, fx, AlertAuthFailed, r.Clinic, r.Ref)
		case adapters.Rejected:
			if reasonOf(callErr) == adp.ReasonInsufficientBalance {
				return m.shortfall(ctx, tx, fx, r)
			}
			return m.failRefund(ctx, tx, fx, r, reasonOf(callErr))
		case adapters.Invalid, adapters.NotFound:
			return m.failRefund(ctx, tx, fx, r, reasonOf(callErr))
		default: // Timeout, Unavailable and anything unrecognised: outcome unknown
			_, err := tx.Exec(ctx, `UPDATE refund SET status='unknown', next_attempt_at=$2, updated_at=$2 WHERE refund_ref=$1 AND status='processing'`, r.Ref, now)
			return err
		}
	})
	if err == nil {
		m.flush(ctx, fx)
	}
	return err
}

// shortfall handles E-PAY-05: the clinic's account cannot fund the refund. Retry with
// backoff (same key and merchant reference), alert the clinic once, open a grievance ticket
// once. New bookings pause after the configured time (ClinicPaymentsPaused).
func (m *Module) shortfall(ctx context.Context, tx pgx.Tx, fx *effects, r refundRow) error {
	now := m.now()
	if _, err := tx.Exec(ctx, `UPDATE refund SET status='escalated', failure_code=$2, shortfall_since=COALESCE(shortfall_since,$3),
		escalated_at=COALESCE(escalated_at,$3), next_attempt_at=$4, updated_at=$3 WHERE refund_ref=$1 AND status='processing'`,
		r.Ref, adp.ReasonInsufficientBalance, now, now.Add(m.backoff(r.Attempts))); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO ops_ticket (ticket_ref, ticket_key, kind, clinic_ref, subject_ref, created_at)
		VALUES ($1,$2,'grievance_refund_shortfall',$3,$4,$5) ON CONFLICT (ticket_key) DO NOTHING`, ids.New(), "shortfall:"+r.Ref, r.Clinic, r.Ref, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 { // first time only
		if err := m.alertOnly(ctx, tx, fx, AlertRefundShortfall, r.Clinic, r.Ref); err != nil {
			return err
		}
		return outboxEmit(ctx, tx, rp.EventRefundEscalated, r.Appt, refundPayload(r, map[string]string{"clinicRef": r.Clinic}))
	}
	return nil
}

func (m *Module) failRefund(ctx context.Context, tx pgx.Tx, fx *effects, r refundRow, code string) error {
	tag, err := tx.Exec(ctx, `UPDATE refund SET status='failed', failure_code=$2, updated_at=$3 WHERE refund_ref=$1 AND status NOT IN ('succeeded','failed')`, r.Ref, code, m.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := m.ticket(ctx, tx, fx, "refund_failed:"+r.Ref, "refund_failed", r.Clinic, r.Ref, AlertRefundFailed); err != nil {
		return err
	}
	return outboxEmit(ctx, tx, rp.EventRefundFailed, r.Appt, refundPayload(r, nil))
}

// succeedRefund marks a refund succeeded (from a verified webhook or a status poll) and
// posts it to the ledger, once.
func (m *Module) succeedRefund(ctx context.Context, tx pgx.Tx, r refundRow, providerRefundID string, at time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE refund SET status='succeeded', succeeded_at=$2, provider_refund_id=COALESCE(provider_refund_id, NULLIF($3,'')),
		failure_code='', updated_at=$2 WHERE refund_ref=$1 AND status <> 'succeeded'`, r.Ref, at, providerRefundID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := m.postRefund(ctx, tx, r, at); err != nil {
		return err
	}
	return outboxEmit(ctx, tx, rp.EventRefundSucceeded, r.Appt, refundPayload(r, nil))
}

// resolveRefund settles an `unknown` (or stale `processing`) refund with findRefund. It
// never calls refund again until the vendor's settle time has passed with NotFound; then it
// repeats the call with the SAME idempotency key and merchant reference.
func (m *Module) resolveRefund(ctx context.Context, r refundRow) error {
	res, err := m.port.FindRefund(ctx, r.Link, r.PaymentID, r.Merchant)
	now := m.now()
	switch adapters.KindOf(err) {
	case "":
		if res.Amount.Minor != r.Amount || res.Amount.Currency != r.Currency || res.ProviderRefundID == "" {
			fx := &effects{}
			if err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { fx = &effects{}; return m.failRefund(ctx, tx, fx, r, "VENDOR_MISMATCH") }); err != nil {
				return err
			}
			m.flush(ctx, fx)
			return nil
		}
		// Found: it was applied. Record the vendor ID; success still needs a webhook or a status poll.
		_, err := m.pool.Exec(ctx, `UPDATE refund SET status='processing', provider_refund_id=$2, failure_code='', next_attempt_at=$3, updated_at=$3
			WHERE refund_ref=$1 AND status IN ('unknown','processing') AND provider_refund_id IS NULL`, r.Ref, res.ProviderRefundID, now)
		return err
	case adapters.NotFound:
		if r.LastCall == nil || now.Sub(*r.LastCall) < m.cfg.SettleAfter {
			return nil
		}
		tag, err := m.pool.Exec(ctx, `UPDATE refund SET status='requested', next_attempt_at=$2, updated_at=$2
			WHERE refund_ref=$1 AND status IN ('unknown','processing') AND provider_refund_id IS NULL AND attempts=$3`, r.Ref, now, r.Attempts)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return m.dispatchRefund(ctx, r.Ref)
		}
		return nil
	default:
		m.log.Warn("payments.refund_resolve_deferred", "op", "find_refund", "kind", string(adapters.KindOf(err)))
		return nil
	}
}

// pollRefund asks the vendor for a refund's status (webhook-late fallback).
func (m *Module) pollRefund(ctx context.Context, r refundRow) error {
	res, err := m.port.GetRefundStatus(ctx, r.Link, str(r.ProviderID))
	if err != nil {
		return nil // try again next round
	}
	if res.Amount.Minor != r.Amount || res.Amount.Currency != r.Currency {
		return nil
	}
	fx := &effects{}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx = &effects{}
		cur, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundCols+` FROM refund WHERE refund_ref=$1 FOR UPDATE`, r.Ref))
		if err != nil {
			return err
		}
		switch res.Status {
		case adp.RefundProcessed:
			at := res.ProcessedAt
			if at.IsZero() {
				at = m.now()
			}
			return m.succeedRefund(ctx, tx, cur, res.ProviderRefundID, at)
		case adp.RefundFailed:
			if cur.Status == "succeeded" {
				return nil
			}
			return m.failRefund(ctx, tx, fx, cur, "VENDOR_FAILED")
		}
		return nil
	})
	if err == nil {
		m.flush(ctx, fx)
	}
	return err
}

// RefundStatus returns the latest refund of an appointment.
func (m *Module) RefundStatus(ctx context.Context, appointmentRef string) (rp.RefundInfo, error) {
	if !ids.Valid(appointmentRef) {
		return rp.RefundInfo{}, rp.ErrNotFound
	}
	r, err := scanRefund(m.pool.QueryRow(ctx, `SELECT `+refundCols+` FROM refund WHERE appointment_ref=$1 ORDER BY refund_ref DESC LIMIT 1`, appointmentRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return rp.RefundInfo{}, rp.ErrNotFound
	}
	if err != nil {
		return rp.RefundInfo{}, err
	}
	return rp.RefundInfo{RefundRef: r.Ref, RuleID: r.Rule, State: rp.RefundState(r.Status), AmountMinor: r.Amount, Currency: r.Currency, UpdatedAt: r.UpdatedAt}, nil
}

// RetryRefundNow lets a clinic admin, after topping up the balance, bring the next retry
// of a shortfall refund forward. It changes only the time; the retry is the same call with
// the same operation key and merchant reference, so it can never refund twice.
func (m *Module) RetryRefundNow(ctx context.Context, clinicRef, refundRef string) error {
	if !ids.Valid(clinicRef) || !ids.Valid(refundRef) {
		return rp.ErrNotFound
	}
	tag, err := m.pool.Exec(ctx, `UPDATE refund SET next_attempt_at=$3, updated_at=$3 WHERE refund_ref=$1 AND clinic_ref=$2 AND status='escalated'`, refundRef, clinicRef, m.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return rp.ErrNotFound
	}
	return nil
}

// ---- appointment events (booking -> payments), module-boundaries section 4 ----

type apptEvent struct {
	Appt, ByWhom, PolicyVersion, Rule string
	Mode                              refundpolicy.Mode
	MinutesPrior                      *int
}

func parseApptEvent(e outbox.Event) (apptEvent, error) {
	a := apptEvent{Appt: e.Payload["appointmentRef"], ByWhom: e.Payload["byWhom"], PolicyVersion: e.Payload["policyVersion"],
		Rule: e.Payload["rule"], Mode: refundpolicy.Mode(e.Payload["mode"])}
	if !ids.Valid(a.Appt) || a.PolicyVersion == "" {
		return a, fmt.Errorf("%w: appointment event needs appointmentRef and policyVersion", rp.ErrInvalid)
	}
	if a.Mode != refundpolicy.Online && a.Mode != refundpolicy.InPerson && a.Mode != refundpolicy.Unknown {
		return a, fmt.Errorf("%w: mode", rp.ErrInvalid)
	}
	if s := e.Payload["minutesBeforeSlot"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return a, fmt.Errorf("%w: minutesBeforeSlot", rp.ErrInvalid)
		}
		a.MinutesPrior = &n
	}
	return a, nil
}

// OnAppointmentCancelled applies the refund policy when booking cancels an appointment.
// Subscribe it as rp.SubscriberCancelled to rp.EventAppointmentCancelled.
func (m *Module) OnAppointmentCancelled(ctx context.Context, e outbox.Event) error {
	return m.onAppointment(ctx, rp.SubscriberCancelled, e, "cancelled")
}

// OnAppointmentExpired handles an unpaid or unconfirmed hold that timed out (E-PAY-01).
func (m *Module) OnAppointmentExpired(ctx context.Context, e outbox.Event) error {
	return m.onAppointment(ctx, rp.SubscriberExpired, e, "expired")
}

// OnAppointmentNoShow handles a no-show; byWhom is "patient" or "doctor".
func (m *Module) OnAppointmentNoShow(ctx context.Context, e outbox.Event) error {
	return m.onAppointment(ctx, rp.SubscriberNoShow, e, "no_show")
}

func (m *Module) onAppointment(ctx context.Context, handler string, e outbox.Event, state string) error {
	a, err := parseApptEvent(e)
	if err != nil {
		return err
	}
	fx := &effects{}
	err = outbox.Once(ctx, m.pool, handler, e, func(tx pgx.Tx) error {
		fx = &effects{}
		o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE appointment_ref=$1 FOR UPDATE`, a.Appt))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no online order (for example pay at clinic): nothing to refund
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_order SET appointment_state=$2, updated_at=$3 WHERE order_ref=$1 AND appointment_state='open'`, o.Ref, state, m.now()); err != nil {
			return err
		}
		if state == "expired" {
			if _, err := tx.Exec(ctx, `UPDATE payment_order SET status='expired', updated_at=$2 WHERE order_ref=$1 AND status='created' AND payment_id IS NULL`, o.Ref, m.now()); err != nil {
				return err
			}
		}
		if o.Status != string(rp.OrderCaptured) {
			return nil // nothing paid yet; a later capture is refunded in full (E-PAY-01, E-PAY-02)
		}
		return m.refundForAppointment(ctx, tx, fx, o, a, state)
	})
	if err != nil {
		return err
	}
	m.flush(ctx, fx)
	return nil
}

// refundForAppointment maps the event to a policy trigger, asks the engine, and records the
// refund. A blocked rule (RF08, RF09; FAIRNESS-01) creates a ticket and NO refund decision.
func (m *Module) refundForAppointment(ctx context.Context, tx pgx.Tx, fx *effects, o orderRow, a apptEvent, state string) error {
	pol, err := m.policyFor(a.PolicyVersion)
	if err != nil {
		return err
	}
	var trig refundpolicy.Trigger
	switch state {
	case "expired":
		trig = refundpolicy.PaymentDebitedBookingFailed
	case "cancelled":
		switch a.ByWhom {
		case "patient":
			trig = refundpolicy.PatientCancelled
		case "doctor", "clinic", "system":
			trig = refundpolicy.ClinicOrDoctorCancelled
		default:
			return fmt.Errorf("%w: byWhom", rp.ErrInvalid)
		}
	case "no_show":
		switch a.ByWhom {
		case "doctor":
			trig = refundpolicy.DoctorNotJoined
		case "patient":
			trig = refundpolicy.PatientNoShow
		default:
			return fmt.Errorf("%w: byWhom", rp.ErrInvalid)
		}
	}
	if a.Rule != "" { // booking named the rule (for example technical failure or conversion)
		rt, ok := pol.RuleTrigger(a.Rule)
		if !ok || trig == refundpolicy.PatientCancelled || trig == refundpolicy.PatientNoShow {
			return fmt.Errorf("%w: rule", rp.ErrInvalid)
		}
		trig = rt
	}
	if trig == refundpolicy.PatientCancelled && a.MinutesPrior == nil {
		return fmt.Errorf("%w: a patient cancellation needs minutesBeforeSlot", rp.ErrInvalid) // fail closed, never guess
	}
	in := refundpolicy.Input{Trigger: trig, Mode: a.Mode, PaidMinor: o.Amount}
	if a.MinutesPrior != nil {
		in.MinutesPrior = *a.MinutesPrior
	}
	if m.cfg.OverrideFor != nil {
		in.Override = m.cfg.OverrideFor(ctx, o.Clinic)
	}
	d, err := pol.Decide(in)
	switch {
	case errors.Is(err, refundpolicy.ErrBlocked):
		return m.ticket(ctx, tx, fx, "policy_blocked:"+o.Appt+":"+d.RuleID, "refund_policy_blocked", o.Clinic, o.Appt, AlertRefundNotPossible)
	case errors.Is(err, refundpolicy.ErrNoRule):
		return m.ticket(ctx, tx, fx, "policy_norule:"+o.Appt+":"+string(trig), "refund_policy_blocked", o.Clinic, o.Appt, AlertRefundNotPossible)
	case err != nil:
		return err
	}
	if a.Rule != "" && d.RuleID != a.Rule {
		return fmt.Errorf("%w: booking named rule %s but the policy selects %s", rp.ErrInvalid, a.Rule, d.RuleID)
	}
	return m.insertRefundTx(ctx, tx, fx, o, str(o.PaymentID), d, pol.Version, refundOpKey(o.Appt, d.RuleID, str(o.PaymentID), false))
}

// policyInput builds the engine input for a system-triggered refund (RF01, RF02), where
// the mode does not matter.
func policyInput(trigger string, paid int64) refundpolicy.Input {
	return refundpolicy.Input{Trigger: refundpolicy.Trigger(trigger), Mode: refundpolicy.Unknown, PaidMinor: paid}
}
