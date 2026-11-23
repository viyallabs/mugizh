package sched

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/problem"
)

// Patient actions on an occurrence (S-154). K-ARC-02: only the action word, an instant and a counter are kept.
//
// Rules (decision log D-33):
//   - taken and skipped are final. The first final action wins. The same final action again (another device,
//     another client action ID) succeeds and changes nothing; a different final action is refused with
//     occurrence.action_conflict and changes nothing.
//   - snoozed is not final. It re-notifies once, after a bounded delay. One snooze at a time (a second one while
//     the first has not fired is occurrence.snooze_active), at most MaxSnoozes per occurrence.
//   - The client sends a client action ID and the time the patient acted. The ID makes the call idempotent: the
//     same ID with the same content returns the current occurrence; the same ID with other content is
//     idempotency.key_reused.
//   - The acting time must not be later than now + ClockSkew, nor earlier than ActionWindowBefore before the
//     occurrence is due, nor later than ActionWindowAfter after it. A snooze must also reach the server within
//     SnoozeStaleAfter of when the patient asked for it (a snooze that old has no meaning any more).
//   - A taken or skipped action is accepted on a pending, dispatched, missed or failed occurrence: "missed" is a
//     server guess (rule D-28) and the patient's record of what happened is better. A cancelled occurrence
//     refuses every action.
const (
	ActionTaken   = "taken"
	ActionSkipped = "skipped"
	ActionSnoozed = "snoozed"
)

// Problems for actions.
var (
	ErrActionConflict = problem.New(http.StatusConflict, "occurrence.action_conflict", "A different final action was already recorded")
	ErrSnoozeActive   = problem.New(http.StatusConflict, "occurrence.snooze_active", "The occurrence is already snoozed")
	ErrSnoozeLimit    = problem.New(http.StatusConflict, "occurrence.snooze_limit", "The occurrence cannot be snoozed again")
)

// ActionInput is the body of POST /v1/occurrences/{occurrenceId}/actions.
type ActionInput struct {
	ClientActionID uuid.UUID
	SubjectRef     uuid.UUID // the caller's subject: an occurrence of another subject is not found
	OccurrenceID   uuid.UUID
	Action         string
	ActedAt        time.Time
	SnoozeMinutes  int // snoozed only; 0 takes the default
}

func (in *ActionInput) validate(s *Service) error {
	var f []problem.FieldError
	if in.ClientActionID == uuid.Nil {
		f = append(f, problem.FieldError{Field: "clientActionId", Code: "required"})
	}
	if in.SubjectRef == uuid.Nil {
		f = append(f, problem.FieldError{Field: "subjectRef", Code: "required"})
	}
	switch in.Action {
	case ActionTaken, ActionSkipped:
		if in.SnoozeMinutes != 0 {
			f = append(f, problem.FieldError{Field: "snoozeMinutes", Code: "invalid"})
		}
	case ActionSnoozed:
		if in.SnoozeMinutes == 0 {
			in.SnoozeMinutes = s.SnoozeDefault
		}
		if in.SnoozeMinutes < s.SnoozeMin || in.SnoozeMinutes > s.SnoozeMax {
			f = append(f, problem.FieldError{Field: "snoozeMinutes", Code: "out_of_range"})
		}
	default:
		f = append(f, problem.FieldError{Field: "action", Code: "invalid"})
	}
	if in.ActedAt.IsZero() {
		f = append(f, problem.FieldError{Field: "actedAt", Code: "required"})
	}
	if len(f) > 0 {
		return problem.BadRequest.WithFields(f...)
	}
	return nil
}

// RecordAction applies a patient action. replayed is true when the client action ID was already recorded with
// the same content (the current occurrence is returned).
func (s *Service) RecordAction(ctx context.Context, in ActionInput) (out *Occurrence, replayed bool, err error) {
	if err := in.validate(s); err != nil {
		return nil, false, err
	}
	at := in.ActedAt.UTC().Truncate(time.Second)
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		out, replayed = nil, false
		o, err := scanOcc(tx.QueryRow(ctx, `SELECT `+occCols+` FROM scheduler.occurrences o JOIN scheduler.activities a ON a.id = o.activity_id
			WHERE o.id = $1 AND o.subject_ref = $2 FOR UPDATE OF o`, in.OccurrenceID, in.SubjectRef))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound // also another subject's occurrence (K-SEC-04)
		}
		if err != nil {
			return err
		}
		// Replay first: a retry must get its answer even if the bounds below no longer hold.
		var rOcc uuid.UUID
		var rAction string
		var rAt time.Time
		var rMin int
		switch err := tx.QueryRow(ctx, `SELECT occurrence_id, action, acted_at, COALESCE(snooze_minutes, 0) FROM scheduler.occurrence_actions WHERE client_action_id = $1`,
			in.ClientActionID).Scan(&rOcc, &rAction, &rAt, &rMin); {
		case err == nil:
			if rOcc != o.ID || rAction != in.Action || !rAt.Equal(at) || rMin != in.SnoozeMinutes {
				return ErrKeyReused
			}
			out, replayed = o, true
			return nil
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return err
		}

		now := s.now()
		outOfRange := problem.BadRequest.WithFields(problem.FieldError{Field: "actedAt", Code: "out_of_range"})
		if at.After(now.Add(s.ClockSkew)) || at.Before(o.DueAt.Add(-s.ActionWindowBefore)) || at.After(o.DueAt.Add(s.ActionWindowAfter)) {
			return outOfRange
		}
		if in.Action == ActionSnoozed && now.Sub(at) > s.SnoozeStaleAfter {
			return outOfRange
		}
		if o.State == OccCancelled {
			return ErrOccState
		}

		final := ""
		if o.Action != nil {
			final = *o.Action
		} else if o.State == OccAcknowledged {
			final = ActionTaken // acknowledged through the service API before actions existed
		}
		switch {
		case final != "":
			if in.Action == ActionSnoozed {
				return ErrOccState
			}
			if in.Action != final {
				return ErrActionConflict
			}
			// The same final action again: nothing changes.
		case in.Action == ActionSnoozed:
			if o.State == OccMissed {
				return ErrOccState
			}
			if o.State == OccPending && o.NotifyAt != nil && o.NotifyAt.After(now) {
				return ErrSnoozeActive
			}
			if o.SnoozeCount >= s.MaxSnoozes {
				return ErrSnoozeLimit
			}
			base := now
			if o.DueAt.After(base) {
				base = o.DueAt
			}
			notify := base.Add(time.Duration(in.SnoozeMinutes) * s.SnoozeUnit)
			if _, err := tx.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'pending', enqueued_at = NULL, notify_at = $2, snooze_count = snooze_count + 1 WHERE id = $1`,
				o.ID, notify); err != nil {
				return err
			}
		default: // taken or skipped
			if _, err := tx.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'acknowledged', action = $2, acted_at = $3, acknowledged_at = $4, notify_at = NULL WHERE id = $1`,
				o.ID, in.Action, at, now); err != nil {
				return err
			}
		}
		var min *int
		if in.Action == ActionSnoozed {
			min = &in.SnoozeMinutes
		}
		tag, err := tx.Exec(ctx, `INSERT INTO scheduler.occurrence_actions (client_action_id, occurrence_id, action, acted_at, snooze_minutes)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, in.ClientActionID, o.ID, in.Action, at, min)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrKeyReused // the ID was recorded by a concurrent request for another occurrence
		}
		out, err = scanOcc(tx.QueryRow(ctx, `SELECT `+occCols+` FROM scheduler.occurrences o JOIN scheduler.activities a ON a.id = o.activity_id WHERE o.id = $1`, o.ID))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return out, replayed, nil
}
