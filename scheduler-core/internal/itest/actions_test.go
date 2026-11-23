package itest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/dispatch"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

// S-154: a patient marks an occurrence taken, skipped or snoozed; offline actions sync later.
// The decisions are in sched/actions.go (bounds, conflict rule, snooze rules).

func actionFixture(t testing.TB) (*sched.Service, *clock, uuid.UUID, *sched.Occurrence) {
	t.Helper()
	svc, _, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY", utc("2026-06-01T07:00:00Z")))
	occ := listAll(t, svc, subject)
	return svc, clk, subject, occ[0] // due 2026-06-01T07:00Z
}

func act(subject uuid.UUID, occ *sched.Occurrence, action string, at time.Time) sched.ActionInput {
	return sched.ActionInput{ClientActionID: uuid.New(), SubjectRef: subject, OccurrenceID: occ.ID, Action: action, ActedAt: at}
}

func TestS154TakenAndSkippedAreFinalAndIdempotentByClientActionID(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t)
	clk.Set(utc("2026-06-01T07:05:00Z"))
	in := act(subject, occ, "taken", utc("2026-06-01T07:02:00Z"))
	got, replayed, err := svc.RecordAction(ctx, in)
	if err != nil || replayed || got.State != sched.OccAcknowledged || got.Action == nil || *got.Action != "taken" || got.ActedAt == nil || !got.ActedAt.Equal(utc("2026-06-01T07:02:00Z")) {
		t.Fatalf("taken: %v replayed=%v %+v", err, replayed, got)
	}
	// Retry with the same client action ID (the phone never saw the answer): same result, flagged as a replay.
	again, replayed, err := svc.RecordAction(ctx, in)
	if err != nil || !replayed || again.State != sched.OccAcknowledged {
		t.Fatalf("replay: %v %v %+v", err, replayed, again)
	}
	// The same ID reused for a different action is refused.
	other := in
	other.Action = "skipped"
	if _, _, err := svc.RecordAction(ctx, other); problemCode(err) != "idempotency.key_reused" {
		t.Fatalf("reused id: %v", err)
	}
}

func TestS154ConflictRuleFirstFinalActionWins(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t)
	clk.Set(utc("2026-06-01T08:00:00Z"))
	if _, _, err := svc.RecordAction(ctx, act(subject, occ, "skipped", utc("2026-06-01T07:10:00Z"))); err != nil {
		t.Fatal(err)
	}
	// A different final action from another device is refused and changes nothing.
	if _, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", utc("2026-06-01T07:20:00Z"))); problemCode(err) != "occurrence.action_conflict" {
		t.Fatalf("conflict: %v", err)
	}
	// The same final action with a new client action ID is accepted and changes nothing.
	got, _, err := svc.RecordAction(ctx, act(subject, occ, "skipped", utc("2026-06-01T07:30:00Z")))
	if err != nil || *got.Action != "skipped" || !got.ActedAt.Equal(utc("2026-06-01T07:10:00Z")) {
		t.Fatalf("same final action: %v %+v", err, got)
	}
	// Snoozing a finished occurrence is not allowed.
	if _, _, err := svc.RecordAction(ctx, act(subject, occ, "snoozed", utc("2026-06-01T07:55:00Z"))); problemCode(err) != "occurrence.invalid_state" {
		t.Fatalf("snooze after final: %v", err)
	}
}

func TestS154ActingTimeBounds(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t) // due 07:00Z
	clk.Set(utc("2026-06-01T09:00:00Z"))
	for name, at := range map[string]time.Time{
		"future":            utc("2026-06-01T09:10:00Z"), // beyond the 5 minute clock skew
		"before the window": utc("2026-06-01T05:59:00Z"), // window opens 1 h before due
		"absurdly late":     utc("2026-06-02T07:01:00Z"), // more than 24 h after due
	} {
		_, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", at))
		if problemCode(err) != "request.invalid" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Edges are inside: a little in the future (skew) and exactly when the window opens.
	if _, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", utc("2026-06-01T09:04:00Z"))); err != nil {
		t.Fatalf("skew: %v", err)
	}
	o2 := listAll(t, svc, subject)[1] // due 2026-06-02T07:00Z
	clk.Set(utc("2026-06-02T09:00:00Z"))
	if _, _, err := svc.RecordAction(ctx, act(subject, o2, "skipped", utc("2026-06-02T06:00:00Z"))); err != nil {
		t.Fatalf("window opens: %v", err)
	}
	// Unknown action and zero time.
	o3 := listAll(t, svc, subject)[2]
	if _, _, err := svc.RecordAction(ctx, act(subject, o3, "ate", clk.Now())); problemCode(err) != "request.invalid" {
		t.Fatalf("action: %v", err)
	}
	if _, _, err := svc.RecordAction(ctx, sched.ActionInput{ClientActionID: uuid.New(), SubjectRef: subject, OccurrenceID: o3.ID, Action: "taken"}); problemCode(err) != "request.invalid" {
		t.Fatalf("zero time: %v", err)
	}
}

func TestS154OfflineTakenAfterMissedIsAccepted(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t)
	clk.Set(utc("2026-06-01T12:00:00Z"))
	if _, err := svc.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state='missed' WHERE id=$1`, occ.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", utc("2026-06-01T07:30:00Z")))
	if err != nil || got.State != sched.OccAcknowledged || *got.Action != "taken" {
		t.Fatalf("taken after missed: %v %+v", err, got)
	}
}

func TestS154OtherSubjectsOccurrenceIsNotFoundAndCancelledIsRefused(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t)
	clk.Set(utc("2026-06-01T07:05:00Z"))
	if _, _, err := svc.RecordAction(ctx, act(uuid.New(), occ, "taken", clk.Now())); problemCode(err) != "resource.not_found" {
		t.Fatalf("other subject: %v", err)
	}
	if o, _ := svc.GetOccurrence(ctx, occ.ID); o.State == sched.OccAcknowledged {
		t.Fatal("another subject's action must change nothing")
	}
	if _, _, err := svc.RecordAction(ctx, act(subject, &sched.Occurrence{ID: uuid.New()}, "taken", clk.Now())); problemCode(err) != "resource.not_found" {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := svc.CancelOccurrence(ctx, occ.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", clk.Now())); problemCode(err) != "occurrence.invalid_state" {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestS154SnoozeRules(t *testing.T) {
	ctx := context.Background()
	svc, clk, subject, occ := actionFixture(t)
	clk.Set(utc("2026-06-01T07:01:00Z"))
	dispatched := func() {
		if _, err := svc.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state='dispatched', dispatched_at=$2, enqueued_at=$2 WHERE id=$1`, occ.ID, clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	dispatched()
	snooze := func(minutes int, at time.Time) (*sched.Occurrence, error) {
		in := act(subject, occ, "snoozed", at)
		in.SnoozeMinutes = minutes
		o, _, err := svc.RecordAction(ctx, in)
		return o, err
	}
	for _, bad := range []int{4, 61, -1} {
		if _, err := snooze(bad, clk.Now()); problemCode(err) != "request.invalid" {
			t.Fatalf("delay %d: %v", bad, err)
		}
	}
	o, err := snooze(10, clk.Now())
	if err != nil || o.State != sched.OccPending || o.NotifyAt == nil || !o.NotifyAt.Equal(utc("2026-06-01T07:11:00Z")) || o.SnoozeCount != 1 || o.Action != nil {
		t.Fatalf("snooze: %v %+v", err, o)
	}
	// One snooze at a time.
	if _, err := snooze(10, clk.Now()); problemCode(err) != "occurrence.snooze_active" {
		t.Fatalf("second snooze while snoozed: %v", err)
	}
	// After the re-notification went out, a new snooze is allowed, up to the limit.
	for i := 2; i <= 3; i++ {
		clk.Add(12 * time.Minute)
		dispatched()
		o, err = snooze(5, clk.Now())
		if err != nil || o.SnoozeCount != i {
			t.Fatalf("snooze %d: %v %+v", i, err, o)
		}
	}
	clk.Add(12 * time.Minute)
	dispatched()
	if _, err := snooze(5, clk.Now()); problemCode(err) != "occurrence.snooze_limit" {
		t.Fatalf("fourth snooze: %v", err)
	}
	// An offline snooze that reaches the server much later than the patient acted is stale.
	if _, err := snooze(5, clk.Now().Add(-20*time.Minute)); problemCode(err) != "request.invalid" {
		t.Fatalf("stale snooze: %v", err)
	}
	// Taken is still possible after snoozes, and ends the reminder cycle.
	got, _, err := svc.RecordAction(ctx, act(subject, occ, "taken", clk.Now()))
	if err != nil || got.State != sched.OccAcknowledged {
		t.Fatalf("taken: %v", err)
	}
}

func TestS154SnoozedOccurrenceIsReclaimedAtNotifyTimeNotBefore(t *testing.T) {
	ctx := context.Background()
	svc, env, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	create(t, svc, baseInput(subject, "UTC", "FREQ=DAILY", utc("2026-06-01T07:00:00Z")))
	occ := listAll(t, svc, subject)[0]
	clk.Set(utc("2026-06-01T07:01:00Z"))
	claim := func(at time.Time) int {
		tx, err := env.App.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		due, err := svc.ClaimDue(ctx, tx, at, 10)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return len(due)
	}
	if n := claim(clk.Now()); n != 1 {
		t.Fatalf("first claim %d", n)
	}
	if _, err := svc.MarkDispatched(ctx, occ.ID); err != nil {
		t.Fatal(err)
	}
	in := act(subject, occ, "snoozed", clk.Now())
	in.SnoozeMinutes = 10
	if _, _, err := svc.RecordAction(ctx, in); err != nil {
		t.Fatal(err)
	}
	if n := claim(utc("2026-06-01T07:10:00Z")); n != 0 {
		t.Fatalf("claimed before the snooze ended: %d", n)
	}
	if n := claim(utc("2026-06-01T07:11:00Z")); n != 1 {
		t.Fatalf("not re-claimed after the snooze: %d", n)
	}
	// A snoozed reminder that could not go out for over an hour is expired like any stale one (D-28), measured
	// from its notify time, not from the original due time.
	in2 := act(subject, occ, "snoozed", clk.Now())
	_ = in2
	if _, err := svc.Pool.Exec(ctx, `UPDATE scheduler.occurrences SET state='pending', enqueued_at=NULL, notify_at=$2 WHERE id=$1`, occ.ID, utc("2026-06-01T07:20:00Z")); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.ExpireStale(ctx, utc("2026-06-01T08:10:00Z"), time.Hour); err != nil || n != 0 {
		t.Fatalf("not yet stale from its notify time: %d %v", n, err)
	}
	if n, err := svc.ExpireStale(ctx, utc("2026-06-01T08:21:00Z"), time.Hour); err != nil || n != 1 {
		t.Fatalf("stale: %d %v", n, err)
	}
}

func TestS154SnoozeRenotifiesWithNewEventIDAndNotifyAt(t *testing.T) {
	ctx := context.Background()
	svc, env := realService(t)
	svc.SnoozeUnit = time.Second // "minutes" are seconds in this test
	rcv := newReceiver(t)
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond})
	a, occ := dueSoon(t, svc, 500*time.Millisecond)
	waitState(t, svc, occ.ID, sched.OccDispatched, 20*time.Second)
	in := sched.ActionInput{ClientActionID: uuid.New(), SubjectRef: a.SubjectRef, OccurrenceID: occ.ID, Action: "snoozed", ActedAt: time.Now(), SnoozeMinutes: 5}
	if _, _, err := svc.RecordAction(ctx, in); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for len(rcv.events()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("no second notification: %d events", len(rcv.events()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	ev := rcv.events()
	if ev[1].event.EventID == ev[0].event.EventID || ev[1].event.EventID != dispatch.EventIDRound(occ.ID, 1) || ev[1].event.NotifyAt == nil ||
		!ev[1].event.DueAt.Equal(occ.DueAt) || ev[0].event.NotifyAt != nil {
		t.Fatalf("events %+v / %+v", ev[0].event, ev[1].event)
	}
	if !strings.Contains(string(ev[1].raw), "notifyAt") || strings.Contains(string(ev[0].raw), "notifyAt") {
		t.Fatal("notifyAt appears only on a snoozed re-notification")
	}
	waitState(t, svc, occ.ID, sched.OccDispatched, 20*time.Second)
}

func TestS154TakenBeforeDispatchStopsDispatch(t *testing.T) {
	ctx := context.Background()
	svc, env := realService(t)
	rcv := newReceiver(t)
	startRuntime(t, env, svc, rcv.srv.URL, testSecret, dispatch.Config{ScanInterval: 100 * time.Millisecond})
	a, occ := dueSoon(t, svc, 2*time.Second)
	_, _, err := svc.RecordAction(ctx, sched.ActionInput{ClientActionID: uuid.New(), SubjectRef: a.SubjectRef, OccurrenceID: occ.ID, Action: "taken", ActedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if n := len(rcv.events()); n != 0 {
		t.Fatalf("a dose already taken must not be dispatched: %d events", n)
	}
}

func TestS154APIActionEndpoint(t *testing.T) {
	svc, _, clk := newService(t, utc("2026-06-01T06:00:00Z"))
	ts := apiServer(t, svc)
	subject := uuid.New()
	create(t, svc, baseInput(subject, "UTC", "FREQ=DAILY", utc("2026-06-01T07:00:00Z")))
	occ := listAll(t, svc, subject)[0]
	clk.Set(utc("2026-06-01T07:05:00Z"))
	url := ts.URL + "/v1/occurrences/" + occ.ID.String() + "/actions"
	body := func(id uuid.UUID, subj uuid.UUID, action, extra string) string {
		return `{"clientActionId":"` + id.String() + `","subjectRef":"` + subj.String() + `","action":"` + action + `","actedAt":"2026-06-01T07:03:00Z"` + extra + `}`
	}
	cid := uuid.New()
	resp, b := do(t, "POST", url, auth(), body(cid, subject, "taken", ""))
	if resp.StatusCode != 200 || !strings.Contains(b, `"action":"taken"`) || !strings.Contains(b, `"timeZone":"UTC"`) {
		t.Fatalf("taken: %d %s", resp.StatusCode, b)
	}
	resp, _ = do(t, "POST", url, auth(), body(cid, subject, "taken", ""))
	if resp.StatusCode != 200 || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", resp.StatusCode, resp.Header)
	}
	resp, b = do(t, "POST", url, auth(), body(uuid.New(), subject, "skipped", ""))
	expectProblem(t, resp, b, 409, "occurrence.action_conflict")
	resp, b = do(t, "POST", url, auth(), body(uuid.New(), uuid.New(), "taken", ""))
	expectProblem(t, resp, b, 404, "resource.not_found")
	resp, b = do(t, "POST", url, auth(), body(uuid.New(), subject, "taken", `,"medicineName":"x"`))
	expectProblem(t, resp, b, 400, "request.invalid")
	resp, b = do(t, "POST", url, auth(), `{"clientActionId":"bad","subjectRef":"`+subject.String()+`","action":"taken","actedAt":"2026-06-01T07:03:00Z"}`)
	expectProblem(t, resp, b, 400, "request.invalid")
	resp, _ = do(t, "POST", url, nil, body(uuid.New(), subject, "taken", ""))
	if resp.StatusCode != 401 {
		t.Fatalf("auth: %d", resp.StatusCode)
	}
	// The list shows the new fields.
	resp, b = do(t, "GET", ts.URL+"/v1/occurrences?subjectRef="+subject.String()+"&limit=1", auth(), "")
	if resp.StatusCode != 200 || !strings.Contains(b, `"action":"taken"`) || !strings.Contains(b, `"actedAt"`) || !strings.Contains(b, `"snoozeCount":0`) {
		t.Fatalf("list: %d %s", resp.StatusCode, b)
	}
}
