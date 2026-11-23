package records_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// The tables a Core database restore rolls back in these tests. Write-once storage (key
// tombstones and the erasure journal) is never restored: it lives outside the database
// and keeps its newest copy (backup-and-restore.md section 2).
var restoreTables = []string{"rights_task", "account_restriction", "rights_request", "document", "event_outbox",
	"consent_record", "consent_state", "permission_grant", "caregiver_link", "caregiver_invitation", "revocation_outbox"}

var backupSeq atomic.Int64

// backup copies the records tables, standing in for a database snapshot.
func (x *rx) backup(t *testing.T) string {
	t.Helper()
	tag := fmt.Sprintf("bk%d", backupSeq.Add(1))
	for _, tbl := range restoreTables {
		if _, err := x.env.Owner.Exec(context.Background(), fmt.Sprintf(`CREATE TABLE records.%s_%s AS TABLE records.%s`, tag, tbl, tbl)); err != nil {
			t.Fatal(err)
		}
	}
	return tag
}

// restore puts the snapshot back, as a point-in-time restore of the Core database would,
// and drops the in-process cache (a restored service starts empty).
func (x *rx) restore(t *testing.T, tag, patient string) {
	t.Helper()
	ctx := context.Background()
	tx, err := x.env.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `TRUNCATE records.`+strings.Join(restoreTables, ", records.")); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range restoreTables {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO records.%s SELECT * FROM records.%s_%s`, tbl, tag, tbl)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	x.m.Invalidate(patient)
}

func (x *rx) journalKeys(t *testing.T) []string {
	t.Helper()
	k, err := x.blobs.List(context.Background(), "erasure-journal/")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TC-04 / E-DP-04 (S-185): a Core restore to a point before an erasure request must not
// make the account visible again. The erasure journal in write-once storage is replayed
// before the service opens: the same request comes back with its original undo window,
// the account is hidden again, and the erasure continues to the same RET-18 hold.
func TestTC04ErasureJournalReplayAfterRestore(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	if _, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(60*day), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	history := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory, ResourceType: "note", ResourceRef: ids.New()}
	p := x.patientRef(patient, true)
	file := x.upload(t, p, "Blood test", pdf("synthetic lab report"))

	before := x.backup(t)
	q, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	if k := x.journalKeys(t); len(k) != 1 || !strings.HasSuffix(k[0], q.ID+"/requested.json") {
		t.Fatalf("the request must be journaled in write-once storage: %v", k)
	}
	if d := mustDecide(t, x.m, history); d.Allowed {
		t.Fatalf("hidden once requested: %+v", d)
	}

	// The incident: the Core database is restored to the earlier snapshot.
	x.restore(t, before, patient)
	if d := mustDecide(t, x.m, history); !d.Allowed {
		t.Fatalf("precondition: the restore brought the account back: %+v", d)
	}
	n, err := x.r.ReplayJournal(ctx)
	if err != nil || n != 1 {
		t.Fatalf("replay: n=%d err=%v", n, err)
	}
	if d := mustDecide(t, x.m, history); d.Allowed || !d.Hide {
		t.Fatalf("after replay the account must be hidden again: %+v", d)
	}
	if _, err := x.r.IssueDocumentLink(ctx, p, file.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("uploads hidden again: %v", err)
	}
	got, err := x.r.GetRequest(ctx, p, q.ID)
	if err != nil || got.Status != rp.StatusInProgress || got.UndoUntil == nil || !got.UndoUntil.Equal(*q.UndoUntil) || !got.ReceivedAt.Equal(q.ReceivedAt) {
		t.Fatalf("the same request with its original times: %+v %v", got, err)
	}
	if n := count(x.outboxTypes(t), "rights.erasure_requested"); n != 1 {
		t.Fatalf("every module is told again (idempotent consumers): %d", n)
	}
	// Replaying again changes nothing (every instance replays at start-up).
	if n, err := x.r.ReplayJournal(ctx); err != nil || n != 0 {
		t.Fatalf("second replay: n=%d err=%v", n, err)
	}
	// The patient is not sent the request notice a second time.
	if k := x.note.kinds(); len(k) != 1 {
		t.Fatalf("notices: %v", k)
	}
	// The erasure carries on to the same hold as before the incident.
	if err := x.r.OnRightsTaskDone(ctx, taskDone(q.ID, "clinical", "restricted", "RET-02")); err != nil {
		t.Fatal(err)
	}
	got, _ = x.r.GetRequest(ctx, p, q.ID)
	if want := q.UndoUntil.Add(180 * day); got.Status != rp.StatusRestricted || got.HoldEndsAt == nil || !got.HoldEndsAt.Equal(want) {
		t.Fatalf("hold until %v: %+v", want, got)
	}
}

// An undo made after the backup was taken survives the restore: the account is released
// again instead of staying hidden against the patient's choice.
func TestErasureJournalReplaysUndo(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	p := x.patientRef(patient, true)
	file := x.upload(t, p, "Scan", pdf("synthetic"))
	q, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	during := x.backup(t)
	x.now = x.now.Add(time.Hour)
	p = x.patientRef(patient, true)
	if err := x.r.CancelErasure(ctx, p, q.ID); err != nil {
		t.Fatal(err)
	}
	x.restore(t, during, patient)
	if _, err := x.r.IssueDocumentLink(ctx, p, file.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("precondition: the restore re-hid the account: %v", err)
	}
	if n, err := x.r.ReplayJournal(ctx); err != nil || n != 1 {
		t.Fatalf("replay: n=%d err=%v", n, err)
	}
	got, _ := x.r.GetRequest(ctx, p, q.ID)
	if got.Status != rp.StatusCancelled {
		t.Fatalf("undo replayed: %+v", got)
	}
	if _, err := x.r.IssueDocumentLink(ctx, p, file.ID); err != nil {
		t.Fatalf("the account is visible again: %v", err)
	}
	if n := count(x.outboxTypes(t), "rights.erasure_cancelled"); n != 1 {
		t.Fatalf("modules are told about the undo: %d", n)
	}
	if n, err := x.r.ReplayJournal(ctx); err != nil || n != 0 {
		t.Fatalf("second replay: n=%d err=%v", n, err)
	}
}

// A restore to a point inside the hold, after the key was destroyed: the restored rows
// are unreadable (crypto-shredding) and are deleted again when the worker runs, and the
// erasure completes again.
func TestErasureAfterRestoreDeletesRestoredRowsAgain(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	p := x.patientRef(patient, true)
	x.upload(t, p, "Scan", pdf("synthetic"))
	q, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.r.OnRightsTaskDone(ctx, taskDone(q.ID, "clinical", "restricted", "RET-02")); err != nil {
		t.Fatal(err)
	}
	inHold := x.backup(t)
	q, _ = x.r.GetRequest(ctx, p, q.ID)
	x.now = q.HoldEndsAt.Add(time.Minute)
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := x.r.OnRightsTaskDone(ctx, taskDone(q.ID, "clinical", "erased", "")); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.r.GetRequest(ctx, p, q.ID); got.Status != rp.StatusCompleted {
		t.Fatalf("completed: %+v", got)
	}

	x.restore(t, inHold, patient)
	var docs int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document WHERE patient_ref=$1`, patient).Scan(&docs)
	if docs != 1 {
		t.Fatalf("precondition: the restore brought the upload row back: %d", docs)
	}
	if _, err := crypto.SealPatient(ctx, x.cr, patient, crypto.AAD{Table: "t", Column: "c", RowID: "r"}, []byte("x")); !errors.Is(err, crypto.ErrKeyDestroyed) {
		t.Fatalf("the destroyed key stays destroyed: %v", err)
	}
	if _, err := x.r.ReplayJournal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document WHERE patient_ref=$1`, patient).Scan(&docs)
	if docs != 0 {
		t.Fatalf("restored rows of an erased subject are deleted again: %d", docs)
	}
	if err := x.r.OnRightsTaskDone(ctx, taskDone(q.ID, "clinical", "erased", "")); err != nil {
		t.Fatal(err)
	}
	if got, _ := x.r.GetRequest(ctx, p, q.ID); got.Status != rp.StatusCompleted {
		t.Fatalf("completed again: %+v", got)
	}
}

// failingJournal refuses journal writes while down, standing in for an object-store outage.
type failingJournal struct {
	blobstore.LocalDir
	down atomic.Bool
}

func (f *failingJournal) PutWriteOnce(ctx context.Context, key string, data []byte) error {
	if f.down.Load() && strings.HasPrefix(key, "erasure-journal/") {
		return errors.New("object store unavailable")
	}
	return f.LocalDir.PutWriteOnce(ctx, key, data)
}

// A journal outage never blocks the patient's request; the worker writes the missing
// entries on its next run. A corrupt journal entry stops start-up (fail closed).
func TestErasureJournalCatchUpAndFailClosed(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	store := &failingJournal{LocalDir: x.blobs}
	r, err := records.NewRights(x.m, rightsConfig(), records.RightsDeps{Crypto: x.cr, Blobs: store, Scanner: x.scan, Notifier: x.note, AccessLog: x.audit})
	if err != nil {
		t.Fatal(err)
	}
	store.down.Store(true)
	p := x.patient(true)
	q, err := r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatalf("the request must not fail on a journal outage: %v", err)
	}
	if k := x.journalKeys(t); len(k) != 0 {
		t.Fatalf("nothing journaled during the outage: %v", k)
	}
	store.down.Store(false)
	if err := r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if k := x.journalKeys(t); len(k) != 1 || !strings.Contains(k[0], q.ID) {
		t.Fatalf("the worker catches up: %v", k)
	}
	if err := r.RunDue(ctx); err != nil {
		t.Fatalf("catch-up is idempotent: %v", err)
	}
	if err := x.blobs.PutWriteOnce(ctx, "erasure-journal/"+ids.New()+"/requested.json", []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReplayJournal(ctx); err == nil {
		t.Fatal("a corrupt journal entry must stop start-up")
	}
}

// An entry with an unknown event or a key that does not match its content stops start-up.
func TestErasureJournalRefusesForgedEntries(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	req, subj := ids.New(), ids.New()
	at := x.now.Format(time.RFC3339)
	for name, doc := range map[string]string{
		req + "/purge.json":           `{"v":1,"requestRef":"` + req + `","subjectRef":"` + subj + `","event":"purge","at":"` + at + `","receivedAt":"` + at + `","undoUntil":"` + at + `"}`,
		ids.New() + "/requested.json": `{"v":1,"requestRef":"` + req + `","subjectRef":"` + subj + `","event":"requested","at":"` + at + `","receivedAt":"` + at + `","undoUntil":"` + at + `"}`,
	} {
		y := setupRights(t)
		if err := y.blobs.PutWriteOnce(ctx, "erasure-journal/"+name, []byte(doc)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.r.ReplayJournal(ctx); err == nil {
			t.Fatalf("%s: a forged entry must stop start-up", name)
		}
	}
	_ = x
}
