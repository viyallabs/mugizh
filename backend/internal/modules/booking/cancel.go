package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Refund policy rule IDs booking names in events (versioned policy: refund-policy.yaml).
const (
	RuleDoctorNotJoined         = "RF03"
	RuleClinicOrDoctorCancelled = "RF04"
	RuleTechnicalFailure        = "RF05"
	RuleConvertedToInPerson     = "RF06"
)

// CancelPreview is the refund outcome a patient is shown BEFORE confirming a cancellation.
type CancelPreview struct {
	AppointmentID     string
	ByWhom            string // patient, or clinic when the clinic did not reconfirm (E-BK-07)
	Paid              bool
	RefundMinor       int64
	Currency          string
	Rule              string
	MinutesBeforeSlot int
}

func (m *Module) previewFor(ctx context.Context, a Appointment, now time.Time) (CancelPreview, error) {
	p := CancelPreview{AppointmentID: a.ID, ByWhom: "patient", Currency: a.Currency, MinutesBeforeSlot: minutesBefore(now, a.StartsAt)}
	if a.ReconfirmState == "overdue" {
		p.ByWhom = "clinic" // the clinic did not reconfirm the doctor: full refund (E-BK-07)
	}
	p.Paid = a.State == StateConfirmed && a.SettlementMode == string(pp.ClinicCollected)
	if !p.Paid {
		return p, nil
	}
	amt, rule, err := m.d.Refunds.PreviewRefund(ctx, a.ID, a.PolicyVersion, p.ByWhom, a.Mode, p.MinutesBeforeSlot)
	if err != nil {
		return p, ProblemPreviewDown // fail closed: never cancel without showing the outcome
	}
	p.RefundMinor, p.Rule = amt, rule
	return p, nil
}

// PreviewCancel tells the patient what a cancellation would refund, before they confirm.
func (m *Module) PreviewCancel(ctx context.Context, patientRef, apptID string) (CancelPreview, error) {
	a, err := m.Mine(ctx, patientRef, apptID)
	if err != nil {
		return CancelPreview{}, err
	}
	if !m.cancellable(a, m.now()) {
		return CancelPreview{}, ProblemCannotCancel
	}
	return m.previewFor(ctx, a, m.now())
}

func (m *Module) cancellable(a Appointment, now time.Time) bool {
	switch a.State {
	case StateHeld, StatePendingPayment:
		return true
	case StateConfirmed:
		return a.StartsAt.After(now)
	}
	return false
}

// Cancel cancels the patient's own appointment. For a paid appointment the request must
// carry the refund amount the patient was shown (ackRefundMinor); if the outcome changed
// since, nothing is cancelled and ProblemRefundChanged asks them to review it (S-144).
func (m *Module) Cancel(ctx context.Context, patientRef, apptID string, ackRefundMinor int64, requestID string) (CancelPreview, Appointment, error) {
	a, err := m.Mine(ctx, patientRef, apptID)
	if err != nil {
		return CancelPreview{}, Appointment{}, err
	}
	now := m.now()
	if !m.cancellable(a, now) {
		return CancelPreview{}, Appointment{}, ProblemCannotCancel
	}
	prev, err := m.previewFor(ctx, a, now)
	if err != nil {
		return prev, Appointment{}, err
	}
	if prev.Paid && ackRefundMinor != prev.RefundMinor {
		return prev, Appointment{}, ProblemRefundChanged
	}
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, apptID, true)
		if err != nil {
			return err
		}
		if cur.PatientRef != patientRef || !m.cancellable(cur, now) {
			return ProblemCannotCancel
		}
		if cur.State == StateConfirmed {
			rule := ""
			if prev.ByWhom != "patient" {
				rule = prev.Rule
				if rule == "" {
					rule = RuleClinicOrDoctorCancelled
				}
			}
			return m.cancelConfirmedTx(ctx, tx, cur, prev.ByWhom, rule, patientRef, "patient", requestID, now)
		}
		return m.cancelUnpaidTx(ctx, tx, cur, "patient", patientRef, "patient", requestID)
	})
	if err != nil {
		return prev, Appointment{}, err
	}
	out, err := m.Mine(ctx, patientRef, apptID)
	return prev, out, err
}

// cancelUnpaidTx cancels a held or pending_payment appointment: no money has been settled.
// If an order may exist (pending_payment), appointment.expired is emitted so a capture that
// arrives later is refunded in full.
func (m *Module) cancelUnpaidTx(ctx context.Context, tx pgx.Tx, a Appointment, byWhom, actorRef, role, requestID string) error {
	now := m.now()
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='cancelled', cancelled_by=$2, cancelled_at=$3, updated_at=$3 WHERE id=$1`, a.ID, byWhom, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hold WHERE appointment_id=$1`, a.ID); err != nil {
		return err
	}
	if err := m.releaseSlot(ctx, tx, a); err != nil {
		return err
	}
	if err := m.audit(ctx, tx, actorRef, role, "booking.cancel", a.PatientRef, a.ID, requestID); err != nil {
		return err
	}
	if a.State == StatePendingPayment {
		p := m.expiredPayload(a)
		p["byWhom"] = byWhom
		return m.emit(ctx, tx, bp.EventExpired, a.ID, p)
	}
	return nil
}

// CancelByStaff cancels on behalf of the doctor or the clinic (RF04: full refund). The
// doctor must own the practice; clinic staff must belong to its clinic.
func (m *Module) CancelByStaff(ctx context.Context, a Actor, apptID string) error {
	byWhom := "clinic"
	if a.Role == ip.RoleDoctor {
		byWhom = "doctor"
	}
	now := m.now()
	return m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := m.staffAppt(ctx, tx, a, apptID, true)
		if err != nil {
			return err
		}
		if !m.cancellable(cur, now) {
			return ProblemCannotCancel
		}
		if cur.State == StateConfirmed {
			return m.cancelConfirmedTx(ctx, tx, cur, byWhom, RuleClinicOrDoctorCancelled, a.UserID, a.Role, a.RequestID, now)
		}
		return m.cancelUnpaidTx(ctx, tx, cur, byWhom, a.UserID, a.Role, a.RequestID)
	})
}

// staffAppt loads an appointment for a doctor or clinic staff member and checks ownership
// (K-SEC-04: someone else's is 404). It does not read patient data; reads of booking
// fields use authorizeBooking.
func (m *Module) staffAppt(ctx context.Context, q querier, a Actor, apptID string, lock bool) (Appointment, error) {
	switch a.Role {
	case ip.RoleDoctor, ip.RoleClinicAdmin, ip.RoleFrontDesk:
	default:
		return Appointment{}, problem.Forbidden
	}
	cur, err := loadAppt(ctx, q, apptID, lock)
	if err != nil {
		return Appointment{}, notFoundErr(err)
	}
	if !ownsPractice(a, practiceRefs{ClinicRef: cur.ClinicRef, ProfessionalRef: cur.ProfessionalRef}) {
		return Appointment{}, problem.NotFound
	}
	return cur, nil
}

// takeSlot takes an open slot for an appointment with one UPDATE ... WHERE state = 'open'.
// It returns errSlotTaken when the slot exists but is not open, problem.NotFound when it
// does not exist, and ProblemModeNotOffered when the open slot has another mode.
func takeSlot(ctx context.Context, tx pgx.Tx, slotID, practiceID, apptID, mode string, now time.Time) (start, end time.Time, err error) {
	err = tx.QueryRow(ctx, `UPDATE slot SET state='taken', appointment_id=$3
		WHERE id=$1 AND practice_id=$2 AND state='open' AND start_at > $4 AND mode=$5 RETURNING start_at, end_at`,
		slotID, practiceID, apptID, now, mode).Scan(&start, &end)
	if !errors.Is(err, pgx.ErrNoRows) {
		return start, end, err
	}
	var st, md string
	var sat time.Time
	derr := tx.QueryRow(ctx, `SELECT state, mode, start_at FROM slot WHERE id=$1 AND practice_id=$2`, slotID, practiceID).Scan(&st, &md, &sat)
	switch {
	case errors.Is(derr, pgx.ErrNoRows):
		return start, end, problem.NotFound
	case derr != nil:
		return start, end, derr
	case st == "open" && sat.After(now) && md != mode:
		return start, end, ProblemModeNotOffered
	}
	return start, end, errSlotTaken
}

// Reschedule moves the patient's confirmed appointment to another open slot of the same
// practice and mode. The payment stays with the appointment, so no refund and no new
// payment is involved; the stored refund policy version still applies. The patient has
// FreeReschedules of these and needs RescheduleMinMinutes notice, unless the clinic caused
// the change (availability changed, or the clinic did not reconfirm), when neither applies.
func (m *Module) Reschedule(ctx context.Context, patientRef, apptID, newSlotID, requestID string) (Appointment, error) {
	if !ids.Valid(newSlotID) {
		return Appointment{}, problem.BadRequest
	}
	a, err := m.Mine(ctx, patientRef, apptID)
	if err != nil {
		return Appointment{}, err
	}
	now := m.now()
	if a.State != StateConfirmed || !a.StartsAt.After(now) {
		return Appointment{}, ProblemCannotReschedule
	}
	exempt := a.NeedsReschedule || a.ReconfirmState == "overdue"
	if !exempt {
		if minutesBefore(now, a.StartsAt) < m.cfg.RescheduleMinMinutes {
			return Appointment{}, ProblemCannotReschedule
		}
		if a.RescheduleCount >= m.cfg.FreeReschedules {
			return Appointment{}, ProblemRescheduleLimit
		}
	}
	pr, err := m.d.Directory.GetPractice(ctx, a.PracticeID) // K-CS-01
	if err != nil {
		if errors.Is(err, dp.ErrNotFound) {
			return Appointment{}, ProblemPracticeGone
		}
		return Appointment{}, err
	}
	if ok, verr := m.stillVerified(ctx, pr.ProfessionalRef); verr != nil || !ok {
		return Appointment{}, ProblemPracticeGone // fail closed (review H3)
	}
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, apptID, true)
		if err != nil {
			return err
		}
		if cur.State != StateConfirmed || cur.PatientRef != patientRef || cur.SlotID != a.SlotID {
			return ProblemCannotReschedule
		}
		start, end, err := takeSlot(ctx, tx, newSlotID, cur.PracticeID, cur.ID, cur.Mode, now)
		if err != nil {
			return err
		}
		if err := m.releaseSlot(ctx, tx, cur); err != nil {
			return err
		}
		inc := 1
		if exempt {
			inc = 0
		}
		if _, err := tx.Exec(ctx, `UPDATE appointment SET slot_id=$2, start_at=$3, end_at=$4, reschedule_count=reschedule_count+$5, needs_reschedule=false, updated_at=$6,
			reminder_day_at=NULL, reminder_soon_at=NULL, consult_notice_at=NULL WHERE id=$1`,
			cur.ID, newSlotID, start, end, inc, now); err != nil {
			return err
		}
		cur.SlotID, cur.StartsAt, cur.EndsAt = newSlotID, start.UTC(), end.UTC()
		if _, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='void' WHERE appointment_id=$1`, cur.ID); err != nil {
			return err
		}
		if err := m.createReconfirmation(ctx, tx, cur, now); err != nil {
			return err
		}
		if err := m.audit(ctx, tx, patientRef, "patient", "booking.reschedule", patientRef, cur.ID, requestID); err != nil {
			return err
		}
		return m.emit(ctx, tx, bp.EventRescheduled, cur.ID, m.confirmedPayload(cur))
	})
	var st *SlotTakenError
	if errors.Is(err, errSlotTaken) {
		st = &SlotTakenError{Next: m.nextOpen(ctx, a.PracticeID, now)}
		return Appointment{}, st
	}
	if err != nil {
		return Appointment{}, err
	}
	return m.Mine(ctx, patientRef, apptID)
}
