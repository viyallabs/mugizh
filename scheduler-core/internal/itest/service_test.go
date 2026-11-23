package itest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

func baseInput(subject uuid.UUID, zone, rule string, start time.Time) sched.CreateInput {
	return sched.CreateInput{SubjectRef: subject, AppRef: "core", ExternalRef: "rx-item-" + uuid.NewString()[:8], TypeKey: "med.dose", RRule: rule, TimeZone: zone, StartsAt: start}
}

func problemCode(err error) string {
	var p *problem.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestCreateGeneratesFourteenDaysAcrossSpringForward(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-03-05T15:00:00Z"))
	subject := uuid.New()
	a := create(t, svc, baseInput(subject, "America/New_York", "FREQ=DAILY", wallIn(t, "America/New_York", "2026-03-05T08:00:00")))
	if a.State != sched.ActivityActive || a.Version != 1 {
		t.Fatalf("unexpected activity %+v", a)
	}
	got := dues(listAll(t, svc, subject))
	want := []string{"2026-03-06T13:00:00Z", "2026-03-07T13:00:00Z"}
	for d := 8; d <= 19; d++ { // 08:00 EDT = 12:00Z from the 8th
		want = append(want, time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC).Format(time.RFC3339))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// TC-39: the patient changes time zone; reminders follow the new zone, with no duplicates and no gaps.
func TestTC39TimeZoneChangeAndDST(t *testing.T) {
	ctx := context.Background()
	svc, env, clk := newService(t, utc("2026-03-01T04:00:00Z"))
	subject := uuid.New()
	// 08:00 daily in a fixed-offset zone (no DST), then the patient travels to a zone with DST inside the horizon.
	a := create(t, svc, baseInput(subject, "Asia/Kolkata", "FREQ=DAILY", wallIn(t, "Asia/Kolkata", "2026-03-01T08:00:00")))
	before := inState(listAll(t, svc, subject), sched.OccPending)
	if len(before) != 14 {
		t.Fatalf("expected 14 pending before the change, got %d", len(before))
	}

	clk.Set(utc("2026-03-02T01:00:00Z")) // 06:30 local: today's 08:00 (02:30Z) is still ahead
	tz := "America/New_York"
	u, err := svc.Update(ctx, a.ID, a.Version, sched.UpdateInput{TimeZone: &tz})
	if err != nil {
		t.Fatal(err)
	}
	if u.TimeZone != tz || u.Version != a.Version+1 {
		t.Fatalf("update not applied: %+v", u)
	}
	all := listAll(t, svc, subject)
	pending := inState(all, sched.OccPending)
	if len(inState(all, sched.OccCancelled)) != 14 {
		t.Fatalf("all 14 old pending occurrences should be cancelled, got %d", len(inState(all, sched.OccCancelled)))
	}
	// Same local time (08:00) in the new zone: EST (13:00Z) until 7 March, EDT (12:00Z) from 8 March.
	var want []string
	for d := 2; d <= 7; d++ {
		want = append(want, time.Date(2026, 3, d, 13, 0, 0, 0, time.UTC).Format(time.RFC3339))
	}
	for d := 8; d <= 16; d++ {
		want = append(want, time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC).Format(time.RFC3339))
	}
	// Horizon is now+14d = 16 March 01:00Z, so the 16th (12:00Z) is not generated yet.
	want = want[:len(want)-1]
	if got := dues(pending); !reflect.DeepEqual(got, want) {
		t.Fatalf("pending after zone change\ngot  %v\nwant %v", got, want)
	}
	// Exactly one pending occurrence per local day in the new zone: no duplicates, no gaps.
	loc, _ := time.LoadLocation(tz)
	seen := map[string]int{}
	for _, o := range pending {
		l := o.DueAt.In(loc)
		if l.Hour() != 8 || l.Minute() != 0 {
			t.Fatalf("%s is not 08:00 local", l)
		}
		seen[l.Format("2006-01-02")]++
	}
	for d := 2; d <= 15; d++ {
		day := time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if seen[day] != 1 {
			t.Fatalf("local day %s has %d occurrences, want 1", day, seen[day])
		}
	}
	// The database enforces uniqueness among live rows.
	var dup int
	if err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM (SELECT activity_id, due_at FROM scheduler.occurrences WHERE state <> 'cancelled' GROUP BY 1, 2 HAVING count(*) > 1) d`).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("duplicate live occurrences: %d, %v", dup, err)
	}
	// A stale ETag is refused; an unchanged request is a no-op.
	if _, err := svc.Update(ctx, a.ID, a.Version, sched.UpdateInput{TimeZone: &tz}); problemCode(err) != "resource.precondition_failed" {
		t.Fatalf("stale If-Match: %v", err)
	}
	same, err := svc.Update(ctx, a.ID, u.Version, sched.UpdateInput{TimeZone: &tz})
	if err != nil || same.Version != u.Version {
		t.Fatalf("unchanged update should be a no-op: %v %+v", err, same)
	}
}

func TestZoneChangeKeepsOverdueUndispatchedOccurrence(t *testing.T) {
	ctx := context.Background()
	svc, _, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	a := create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	clk.Set(utc("2026-06-01T07:00:10Z")) // the 07:00Z occurrence is overdue but not yet dispatched
	tz := "America/New_York"
	if _, err := svc.Update(ctx, a.ID, a.Version, sched.UpdateInput{TimeZone: &tz}); err != nil {
		t.Fatal(err)
	}
	var overdue *sched.Occurrence
	for _, o := range inState(listAll(t, svc, subject), sched.OccPending) {
		if o.DueAt.Equal(utc("2026-06-01T07:00:00Z")) {
			overdue = o
		}
	}
	if overdue == nil {
		t.Fatal("the overdue pending occurrence must survive a zone change (no gap)")
	}
}

func TestFallBackOverlapFiresOncePerLocalDay(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-10-30T12:00:00Z"))
	subject := uuid.New()
	create(t, svc, baseInput(subject, "America/New_York", "FREQ=DAILY", wallIn(t, "America/New_York", "2026-10-30T01:30:00")))
	got := dues(listAll(t, svc, subject))
	want := []string{"2026-10-31T05:30:00Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"}
	if !reflect.DeepEqual(got[:3], want) {
		t.Fatalf("got %v", got[:4])
	}
}

func TestRollingHorizonExtendsIdempotently(t *testing.T) {
	ctx := context.Background()
	svc, env, clk := newService(t, utc("2026-10-25T00:00:00Z"))
	subject := uuid.New()
	a := create(t, svc, baseInput(subject, "America/New_York", "FREQ=DAILY;BYHOUR=1,9", wallIn(t, "America/New_York", "2026-10-25T09:00:00")))
	n0 := len(listAll(t, svc, subject))
	if acts, _, err := svc.GenerateDue(ctx); err != nil || acts != 0 {
		t.Fatalf("nothing is due for extension yet: %d %v", acts, err)
	}
	// One day later the horizon is extended by a day; running it twice adds nothing the second time.
	clk.Add(24 * time.Hour)
	acts, occ, err := svc.GenerateDue(ctx)
	if err != nil || acts != 1 || occ != 2 {
		t.Fatalf("extension: activities=%d occurrences=%d err=%v", acts, occ, err)
	}
	if acts, occ, _ := svc.GenerateDue(ctx); acts != 0 || occ != 0 {
		t.Fatalf("second run should be a no-op, got %d %d", acts, occ)
	}
	// Move across the fall-back of 1 November in daily steps; the horizon follows without duplicates or gaps.
	for i := 0; i < 8; i++ {
		clk.Add(24 * time.Hour)
		if _, _, err := svc.GenerateDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	all := listAll(t, svc, subject)
	if len(all) != n0+2*9 {
		t.Fatalf("occurrences: got %d want %d", len(all), n0+18)
	}
	loc, _ := time.LoadLocation("America/New_York")
	perDay := map[string]int{}
	for _, o := range all {
		perDay[o.DueAt.In(loc).Format("2006-01-02")]++
	}
	for day, n := range perDay {
		if n != 2 && day != "2026-10-25" && day != "2026-11-08" { // the edges of the window hold one
			t.Fatalf("local day %s has %d occurrences, want 2", day, n)
		}
	}
	var dup int
	if err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM (SELECT activity_id, due_at FROM scheduler.occurrences GROUP BY 1, 2 HAVING count(*) > 1) d`).Scan(&dup); err != nil || dup != 0 {
		t.Fatalf("duplicates: %d %v", dup, err)
	}
	if got, _ := svc.Get(ctx, a.ID); got.GeneratedThrough.Before(clk.Now().Add(13 * 24 * time.Hour)) {
		t.Fatalf("horizon not extended: %v", got.GeneratedThrough)
	}
}

func TestEndedWhenRuleIsExhausted(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	a := create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY;COUNT=3", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	if a.State != sched.ActivityEnded {
		t.Fatalf("a COUNT=3 rule inside the horizon is exhausted: state %s", a.State)
	}
	if n := len(listAll(t, svc, subject)); n != 3 {
		t.Fatalf("occurrences %d", n)
	}
}

func TestPauseResumeCancel(t *testing.T) {
	ctx := context.Background()
	svc, _, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	a := create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	if n := len(inState(listAll(t, svc, subject), sched.OccPending)); n != 14 {
		t.Fatalf("pending %d", n)
	}
	p, err := svc.Pause(ctx, a.ID)
	if err != nil || p.State != sched.ActivityPaused {
		t.Fatalf("pause: %v %+v", err, p)
	}
	all := listAll(t, svc, subject)
	if len(inState(all, sched.OccPending)) != 0 || len(inState(all, sched.OccCancelled)) != 14 {
		t.Fatal("pause cancels every pending occurrence")
	}
	if _, err := svc.Pause(ctx, a.ID); err != nil {
		t.Fatalf("pausing twice is fine: %v", err)
	}
	// A paused activity is not extended.
	clk.Add(48 * time.Hour)
	if acts, _, _ := svc.GenerateDue(ctx); acts != 0 {
		t.Fatal("paused activities are not generated")
	}
	r, err := svc.Resume(ctx, a.ID)
	if err != nil || r.State != sched.ActivityActive {
		t.Fatalf("resume: %v", err)
	}
	pend := inState(listAll(t, svc, subject), sched.OccPending)
	if len(pend) != 14 {
		t.Fatalf("resume regenerates 14 days from now, got %d", len(pend))
	}
	if !pend[0].DueAt.After(clk.Now()) {
		t.Fatal("nothing in the past is generated")
	}
	if _, err := svc.Resume(ctx, a.ID); err != nil {
		t.Fatalf("resuming twice is fine: %v", err)
	}
	c, err := svc.Cancel(ctx, a.ID)
	if err != nil || c.State != sched.ActivityCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if len(inState(listAll(t, svc, subject), sched.OccPending)) != 0 {
		t.Fatal("cancel cancels pending occurrences")
	}
	if _, err := svc.Cancel(ctx, a.ID); err != nil {
		t.Fatalf("cancelling twice is fine: %v", err)
	}
	for name, fn := range map[string]func() error{
		"resume": func() error { _, err := svc.Resume(ctx, a.ID); return err },
		"pause":  func() error { _, err := svc.Pause(ctx, a.ID); return err },
		"update": func() error {
			tz := "Europe/Paris"
			_, err := svc.Update(ctx, a.ID, c.Version, sched.UpdateInput{TimeZone: &tz})
			return err
		},
	} {
		if code := problemCode(fn()); code != "activity.invalid_state" {
			t.Fatalf("%s on a cancelled activity: %q", name, code)
		}
	}
	if _, err := svc.Get(ctx, uuid.New()); problemCode(err) != "resource.not_found" {
		t.Fatalf("unknown activity: %v", err)
	}
}

func TestOccurrenceStates(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	subject := uuid.New()
	create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	occ := listAll(t, svc, subject)
	ack, err := svc.Acknowledge(ctx, occ[0].ID)
	if err != nil || ack.State != sched.OccAcknowledged || ack.AcknowledgedAt == nil {
		t.Fatalf("ack: %v %+v", err, ack)
	}
	if _, err := svc.Acknowledge(ctx, occ[0].ID); err != nil {
		t.Fatalf("acknowledging twice is fine: %v", err)
	}
	if _, err := svc.CancelOccurrence(ctx, occ[0].ID); problemCode(err) != "occurrence.invalid_state" {
		t.Fatalf("cancel after ack: %v", err)
	}
	if _, err := svc.Miss(ctx, occ[1].ID); problemCode(err) != "occurrence.invalid_state" {
		t.Fatalf("miss before dispatch: %v", err)
	}
	c, err := svc.CancelOccurrence(ctx, occ[1].ID)
	if err != nil || c.State != sched.OccCancelled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := svc.CancelOccurrence(ctx, occ[1].ID); err != nil {
		t.Fatalf("cancelling twice is fine: %v", err)
	}
	if _, err := svc.Acknowledge(ctx, uuid.New()); problemCode(err) != "resource.not_found" {
		t.Fatalf("unknown occurrence: %v", err)
	}
}

func TestListPaginationAndRange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	subject, other := uuid.New(), uuid.New()
	create(t, svc, baseInput(subject, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	create(t, svc, baseInput(other, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T09:00:00")))
	var pages, total int
	cursor := ""
	var last time.Time
	for {
		items, next, err := svc.List(ctx, sched.ListParams{SubjectRef: subject, Limit: 5, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, o := range items {
			if o.SubjectRef != subject || !o.DueAt.After(last) {
				t.Fatalf("wrong subject or order: %+v", o)
			}
			last = o.DueAt
			total++
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if total != 14 || pages != 3 {
		t.Fatalf("total %d pages %d", total, pages)
	}
	from, to := utc("2026-06-03T00:00:00Z"), utc("2026-06-05T00:00:00Z")
	items, _, err := svc.List(ctx, sched.ListParams{SubjectRef: subject, From: &from, To: &to, Limit: 100})
	if err != nil || len(items) != 2 {
		t.Fatalf("range: %d %v", len(items), err)
	}
	if _, _, err := svc.List(ctx, sched.ListParams{SubjectRef: subject, Cursor: "not-a-cursor"}); problemCode(err) != "request.invalid" {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	start := wallIn(t, "Europe/London", "2026-06-01T08:00:00")
	bad := map[string]func(*sched.CreateInput){
		"request.invalid":            func(in *sched.CreateInput) { in.TypeKey = "free text" },
		"activity.rrule_invalid":     func(in *sched.CreateInput) { in.RRule = "FREQ=MINUTELY" },
		"activity.time_zone_invalid": func(in *sched.CreateInput) { in.TimeZone = "Local" },
	}
	for code, mut := range bad {
		in := baseInput(uuid.New(), "Europe/London", "FREQ=DAILY", start)
		mut(&in)
		if _, _, err := svc.Create(context.Background(), uuid.New(), in); problemCode(err) != code {
			t.Errorf("want %s, got %v", code, err)
		}
	}
	for _, mut := range []func(*sched.CreateInput){
		func(in *sched.CreateInput) { in.ExternalRef = "has spaces and a name" },
		func(in *sched.CreateInput) { in.AppRef = "Core" },
		func(in *sched.CreateInput) { in.SubjectRef = uuid.Nil },
		func(in *sched.CreateInput) { e := in.StartsAt.Add(-time.Hour); in.EndsAt = &e },
	} {
		in := baseInput(uuid.New(), "Europe/London", "FREQ=DAILY", start)
		mut(&in)
		if _, _, err := svc.Create(context.Background(), uuid.New(), in); problemCode(err) != "request.invalid" {
			t.Errorf("want request.invalid, got %v", err)
		}
	}
}

func TestServiceIdempotency(t *testing.T) {
	ctx := context.Background()
	svc, _, clk := newService(t, utc("2026-06-01T00:00:00Z"))
	in := baseInput(uuid.New(), "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00"))
	key := uuid.New()
	a1, rep1, err := svc.Create(ctx, key, in)
	if err != nil || rep1 {
		t.Fatalf("first: %v %v", err, rep1)
	}
	a2, rep2, err := svc.Create(ctx, key, in)
	if err != nil || !rep2 || a2.ID != a1.ID {
		t.Fatalf("replay: %v %v", err, rep2)
	}
	if n := len(listAll(t, svc, in.SubjectRef)); n != 14 {
		t.Fatalf("replay must not create occurrences again: %d", n)
	}
	in2 := in
	in2.RRule = "FREQ=DAILY;BYHOUR=9"
	if _, _, err := svc.Create(ctx, key, in2); problemCode(err) != "idempotency.key_reused" {
		t.Fatalf("key reuse with another body: %v", err)
	}
	// Concurrent first requests with the same key create exactly one activity.
	in3 := baseInput(uuid.New(), "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00"))
	key3 := uuid.New()
	ids := make(chan uuid.UUID, 8)
	for i := 0; i < 8; i++ {
		go func() {
			a, _, err := svc.Create(ctx, key3, in3)
			if err != nil {
				ids <- uuid.Nil
				return
			}
			ids <- a.ID
		}()
	}
	first := <-ids
	for i := 1; i < 8; i++ {
		if id := <-ids; id != first || id == uuid.Nil {
			t.Fatalf("concurrent creates produced different results")
		}
	}
	_ = clk
}

func TestEraseRemovesEverythingForSubject(t *testing.T) {
	ctx := context.Background()
	svc, env, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	s1, s2 := uuid.New(), uuid.New()
	create(t, svc, baseInput(s1, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	create(t, svc, baseInput(s1, "Europe/London", "FREQ=DAILY;BYHOUR=9,10", wallIn(t, "Europe/London", "2026-06-01T09:00:00")))
	create(t, svc, baseInput(s2, "Europe/London", "FREQ=DAILY", wallIn(t, "Europe/London", "2026-06-01T08:00:00")))
	rep, err := svc.Erase(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Subjects != 1 || rep.Activities != 2 || rep.Occurrences != 14+28 {
		t.Fatalf("report %+v", rep)
	}
	for _, q := range []string{
		`SELECT count(*) FROM scheduler.subjects WHERE subject_ref = $1`,
		`SELECT count(*) FROM scheduler.activities WHERE subject_ref = $1`,
		`SELECT count(*) FROM scheduler.occurrences WHERE subject_ref = $1`,
		`SELECT count(*) FROM scheduler.idempotency_keys k JOIN scheduler.activities a ON a.id = k.activity_id WHERE a.subject_ref = $1`,
	} {
		var n int
		if err := env.Owner.QueryRow(ctx, q, s1).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s -> %d %v", q, n, err)
		}
	}
	var keys int
	if err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM scheduler.idempotency_keys`).Scan(&keys); err != nil || keys != 1 {
		t.Fatalf("only the other subject's idempotency key remains: %d %v", keys, err)
	}
	if n := len(listAll(t, svc, s2)); n != 14 {
		t.Fatalf("other subject untouched: %d", n)
	}
	again, err := svc.Erase(ctx, s1)
	if err != nil || (again != sched.ErasureReport{}) {
		t.Fatalf("erasing again is a no-op with zero counts: %+v %v", again, err)
	}
}
