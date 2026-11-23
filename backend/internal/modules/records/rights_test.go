package records_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	stdpng "image/png"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	malfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

const day = 24 * time.Hour

type notifier struct {
	mu   sync.Mutex
	got  []string
	fail error
}

func (n *notifier) NoticeToPhoneOnFile(_ context.Context, _, kind, ref string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, kind+":"+ref)
	return n.fail
}

func (n *notifier) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, g := range n.got {
		out = append(out, strings.SplitN(g, ":", 2)[0])
	}
	return out
}

type rx struct {
	*fx
	r     *records.Rights
	cr    *crypto.Service
	blobs blobstore.LocalDir
	scan  *malfake.Scanner
	note  *notifier
	audit *audit.Module
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func rightsConfig() records.RightsConfig {
	return records.RightsConfig{
		Modules: []string{"records", "clinical"}, RespondWithin: 15 * day, RecordCopyWithin: 72 * time.Hour, AckWithin: 24 * time.Hour,
		UndoWindow: 7 * day, HoldRegistration: 180 * day, HoldProcessing: 365 * day, HoldProcessingFrom: time.Date(2027, 5, 13, 0, 0, 0, 0, time.UTC),
		ExportAvailability: 24 * time.Hour, LinkTTL: 5 * time.Minute, UploadMaxBytes: 64 * 1024, ScanTimeout: 200 * time.Millisecond,
		ProhibitedValidators: []string{"verhoeff_12_digit_first_2_to_9", "digits_16"},
	}
}

func setupRights(t *testing.T) *rx {
	t.Helper()
	f := setup(t)
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	blobs := blobstore.LocalDir{Root: t.TempDir()}
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	x := &rx{fx: f, cr: cr, blobs: blobs, scan: &malfake.Scanner{}, note: &notifier{}}
	x.audit = audit.New(f.env.Pools.For("audit"), blobstore.LocalDir{Root: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	x.audit.SetClock(func() time.Time { return f.now })
	x.r, err = records.NewRights(f.m, rightsConfig(), records.RightsDeps{Crypto: cr, Blobs: blobs, Scanner: x.scan, Notifier: x.note, AccessLog: x.audit})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *rx) patient(stepUp bool) rp.Caller {
	return x.patientRef(ids.New(), stepUp)
}

func (x *rx) patientRef(ref string, stepUp bool) rp.Caller {
	c := rp.Caller{UserRef: ref, Role: "patient"}
	if stepUp {
		t := x.now
		c.StepUpAt = &t
	}
	return c
}

func pdf(body string) []byte { return []byte("%PDF-1.4\n" + body) }

func (x *rx) upload(t *testing.T, c rp.Caller, title string, data []byte) rp.DocumentInfo {
	t.Helper()
	d, err := x.r.Upload(context.Background(), c, title, "application/pdf", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func taskDone(ref, module, outcome, rule string) outbox.Event {
	p := map[string]string{"requestRef": ref, "module": module, "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Event{ID: ids.New(), Type: "rights.task_done", Version: 1, OccurredAt: time.Now(), Producer: module, Payload: p}
}

func (x *rx) outboxTypes(t *testing.T) []string {
	t.Helper()
	rows, err := x.env.Owner.Query(context.Background(), `SELECT type FROM records.event_outbox ORDER BY occurred_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func count(types []string, v string) int {
	n := 0
	for _, x := range types {
		if x == v {
			n++
		}
	}
	return n
}

// TC-63 (E-SEC-10): erasure and export without a fresh step-up are refused; with one,
// the account is restricted at once, the phone is told, and undo works inside the window
// but not after it.
func TestTC63ErasureNeedsStepUpAndUndoInsideWindow(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	p := x.patient(false)
	if _, err := x.r.RequestRights(ctx, p, rp.RightsErasure, ""); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("no step-up: %v", err)
	}
	if _, err := x.r.RequestRights(ctx, p, rp.RightsExport, ""); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("export without step-up: %v", err)
	}
	old := x.now.Add(-6 * time.Minute)
	p.StepUpAt = &old
	if _, err := x.r.RequestRights(ctx, p, rp.RightsErasure, ""); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("stale step-up (6 min): %v", err)
	}
	p = x.patientRef(p.UserRef, true)
	q, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != rp.StatusInProgress || q.UndoUntil == nil || !q.UndoUntil.Equal(x.now.Add(7*day)) {
		t.Fatalf("erasure state: %+v", q)
	}
	if got := x.note.kinds(); len(got) != 1 || got[0] != "erasure_requested" {
		t.Fatalf("notice to the phone on file: %v", got)
	}
	if n := count(x.outboxTypes(t), "rights.erasure_requested"); n != 1 {
		t.Fatalf("rights.erasure_requested emitted %d times", n)
	}
	if _, err := x.r.RequestRights(ctx, p, rp.RightsErasure, ""); !errors.Is(err, rp.ErrRestricted) {
		t.Fatalf("a second request while restricted: %v", err)
	}
	// Undo needs a step-up too.
	if err := x.r.CancelErasure(ctx, x.patientRef(p.UserRef, false), q.ID); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("undo without step-up: %v", err)
	}
	x.now = x.now.Add(3 * day)
	p = x.patientRef(p.UserRef, true)
	if err := x.r.CancelErasure(ctx, p, q.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := x.r.GetRequest(ctx, p, q.ID)
	if got.Status != rp.StatusCancelled {
		t.Fatalf("after undo: %+v", got)
	}
	if n := count(x.outboxTypes(t), "rights.erasure_cancelled"); n != 1 {
		t.Fatalf("rights.erasure_cancelled emitted %d times", n)
	}
	// The account is usable again and a new erasure can start; this time let the window pass.
	q2, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(7*day + time.Minute)
	p = x.patientRef(p.UserRef, true)
	if err := x.r.CancelErasure(ctx, p, q2.ID); !errors.Is(err, rp.ErrUndoClosed) {
		t.Fatalf("undo after the window: %v", err)
	}
	// Another person cannot undo it (looks like "not found").
	if err := x.r.CancelErasure(ctx, x.patient(true), q2.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("someone else's undo: %v", err)
	}
	// A restricted session (unverified device) is refused outright.
	rs := p
	rs.Restricted = true
	if _, err := x.r.RequestRights(ctx, rs, rp.RightsAccess, ""); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("restricted session: %v", err)
	}
}

// TC-03 (E-DP-03) and the two-step erasure: step 1 hides everything at once and restricts
// clinical records under RET-02; the legal-minimum hold (RET-18) runs; the patient key is
// destroyed only when the hold ends and every module has deleted; the report says what
// was kept and why.
func TestTC03ErasureTwoStepsWithHoldAndKeyDestruction(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	if _, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(60*day), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := x.m.GrantT2(ctx, patient, doc, clinic); err != nil {
		t.Fatal(err)
	}
	p := x.patientRef(patient, true)
	file := x.upload(t, p, "Blood test", pdf("synthetic lab report"))
	history := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory, ResourceType: "note", ResourceRef: ids.New()}
	own := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: doc, ResourceType: "note", ResourceRef: ids.New()}
	if d := mustDecide(t, x.m, history); !d.Allowed {
		t.Fatalf("before erasure the shared history is readable: %+v", d)
	}

	q, err := x.r.RequestRights(ctx, p, rp.RightsErasure, "")
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != rp.StatusInProgress {
		t.Fatalf("clinical has not reported yet: %+v", q)
	}
	// Step 1 hides the data from every role at once; only T2 own records (RET-02) stay reachable.
	if d := mustDecide(t, x.m, history); d.Allowed || !d.Hide {
		t.Fatalf("shared history must be hidden during erasure: %+v", d)
	}
	if d := mustDecide(t, x.m, own); !d.Allowed || d.Tier != rp.T2OwnRecords {
		t.Fatalf("the treating doctor keeps own-records access (RET-02): %+v", d)
	}
	if _, err := x.r.IssueDocumentLink(ctx, p, file.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("the app no longer shows the upload: %v", err)
	}
	// The clinical module reports: notes restricted under RET-02. Delivered twice (idempotent).
	ev := taskDone(q.ID, "clinical", "restricted", "RET-02")
	for i := 0; i < 2; i++ {
		if err := x.r.OnRightsTaskDone(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	q, _ = x.r.GetRequest(ctx, p, q.ID)
	wantHold := x.now.Add(7 * day).Add(180 * day)
	if q.Status != rp.StatusRestricted || q.HoldEndsAt == nil || !q.HoldEndsAt.Equal(wantHold) {
		t.Fatalf("step 1 done, hold running until %v: %+v", wantHold, q)
	}
	kept := map[string]string{}
	for _, it := range q.Report {
		kept[it.Module+"/"+it.RetentionRule] = it.Action
	}
	for k, want := range map[string]string{"clinical/RET-02": "restricted", "records/RET-05": "restricted", "records/RET-06": "restricted",
		"records/RET-09": "restricted", "records/RET-18": "erased_after_hold"} {
		if kept[k] != want {
			t.Fatalf("report %s: want %s, got %q (%+v)", k, want, kept[k], q.Report)
		}
	}
	// Nothing is deleted and the key is untouched during the hold.
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	x.now = wantHold.Add(-time.Hour)
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	var docs int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document WHERE patient_ref=$1`, patient).Scan(&docs)
	if docs != 1 {
		t.Fatalf("documents are kept during the hold, got %d", docs)
	}
	if _, err := crypto.SealPatient(ctx, x.cr, patient, crypto.AAD{Table: "t", Column: "c", RowID: "r"}, []byte("x")); err != nil {
		t.Fatalf("patient key must survive the hold: %v", err)
	}
	// The hold ends: step 2 starts, records deletes its data, but the key waits for clinical.
	x.now = wantHold.Add(time.Minute)
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	q, _ = x.r.GetRequest(ctx, p, q.ID)
	if q.Status != rp.StatusFinalising {
		t.Fatalf("waiting for clinical to delete: %+v", q)
	}
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document WHERE patient_ref=$1`, patient).Scan(&docs)
	if docs != 0 {
		t.Fatalf("records must have deleted the uploads, got %d", docs)
	}
	if _, err := crypto.SealPatient(ctx, x.cr, patient, crypto.AAD{Table: "t", Column: "c", RowID: "r"}, []byte("x")); err != nil {
		t.Fatalf("key must not be destroyed before every module has deleted: %v", err)
	}
	if n := count(x.outboxTypes(t), "rights.erasure_due"); n != 1 {
		t.Fatalf("rights.erasure_due emitted %d times", n)
	}
	if err := x.r.OnRightsTaskDone(ctx, taskDone(q.ID, "clinical", "erased", "")); err != nil {
		t.Fatal(err)
	}
	q, _ = x.r.GetRequest(ctx, p, q.ID)
	if q.Status != rp.StatusCompleted || q.CompletedAt == nil {
		t.Fatalf("erasure complete: %+v", q)
	}
	if _, err := crypto.SealPatient(ctx, x.cr, patient, crypto.AAD{Table: "t", Column: "c", RowID: "r"}, []byte("x")); !errors.Is(err, crypto.ErrKeyDestroyed) {
		t.Fatalf("patient key must be destroyed: %v", err)
	}
	for _, it := range q.Report {
		if it.Module == "records" && it.RetentionRule == "RET-18" && it.Action != "erased" {
			t.Fatalf("final report: %+v", q.Report)
		}
	}
	if k := x.note.kinds(); k[len(k)-1] != "erasure_completed" {
		t.Fatalf("completion notice: %v", k)
	}
	// Consent records, grants and the request itself are restricted, not deleted.
	var consents, grants int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.consent_record WHERE subject_ref=$1`, patient).Scan(&consents)
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.permission_grant WHERE patient_ref=$1`, patient).Scan(&grants)
	if consents == 0 || grants == 0 {
		t.Fatalf("legal-retention records must remain: consents=%d grants=%d", consents, grants)
	}
}

// A legal order (staff grant with reason legal_order) still reaches held data; other
// staff grants do not.
func TestErasureHoldHiddenFromStaffExceptLegalOrder(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	x.m.SetRoleChecker(allRoles{})
	patient, staff, approver := ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	mk := func(reason string, kind rp.DataKind, who string) {
		if _, err := x.m.GrantJIT(ctx, rp.JITRequest{StaffRef: who, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{kind},
			ReasonCode: reason, TicketRef: "T-" + reason, ApproverRef: approver, Duration: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	legal := ids.New()
	mk("complaint", rp.KindBooking, staff)
	mk("legal_order", rp.KindClinicalHistory, legal)
	if _, err := x.r.RequestRights(ctx, x.patientRef(patient, true), rp.RightsErasure, ""); err != nil {
		t.Fatal(err)
	}
	asGO := func(id string, k rp.DataKind) rp.Decision {
		return mustDecide(t, x.m, rp.Request{Actor: rp.Actor{Ref: id, Role: "grievance_officer"}, PatientRef: patient, Kind: k, ResourceType: "x", ResourceRef: ids.New()})
	}
	if d := asGO(staff, rp.KindBooking); d.Allowed {
		t.Fatalf("ordinary staff grants must not open held data: %+v", d)
	}
	if d := asGO(legal, rp.KindClinicalHistory); !d.Allowed {
		t.Fatalf("a verified legal order opens held data: %+v", d)
	}
}

type allRoles struct{}

func (allRoles) HasRole(context.Context, string, string) (bool, error) { return true, nil }

// Timers come from config: acknowledge, 15 days for rights requests, 72 hours for a copy
// of the medical records.
func TestRightsRequestDeadlines(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	p := x.patient(true)
	acc, err := x.r.RequestRights(ctx, p, rp.RightsAccess, "")
	if err != nil {
		t.Fatal(err)
	}
	if !acc.AckDueAt.Equal(x.now.Add(24*time.Hour)) || !acc.DueAt.Equal(x.now.Add(15*day)) || acc.AcknowledgedAt == nil {
		t.Fatalf("access timers: %+v", acc)
	}
	exp, err := x.r.RequestRights(ctx, p, rp.RightsExport, "")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.DueAt.Equal(x.now.Add(72 * time.Hour)) {
		t.Fatalf("a copy of medical records is due in 72 h: %+v", exp)
	}
	cor, err := x.r.RequestRights(ctx, p, rp.RightsCorrection, "My date of birth is wrong")
	if err != nil || !cor.DueAt.Equal(x.now.Add(15*day)) {
		t.Fatalf("correction: %+v %v", cor, err)
	}
	if _, err := x.r.RequestRights(ctx, p, rp.RightsCorrection, ""); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("a correction needs text: %v", err)
	}
	nom, err := x.r.RequestRights(ctx, p, rp.RightsNomination, `{"name":"Test Nominee","relation":"sibling","phone":"0000000000"}`)
	if err != nil || nom.Status != rp.StatusReceived {
		t.Fatalf("nomination: %+v %v", nom, err)
	}
	var sealed []byte
	_ = x.env.Owner.QueryRow(ctx, `SELECT details_sealed FROM records.rights_request WHERE id=$1`, nom.ID).Scan(&sealed)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte("Test Nominee")) {
		t.Fatal("nominee details must be stored encrypted")
	}
	if n := count(x.outboxTypes(t), "rights.request_received"); n != 4 {
		t.Fatalf("rights.request_received: %d", n)
	}
	list, _, err := x.r.ListRequests(ctx, p, "", 3)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	other := x.patient(true)
	if _, err := x.r.GetRequest(ctx, other, acc.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("someone else's request: %v", err)
	}
}

// Export: a JSON file of the patient's records data, encrypted at rest, delivered only
// through a single-use link bound to the patient.
func TestExportIsEncryptedAndDeliveredThroughLink(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	p := x.patientRef(patient, true)
	x.upload(t, p, "Discharge summary", pdf("body"))
	q, err := x.r.RequestRights(ctx, p, rp.RightsExport, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.IssueExportLink(ctx, p, q.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("no link before the export is ready: %v", err)
	}
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	q, _ = x.r.GetRequest(ctx, p, q.ID)
	if q.Status != rp.StatusCompleted || q.ExportExpiresAt == nil {
		t.Fatalf("export: %+v", q)
	}
	keys, _ := x.blobs.List(ctx, "exports/")
	if len(keys) != 1 {
		t.Fatalf("export blob: %v", keys)
	}
	raw, _ := x.blobs.Get(ctx, keys[0])
	if bytes.Contains(raw, []byte("P-CARE")) || bytes.Contains(raw, []byte("Discharge")) {
		t.Fatal("the export must be encrypted at rest")
	}
	l, err := x.r.IssueExportLink(ctx, p, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.Download(ctx, x.patient(true), l.Token); !errors.Is(err, rp.ErrLinkInvalid) {
		t.Fatalf("another user: %v", err)
	}
	f, err := x.r.Download(ctx, p, l.Token)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Consents  []map[string]any `json:"consents"`
		Documents []map[string]any `json:"documents"`
	}
	if err := json.Unmarshal(f.Data, &doc); err != nil || len(doc.Consents) != 3 || len(doc.Documents) != 1 || doc.Documents[0]["title"] != "Discharge summary" {
		t.Fatalf("export content: %v %s", err, f.Data)
	}
	if _, err := x.r.Download(ctx, p, l.Token); !errors.Is(err, rp.ErrLinkExpired) {
		t.Fatalf("reuse: %v", err)
	}
	// After the availability window the export is no longer offered.
	x.now = x.now.Add(25 * time.Hour)
	if _, err := x.r.IssueExportLink(ctx, p, q.ID); !errors.Is(err, rp.ErrLinkExpired) {
		t.Fatalf("after the window: %v", err)
	}
}

func verhoeffNumber() string {
	for i := 0; i < 100000; i++ {
		s := "23456789" + fmt.Sprintf("%04d", i)
		if scrub.Verhoeff(s) {
			return s
		}
	}
	return ""
}

// TC-14 (E-DP-20): EICAR, a script renamed .pdf, a mismatched type and an oversized file
// are rejected; a scanner that fails or times out rejects too; nothing is stored then.
func TestTC14UploadRejections(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	p := x.patient(false)
	up := func(title, ct string, data []byte) error {
		_, err := x.r.Upload(ctx, p, title, ct, bytes.NewReader(data))
		return err
	}
	stored := func() int {
		var n int
		_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document`).Scan(&n)
		ks, _ := x.blobs.List(ctx, "documents/")
		if n != len(ks) {
			t.Fatalf("rows and blobs disagree: %d vs %d", n, len(ks))
		}
		return n
	}
	if err := up("Scan", "application/pdf", pdf(malfake.EICAR)); !errors.Is(err, rp.ErrMalware) {
		t.Fatalf("EICAR: %v", err)
	}
	if err := up("Scan", "application/pdf", []byte("#!/bin/sh\nrm -rf /\n")); !errors.Is(err, rp.ErrFileType) {
		t.Fatalf("script renamed .pdf: %v", err)
	}
	if err := up("Scan", "application/pdf", []byte("<html><script>alert(1)</script></html>")); !errors.Is(err, rp.ErrFileType) {
		t.Fatalf("html: %v", err)
	}
	fakePNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}, rnd(16)...)
	if err := up("Scan", "image/png", fakePNG); !errors.Is(err, rp.ErrFileType) {
		t.Fatalf("PNG magic bytes with no image behind them (TM-42): %v", err)
	}
	var pb bytes.Buffer
	_ = stdpng.Encode(&pb, image.NewGray(image.Rect(0, 0, 8, 8)))
	png := pb.Bytes()
	if err := up("Scan", "application/pdf", png); !errors.Is(err, rp.ErrFileType) {
		t.Fatalf("png declared as pdf: %v", err)
	}
	if err := up("Scan", "image/png", png); err != nil {
		t.Fatalf("a real png: %v", err)
	}
	if err := up("Scan", "application/pdf", append(pdf(""), make([]byte, 70*1024)...)); !errors.Is(err, rp.ErrTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	if err := up("", "application/pdf", pdf("x")); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("empty title: %v", err)
	}
	if err := up("ref "+verhoeffNumber(), "application/pdf", pdf("x")); !errors.Is(err, rp.ErrProhibitedID) {
		t.Fatalf("a prohibited identifier in the title: %v", err)
	}
	if got := stored(); got != 1 {
		t.Fatalf("only the png may be stored, got %d", got)
	}
	// Scanner failure and timeout: rejected, never stored unscanned.
	x.scan.FailWith = adapters.E(adapters.Unavailable, "down")
	if err := up("Scan", "application/pdf", pdf("x")); !errors.Is(err, rp.ErrScanUnavailable) {
		t.Fatalf("scanner down: %v", err)
	}
	x.scan.FailWith, x.scan.Hang = nil, true
	if err := up("Scan", "application/pdf", pdf("x")); !errors.Is(err, rp.ErrScanUnavailable) {
		t.Fatalf("scanner timeout: %v", err)
	}
	if got := stored(); got != 1 {
		t.Fatalf("nothing unscanned may be stored, got %d", got)
	}
	// A restricted session and a non-patient cannot upload.
	x.scan.Hang = false
	rs := p
	rs.Restricted = true
	if _, err := x.r.Upload(ctx, rs, "Scan", "application/pdf", bytes.NewReader(pdf("x"))); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("restricted session: %v", err)
	}
	doc := p
	doc.Role = "doctor"
	if _, err := x.r.Upload(ctx, doc, "Scan", "application/pdf", bytes.NewReader(pdf("x"))); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("doctor: %v", err)
	}
}

// Uploads are encrypted at rest: no plaintext bytes in the blob store or the title column.
func TestUploadEncryptedAtRest(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	p := x.patient(false)
	secret := "PLAINTEXT-MARKER-synthetic-hba1c-7.1"
	d := x.upload(t, p, "Lab report title", pdf(secret))
	ks, _ := x.blobs.List(ctx, "documents/")
	if len(ks) != 1 {
		t.Fatalf("blobs: %v", ks)
	}
	raw, _ := x.blobs.Get(ctx, ks[0])
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("%PDF")) {
		t.Fatal("plaintext bytes found in the blob store")
	}
	var title []byte
	_ = x.env.Owner.QueryRow(ctx, `SELECT title_sealed FROM records.document WHERE id=$1`, d.ID).Scan(&title)
	if bytes.Contains(title, []byte("Lab report")) {
		t.Fatal("the title must be sealed")
	}
	list, err := x.r.ListDocuments(ctx, p)
	if err != nil || len(list) != 1 || list[0].Title != "Lab report title" {
		t.Fatalf("list: %v %+v", err, list)
	}
	// A ciphertext moved to another row fails to open (row-bound AAD).
	var sealed crypto.Sealed[crypto.PatientKey]
	_ = sealed.Scan(raw)
	if _, err := crypto.Open(ctx, x.cr, crypto.AAD{Table: "document", Column: "file", RowID: ids.New()}, sealed); err == nil {
		t.Fatal("a file sealed for one row must not open for another")
	}
}

// TC-15 (E-DP-21): a link is refused after expiry, for another user and on a second use;
// the owner still gets the file on the first try; a doctor's link re-checks the grant.
func TestTC15SecureLinks(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	p := x.patientRef(patient, false)
	secret := "synthetic-secret-content"
	d := x.upload(t, p, "Report", pdf(secret))

	l, err := x.r.IssueDocumentLink(ctx, p, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Token) < 32 || !l.ExpiresAt.Equal(x.now.Add(5*time.Minute)) {
		t.Fatalf("link: %+v", l)
	}
	var plainHits int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.file_link WHERE encode(token_hash,'hex')=$1`, l.Token).Scan(&plainHits)
	if plainHits != 0 {
		t.Fatal("only a hash of the token may be stored")
	}
	if _, err := x.r.Download(ctx, x.patient(false), l.Token); !errors.Is(err, rp.ErrLinkInvalid) {
		t.Fatalf("another user: %v", err)
	}
	if _, err := x.r.Download(ctx, p, "0123456789abcdef0123456789abcdef"); !errors.Is(err, rp.ErrLinkInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
	f, err := x.r.Download(ctx, p, l.Token)
	if err != nil || !bytes.Contains(f.Data, []byte(secret)) || f.ContentType != "application/pdf" {
		t.Fatalf("first use by the owner: %v", err)
	}
	if _, err := x.r.Download(ctx, p, l.Token); !errors.Is(err, rp.ErrLinkExpired) {
		t.Fatalf("second use: %v", err)
	}
	l2, _ := x.r.IssueDocumentLink(ctx, p, d.ID)
	x.now = x.now.Add(6 * time.Minute)
	if _, err := x.r.Download(ctx, p, l2.Token); !errors.Is(err, rp.ErrLinkExpired) {
		t.Fatalf("after expiry: %v", err)
	}

	// A doctor without a grant gets nothing; with a T3 share the link works; after the
	// patient revokes, a link issued earlier is refused at download.
	dc := rp.Caller{UserRef: doc, Role: "doctor", ClinicRef: clinic}
	if _, err := x.r.IssueDocumentLink(ctx, dc, d.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("doctor without a relationship: %v", err)
	}
	gid, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(7*day), versions["P-SHARE"], "PN-1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	dl, err := x.r.IssueDocumentLink(ctx, dc, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.m.Revoke(ctx, gid, patient, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.Download(ctx, dc, dl.Token); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("access re-checked at download: %v", err)
	}
	if x.auditCount(t, "denied") == 0 {
		t.Fatal("the refused read must be audited")
	}
	// A restricted session cannot use a link.
	l3, _ := x.r.IssueDocumentLink(ctx, p, d.ID)
	rs := p
	rs.Restricted = true
	if _, err := x.r.Download(ctx, rs, l3.Token); err == nil {
		t.Fatal("restricted session must be refused")
	}
}

// S-170: the patient's access log shows reads by people only, not denials, not their own.
func TestAccessLogShowsReadsByPeopleOnly(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking, stranger := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	if _, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(7*day), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	rq := func(actor rp.Actor, k rp.DataKind) {
		mustDecide(t, x.m, rp.Request{Actor: actor, PatientRef: patient, Kind: k, ResourceType: "note", ResourceRef: ids.New()})
	}
	rq(doctor(doc, clinic), rp.KindClinicalHistory)                     // allowed, by a person
	rq(doctor(stranger, clinic), rp.KindClinicalHistory)                // denied
	rq(rp.Actor{Ref: patient, Role: "patient"}, rp.KindClinicalHistory) // the patient themself
	if _, err := x.audit.ChainBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	es, _, err := x.r.AccessLog(ctx, x.patientRef(patient, false), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].ActorRef != doc || es[0].ActorRole != "doctor" || es[0].Kind != "clinical_history" || es[0].Purpose != string(rp.T3HistoryShare) {
		t.Fatalf("access log: %+v", es)
	}
	if _, _, err := x.r.AccessLog(ctx, rp.Caller{UserRef: doc, Role: "doctor"}, "", 20); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("only the patient reads their log: %v", err)
	}
}

// failingBlobs fails every write: the upload must leave no row behind (review M6).
type failingBlobs struct{ blobstore.LocalDir }

func (failingBlobs) PutWriteOnce(context.Context, string, []byte) error {
	return errors.New("disk full")
}

// Review M6: per-patient document quota (count and bytes), and no row without its blob.
func TestReviewM6UploadQuotaAndNoOrphans(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	cfg := rightsConfig()
	cfg.MaxDocuments, cfg.MaxTotalBytes = 2, 1024
	r2, err := records.NewRights(x.fx.m, cfg, records.RightsDeps{Crypto: x.cr, Blobs: x.blobs, Scanner: x.scan, Notifier: x.note, AccessLog: x.audit})
	if err != nil {
		t.Fatal(err)
	}
	p := x.patient(false)
	up := func(data []byte) error {
		_, err := r2.Upload(ctx, p, "Scan", "application/pdf", bytes.NewReader(data))
		return err
	}
	if err := up(pdf("a")); err != nil {
		t.Fatal(err)
	}
	if err := up(pdf("b")); err != nil {
		t.Fatal(err)
	}
	if err := up(pdf("c")); !errors.Is(err, rp.ErrQuota) {
		t.Fatalf("count quota: %v", err)
	}
	other := x.patient(false)
	if _, err := r2.Upload(ctx, other, "Big", "application/pdf", bytes.NewReader(append(pdf("x"), make([]byte, 2000)...))); !errors.Is(err, rp.ErrQuota) {
		t.Fatalf("byte quota: %v", err)
	}
	var n int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document`).Scan(&n)
	if ks, _ := x.blobs.List(ctx, "documents/"); n != 2 || len(ks) != 2 {
		t.Fatalf("rows=%d blobs=%d", n, len(ks))
	}
	// A failed blob write leaves no row.
	r3, _ := records.NewRights(x.fx.m, rightsConfig(), records.RightsDeps{Crypto: x.cr, Blobs: failingBlobs{x.blobs}, Scanner: x.scan, Notifier: x.note, AccessLog: x.audit})
	if _, err := r3.Upload(ctx, x.patient(false), "Scan", "application/pdf", bytes.NewReader(pdf("z"))); err == nil {
		t.Fatal("expected failure")
	}
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document`).Scan(&n)
	if n != 2 {
		t.Fatalf("row left behind: %d", n)
	}
}

// Review M6: finished exports are purged after ExportAvailability (blob deleted, row marked gone).
func TestReviewM6ExpiredExportIsPurged(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	p := x.patientRef(patient, true)
	q, err := x.r.RequestRights(ctx, p, rp.RightsExport, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := x.blobs.List(ctx, "exports/"); len(ks) != 1 {
		t.Fatalf("export blob: %v", ks)
	}
	x.now = x.now.Add(25 * time.Hour)
	if err := x.r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if ks, _ := x.blobs.List(ctx, "exports/"); len(ks) != 0 {
		t.Fatalf("blob must be deleted: %v", ks)
	}
	var gone bool
	_ = x.env.Owner.QueryRow(ctx, `SELECT export_blob_key IS NULL FROM records.rights_request WHERE id=$1`, q.ID).Scan(&gone)
	if !gone {
		t.Fatal("export must be marked gone")
	}
}
