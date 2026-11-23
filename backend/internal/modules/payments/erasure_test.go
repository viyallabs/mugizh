package payments_test

import (
	"encoding/json"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func erasureEvent(typ, req, subject string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: subject, Payload: map[string]string{"requestRef": req, "subjectRef": subject}}
}

func (f *fx) taskDone(t *testing.T, req string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(ctx, `SELECT payload::text FROM payments.event_outbox WHERE type='rights.task_done' ORDER BY occurred_at`)
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

func TestKDP07_PaymentsErasureKeepsFinancialRecordsUnderRET04(t *testing.T) {
	f := setup(t)
	f.open(t)
	orders := f.n(t, `SELECT count(*) FROM payments.payment_order`)
	req, subject := ids.New(), ids.New()
	ev := erasureEvent("rights.erasure_requested", req, subject)
	for i := 0; i < 2; i++ { // replay-safe
		if err := f.m.OnErasureRequested(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	got := f.taskDone(t, req)
	if len(got) != 1 || got[0]["module"] != "payments" || got[0]["outcome"] != "restricted" || got[0]["retentionRule"] != "RET-04" || len(got[0]) != 4 {
		t.Fatalf("step 1 payload %v", got)
	}
	if f.n(t, `SELECT count(*) FROM payments.payment_order`) != orders || orders == 0 {
		t.Fatal("financial records must be kept")
	}
	// Cancel changes nothing and reports nothing.
	if err := f.m.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, subject)); err != nil {
		t.Fatal(err)
	}
	if len(f.taskDone(t, req)) != 1 {
		t.Fatal("cancel must not report")
	}
	// Step 2 reports none, once.
	due := erasureEvent("rights.erasure_due", req, subject)
	for i := 0; i < 2; i++ {
		if err := f.m.OnErasureDue(ctx, due); err != nil {
			t.Fatal(err)
		}
	}
	got = f.taskDone(t, req)
	if len(got) != 2 || got[1]["outcome"] != "none" || got[1]["module"] != "payments" {
		t.Fatalf("step 2 payload %v", got)
	}
	if err := f.m.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Type: "rights.erasure_requested"}); err == nil {
		t.Fatal("event without refs must fail")
	}
}
