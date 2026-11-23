package notifications_test

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func erasureEvent(typ, req, subject string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: subject, Payload: map[string]string{"requestRef": req, "subjectRef": subject}}
}

func (f *fx) taskDone(t *testing.T, req string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload::text FROM notifications.event_outbox WHERE type='rights.task_done' ORDER BY occurred_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		m := map[string]string{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		if m["requestRef"] == req {
			out = append(out, m)
		}
	}
	return out
}

func TestKDP07_NotificationsErasureStep1DeletesAndReportsOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u, other := f.user(true), f.user(true)
	_ = f.m.SetPreferences(ctx, u, notifications.Preferences{TimeZone: "UTC", Locale: "en", Discreet: true})
	_ = f.m.SetPreferences(ctx, other, notifications.Preferences{TimeZone: "UTC", Locale: "en"})
	_, _ = f.m.Send(ctx, booking(u, "e1"))
	_, _ = f.m.Send(ctx, booking(other, "e2"))
	f.run(t, f.now.Add(1))
	req := ids.New()
	ev := erasureEvent("rights.erasure_requested", req, u)
	for i := 0; i < 2; i++ { // replay-safe
		if err := f.m.OnErasureRequested(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`SELECT count(*) FROM notifications.preference WHERE user_ref=$1`, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1`, `SELECT count(*) FROM notifications.delivery_log WHERE recipient_ref=$1`} {
		if f.count(t, q, u) != 0 {
			t.Fatalf("not deleted: %s", q)
		}
	}
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1`, other) != 1 {
		t.Fatal("other user's data must stay")
	}
	got := f.taskDone(t, req)
	if len(got) != 1 || got[0]["module"] != "notifications" || got[0]["outcome"] != "deleted" || len(got[0]) != 3 {
		t.Fatalf("task_done payload %v", got)
	}
}

// TM-33 (S-178): the "erasure requested" notice is queued just before the erasure event
// reaches this module. Step 1 must not delete it, or the person is never told. Notices
// with reason data_rights survive step 1 and are swept at step 2 like everything else.
func TestTM33ErasureNoticeSurvivesStep1(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user(true)
	notice := booking(u, "erasure_requested:x")
	notice.Reason = "data_rights"
	if _, err := f.m.Send(ctx, notice); err != nil {
		t.Fatal(err)
	}
	_, _ = f.m.Send(ctx, booking(u, "other"))
	req := ids.New()
	if err := f.m.OnErasureRequested(ctx, erasureEvent("rights.erasure_requested", req, u)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1 AND reason='data_rights'`, u); n != 1 {
		t.Fatalf("the erasure notice must survive step 1, found %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1 AND reason<>'data_rights'`, u); n != 0 {
		t.Fatalf("everything else is deleted at step 1, found %d", n)
	}
	if err := f.m.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req, u)); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1`, u); n != 0 {
		t.Fatalf("step 2 sweeps the notice too, found %d", n)
	}
}

func TestKDP07_NotificationsErasureDueSweepsAndCancelIsNoop(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user(true)
	req := ids.New()
	if err := f.m.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, u)); err != nil {
		t.Fatal(err)
	}
	if len(f.taskDone(t, req)) != 0 {
		t.Fatal("cancel reports nothing")
	}
	if err := f.m.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req, u)); err != nil {
		t.Fatal(err)
	}
	if got := f.taskDone(t, req); len(got) != 1 || got[0]["outcome"] != "none" {
		t.Fatalf("%v", got)
	}
	// A notice written after step 1 is swept at step 2.
	_, _ = f.m.Send(ctx, booking(u, "late"))
	req2 := ids.New()
	if err := f.m.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req2, u)); err != nil {
		t.Fatal(err)
	}
	if got := f.taskDone(t, req2); len(got) != 1 || got[0]["outcome"] != "erased" {
		t.Fatalf("%v", got)
	}
	if err := f.m.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Type: "rights.erasure_requested"}); err == nil {
		t.Fatal("event without refs must fail")
	}
}
