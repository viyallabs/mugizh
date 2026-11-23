package identity_test

import (
	"context"
	"encoding/json"
	"testing"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func erasureEvent(typ, req, subject string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: subject, Payload: map[string]string{"requestRef": req, "subjectRef": subject}}
}

func (f *fx) taskDone(t *testing.T, req string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload::text FROM identity.event_outbox WHERE type='rights.task_done' ORDER BY occurred_at`)
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

func (f *fx) n(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKDP07_IdentityErasureClosesAccountThenDeletesAfterHold(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := newDevice(t)
	tok := f.signIn(t, phone("31"), ip.AppPatient, d, "")
	req := ids.New()
	ev := erasureEvent("rights.erasure_requested", req, tok.UserID)
	for i := 0; i < 2; i++ { // replay-safe
		if err := f.m.OnErasureRequested(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.m.Authenticate(ctx, tok.AccessToken); err == nil {
		t.Fatal("sessions must be revoked")
	}
	if f.n(t, `SELECT count(*) FROM identity.session WHERE user_id=$1 AND revoked_at IS NULL`, tok.UserID) != 0 ||
		f.n(t, `SELECT count(*) FROM identity.device WHERE user_id=$1 AND status<>'revoked'`, tok.UserID) != 0 {
		t.Fatal("sessions and devices revoked")
	}
	if f.n(t, `SELECT count(*) FROM identity.user_account WHERE id=$1 AND status='closed' AND closed_at IS NOT NULL`, tok.UserID) != 1 {
		t.Fatal("account closed and kept restricted (RET-18)")
	}
	got := f.taskDone(t, req)
	if len(got) != 1 || got[0]["module"] != "identity" || got[0]["outcome"] != "restricted" || got[0]["retentionRule"] != "RET-18" || len(got[0]) != 4 {
		t.Fatalf("step 1 payload %v", got)
	}
	// The phone is free for a new account.
	tok2 := f.signIn(t, phone("31"), ip.AppPatient, newDevice(t), "")
	if !tok2.NewAccount || tok2.UserID == tok.UserID {
		t.Fatalf("a new account must be created: %+v", tok2)
	}

	// Cancel while the number is taken: the old account stays closed.
	if err := f.m.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, tok.UserID)); err != nil {
		t.Fatal(err)
	}
	if f.n(t, `SELECT count(*) FROM identity.user_account WHERE id=$1 AND status='closed'`, tok.UserID) != 1 {
		t.Fatal("must not reopen onto a taken number")
	}
	if len(f.taskDone(t, req)) != 1 {
		t.Fatal("cancel reports nothing")
	}

	// Step 2 deletes the rows, once.
	due := erasureEvent("rights.erasure_due", req, tok.UserID)
	for i := 0; i < 2; i++ {
		if err := f.m.OnErasureDue(ctx, due); err != nil {
			t.Fatal(err)
		}
	}
	if f.n(t, `SELECT count(*) FROM identity.user_account WHERE id=$1`, tok.UserID) != 0 || f.n(t, `SELECT count(*) FROM identity.device WHERE user_id=$1`, tok.UserID) != 0 ||
		f.n(t, `SELECT count(*) FROM identity.role_assignment WHERE user_id=$1`, tok.UserID) != 0 {
		t.Fatal("account rows deleted")
	}
	if f.n(t, `SELECT count(*) FROM identity.user_account WHERE id=$1`, tok2.UserID) != 1 {
		t.Fatal("the new account is untouched")
	}
	got = f.taskDone(t, req)
	if len(got) != 2 || got[1]["outcome"] != "erased" || got[1]["module"] != "identity" {
		t.Fatalf("step 2 payload %v", got)
	}
}

func TestKDP07_IdentityCancelReopensWhenNumberStillFree(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok := f.signIn(t, phone("32"), ip.AppPatient, newDevice(t), "")
	req := ids.New()
	if err := f.m.OnErasureRequested(ctx, erasureEvent("rights.erasure_requested", req, tok.UserID)); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, tok.UserID)); err != nil {
		t.Fatal(err)
	}
	if f.n(t, `SELECT count(*) FROM identity.user_account WHERE id=$1 AND status='active' AND closed_at IS NULL`, tok.UserID) != 1 {
		t.Fatal("account reopened")
	}
	// Revoked sessions are not restored: the person signs in again, to the same account.
	if tok3 := f.signIn(t, phone("32"), ip.AppPatient, newDevice(t), ""); tok3.UserID != tok.UserID {
		t.Fatal("same account after reopening")
	}
}

func TestKDP07_IdentityUnknownSubjectReportsNoneAndBadEventFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	req, subject := ids.New(), ids.New()
	if err := f.m.OnErasureRequested(ctx, erasureEvent("rights.erasure_requested", req, subject)); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req, subject)); err != nil {
		t.Fatal(err)
	}
	got := f.taskDone(t, req)
	if len(got) != 2 || got[0]["outcome"] != "none" || got[1]["outcome"] != "none" {
		t.Fatalf("%v", got)
	}
	if err := f.m.OnErasureDue(ctx, outbox.Event{ID: ids.New(), Type: "rights.erasure_due"}); err == nil {
		t.Fatal("event without refs must fail")
	}
}
