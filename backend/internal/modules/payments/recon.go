package payments

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// ReconResult is the outcome of one reconciliation.
type ReconResult struct {
	Lines      int
	Mismatches int
}

type reconKey struct {
	kind adp.SettlementKind
	id   string // payment ID for captures, merchant refund ref for refunds
}

type reconVal struct {
	ref      string // merchant reference
	amount   int64
	currency string
}

// Reconcile compares the vendor's settlement report for one clinic account and one UTC day
// with our records (S-146, C17). Every difference (missing either side, different amount,
// currency or reference) opens a once-only ticket and raises an alert. Re-running it for
// the same day never duplicates tickets.
func (m *Module) Reconcile(ctx context.Context, clinicLinkRef string, day time.Time) (ReconResult, error) {
	var res ReconResult
	if !ids.Valid(clinicLinkRef) {
		return res, fmt.Errorf("payments: reconcile: bad link")
	}
	var clinic string
	if err := m.pool.QueryRow(ctx, `SELECT clinic_ref::text FROM clinic_merchant_link WHERE link_ref=$1`, clinicLinkRef).Scan(&clinic); err != nil {
		return res, err
	}
	d := day.UTC().Truncate(24 * time.Hour)
	end := d.Add(24 * time.Hour)
	lines, err := m.port.FetchSettlementReport(ctx, clinicLinkRef, d)
	if err != nil {
		return res, err
	}
	vendor := map[reconKey]reconVal{}
	for _, l := range lines {
		k := reconKey{l.Kind, l.PaymentID}
		if l.Kind == adp.SettlementRefund {
			k.id = l.MerchantRef
		}
		vendor[k] = reconVal{l.MerchantRef, l.Amount.Minor, l.Amount.Currency}
	}
	ours := map[reconKey]reconVal{}
	rows, err := m.pool.Query(ctx, `SELECT a.payment_id, o.merchant_ref, a.amount_minor, a.currency FROM payment_attempt a JOIN payment_order o USING (order_ref)
		WHERE o.link_ref=$1 AND a.status='captured' AND a.event_at >= $2 AND a.event_at < $3`, clinicLinkRef, d, end)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var pid string
		var v reconVal
		if err := rows.Scan(&pid, &v.ref, &v.amount, &v.currency); err != nil {
			rows.Close()
			return res, err
		}
		ours[reconKey{adp.SettlementCapture, pid}] = v
	}
	rows.Close()
	rrows, err := m.pool.Query(ctx, `SELECT merchant_ref, amount_minor, currency FROM refund WHERE link_ref=$1 AND status='succeeded' AND succeeded_at >= $2 AND succeeded_at < $3`, clinicLinkRef, d, end)
	if err != nil {
		return res, err
	}
	for rrows.Next() {
		var v reconVal
		if err := rrows.Scan(&v.ref, &v.amount, &v.currency); err != nil {
			rrows.Close()
			return res, err
		}
		ours[reconKey{adp.SettlementRefund, v.ref}] = v
	}
	rrows.Close()

	var bad []string
	for k, v := range vendor {
		o, ok := ours[k]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s:%s:missing_local", k.kind, k.id))
		case o != v:
			bad = append(bad, fmt.Sprintf("%s:%s:differs", k.kind, k.id))
		}
	}
	for k := range ours {
		if _, ok := vendor[k]; !ok {
			bad = append(bad, fmt.Sprintf("%s:%s:missing_vendor", k.kind, k.id))
		}
	}
	res.Lines, res.Mismatches = len(lines), len(bad)
	date := d.Format("2006-01-02")
	fx := &effects{}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		fx = &effects{}
		for _, b := range bad {
			if err := m.ticket(ctx, tx, fx, "recon:"+clinicLinkRef+":"+date+":"+b, "reconciliation_mismatch", clinic, "recon:"+date, AlertReconMismatch); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO reconciliation_run (link_ref, run_date, line_count, mismatch_count, ran_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (link_ref, run_date) DO UPDATE SET line_count=EXCLUDED.line_count, mismatch_count=EXCLUDED.mismatch_count, ran_at=EXCLUDED.ran_at`,
			clinicLinkRef, d, res.Lines, res.Mismatches, m.now())
		return err
	})
	if err == nil {
		m.flush(ctx, fx)
	}
	return res, err
}
