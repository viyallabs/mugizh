// Package migrate applies scheduler-core's database migrations (copied pattern from the backend, decision D-08; scheduler-core has its own database).
//
// Rules:
//   - Each module has its own folder of NNNN_name.up.sql / NNNN_name.down.sql files and
//     touches only its own schema (B1; checked by archtest).
//   - An applied migration's checksum is stored; if the file later changes, the runner
//     refuses to start (no silent edits of history).
//   - Every migration runs in its own transaction with search_path set to the module
//     schema. A PostgreSQL advisory lock stops two runners at once.
//   - Down files exist for rollback (S-094); production uses expand-and-contract, so a
//     down step is a tested escape hatch, not the normal path.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is one module's migrations.
type Source struct {
	Module string // module name, e.g. "identity"
	Schema string // schema the migrations run in (normally the module name)
	FS     fs.FS  // folder holding the .sql files at its root
}

type step struct {
	version  int
	name     string
	up, down string
	sum      string
}

var reFile = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

const lockKey = 7_311_002_026 // arbitrary constant for pg_advisory_lock

func load(src Source) ([]step, error) {
	entries, err := fs.ReadDir(src.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate %s: read dir: %w", src.Module, err)
	}
	byVer := map[int]*step{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := reFile.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate %s: bad file name %q", src.Module, e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		b, err := fs.ReadFile(src.FS, e.Name())
		if err != nil {
			return nil, err
		}
		s := byVer[v]
		if s == nil {
			s = &step{version: v, name: m[2]}
			byVer[v] = s
		} else if s.name != m[2] {
			return nil, fmt.Errorf("migrate %s: version %04d has two names", src.Module, v)
		}
		if m[3] == "up" {
			s.up = string(b)
		} else {
			s.down = string(b)
		}
	}
	out := make([]step, 0, len(byVer))
	for _, s := range byVer {
		if s.up == "" || s.down == "" {
			return nil, fmt.Errorf("migrate %s: %04d_%s needs both up and down files", src.Module, s.version, s.name)
		}
		h := sha256.Sum256([]byte(s.up))
		s.sum = hex.EncodeToString(h[:])
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

const bookkeeping = `
CREATE SCHEMA IF NOT EXISTS scheduler;
CREATE TABLE IF NOT EXISTS scheduler.schema_migrations (
  module     text        NOT NULL,
  version    int         NOT NULL,
  name       text        NOT NULL,
  checksum   text        NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (module, version)
);`

// Up applies all pending migrations of every source, in the order given.
func Up(ctx context.Context, pool *pgxpool.Pool, sources []Source) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey) }()
	if _, err := conn.Exec(ctx, bookkeeping); err != nil {
		return fmt.Errorf("migrate: bookkeeping: %w", err)
	}
	for _, src := range sources {
		if err := upOne(ctx, conn.Conn(), src); err != nil {
			return err
		}
	}
	return nil
}

func upOne(ctx context.Context, conn *pgx.Conn, src Source) error {
	steps, err := load(src)
	if err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM scheduler.schema_migrations WHERE module=$1`, src.Module)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			rows.Close()
			return err
		}
		applied[v] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range steps {
		if sum, ok := applied[s.version]; ok {
			if sum != s.sum {
				return fmt.Errorf("migrate %s: applied migration %04d_%s was edited (checksum mismatch); add a new migration instead", src.Module, s.version, s.name)
			}
			continue
		}
		if err := apply(ctx, conn, src, s.version, s.name, s.up, s.sum, true); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, conn *pgx.Conn, src Source, version int, name, sql, sum string, up bool) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if !validIdent(src.Schema) {
		return fmt.Errorf("migrate: invalid schema name %q", src.Schema)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s; SET LOCAL search_path TO %s, public", src.Schema, src.Schema)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		dir := "up"
		if !up {
			dir = "down"
		}
		return fmt.Errorf("migrate %s: %04d_%s %s: %w", src.Module, version, name, dir, err)
	}
	if up {
		_, err = tx.Exec(ctx, `INSERT INTO scheduler.schema_migrations(module,version,name,checksum) VALUES ($1,$2,$3,$4)`, src.Module, version, name, sum)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM scheduler.schema_migrations WHERE module=$1 AND version=$2`, src.Module, version)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Down rolls back the last n applied migrations of one module.
func Down(ctx context.Context, pool *pgxpool.Pool, src Source, n int) error {
	if n <= 0 {
		return errors.New("migrate: n must be positive")
	}
	steps, err := load(src)
	if err != nil {
		return err
	}
	byVer := map[int]step{}
	for _, s := range steps {
		byVer[s.version] = s
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey) }()
	rows, err := conn.Query(ctx, `SELECT version FROM scheduler.schema_migrations WHERE module=$1 ORDER BY version DESC LIMIT $2`, src.Module, n)
	if err != nil {
		return err
	}
	var vers []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		vers = append(vers, v)
	}
	rows.Close()
	for _, v := range vers {
		s, ok := byVer[v]
		if !ok {
			return fmt.Errorf("migrate %s: no files for applied version %04d", src.Module, v)
		}
		if err := apply(ctx, conn.Conn(), src, s.version, s.name, s.down, s.sum, false); err != nil {
			return err
		}
	}
	return nil
}

func validIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return !strings.HasPrefix(s, "pg_")
}
