package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

// Queue names.
const (
	QueueDispatch    = "dispatch"
	QueueMaintenance = "maintenance"
)

// DeliverArgs is the River job for one due occurrence. It carries only what the callback payload carries.
type DeliverArgs struct {
	OccurrenceID        uuid.UUID `json:"occurrenceId"`
	ActivityExternalRef string    `json:"activityExternalRef"`
	TypeKey             string    `json:"typeKey"`
	DueAt               time.Time `json:"dueAt"`
	SubjectRef          uuid.UUID `json:"subjectRef"`
	// Round is the snooze count when the job was queued (0 for the first delivery); NotifyAt is set from round 1.
	Round    int        `json:"round,omitempty"`
	NotifyAt *time.Time `json:"notifyAt,omitempty"`
}

// Kind implements river.JobArgs.
func (DeliverArgs) Kind() string { return sched.DeliverKind }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DeliverArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: QueueDispatch} }

// MaintenanceArgs is the periodic job that extends the generation horizon and tidies up.
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return "maintenance.tick" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (MaintenanceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaintenance, MaxAttempts: 3}
}

// Config tunes the runtime. Zero values take the defaults in Defaults.
type Config struct {
	Workers          int           // concurrent deliveries
	ScanInterval     time.Duration // how often due occurrences are claimed
	ScanBatch        int           // occurrences claimed per transaction
	MaxAttempts      int           // delivery attempts before the occurrence is marked failed
	MaxLateness      time.Duration // pending occurrences later than this are marked missed, not sent
	MissedAfter      time.Duration // dispatched occurrences not acknowledged after this become missed
	MaintenanceEvery time.Duration // horizon extension period
	RetryBase        time.Duration // first retry delay; doubles each attempt
	RetryMax         time.Duration
	JobTimeout       time.Duration
}

// Defaults fills unset fields.
func (c Config) Defaults() Config {
	d := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	if c.Workers <= 0 {
		c.Workers = 200
	}
	if c.ScanBatch <= 0 {
		c.ScanBatch = 1000
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 8
	}
	d(&c.ScanInterval, 500*time.Millisecond)
	d(&c.MaxLateness, time.Hour)
	d(&c.MissedAfter, 6*time.Hour)
	d(&c.MaintenanceEvery, time.Minute)
	d(&c.RetryBase, 5*time.Second)
	d(&c.RetryMax, 10*time.Minute)
	d(&c.JobTimeout, 30*time.Second)
	return c
}

// DeliverWorker sends one occurrence.due event.
type DeliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	svc    *sched.Service
	sender *Sender
	cfg    Config
	log    *slog.Logger
}

// Work delivers the event, then marks the occurrence dispatched. Delivery is at least once; the receiver
// de-duplicates by eventId.
func (w *DeliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	a := job.Args
	pending, err := w.svc.StillDeliverable(ctx, a.OccurrenceID, a.Round)
	if err != nil {
		return err
	}
	if !pending {
		w.log.Info("dispatch.skipped", "occurrenceId", a.OccurrenceID) // cancelled, acknowledged early or erased
		return nil
	}
	err = w.sender.Send(ctx, Event{OccurrenceID: a.OccurrenceID, ActivityExternalRef: a.ActivityExternalRef, TypeKey: a.TypeKey,
		DueAt: a.DueAt, SubjectRef: a.SubjectRef, EventID: EventIDRound(a.OccurrenceID, a.Round), NotifyAt: a.NotifyAt})
	if err == nil {
		if _, err := w.svc.MarkDispatchedRound(ctx, a.OccurrenceID, a.Round); err != nil {
			return err // the job retries; the receiver ignores the repeated eventId
		}
		return nil
	}
	var se *SendError
	permanent := errors.As(err, &se) && se.Permanent
	if permanent || job.Attempt >= job.MaxAttempts {
		if merr := w.svc.MarkFailed(ctx, a.OccurrenceID); merr != nil {
			return merr
		}
		// ALERT: an occurrence was not delivered. Monitoring pages on this event name.
		w.log.Error("dispatch.failed", "occurrenceId", a.OccurrenceID, "attempts", job.Attempt, "permanent", permanent, "reason", err.Error())
		if permanent {
			return river.JobCancel(err)
		}
		return err
	}
	w.log.Warn("dispatch.retry", "occurrenceId", a.OccurrenceID, "attempt", job.Attempt, "reason", err.Error())
	return err
}

// NextRetry doubles the delay each attempt, capped.
func (w *DeliverWorker) NextRetry(job *river.Job[DeliverArgs]) time.Time {
	d := w.cfg.RetryBase
	for i := 1; i < job.Attempt && d < w.cfg.RetryMax; i++ {
		d *= 2
	}
	if d > w.cfg.RetryMax {
		d = w.cfg.RetryMax
	}
	return time.Now().Add(d)
}

// MaintenanceWorker extends the horizon, expires old replay keys and marks unacknowledged doses missed.
type MaintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	svc *sched.Service
	cfg Config
	log *slog.Logger
}

// Work runs one maintenance pass.
func (w *MaintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	acts, occ, err := w.svc.GenerateDue(ctx)
	if err != nil {
		w.log.Error("generate.failed", "activities", acts, "occurrences", occ) // alert
		return err
	}
	if acts > 0 {
		w.log.Info("generate.extended", "activities", acts, "occurrences", occ)
	}
	if _, err := w.svc.ExpireIdempotency(ctx); err != nil {
		return err
	}
	n, err := w.svc.MarkMissed(ctx, w.cfg.MissedAfter)
	if err != nil {
		return err
	}
	if n > 0 {
		w.log.Info("occurrences.missed", "count", n)
	}
	return nil
}

// Runtime owns the River client and the due-scanner.
type Runtime struct {
	pool   *pgxpool.Pool
	svc    *sched.Service
	client *river.Client[pgx.Tx]
	cfg    Config
	log    *slog.Logger

	stop   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds the runtime. The pool must use the scheduler login role.
func New(pool *pgxpool.Pool, svc *sched.Service, sender *Sender, cfg Config, log *slog.Logger) (*Runtime, error) {
	cfg = cfg.Defaults()
	workers := river.NewWorkers()
	river.AddWorker(workers, &DeliverWorker{svc: svc, sender: sender, cfg: cfg, log: log})
	river.AddWorker(workers, &MaintenanceWorker{svc: svc, cfg: cfg, log: log})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:  db.QueueSchema,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			QueueDispatch:    {MaxWorkers: cfg.Workers},
			QueueMaintenance: {MaxWorkers: 2},
		},
		MaxAttempts:                 cfg.MaxAttempts,
		JobTimeout:                  cfg.JobTimeout,
		CompletedJobRetentionPeriod: time.Hour,
		FetchPollInterval:           250 * time.Millisecond,
		FetchCooldown:               50 * time.Millisecond,
		Logger:                      log,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(cfg.MaintenanceEvery), func() (river.JobArgs, *river.InsertOpts) {
				return MaintenanceArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: max(time.Second, cfg.MaintenanceEvery/2)}}
			}, &river.PeriodicJobOpts{RunOnStart: true}),
		},
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{pool: pool, svc: svc, client: client, cfg: cfg, log: log, stop: make(chan struct{})}, nil
}

// Start starts the queue workers and the scanner.
func (r *Runtime) Start(ctx context.Context) error {
	if err := r.client.Start(ctx); err != nil {
		return err
	}
	r.wg.Add(1)
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go r.scanLoop(runCtx)
	return nil
}

// Stop stops the scanner and waits for running deliveries to finish.
func (r *Runtime) Stop(ctx context.Context) error {
	close(r.stop)
	r.wg.Wait()
	if r.cancel != nil {
		r.cancel()
	}
	return r.client.Stop(ctx)
}

func (r *Runtime) scanLoop(ctx context.Context) {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.ScanInterval)
	defer t.Stop()
	lastExpire := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
		}
		if _, err := r.Scan(ctx); err != nil {
			r.log.Error("scan.failed", "reason", err.Error())
		}
		if time.Since(lastExpire) > 10*time.Second {
			lastExpire = time.Now()
			if n, err := r.svc.ExpireStale(ctx, r.svc.Clock(), r.cfg.MaxLateness); err != nil {
				r.log.Error("scan.expire_failed", "reason", err.Error())
			} else if n > 0 {
				// ALERT: reminders were not sent because the scheduler was down or far behind.
				r.log.Error("scan.expired_stale", "count", n)
			}
		}
	}
}

// Scan claims every due occurrence in batches and queues one delivery job for each, in the same transaction
// as the claim. It returns how many were queued.
func (r *Runtime) Scan(ctx context.Context) (int, error) {
	total := 0
	for {
		var n int
		err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
			due, err := r.svc.ClaimDue(ctx, tx, r.svc.Clock().UTC(), r.cfg.ScanBatch)
			if err != nil {
				return err
			}
			n = len(due)
			if n == 0 {
				return nil
			}
			params := make([]river.InsertManyParams, n)
			for i, d := range due {
				params[i] = river.InsertManyParams{
					Args:       DeliverArgs{OccurrenceID: d.OccurrenceID, ActivityExternalRef: d.ExternalRef, TypeKey: d.TypeKey, DueAt: d.DueAt, SubjectRef: d.SubjectRef, Round: d.Round, NotifyAt: d.NotifyAt},
					InsertOpts: &river.InsertOpts{MaxAttempts: r.cfg.MaxAttempts},
				}
			}
			_, err = r.client.InsertManyTx(ctx, tx, params)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < r.cfg.ScanBatch {
			return total, nil
		}
	}
}
