package itest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/dispatch"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/signing"
)

var testSecret = []byte("synthetic-hmac-secret-0123456789-abcdef")

type received struct {
	raw       []byte
	event     dispatch.Event
	headerEvt string
	idemKey   string
}

// receiver is a fake Core API: it verifies the HMAC signature, de-duplicates by eventId and can be told to fail.
type receiver struct {
	srv       *httptest.Server
	mu        sync.Mutex
	got       []received
	badSig    int
	calls     atomic.Int64
	failFirst int64 // answer 500 to this many first calls
	status    int   // when non-zero, always answer with this status
}

func newReceiver(t testing.TB) *receiver {
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		n := r.calls.Add(1)
		if err := signing.Verify(testSecret, req.Header.Get(signing.HeaderTimestamp), body, req.Header.Get(signing.HeaderSignature), time.Now(), 5*time.Minute); err != nil {
			r.mu.Lock()
			r.badSig++
			r.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.status != 0 {
			w.WriteHeader(r.status)
			return
		}
		if n <= r.failFirst {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var ev dispatch.Event
		_ = json.Unmarshal(body, &ev)
		r.mu.Lock()
		r.got = append(r.got, received{raw: body, event: ev, headerEvt: req.Header.Get(signing.HeaderEventID), idemKey: req.Header.Get("Idempotency-Key")})
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) events() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// startRuntime runs the queue against the receiver with the real clock.
func startRuntime(t testing.TB, env *Env, svc *sched.Service, url string, secret []byte, cfg dispatch.Config) *lockedBuf {
	t.Helper()
	buf := &lockedBuf{}
	log := logging.New(buf, slog.LevelInfo)
	rt, err := dispatch.New(env.App, svc, dispatch.NewSender(url, secret, 5*time.Second), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	return buf
}

func realService(t testing.TB) (*sched.Service, *Env) {
	env := New(t)
	return sched.NewService(env.App), env
}

// dueSoon creates a one-off activity whose only occurrence is due in `in`.
func dueSoon(t testing.TB, svc *sched.Service, in time.Duration) (*sched.Activity, *sched.Occurrence) {
	t.Helper()
	subject := uuid.New()
	start := time.Now().Add(in).UTC().Truncate(time.Second).Add(time.Second)
	a := create(t, svc, sched.CreateInput{SubjectRef: subject, AppRef: "core", ExternalRef: "rx-item-" + uuid.NewString()[:8], TypeKey: "med.dose",
		RRule: "FREQ=DAILY;COUNT=1", TimeZone: "UTC", StartsAt: start})
	occ := listAll(t, svc, subject)
	if len(occ) != 1 {
		t.Fatalf("expected one occurrence, got %d", len(occ))
	}
	return a, occ[0]
}

func waitState(t testing.TB, svc *sched.Service, id uuid.UUID, want string, within time.Duration) *sched.Occurrence {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		o, err := svc.GetOccurrence(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if o.State == want {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("occurrence is %q after %s, want %q", o.State, within, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDispatchDeliversOnceWithValidSignature(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond})
	a, occ := dueSoon(t, svc, 500*time.Millisecond)
	done := waitState(t, svc, occ.ID, sched.OccDispatched, 20*time.Second)
	time.Sleep(700 * time.Millisecond) // a duplicate delivery would show up now
	got := rcv.events()
	if len(got) != 1 || rcv.badSig != 0 {
		t.Fatalf("deliveries %d, bad signatures %d", len(got), rcv.badSig)
	}
	ev := got[0]
	if ev.event.OccurrenceID != occ.ID || ev.event.ActivityExternalRef != a.ExternalRef || ev.event.TypeKey != "med.dose" ||
		ev.event.SubjectRef != a.SubjectRef || !ev.event.DueAt.Equal(occ.DueAt) {
		t.Fatalf("payload %+v", ev.event)
	}
	if ev.event.EventID != dispatch.EventID(occ.ID) || ev.headerEvt != ev.event.EventID.String() || ev.idemKey != ev.event.EventID.String() {
		t.Fatal("eventId must be deterministic and repeated in the headers")
	}
	// The payload carries exactly these six fields and nothing else (K-ARC-02).
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(ev.raw, &keys); err != nil || len(keys) != 6 {
		t.Fatalf("payload keys %v", keys)
	}
	for _, k := range []string{"occurrenceId", "activityExternalRef", "typeKey", "dueAt", "subjectRef", "eventId"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if done.DispatchedAt == nil || done.DispatchedAt.Sub(occ.DueAt) > 5*time.Second || done.DispatchedAt.Before(occ.DueAt) {
		t.Fatalf("dispatched at %v for due %v", done.DispatchedAt, occ.DueAt)
	}
}

func TestDispatchRetriesOnServerError(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	rcv.failFirst = 2
	logs := startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond, RetryBase: 50 * time.Millisecond, MaxAttempts: 5})
	_, occ := dueSoon(t, svc, 300*time.Millisecond)
	waitState(t, svc, occ.ID, sched.OccDispatched, 60*time.Second)
	if n := rcv.calls.Load(); n != 3 {
		t.Fatalf("calls %d, want 3 (two 500s then success)", n)
	}
	if got := rcv.events(); len(got) != 1 || got[0].event.EventID != dispatch.EventID(occ.ID) {
		t.Fatalf("exactly one accepted event with the same eventId on every attempt: %+v", got)
	}
	if strings.Contains(logs.String(), "dispatch.failed") {
		t.Fatal("a recovered delivery is not a failure")
	}
}

func TestDispatchGivesUpAndAlerts(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	rcv.status = http.StatusInternalServerError
	logs := startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond, RetryBase: 50 * time.Millisecond, MaxAttempts: 2})
	_, occ := dueSoon(t, svc, 300*time.Millisecond)
	waitState(t, svc, occ.ID, sched.OccFailed, 60*time.Second)
	if n := rcv.calls.Load(); n != 2 {
		t.Fatalf("calls %d, want 2", n)
	}
	if out := logs.String(); !strings.Contains(out, `"msg":"dispatch.failed"`) || !strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("expected an ERROR dispatch.failed event, logs: %s", out)
	}
}

func TestDispatchPermanentClientErrorIsNotRetried(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	rcv.status = http.StatusBadRequest
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond, MaxAttempts: 5})
	_, occ := dueSoon(t, svc, 300*time.Millisecond)
	waitState(t, svc, occ.ID, sched.OccFailed, 20*time.Second)
	time.Sleep(time.Second)
	if n := rcv.calls.Load(); n != 1 {
		t.Fatalf("a 400 must not be retried: %d calls", n)
	}
}

func TestReceiverRejectsBadSignature(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	logs := startRuntime(t, env, svc, rcv.srv.URL, []byte("a-different-secret-that-the-receiver-does-not-know"), dispatch.Config{ScanInterval: 100 * time.Millisecond})
	_, occ := dueSoon(t, svc, 300*time.Millisecond)
	waitState(t, svc, occ.ID, sched.OccFailed, 20*time.Second)
	if len(rcv.events()) != 0 || rcv.badSig == 0 {
		t.Fatalf("receiver must reject: accepted %d, bad %d", len(rcv.events()), rcv.badSig)
	}
	if strings.Contains(logs.String(), "a-different-secret") || strings.Contains(logs.String(), string(testSecret)) {
		t.Fatal("the HMAC secret must never be logged")
	}
}

func TestDispatchSkipsCancelledAndAcknowledged(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond})
	_, o1 := dueSoon(t, svc, 2*time.Second)
	a2, o2 := dueSoon(t, svc, 2*time.Second)
	_, o3 := dueSoon(t, svc, 2*time.Second)
	if _, err := svc.Acknowledge(context.Background(), o1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelOccurrence(context.Background(), o2.ID); err != nil {
		t.Fatal(err)
	}
	_ = a2
	waitState(t, svc, o3.ID, sched.OccDispatched, 20*time.Second) // the untouched one goes out
	time.Sleep(time.Second)
	got := rcv.events()
	if len(got) != 1 || got[0].event.OccurrenceID != o3.ID {
		t.Fatalf("only the untouched occurrence may be delivered, got %d", len(got))
	}
}

func TestPausedActivityIsNotDispatched(t *testing.T) {
	svc, env := realService(t)
	rcv := newReceiver(t)
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond})
	a, occ := dueSoonRecurring(t, svc, 2*time.Second)
	if _, err := svc.Pause(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if len(rcv.events()) != 0 {
		t.Fatal("a paused activity must not dispatch")
	}
	if o, _ := svc.GetOccurrence(context.Background(), occ.ID); o.State != sched.OccCancelled {
		t.Fatalf("state %s", o.State)
	}
}

func TestStaleOccurrencesAreMissedNotSent(t *testing.T) {
	ctx := context.Background()
	svc, env, clk := newService(t, utc("2026-06-01T10:00:00Z"))
	rcv := newReceiver(t)
	rt, err := dispatch.New(env.App, svc, dispatch.NewSender(rcv.srv.URL, testSecret, time.Second), dispatch.Config{}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	subject := uuid.New()
	for _, hhmm := range []string{"2026-06-01T10:00:30", "2026-06-01T11:45:00"} {
		create(t, svc, sched.CreateInput{SubjectRef: subject, AppRef: "core", ExternalRef: "r-" + hhmm[11:13], TypeKey: "med.dose",
			RRule: "FREQ=DAILY;COUNT=1", TimeZone: "UTC", StartsAt: wallIn(t, "UTC", hhmm)})
	}
	clk.Set(utc("2026-06-01T12:00:00Z")) // 1h30m and 15m late; the limit is 1h
	n, err := svc.ExpireStale(ctx, clk.Now(), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	if q, err := rt.Scan(ctx); err != nil || q != 1 {
		t.Fatalf("the 15-minute-late occurrence is still queued: %d %v", q, err)
	}
	occ := listAll(t, svc, subject)
	if len(inState(occ, sched.OccMissed)) != 1 || len(inState(occ, sched.OccPending)) != 1 {
		t.Fatalf("states %v", occ)
	}
}

func TestEraseRemovesQueuedJobs(t *testing.T) {
	ctx := context.Background()
	svc, env, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	rt, err := dispatch.New(env.App, svc, dispatch.NewSender("http://127.0.0.1:1", testSecret, time.Second), dispatch.Config{}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	s1, s2 := uuid.New(), uuid.New()
	create(t, svc, baseInput(s1, "UTC", "FREQ=DAILY", utc("2026-06-01T08:00:00Z")))
	create(t, svc, baseInput(s2, "UTC", "FREQ=DAILY", utc("2026-06-01T08:00:00Z")))
	clk.Set(utc("2026-06-04T09:00:00Z"))
	if n, err := rt.Scan(ctx); err != nil || n != 8 {
		t.Fatalf("queued %d %v", n, err)
	}
	count := func(s uuid.UUID) int {
		var n int
		if err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM scheduler_queue.river_job WHERE kind = $1 AND args->>'subjectRef' = $2`, sched.DeliverKind, s.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(s1) != 4 || count(s2) != 4 {
		t.Fatalf("jobs %d %d", count(s1), count(s2))
	}
	rep, err := svc.Erase(ctx, s1)
	if err != nil || rep.QueueJobs != 4 {
		t.Fatalf("report %+v %v", rep, err)
	}
	if count(s1) != 0 || count(s2) != 4 {
		t.Fatalf("after erase: %d %d", count(s1), count(s2))
	}
}

func TestPeriodicJobExtendsHorizon(t *testing.T) {
	ctx := context.Background()
	svc, env := realService(t)
	rcv := newReceiver(t)
	logs := startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond, MaintenanceEvery: time.Second})
	a, _ := dueSoonRecurring(t, svc, time.Hour)
	// Make the horizon look 10 days short; the maintenance job must restore it.
	if _, err := env.Owner.Exec(ctx, `UPDATE scheduler.activities SET generated_through = now() + interval '4 days', state = 'active' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, _ := svc.Get(ctx, a.ID)
		if got.GeneratedThrough.After(time.Now().Add(13 * 24 * time.Hour)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("horizon not extended by the periodic job: %v (version %s) logs: %s", got.GeneratedThrough, strconv.Itoa(got.Version), logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// dueSoonRecurring is dueSoon with an open-ended daily rule, so the activity stays active.
func dueSoonRecurring(t testing.TB, svc *sched.Service, in time.Duration) (*sched.Activity, *sched.Occurrence) {
	t.Helper()
	subject := uuid.New()
	start := time.Now().Add(in).UTC().Truncate(time.Second).Add(time.Second)
	a := create(t, svc, sched.CreateInput{SubjectRef: subject, AppRef: "core", ExternalRef: "rx-item-" + uuid.NewString()[:8], TypeKey: "med.dose",
		RRule: "FREQ=DAILY", TimeZone: "UTC", StartsAt: start})
	return a, listAll(t, svc, subject)[0]
}
