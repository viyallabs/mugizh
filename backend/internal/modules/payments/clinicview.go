package payments

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// ClinicReader identifies who reads clinic payments, for the audit event.
type ClinicReader struct {
	UserRef, Role, RequestID, Client string
}

// ClinicPaymentRow is one order with its latest refund. It carries NO patient identity and no
// appointment reference: payments holds none, so a clinic admin can read it without the
// records gate; a view that adds appointment references must go through records.authorize.
type ClinicPaymentRow struct {
	OrderRef     string
	State        rp.OrderState
	AmountMinor  int64
	Currency     string
	CreatedAt    time.Time
	RefundRef    string
	RefundState  rp.RefundState
	RefundAmount int64
}

// ClinicPayments lists a clinic's orders, newest first (cursor = last order ref), and writes
// an audit event in the same transaction as the read (K-DP-02, K-SEC-01).
func (m *Module) ClinicPayments(ctx context.Context, who ClinicReader, clinicRef string, limit int, cursor string) ([]ClinicPaymentRow, string, error) {
	if !ids.Valid(clinicRef) || (cursor != "" && !ids.Valid(cursor)) {
		return nil, "", rp.ErrNotFound
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if cursor == "" {
		cursor = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}
	var out []ClinicPaymentRow
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		out = nil
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: who.UserRef, ActorRole: who.Role, Action: "payments.clinic_list", ResourceType: "clinic_payments",
			ResourceRef: clinicRef, Result: ap.Allowed, Client: who.Client, RequestID: who.RequestID}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT o.order_ref::text, o.status, o.amount_minor, o.currency, o.created_at,
			COALESCE(r.refund_ref::text,''), COALESCE(r.status,''), COALESCE(r.amount_minor,0)
			FROM payment_order o LEFT JOIN LATERAL (SELECT refund_ref, status, amount_minor FROM refund WHERE order_ref=o.order_ref ORDER BY refund_ref DESC LIMIT 1) r ON true
			WHERE o.clinic_ref=$1 AND o.order_ref < $2::uuid ORDER BY o.order_ref DESC LIMIT $3`, clinicRef, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ClinicPaymentRow
			var st, rs string
			if err := rows.Scan(&r.OrderRef, &st, &r.AmountMinor, &r.Currency, &r.CreatedAt, &r.RefundRef, &rs, &r.RefundAmount); err != nil {
				return err
			}
			r.State, r.RefundState = rp.OrderState(st), rp.RefundState(rs)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].OrderRef
	}
	return out, next, nil
}

// ClinicStatus is the pause flag and the number of refunds waiting for clinic balance.
func (m *Module) ClinicStatus(ctx context.Context, clinicRef string) (paused bool, since *time.Time, delayed int, err error) {
	if !ids.Valid(clinicRef) {
		return false, nil, 0, rp.ErrNotFound
	}
	if since, err = m.pausedSince(ctx, m.pool, clinicRef); err != nil {
		return true, nil, 0, err
	}
	err = m.pool.QueryRow(ctx, `SELECT count(*) FROM refund WHERE clinic_ref=$1 AND status='escalated'`, clinicRef).Scan(&delayed)
	return since != nil, since, delayed, err
}
