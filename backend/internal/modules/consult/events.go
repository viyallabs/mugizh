package consult

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func auditRecord(ctx context.Context, tx pgx.Tx, a Actor, action string, s Session) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: a.UserID, ActorRole: a.Role, Action: action, SubjectRef: s.PatientRef,
		ResourceType: "consult_session", ResourceRef: s.ID, Result: ap.Allowed, RequestID: a.RequestID})
}

// auditDetail is auditRecord with a purpose and a reason code (both codes, never free text).
func auditDetail(ctx context.Context, tx pgx.Tx, a Actor, action string, s Session, purpose, reason string) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: a.UserID, ActorRole: a.Role, Action: action, SubjectRef: s.PatientRef,
		ResourceType: "consult_session", ResourceRef: s.ID, Purpose: purpose, ReasonCode: reason, Result: ap.Allowed, RequestID: a.RequestID})
}

func systemActor() Actor { return Actor{UserID: SystemActor, Role: "system"} }

// OnAppointmentConfirmed creates the consult session of a confirmed online appointment.
// In-person appointments get none. Subscribe as cp.SubscriberAppointmentConfirmed to
// "appointment.confirmed". It is idempotent: a second event for the same appointment only
// refreshes the slot times of a session that has not started.
func (m *Module) OnAppointmentConfirmed(ctx context.Context, e outbox.Event) error {
	return m.upsertFromAppointment(ctx, cp.SubscriberAppointmentConfirmed, e)
}

// OnAppointmentRescheduled moves the slot of a session that has not started. Subscribe as
// cp.SubscriberAppointmentRescheduled to "appointment.rescheduled" (same payload as confirmed).
func (m *Module) OnAppointmentRescheduled(ctx context.Context, e outbox.Event) error {
	return m.upsertFromAppointment(ctx, cp.SubscriberAppointmentRescheduled, e)
}

func (m *Module) upsertFromAppointment(ctx context.Context, subscriber string, e outbox.Event) error {
	p := e.Payload
	mode := p["mode"]
	if mode != cp.ModeVideo && mode != cp.ModeAudio {
		return nil // in-person: no session
	}
	for _, k := range []string{"appointmentRef", "patientRef", "doctorRef", "clinicRef"} {
		if !ids.Valid(p[k]) {
			return fmt.Errorf("consult: appointment event without a valid %s", k)
		}
	}
	start, err1 := time.Parse(time.RFC3339, p["slotStart"])
	end, err2 := time.Parse(time.RFC3339, p["slotEnd"])
	if err1 != nil || err2 != nil || !end.After(start) {
		return errors.New("consult: appointment event without valid slot times")
	}
	return outbox.Once(ctx, m.pool, subscriber, e, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO session (id, appointment_ref, patient_ref, doctor_ref, clinic_ref, booked_mode, current_mode, slot_start, slot_end, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$9)
			ON CONFLICT (appointment_ref) DO UPDATE SET slot_start=EXCLUDED.slot_start, slot_end=EXCLUDED.slot_end, updated_at=EXCLUDED.updated_at
			WHERE session.state='scheduled' AND session.provider_room_ref=''`,
			ids.New(), p["appointmentRef"], p["patientRef"], p["doctorRef"], p["clinicRef"], mode, start.UTC(), end.UTC(), m.now())
		return err
	})
}

// OnAppointmentCancelled closes the session of a cancelled appointment that has not ended and
// closes its room. It emits nothing: booking already knows. Subscribe as
// cp.SubscriberAppointmentCancelled to "appointment.cancelled".
func (m *Module) OnAppointmentCancelled(ctx context.Context, e outbox.Event) error {
	ref := e.Payload["appointmentRef"]
	if !ids.Valid(ref) {
		return errors.New("consult: appointment.cancelled without a valid appointmentRef")
	}
	var room string
	err := outbox.Once(ctx, m.pool, cp.SubscriberAppointmentCancelled, e, func(tx pgx.Tx) error {
		room = ""
		s, err := loadSession(ctx, tx, ref, true, true)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && terminal(s.State)) {
			return nil
		}
		if err != nil {
			return err
		}
		room = s.RoomRef
		now := m.now()
		if _, err := tx.Exec(ctx, `UPDATE session SET state='cancelled', ended_at=$2, updated_at=$2 WHERE id=$1`, s.ID, now); err != nil {
			return err
		}
		return auditRecord(ctx, tx, systemActor(), "consult.cancelled", s)
	})
	if err == nil && room != "" {
		m.endRoom(ctx, room)
	}
	return err
}
