package payments

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
)

// PollStats counts what one PollPending round did.
type PollStats struct{ Dispatched, Resolved, Polled int }

const pollBatch = 100

// PollPending is the worker for everything that is waiting on the payment company (K-PAY-02
// polling fallback, TC-28). `now` selects what is due; changes are stamped with the module
// clock, which equals real time in production. One round:
//  1. sends due `requested` orders and refunds (recovers from a crash after the commit);
//  2. resolves `unknown` records with findOrder / findRefund, never a blind retry;
//  3. polls open orders for a capture the webhook did not deliver, and expires unpaid ones
//     after the hold (a later debit is still caught for LatePollWindow and refunded);
//  4. retries shortfall refunds that are due, and polls accepted refunds for their outcome.
func (m *Module) PollPending(ctx context.Context, now time.Time) (PollStats, error) {
	var st PollStats
	now = now.UTC()

	oids, err := m.strings(ctx, `SELECT order_ref::text FROM payment_order WHERE status='requested' AND next_attempt_at<=$1 ORDER BY next_attempt_at LIMIT $2`, now, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range oids {
		if err := m.dispatchOrder(ctx, id); err != nil {
			return st, err
		}
		st.Dispatched++
	}
	rids, err := m.strings(ctx, `SELECT refund_ref::text FROM refund WHERE status IN ('requested','escalated') AND provider_refund_id IS NULL AND next_attempt_at<=$1 ORDER BY next_attempt_at LIMIT $2`, now, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range rids {
		if err := m.dispatchRefund(ctx, id); err != nil {
			return st, err
		}
		st.Dispatched++
	}

	unk, err := m.strings(ctx, `SELECT order_ref::text FROM payment_order WHERE status IN ('unknown','processing') AND provider_order_id IS NULL AND next_attempt_at<=$1 ORDER BY next_attempt_at LIMIT $2`, now, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range unk {
		o, err := scanOrder(m.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE order_ref=$1`, id))
		if err != nil {
			return st, err
		}
		if err := m.resolveOrder(ctx, o); err != nil {
			return st, err
		}
		st.Resolved++
	}
	unkR, err := m.strings(ctx, `SELECT refund_ref::text FROM refund WHERE status IN ('unknown','processing') AND provider_refund_id IS NULL AND next_attempt_at<=$1 ORDER BY next_attempt_at LIMIT $2`, now, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range unkR {
		r, err := scanRefund(m.pool.QueryRow(ctx, `SELECT `+refundCols+` FROM refund WHERE refund_ref=$1`, id))
		if err != nil {
			return st, err
		}
		if err := m.resolveRefund(ctx, r); err != nil {
			return st, err
		}
		st.Resolved++
	}

	open, err := m.strings(ctx, `SELECT order_ref::text FROM payment_order WHERE status IN ('created','expired') AND provider_order_id IS NOT NULL AND payment_id IS NULL
		AND created_at <= $1 AND $2 <= hold_expires_at + $3::interval ORDER BY created_at LIMIT $4`, now.Add(-m.cfg.PollAfter), now, m.cfg.LatePollWindow, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range open {
		o, err := scanOrder(m.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE order_ref=$1`, id))
		if err != nil {
			return st, err
		}
		st.Polled++
		ps, perr := m.port.GetPaymentStatus(ctx, o.Link, str(o.ProviderID))
		if perr == nil && ps.Status == adp.OrderCaptured && ps.PaymentID != "" {
			if err := m.applyPolledCapture(ctx, o, ps); err != nil {
				return st, err
			}
			continue
		}
		if perr == nil && o.Status == "created" && !now.Before(o.HoldExpires) {
			if _, err := m.pool.Exec(ctx, `UPDATE payment_order SET status='expired', updated_at=$2 WHERE order_ref=$1 AND status='created' AND payment_id IS NULL`, o.Ref, m.now()); err != nil {
				return st, err
			}
		}
	}

	proc, err := m.strings(ctx, `SELECT refund_ref::text FROM refund WHERE status='processing' AND provider_refund_id IS NOT NULL ORDER BY updated_at LIMIT $1`, pollBatch)
	if err != nil {
		return st, err
	}
	for _, id := range proc {
		r, err := scanRefund(m.pool.QueryRow(ctx, `SELECT `+refundCols+` FROM refund WHERE refund_ref=$1`, id))
		if err != nil {
			return st, err
		}
		st.Polled++
		if err := m.pollRefund(ctx, r); err != nil {
			return st, err
		}
	}
	return st, nil
}

func (m *Module) applyPolledCapture(ctx context.Context, o orderRow, ps adp.PaymentStatus) error {
	fx := &effects{}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx = &effects{}
		reason, err := m.applyCapture(ctx, tx, fx, o.Link, capture{MerchantRef: o.Merchant, ProviderOrderID: str(o.ProviderID), PaymentID: ps.PaymentID,
			Amount: ps.Amount.Minor, Currency: ps.Amount.Currency, At: ps.CapturedAt})
		if err != nil || reason == "" {
			return err
		}
		return m.ticket(ctx, tx, fx, "poll:"+o.Ref+":"+reason, "webhook_rejected", o.Clinic, o.Ref, AlertWebhookRejected)
	})
	if err == nil {
		m.flush(ctx, fx)
	}
	return err
}

func (m *Module) strings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := m.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
