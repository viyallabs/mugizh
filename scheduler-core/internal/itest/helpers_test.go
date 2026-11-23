package itest

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/api"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

// clock is a settable clock for the service under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// wallIn parses a local wall-clock time in a zone (test helper).
func wallIn(t testing.TB, zone, wall string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02T15:04:05", wall, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func quietLog() *slog.Logger { return logging.New(io.Discard, slog.LevelError) }

// newService returns a service on a fresh database with a fake clock.
func newService(t testing.TB, now time.Time) (*sched.Service, *Env, *clock) {
	t.Helper()
	env := New(t)
	c := &clock{t: now}
	svc := sched.NewService(env.App)
	svc.Clock = c.Now
	return svc, env, c
}

func create(t testing.TB, svc *sched.Service, in sched.CreateInput) *sched.Activity {
	t.Helper()
	a, _, err := svc.Create(context.Background(), uuid.New(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return a
}

// listAll returns every occurrence of a subject in order.
func listAll(t testing.TB, svc *sched.Service, subject uuid.UUID) []*sched.Occurrence {
	t.Helper()
	var out []*sched.Occurrence
	cursor := ""
	for {
		items, next, err := svc.List(context.Background(), sched.ListParams{SubjectRef: subject, Limit: 100, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, items...)
		if next == "" {
			return out
		}
		cursor = next
	}
}

func inState(occ []*sched.Occurrence, state string) []*sched.Occurrence {
	var out []*sched.Occurrence
	for _, o := range occ {
		if o.State == state {
			out = append(out, o)
		}
	}
	return out
}

func dues(occ []*sched.Occurrence) []string {
	out := make([]string, len(occ))
	for i, o := range occ {
		out[i] = o.DueAt.UTC().Format(time.RFC3339)
	}
	return out
}

const testToken = "synthetic-service-token-for-tests"

// apiServer starts the HTTP API on a test server.
func apiServer(t testing.TB, svc *sched.Service) *httptest.Server {
	t.Helper()
	h, err := hex.DecodeString(api.HashToken(testToken))
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Svc: svc, Log: quietLog(), TokenHashes: [][]byte{h}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t testing.TB, method, url string, headers map[string]string, body string) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}
