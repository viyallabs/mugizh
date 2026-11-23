// Package db opens scheduler-core's PostgreSQL pool. scheduler-core has its own database and its own
// login role (a member of the NOLOGIN group role mugizh_scheduler, decision D-02); there is no shared
// superuser pool in application code.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GroupRole is the NOLOGIN role that holds scheduler-core's privileges.
const GroupRole = "mugizh_scheduler"

// Schema names. River's tables live in QueueSchema.
const (
	Schema      = "scheduler"
	QueueSchema = "scheduler_queue"
)

// Options configures the pool.
type Options struct {
	MaxConns   int32
	RequireTLS bool
	// SearchPath puts the schema first so SQL may use unqualified names where convenient.
	SearchPath string
}

// Open connects one pool. Outside local and test environments TLS is required.
func Open(ctx context.Context, dsn string, o Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("db: invalid connection string")
	}
	if o.RequireTLS && cfg.ConnConfig.TLSConfig == nil {
		return nil, errors.New("db: TLS is required in this environment (sslmode must not be disable)")
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "mugizh-scheduler-core"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	if o.SearchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = o.SearchPath
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// TxRunner is the subset of pgxpool.Pool that InTx needs.
type TxRunner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// InTx runs fn in a transaction and commits if fn returns nil. Serialization failures and deadlocks
// are retried up to 3 times; fn must be safe to re-run (no external side effects inside).
func InTx(ctx context.Context, pool TxRunner, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = inTxOnce(ctx, pool, fn)
		if err == nil || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 15 * time.Millisecond):
		}
	}
	return err
}

func inTxOnce(ctx context.Context, pool TxRunner, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func retryable(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "40001" || pg.Code == "40P01"
	}
	return false
}

// IsInsufficientPrivilege reports error 42501 (used by tests proving least privilege).
func IsInsufficientPrivilege(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42501"
}
