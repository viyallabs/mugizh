package consult

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// SyncParticipants polls the vendor for the room's join and leave events and applies them
// (S-137). It is safe to call repeatedly. A session without a room is left alone.
func (m *Module) SyncParticipants(ctx context.Context, sessionID string) error {
	s, err := loadSession(ctx, m.pool, sessionID, false, false)
	if err != nil {
		return err
	}
	if s.RoomRef == "" || terminal(s.State) {
		return nil
	}
	evs, err := m.d.Video.ParticipantEvents(ctx, s.RoomRef)
	if err != nil {
		return err
	}
	return m.applyEvents(ctx, s.ID, evs)
}

// RecordParticipantEvents is the webhook entry: the adapter's webhook handler (signature
// already verified) passes the events of one room. Unknown rooms are ignored.
func (m *Module) RecordParticipantEvents(ctx context.Context, roomRef string, evs []video.Event) error {
	var id string
	err := m.pool.QueryRow(ctx, `SELECT id FROM session WHERE provider_room_ref=$1`, roomRef).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.applyEvents(ctx, id, evs)
}

// SyncOpenSessions polls every session that has a room and has not ended (job, every minute).
func (m *Module) SyncOpenSessions(ctx context.Context, now time.Time) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT id FROM session WHERE state IN ('scheduled','open','both_connected') AND provider_room_ref<>'' AND slot_end + make_interval(secs => $2) >= $1 ORDER BY slot_start LIMIT 500`,
		now, m.cfg.JoinLate.Seconds())
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
	n := 0
	for _, id := range todo {
		if err := m.SyncParticipants(ctx, id); err != nil {
			m.log.Warn("consult.sync_failed", logging.Err(err))
			continue
		}
		n++
	}
	return n, nil
}

type storedEvent struct {
	role string
	kind string
	at   time.Time
	id   string
}

// applyEvents stores the events and moves the session to both_connected when both sides were
// present at the same moment (E-BK-08). A leave event never ends the consult (TC-32).
func (m *Module) applyEvents(ctx context.Context, sessionID string, evs []video.Event) error {
	return m.inTx(ctx, func(tx pgx.Tx) error {
		s, err := loadSession(ctx, tx, sessionID, false, true)
		if err != nil {
			return err
		}
		if terminal(s.State) {
			return nil
		}
		roles := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT participant_ref::text, role FROM participant WHERE session_id=$1`, sessionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ref, role string
			if err := rows.Scan(&ref, &role); err != nil {
				rows.Close()
				return err
			}
			roles[ref] = role
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range evs {
			role, ok := roles[e.ParticipantRef]
			if !ok || (e.Kind != video.Joined && e.Kind != video.Left) || e.ID == "" || len(e.ID) > 100 {
				continue // not one of our two participants
			}
			if _, err := tx.Exec(ctx, `INSERT INTO participant_event (session_id, event_id, role, kind, at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
				sessionID, e.ID, role, string(e.Kind), e.At.UTC()); err != nil {
				return err
			}
		}
		stored, err := loadEvents(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		anyJoin := false
		var bothAt *time.Time
		present := map[string]bool{}
		for _, e := range stored {
			present[e.role] = e.kind == string(video.Joined)
			anyJoin = anyJoin || e.kind == string(video.Joined)
			if bothAt == nil && present[rolePatient] && present[roleDoctor] {
				t := e.at
				bothAt = &t
			}
		}
		now := m.now()
		if anyJoin && s.State == StateScheduled {
			if _, err := tx.Exec(ctx, `UPDATE session SET state='open', opened_at=$2, updated_at=$2 WHERE id=$1`, sessionID, now); err != nil {
				return err
			}
		}
		if bothAt == nil || s.State == StateBothConnected {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE session SET state='both_connected', both_connected_at=$2, opened_at=coalesce(opened_at,$2), updated_at=$3 WHERE id=$1`, sessionID, *bothAt, now); err != nil {
			return err
		}
		return m.emit(ctx, tx, cp.EventBothConnected, s.AppointmentRef, map[string]string{cp.PayloadSessionRef: s.ID, cp.PayloadAppointmentRef: s.AppointmentRef})
	})
}

func loadEvents(ctx context.Context, tx pgx.Tx, sessionID string) ([]storedEvent, error) {
	rows, err := tx.Query(ctx, `SELECT role, kind, at, event_id FROM participant_event WHERE session_id=$1`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(&e.role, &e.kind, &e.at, &e.id); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.Before(out[j].at)
		}
		// at the same instant, joins come before leaves
		return out[i].kind == string(video.Joined) && out[j].kind != string(video.Joined)
	})
	return out, rows.Err()
}

// hasJoined reports whether a role has a stored join event for the session.
func hasJoined(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, sessionID, role string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM participant_event WHERE session_id=$1 AND role=$2 AND kind='joined')`, sessionID, role).Scan(&ok)
	return ok, err
}

// changeModeTx records a change of call mode and updates the current mode.
func (m *Module) changeModeTx(ctx context.Context, tx pgx.Tx, s Session, to, reason, by string, at time.Time) error {
	var byArg any
	if ids.Valid(by) {
		byArg = by
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mode_change (id, session_id, from_mode, to_mode, reason, changed_by, changed_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		ids.New(), s.ID, s.CurrentMode, to, reason, byArg, at); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE session SET current_mode=$2, updated_at=$3 WHERE id=$1`, s.ID, to, at)
	return err
}

var validModeReasons = map[string]string{"network": reasonNetworkChoice, "device": reasonDeviceCheck, "user_choice": "user_choice"}

// ChangeMode records that a live call dropped from video to audio, so the mode at signing is
// audio (E-RX-06). Only video to audio is allowed; repeating it is harmless.
func (m *Module) ChangeMode(ctx context.Context, a Actor, consultID, to, reason string) (View, error) {
	if !ids.Valid(consultID) {
		return View{}, problem.NotFound
	}
	if _, err := m.access(ctx, a, consultID, false); err != nil {
		return View{}, err
	}
	rsn, ok := validModeReasons[reason]
	if to != cp.ModeAudio || !ok {
		return View{}, problem.BadRequest
	}
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		s, err := loadSession(ctx, tx, consultID, false, true)
		if err != nil {
			return err
		}
		if s.State != StateOpen && s.State != StateBothConnected {
			return ErrClosed
		}
		switch s.CurrentMode {
		case cp.ModeAudio:
			return nil
		case cp.ModeInPerson:
			return ErrModeChange
		}
		if err := m.changeModeTx(ctx, tx, s, to, rsn, a.UserID, m.now()); err != nil {
			return err
		}
		return m.auditEvent(ctx, tx, a, "consult.mode_change", s)
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
