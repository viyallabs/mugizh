package outbox

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// QueueSchema holds River's tables (role mugizh_queue).
const QueueSchema = "queue"

// MigrateQueue applies River's own migrations in the queue schema. Run by the migration
// owner, after the bootstrap migration created the schema and default grants.
func MigrateQueue(ctx context.Context, owner *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(owner), &rivermigrate.Config{Schema: QueueSchema})
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// NewClient builds a River client on the queue role's pool. With workers == nil it is
// insert-only (used by the relay in the API process).
func NewClient(pool *pgxpool.Pool, workers *river.Workers, queues map[string]river.QueueConfig, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	cfg := &river.Config{Schema: QueueSchema, Logger: log}
	if workers != nil {
		cfg.Workers = workers
		cfg.Queues = queues
	}
	return river.NewClient(riverpgxv5.New(pool), cfg)
}

// RegisterDelivery adds the event delivery worker to workers.
func RegisterDelivery(workers *river.Workers, bus *Bus) {
	river.AddWorker(workers, &Worker{Bus: bus})
}
