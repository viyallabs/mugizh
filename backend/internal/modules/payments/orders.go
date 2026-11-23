package payments

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

type orderRow struct {
	Ref, Op, Merchant, Appt, Clinic, Link string
	Amount                                int64
	Currency, Status, ApptState           string
	ProviderID, PaymentID                 *string
	CheckoutURL                           string
	CapturedAt                            *time.Time
	HoldExpires                           time.Time
	Attempts                              int
	LastCall                              *time.Time
	Next                                  time.Time
	// Document profile copied when the order was created (S-134); DocType "" means no document.
	DocType, DocLegalName, DocAddress, DocTaxID string
}

const orderCols = `order_ref::text, operation_key, merchant_ref, appointment_ref::text, clinic_ref::text, link_ref::text,
	amount_minor, currency, status, appointment_state, provider_order_id, checkout_url, payment_id, captured_at,
	hold_expires_at, attempts, last_call_at, next_attempt_at, doc_type, doc_legal_name, doc_address, doc_tax_id`

func scanOrder(r pgx.Row) (orderRow, error) {
	var o orderRow
	err := r.Scan(&o.Ref, &o.Op, &o.Merchant, &o.Appt, &o.Clinic, &o.Link, &o.Amount, &o.Currency, &o.Status, &o.ApptState,
		&o.ProviderID, &o.CheckoutURL, &o.PaymentID, &o.CapturedAt, &o.HoldExpires, &o.Attempts, &o.LastCall, &o.Next,
		&o.DocType, &o.DocLegalName, &o.DocAddress, &o.DocTaxID)
	return o, err
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (o orderRow) checkout() rp.Checkout {
	return rp.Checkout{OrderRef: o.Ref, ProviderOrderID: str(o.ProviderID), CheckoutURL: o.CheckoutURL, State: rp.OrderState(o.Status),
		AmountMinor: o.Amount, Currency: o.Currency}
}

// ---- merchant links and settlement mode (S-163) ----

// LinkParams describes a clinic's merchant link. Only credential REFERENCES are accepted.
type LinkParams struct {
	ClinicRef        string
	Aggregator       string
	SettlementMode   string
	CredentialRef    string // secrets-manager reference, "secretref:clinics/<clinicRef>/..."; never the secret
	WebhookSecretRef string
	// ActorRef and RequestID are written to the audit event. An empty ActorRef is recorded as
	// the system actor.
	ActorRef, ActorRole, RequestID string
}

var reSecretRef = regexp.MustCompile(`^secretref:[A-Za-z0-9_./\-]{1,200}$`)

// systemActor is the audit actor for link changes made without a signed-in user.
const systemActor = "system:payments"

// validateLink checks the parameters. A credential or webhook-secret reference must live in
// the clinic's own namespace, secretref:clinics/<clinicRef>/..., so one clinic can never be
// pointed at another clinic's account credentials (review M3).
func (m *Module) validateLink(p LinkParams) error {
	if !ids.Valid(p.ClinicRef) || p.Aggregator == "" {
		return rp.ErrInvalid
	}
	if p.SettlementMode == "split" {
		return rp.ErrSplitNotReady
	}
	if !slices.Contains(m.cfg.AllowedModes, p.SettlementMode) || (p.SettlementMode != string(rp.ClinicCollected) && p.SettlementMode != string(rp.PayAtClinic)) {
		return rp.ErrInvalid
	}
	ns := "secretref:clinics/" + p.ClinicRef + "/"
	okRef := func(s string) bool {
		return reSecretRef.MatchString(s) && strings.HasPrefix(s, ns) && len(s) > len(ns) && !strings.Contains(s, "..")
	}
	switch p.SettlementMode {
	case string(rp.ClinicCollected):
		if !okRef(p.CredentialRef) || !okRef(p.WebhookSecretRef) {
			return rp.ErrInvalid // anything that is not a reference to this clinic's secrets is refused
		}
	default:
		if (p.CredentialRef != "" && !okRef(p.CredentialRef)) || (p.WebhookSecretRef != "" && !okRef(p.WebhookSecretRef)) {
			return rp.ErrInvalid
		}
	}
	return nil
}

func (p LinkParams) actor() (string, string) {
	if p.ActorRef == "" {
		return systemActor, "system"
	}
	role := p.ActorRole
	if role == "" {
		role = "system"
	}
	return p.ActorRef, role
}

// LinkClinic creates a clinic's merchant link and returns the link reference used in the
// webhook URL. It never overwrites: a clinic that already has a link gets ErrLinkExists, and
// the change goes through UpdateClinicLink. Split settlement is refused (v1.2). The change is
// audited in the same transaction.
func (m *Module) LinkClinic(ctx context.Context, p LinkParams) (string, error) {
	if err := m.validateLink(p); err != nil {
		return "", err
	}
	actor, role := p.actor()
	ref := ids.New()
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO clinic_merchant_link (link_ref, clinic_ref, aggregator, settlement_mode, credential_ref, webhook_secret_ref, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7) ON CONFLICT (clinic_ref) DO NOTHING`, ref, p.ClinicRef, p.Aggregator, p.SettlementMode, p.CredentialRef, p.WebhookSecretRef, m.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return rp.ErrLinkExists
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: actor, ActorRole: role, Action: "payments.link_created", ResourceType: "merchant_link", ResourceRef: ref,
			Purpose: p.SettlementMode, Result: ap.Allowed, RequestID: p.RequestID})
	})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// UpdateClinicLink changes an existing link on purpose and audits the old and the new
// mode. It returns the link reference. A change applies to new orders only; existing orders
// keep their link.
func (m *Module) UpdateClinicLink(ctx context.Context, p LinkParams) (string, error) {
	if err := m.validateLink(p); err != nil {
		return "", err
	}
	actor, role := p.actor()
	var ref string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var oldMode string
		if err := tx.QueryRow(ctx, `SELECT link_ref::text, settlement_mode FROM clinic_merchant_link WHERE clinic_ref=$1 FOR UPDATE`, p.ClinicRef).Scan(&ref, &oldMode); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return rp.ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE clinic_merchant_link SET aggregator=$2, settlement_mode=$3, credential_ref=$4, webhook_secret_ref=$5, updated_at=$6 WHERE clinic_ref=$1`,
			p.ClinicRef, p.Aggregator, p.SettlementMode, p.CredentialRef, p.WebhookSecretRef, m.now()); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: actor, ActorRole: role, Action: "payments.link_updated", ResourceType: "merchant_link", ResourceRef: ref,
			Purpose: oldMode + ":" + p.SettlementMode, Result: ap.Allowed, RequestID: p.RequestID})
	})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// LinkInfo is a link as the daily reconciliation job needs it: references only.
type LinkInfo struct {
	LinkRef, ClinicRef, SettlementMode string
}

// ListLinks returns every active clinic-collected link, so the scheduler can run Reconcile
// once a day per link.
func (m *Module) ListLinks(ctx context.Context) ([]LinkInfo, error) {
	rows, err := m.pool.Query(ctx, `SELECT link_ref::text, clinic_ref::text, settlement_mode FROM clinic_merchant_link
		WHERE status='active' AND settlement_mode='clinic_collected' ORDER BY created_at, link_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkInfo{}
	for rows.Next() {
		var l LinkInfo
		if err := rows.Scan(&l.LinkRef, &l.ClinicRef, &l.SettlementMode); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SettlementMode returns the clinic's settlement mode.
func (m *Module) SettlementMode(ctx context.Context, clinicRef string) (rp.SettlementMode, error) {
	if !ids.Valid(clinicRef) {
		return "", rp.ErrInvalid
	}
	var mode string
	err := m.pool.QueryRow(ctx, `SELECT settlement_mode FROM clinic_merchant_link WHERE clinic_ref=$1 AND status='active'`, clinicRef).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", rp.ErrNoMerchant
	}
	return rp.SettlementMode(mode), err
}

// ClinicPaymentsPaused reports whether new bookings must pause for the clinic (E-PAY-05).
// It is derived from the refunds themselves, so it clears by itself once they succeed.
// On any error it answers true (fail closed).
func (m *Module) ClinicPaymentsPaused(ctx context.Context, clinicRef string) (bool, error) {
	if !ids.Valid(clinicRef) {
		return true, rp.ErrInvalid
	}
	p, err := m.pausedSince(ctx, m.pool, clinicRef)
	if err != nil {
		return true, err
	}
	return p != nil, nil
}

func (m *Module) pausedSince(ctx context.Context, q queryer, clinicRef string) (*time.Time, error) {
	var since *time.Time
	err := q.QueryRow(ctx, `SELECT min(shortfall_since) FROM refund WHERE clinic_ref=$1 AND status='escalated' AND failure_code=$2 AND shortfall_since <= $3`,
		clinicRef, adp.ReasonInsufficientBalance, m.now().Add(-m.cfg.PauseAfter)).Scan(&since)
	return since, err
}

// ---- creating orders ----

// CreateOrder implements the public interface (S-125).
func (m *Module) CreateOrder(ctx context.Context, appointmentRef, clinicRef string, amountMinor int64, currency string, holdExpiresAt time.Time) (rp.Checkout, error) {
	if !ids.Valid(appointmentRef) || !ids.Valid(clinicRef) || amountMinor <= 0 || currency != m.cfg.Currency || !holdExpiresAt.After(m.now()) {
		return rp.Checkout{}, rp.ErrInvalid
	}
	opKey := "order:" + appointmentRef
	var o orderRow
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		existing, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE operation_key=$1`, opKey))
		if err == nil {
			o = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var link, mode, status string
		err = tx.QueryRow(ctx, `SELECT link_ref::text, settlement_mode, status FROM clinic_merchant_link WHERE clinic_ref=$1`, clinicRef).Scan(&link, &mode, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return rp.ErrNoMerchant
		}
		if err != nil {
			return err
		}
		if mode == string(rp.PayAtClinic) {
			return rp.ErrPayAtClinic
		}
		if mode != string(rp.ClinicCollected) || status != "active" {
			return rp.ErrNoMerchant
		}
		since, err := m.pausedSince(ctx, tx, clinicRef)
		if err != nil {
			return err
		}
		if since != nil {
			return rp.ErrClinicPaused
		}
		ref := ids.New()
		now := m.now()
		// S-134: the document profile in force now is copied onto the order, so a later change
		// applies only to new bookings (money-flow section 6).
		docType, docName, docAddr, docTax, err := m.documentSnapshot(ctx, tx, clinicRef)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO payment_order (order_ref, operation_key, merchant_ref, appointment_ref, clinic_ref, link_ref, amount_minor, currency,
			status, hold_expires_at, next_attempt_at, created_at, updated_at, doc_type, doc_legal_name, doc_address, doc_tax_id)
			VALUES ($1::uuid,$2,$1::text,$3,$4,$5,$6,$7,'requested',$8,$9,$9,$9,$10,$11,$12,$13)
			ON CONFLICT (operation_key) DO NOTHING`, ref, opKey, appointmentRef, clinicRef, link, amountMinor, currency, holdExpiresAt.UTC(), now,
			docType, docName, docAddr, docTax); err != nil {
			return err
		}
		o, err = scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE operation_key=$1`, opKey))
		return err
	})
	if err != nil {
		return rp.Checkout{}, err
	}
	// The same appointment can never be re-priced or moved to another clinic.
	if o.Clinic != clinicRef || o.Amount != amountMinor || o.Currency != currency {
		return rp.Checkout{}, rp.ErrConflict
	}
	if o.Status == string(rp.OrderRequested) {
		if err := m.dispatchOrder(ctx, o.Ref); err != nil {
			return rp.Checkout{}, err
		}
		if o, err = scanOrder(m.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE order_ref=$1`, o.Ref)); err != nil {
			return rp.Checkout{}, err
		}
	}
	if o.Status == string(rp.OrderFailed) {
		return rp.Checkout{}, rp.ErrOrderFailed
	}
	return o.checkout(), nil
}

// dispatchOrder claims a `requested` order and calls the vendor ONCE. The claim moves the
// row to `processing` before the call, so a crash leaves a record that is resolved with
// findOrder, never one that looks untouched.
func (m *Module) dispatchOrder(ctx context.Context, orderRef string) error {
	now := m.now()
	o, err := scanOrder(m.pool.QueryRow(ctx, `UPDATE payment_order SET status='processing', attempts=attempts+1, last_call_at=$2, next_attempt_at=$3, updated_at=$2
		WHERE order_ref=$1 AND status='requested' AND next_attempt_at<=$2 RETURNING `+orderCols, orderRef, now, now.Add(m.cfg.CallLease)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	res, callErr := m.port.CreateOrder(ctx, adp.CreateOrderRequest{ClinicLinkRef: o.Link, Amount: adp.Money{Minor: o.Amount, Currency: o.Currency},
		IdempotencyKey: o.Op, MerchantOrderRef: o.Merchant, AppointmentRef: o.Appt})
	return m.afterCreateOrder(ctx, o, res, callErr)
}

func (m *Module) afterCreateOrder(ctx context.Context, o orderRow, res adp.Order, callErr error) error {
	now := m.now()
	fx := &effects{}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx = &effects{}
		switch adapters.KindOf(callErr) {
		case "":
			if (res.MerchantOrderRef != "" && res.MerchantOrderRef != o.Merchant) || res.Amount.Minor != o.Amount || res.Amount.Currency != o.Currency || res.ProviderOrderID == "" {
				return m.failOrder(ctx, tx, fx, o, "VENDOR_MISMATCH")
			}
			if !secureCheckoutURL(res.CheckoutURL) {
				return m.failOrder(ctx, tx, fx, o, "VENDOR_BAD_URL")
			}
			_, err := tx.Exec(ctx, `UPDATE payment_order SET status='created', provider_order_id=$2, checkout_url=$3, failure_code='', updated_at=$4
				WHERE order_ref=$1 AND status IN ('processing','unknown')`, o.Ref, res.ProviderOrderID, res.CheckoutURL, now)
			return err
		case adapters.RateLimited: // definitely not applied: back to requested after a wait
			_, err := tx.Exec(ctx, `UPDATE payment_order SET status='requested', next_attempt_at=$2, updated_at=$3 WHERE order_ref=$1 AND status='processing'`,
				o.Ref, now.Add(m.cfg.CallBackoff), now)
			return err
		case adapters.AuthFailed:
			if _, err := tx.Exec(ctx, `UPDATE payment_order SET status='requested', next_attempt_at=$2, failure_code='AUTH_FAILED', updated_at=$3 WHERE order_ref=$1 AND status='processing'`,
				o.Ref, now.Add(time.Hour), now); err != nil {
				return err
			}
			return m.alertOnly(ctx, tx, fx, AlertAuthFailed, o.Clinic, o.Ref)
		case adapters.Rejected, adapters.Invalid, adapters.NotFound:
			return m.failOrder(ctx, tx, fx, o, reasonOf(callErr))
		default: // Timeout, Unavailable and anything unrecognised: outcome unknown
			_, err := tx.Exec(ctx, `UPDATE payment_order SET status='unknown', next_attempt_at=$2, updated_at=$2 WHERE order_ref=$1 AND status='processing'`, o.Ref, now)
			return err
		}
	})
	if err == nil {
		m.flush(ctx, fx)
	}
	return err
}

func reasonOf(err error) string {
	var e *adapters.Error
	if errors.As(err, &e) && regexp.MustCompile(`^[A-Z0-9_]{1,40}$`).MatchString(e.Reason) {
		return e.Reason
	}
	return "REJECTED"
}

func (m *Module) failOrder(ctx context.Context, tx pgx.Tx, fx *effects, o orderRow, code string) error {
	if _, err := tx.Exec(ctx, `UPDATE payment_order SET status='failed', failure_code=$2, updated_at=$3 WHERE order_ref=$1 AND status IN ('processing','unknown')`, o.Ref, code, m.now()); err != nil {
		return err
	}
	if err := m.ticket(ctx, tx, fx, "order_failed:"+o.Ref, "order_failed", o.Clinic, o.Ref, AlertOrderFailed); err != nil {
		return err
	}
	return outboxEmit(ctx, tx, rp.EventPaymentFailed, o.Appt, map[string]string{"orderRef": o.Ref, "appointmentRef": o.Appt})
}

// resolveOrder settles a `unknown` (or stale `processing`) order with findOrder. It never
// calls createOrder until the vendor's settle time has passed with NotFound.
func (m *Module) resolveOrder(ctx context.Context, o orderRow) error {
	res, err := m.port.FindOrder(ctx, o.Link, o.Merchant)
	now := m.now()
	switch adapters.KindOf(err) {
	case "":
		if res.Amount.Minor != o.Amount || res.Amount.Currency != o.Currency || res.ProviderOrderID == "" || !secureCheckoutURL(res.CheckoutURL) {
			fx := &effects{}
			if err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { fx = &effects{}; return m.failOrder(ctx, tx, fx, o, "VENDOR_MISMATCH") }); err != nil {
				return err
			}
			m.flush(ctx, fx)
			return nil
		}
		_, err := m.pool.Exec(ctx, `UPDATE payment_order SET status='created', provider_order_id=$2, checkout_url=$3, updated_at=$4
			WHERE order_ref=$1 AND status IN ('processing','unknown') AND provider_order_id IS NULL AND attempts=$5`, o.Ref, res.ProviderOrderID, res.CheckoutURL, now, o.Attempts)
		return err
	case adapters.NotFound:
		if o.LastCall == nil || now.Sub(*o.LastCall) < m.cfg.SettleAfter {
			return nil // too early to believe NotFound
		}
		// Settle time has passed and the vendor has no such order: safe to repeat the call with
		// the same idempotency key and merchant reference. The attempts check stops two workers
		// both doing it.
		tag, err := m.pool.Exec(ctx, `UPDATE payment_order SET status='requested', next_attempt_at=$2, updated_at=$2
			WHERE order_ref=$1 AND status IN ('processing','unknown') AND provider_order_id IS NULL AND attempts=$3`, o.Ref, now, o.Attempts)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return m.dispatchOrder(ctx, o.Ref)
		}
		return nil
	default:
		m.log.Warn("payments.order_resolve_deferred", "op", "find_order", "kind", string(adapters.KindOf(err)))
		return nil
	}
}

// OrderStatus returns the order of an appointment.
func (m *Module) OrderStatus(ctx context.Context, appointmentRef string) (rp.OrderInfo, error) {
	if !ids.Valid(appointmentRef) {
		return rp.OrderInfo{}, rp.ErrNotFound
	}
	o, err := scanOrder(m.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE appointment_ref=$1`, appointmentRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return rp.OrderInfo{}, rp.ErrNotFound
	}
	if err != nil {
		return rp.OrderInfo{}, err
	}
	return rp.OrderInfo{OrderRef: o.Ref, State: rp.OrderState(o.Status), AmountMinor: o.Amount, Currency: o.Currency, CapturedAt: o.CapturedAt}, nil
}

// secureCheckoutURL accepts only an absolute https URL with a host. Anything else from the
// payment company is refused (fail closed, review M3): a patient is never sent to http: or
// another scheme.
func secureCheckoutURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
