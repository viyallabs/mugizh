package payments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// WebhookOutcome is the result of receiving a webhook.
type WebhookOutcome int

// Outcomes.
const (
	WebhookStored       WebhookOutcome = iota // verified and stored: reply 2xx
	WebhookDuplicate                          // verified, already stored: reply 2xx, nothing more to do
	WebhookUnauthorized                       // unknown clinic link or bad signature: reply 401
	WebhookMalformed                          // signature fine, body unusable: reply 400
)

const maxWebhookBody = 256 << 10

// WebhookHandler is the HTTP handler for POST /webhooks/payments/{clinicLinkRef}. It is NOT
// part of the public OpenAPI spec (the vendor calls it, not an app); the composition root
// mounts it separately on a chi router that provides the clinicLinkRef URL parameter.
func (m *Module) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		out, err := m.HandleWebhook(r.Context(), chi.URLParam(r, "clinicLinkRef"), r.Header, body)
		switch {
		case err != nil:
			w.WriteHeader(http.StatusInternalServerError) // not stored: the vendor retries
		case out == WebhookUnauthorized:
			w.WriteHeader(http.StatusUnauthorized)
		case out == WebhookMalformed:
			w.WriteHeader(http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
}

// HandleWebhook verifies the signature FIRST (constant time, per-clinic secret, inside the
// adapter), then stores the raw event de-duplicated by the vendor's event ID. A 2xx is
// only returned after the event is committed (A7). Nothing else happens inline: the state
// change is made later by ProcessWebhooks.
func (m *Module) HandleWebhook(ctx context.Context, clinicLinkRef string, headers http.Header, body []byte) (WebhookOutcome, error) {
	if !ids.Valid(clinicLinkRef) || len(body) == 0 || len(body) > maxWebhookBody {
		return WebhookUnauthorized, nil
	}
	var active bool
	err := m.pool.QueryRow(ctx, `SELECT true FROM clinic_merchant_link WHERE link_ref=$1 AND status='active' AND settlement_mode='clinic_collected'`, clinicLinkRef).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookUnauthorized, nil // same answer as a bad signature: no link enumeration
	}
	if err != nil {
		return 0, err
	}
	ev, err := m.port.VerifyWebhook(ctx, clinicLinkRef, headers, body)
	if err != nil {
		if adapters.KindOf(err) == adapters.Invalid {
			m.log.Warn("payments.webhook_bad_signature", "op", "verify_webhook", "reason", "invalid")
			return WebhookUnauthorized, nil
		}
		return 0, err
	}
	if ev.ID == "" || len(ev.ID) > 100 || ev.Type == "" || len(ev.Type) > 60 {
		return WebhookMalformed, nil
	}
	tag, err := m.pool.Exec(ctx, `INSERT INTO webhook_event (id, link_ref, vendor_event_id, event_type, merchant_order_ref, provider_order_id, payment_id,
		merchant_refund_ref, provider_refund_id, amount_minor, currency, occurred_at, raw_body, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (link_ref, vendor_event_id) DO NOTHING`,
		ids.New(), clinicLinkRef, ev.ID, string(ev.Type), ev.MerchantOrderRef, ev.ProviderOrderID, ev.PaymentID, ev.MerchantRefundRef, ev.ProviderRefundID,
		ev.Amount.Minor, ev.Amount.Currency, ev.OccurredAt.UTC(), body, m.now())
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return WebhookDuplicate, nil
	}
	return WebhookStored, nil
}

type webhookRow struct {
	ID, Link, VendorID, Type                      string
	MerchantOrderRef, ProviderOrderID, PaymentID  string
	MerchantRefundRef, ProviderRefundID, Currency string
	Amount                                        int64
	At                                            time.Time
}

// reasons that mean "someone sent us something that does not match our records": ticket + alert.
var suspicious = map[string]bool{"order_unknown": true, "amount_mismatch": true, "order_mismatch": true, "refund_unknown": true,
	"refund_mismatch": true, "bad_payment_id": true}

// ProcessWebhooks processes up to limit stored events, oldest first, each in its own
// transaction, and returns how many it handled. Run it from a worker. An event is rejected
// unless its order (or refund), amount and currency all match our records (TC-58, E-SEC-05).
func (m *Module) ProcessWebhooks(ctx context.Context, limit int) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM webhook_event WHERE status='received' ORDER BY received_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var todo []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range todo {
		done, err := m.processOne(ctx, id)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

func (m *Module) processOne(ctx context.Context, id string) (bool, error) {
	done := false
	fx := &effects{}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx, done = &effects{}, false
		var w webhookRow
		err := tx.QueryRow(ctx, `SELECT id::text, link_ref::text, vendor_event_id, event_type, merchant_order_ref, provider_order_id, payment_id,
			merchant_refund_ref, provider_refund_id, amount_minor, currency, occurred_at FROM webhook_event WHERE id=$1 AND status='received' FOR UPDATE SKIP LOCKED`, id).
			Scan(&w.ID, &w.Link, &w.VendorID, &w.Type, &w.MerchantOrderRef, &w.ProviderOrderID, &w.PaymentID, &w.MerchantRefundRef, &w.ProviderRefundID, &w.Amount, &w.Currency, &w.At)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var reason string
		switch adp.EventType(w.Type) {
		case adp.EventPaymentCaptured:
			reason, err = m.applyCapture(ctx, tx, fx, w.Link, capture{MerchantRef: w.MerchantOrderRef, ProviderOrderID: w.ProviderOrderID,
				PaymentID: w.PaymentID, Amount: w.Amount, Currency: w.Currency, At: w.At})
		case adp.EventPaymentFailed:
			reason, err = m.applyFailed(ctx, tx, w)
		case adp.EventRefundSucceeded, adp.EventRefundFailed:
			reason, err = m.applyRefundEvent(ctx, tx, fx, w)
		default:
			reason = "unknown_type"
		}
		if err != nil {
			return err
		}
		status := "processed"
		if reason != "" {
			status = "rejected"
			if suspicious[reason] {
				if err := m.ticket(ctx, tx, fx, "webhook:"+w.ID, "webhook_rejected", "", w.VendorID, AlertWebhookRejected); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE webhook_event SET status=$2, reject_reason=$3, processed_at=$4 WHERE id=$1`, w.ID, status, reason, m.now()); err != nil {
			return err
		}
		done = true
		return nil
	})
	if err != nil {
		return false, err
	}
	m.flush(ctx, fx)
	return done, nil
}

type capture struct {
	MerchantRef, ProviderOrderID, PaymentID, Currency string
	Amount                                            int64
	At                                                time.Time
}

// lookupOrder finds the order for a vendor reference and checks everything that must match.
func (m *Module) lookupOrder(ctx context.Context, tx pgx.Tx, link, merchantRef, providerOrderID, currency string, amount int64) (orderRow, string, error) {
	o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE merchant_ref=$1 FOR UPDATE`, merchantRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, "order_unknown", nil
	}
	if err != nil {
		return o, "", err
	}
	switch {
	case o.Link != link:
		return o, "order_unknown", nil
	case amount != o.Amount || currency != o.Currency:
		return o, "amount_mismatch", nil
	case providerOrderID != "" && o.ProviderID != nil && *o.ProviderID != providerOrderID:
		return o, "order_mismatch", nil
	}
	return o, "", nil
}

// applyCapture applies a verified capture to its order. It is idempotent (the same payment
// twice changes nothing). A capture that arrives after the hold expired, or for an
// appointment booking reports as cancelled or expired, is recorded in the ledger (the money
// WAS taken) and refunded in full automatically (E-PAY-01, E-PAY-02). A second payment on an
// already paid order is a duplicate and its refund follows rule RF02 (E-PAY-03).
func (m *Module) applyCapture(ctx context.Context, tx pgx.Tx, fx *effects, link string, c capture) (string, error) {
	if !reVendorID.MatchString(c.PaymentID) {
		return "bad_payment_id", nil
	}
	o, reason, err := m.lookupOrder(ctx, tx, link, c.MerchantRef, c.ProviderOrderID, c.Currency, c.Amount)
	if err != nil || reason != "" {
		return reason, err
	}
	if o.ProviderID == nil && c.ProviderOrderID != "" {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_order WHERE provider_order_id=$1)`, c.ProviderOrderID).Scan(&taken); err != nil {
			return "", err
		}
		if taken {
			return "order_mismatch", nil
		}
	}
	at := c.At
	if at.IsZero() {
		at = m.now()
	}
	tag, err := tx.Exec(ctx, `INSERT INTO payment_attempt (order_ref, payment_id, status, amount_minor, currency, event_at, created_at) VALUES ($1,$2,'captured',$3,$4,$5,$6)
		ON CONFLICT (order_ref, payment_id) DO UPDATE SET status='captured', event_at=EXCLUDED.event_at WHERE payment_attempt.status <> 'captured'`,
		o.Ref, c.PaymentID, c.Amount, c.Currency, at, m.now())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", nil // this payment was already applied
	}
	if o.PaymentID == nil { // the first capture of this order
		if _, err := tx.Exec(ctx, `UPDATE payment_order SET status='captured', payment_id=$2, captured_at=$3, provider_order_id=COALESCE(provider_order_id, NULLIF($4,'')), updated_at=$5 WHERE order_ref=$1`,
			o.Ref, c.PaymentID, at, c.ProviderOrderID, m.now()); err != nil {
			return "", err
		}
		if err := m.postCapture(ctx, tx, o, c.PaymentID, at); err != nil {
			return "", err
		}
		if at.After(o.HoldExpires) || o.ApptState != "open" { // late debit or the booking is gone
			return "", m.autoRefund(ctx, tx, fx, o, c.PaymentID, "payment_debited_booking_failed", false)
		}
		if err := m.issueDocument(ctx, tx, fx, o, c.PaymentID, at); err != nil { // S-134
			return "", err
		}
		return "", outboxEmit(ctx, tx, rp.EventPaymentCaptured, o.Appt, map[string]string{"orderRef": o.Ref, "appointmentRef": o.Appt})
	}
	// A different payment on an order that is already paid: money was taken twice.
	if err := m.postCapture(ctx, tx, o, c.PaymentID, at); err != nil {
		return "", err
	}
	return "", m.autoRefund(ctx, tx, fx, o, c.PaymentID, "duplicate_payment", true)
}

// autoRefund asks the policy engine for a system-triggered refund (RF01, RF02) and records it.
func (m *Module) autoRefund(ctx context.Context, tx pgx.Tx, fx *effects, o orderRow, paymentID, trigger string, duplicate bool) error {
	pol := m.cfg.Policy
	in := policyInput(trigger, o.Amount)
	d, err := pol.Decide(in)
	if err != nil {
		return err
	}
	return m.insertRefundTx(ctx, tx, fx, o, paymentID, d, pol.Version, refundOpKey(o.Appt, d.RuleID, paymentID, duplicate))
}

// applyFailed records a failed payment attempt. It never fails the ORDER (the patient may
// try again on the same hosted checkout) and never overrides a captured payment.
func (m *Module) applyFailed(ctx context.Context, tx pgx.Tx, w webhookRow) (string, error) {
	if !reVendorID.MatchString(w.PaymentID) {
		return "bad_payment_id", nil
	}
	o, reason, err := m.lookupOrder(ctx, tx, w.Link, w.MerchantOrderRef, w.ProviderOrderID, w.Currency, w.Amount)
	if err != nil || reason != "" {
		return reason, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO payment_attempt (order_ref, payment_id, status, amount_minor, currency, event_at, created_at) VALUES ($1,$2,'failed',$3,$4,$5,$6)
		ON CONFLICT (order_ref, payment_id) DO NOTHING`, o.Ref, w.PaymentID, w.Amount, w.Currency, w.At, m.now())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 || o.Status == string(rp.OrderCaptured) {
		return "", nil // duplicate, or stale: a capture already won
	}
	return "", outboxEmit(ctx, tx, rp.EventPaymentFailed, o.Appt, map[string]string{"orderRef": o.Ref, "appointmentRef": o.Appt})
}

// applyRefundEvent applies a verified refund.succeeded or refund.failed.
func (m *Module) applyRefundEvent(ctx context.Context, tx pgx.Tx, fx *effects, w webhookRow) (string, error) {
	r, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundCols+` FROM refund WHERE merchant_ref=$1 FOR UPDATE`, w.MerchantRefundRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return "refund_unknown", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case r.Link != w.Link:
		return "refund_unknown", nil
	case w.Amount != r.Amount || w.Currency != r.Currency:
		return "amount_mismatch", nil
	case (w.PaymentID != "" && w.PaymentID != r.PaymentID) || (w.ProviderRefundID != "" && r.ProviderID != nil && *r.ProviderID != w.ProviderRefundID):
		return "refund_mismatch", nil
	}
	if adp.EventType(w.Type) == adp.EventRefundSucceeded {
		return "", m.succeedRefund(ctx, tx, r, w.ProviderRefundID, w.At)
	}
	if r.Status == "succeeded" || r.Status == "failed" {
		return "", nil // stale or duplicate: an earlier answer already decided it
	}
	return "", m.failRefund(ctx, tx, fx, r, "VENDOR_FAILED")
}
