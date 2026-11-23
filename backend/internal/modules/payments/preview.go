package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
)

// PreviewRefund tells the patient, before they confirm, what a cancellation would refund
// (S-144: "see the refund outcome before confirming"). It applies the same engine and the
// policy version stored with the booking as the real refund does, so the preview equals
// what is applied (K-PAY-05). No order (pay at clinic) means nothing to refund: 0, "".
// mode is the booking mode: video or audio count as online.
func (m *Module) PreviewRefund(ctx context.Context, appointmentRef, policyVersion, byWhom, mode string, minutesBeforeSlot int) (int64, string, error) {
	pol, err := m.policyFor(policyVersion)
	if err != nil {
		return 0, "", err
	}
	o, err := scanOrder(m.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM payment_order WHERE appointment_ref=$1`, appointmentRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var trig refundpolicy.Trigger
	switch byWhom {
	case "patient":
		trig = refundpolicy.PatientCancelled
	case "doctor", "clinic", "system":
		trig = refundpolicy.ClinicOrDoctorCancelled
	default:
		return 0, "", fmt.Errorf("%w: byWhom", rp.ErrInvalid)
	}
	var pm refundpolicy.Mode
	switch mode {
	case "video", "audio":
		pm = refundpolicy.Online
	case "in_person":
		pm = refundpolicy.InPerson
	default:
		return 0, "", fmt.Errorf("%w: mode", rp.ErrInvalid)
	}
	if minutesBeforeSlot < 0 {
		minutesBeforeSlot = 0
	}
	in := refundpolicy.Input{Trigger: trig, Mode: pm, PaidMinor: o.Amount, MinutesPrior: minutesBeforeSlot}
	if m.cfg.OverrideFor != nil {
		in.Override = m.cfg.OverrideFor(ctx, o.Clinic)
	}
	d, err := pol.Decide(in)
	if err != nil {
		return 0, d.RuleID, err // blocked rules (RF08, RF09) and no-match are reported, never guessed
	}
	return d.RefundMinor, d.RuleID, nil
}
