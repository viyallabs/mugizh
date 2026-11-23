package itest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/dispatch"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/signing"
)

// S-068 prototype: reminder dispatch at the scale of 1,000,000 a day with 99.9% dispatched within 60 s, and the
// proof that River on PostgreSQL (ADR-07) can carry it.
//
// Traffic model (docs/test/load-test-plan.md L7): 1M a day with peaks at three times a day. Taking 25% of the
// day's volume in a 10-minute peak gives 250,000 / 600 s = about 417 per second (the steady-peak phase). The
// burst phase is a stress test far beyond the model: everything due within a few seconds.
//
// The test seeds occurrences directly in the database (generation is measured elsewhere), starts the real
// runtime (scanner + River workers + HMAC-signing sender) against an in-process fake Core API that verifies every
// signature, and measures lateness = dispatched_at - due_at from the database.
//
// Run the full prototype with:  S068=1 S068_N=50000 S068_WINDOW=120 go test ./internal/itest -run S068 -v -timeout 30m
func TestS068Smoke(t *testing.T) {
	runS068(t, 3000, 3, 100, 20)
}

func TestS068Prototype(t *testing.T) {
	if os.Getenv("S068") != "1" {
		t.Skip("set S068=1 to run the S-068 load prototype")
	}
	runS068(t, envInt("S068_N", 100000), envInt("S068_WINDOW", 10), envInt("S068_WORKERS", 200), envInt("S068_DBCONNS", 40))
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

type s068Result struct {
	n                       int
	windowSec               float64
	p50, p90, p99, p999, mx float64
	within60                float64
	throughput              float64
	elapsed                 float64
}

func runS068(t *testing.T, n, windowSec, workers, dbConns int) s068Result {
	ctx := context.Background()
	env := NewWithConns(t, int32(dbConns))
	svc := sched.NewService(env.App)

	// Seed 1000 subjects with one activity each, then n occurrences spread evenly over the window.
	if _, err := env.Owner.Exec(ctx, `INSERT INTO scheduler.subjects (subject_ref) SELECT gen_random_uuid() FROM generate_series(1, 1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Owner.Exec(ctx, `INSERT INTO scheduler.activities (id, subject_ref, app_ref, external_ref, type_key, rrule, time_zone, starts_at, generated_through)
		SELECT gen_random_uuid(), subject_ref, 'core', 'rx-' || row_number() OVER (), 'med.dose', 'FREQ=DAILY', 'UTC', now(), now() + interval '14 days' FROM scheduler.subjects`); err != nil {
		t.Fatal(err)
	}
	lead := 4.0 // seconds until the first occurrence is due
	if _, err := env.Owner.Exec(ctx, `
		WITH acts AS (SELECT id, subject_ref, row_number() OVER () - 1 AS rn FROM scheduler.activities)
		INSERT INTO scheduler.occurrences (id, activity_id, subject_ref, due_at)
		SELECT gen_random_uuid(), a.id, a.subject_ref, now() + make_interval(secs => $1 + g * ($2::float8 / $3))
		FROM generate_series(0, $3 - 1) AS g JOIN acts a ON a.rn = g % 1000`, lead, float64(windowSec), n); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Owner.Exec(ctx, `ANALYZE scheduler.occurrences`); err != nil {
		t.Fatal(err)
	}

	var received atomic.Int64
	var badSig atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if signing.Verify(testSecret, r.Header.Get(signing.HeaderTimestamp), body, r.Header.Get(signing.HeaderSignature), time.Now(), time.Minute) != nil {
			badSig.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	startRuntime(t, env, svc, srv.URL, testSecret, dispatch.Config{Workers: workers, ScanInterval: 250 * time.Millisecond, ScanBatch: 1000})

	started := time.Now()
	deadline := started.Add(time.Duration(windowSec)*time.Second + 5*time.Minute)
	for {
		var done int
		if err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM scheduler.occurrences WHERE state = 'dispatched'`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d dispatched before the deadline (received %d, bad signatures %d)", done, n, received.Load(), badSig.Load())
		}
		time.Sleep(500 * time.Millisecond)
	}
	if received.Load() < int64(n) || badSig.Load() != 0 {
		t.Fatalf("received %d of %d, bad signatures %d", received.Load(), n, badSig.Load())
	}

	var r s068Result
	r.n, r.windowSec = n, float64(windowSec)
	err := env.Owner.QueryRow(ctx, `
		SELECT percentile_cont(0.5)   WITHIN GROUP (ORDER BY lat),
		       percentile_cont(0.9)   WITHIN GROUP (ORDER BY lat),
		       percentile_cont(0.99)  WITHIN GROUP (ORDER BY lat),
		       percentile_cont(0.999) WITHIN GROUP (ORDER BY lat),
		       max(lat),
		       100.0 * count(*) FILTER (WHERE lat <= 60) / count(*),
		       count(*) / GREATEST(extract(epoch FROM max(dispatched_at) - min(due_at)), 1)
		FROM (SELECT extract(epoch FROM dispatched_at - due_at) AS lat, dispatched_at, due_at FROM scheduler.occurrences WHERE state = 'dispatched') x`).
		Scan(&r.p50, &r.p90, &r.p99, &r.p999, &r.mx, &r.within60, &r.throughput)
	if err != nil {
		t.Fatal(err)
	}
	r.elapsed = time.Since(started).Seconds()
	t.Logf("S-068 RESULT n=%d window=%.0fs workers=%d dbconns=%d | lateness p50=%.2fs p90=%.2fs p99=%.2fs p99.9=%.2fs max=%.2fs | within 60s: %.3f%% | throughput %.0f/s | wall %.0fs",
		r.n, r.windowSec, workers, dbConns, r.p50, r.p90, r.p99, r.p999, r.mx, r.within60, r.throughput, r.elapsed)
	if r.within60 < 99.9 {
		t.Errorf("only %.3f%% dispatched within 60 s (target 99.9%%)", r.within60)
	}
	return r
}
