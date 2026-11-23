// Package outbox carries events between modules (ADR-28, decision D-07).
//
// A module writes an event to its OWN schema's event_outbox table in the same
// transaction as the change (Emit). A relay moves events to River jobs, one per
// subscriber, and deletes them from the outbox. Delivery is at least once, so every
// subscriber is idempotent (Once). Events carry references and codes only, never
// health content (module-boundaries §4).
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// TableDDL is the event_outbox table every event-producing module creates in its own
// schema (copy it into the module's migration), plus the processed_event table every
// subscribing module creates.
const TableDDL = `
CREATE TABLE event_outbox (
  event_id    uuid        PRIMARY KEY,
  type        text        NOT NULL,
  version     int         NOT NULL DEFAULT 1,
  occurred_at timestamptz NOT NULL,
  subject_ref text        NOT NULL DEFAULT '',
  payload     jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX event_outbox_occurred_idx ON event_outbox (occurred_at, event_id);
CREATE TABLE processed_event (
  handler      text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (handler, event_id)
);`

// Event is the standard envelope.
type Event struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Version    int               `json:"version"`
	OccurredAt time.Time         `json:"occurredAt"`
	Producer   string            `json:"producer"`
	SubjectRef string            `json:"subjectRef,omitempty"`
	Payload    map[string]string `json:"payload,omitempty"`
}

var (
	reType  = regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)
	reKey   = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,40}$`)
	reValue = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{0,100}$`)
)

// ErrInvalid is returned for events that carry anything but references and codes.
var ErrInvalid = errors.New("outbox: invalid event")

func (e *Event) validate() error {
	if !reType.MatchString(e.Type) {
		return fmt.Errorf("%w: type", ErrInvalid)
	}
	if e.SubjectRef != "" && !reValue.MatchString(e.SubjectRef) {
		return fmt.Errorf("%w: subject", ErrInvalid)
	}
	for k, v := range e.Payload {
		if !reKey.MatchString(k) || !reValue.MatchString(v) {
			return fmt.Errorf("%w: payload field %s", ErrInvalid, k)
		}
	}
	return nil
}

// Emit writes an event to the calling module's outbox inside tx. The module's pool sets
// search_path to its schema, so the unqualified table is the module's own.
func Emit(ctx context.Context, tx pgx.Tx, e Event) error {
	if e.ID == "" {
		e.ID = ids.New()
	}
	if e.Version == 0 {
		e.Version = 1
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if err := e.validate(); err != nil {
		return err
	}
	p, _ := json.Marshal(e.Payload)
	_, err := tx.Exec(ctx, `INSERT INTO event_outbox (event_id, type, version, occurred_at, subject_ref, payload) VALUES ($1,$2,$3,$4,$5,$6)`,
		e.ID, e.Type, e.Version, e.OccurredAt, e.SubjectRef, p)
	return err
}

// Handler processes one event for one subscriber.
type Handler func(ctx context.Context, e Event) error

type sub struct {
	name    string
	handler Handler
}

// Bus holds subscriptions and delivers events through River.
type Bus struct {
	mu   sync.RWMutex
	subs map[string][]sub // event type → subscribers
	log  *slog.Logger
}

// NewBus returns an empty bus.
func NewBus(log *slog.Logger) *Bus { return &Bus{subs: map[string][]sub{}, log: log} }

// Subscribe registers handler under a unique subscriber name (e.g. "records.grant_t1").
func (b *Bus) Subscribe(name, eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs[eventType] {
		if s.name == name {
			panic("outbox: duplicate subscriber " + name)
		}
	}
	b.subs[eventType] = append(b.subs[eventType], sub{name: name, handler: h})
}

func (b *Bus) subscribers(eventType string) []sub {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]sub(nil), b.subs[eventType]...)
}

func (b *Bus) handler(name, eventType string) (Handler, bool) {
	for _, s := range b.subscribers(eventType) {
		if s.name == name {
			return s.handler, true
		}
	}
	return nil, false
}

// Dispatch delivers one event to every subscriber, in subscriber-name order (the order the
// relay enqueues them), stopping at the first error. The worker delivers through River; this
// is for end-to-end tests and tools that need the same handlers synchronously.
func (b *Bus) Dispatch(ctx context.Context, e Event) error {
	subs := b.subscribers(e.Type)
	sort.Slice(subs, func(i, j int) bool { return subs[i].name < subs[j].name })
	for _, s := range subs {
		if err := s.handler(ctx, e); err != nil {
			return fmt.Errorf("outbox: %s: %w", s.name, err)
		}
	}
	return nil
}

// DeliverArgs is the River job that delivers one event to one subscriber.
type DeliverArgs struct {
	Subscriber string `json:"subscriber"`
	Event      Event  `json:"event"`
}

// Kind implements river.JobArgs.
func (DeliverArgs) Kind() string { return "event.deliver" }

// InsertOpts gives delivery jobs 10 attempts before they are discarded (dead letter);
// a discarded job raises an alert from the worker process.
func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 10, Queue: "events"}
}

// Worker is the River worker for DeliverArgs.
type Worker struct {
	river.WorkerDefaults[DeliverArgs]
	Bus *Bus
}

// Work implements river.Worker.
func (w *Worker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	h, ok := w.Bus.handler(job.Args.Subscriber, job.Args.Event.Type)
	if !ok {
		// Subscriber removed: nothing to do, but say so.
		w.Bus.log.Warn("outbox.unknown_subscriber", "event_type", job.Args.Event.Type)
		return nil
	}
	return h(ctx, job.Args.Event)
}

// Relay moves one module's outbox into River jobs.
type Relay struct {
	Module string
	Pool   *pgxpool.Pool // the module's own pool
	Queue  *river.Client[pgx.Tx]
	Bus    *Bus
	Log    *slog.Logger
}

// Once relays up to limit events and returns how many were relayed.
func (r *Relay) Once(ctx context.Context, limit int) (int, error) {
	n := 0
	err := db.InTx(ctx, r.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		n = 0
		rows, err := tx.Query(ctx, `SELECT event_id, type, version, occurred_at, subject_ref, payload FROM event_outbox
			ORDER BY occurred_at, event_id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var evs []Event
		for rows.Next() {
			var e Event
			var p []byte
			if err := rows.Scan(&e.ID, &e.Type, &e.Version, &e.OccurredAt, &e.SubjectRef, &p); err != nil {
				rows.Close()
				return err
			}
			e.Producer = r.Module
			if len(p) > 0 {
				_ = json.Unmarshal(p, &e.Payload)
			}
			evs = append(evs, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		var params []river.InsertManyParams
		idsDone := make([]string, 0, len(evs))
		for _, e := range evs {
			subs := r.Bus.subscribers(e.Type)
			sort.Slice(subs, func(i, j int) bool { return subs[i].name < subs[j].name })
			for _, s := range subs {
				params = append(params, river.InsertManyParams{Args: DeliverArgs{Subscriber: s.name, Event: e}})
			}
			idsDone = append(idsDone, e.ID)
		}
		// Insert jobs first (separate pool and role), then delete from the outbox. A crash
		// between the two re-delivers; subscribers are idempotent.
		if len(params) > 0 {
			if _, err := r.Queue.InsertMany(ctx, params); err != nil {
				return fmt.Errorf("outbox: enqueue: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM event_outbox WHERE event_id = ANY($1::uuid[])`, idsDone); err != nil {
			return err
		}
		n = len(evs)
		return nil
	})
	return n, err
}

// Run relays until ctx ends.
func (r *Relay) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for {
			n, err := r.Once(ctx, 200)
			if err != nil {
				r.Log.Error("outbox.relay_failed", "module", r.Module, logging.Err(err))
				break
			}
			if n < 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once runs fn for an event at most once per handler: it records (handler, event) in the
// subscriber's processed_event table inside the same transaction as fn's changes.
func Once(ctx context.Context, pool *pgxpool.Pool, handler string, e Event, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO processed_event (handler, event_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, handler, e.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already processed
		}
		return fn(tx)
	})
}
