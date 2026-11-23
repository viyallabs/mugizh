package consult

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// PatientIdentity is what the doctor sees to identify the patient (P02).
type PatientIdentity struct {
	FullName string
	AgeYears int
}

// View is a session as shown to one of its two participants.
type View struct {
	Session
	JoinOpensAt, JoinClosesAt time.Time
	ConsentRecorded           bool
	PatientInIndia            bool
	// Patient is set for the doctor only, and only when records allowed the read (K-DP-02).
	Patient *PatientIdentity
}

func (m *Module) view(ctx context.Context, a Actor, s Session) (View, error) {
	v := View{Session: s}
	v.JoinOpensAt, v.JoinClosesAt = m.joinWindow(s)
	var textVer int
	var textID string
	err := m.pool.QueryRow(ctx, `SELECT consent_text_id, consent_text_version, in_country_declared FROM pre_join WHERE session_id=$1`, s.ID).
		Scan(&textID, &textVer, &v.PatientInIndia)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return View{}, err
	default:
		v.ConsentRecorded = true
	}
	if a.Role == roleDoctor && !terminal(s.State) {
		if id, ok, err := m.patientIdentity(ctx, a, s); err != nil {
			return View{}, err
		} else if ok {
			v.Patient = &id
		}
	}
	return v, nil
}

// patientIdentity reads the patient's name and age through records.Authorize (K-DP-02). ok is
// false when the read is not allowed or the profile does not exist.
func (m *Module) patientIdentity(ctx context.Context, a Actor, s Session) (PatientIdentity, bool, error) {
	d, err := m.d.Gate.Authorize(ctx, rp.Request{
		Actor:      rp.Actor{Ref: a.UserID, Role: a.Role, ClinicRef: a.ClinicRef},
		PatientRef: s.PatientRef, Kind: rp.KindIdentityBasic, BookingRef: s.AppointmentRef,
		ResourceType: "consult_session", ResourceRef: s.ID, RequestID: a.RequestID,
	})
	if err != nil {
		return PatientIdentity{}, false, err
	}
	if !d.Allowed {
		return PatientIdentity{}, false, nil
	}
	name, age, _, err := m.d.Profiles.ProfileFacts(ctx, s.PatientRef)
	if err != nil {
		m.log.Warn("consult.profile_unavailable", logging.Err(err))
		return PatientIdentity{}, false, nil
	}
	return PatientIdentity{FullName: name, AgeYears: age}, true, nil
}

// Get returns the session of an appointment or by its ID, for its patient or doctor.
func (m *Module) Get(ctx context.Context, a Actor, ref string, byAppointment bool) (View, error) {
	if !ids.Valid(ref) {
		return View{}, problem.NotFound
	}
	s, err := m.access(ctx, a, ref, byAppointment)
	if err != nil {
		return View{}, err
	}
	// Best effort: bring the connection state up to date. A vendor error never blocks a read.
	if err := m.SyncParticipants(ctx, s.ID); err != nil {
		m.log.Warn("consult.sync_failed", logging.Err(err))
	}
	if s, err = loadSession(ctx, m.pool, s.ID, false, false); err != nil {
		return View{}, err
	}
	return m.view(ctx, a, s)
}

// PreJoinInput is the device check and the consent of one consult.
type PreJoinInput struct {
	CameraOK, MicrophoneOK, NetworkOK bool
	ConsentTextID                     string
	ConsentTextVersion                int
	InIndiaNow                        bool
	AcceptAudioFallback               bool
}

// PreJoin records the device check and the teleconsultation consent for this consult (C09),
// including the patient's "I am in India now" declaration (P10, TC-24). A patient who does not
// declare it gets consult.patient_location_not_allowed, and joining stays refused.
func (m *Module) PreJoin(ctx context.Context, a Actor, consultID string, in PreJoinInput) (View, error) {
	if a.Role != rolePatient {
		return View{}, problem.Forbidden
	}
	if !ids.Valid(consultID) {
		return View{}, problem.NotFound
	}
	if _, err := m.access(ctx, a, consultID, false); err != nil {
		return View{}, err
	}
	var declined bool
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		declined = false
		s, err := loadSession(ctx, tx, consultID, false, true)
		if err != nil {
			return err
		}
		now := m.now()
		switch {
		case terminal(s.State):
			return ErrClosed
		case !m.inWindow(s, now):
			return ErrOutsideWindow
		case in.ConsentTextID != m.cfg.ConsentTextID || in.ConsentTextVersion != m.cfg.ConsentTextVersion:
			return ErrConsentStale
		case !in.MicrophoneOK:
			return ErrDeviceCheck
		}
		if (!in.CameraOK || !in.NetworkOK) && s.CurrentMode == "video" {
			if !in.AcceptAudioFallback {
				return ErrDeviceCheck
			}
			if err := m.changeModeTx(ctx, tx, s, "audio", reasonDeviceCheck, a.UserID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pre_join (session_id, consent_text_id, consent_text_version, in_country_declared, camera_ok, microphone_ok, network_ok, accepted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (session_id) DO UPDATE SET consent_text_id=EXCLUDED.consent_text_id, consent_text_version=EXCLUDED.consent_text_version,
			  in_country_declared=EXCLUDED.in_country_declared, camera_ok=EXCLUDED.camera_ok, microphone_ok=EXCLUDED.microphone_ok,
			  network_ok=EXCLUDED.network_ok, accepted_at=EXCLUDED.accepted_at`,
			s.ID, in.ConsentTextID, in.ConsentTextVersion, in.InIndiaNow, in.CameraOK, in.MicrophoneOK, in.NetworkOK, now); err != nil {
			return err
		}
		declined = !in.InIndiaNow
		// Review F4: the record above keeps the latest answer; every answer, with the consent text
		// version it was given under, stays in the audit trail (P10 and C09 evidence).
		reason := "in_country_declared"
		if declined {
			reason = "in_country_not_declared"
		}
		return auditDetail(ctx, tx, a, "consult.pre_join", s, fmt.Sprintf("%s:%d", in.ConsentTextID, in.ConsentTextVersion), reason)
	})
	if err != nil {
		return View{}, err
	}
	if declined {
		return View{}, ErrOutsideCountry
	}
	s, err := loadSession(ctx, m.pool, consultID, false, false)
	if err != nil {
		return View{}, err
	}
	return m.view(ctx, a, s)
}

// JoinResult is a join token for one participant.
type JoinResult struct {
	ConsultID string
	Mode      string
	Token     string
	ExpiresAt time.Time
	State     string
}

// Join issues a short-lived join token bound to the room and to the caller only. It can be
// called again after a drop (rejoin, TC-32). Patients need the consent and the in-India
// declaration; doctors must still be Verified (K-CS-01). Only within the window around the slot.
func (m *Module) Join(ctx context.Context, a Actor, consultID string) (JoinResult, error) {
	if !ids.Valid(consultID) {
		return JoinResult{}, problem.NotFound
	}
	s, err := m.access(ctx, a, consultID, false)
	if err != nil {
		return JoinResult{}, err
	}
	now := m.now()
	switch {
	case terminal(s.State):
		return JoinResult{}, ErrClosed
	case !m.inWindow(s, now):
		return JoinResult{}, ErrOutsideWindow
	}
	if err := m.joinPreconditions(ctx, a, s); err != nil {
		return JoinResult{}, err
	}
	// Record the attempt before the room exists, so a vendor outage that stops every room
	// still leaves proof that this side tried (technical_failure needs it; review F2).
	if _, err := m.pool.Exec(ctx, `INSERT INTO participant (session_id, role, user_ref, participant_ref) VALUES ($1,$2,$3,$4) ON CONFLICT (session_id, role) DO NOTHING`,
		s.ID, a.Role, a.UserID, ids.New()); err != nil {
		return JoinResult{}, err
	}
	if s, err = m.ensureRoom(ctx, s); err != nil {
		return JoinResult{}, err
	}
	ref, err := m.markParticipant(ctx, a, s, now)
	if err != nil {
		return JoinResult{}, err
	}
	exp := now.Add(m.cfg.TokenTTL)
	if c := s.SlotEnd.Add(m.cfg.JoinLate); exp.After(c) {
		exp = c
	}
	tok, err := m.d.Video.IssueJoinToken(ctx, video.TokenRequest{RoomID: s.RoomRef, ParticipantRef: ref, Role: video.Role(a.Role), ExpiresAt: exp})
	if err != nil {
		m.log.Error("consult.token_failed", logging.Err(err))
		return JoinResult{}, ErrRoomUnavailable
	}
	if _, err := m.pool.Exec(ctx, `UPDATE participant SET tokens_issued=tokens_issued+1, last_token_at=$3 WHERE session_id=$1 AND role=$2`, s.ID, a.Role, now); err != nil {
		return JoinResult{}, err
	}
	cur, err := loadSession(ctx, m.pool, s.ID, false, false)
	if err != nil {
		return JoinResult{}, err
	}
	return JoinResult{ConsultID: s.ID, Mode: cur.CurrentMode, Token: tok.Value, ExpiresAt: tok.ExpiresAt, State: cur.State}, nil
}

func (m *Module) joinPreconditions(ctx context.Context, a Actor, s Session) error {
	if a.Role == roleDoctor {
		ok, err := m.d.Verifier.IsVerified(ctx, s.DoctorRef)
		if err != nil {
			m.log.Error("consult.verification_check_failed", logging.Err(err))
			return ErrRoomUnavailable // fail closed on an unknown answer
		}
		if !ok {
			return ErrDoctorNotVerified
		}
		return nil
	}
	var id string
	var ver int
	var inCountry bool
	err := m.pool.QueryRow(ctx, `SELECT consent_text_id, consent_text_version, in_country_declared FROM pre_join WHERE session_id=$1`, s.ID).Scan(&id, &ver, &inCountry)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrConsentRequired
	case err != nil:
		return err
	case id != m.cfg.ConsentTextID || ver != m.cfg.ConsentTextVersion:
		return ErrConsentRequired
	case !inCountry:
		return ErrOutsideCountry // TC-24, P10
	}
	return nil
}

// ensureRoom creates the room on the first join: video, or audio only when video cannot be
// created (the mode change is recorded). A room that reports recording is refused (ADR-13).
func (m *Module) ensureRoom(ctx context.Context, s Session) (Session, error) {
	if s.RoomRef != "" {
		return s, nil
	}
	req := video.RoomRequest{SessionRef: s.ID, StartsAt: s.SlotStart, MaxDuration: m.cfg.MaxDuration, AudioOnly: s.CurrentMode != "video"}
	room, err := m.d.Video.CreateRoom(ctx, req)
	fellBack := false
	if err != nil && !req.AudioOnly {
		req.AudioOnly = true
		room, err = m.d.Video.CreateRoom(ctx, req)
		fellBack = err == nil
	}
	if err != nil {
		m.log.Error("consult.room_failed", logging.Err(err))
		return s, ErrRoomUnavailable
	}
	if room.Recording {
		m.log.Error("consult.room_recording_on")
		m.endRoom(ctx, room.ID)
		return s, ErrRoomUnavailable
	}
	var won bool
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		won = false
		cur, err := loadSession(ctx, tx, s.ID, false, true)
		if err != nil {
			return err
		}
		if cur.RoomRef != "" || terminal(cur.State) {
			s = cur
			return nil
		}
		won = true
		if _, err := tx.Exec(ctx, `UPDATE session SET provider_room_ref=$2, updated_at=$3 WHERE id=$1`, s.ID, room.ID, m.now()); err != nil {
			return err
		}
		s.RoomRef = room.ID
		if fellBack && cur.CurrentMode == "video" {
			return m.changeModeTx(ctx, tx, cur, "audio", reasonRoomFallback, "", m.now())
		}
		return nil
	})
	if err != nil {
		return s, err
	}
	if !won {
		m.endRoom(ctx, room.ID) // another request created the room first
		if terminal(s.State) {
			return s, ErrClosed
		}
	}
	return s, nil
}

// markParticipant records the participant (with an opaque vendor reference) and moves a
// scheduled session to open. It returns the participant reference for the token.
func (m *Module) markParticipant(ctx context.Context, a Actor, s Session, now time.Time) (string, error) {
	var ref string
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadSession(ctx, tx, s.ID, false, true)
		if err != nil {
			return err
		}
		if terminal(cur.State) {
			return ErrClosed
		}
		if _, err := tx.Exec(ctx, `INSERT INTO participant (session_id, role, user_ref, participant_ref) VALUES ($1,$2,$3,$4) ON CONFLICT (session_id, role) DO NOTHING`,
			s.ID, a.Role, a.UserID, ids.New()); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT participant_ref FROM participant WHERE session_id=$1 AND role=$2`, s.ID, a.Role).Scan(&ref); err != nil {
			return err
		}
		// Stamped under the session lock, so the doctor no-show job (which takes the same lock)
		// sees a join in progress before the vendor has issued the token (review F6).
		if _, err := tx.Exec(ctx, `UPDATE participant SET last_token_at=$3 WHERE session_id=$1 AND role=$2`, s.ID, a.Role, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session SET state='open', opened_at=$2, updated_at=$2 WHERE id=$1 AND state='scheduled'`, s.ID, now); err != nil {
			return err
		}
		return m.auditEvent(ctx, tx, a, "consult.join", cur)
	})
	return ref, err
}

func (m *Module) endRoom(ctx context.Context, roomID string) {
	if err := m.d.Video.EndRoom(ctx, roomID); err != nil {
		m.log.Warn("consult.end_room_failed", logging.Err(err))
	}
}
