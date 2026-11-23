// Package itest holds scheduler-core integration tests (service, API, dispatch, contract, S-068). The harness is real PostgreSQL in a container (same image as
// the backend, decision D-05), migrations applied once into a template database, and a fresh clone per test.
// The application connects as its own login role, a member of mugizh_scheduler, exactly as in production, so a
// grant mistake fails a test. Synthetic data only (CLAUDE.md section 5.3).
//
// TestMain calls Main so the container is removed afterwards. It lives in _test.go files because the dependency
// allow-list (D-13) permits testcontainers only there.
package itest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/migrations"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
)

// Image is the database image used by tests.
const Image = "postgis/postgis:17-3.5"

// AppUser is the login role of the application.
const AppUser = "mugizh_app_scheduler"

var (
	startOnce sync.Once
	ctr       *postgres.PostgresContainer
	ownerURL  *url.URL
	startErr  error

	mu       sync.Mutex
	template string
	appPW    string
)

// Main runs the tests and removes the container afterwards.
func Main(m interface{ Run() int }) {
	code := m.Run()
	if ctr != nil {
		_ = ctr.Terminate(context.Background())
	}
	os.Exit(code)
}

func start() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, startErr = postgres.Run(ctx, Image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("owner"),
		postgres.WithPassword(randHex(12)),
		postgres.BasicWaitStrategies(),
	)
	if startErr != nil {
		return
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		startErr = err
		return
	}
	ownerURL, startErr = url.Parse(dsn)
}

// Env is one test's database.
type Env struct {
	Name   string        // database name
	Owner  *pgxpool.Pool // superuser pool: assertions only, never handed to application code
	App    *pgxpool.Pool // the application's pool (least-privilege login role)
	AppDSN string
}

// New returns a fresh database cloned from the migrated template.
func New(t testing.TB) *Env { return NewWithConns(t, 8) }

// NewWithConns is New with a chosen application pool size.
func NewWithConns(t testing.TB, maxConns int32) *Env {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	startOnce.Do(start)
	if startErr != nil {
		t.Fatalf("testkit: start database container: %v", startErr)
	}
	ctx := context.Background()
	tmpl, err := ensureTemplate(ctx)
	if err != nil {
		t.Fatalf("testkit: template: %v", err)
	}
	name := "t_" + randHex(8)
	maint, err := pgxpool.New(ctx, ownerURL.String())
	if err != nil {
		t.Fatalf("testkit: maintenance pool: %v", err)
	}
	defer maint.Close()
	if _, err := maint.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, tmpl)); err != nil {
		t.Fatalf("testkit: create db: %v", err)
	}
	env := &Env{Name: name}
	env.Owner, err = pgxpool.New(ctx, dsnFor(ownerURL.User.Username(), ownerPW(), name))
	if err != nil {
		t.Fatalf("testkit: owner pool: %v", err)
	}
	env.AppDSN = dsnFor(AppUser, appPW, name)
	env.App, err = db.Open(ctx, env.AppDSN, db.Options{MaxConns: maxConns, SearchPath: db.Schema})
	if err != nil {
		t.Fatalf("testkit: app pool: %v", err)
	}
	t.Cleanup(func() {
		env.App.Close()
		env.Owner.Close()
		c, err := pgxpool.New(context.Background(), ownerURL.String())
		if err == nil {
			_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
			c.Close()
		}
	})
	return env
}

func ensureTemplate(ctx context.Context) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if template != "" {
		return template, nil
	}
	name := "tmpl_" + randHex(6)
	maint, err := pgxpool.New(ctx, ownerURL.String())
	if err != nil {
		return "", err
	}
	defer maint.Close()
	if _, err := maint.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		return "", err
	}
	owner, err := pgxpool.New(ctx, dsnFor(ownerURL.User.Username(), ownerPW(), name))
	if err != nil {
		return "", err
	}
	defer owner.Close()
	if err := migrations.Up(ctx, owner); err != nil {
		return "", err
	}
	appPW = randHex(12)
	if _, err := owner.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE %s`, AppUser, appPW, db.GroupRole)); err != nil {
		return "", fmt.Errorf("create login: %w", err)
	}
	template = name
	return name, nil
}

func dsnFor(user, pass, dbname string) string {
	u := *ownerURL
	u.User = url.UserPassword(user, pass)
	u.Path = "/" + dbname
	return u.String()
}

func ownerPW() string { p, _ := ownerURL.User.Password(); return p }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func TestMain(m *testing.M) { Main(m) }
