package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Consult outcome codes carried in consult.ended (consult module, S-148). Booking does not
// import consult: the codes are the event contract.
const (
	consultCompleted         = "completed"
	consultConvertedInPerson = "converted_in_person"
	consultPatientNoShow     = "patient_no_show"
	consultDoctorNoShow      = "doctor_no_show"
	consultTechnicalFailure  = "technical_failure"
	consultMinorStopped      = "minor_stopped"
)

// OnConsultEnded applies an online consult's outcome to its appointment (consult.ended;
// D-46, review F5/F6). Subscribe it as bp.SubscriberConsultEnded. Payments then applies the
// versioned refund policy stored with the booking (K-PAY-05):
//
//   - completed: the appointment is completed;
//   - patient_no_show: a patient no-show (RF09 is blocked by FAIRNESS-01, so payments opens a
//     ticket and decides no refund on its own);
//   - doctor_no_show: a doctor no-show (RF03); "too early" is an error so the event retries;
//   - technical_failure: cancelled by the system under RF05;
//   - converted_in_person: cancelled by the system under RF06;
//   - minor_stopped: nothing here; the CH-12 support case decides (D-42).
//
// An appointment that has already moved on (cancelled, completed, no-show) is left alone.
func (m *Module) OnConsultEnded(ctx context.Context, e outbox.Event) error {
	appt, outcome := e.Payload["appointmentRef"], e.Payload["outcome"]
	if !ids.Valid(appt) {
		return fmt.Errorf("booking: consult.ended without a valid appointmentRef")
	}
	switch outcome {
	case consultCompleted, consultConvertedInPerson, consultPatientNoShow, consultTechnicalFailure, consultMinorStopped:
	case consultDoctorNoShow:
		err := m.MarkDoctorNoShow(ctx, appt)
		if errors.Is(err, ProblemBadMark) || errors.Is(err, bp.ErrNotFound) {
			m.log.Warn("booking.consult_outcome_not_applied", "event_id", e.ID, "outcome", outcome)
			return nil
		}
		return err // idempotent; bp.ErrTooEarly retries later
	default:
		return fmt.Errorf("booking: consult.ended with an unknown outcome")
	}
	return outbox.Once(ctx, m.pool, bp.SubscriberConsultEnded, e, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, appt, true)
		if errors.Is(err, pgx.ErrNoRows) {
			m.log.Warn("booking.consult_for_unknown_appointment", "event_id", e.ID)
			return nil
		}
		if err != nil {
			return err
		}
		if cur.Mode == ModeInPerson || (cur.State != StateConfirmed && cur.State != StateInProgress) {
			return nil
		}
		now := m.now()
		switch outcome {
		case consultCompleted:
			return m.completeTx(ctx, tx, cur, SystemActor, "system", "", now)
		case consultPatientNoShow:
			return m.noShowTx(ctx, tx, cur, "patient", SystemActor, "system", "", now)
		case consultTechnicalFailure:
			return m.cancelConfirmedTx(ctx, tx, cur, "system", RuleTechnicalFailure, SystemActor, "system", "", now)
		case consultConvertedInPerson:
			return m.cancelConfirmedTx(ctx, tx, cur, "system", RuleConvertedToInPerson, SystemActor, "system", "", now)
		}
		return nil // minor_stopped
	})
}
