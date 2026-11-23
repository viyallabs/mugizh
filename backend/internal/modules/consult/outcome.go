package consult

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// ConfirmIdentity records that the doctor confirmed the patient's name and age (P02, S-147,
// TC-34). Clinical's composer stays closed until this is set. The doctor must be able to see
// the name and age through records.Authorize, and both sides must be connected.
func (m *Module) ConfirmIdentity(ctx context.Context, a Actor, consultID string, nameOK, ageOK bool) (View, error) {
	if a.Role != roleDoctor {
		return View{}, problem.Forbidden
	}
	if !ids.Valid(consultID) {
		return View{}, problem.NotFound
	}
	pre, err := m.access(ctx, a, consultID, false)
	if err != nil {
		return View{}, err
	}
	if !nameOK || !ageOK {
		return View{}, ErrIdentityNotSure
	}
	if _, ok, err := m.patientIdentity(ctx, a, pre); err != nil {
		return View{}, err
	} else if !ok {
		return View{}, ErrIdentityMissing
	}
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		s, err := loadSession(ctx, tx, consultID, false, true)
		if err != nil {
			return err
		}
		switch {
		case s.MinorFlagged || terminal(s.State):
			return ErrClosed
		case s.State != StateBothConnected:
			return ErrNotConnected
		}
		// A repeat confirmation during the live call refreshes the time: if the patient changed
		// their name, date of birth or sex after an earlier confirmation, only a new one counts
		// (review F3).

		now := m.now()
		if _, err := tx.Exec(ctx, `UPDATE session SET identity_confirmed=true, identity_confirmed_at=$2, identity_confirmed_by=$3, updated_at=$2 WHERE id=$1`, s.ID, now, a.UserID); err != nil {
			return err
		}
		return m.auditEvent(ctx, tx, a, "consult.identity_confirmed", s)
	})
	if err != nil {
		return View{}, err
	}
	s, err := loadSession(ctx, m.pool, consultID, false, false)
	if err != nil {
		return View{}, err
	}
	return m.view(ctx, a, s)
}

// endSession finishes a session inside tx and emits consult.ended.
func (m *Module) endSession(ctx context.Context, tx pgx.Tx, a Actor, s Session, state, outcome string, short bool, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE session SET state=$2, outcome=$3, ended_at=$4, short_call=$5, updated_at=$4 WHERE id=$1`, s.ID, state, outcome, now, short); err != nil {
		return err
	}
	p := map[string]string{cp.PayloadSessionRef: s.ID, cp.PayloadAppointmentRef: s.AppointmentRef, cp.PayloadOutcome: outcome, cp.PayloadEndedAt: utc(now)}
	if short {
		p[cp.PayloadFlag] = cp.FlagShortCall
	}
	if err := m.emit(ctx, tx, cp.EventEnded, s.AppointmentRef, p); err != nil {
		return err
	}
	return m.auditEvent(ctx, tx, a, "consult.ended."+outcome, s)
}

// SetOutcome ends the consult with an outcome set by a person (S-148).
//
//   - completed: only when both sides were connected (E-BK-08, TC-27) and the doctor confirmed
//     the patient's identity (P02). Under MinDuration it is flagged short_call.
//   - converted_in_person: both sides connected; the mode becomes in_person (TC-33).
//   - patient_no_show: the doctor was present and the patient was not, after the grace period.
//   - technical_failure: the doctor or the patient, from any live state.
//
// The same outcome set twice is harmless; a different one on an ended consult is refused.
func (m *Module) SetOutcome(ctx context.Context, a Actor, consultID, outcome string) (View, error) {
	if !ids.Valid(consultID) {
		return View{}, problem.NotFound
	}
	pre, err := m.access(ctx, a, consultID, false)
	if err != nil {
		return View{}, err
	}
	switch outcome {
	case cp.OutcomeCompleted, cp.OutcomeConvertedInPerson, cp.OutcomePatientNoShow:
		if a.Role != roleDoctor {
			return View{}, ErrWrongOutcome
		}
	case cp.OutcomeTechnicalFailure:
	default:
		return View{}, problem.BadRequest
	}
	if outcome == cp.OutcomePatientNoShow && !terminal(pre.State) {
		// Fail closed on an unknown answer: a wrong no-show costs the patient money (TC-27).
		if err := m.SyncParticipants(ctx, pre.ID); err != nil {
			m.log.Warn("consult.sync_failed", logging.Err(err))
			return View{}, ErrUnavailable
		}
	}
	var room string
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		room = ""
		s, err := loadSession(ctx, tx, consultID, false, true)
		if err != nil {
			return err
		}
		if terminal(s.State) {
			if s.Outcome == outcome {
				return nil
			}
			return ErrClosed
		}
		now := m.now()
		state, short := StateEnded, false
		switch outcome {
		case cp.OutcomeCompleted:
			if s.State != StateBothConnected || s.BothConnectedAt == nil {
				return ErrNotCompletable
			}
			if !s.IdentityConfirmed {
				return ErrIdentityRequired
			}
			short = now.Sub(*s.BothConnectedAt) < m.cfg.MinDuration
		case cp.OutcomeConvertedInPerson:
			if s.State != StateBothConnected {
				return ErrNotCompletable
			}
			if err := m.changeModeTx(ctx, tx, s, cp.ModeInPerson, reasonConversion, a.UserID, now); err != nil {
				return err
			}
		case cp.OutcomePatientNoShow:
			doctorIn, err := hasJoined(ctx, tx, s.ID, roleDoctor)
			if err != nil {
				return err
			}
			patientIn, err := hasJoined(ctx, tx, s.ID, rolePatient)
			if err != nil {
				return err
			}
			if !doctorIn || patientIn || now.Before(s.SlotStart.Add(m.cfg.PatientNoShowGrace)) {
				return ErrNotNoShow
			}
			state = StateNoShow
		case cp.OutcomeTechnicalFailure:
			// Review F2: only inside the join window, and only by a side that tried to join
			// (a token was issued or a room could not be created for it). Otherwise either side
			// could end a consult early, or dodge a no-show, by declaring a failure.
			if !m.inWindow(s, now) {
				return ErrOutsideWindow
			}
			var tried bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM participant WHERE session_id=$1 AND role=$2)`, s.ID, a.Role).Scan(&tried); err != nil {
				return err
			}
			if !tried {
				return ErrNotTechnical
			}
			state = StateFailedTech
		}
		room = s.RoomRef
		return m.endSession(ctx, tx, a, s, state, outcome, short, now)
	})
	if err != nil {
		return View{}, err
	}
	if room != "" {
		m.endRoom(ctx, room)
	}
	s, err := loadSession(ctx, m.pool, consultID, false, false)
	if err != nil {
		return View{}, err
	}
	return m.view(ctx, a, s)
}

// ReportMinor stops the consult because the doctor found the patient is a minor (TC-48,
// E-FAM-04, K-DP-10). It records the flag, ends the consult with outcome minor_stopped and
// emits consult.minor_flagged so the account is handled under CH-12. Identity is never confirmed.
func (m *Module) ReportMinor(ctx context.Context, a Actor, consultID string) (View, error) {
	if a.Role != roleDoctor {
		return View{}, problem.Forbidden
	}
	if !ids.Valid(consultID) {
		return View{}, problem.NotFound
	}
	if _, err := m.access(ctx, a, consultID, false); err != nil {
		return View{}, err
	}
	var room string
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		room = ""
		s, err := loadSession(ctx, tx, consultID, false, true)
		if err != nil {
			return err
		}
		if s.State != StateOpen && s.State != StateBothConnected {
			if s.MinorFlagged {
				return nil
			}
			return ErrClosed
		}
		now := m.now()
		if _, err := tx.Exec(ctx, `UPDATE session SET minor_flagged=true, identity_confirmed=false, updated_at=$2 WHERE id=$1`, s.ID, m.now()); err != nil {
			return err
		}
		room = s.RoomRef
		if err := m.endSession(ctx, tx, a, s, StateEnded, cp.OutcomeMinorStopped, false, now); err != nil {
			return err
		}
		return m.emit(ctx, tx, cp.EventMinorFlagged, s.PatientRef, map[string]string{
			cp.PayloadSessionRef: s.ID, cp.PayloadAppointmentRef: s.AppointmentRef, cp.PayloadPatientRef: s.PatientRef})
	})
	if err != nil {
		return View{}, err
	}
	if room != "" {
		m.endRoom(ctx, room)
	}
	s, err := loadSession(ctx, m.pool, consultID, false, false)
	if err != nil {
		return View{}, err
	}
	return m.view(ctx, a, s)
}

// DetectDoctorNoShow is a job (run every minute): when the doctor has not connected within
// DoctorJoinTimeout after the slot start, the session ends with outcome doctor_no_show and
// consult.ended tells booking, which marks the no-show (E-BK-04, RF03, TC-25). A vendor error
// or an unknown answer never penalises the doctor: that session is skipped until the next run.
func (m *Module) DetectDoctorNoShow(ctx context.Context, now time.Time) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT id FROM session WHERE state IN ('scheduled','open') AND slot_start + make_interval(secs => $2) <= $1
		ORDER BY slot_start LIMIT 200`,
		now, m.cfg.DoctorJoinTimeout.Seconds())
	if err != nil {
		return 0, err
	}
	var todo []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	marked := 0
	for _, id := range todo {
		ok, err := m.markDoctorNoShow(ctx, id, now)
		if err != nil {
			m.log.Warn("consult.doctor_no_show_failed", logging.Err(err))
			continue
		}
		if ok {
			marked++
		}
	}
	return marked, nil
}

func (m *Module) markDoctorNoShow(ctx context.Context, id string, now time.Time) (bool, error) {
	s, err := loadSession(ctx, m.pool, id, false, false)
	if err != nil {
		return false, err
	}
	if s.RoomRef != "" { // bring the join events up to date before judging the doctor
		if err := m.SyncParticipants(ctx, id); err != nil {
			return false, err
		}
	}
	// Review F6: the decision is taken once, under the row lock, after the sync; booking learns
	// it from consult.ended (durable, retried), so a doctor who connects in between is never
	// marked absent and booking and consult cannot disagree.
	var done bool
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		done = false
		cur, err := loadSession(ctx, tx, id, false, true)
		if err != nil {
			return err
		}
		joined, err := hasJoined(ctx, tx, id, roleDoctor)
		if err != nil || terminal(cur.State) || joined {
			return err
		}
		// A doctor who was issued a join token within its lifetime may still be connecting:
		// give the benefit of the doubt until the token has expired.
		var recent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM participant WHERE session_id=$1 AND role=$2 AND last_token_at > $3)`,
			id, roleDoctor, now.Add(-m.cfg.TokenTTL)).Scan(&recent); err != nil || recent {
			return err
		}
		done = true
		return m.endSession(ctx, tx, systemActor(), cur, StateNoShow, cp.OutcomeDoctorNoShow, false, now)
	})
	if err == nil && done && s.RoomRef != "" {
		m.endRoom(ctx, s.RoomRef)
	}
	return done, err
}

// SessionFacts implements cp.FactsReader: what clinical needs that consult owns (P02, P10,
// E-RX-06). It returns cp.ErrNotFound when the appointment has no session.
func (m *Module) SessionFacts(ctx context.Context, appointmentRef string) (cp.SessionFacts, error) {
	if !ids.Valid(appointmentRef) {
		return cp.SessionFacts{}, cp.ErrNotFound
	}
	s, err := loadSession(ctx, m.pool, appointmentRef, true, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return cp.SessionFacts{}, cp.ErrNotFound
	}
	if err != nil {
		return cp.SessionFacts{}, err
	}
	var consent, inIndia bool
	var textID string
	var ver int
	err = m.pool.QueryRow(ctx, `SELECT consent_text_id, consent_text_version, in_country_declared FROM pre_join WHERE session_id=$1`, s.ID).Scan(&textID, &ver, &inIndia)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return cp.SessionFacts{}, err
	default:
		consent = textID == m.cfg.ConsentTextID && ver == m.cfg.ConsentTextVersion
	}
	f := cp.SessionFacts{
		AppointmentRef: s.AppointmentRef, SessionRef: s.ID, State: s.State, Outcome: s.Outcome,
		BookedMode: s.BookedMode, ModeAtSigning: s.CurrentMode, ConsultAt: s.SlotStart,
		BothConnected: s.BothConnectedAt != nil, PatientInIndia: consent && inIndia,
		IdentityConfirmed: s.IdentityConfirmed && !s.MinorFlagged, Initiator: initiatorPatient,
		ExplicitConsent: consent, MinorFlagged: s.MinorFlagged,
	}
	if s.BothConnectedAt != nil {
		f.ConsultAt = *s.BothConnectedAt
	}
	if f.IdentityConfirmed {
		var at *time.Time
		if err := m.pool.QueryRow(ctx, `SELECT identity_confirmed_at FROM session WHERE id=$1`, s.ID).Scan(&at); err != nil {
			return cp.SessionFacts{}, err
		}
		if at == nil {
			f.IdentityConfirmed = false // a confirmation without its time is not trusted
		} else {
			f.IdentityConfirmedAt = at.UTC()
		}
	}
	if f.PatientInIndia {
		f.PatientCountry = m.cfg.DeclaredCountry
	}
	return f, nil
}
