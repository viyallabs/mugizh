// Package migrations holds scheduler-core's embedded SQL migrations and runs them, followed by River's own
// migrations in the scheduler_queue schema. Run with the migration owner's pool, never the application role.
package migrations

import (
	"context"
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/migrate"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// Source returns the scheduler schema migrations.
func Source() migrate.Source {
	sub, err := fs.Sub(sqlFS, "sql")
	if err != nil {
		panic(err)
	}
	return migrate.Source{Module: "scheduler", Schema: db.Schema, FS: sub}
}

// Up applies the scheduler migrations and then River's migrations.
func Up(ctx context.Context, owner *pgxpool.Pool) error {
	if err := migrate.Up(ctx, owner, []migrate.Source{Source()}); err != nil {
		return err
	}
	m, err := rivermigrate.New(riverpgxv5.New(owner), &rivermigrate.Config{Schema: db.QueueSchema})
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}
