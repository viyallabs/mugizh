package reviews

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// OnAppointmentCompleted builds the eligibility read model (rule B5) from booking's
// appointment.completed: {appointmentRef, patientRef, doctorRef, clinicRef, practiceRef}. The
// completion time is the event's time. Subscribe as rp.SubscriberAppointmentCompleted.
// Idempotent: a second event for the same appointment changes nothing.
func (m *Module) OnAppointmentCompleted(ctx context.Context, e outbox.Event) error {
	appt, patient, doctor, practice := e.Payload["appointmentRef"], e.Payload["patientRef"], e.Payload["doctorRef"], e.Payload["practiceRef"]
	if !ids.Valid(appt) || !ids.Valid(patient) || !ids.Valid(doctor) || !ids.Valid(practice) {
		return fmt.Errorf("reviews: appointment.completed needs appointmentRef, patientRef, doctorRef and practiceRef")
	}
	at := e.OccurredAt
	if at.IsZero() {
		at = m.now()
	}
	return outbox.Once(ctx, m.pool, rp.SubscriberAppointmentCompleted, e, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO visit (appointment_ref, patient_ref, doctor_ref, practice_ref, completed_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (appointment_ref) DO NOTHING`, appt, patient, doctor, practice, at.UTC())
		return err
	})
}
