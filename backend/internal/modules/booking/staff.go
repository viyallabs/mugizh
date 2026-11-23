package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Marks a doctor or clinic staff member can make on an appointment (S-131).
const (
	MarkArrived       = "arrived"
	MarkCompleted     = "completed"
	MarkPatientNoShow = "patient_no_show"
	MarkDoctorNoShow  = "doctor_no_show"
)

// DayItem is one appointment in the day view: booking fields only (time, mode, status),
// never fees or payment data. Each row was authorised by the records gate (K-DP-02).
type DayItem struct {
	AppointmentID   string
	PatientRef      string
	StartsAt        time.Time
	EndsAt          time.Time
	Mode            string
	State           string
	NoShowBy        string
	CancelledBy     string
	NeedsReschedule bool
	ReconfirmState  string
}

// DayView lists the appointments of a practice on a local date (in the practice zone) for
// the doctor who owns it or the staff of its clinic (S-131). Held and unpaid bookings are not
// shown. A row the records gate denies is left out: front desk sees booking fields only.
func (m *Module) DayView(ctx context.Context, a Actor, practiceID, date string, limit int, cursor string) ([]DayItem, string, error) {
	r, err := m.staffPractice(ctx, a, practiceID, false)
	if err != nil {
		return nil, "", err
	}
	loc, err := loadZone(r.TimeZone)
	if err != nil {
		return nil, "", problem.NotFound
	}
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, "", problem.BadRequest
	}
	from, to := day.UTC(), day.AddDate(0, 0, 1).UTC()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := []any{practiceID, from, to, limit + 1}
	where := ``
	if cursor != "" {
		n, id, ok := parseCursor(cursor)
		if !ok {
			return nil, "", problem.BadRequest
		}
		args = append(args, time.Unix(0, n).UTC(), id)
		where = ` AND (a.start_at, a.id) > ($5, $6::uuid)`
	}
	rows, err := m.pool.Query(ctx, `SELECT `+apptCols+` FROM appointment a WHERE a.practice_id=$1 AND a.start_at >= $2 AND a.start_at < $3
		AND a.state NOT IN ('held','pending_payment','expired')`+where+` ORDER BY a.start_at, a.id LIMIT $4`, args...)
	if err != nil {
		return nil, "", err
	}
	var all []Appointment
	for rows.Next() {
		ap, err := scanAppt(rows)
		if err != nil {
			rows.Close()
			return nil, "", err
		}
		all = append(all, ap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(all) > limit {
		all = all[:limit]
		last := all[len(all)-1]
		next = fmt.Sprintf("%d:%s", last.StartsAt.UnixNano(), last.ID)
	}
	out := []DayItem{}
	for _, ap := range all {
		ok, err := m.authorizeBooking(ctx, a, ap.PatientRef, ap.ID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		out = append(out, DayItem{AppointmentID: ap.ID, PatientRef: ap.PatientRef, StartsAt: ap.StartsAt, EndsAt: ap.EndsAt, Mode: ap.Mode, State: ap.State,
			NoShowBy: ap.NoShowBy, CancelledBy: ap.CancelledBy, NeedsReschedule: ap.NeedsReschedule, ReconfirmState: ap.ReconfirmState})
	}
	return out, next, nil
}

// Mark records arrived, completed, or a no-show on either side. It is audited. A patient
// no-show can be marked only after the grace time, a doctor no-show only after the join
// timeout; the doctor must own the practice, clinic staff must belong to its clinic.
// It returns the new state.
func (m *Module) Mark(ctx context.Context, a Actor, apptID, mark string) (string, error) {
	now := m.now()
	state := ""
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := m.staffAppt(ctx, tx, a, apptID, true)
		if err != nil {
			return err
		}
		// TM-13: staff never decide an online consult's outcome. "Completed" needs both sides
		// connected and a no-show needs join evidence; both come only from consult.ended
		// (OnConsultEnded, D-46, D-50).
		if cur.Mode != ModeInPerson {
			return ProblemOnlineOutcome
		}
		switch mark {
		case MarkArrived:
			if cur.State != StateConfirmed {
				return ProblemBadMark
			}
			if _, err := tx.Exec(ctx, `UPDATE appointment SET state='in_progress', arrived_at=$2, updated_at=$2 WHERE id=$1`, cur.ID, now); err != nil {
				return err
			}
			state = StateInProgress
		case MarkCompleted:
			if cur.State != StateConfirmed && cur.State != StateInProgress {
				return ProblemBadMark
			}
			if cur.StartsAt.After(now) {
				return ProblemMarkTooEarly
			}
			if err := m.completeEffects(ctx, tx, cur, now); err != nil {
				return err
			}
			state = StateCompleted
		case MarkPatientNoShow:
			if cur.State != StateConfirmed {
				return ProblemBadMark
			}
			if now.Before(cur.StartsAt.Add(time.Duration(m.cfg.PatientGraceMinutes) * time.Minute)) {
				return ProblemMarkTooEarly
			}
			if err := m.noShowTx(ctx, tx, cur, "patient", a.UserID, a.Role, a.RequestID, now); err != nil {
				return err
			}
			state = StateNoShow
		case MarkDoctorNoShow:
			if cur.State != StateConfirmed {
				return ProblemBadMark
			}
			if now.Before(cur.StartsAt.Add(time.Duration(m.cfg.DoctorJoinTimeoutMinutes) * time.Minute)) {
				return ProblemMarkTooEarly
			}
			if err := m.noShowTx(ctx, tx, cur, "doctor", a.UserID, a.Role, a.RequestID, now); err != nil {
				return err
			}
			state = StateNoShow
		default:
			return problem.BadRequest
		}
		return m.audit(ctx, tx, a.UserID, a.Role, "booking.mark_"+mark, cur.PatientRef, cur.ID, a.RequestID)
	})
	return state, err
}

// noShowTx records a no-show and emits appointment.no_show {..., byWhom: the side that did
// not show, policyVersion, mode}. A doctor no-show of an online consult names rule RF03.
func (m *Module) noShowTx(ctx context.Context, tx pgx.Tx, a Appointment, side, actorRef, role, requestID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='no_show', no_show_by=$2, updated_at=$3 WHERE id=$1`, a.ID, side, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='void' WHERE appointment_id=$1 AND state IN ('pending','overdue')`, a.ID); err != nil {
		return err
	}
	p := map[string]string{"appointmentRef": a.ID, "patientRef": a.PatientRef, "clinicRef": a.ClinicRef, "byWhom": side,
		"policyVersion": a.PolicyVersion, "mode": refundMode(a.Mode), "consultMode": a.Mode}
	if side == "doctor" && a.Mode != ModeInPerson {
		p["rule"] = RuleDoctorNotJoined
	}
	if err := m.audit(ctx, tx, actorRef, role, "booking.no_show", a.PatientRef, a.ID, requestID); err != nil {
		return err
	}
	return m.emit(ctx, tx, bp.EventNoShow, a.ID, p)
}

// MarkDoctorNoShow implements public.Service for the consult module: the doctor did not
// join an online consult within the join timeout (E-BK-04, RF03, TC-25). It is idempotent.
func (m *Module) MarkDoctorNoShow(ctx context.Context, appointmentRef string) error {
	now := m.now()
	return m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, appointmentRef, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return bp.ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.State == StateNoShow && cur.NoShowBy == "doctor" {
			return nil
		}
		if cur.State != StateConfirmed && cur.State != StateInProgress || cur.Mode == ModeInPerson {
			return ProblemBadMark
		}
		if now.Before(cur.StartsAt.Add(time.Duration(m.cfg.DoctorJoinTimeoutMinutes) * time.Minute)) {
			return bp.ErrTooEarly
		}
		return m.noShowTx(ctx, tx, cur, "doctor", SystemActor, "system", "", now)
	})
}

// ---- reconfirmation (E-BK-07) ----

// Reconfirmation is a day-before task: the clinic confirms the doctor will be there.
type Reconfirmation struct {
	AppointmentID string
	PatientRef    string // set only for the system listing (UnconfirmedReconfirmations)
	PracticeID    string
	DoctorRef     string
	ClinicRef     string
	StartsAt      time.Time
	Mode          string
	DueAt         time.Time
	State         string // pending | overdue
}

// Reconfirm records that the clinic confirmed the doctor will be there. Clinic staff and
// the doctor of that practice only.
func (m *Module) Reconfirm(ctx context.Context, a Actor, apptID string) error {
	now := m.now()
	return m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := m.staffAppt(ctx, tx, a, apptID, true)
		if err != nil {
			return err
		}
		if cur.State != StateConfirmed {
			return ProblemBadMark
		}
		if _, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='reconfirmed', reconfirmed_by=$2, reconfirmed_at=$3 WHERE appointment_id=$1 AND state IN ('pending','overdue')`, cur.ID, a.UserID, now); err != nil {
			return err
		}
		return m.audit(ctx, tx, a.UserID, a.Role, "booking.reconfirm", cur.PatientRef, cur.ID, a.RequestID)
	})
}

const reconfCols = `a.id, a.patient_ref, a.practice_id, a.professional_ref, a.clinic_ref, a.start_at, a.mode, t.due_at, t.state`

func scanReconf(rows pgx.Rows) ([]Reconfirmation, error) {
	defer rows.Close()
	out := []Reconfirmation{}
	for rows.Next() {
		var r Reconfirmation
		if err := rows.Scan(&r.AppointmentID, &r.PatientRef, &r.PracticeID, &r.DoctorRef, &r.ClinicRef, &r.StartsAt, &r.Mode, &r.DueAt, &r.State); err != nil {
			return nil, err
		}
		r.StartsAt, r.DueAt = r.StartsAt.UTC(), r.DueAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnconfirmedReconfirmations lists every task past its due time that the clinic has not
// reconfirmed, for a notification worker (TC-26). It is a system read.
func (m *Module) UnconfirmedReconfirmations(ctx context.Context, now time.Time) ([]Reconfirmation, error) {
	rows, err := m.pool.Query(ctx, `SELECT `+reconfCols+` FROM reconfirmation_task t JOIN appointment a ON a.id=t.appointment_id
		WHERE t.state IN ('pending','overdue') AND t.due_at <= $1 AND a.state='confirmed' AND a.start_at > $1 ORDER BY a.start_at, a.id`, now)
	if err != nil {
		return nil, err
	}
	return scanReconf(rows)
}

// ListReconfirmations lists the unconfirmed tasks of the caller's clinic. It shows no patient
// identity: the clinic needs the doctor, the time and the mode.
func (m *Module) ListReconfirmations(ctx context.Context, a Actor, clinicRef string, limit int, cursor string) ([]Reconfirmation, string, error) {
	if a.Role != ip.RoleClinicAdmin && a.Role != ip.RoleFrontDesk || a.ClinicRef == "" || a.ClinicRef != clinicRef || !ids.Valid(clinicRef) {
		return nil, "", problem.NotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	now := m.now()
	args := []any{clinicRef, now, limit + 1}
	where := ``
	if cursor != "" {
		n, id, ok := parseCursor(cursor)
		if !ok {
			return nil, "", problem.BadRequest
		}
		args = append(args, time.Unix(0, n).UTC(), id)
		where = ` AND (a.start_at, a.id) > ($4, $5::uuid)`
	}
	rows, err := m.pool.Query(ctx, `SELECT `+reconfCols+` FROM reconfirmation_task t JOIN appointment a ON a.id=t.appointment_id
		WHERE t.clinic_ref=$1 AND t.state IN ('pending','overdue') AND t.due_at <= $2 AND a.state='confirmed' AND a.start_at > $2`+where+` ORDER BY a.start_at, a.id LIMIT $3`, args...)
	if err != nil {
		return nil, "", err
	}
	out, err := scanReconf(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = fmt.Sprintf("%d:%s", last.StartsAt.UnixNano(), last.AppointmentID)
	}
	for i := range out {
		out[i].PatientRef = ""
	}
	return out, next, nil
}

// SweepReconfirmations marks pending tasks that are past their due time as overdue and
// emits appointment.reconfirmation_overdue once per task, so the patient is warned and
// offered a reschedule or a full refund (E-BK-07, TC-26). Run it every few minutes.
func (m *Module) SweepReconfirmations(ctx context.Context, now time.Time) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT t.appointment_id FROM reconfirmation_task t JOIN appointment a ON a.id=t.appointment_id
		WHERE t.state='pending' AND t.due_at <= $1 AND a.state='confirmed' AND a.start_at > $1 ORDER BY t.due_at LIMIT 500`, now)
	if err != nil {
		return 0, err
	}
	var refs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		refs = append(refs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range refs {
		var flagged bool
		err := m.inTx(ctx, func(tx pgx.Tx) error {
			cur, err := loadAppt(ctx, tx, id, true)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='overdue' WHERE appointment_id=$1 AND state='pending'`, id)
			if err != nil {
				return err
			}
			flagged = false
			if tag.RowsAffected() == 0 || cur.State != StateConfirmed {
				return nil
			}
			flagged = true
			return m.emit(ctx, tx, bp.EventReconfirmationLapsed, id, map[string]string{"appointmentRef": id, "patientRef": cur.PatientRef,
				"doctorRef": cur.ProfessionalRef, "clinicRef": cur.ClinicRef, "slotStart": utc(cur.StartsAt)})
		})
		if err != nil {
			m.log.Error("booking.reconfirmation_sweep_failed", logging.Err(err))
			continue
		}
		if flagged {
			n++
		}
	}
	return n, nil
}

// completeEffects marks an appointment completed, voids its reconfirmation task and emits
// appointment.completed. The caller audits.
func (m *Module) completeEffects(ctx context.Context, tx pgx.Tx, a Appointment, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE appointment SET state='completed', completed_at=$2, updated_at=$2 WHERE id=$1`, a.ID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reconfirmation_task SET state='void' WHERE appointment_id=$1 AND state IN ('pending','overdue')`, a.ID); err != nil {
		return err
	}
	return m.emit(ctx, tx, bp.EventCompleted, a.ID, map[string]string{"appointmentRef": a.ID, "patientRef": a.PatientRef,
		"doctorRef": a.ProfessionalRef, "clinicRef": a.ClinicRef, "practiceRef": a.PracticeID})
}

// completeTx completes an appointment for a system actor (consult outcome) and audits it.
func (m *Module) completeTx(ctx context.Context, tx pgx.Tx, a Appointment, actorRef, role, requestID string, now time.Time) error {
	if err := m.completeEffects(ctx, tx, a, now); err != nil {
		return err
	}
	return m.audit(ctx, tx, actorRef, role, "booking.completed", a.PatientRef, a.ID, requestID)
}
