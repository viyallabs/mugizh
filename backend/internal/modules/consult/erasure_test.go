package consult_test

import (
	"context"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// K-DP-07, D-32: consult records are the teleconsultation record (RET-02), so erasure
// restricts and reports, never deletes; replays and cancels report nothing extra.
func TestKDP07_ConsultErasureKeepsRecordsUnderRET02(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	var sessions int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.session`).Scan(&sessions)
	req := ids.New()
	ev := func(typ string) outbox.Event {
		return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: c.patient, Payload: map[string]string{"requestRef": req, "subjectRef": c.patient}}
	}
	step1 := ev("rights.erasure_requested")
	for i := 0; i < 2; i++ {
		if err := f.m.OnErasureRequested(ctx, step1); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.OnErasureCancelled(ctx, ev("rights.erasure_cancelled")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureDue(ctx, ev("rights.erasure_due")); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	for _, e := range f.events(t, "rights.task_done") {
		if e["requestRef"] == req {
			got = append(got, e)
		}
	}
	outcomes := map[string]string{}
	for _, g := range got {
		if g["module"] != "consult" {
			t.Fatalf("module: %v", g)
		}
		outcomes[g["outcome"]] = g["retentionRule"]
	}
	if rule, ok := outcomes["restricted"]; len(got) != 2 || !ok || rule != "RET-02" {
		t.Fatalf("task reports: %v", got)
	}
	if _, ok := outcomes["none"]; !ok {
		t.Fatalf("task reports: %v", got)
	}
	var after int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.session`).Scan(&after)
	if sessions == 0 || after != sessions {
		t.Fatalf("records must be kept: before %d after %d", sessions, after)
	}
	if err := f.m.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Payload: map[string]string{"requestRef": "x"}}); err == nil {
		t.Fatal("malformed event must be refused")
	}
}
