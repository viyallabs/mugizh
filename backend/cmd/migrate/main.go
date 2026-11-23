// Command migrate applies database migrations as the migration owner (S-094): the Core
// database (bootstrap, every module, River's queue tables) and the separate key store.
// Usage: migrate up | migrate down <module> <n>
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: migrate up | down <module> <n>")
	}
	core, err := pgxpool.New(ctx, os.Getenv("MUGIZH_DB_URL_OWNER"))
	if err != nil {
		return err
	}
	defer core.Close()
	switch os.Args[1] {
	case "up":
		if err := migrate.Up(ctx, core, app.MigrationSources()); err != nil {
			return err
		}
		if err := outbox.MigrateQueue(ctx, core); err != nil {
			return fmt.Errorf("queue: %w", err)
		}
		ks, err := pgxpool.New(ctx, os.Getenv("MUGIZH_KEYSTORE_OWNER_URL"))
		if err != nil {
			return err
		}
		defer ks.Close()
		if err := migrate.Up(ctx, ks, []migrate.Source{crypto.KeystoreSource()}); err != nil {
			return fmt.Errorf("keystore: %w", err)
		}
		fmt.Println("migrations applied")
	case "down":
		if len(os.Args) != 4 {
			return fmt.Errorf("usage: migrate down <module> <n>")
		}
		n, err := strconv.Atoi(os.Args[3])
		if err != nil {
			return err
		}
		for _, s := range app.MigrationSources() {
			if s.Module == os.Args[2] {
				return migrate.Down(ctx, core, s, n)
			}
		}
		return fmt.Errorf("unknown module %q", os.Args[2])
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
	return nil
}
