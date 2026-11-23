package clinical_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	schfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// erasingScheduler adds the subject-erase call the real client must also have.
type erasingScheduler struct {
	*schfake.Client
	erased []string
}

func (s *erasingScheduler) EraseSubject(_ context.Context, subject string) error {
	s.erased = append(s.erased, subject)
	return nil
}

func erasureEvent(typ, req, subject string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: subject, Payload: map[string]string{"requestRef": req, "subjectRef": subject}}
}

func (f *rxfx) taskDone(t *testing.T, req string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload::text FROM clinical.event_outbox WHERE type='rights.task_done' ORDER BY occurred_at`)
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

func TestKDP07_ClinicalErasureKeepsRecordsCancelsRemindersReportsOnce(t *testing.T) {
	ctx := context.Background()
	g := rxSetup(t, true)
	g.gate.consent[g.pat+"|P-REMIND"] = true
	g.prepare(t, item("l1", "paracetamol"))
	r, err := g.rx.Sign(ctx, g.caller(true), g.appt)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.rx.HandleSigned(ctx, signedEvent(r.PrescriptionRef, "1")); err != nil {
		t.Fatal(err)
	}
	if len(g.sch.Active()) != 1 {
		t.Fatal("set-up: one reminder expected")
	}
	versions := g.count(t, `SELECT count(*) FROM clinical.prescription_version`)

	// Step 1: reminders cancelled, records kept, RET-02 reported once.
	req, ev := ids.New(), erasureEvent("rights.erasure_requested", "", g.pat)
	ev.Payload["requestRef"] = req
	for i := 0; i < 2; i++ {
		if err := g.rx.OnErasureRequested(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.sch.Active()) != 0 || g.count(t, `SELECT count(*) FROM clinical.reminder_link WHERE state='active'`) != 0 {
		t.Fatal("reminders must be cancelled")
	}
	if g.count(t, `SELECT count(*) FROM clinical.prescription_version`) != versions || versions == 0 {
		t.Fatal("prescriptions are kept under RET-02")
	}
	got := g.taskDone(t, req)
	if len(got) != 1 || got[0]["module"] != "clinical" || got[0]["outcome"] != "restricted" || got[0]["retentionRule"] != "RET-02" || len(got[0]) != 4 {
		t.Fatalf("step 1 payload %v", got)
	}

	// Cancel: nothing reported, records untouched.
	if err := g.rx.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, g.pat)); err != nil {
		t.Fatal(err)
	}
	if len(g.taskDone(t, req)) != 1 {
		t.Fatal("cancel must not report")
	}

	// Step 2 with a scheduler that cannot erase the subject fails closed (no false "done").
	g.sch.FailErase = errors.New("scheduler-core unavailable")
	if err := g.rx.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req, g.pat)); err == nil {
		t.Fatal("must fail closed")
	}
	if len(g.taskDone(t, req)) != 1 {
		t.Fatal("no report on failure")
	}
}

func TestKDP07_ClinicalErasureStep2ErasesSchedulerSubject(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	es := &erasingScheduler{Client: f.sch}
	cfg := clinical.DefaultConfig()
	cfg.ESignEnabled = true
	cfg.ClassificationVersion = "2026.12.1"
	cfg.ProhibitedValidators = []string{aadhaarNames}
	rx, err := clinical.NewRx(f.m, cfg, clinical.Deps{Crypto: f.cr, Gate: f.gate, Granter: f.gr, Verifier: f.ver, Consults: f.cons,
		Signer: f.sig, Scheduler: es, Notifier: f.not})
	if err != nil {
		t.Fatal(err)
	}
	req := ids.New()
	if err := rx.OnErasureRequested(ctx, erasureEvent("rights.erasure_requested", req, f.pat)); err != nil {
		t.Fatal(err)
	}
	if len(es.erased) != 1 || es.erased[0] != f.pat {
		t.Fatalf("step 1 erases the subject: %v", es.erased)
	}
	due := erasureEvent("rights.erasure_due", req, f.pat)
	for i := 0; i < 2; i++ {
		if err := rx.OnErasureDue(ctx, due); err != nil {
			t.Fatal(err)
		}
	}
	got := f.taskDone(t, req)
	if len(got) != 2 || got[1]["outcome"] != "none" || got[1]["module"] != "clinical" || got[1]["requestRef"] != req {
		t.Fatalf("%v", got)
	}
	if err := rx.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Type: "rights.erasure_requested"}); err == nil {
		t.Fatal("event without refs must fail")
	}
}
