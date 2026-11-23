package booking

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Appointment is the booking record. Times are UTC; the practice and patient zones are
// stored with it (K-ARC-04). The fee and the refund policy version are copied at hold time.
type Appointment struct {
	ID                      string
	PatientRef              string
	PracticeID              string
	ProfessionalRef         string
	ClinicRef               string
	SlotID                  string
	StartsAt                time.Time
	EndsAt                  time.Time
	Mode                    string
	PracticeTimeZone        string
	PatientTimeZone         string
	PatientInIndiaConfirmed *bool
	FeeMinor                int64
	Currency                string
	FeeVersion              string
	PolicyVersion           string
	SettlementMode          string
	State                   string
	CancelledBy             string
	CancelRule              string
	NoShowBy                string
	OrderRef                string
	RescheduleCount         int
	NeedsReschedule         bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
	HoldExpiresAt           *time.Time
	ReconfirmState          string // pending | overdue | reconfirmed | void | ""
}

// PracticeLocalStart is the start in the practice's zone (RFC 3339 with offset).
func (a Appointment) PracticeLocalStart() string { return localIn(a.StartsAt, a.PracticeTimeZone) }

// PatientLocalStart is the start in the patient's zone (RFC 3339 with offset).
func (a Appointment) PatientLocalStart() string { return localIn(a.StartsAt, a.PatientTimeZone) }

func localIn(t time.Time, zone string) string {
	loc, err := loadZone(zone)
	if err != nil {
		return t.UTC().Format(time.RFC3339)
	}
	return t.In(loc).Format(time.RFC3339)
}

const apptCols = `a.id, a.patient_ref, a.practice_id, a.professional_ref, a.clinic_ref, a.slot_id, a.start_at, a.end_at, a.mode,
 a.practice_time_zone, a.patient_time_zone, a.patient_in_india_confirmed, a.fee_minor, a.currency, a.fee_version, a.policy_version,
 a.settlement_mode, a.state, a.cancelled_by, a.cancel_rule, a.no_show_by, a.order_ref, a.reschedule_count, a.needs_reschedule,
 a.created_at, a.updated_at,
 (SELECT h.expires_at FROM hold h WHERE h.appointment_id = a.id),
 COALESCE((SELECT t.state FROM reconfirmation_task t WHERE t.appointment_id = a.id), '')`

func scanAppt(row pgx.Row) (Appointment, error) {
	var a Appointment
	var cancelledBy, noShowBy, orderRef *string
	err := row.Scan(&a.ID, &a.PatientRef, &a.PracticeID, &a.ProfessionalRef, &a.ClinicRef, &a.SlotID, &a.StartsAt, &a.EndsAt, &a.Mode,
		&a.PracticeTimeZone, &a.PatientTimeZone, &a.PatientInIndiaConfirmed, &a.FeeMinor, &a.Currency, &a.FeeVersion, &a.PolicyVersion,
		&a.SettlementMode, &a.State, &cancelledBy, &a.CancelRule, &noShowBy, &orderRef, &a.RescheduleCount, &a.NeedsReschedule,
		&a.CreatedAt, &a.UpdatedAt, &a.HoldExpiresAt, &a.ReconfirmState)
	if err != nil {
		return a, err
	}
	a.StartsAt, a.EndsAt = a.StartsAt.UTC(), a.EndsAt.UTC()
	if cancelledBy != nil {
		a.CancelledBy = *cancelledBy
	}
	if noShowBy != nil {
		a.NoShowBy = *noShowBy
	}
	if orderRef != nil {
		a.OrderRef = *orderRef
	}
	if a.HoldExpiresAt != nil {
		t := a.HoldExpiresAt.UTC()
		a.HoldExpiresAt = &t
	}
	return a, nil
}

// loadAppt reads an appointment; lock makes it SELECT ... FOR UPDATE (inside a transaction).
func loadAppt(ctx context.Context, q querier, id string, lock bool) (Appointment, error) {
	if !ids.Valid(id) {
		return Appointment{}, pgx.ErrNoRows
	}
	sql := `SELECT ` + apptCols + ` FROM appointment a WHERE a.id = $1`
	if lock {
		sql += ` FOR UPDATE OF a`
	}
	return scanAppt(q.QueryRow(ctx, sql, id))
}

func notFoundErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return problem.NotFound
	}
	return err
}

// Mine returns the patient's own appointment. Anyone else's is 404 (K-SEC-04).
func (m *Module) Mine(ctx context.Context, patientRef, id string) (Appointment, error) {
	a, err := loadAppt(ctx, m.pool, id, false)
	if err != nil {
		return Appointment{}, notFoundErr(err)
	}
	if a.PatientRef != patientRef {
		return Appointment{}, problem.NotFound
	}
	return a, nil
}

// ListMine lists the patient's appointments, newest first, with a cursor.
func (m *Module) ListMine(ctx context.Context, patientRef string, limit int, cursor string) ([]Appointment, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{patientRef, limit + 1}
	where := ``
	if cursor != "" {
		sec, id, ok := parseCursor(cursor)
		if !ok {
			return nil, "", problem.BadRequest
		}
		args = append(args, time.Unix(0, sec).UTC(), id)
		where = ` AND (a.start_at, a.id) < ($3, $4::uuid)`
	}
	rows, err := m.pool.Query(ctx, `SELECT `+apptCols+` FROM appointment a WHERE a.patient_ref=$1 AND a.state <> 'expired'`+where+` ORDER BY a.start_at DESC, a.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Appointment{}
	for rows.Next() {
		a, err := scanAppt(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = fmt.Sprintf("%d:%s", last.StartsAt.UnixNano(), last.ID)
	}
	return out, next, nil
}

func parseCursor(c string) (int64, string, bool) {
	a, b, ok := strings.Cut(c, ":")
	if !ok || !ids.Valid(b) {
		return 0, "", false
	}
	n, err := strconv.ParseInt(a, 10, 64)
	return n, b, err == nil
}

// PatientOf implements payments' AppointmentOwners port.
func (m *Module) PatientOf(ctx context.Context, appointmentRef string) (string, bool, error) {
	if !ids.Valid(appointmentRef) {
		return "", false, nil
	}
	var p string
	err := m.pool.QueryRow(ctx, `SELECT patient_ref FROM appointment WHERE id=$1`, appointmentRef).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return p, err == nil, err
}

// Get implements public.Service.
func (m *Module) Get(ctx context.Context, appointmentRef string) (bp.Info, error) {
	a, err := loadAppt(ctx, m.pool, appointmentRef, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return bp.Info{}, bp.ErrNotFound
	}
	if err != nil {
		return bp.Info{}, err
	}
	return bp.Info{ID: a.ID, PatientRef: a.PatientRef, ProfessionalRef: a.ProfessionalRef, ClinicRef: a.ClinicRef, PracticeRef: a.PracticeID,
		Mode: a.Mode, StartsAt: a.StartsAt, EndsAt: a.EndsAt, State: a.State}, nil
}

// ---- checkout ----

// CheckoutResult is what the app needs to pay: the hosted checkout URL (online) or the
// confirmed appointment (pay at clinic).
type CheckoutResult struct {
	Appointment Appointment
	OrderRef    string
	CheckoutURL string
	OrderState  string
	AmountMinor int64
	Currency    string
}

// Checkout opens the hosted checkout for a held appointment (K-PAY-01, K-PAY-02). The
// amount is the fee copied at hold time, never a client value. It is idempotent: the same
// appointment gets the same order. A pay-at-clinic booking was confirmed at hold time and
// returns as it is.
func (m *Module) Checkout(ctx context.Context, patientRef, apptID string) (CheckoutResult, error) {
	a, err := m.Mine(ctx, patientRef, apptID)
	if err != nil {
		return CheckoutResult{}, err
	}
	switch a.State {
	case StateConfirmed:
		return CheckoutResult{Appointment: a, OrderRef: a.OrderRef, AmountMinor: a.FeeMinor, Currency: a.Currency}, nil
	case StateHeld, StatePendingPayment:
	default:
		return CheckoutResult{}, ProblemNotPayable
	}
	now := m.now()
	if a.HoldExpiresAt == nil || !a.HoldExpiresAt.After(now) {
		return CheckoutResult{}, ProblemHoldExpired
	}
	if a.SettlementMode != string(pp.ClinicCollected) {
		return CheckoutResult{}, ProblemNotPayable
	}
	// K-CS-01: the practice must still be listed.
	if _, err := m.d.Directory.GetPractice(ctx, a.PracticeID); err != nil {
		if errors.Is(err, dp.ErrNotFound) {
			return CheckoutResult{}, ProblemPracticeGone
		}
		return CheckoutResult{}, err
	}
	if paused, err := m.d.Payments.ClinicPaymentsPaused(ctx, a.ClinicRef); err != nil {
		return CheckoutResult{}, err // fail closed
	} else if paused {
		return CheckoutResult{}, ProblemPaymentsPaused
	}
	// Record the intent first, so that a hold that times out after the order was created
	// is expired with an event and a late capture is refunded (E-PAY-01).
	if err := m.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE appointment SET state='pending_payment', updated_at=$2 WHERE id=$1 AND state='held'`, a.ID, now)
		return err
	}); err != nil {
		return CheckoutResult{}, err
	}
	co, err := m.d.Payments.CreateOrder(ctx, a.ID, a.ClinicRef, a.FeeMinor, a.Currency, *a.HoldExpiresAt)
	switch {
	case errors.Is(err, pp.ErrClinicPaused):
		return CheckoutResult{}, ProblemPaymentsPaused
	case errors.Is(err, pp.ErrNoMerchant), errors.Is(err, pp.ErrPayAtClinic), errors.Is(err, pp.ErrSplitNotReady):
		return CheckoutResult{}, ProblemPaymentUnavail
	case err != nil:
		return CheckoutResult{}, err
	}
	if err := m.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE appointment SET order_ref=$2, updated_at=$3 WHERE id=$1 AND state IN ('pending_payment','confirmed')`, a.ID, co.OrderRef, now); err != nil {
			return err
		}
		return m.audit(ctx, tx, patientRef, "patient", "booking.checkout", patientRef, a.ID, "")
	}); err != nil {
		return CheckoutResult{}, err
	}
	a, err = m.Mine(ctx, patientRef, apptID)
	if err != nil {
		return CheckoutResult{}, err
	}
	return CheckoutResult{Appointment: a, OrderRef: co.OrderRef, CheckoutURL: co.CheckoutURL, OrderState: string(co.State), AmountMinor: co.AmountMinor, Currency: co.Currency}, nil
}

// ---- state changes shared by several paths (all inside a transaction) ----

func (m *Module) confirmedPayload(a Appointment) map[string]string {
	return map[string]string{"appointmentRef": a.ID, "patientRef": a.PatientRef, "doctorRef": a.ProfessionalRef, "clinicRef": a.ClinicRef,
		"practiceRef": a.PracticeID, "mode": a.Mode, "slotStart": utc(a.StartsAt), "slotEnd": utc(a.EndsAt)}
}

// confirmTx confirms a held or pending appointment: the hold is deleted (the slot stays
// taken), the reconfirmation task is created and appointment.confirmed is emitted.
func (m *Module) confirmTx(ctx context.Context, tx pgx.Tx, a Appointment, actorRef, role, requestID string) error {
	now := m.now()
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='confirmed', confirmed_at=$2, updated_at=$2 WHERE id=$1 AND state IN ('held','pending_payment')`, a.ID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hold WHERE appointment_id=$1`, a.ID); err != nil {
		return err
	}
	if err := m.createReconfirmation(ctx, tx, a, now); err != nil {
		return err
	}
	if err := m.audit(ctx, tx, actorRef, role, "booking.confirm", a.PatientRef, a.ID, requestID); err != nil {
		return err
	}
	return m.emit(ctx, tx, bp.EventConfirmed, a.ID, m.confirmedPayload(a))
}

// createReconfirmation adds the day-before task (E-BK-07) when the appointment is booked
// far enough ahead for the task to fall in the future.
func (m *Module) createReconfirmation(ctx context.Context, tx pgx.Tx, a Appointment, now time.Time) error {
	due := a.StartsAt.Add(-time.Duration(m.cfg.ReconfirmLeadHours) * time.Hour)
	if !due.After(now) {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO reconfirmation_task (id, appointment_id, clinic_ref, due_at, created_at) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (appointment_id) DO UPDATE SET due_at=EXCLUDED.due_at, state='pending', reconfirmed_by=NULL, reconfirmed_at=NULL`,
		ids.New(), a.ID, a.ClinicRef, due, now)
	return err
}

// releaseSlot frees the slot of a dead appointment: reopened, or removed when it no longer
// fits the practice's availability (needs_reschedule).
func (m *Module) releaseSlot(ctx context.Context, tx pgx.Tx, a Appointment) error {
	if a.NeedsReschedule {
		_, err := tx.Exec(ctx, `DELETE FROM slot WHERE id=$1 AND appointment_id=$2`, a.SlotID, a.ID)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE slot SET state='open', appointment_id=NULL WHERE id=$1 AND appointment_id=$2`, a.SlotID, a.ID)
	return err
}

func (m *Module) expiredPayload(a Appointment) map[string]string {
	return map[string]string{"appointmentRef": a.ID, "byWhom": "system", "policyVersion": a.PolicyVersion,
		"mode": refundMode(a.Mode), "consultMode": a.Mode}
}

// expireTx expires a held or pending_payment appointment (hold timeout or a hold the
// patient let go). It reports whether it changed anything. appointment.expired is emitted
// for pending_payment, where an order may exist, so a late capture is refunded in full.
func (m *Module) expireTx(ctx context.Context, tx pgx.Tx, apptID, reason string) (bool, error) {
	a, err := loadAppt(ctx, tx, apptID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.State != StateHeld && a.State != StatePendingPayment {
		return false, nil
	}
	now := m.now()
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='expired', updated_at=$2 WHERE id=$1`, a.ID, now); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hold WHERE appointment_id=$1`, a.ID); err != nil {
		return false, err
	}
	if err := m.releaseSlot(ctx, tx, a); err != nil {
		return false, err
	}
	if err := m.audit(ctx, tx, SystemActor, "system", "booking."+reason, a.PatientRef, a.ID, ""); err != nil {
		return false, err
	}
	if a.State == StatePendingPayment {
		if err := m.emit(ctx, tx, bp.EventExpired, a.ID, m.expiredPayload(a)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// cancelPayload is the appointment.cancelled payload. mode and minutesBeforeSlot are
// always present: payments fails closed without them for a patient cancellation.
func (m *Module) cancelPayload(a Appointment, byWhom, rule string, now time.Time) map[string]string {
	p := m.confirmedPayload(a)
	delete(p, "slotStart")
	delete(p, "slotEnd")
	p["byWhom"] = byWhom
	p["policyVersion"] = a.PolicyVersion
	p["mode"] = refundMode(a.Mode)
	p["consultMode"] = a.Mode
	p["minutesBeforeSlot"] = strconv.Itoa(minutesBefore(now, a.StartsAt))
	if rule != "" {
		p["rule"] = rule
	}
	return p
}

// cancelConfirmedTx cancels a confirmed appointment: slot released, the reconfirmation
// task voided, appointment.cancelled emitted (payments refunds, records revokes T1).
func (m *Module) cancelConfirmedTx(ctx context.Context, tx pgx.Tx, a Appointment, byWhom, rule, actorRef, role, requestID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='cancelled', cancelled_by=$2, cancel_rule=$3, cancelled_at=$4, updated_at=$4 WHERE id=$1`, a.ID, byWhom, rule, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hold WHERE appointment_id=$1`, a.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='void' WHERE appointment_id=$1 AND state IN ('pending','overdue')`, a.ID); err != nil {
		return err
	}
	if err := m.releaseSlot(ctx, tx, a); err != nil {
		return err
	}
	if err := m.audit(ctx, tx, actorRef, role, "booking.cancel", a.PatientRef, a.ID, requestID); err != nil {
		return err
	}
	return m.emit(ctx, tx, bp.EventCancelled, a.ID, m.cancelPayload(a, byWhom, rule, now))
}
