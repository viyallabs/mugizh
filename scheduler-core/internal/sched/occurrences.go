package sched

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/problem"
)

const occCols = `o.id, o.activity_id, o.subject_ref, a.external_ref, a.type_key, a.time_zone, o.due_at, o.state, o.dispatched_at, o.acknowledged_at,
	o.action, o.acted_at, o.notify_at, o.snooze_count`

func scanOcc(row pgx.Row) (*Occurrence, error) {
	var o Occurrence
	if err := row.Scan(&o.ID, &o.ActivityID, &o.SubjectRef, &o.ExternalRef, &o.TypeKey, &o.TimeZone, &o.DueAt, &o.State, &o.DispatchedAt, &o.AcknowledgedAt,
		&o.Action, &o.ActedAt, &o.NotifyAt, &o.SnoozeCount); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListParams selects a page of a subject's occurrences.
type ListParams struct {
	SubjectRef uuid.UUID
	From, To   *time.Time
	Limit      int
	Cursor     string
}

// List returns occurrences ordered by (due_at, id) and the next cursor ("" on the last page).
func (s *Service) List(ctx context.Context, p ListParams) ([]*Occurrence, string, error) {
	if p.Limit < 1 || p.Limit > 100 {
		p.Limit = 20
	}
	var curT *time.Time
	var curID *uuid.UUID
	if p.Cursor != "" {
		t, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, "", problem.BadRequest.WithFields(problem.FieldError{Field: "cursor", Code: "invalid"})
		}
		curT, curID = &t, &id
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+occCols+` FROM scheduler.occurrences o JOIN scheduler.activities a ON a.id = o.activity_id
		WHERE o.subject_ref = $1
		  AND ($2::timestamptz IS NULL OR o.due_at >= $2)
		  AND ($3::timestamptz IS NULL OR o.due_at < $3)
		  AND ($4::timestamptz IS NULL OR (o.due_at, o.id) > ($4, $5::uuid))
		ORDER BY o.due_at, o.id LIMIT $6`, p.SubjectRef, p.From, p.To, curT, curID, p.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*Occurrence
	for rows.Next() {
		o, err := scanOcc(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > p.Limit {
		out = out[:p.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.DueAt, last.ID)
	}
	return out, next, nil
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	ts, ids, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(ids)
	return t, id, err
}

// GetOccurrence returns one occurrence or ErrNotFound.
func (s *Service) GetOccurrence(ctx context.Context, id uuid.UUID) (*Occurrence, error) {
	o, err := scanOcc(s.Pool.QueryRow(ctx, `SELECT `+occCols+` FROM scheduler.occurrences o JOIN scheduler.activities a ON a.id = o.activity_id WHERE o.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

// Acknowledge moves pending or dispatched to acknowledged. Repeating it succeeds.
func (s *Service) Acknowledge(ctx context.Context, id uuid.UUID) (*Occurrence, error) {
	return s.transition(ctx, id, OccAcknowledged, "acknowledged_at", OccPending, OccDispatched)
}

// Miss moves dispatched to missed. Repeating it succeeds.
func (s *Service) Miss(ctx context.Context, id uuid.UUID) (*Occurrence, error) {
	return s.transition(ctx, id, OccMissed, "", OccDispatched)
}

// CancelOccurrence moves pending to cancelled. Repeating it succeeds.
func (s *Service) CancelOccurrence(ctx context.Context, id uuid.UUID) (*Occurrence, error) {
	return s.transition(ctx, id, OccCancelled, "", OccPending)
}

func (s *Service) transition(ctx context.Context, id uuid.UUID, to, tsCol string, from ...string) (*Occurrence, error) {
	set := "state = $2"
	if tsCol != "" {
		set += ", " + tsCol + " = now()"
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET `+set+` WHERE id = $1 AND state = ANY($3)`, id, to, from)
	if err != nil {
		return nil, err
	}
	o, err := s.GetOccurrence(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 && o.State != to {
		return nil, ErrOccState
	}
	return o, nil
}

// ---- dispatch support ----

// Due is an occurrence claimed for delivery.
type Due struct {
	OccurrenceID uuid.UUID
	SubjectRef   uuid.UUID
	ExternalRef  string
	TypeKey      string
	DueAt        time.Time
	// NotifyAt is set (and Round > 0) for a snoozed occurrence that is notified again.
	NotifyAt *time.Time
	Round    int
}

// ClaimDue marks up to limit pending, not yet enqueued occurrences due at or before now as enqueued and returns
// them, inside the caller's transaction, so the queue insert and the claim commit together. Rows are locked
// with SKIP LOCKED so several instances never claim the same occurrence.
func (s *Service) ClaimDue(ctx context.Context, tx pgx.Tx, now time.Time, limit int) ([]Due, error) {
	rows, err := tx.Query(ctx, `
		WITH c AS (
		  SELECT id FROM scheduler.occurrences
		  WHERE state = 'pending' AND enqueued_at IS NULL AND COALESCE(notify_at, due_at) <= $1
		  ORDER BY COALESCE(notify_at, due_at) LIMIT $2 FOR UPDATE SKIP LOCKED),
		u AS (
		  UPDATE scheduler.occurrences o SET enqueued_at = now() FROM c WHERE o.id = c.id
		  RETURNING o.id, o.activity_id, o.subject_ref, o.due_at, o.notify_at, o.snooze_count)
		SELECT u.id, u.subject_ref, a.external_ref, a.type_key, u.due_at, u.notify_at, u.snooze_count
		FROM u JOIN scheduler.activities a ON a.id = u.activity_id`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var d Due
		if err := rows.Scan(&d.OccurrenceID, &d.SubjectRef, &d.ExternalRef, &d.TypeKey, &d.DueAt, &d.NotifyAt, &d.Round); err != nil {
			return nil, err
		}
		if d.Round == 0 {
			d.NotifyAt = nil // only a snoozed re-notification carries a notify time
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ExpireStale marks pending occurrences that are more than maxLate overdue and were never enqueued as missed:
// a medicine reminder hours late is worse than none, and the outage is logged by the caller.
func (s *Service) ExpireStale(ctx context.Context, now time.Time, maxLate time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'missed'
		WHERE state = 'pending' AND enqueued_at IS NULL AND COALESCE(notify_at, due_at) < $1`, now.Add(-maxLate))
	return tag.RowsAffected(), err
}

// StillPending reports whether the occurrence still awaits delivery (it may have been cancelled, paused away,
// acknowledged early or erased since it was queued).
func (s *Service) StillPending(ctx context.Context, id uuid.UUID) (bool, error) {
	var st string
	err := s.Pool.QueryRow(ctx, `SELECT state FROM scheduler.occurrences WHERE id = $1`, id).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return st == OccPending, err
}

// StillDeliverable is StillPending for a delivery job of a given round: a snooze after the job was queued bumps
// the occurrence's round, so the old job must not send (the snooze makes the occurrence claimable again).
func (s *Service) StillDeliverable(ctx context.Context, id uuid.UUID, round int) (bool, error) {
	var st string
	var cur int
	err := s.Pool.QueryRow(ctx, `SELECT state, snooze_count FROM scheduler.occurrences WHERE id = $1`, id).Scan(&st, &cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return st == OccPending && cur == round, err
}

// MarkDispatchedRound records a delivery of the given round; it reports false when a snooze or an action
// changed the occurrence meanwhile.
func (s *Service) MarkDispatchedRound(ctx context.Context, id uuid.UUID, round int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'dispatched', dispatched_at = now() WHERE id = $1 AND state = 'pending' AND snooze_count = $2`, id, round)
	return tag.RowsAffected() == 1, err
}

// MarkDispatched records a successful delivery. It reports false when the occurrence was no longer pending.
func (s *Service) MarkDispatched(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'dispatched', dispatched_at = now() WHERE id = $1 AND state = 'pending'`, id)
	return tag.RowsAffected() == 1, err
}

// MarkFailed records that delivery gave up.
func (s *Service) MarkFailed(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'failed' WHERE id = $1 AND state = 'pending'`, id)
	return err
}
