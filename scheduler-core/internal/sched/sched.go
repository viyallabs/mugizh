// Package sched is scheduler-core's domain: activities, occurrences, rolling generation, state changes and
// erasure (S-127, S-140). K-ARC-02: it holds opaque references, an RRULE and an IANA zone, never health data.
package sched

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/recur"
)

// DeliverKind is the River job kind of an occurrence delivery. It is here so erasure can remove unfinished
// jobs of a subject without importing the dispatch package.
const DeliverKind = "occurrence.deliver"

// Activity states.
const (
	ActivityActive    = "active"
	ActivityPaused    = "paused"
	ActivityEnded     = "ended"
	ActivityCancelled = "cancelled"
)

// Occurrence states.
const (
	OccPending      = "pending"
	OccDispatched   = "dispatched"
	OccAcknowledged = "acknowledged"
	OccMissed       = "missed"
	OccCancelled    = "cancelled"
	OccFailed       = "failed"
)

// TypeKeys is the closed, coarse enum of activity types (K-ARC-02).
var TypeKeys = map[string]bool{"med.dose": true, "injection.dose": true, "test.due": true, "appointment": true, "custom": true}

var (
	appRefRe      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	externalRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Problems returned by the service.
var (
	ErrRuleInvalid   = problem.New(http.StatusBadRequest, "activity.rrule_invalid", "The recurrence rule is not acceptable")
	ErrZoneInvalid   = problem.New(http.StatusBadRequest, "activity.time_zone_invalid", "The time zone is not acceptable")
	ErrActivityState = problem.New(http.StatusConflict, "activity.invalid_state", "The activity is not in a state that allows this")
	ErrOccState      = problem.New(http.StatusConflict, "occurrence.invalid_state", "The occurrence is not in a state that allows this")
	ErrKeyReused     = problem.New(http.StatusConflict, "idempotency.key_reused", "The idempotency key was used with a different request")
	ErrStale         = problem.New(http.StatusPreconditionFailed, "resource.precondition_failed", "The activity changed since it was read")
	ErrNotFound      = problem.NotFound
)

// Activity is a recurring activity.
type Activity struct {
	ID               uuid.UUID
	SubjectRef       uuid.UUID
	AppRef           string
	ExternalRef      string
	TypeKey          string
	RRule            string
	TimeZone         string
	StartsAt         time.Time
	EndsAt           *time.Time
	State            string
	GeneratedThrough time.Time
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Occurrence is one due instant of an activity.
type Occurrence struct {
	ID             uuid.UUID
	ActivityID     uuid.UUID
	SubjectRef     uuid.UUID
	ExternalRef    string
	TypeKey        string
	TimeZone       string // the activity's IANA zone, so a client can show the local time
	DueAt          time.Time
	State          string
	DispatchedAt   *time.Time
	AcknowledgedAt *time.Time
	// Patient action (S-154). Action is "taken" or "skipped" once final; ActedAt is when the patient acted.
	Action   *string
	ActedAt  *time.Time
	NotifyAt *time.Time // when a snoozed occurrence is notified again
	// SnoozeCount is how many times the occurrence was snoozed; it doubles as the delivery round.
	SnoozeCount int
}

// CreateInput is the body of POST /v1/activities. The json tags double as the canonical form that is hashed
// for idempotency.
type CreateInput struct {
	SubjectRef  uuid.UUID  `json:"subjectRef"`
	AppRef      string     `json:"appRef"`
	ExternalRef string     `json:"externalRef"`
	TypeKey     string     `json:"typeKey"`
	RRule       string     `json:"rrule"`
	TimeZone    string     `json:"timeZone"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      *time.Time `json:"endsAt,omitempty"`
}

// UpdateInput is the body of PATCH /v1/activities/{id}; nil fields stay unchanged.
type UpdateInput struct {
	TimeZone *string
	RRule    *string
	EndsAt   *time.Time
}

// Service holds the domain operations.
type Service struct {
	Pool *pgxpool.Pool
	// Clock returns the current time; tests replace it.
	Clock func() time.Time
	// Horizon is how far ahead occurrences are generated (14 days).
	Horizon time.Duration
	// RefreshSlack: GenerateDue extends an activity once its generated horizon is older than Horizon-RefreshSlack.
	RefreshSlack time.Duration

	// Patient action rules (S-154, see actions.go).
	ActionWindowBefore time.Duration // an action may be dated this long before the occurrence is due
	ActionWindowAfter  time.Duration // ... and this long after
	ClockSkew          time.Duration // tolerance for a phone clock that runs ahead
	SnoozeStaleAfter   time.Duration // a snooze older than this when it arrives is refused
	SnoozeMin          int           // snooze length bounds, in SnoozeUnit
	SnoozeMax          int
	SnoozeDefault      int
	MaxSnoozes         int
	// SnoozeUnit is the length of one snooze "minute" (tests shorten it).
	SnoozeUnit time.Duration
}

// NewService returns a Service with the standard horizon.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{Pool: pool, Clock: time.Now, Horizon: 14 * 24 * time.Hour, RefreshSlack: 6 * time.Hour,
		ActionWindowBefore: time.Hour, ActionWindowAfter: 24 * time.Hour, ClockSkew: 5 * time.Minute, SnoozeStaleAfter: 15 * time.Minute,
		SnoozeMin: 5, SnoozeMax: 60, SnoozeDefault: 10, MaxSnoozes: 3, SnoozeUnit: time.Minute}
}

func (s *Service) now() time.Time { return s.Clock().UTC().Truncate(time.Microsecond) }

const activityCols = `id, subject_ref, app_ref, external_ref, type_key, rrule, time_zone, starts_at, ends_at, state, generated_through, version, created_at, updated_at`

func scanActivity(row pgx.Row) (*Activity, error) {
	var a Activity
	err := row.Scan(&a.ID, &a.SubjectRef, &a.AppRef, &a.ExternalRef, &a.TypeKey, &a.RRule, &a.TimeZone, &a.StartsAt, &a.EndsAt,
		&a.State, &a.GeneratedThrough, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (in *CreateInput) validate() error {
	var f []problem.FieldError
	if in.SubjectRef == uuid.Nil {
		f = append(f, problem.FieldError{Field: "subjectRef", Code: "required"})
	}
	if !appRefRe.MatchString(in.AppRef) {
		f = append(f, problem.FieldError{Field: "appRef", Code: "invalid"})
	}
	if !externalRefRe.MatchString(in.ExternalRef) {
		f = append(f, problem.FieldError{Field: "externalRef", Code: "invalid"})
	}
	if !TypeKeys[in.TypeKey] {
		f = append(f, problem.FieldError{Field: "typeKey", Code: "invalid"})
	}
	if in.StartsAt.IsZero() {
		f = append(f, problem.FieldError{Field: "startsAt", Code: "required"})
	}
	if in.EndsAt != nil && !in.EndsAt.After(in.StartsAt) {
		f = append(f, problem.FieldError{Field: "endsAt", Code: "out_of_range"})
	}
	if len(f) > 0 {
		return problem.BadRequest.WithFields(f...)
	}
	return nil
}

// parseRule maps recur errors to problems.
func parseRule(rule, zone string, start time.Time, end *time.Time) (*recur.Rule, error) {
	r, err := recur.Parse(rule, zone, start, end)
	switch {
	case err == nil:
		return r, nil
	case errors.Is(err, recur.ErrZone):
		return nil, ErrZoneInvalid.WithFields(problem.FieldError{Field: "timeZone", Code: "invalid"})
	default:
		return nil, ErrRuleInvalid.WithFields(problem.FieldError{Field: "rrule", Code: "invalid"})
	}
}

// Create makes an activity and its first 14 days of occurrences. replayed is true when the same idempotency
// key and body already created the activity (the current activity is returned).
func (s *Service) Create(ctx context.Context, key uuid.UUID, in CreateInput) (a *Activity, replayed bool, err error) {
	if err := in.validate(); err != nil {
		return nil, false, err
	}
	in.StartsAt = in.StartsAt.UTC().Truncate(time.Second)
	if in.EndsAt != nil {
		e := in.EndsAt.UTC().Truncate(time.Second)
		in.EndsAt = &e
	}
	rule, err := parseRule(in.RRule, in.TimeZone, in.StartsAt, in.EndsAt)
	if err != nil {
		return nil, false, err
	}
	body, _ := json.Marshal(in)
	hash := sha256.Sum256(body)
	now := s.now()

	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		a, replayed = nil, false
		// Serialise requests with the same key so a retry that races the first request replays it.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scheduler.idempotency_keys WHERE idem_key = $1 AND created_at < now() - interval '24 hours'`, key); err != nil {
			return err
		}
		var stored []byte
		var actID uuid.UUID
		switch err := tx.QueryRow(ctx, `SELECT request_hash, activity_id FROM scheduler.idempotency_keys WHERE idem_key = $1`, key).Scan(&stored, &actID); {
		case err == nil:
			if !bytes.Equal(stored, hash[:]) {
				return ErrKeyReused
			}
			a, err = scanActivity(tx.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1`, actID))
			replayed = true
			return err
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO scheduler.subjects (subject_ref) VALUES ($1) ON CONFLICT DO NOTHING`, in.SubjectRef); err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		horizon := now.Add(s.Horizon)
		a, err = scanActivity(tx.QueryRow(ctx, `INSERT INTO scheduler.activities
			(id, subject_ref, app_ref, external_ref, type_key, rrule, time_zone, starts_at, ends_at, generated_through, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) RETURNING `+activityCols,
			id, in.SubjectRef, in.AppRef, in.ExternalRef, in.TypeKey, in.RRule, in.TimeZone, in.StartsAt, in.EndsAt, horizon, now))
		if err != nil {
			return err
		}
		if _, err := s.generate(ctx, tx, a, rule, now, horizon); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO scheduler.idempotency_keys (idem_key, request_hash, activity_id) VALUES ($1,$2,$3)`, key, hash[:], id); err != nil {
			return err
		}
		a, err = scanActivity(tx.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1`, id))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return a, replayed, nil
}

// generate inserts occurrences for (from, to] and moves the activity's horizon to `to`. The activity row must
// be locked by the caller. An exhausted rule ends the activity.
func (s *Service) generate(ctx context.Context, tx pgx.Tx, a *Activity, rule *recur.Rule, from, to time.Time) (inserted int, err error) {
	instants := rule.Between(from, to)
	if len(instants) > 0 {
		ids := make([]string, len(instants))
		for i := range instants {
			id, err := uuid.NewV7()
			if err != nil {
				return 0, err
			}
			ids[i] = id.String()
		}
		tag, err := tx.Exec(ctx, `INSERT INTO scheduler.occurrences (id, activity_id, subject_ref, due_at)
			SELECT x.id::uuid, $3, $4, x.due FROM unnest($1::text[], $2::timestamptz[]) AS x(id, due)
			ON CONFLICT (activity_id, due_at) WHERE state <> 'cancelled' DO NOTHING`, ids, instants, a.ID, a.SubjectRef)
		if err != nil {
			return 0, err
		}
		inserted = int(tag.RowsAffected())
	}
	state := a.State
	if state == ActivityActive && rule.Exhausted(to) {
		state = ActivityEnded
	}
	_, err = tx.Exec(ctx, `UPDATE scheduler.activities SET generated_through = $2, state = $3, updated_at = $4 WHERE id = $1`,
		a.ID, to, state, s.now())
	return inserted, err
}

// ErrStoredRule reports activities whose stored rule or zone no longer parses (a data or tzdata problem to alert on).
var ErrStoredRule = errors.New("sched: some activities have a stored rule that no longer parses")

// GenerateDue extends the horizon of active activities that are within RefreshSlack of running short.
// It is idempotent and safe to run from several instances (rows are claimed with SKIP LOCKED). An activity
// whose stored rule no longer parses is skipped, so it cannot block the others, and is reported in the error.
func (s *Service) GenerateDue(ctx context.Context) (activities, occurrences int, err error) {
	bad := 0
	for {
		var n, occ, nbad int
		err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			n, occ, nbad = 0, 0, 0
			now := s.now()
			target := now.Add(s.Horizon)
			rows, err := tx.Query(ctx, `SELECT `+activityCols+` FROM scheduler.activities
				WHERE state = 'active' AND generated_through < $1 ORDER BY generated_through LIMIT 200 FOR UPDATE SKIP LOCKED`,
				target.Add(-s.RefreshSlack))
			if err != nil {
				return err
			}
			var batch []*Activity
			for rows.Next() {
				a, err := scanActivity(rows)
				if err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, a)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, a := range batch {
				n++
				rule, err := recur.Parse(a.RRule, a.TimeZone, a.StartsAt, a.EndsAt)
				if err != nil {
					nbad++
					if _, err := tx.Exec(ctx, `UPDATE scheduler.activities SET generated_through = $2 WHERE id = $1`, a.ID, target); err != nil {
						return err
					}
					continue
				}
				from := a.GeneratedThrough
				if from.Before(now) {
					from = now
				}
				c, err := s.generate(ctx, tx, a, rule, from, target)
				if err != nil {
					return err
				}
				occ += c
			}
			return nil
		})
		if err != nil {
			return activities, occurrences, err
		}
		activities += n
		occurrences += occ
		bad += nbad
		if n < 200 {
			break
		}
	}
	if bad > 0 {
		return activities, occurrences, ErrStoredRule
	}
	return activities, occurrences, nil
}

// Get returns an activity or ErrNotFound.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Activity, error) {
	a, err := scanActivity(s.Pool.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func lockActivity(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Activity, error) {
	a, err := scanActivity(tx.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// Update changes the zone, rule or end of an active activity (E-REM-01). ifMatch is the version the caller
// last read. A change of zone keeps the same local times in the new zone. Pending future occurrences are
// cancelled and regenerated from now, so there are no duplicates and no gaps (TC-39).
func (s *Service) Update(ctx context.Context, id uuid.UUID, ifMatch int, in UpdateInput) (*Activity, error) {
	var out *Activity
	var rule *recur.Rule
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		a, err := lockActivity(ctx, tx, id)
		if err != nil {
			return err
		}
		if a.Version != ifMatch {
			return ErrStale
		}
		if a.State != ActivityActive {
			return ErrActivityState
		}
		zone, rruleText, ends, start := a.TimeZone, a.RRule, a.EndsAt, a.StartsAt
		if in.TimeZone != nil && *in.TimeZone != a.TimeZone {
			zone = *in.TimeZone
			newLoc, zerr := recur.LoadZone(zone)
			oldLoc, oerr := recur.LoadZone(a.TimeZone)
			if zerr != nil || oerr != nil {
				return ErrZoneInvalid.WithFields(problem.FieldError{Field: "timeZone", Code: "invalid"})
			}
			start = recur.Resolve(wall(a.StartsAt, oldLoc), newLoc).UTC()
		} else if in.TimeZone != nil {
			if _, zerr := recur.LoadZone(*in.TimeZone); zerr != nil {
				return ErrZoneInvalid.WithFields(problem.FieldError{Field: "timeZone", Code: "invalid"})
			}
		}
		if in.RRule != nil {
			rruleText = *in.RRule
		}
		if in.EndsAt != nil {
			e := in.EndsAt.UTC().Truncate(time.Second)
			ends = &e
		}
		if ends != nil && !ends.After(start) {
			return problem.BadRequest.WithFields(problem.FieldError{Field: "endsAt", Code: "out_of_range"})
		}
		rule, err = parseRule(rruleText, zone, start, ends)
		if err != nil {
			return err
		}
		changed := zone != a.TimeZone || rruleText != a.RRule || (ends == nil) != (a.EndsAt == nil) || (ends != nil && !ends.Equal(*a.EndsAt))
		if !changed {
			out = a
			return nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'cancelled' WHERE activity_id = $1 AND state = 'pending' AND due_at > $2`, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE scheduler.activities SET rrule=$2, time_zone=$3, starts_at=$4, ends_at=$5, version = version + 1 WHERE id=$1`, id, rruleText, zone, start, ends); err != nil {
			return err
		}
		a.RRule, a.TimeZone, a.StartsAt, a.EndsAt = rruleText, zone, start, ends
		if _, err := s.generate(ctx, tx, a, rule, now, now.Add(s.Horizon)); err != nil {
			return err
		}
		out, err = scanActivity(tx.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1`, id))
		return err
	})
	return out, err
}

// wall returns the local wall-clock time of t in loc as a floating (UTC-fielded) time.
func wall(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// Pause cancels every pending occurrence and stops generation. Pausing a paused activity succeeds.
func (s *Service) Pause(ctx context.Context, id uuid.UUID) (*Activity, error) {
	return s.setState(ctx, id, ActivityPaused, []string{ActivityActive, ActivityPaused}, true)
}

// Cancel is terminal. Cancelling a cancelled activity succeeds.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (*Activity, error) {
	return s.setState(ctx, id, ActivityCancelled, []string{ActivityActive, ActivityPaused, ActivityEnded, ActivityCancelled}, true)
}

// Resume regenerates occurrences from now. Resuming an active activity succeeds.
func (s *Service) Resume(ctx context.Context, id uuid.UUID) (*Activity, error) {
	return s.setState(ctx, id, ActivityActive, []string{ActivityPaused, ActivityActive}, false)
}

func (s *Service) setState(ctx context.Context, id uuid.UUID, to string, from []string, cancelPending bool) (*Activity, error) {
	var out *Activity
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		a, err := lockActivity(ctx, tx, id)
		if err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || a.State == f
		}
		if !ok {
			return ErrActivityState
		}
		if a.State == to {
			out = a
			return nil
		}
		now := s.now()
		if cancelPending {
			if _, err := tx.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'cancelled' WHERE activity_id = $1 AND state = 'pending'`, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE scheduler.activities SET state = $2, updated_at = $3, version = version + 1 WHERE id = $1`, id, to, now); err != nil {
			return err
		}
		a.State = to
		if to == ActivityActive {
			rule, err := recur.Parse(a.RRule, a.TimeZone, a.StartsAt, a.EndsAt)
			if err != nil {
				return ErrRuleInvalid
			}
			if _, err := s.generate(ctx, tx, a, rule, now, now.Add(s.Horizon)); err != nil {
				return err
			}
		}
		out, err = scanActivity(tx.QueryRow(ctx, `SELECT `+activityCols+` FROM scheduler.activities WHERE id = $1`, id))
		return err
	})
	return out, err
}

// Erase deletes everything held for a subject (K-DP-07), including unfinished queue jobs, and reports counts.
type ErasureReport struct{ Subjects, Activities, Occurrences, QueueJobs int }

// Erase removes the subject. It is idempotent: an unknown subject yields zero counts.
func (s *Service) Erase(ctx context.Context, subject uuid.UUID) (ErasureReport, error) {
	var r ErasureReport
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		r = ErasureReport{}
		if err := tx.QueryRow(ctx, `WITH d AS (DELETE FROM scheduler_queue.river_job WHERE kind = $1 AND args->>'subjectRef' = $2 RETURNING 1) SELECT count(*) FROM d`,
			DeliverKind, subject.String()).Scan(&r.QueueJobs); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler.occurrences WHERE subject_ref = $1`, subject).Scan(&r.Occurrences); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler.activities WHERE subject_ref = $1`, subject).Scan(&r.Activities); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM scheduler.subjects WHERE subject_ref = $1`, subject) // cascades
		if err != nil {
			return err
		}
		r.Subjects = int(tag.RowsAffected())
		return nil
	})
	return r, err
}

// ExpireIdempotency deletes replay records older than 24 hours.
func (s *Service) ExpireIdempotency(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM scheduler.idempotency_keys WHERE created_at < now() - interval '24 hours'`)
	return tag.RowsAffected(), err
}

// MarkMissed marks dispatched occurrences that nobody acknowledged within `after` as missed.
func (s *Service) MarkMissed(ctx context.Context, after time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state = 'missed' WHERE state = 'dispatched' AND dispatched_at < $1`, s.now().Add(-after))
	return tag.RowsAffected(), err
}
