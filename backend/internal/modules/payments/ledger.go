package payments

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// The ledger is append-only and double-entry (K-PAY-04, S-132). Amounts are signed integer
// minor units: debits positive, credits negative, every transaction sums to zero per
// currency (enforced by a deferred constraint trigger). Mode B money is in the clinic's
// own account, not ours, so every entry is a MEMO entry that records what happened.

// ClinicAccount is the memo account for the money the clinic's merchant account holds.
func ClinicAccount(clinicRef string) string { return "memo:clinic_account:" + clinicRef }

// PatientPaymentsAccount is the memo counter-account for patient payments to a clinic.
func PatientPaymentsAccount(clinicRef string) string { return "memo:patient_payments:" + clinicRef }

func (m *Module) post(ctx context.Context, tx pgx.Tx, key, kind, orderRef, refundRef, clinicRef, currency string, amount int64, debitClinic bool, at time.Time) error {
	txn := ids.New()
	tag, err := tx.Exec(ctx, `INSERT INTO ledger_txn (txn_id, txn_key, kind, order_ref, refund_ref, occurred_at) VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6)
		ON CONFLICT (txn_key) DO NOTHING`, txn, key, kind, orderRef, refundRef, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already posted: posting is idempotent
	}
	sign := int64(1)
	if !debitClinic {
		sign = -1
	}
	for _, e := range []struct {
		account string
		amount  int64
	}{{ClinicAccount(clinicRef), sign * amount}, {PatientPaymentsAccount(clinicRef), -sign * amount}} {
		if _, err := tx.Exec(ctx, `INSERT INTO ledger_entry (entry_id, txn_id, account, amount_minor, currency, memo, occurred_at) VALUES ($1,$2,$3,$4,$5,true,$6)`,
			ids.New(), txn, e.account, e.amount, currency, at); err != nil {
			return err
		}
	}
	return nil
}

// postCapture records money received by the clinic's account.
func (m *Module) postCapture(ctx context.Context, tx pgx.Tx, o orderRow, paymentID string, at time.Time) error {
	return m.post(ctx, tx, "capture:"+o.Ref+":"+paymentID, "payment_captured", o.Ref, "", o.Clinic, o.Currency, o.Amount, true, at)
}

// postRefund records money returned to the patient from the clinic's account.
func (m *Module) postRefund(ctx context.Context, tx pgx.Tx, r refundRow, at time.Time) error {
	return m.post(ctx, tx, "refund:"+r.Ref, "refund_succeeded", r.Order, r.Ref, r.Clinic, r.Currency, r.Amount, false, at)
}

// Balance derives an account's balance from the entries (never stored, never edited).
func (m *Module) Balance(ctx context.Context, account, currency string) (int64, error) {
	var b int64
	err := m.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor),0)::bigint FROM ledger_entry WHERE account=$1 AND currency=$2`, account, currency).Scan(&b)
	return b, err
}
