package clinical_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// S-169: the doctor's view of a shared history. The real gate is covered in the records
// module (TC-01, TC-02, S-168 tests); here the gate is a stateful fake so the view's own
// duties are tested: signed records of OTHER doctors only, expiry data, refusals, audit.

type histGate struct {
	mu   sync.Mutex
	dec  rp.Decision
	err  error
	reqs []rp.Request
}

func (g *histGate) Authorize(_ context.Context, r rp.Request) (rp.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = append(g.reqs, r)
	return g.dec, g.err
}
func (g *histGate) HasConsent(context.Context, string, string) (bool, error) { return true, nil }
func (g *histGate) set(d rp.Decision, err error)                             { g.mu.Lock(); g.dec, g.err = d, err; g.mu.Unlock() }
func (g *histGate) calls() int                                               { g.mu.Lock(); defer g.mu.Unlock(); return len(g.reqs) }

type fakeWindows struct {
	mu    sync.Mutex
	until time.Time
	ok    bool
	err   error
}

func (w *fakeWindows) HistoryAccessUntil(context.Context, string, string) (time.Time, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.until, w.ok, w.err
}

type histfx struct {
	*rxfx
	h       *clinical.History
	gate    *histGate
	win     *fakeWindows
	now     time.Time
	viewer  string
	authors []string
}

const histMarker = "synthetic-history-marker"

func histSetup(t *testing.T) *histfx {
	t.Helper()
	f := rxSetup(t, true)
	hf := &histfx{rxfx: f, gate: &histGate{dec: rp.Decision{Allowed: true, Tier: rp.T3HistoryShare, GrantID: ids.New()}},
		win: &fakeWindows{ok: true}, now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), viewer: ids.New()}
	hf.win.until = hf.now.Add(2 * time.Hour)
	h, err := clinical.NewHistory(f.m, clinical.HistoryConfig{WarnBefore: 10 * time.Minute}, clinical.HistoryDeps{
		Crypto: f.cr, Gate: hf.gate, Windows: hf.win, Verifier: f.ver, Now: func() time.Time { return hf.now }})
	if err != nil {
		t.Fatal(err)
	}
	hf.h = h
	return hf
}

func (f *histfx) viewerCaller() clinical.Caller {
	return clinical.Caller{UserID: f.viewer, Role: "doctor", ClinicRef: f.clinic, RequestID: ids.New()}
}

// author writes a note and a prescription for the patient as another doctor. Signed or left as drafts.
func (f *histfx) author(t *testing.T, doc string, sign bool) (appt string) {
	t.Helper()
	ctx := context.Background()
	appt = ids.New()
	facts := f.facts(rules.ModeVideo)
	facts.DoctorRef, facts.AppointmentRef = doc, appt
	f.cons.m[appt] = facts
	c := clinical.Caller{UserID: doc, Role: "doctor", ClinicRef: f.clinic, RequestID: ids.New()}
	if _, err := f.rx.SaveNote(ctx, c, appt, clinical.NoteContent{Complaint: histMarker + "-" + doc[:8], Findings: "synthetic findings", Diagnosis: "synthetic diagnosis", Advice: "rest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rx.SaveDraft(ctx, c, appt, clinical.DraftContent{Items: []clinical.ItemInput{item("l1", "paracetamol")}}); err != nil {
		t.Fatal(err)
	}
	if sign {
		now := time.Now()
		c.StepUpAt = &now
		if _, err := f.rx.Sign(ctx, c, appt); err != nil {
			t.Fatal(err)
		}
	}
	f.authors = append(f.authors, doc)
	return appt
}

func (f *histfx) auditCount(t *testing.T, action, result string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action=$1 AND result=$2`, action, result)
}

func TestS169HistoryShowsOnlySignedRecordsOfOtherDoctors(t *testing.T) {
	f := histSetup(t)
	ctx := context.Background()
	signedBy := ids.New()
	draftBy := ids.New()
	f.author(t, signedBy, true)
	f.author(t, draftBy, false) // draft note and draft prescription of another doctor
	f.author(t, f.viewer, true) // the viewer's own records are not "history"

	v, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 2 {
		t.Fatalf("want the signed note and prescription of one other doctor, got %d", len(v.Items))
	}
	kinds := map[string]bool{}
	for _, it := range v.Items {
		kinds[it.Kind] = true
		if it.AuthorRef != signedBy {
			t.Fatalf("item by %s, want only %s", it.AuthorRef, signedBy)
		}
		if it.Note != nil && (it.Note.State != "signed" || !strings.Contains(it.Note.Content.Complaint, histMarker) || it.Note.SignedAt == nil) {
			t.Fatalf("note: %+v", it.Note)
		}
		if it.Rx != nil && (it.Rx.State != "signed" || len(it.Rx.Content.Items) != 1 || it.Rx.Content.Items[0].DrugRef != "paracetamol" || len(it.Rx.Document) != 0) {
			t.Fatalf("prescription: %+v", it.Rx)
		}
	}
	if !kinds["note"] || !kinds["prescription"] {
		t.Fatalf("kinds %v", kinds)
	}
	// The gate was asked for the history kind, for this doctor and patient.
	if f.gate.calls() != 1 || f.gate.reqs[0].Kind != rp.KindClinicalHistory || f.gate.reqs[0].PatientRef != f.pat || f.gate.reqs[0].Actor.Ref != f.viewer {
		t.Fatalf("gate calls: %+v", f.gate.reqs)
	}
	// An amendment in progress does not replace or hide the signed version (K-CS-05).
	f.mustAmend(t, signedBy)
	v, err = f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, "")
	if err != nil || len(v.Items) != 2 {
		t.Fatalf("after amend draft: %d %v", len(v.Items), err)
	}
	for _, it := range v.Items {
		if it.Rx != nil && it.Rx.Version != 1 {
			t.Fatalf("draft version leaked: %+v", it.Rx)
		}
	}
}

func (f *histfx) mustAmend(t *testing.T, doc string) {
	t.Helper()
	var rxRef string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT id::text FROM clinical.prescription WHERE doctor_ref=$1`, doc).Scan(&rxRef); err != nil {
		t.Fatal(err)
	}
	c := clinical.Caller{UserID: doc, Role: "doctor", ClinicRef: f.clinic, RequestID: ids.New()}
	d, err := f.rx.Amend(context.Background(), c, rxRef)
	if err != nil || d.State != "draft" {
		t.Fatalf("amend: %+v %v", d, err)
	}
}

// TC-02 (E-DP-02): the response says when access ends and when to warn (10 minutes before).
func TestTC02HistoryCarriesExpiryWarning(t *testing.T) {
	f := histSetup(t)
	ctx := context.Background()
	f.author(t, ids.New(), true)

	v, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Access.ExpiresAt.Equal(f.win.until) || !v.Access.WarnAt.Equal(f.win.until.Add(-10*time.Minute)) || v.Access.Warn {
		t.Fatalf("2 h left: %+v", v.Access)
	}
	f.now = f.win.until.Add(-9 * time.Minute) // inside the warning window
	v, err = f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, "")
	if err != nil || !v.Access.Warn {
		t.Fatalf("9 min left: %+v %v", v.Access, err)
	}
	if v.Access.Tier != rp.T3HistoryShare || v.Access.GrantID == "" {
		t.Fatalf("grant data missing: %+v", v.Access)
	}
}

// TC-01 (E-DP-01) and S-169 acceptance through the HTTP handler: once the gate refuses, the
// next request is 403 consent.required with a reason code only, and no clinical data is read.
func TestTC01HandlerDeniesAfterRevokeWithRequestAccessPrompt(t *testing.T) {
	f := histSetup(t)
	f.author(t, ids.New(), true)
	h := httpapi.Handlers{H: f.h}
	ctx := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: f.viewer, ActingRole: ip.RoleDoctor, ActingClinic: f.clinic})
	pat := mustUUID(t, f.pat)

	res, err := h.GetPatientHistory(ctx, httpapi.GetPatientHistoryRequestObject{PatientId: pat})
	if err != nil {
		t.Fatal(err)
	}
	if ok := res.(httpapi.GetPatientHistory200JSONResponse); len(ok.Items) != 2 || ok.Access.Warn {
		t.Fatalf("%+v", ok)
	}

	// The patient revokes (or the share ends): the gate now refuses with grant_expired.
	f.gate.set(rp.Decision{Reason: "grant_expired"}, nil)
	_, err = h.GetPatientHistory(ctx, httpapi.GetPatientHistoryRequestObject{PatientId: pat})
	var pr *problem.Problem
	if !errors.As(err, &pr) || pr.Status != 403 || pr.Code != "consent.required" || len(pr.Errors) != 1 || pr.Errors[0].Code != "grant_expired" {
		t.Fatalf("after revoke: %#v", err)
	}
	if strings.Contains(pr.Detail+pr.Title, histMarker) {
		t.Fatal("the problem must carry no clinical data")
	}

	// A doctor with no relationship with the patient: 404, indistinguishable from a missing patient.
	f.gate.set(rp.Decision{Reason: "no_relationship", Hide: true}, nil)
	_, err = h.GetPatientHistory(ctx, httpapi.GetPatientHistoryRequestObject{PatientId: pat})
	if !errors.As(err, &pr) || pr.Status != 404 {
		t.Fatalf("unrelated doctor: %#v", err)
	}
}

func TestS169HandlerFailsClosed(t *testing.T) {
	f := histSetup(t)
	pat := mustUUID(t, f.pat)
	doctorCtx := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: f.viewer, ActingRole: ip.RoleDoctor})
	patientCtx := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: f.pat, ActingRole: ip.RolePatient})
	if _, err := (httpapi.Handlers{}).GetPatientHistory(doctorCtx, httpapi.GetPatientHistoryRequestObject{PatientId: pat}); !errors.Is(err, problem.Unavailable) {
		t.Fatalf("not wired: %v", err)
	}
	if _, err := (httpapi.Handlers{H: f.h}).GetPatientHistory(patientCtx, httpapi.GetPatientHistoryRequestObject{PatientId: pat}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("a patient on the doctor route: %v", err)
	}
	if f.gate.calls() != 0 {
		t.Fatal("no gate call expected")
	}
}

func TestS169UnverifiedDoctorRefusedAndAudited(t *testing.T) {
	f := histSetup(t)
	f.author(t, ids.New(), true)
	f.ver.mu.Lock()
	f.ver.bad[f.viewer] = true
	f.ver.mu.Unlock()
	_, err := f.h.PatientHistory(context.Background(), f.viewerCaller(), f.pat, 50, "")
	if !errors.Is(err, clinical.ErrDoctorNotVerified) {
		t.Fatalf("unverified: %v", err)
	}
	if f.gate.calls() != 0 {
		t.Fatal("no data path may be reached")
	}
	if f.auditCount(t, "records.read.clinical_history", "denied") != 1 {
		t.Fatal("the refusal must be audited")
	}
}

func TestS169FailsClosedOnGateOrWindowErrors(t *testing.T) {
	f := histSetup(t)
	f.author(t, ids.New(), true)
	ctx := context.Background()

	f.gate.set(rp.Decision{}, errors.New("audit down"))
	if v, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, ""); err == nil || len(v.Items) != 0 {
		t.Fatalf("gate error must refuse: %v", err)
	}
	f.gate.set(rp.Decision{Allowed: true, Tier: rp.T3HistoryShare, GrantID: ids.New()}, nil)
	f.win.ok = false // allowed, but no known end: refuse
	var de *clinical.DeniedError
	if _, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, ""); !errors.As(err, &de) || de.Hide {
		t.Fatalf("no window: %v", err)
	}
	f.win.ok, f.win.err = true, errors.New("down")
	if _, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 50, ""); err == nil {
		t.Fatal("window error must refuse")
	}
	// Wrong role and the patient's own ID are plain not-found.
	c := f.viewerCaller()
	c.Role = "patient"
	if _, err := f.h.PatientHistory(ctx, c, f.pat, 50, ""); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("role: %v", err)
	}
	c = f.viewerCaller()
	if _, err := f.h.PatientHistory(ctx, c, c.UserID, 50, ""); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("self: %v", err)
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestS169HistoryPagesWithoutRepeatsOrGaps(t *testing.T) {
	f := histSetup(t)
	ctx := context.Background()
	f.author(t, ids.New(), true)
	f.author(t, ids.New(), true) // four records in all: two notes and two prescriptions
	seen := map[string]bool{}
	after := ""
	for pages := 0; pages < 5; pages++ {
		v, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 3, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range v.Items {
			k := it.Kind
			if it.Note != nil {
				k += it.Note.NoteRef
			} else {
				k += it.Rx.PrescriptionRef
			}
			if seen[k] {
				t.Fatalf("repeated %s", k)
			}
			seen[k] = true
		}
		if v.Next == "" {
			break
		}
		after = v.Next
	}
	if len(seen) != 4 {
		t.Fatalf("saw %d of 4", len(seen))
	}
	if _, err := f.h.PatientHistory(ctx, f.viewerCaller(), f.pat, 3, "garbage"); err == nil {
		t.Fatal("a bad cursor must be refused")
	}
}
