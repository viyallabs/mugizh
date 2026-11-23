// Package db holds the PostgreSQL connection pools. Each module connects with its own
// role, which has rights only on its own schema plus INSERT on audit.audit_outbox
// (module rule B2). There is no shared "superuser" pool in application code.
package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Modules lists every schema-owning module of the Core API (module map, S-036).
// scheduler-core has its own database and is not listed (B10).
var Modules = []string{
	"admin", "audit", "booking", "clinical", "consult", "directory", "identity",
	"notifications", "payments", "platform", "records", "verification",
}

// RolePrefix namespaces database roles, which are cluster-wide in PostgreSQL.
const RolePrefix = "mugizh_"

// GroupRole is the NOLOGIN role that holds a module's privileges.
func GroupRole(module string) string { return RolePrefix + "mod_" + module }

// Pools maps module name to its pool.
type Pools struct{ m map[string]*pgxpool.Pool }

// NewPools wraps already-open pools (used by tests and main).
func NewPools(m map[string]*pgxpool.Pool) *Pools { return &Pools{m: m} }

// For returns the module's pool. It panics if the module has no pool, because that is a
// wiring bug that must fail at start-up, never at request time.
func (p *Pools) For(module string) *pgxpool.Pool {
	pool, ok := p.m[module]
	if !ok {
		panic("db: no pool for module " + module)
	}
	return pool
}

// Close closes every pool.
func (p *Pools) Close() {
	for _, pool := range p.m {
		pool.Close()
	}
}

// Names returns the modules that have pools, sorted.
func (p *Pools) Names() []string {
	out := make([]string, 0, len(p.m))
	for k := range p.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Options configures a pool.
type Options struct {
	MaxConns   int32
	RequireTLS bool
	// SearchPath is the module schema, so module SQL can use unqualified table names.
	SearchPath string
}

// Open connects one pool. TLS is required outside local and test environments
// (encryption-design §1: the database rejects non-TLS connections); Open refuses
// sslmode=disable when RequireTLS is set.
func Open(ctx context.Context, dsn string, o Options) (*pgxpool.Pool, error) {
	maxConns, requireTLS := o.MaxConns, o.RequireTLS
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	if requireTLS && cfg.ConnConfig.TLSConfig == nil {
		return nil, errors.New("db: TLS is required in this environment (sslmode must not be disable)")
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "mugizh-backend"
	// Statement timeout guards against runaway queries holding locks.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	if o.SearchPath != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = o.SearchPath + ", public"
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

// InTx runs fn in a transaction and commits if fn returns nil. Serialization failures
// and deadlocks are retried up to 3 times; fn must therefore be safe to re-run (no
// external side effects inside: use the outbox).
func InTx(ctx context.Context, pool TxRunner, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = inTxOnce(ctx, pool, opts, fn)
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

func inTxOnce(ctx context.Context, pool TxRunner, opts pgx.TxOptions, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, opts)
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

// IsUniqueViolation reports a unique-constraint error, optionally for one constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return constraint == "" || pg.ConstraintName == constraint
	}
	return false
}

// IsInsufficientPrivilege reports error 42501 (used by tests proving B2 and append-only grants).
func IsInsufficientPrivilege(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42501"
}
