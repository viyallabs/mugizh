// Package testkit is the integration-test harness (S-101): real PostgreSQL + PostGIS in a
// container (same major version as staging, decision D-05), migrations applied once into
// a template database, and a fresh clone per test. Each module connects with its own
// login role, exactly as in production, so grant mistakes fail tests (B2).
//
// Synthetic data only (CLAUDE.md §5.3).
//
// A package that uses testkit must call testkit.Main from TestMain so the container is
// removed when the tests finish.
package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

// Image is the database image used by tests and the local stack.
const Image = "postgis/postgis:17-3.5"

var (
	startOnce sync.Once
	ctr       *postgres.PostgresContainer
	ownerURL  *url.URL // points at the maintenance database
	startErr  error

	mu        sync.Mutex
	templates = map[string]string{}
	loginPw   = map[string]string{}
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
	Name  string        // database name
	Owner *pgxpool.Pool // superuser pool: test assertions only, never handed to app code
	Pools *db.Pools     // one pool per module login role
	urls  map[string]string
}

// URL returns the DSN for a module login role (for code that opens its own pool).
func (e *Env) URL(module string) string { return e.urls[module] }

// Spec says what a test database needs.
type Spec struct {
	Key     string           // template cache key; defaults to the source module names
	Sources []migrate.Source // applied in order (bootstrap first)
	Logins  []string         // modules that get login roles and pools (default: db.Modules)
	// NoModuleLogins skips the default module logins (for the key store database).
	NoModuleLogins bool
	// Extra group roles to create logins for, e.g. "queue" → mugizh_queue.
	ExtraLogins map[string]string
}

// New returns a fresh database for the test, cloned from a migrated template.
func New(t testing.TB, spec Spec) *Env {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	startOnce.Do(start)
	if startErr != nil {
		t.Fatalf("testkit: start database container: %v", startErr)
	}
	ctx := context.Background()
	if len(spec.Logins) == 0 && !spec.NoModuleLogins {
		spec.Logins = db.Modules
	}
	key := spec.Key
	if key == "" {
		names := make([]string, 0, len(spec.Sources))
		for _, s := range spec.Sources {
			names = append(names, s.Module)
		}
		key = strings.Join(names, ",")
	}

	tmpl, err := template(ctx, key, spec)
	if err != nil {
		t.Fatalf("testkit: template: %v", err)
	}

	name := "t_" + randHex(8)
	maint, err := pgxpool.New(ctx, ownerURL.String())
	if err != nil {
		t.Fatalf("testkit: maint pool: %v", err)
	}
	defer maint.Close()
	if _, err := maint.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, tmpl)); err != nil {
		t.Fatalf("testkit: create db: %v", err)
	}

	env := &Env{Name: name, urls: map[string]string{}}
	env.Owner, err = pgxpool.New(ctx, dsnFor(ownerURL.User.Username(), pw(ownerURL), name))
	if err != nil {
		t.Fatalf("testkit: owner pool: %v", err)
	}
	pools := map[string]*pgxpool.Pool{}
	logins := map[string]string{}
	for _, m := range spec.Logins {
		logins[m] = db.GroupRole(m)
	}
	for m, g := range spec.ExtraLogins {
		logins[m] = g
	}
	for m := range logins {
		user := db.RolePrefix + "app_" + m
		u := dsnFor(user, loginPw[user], name)
		env.urls[m] = u
		// Under a full parallel run every package starts its own container, and a connect can
		// time out while the Docker VM is busy. Retry briefly before failing the test.
		var p *pgxpool.Pool
		var err error
		for attempt := 0; attempt < 6; attempt++ {
			if p, err = db.Open(ctx, u, db.Options{MaxConns: 4, SearchPath: searchPath(m)}); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("testkit: pool %s: %v", m, err)
		}
		pools[m] = p
	}
	env.Pools = db.NewPools(pools)
	t.Cleanup(func() {
		env.Pools.Close()
		env.Owner.Close()
		c, err := pgxpool.New(context.Background(), ownerURL.String())
		if err == nil {
			_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
			c.Close()
		}
	})
	return env
}

func template(ctx context.Context, key string, spec Spec) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if n, ok := templates[key]; ok {
		return n, nil
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
	owner, err := pgxpool.New(ctx, dsnFor(ownerURL.User.Username(), pw(ownerURL), name))
	if err != nil {
		return "", err
	}
	if err := migrate.Up(ctx, owner, spec.Sources); err != nil {
		owner.Close()
		return "", err
	}
	// Login roles are cluster-wide; create each once per process.
	groups := map[string]string{}
	for _, m := range spec.Logins {
		groups[m] = db.GroupRole(m)
	}
	for m, g := range spec.ExtraLogins {
		groups[m] = g
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, m := range keys {
		user := db.RolePrefix + "app_" + m
		if _, ok := loginPw[user]; ok {
			continue
		}
		p := randHex(12)
		if _, err := owner.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE %s`, user, p, groups[m])); err != nil {
			owner.Close()
			return "", fmt.Errorf("create login %s: %w", user, err)
		}
		loginPw[user] = p
	}
	owner.Close()
	templates[key] = name
	return name, nil
}

func dsnFor(user, pass, dbname string) string {
	u := *ownerURL
	u.User = url.UserPassword(user, pass)
	u.Path = "/" + dbname
	return u.String()
}

func pw(u *url.URL) string { p, _ := u.User.Password(); return p }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// searchPath maps a login name to its schema ("queue" for the River role).
func searchPath(m string) string { return m }
