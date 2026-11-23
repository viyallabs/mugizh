package records_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records/httpapi"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// ---- fakes of the consumer interfaces ----

type fakeAppts struct {
	mu sync.Mutex
	m  map[string]rp.AppointmentFacts // patient|doctor|appointment
}

func (a *fakeAppts) ConfirmedAppointment(_ context.Context, patient, doctor, appt string) (rp.AppointmentFacts, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.m[patient+"|"+doctor+"|"+appt]
	if !ok {
		return rp.AppointmentFacts{}, rp.ErrNoAppointment
	}
	return f, nil
}

type fakeVer struct {
	mu  sync.Mutex
	bad map[string]bool
	err error
}

func (v *fakeVer) IsVerified(_ context.Context, ref string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return true, v.err // a lookup error must count as not verified even if ok is true
	}
	return !v.bad[ref], nil
}

type sfx struct {
	*rx
	s     *records.Sharing
	appts *fakeAppts
	ver   *fakeVer

	patient, doc, clinic, appt string
	endsAt                     time.Time
}

var shareCfg = records.ShareConfig{DefaultAfterAppointment: 7 * day, ExtensionMax: 24 * time.Hour, WarnBefore: 10 * time.Minute, RequestTTL: 7 * day}

func shareSetup(t *testing.T) *sfx {
	t.Helper()
	x := setupRights(t)
	f := &sfx{rx: x, appts: &fakeAppts{m: map[string]rp.AppointmentFacts{}}, ver: &fakeVer{bad: map[string]bool{}},
		patient: ids.New(), doc: ids.New(), clinic: ids.New(), appt: ids.New()}
	f.endsAt = x.now.Add(30 * time.Minute)
	f.appts.m[f.patient+"|"+f.doc+"|"+f.appt] = rp.AppointmentFacts{ClinicRef: f.clinic, EndsAt: f.endsAt}
	var err error
	if f.s, err = records.NewSharing(x.m, shareCfg, f.appts, f.ver); err != nil {
		t.Fatal(err)
	}
	x.consent(t, f.patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	return f
}

func (f *sfx) shareIn() rp.ShareInput {
	return rp.ShareInput{DoctorRef: f.doc, AppointmentRef: f.appt, TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}
}

func (f *sfx) historyReq(doc string) rp.Request {
	return rp.Request{Actor: doctor(doc, f.clinic), PatientRef: f.patient, Kind: rp.KindClinicalHistory, ResourceType: "clinical_history"}
}

func (f *sfx) decide(t *testing.T, doc string) rp.Decision {
	t.Helper()
	return mustDecide(t, f.m, f.historyReq(doc))
}

func (f *sfx) advance(d time.Duration) {
	f.now = f.now.Add(d)
	f.m.FlushCache()
}

func (f *sfx) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *sfx) auditN(t *testing.T, action, result string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action=$1 AND result=$2`, action, result)
}

// ---- S-168: creating the share ----

func TestS168ShareRecordsConsentAndGrantWithDefaultExpiry(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()

	// The P-SHARE text version must be the one in force; nothing is created otherwise.
	bad := f.shareIn()
	bad.TextVersion = versions["P-SHARE"] + 1
	if _, err := f.s.Share(ctx, f.patient, bad); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("wrong text version: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM records.permission_grant WHERE tier='T3_history_share'`); n != 0 {
		t.Fatalf("grants after refusal: %d", n)
	}
	if ok, _ := f.m.HasConsent(ctx, f.patient, "P-SHARE"); ok {
		t.Fatal("no consent may be recorded for the wrong text")
	}

	// Default: the appointment end plus 7 days.
	r, err := f.s.Share(ctx, f.patient, f.shareIn())
	if err != nil {
		t.Fatal(err)
	}
	if want := f.endsAt.Add(7 * day); !r.ExpiresAt.Equal(want) || r.DoctorRef != f.doc {
		t.Fatalf("expiry %v want %v", r.ExpiresAt, want)
	}
	if n := f.count(t, `SELECT count(*) FROM records.consent_record WHERE purpose_id='P-SHARE' AND text_version=$1 AND action='grant'`, versions["P-SHARE"]); n != 1 {
		t.Fatalf("P-SHARE consent records: %d", n)
	}
	var tier, scope, createdFrom string
	if err := f.env.Owner.QueryRow(ctx, `SELECT tier, scope_ref, created_from FROM records.permission_grant WHERE id=$1`, r.GrantID).Scan(&tier, &scope, &createdFrom); err != nil {
		t.Fatal(err)
	}
	if tier != string(rp.T3HistoryShare) || scope != f.appt || createdFrom != "share.patient" {
		t.Fatalf("grant: %s %s %s", tier, scope, createdFrom)
	}

	// The tap is idempotent while the share is active.
	again, err := f.s.Share(ctx, f.patient, f.shareIn())
	if err != nil || again.GrantID != r.GrantID {
		t.Fatalf("repeat: %+v %v", again, err)
	}

	// The shared doctor reads history, audited; another doctor has no relationship (404).
	if d := f.decide(t, f.doc); !d.Allowed || d.Tier != rp.T3HistoryShare || d.GrantID != r.GrantID {
		t.Fatalf("shared doctor: %+v", d)
	}
	if d := f.decide(t, ids.New()); d.Allowed || !d.Hide {
		t.Fatalf("other doctor: %+v", d)
	}
	if f.auditN(t, "records.read.clinical_history", "allowed") != 1 || f.auditN(t, "records.read.clinical_history", "denied") != 1 {
		t.Fatal("every read, allowed or denied, must be audited")
	}
	if f.auditN(t, "permission.created", "allowed") < 1 || f.auditN(t, "consent.grant", "allowed") < 1 {
		t.Fatal("consent and grant creation must be audited")
	}
}

func TestS168ShareScopeAndChosenExpiry(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()

	// A shorter end chosen by the patient is honoured; a later one is refused.
	short := f.now.Add(3 * time.Hour)
	in := f.shareIn()
	in.Until = &short
	r, err := f.s.Share(ctx, f.patient, in)
	if err != nil || !r.ExpiresAt.Equal(short) {
		t.Fatalf("%+v %v", r, err)
	}
	late := f.endsAt.Add(7*day + time.Second)
	in.Until = &late
	if _, err := f.s.Share(ctx, f.patient, in); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("too late: %v", err)
	}
	past := f.now.Add(-time.Minute)
	in.Until = &past
	if _, err := f.s.Share(ctx, f.patient, in); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("in the past: %v", err)
	}

	// The share covers history and documents, not the doctor's current-consult data (that is T1).
	if d := f.decide(t, f.doc); !d.Allowed {
		t.Fatalf("history: %+v", d)
	}
	docs := f.historyReq(f.doc)
	docs.Kind = rp.KindDocuments
	if d := mustDecide(t, f.m, docs); !d.Allowed || d.Tier != rp.T3HistoryShare {
		t.Fatalf("documents: %+v", d)
	}
	for _, k := range []rp.DataKind{rp.KindPayment, rp.KindBooking} {
		q := f.historyReq(f.doc)
		q.Kind = k
		if d := mustDecide(t, f.m, q); d.Allowed {
			t.Fatalf("a history share must not open %s: %+v", k, d)
		}
	}

	// An appointment whose default window has already passed cannot be shared.
	f.advance(8*day + time.Hour)
	in = f.shareIn()
	if _, err := f.s.Share(ctx, f.patient, in); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("old appointment: %v", err)
	}
}

func TestS168ShareNeedsVerifiedDoctorAndConfirmedAppointment(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()

	f.ver.bad[f.doc] = true
	if _, err := f.s.Share(ctx, f.patient, f.shareIn()); !errors.Is(err, rp.ErrDoctorNotVerified) {
		t.Fatalf("unverified: %v", err)
	}
	f.ver.bad[f.doc] = false
	f.ver.err = errors.New("verification down")
	if _, err := f.s.Share(ctx, f.patient, f.shareIn()); !errors.Is(err, rp.ErrDoctorNotVerified) {
		t.Fatalf("lookup error must fail closed: %v", err)
	}
	f.ver.err = nil

	other := f.shareIn()
	other.AppointmentRef = ids.New() // not a confirmed appointment with this doctor
	if _, err := f.s.Share(ctx, f.patient, other); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("unknown appointment: %v", err)
	}
	if _, err := f.s.Share(ctx, ids.New(), f.shareIn()); !errors.Is(err, rp.ErrNotFound) { // someone else's appointment
		t.Fatalf("someone else's appointment: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM records.permission_grant WHERE tier IN ('T3_history_share','T4_extension')`); n != 0 {
		t.Fatalf("grants after refusals: %d", n)
	}
	if _, err := f.s.Share(ctx, f.patient, f.shareIn()); err != nil {
		t.Fatalf("now valid: %v", err)
	}
}

// ---- TC-01 through the HTTP handlers ----

func principal(ref, role string) context.Context {
	return ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ref, ActingRole: role})
}

func TestTC01RevokeThroughHandlersBlocksNextReadAndClearsCache(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	h := httpapi.Handlers{M: f.m, R: f.r, S: f.s}
	pctx, dctx := principal(f.patient, ip.RolePatient), principal(f.doc, ip.RoleDoctor)
	f.upload(t, f.patientRef(f.patient, false), "synthetic report", pdf("body"))

	res, err := h.CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: &httpapi.HistoryShareCreate{
		DoctorId: uuid.MustParse(f.doc), AppointmentId: uuid.MustParse(f.appt), TextVersion: versions["P-SHARE"], NoticeVersion: ptr("PN-1.0")}})
	if err != nil {
		t.Fatal(err)
	}
	grant := res.(httpapi.CreateHistoryShare201JSONResponse)

	// A second API instance with a long cache TTL, listening for changes.
	other := f.newInstance(t, 60*time.Second)
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go other.Listen(lctx)
	time.Sleep(300 * time.Millisecond)

	// The doctor opens the shared list (documents) and warms both caches.
	docs, err := h.ListPatientDocuments(dctx, httpapi.ListPatientDocumentsRequestObject{PatientId: uuid.MustParse(f.patient)})
	if err != nil {
		t.Fatal(err)
	}
	if l := docs.(httpapi.ListPatientDocuments200JSONResponse); len(l.Items) != 1 || l.AccessExpiresAt == nil || !l.AccessExpiresAt.Equal(grant.ExpiresAt) {
		t.Fatalf("%+v", l)
	}
	if d := mustDecide(t, other, f.historyReq(f.doc)); !d.Allowed {
		t.Fatalf("other instance: %+v", d)
	}

	// The patient revokes.
	if _, err := h.RevokeAccessGrant(pctx, httpapi.RevokeAccessGrantRequestObject{GrantId: grant.GrantId}); err != nil {
		t.Fatal(err)
	}

	// The doctor's very next request is 403 consent.required on this instance...
	_, err = h.ListPatientDocuments(dctx, httpapi.ListPatientDocumentsRequestObject{PatientId: uuid.MustParse(f.patient)})
	var pr *problem.Problem
	if !errors.As(err, &pr) || pr.Status != 403 || pr.Code != "consent.required" {
		t.Fatalf("after revoke: %#v", err)
	}
	if d := f.decide(t, f.doc); d.Allowed || d.Hide || d.Reason != "grant_expired" {
		t.Fatalf("history after revoke: %+v", d)
	}
	// ...and on the other instance once its cache entry is dropped by NOTIFY.
	deadline := time.Now().Add(5 * time.Second)
	for mustDecide(t, other, f.historyReq(f.doc)).Allowed {
		if time.Now().After(deadline) {
			t.Fatal("other instance still allows access after revoke")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok, _ := f.m.HistoryAccessUntil(ctx, f.patient, f.doc); ok {
		t.Fatal("no active window may remain after revoke")
	}
	if f.auditN(t, "permission.revoked", "allowed") != 1 || f.auditN(t, "records.read.documents", "denied") < 1 || f.auditN(t, "records.read.documents", "allowed") < 1 {
		t.Fatal("grant, revoke, allowed and denied reads must all be audited")
	}
}

func ptr[T any](v T) *T { return &v }

// ---- TC-02: expiry and extension ----

func TestTC02ExtensionNeedsPatientApproval(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	until := f.now.Add(15 * time.Minute)
	in := f.shareIn()
	in.Until = &until
	share, err := f.s.Share(ctx, f.patient, in)
	if err != nil {
		t.Fatal(err)
	}

	// Too early: more than the warning time is left.
	if _, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); !errors.Is(err, rp.ErrExtensionNotAllowed) {
		t.Fatalf("early: %v", err)
	}
	// An unrelated doctor learns nothing: 404. An unverified one is refused.
	if _, err := f.s.RequestExtension(ctx, ids.New(), f.patient, ""); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("unrelated: %v", err)
	}

	f.advance(6 * time.Minute) // 9 minutes left: inside the warning window
	if u, ok, _ := f.m.HistoryAccessUntil(ctx, f.patient, f.doc); !ok || !u.Equal(until) {
		t.Fatalf("window %v %v", u, ok)
	}
	req, err := f.s.RequestExtension(ctx, f.doc, f.patient, "req-1")
	if err != nil || req.Status != "pending" || req.ExtensionMax != 24*time.Hour {
		t.Fatalf("%+v %v", req, err)
	}
	if again, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); err != nil || again.ID != req.ID {
		t.Fatalf("repeat tap must return the open request: %+v %v", again, err)
	}
	// Asking gives no access beyond the share's end.
	if d := f.decide(t, f.doc); !d.Allowed || d.Tier != rp.T3HistoryShare {
		t.Fatalf("still inside the share: %+v", d)
	}
	f.advance(9 * time.Minute) // exactly at expiry
	if d := f.decide(t, f.doc); d.Allowed || d.Reason != "grant_expired" || d.Hide {
		t.Fatalf("at expiry, request pending: no silent extension, got %+v", d)
	}

	// The patient sees the request, in the list; the doctor is named by reference only.
	pend, err := f.s.PendingRequests(ctx, f.patient)
	if err != nil || len(pend) != 1 || pend[0].ID != req.ID || pend[0].DoctorRef != f.doc {
		t.Fatalf("%+v %v", pend, err)
	}

	// Decline: still no access, and the request is closed.
	if err := f.s.Decline(ctx, f.patient, req.ID, ""); err != nil {
		t.Fatal(err)
	}
	if d := f.decide(t, f.doc); d.Allowed {
		t.Fatalf("declined: %+v", d)
	}
	if err := f.s.Decline(ctx, f.patient, req.ID, ""); !errors.Is(err, rp.ErrRequestClosed) {
		t.Fatalf("decline twice: %v", err)
	}
	if _, err := f.s.Approve(ctx, f.patient, req.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrRequestClosed) {
		t.Fatalf("approve a declined request: %v", err)
	}
	if p, _ := f.s.PendingRequests(ctx, f.patient); len(p) != 0 {
		t.Fatalf("pending after decline: %+v", p)
	}

	// Review F4: right after a decline the same doctor cannot ask again (no stream of pushes).
	if _, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); !errors.Is(err, rp.ErrExtensionNotAllowed) {
		t.Fatalf("asking again straight after a decline: %v", err)
	}
	f.advance(24*time.Hour + time.Minute)

	// After the cooldown the doctor asks again (the share is over, not stopped); only the owner can answer.
	req2, err := f.s.RequestExtension(ctx, f.doc, f.patient, "")
	if err != nil || req2.ID == req.ID {
		t.Fatalf("%+v %v", req2, err)
	}
	if _, err := f.s.Approve(ctx, ids.New(), req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("someone else's request: %v", err)
	}
	if d := f.decide(t, f.doc); d.Allowed {
		t.Fatalf("a request alone gives no access: %+v", d)
	}
	// The approval needs the text in force and at most the maximum.
	if _, err := f.s.Approve(ctx, f.patient, req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"] + 1, NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("wrong text: %v", err)
	}
	if _, err := f.s.Approve(ctx, f.patient, req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0", For: 25 * time.Hour}); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("too long: %v", err)
	}
	f.ver.bad[f.doc] = true
	if _, err := f.s.Approve(ctx, f.patient, req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrDoctorNotVerified) {
		t.Fatalf("unverified doctor: %v", err)
	}
	f.ver.bad[f.doc] = false

	got, err := f.s.Approve(ctx, f.patient, req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0", For: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(f.now.Add(2*time.Hour)) || got.DoctorRef != f.doc {
		t.Fatalf("%+v", got)
	}
	if d := f.decide(t, f.doc); !d.Allowed || d.Tier != rp.T4Extension || d.GrantID != got.GrantID {
		t.Fatalf("after approval: %+v", d)
	}
	if u, ok, _ := f.m.HistoryAccessUntil(ctx, f.patient, f.doc); !ok || !u.Equal(got.ExpiresAt) {
		t.Fatalf("window %v %v", u, ok)
	}
	if _, err := f.s.Approve(ctx, f.patient, req2.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrRequestClosed) {
		t.Fatalf("approve twice: %v", err)
	}
	// The extension ends on time too (no silent access), and the patient can stop it (T4 is revocable).
	f.advance(2 * time.Hour)
	if d := f.decide(t, f.doc); d.Allowed {
		t.Fatalf("extension expired: %+v", d)
	}

	// Everything was audited and announced by reference.
	for _, a := range []string{"share.extension_requested", "share.extension_declined", "share.extension_approved"} {
		if f.auditN(t, a, "allowed") < 1 {
			t.Fatalf("missing audit event %s", a)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM records.event_outbox WHERE type IN ('share.extension_requested','share.extension_decided')`); n != 4 {
		t.Fatalf("events: %d", n)
	}
	if share.GrantID == got.GrantID {
		t.Fatal("the extension must be a separate T4 grant")
	}
	if n := f.count(t, `SELECT count(*) FROM records.permission_grant WHERE tier='T4_extension' AND scope_ref=$1`, req2.ID); n != 1 {
		t.Fatalf("T4 grants: %d", n)
	}
}

func TestTC02LapsedRequestCannotBeApproved(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	until := f.now.Add(5 * time.Minute)
	in := f.shareIn()
	in.Until = &until
	if _, err := f.s.Share(ctx, f.patient, in); err != nil {
		t.Fatal(err)
	}
	req, err := f.s.RequestExtension(ctx, f.doc, f.patient, "")
	if err != nil {
		t.Fatal(err)
	}
	f.advance(8 * day)
	if p, _ := f.s.PendingRequests(ctx, f.patient); len(p) != 0 {
		t.Fatalf("a lapsed request is not listed: %+v", p)
	}
	if _, err := f.s.Approve(ctx, f.patient, req.ID, rp.ApproveInput{TextVersion: versions["P-SHARE"], NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrRequestClosed) {
		t.Fatalf("lapsed: %v", err)
	}
	if d := f.decide(t, f.doc); d.Allowed {
		t.Fatalf("lapsed request gave access: %+v", d)
	}
	// A new request can be made after the old one lapsed.
	if r2, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); err != nil || r2.ID == req.ID {
		t.Fatalf("%+v %v", r2, err)
	}
}

func TestS168ExtensionRefusedAfterPatientStoppedSharing(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	until := f.now.Add(5 * time.Minute)
	in := f.shareIn()
	in.Until = &until
	share, err := f.s.Share(ctx, f.patient, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Revoke(ctx, share.GrantID, f.patient, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); !errors.Is(err, rp.ErrExtensionNotAllowed) {
		t.Fatalf("revoked share: %v", err)
	}
	// A doctor who only has current-care access (T1) has nothing to extend either.
	other := ids.New()
	if _, err := f.m.GrantT1(ctx, f.patient, other, f.clinic, ids.New(), f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequestExtension(ctx, other, f.patient, ""); !errors.Is(err, rp.ErrExtensionNotAllowed) {
		t.Fatalf("T1 only: %v", err)
	}
	f.ver.err = errors.New("down")
	if _, err := f.s.RequestExtension(ctx, f.doc, f.patient, ""); !errors.Is(err, rp.ErrDoctorNotVerified) {
		t.Fatalf("verification error: %v", err)
	}
}

// ---- handlers: roles, wiring, shapes ----

func TestS168HandlersFailClosedAndMapErrors(t *testing.T) {
	f := shareSetup(t)
	pctx, dctx := principal(f.patient, ip.RolePatient), principal(f.doc, ip.RoleDoctor)
	body := &httpapi.HistoryShareCreate{DoctorId: uuid.MustParse(f.doc), AppointmentId: uuid.MustParse(f.appt), TextVersion: versions["P-SHARE"], NoticeVersion: ptr("PN-1.0")}

	// Not wired: refused.
	if _, err := (httpapi.Handlers{M: f.m}).CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: body}); !errors.Is(err, problem.Unavailable) {
		t.Fatalf("not wired: %v", err)
	}
	h := httpapi.Handlers{M: f.m, R: f.r, S: f.s}
	// Roles: the doctor cannot use patient routes and the patient cannot use doctor routes.
	if _, err := h.CreateHistoryShare(dctx, httpapi.CreateHistoryShareRequestObject{Body: body}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("doctor creating a share: %v", err)
	}
	if _, err := h.RequestAccessExtension(pctx, httpapi.RequestAccessExtensionRequestObject{PatientId: uuid.MustParse(f.patient)}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("patient requesting: %v", err)
	}
	if _, err := h.ListPatientDocuments(pctx, httpapi.ListPatientDocumentsRequestObject{PatientId: uuid.MustParse(f.patient)}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("patient on the doctor route: %v", err)
	}
	// Errors map to stable problems.
	var pr *problem.Problem
	f.ver.bad[f.doc] = true
	if _, err := h.CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: body}); !errors.As(err, &pr) || pr.Code != "records.doctor_not_verified" {
		t.Fatalf("unverified: %#v", err)
	}
	// Review F3 (K-CS-01): the doctor's document list re-checks Verified on every read.
	if _, err := h.ListPatientDocuments(dctx, httpapi.ListPatientDocumentsRequestObject{PatientId: uuid.MustParse(f.patient)}); !errors.As(err, &pr) || pr.Code != "records.doctor_not_verified" {
		t.Fatalf("suspended doctor listing documents: %#v", err)
	}
	f.ver.bad[f.doc] = false
	if _, err := (httpapi.Handlers{M: f.m, R: f.r}).ListPatientDocuments(dctx, httpapi.ListPatientDocumentsRequestObject{PatientId: uuid.MustParse(f.patient)}); !errors.Is(err, problem.Unavailable) {
		t.Fatalf("document list without the verifier must fail closed: %v", err)
	}
	bad := *body
	bad.AppointmentId = uuid.New()
	if _, err := h.CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: &bad}); !errors.As(err, &pr) || pr.Status != 404 {
		t.Fatalf("unknown appointment: %#v", err)
	}
	wrong := *body
	wrong.TextVersion++
	if _, err := h.CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: &wrong}); !errors.As(err, &pr) || pr.Code != "records.invalid" {
		t.Fatalf("wrong text version: %#v", err)
	}
	if _, err := h.RequestAccessExtension(dctx, httpapi.RequestAccessExtensionRequestObject{PatientId: uuid.MustParse(f.patient)}); !errors.As(err, &pr) || pr.Status != 404 {
		t.Fatalf("no share, no relationship: %#v", err)
	}

	// Full flow through handlers: share, doctor asks, patient lists and approves.
	until := f.now.Add(5 * time.Minute)
	body.Until = &until
	if _, err := h.CreateHistoryShare(pctx, httpapi.CreateHistoryShareRequestObject{Body: body}); err != nil {
		t.Fatal(err)
	}
	res, err := h.RequestAccessExtension(dctx, httpapi.RequestAccessExtensionRequestObject{PatientId: uuid.MustParse(f.patient)})
	if err != nil {
		t.Fatal(err)
	}
	asked := res.(httpapi.RequestAccessExtension202JSONResponse)
	lst, err := h.ListAccessExtensionRequests(pctx, httpapi.ListAccessExtensionRequestsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	items := lst.(httpapi.ListAccessExtensionRequests200JSONResponse).Items
	if len(items) != 1 || items[0].RequestId != asked.RequestId || items[0].MaxExtensionSeconds != 86400 || items[0].DoctorId.String() != f.doc {
		t.Fatalf("%+v", items)
	}
	// Someone else's request id is 404; approving again after approval is 409.
	if _, err := h.ApproveAccessExtension(principal(ids.New(), ip.RolePatient), httpapi.ApproveAccessExtensionRequestObject{RequestId: asked.RequestId,
		Body: &httpapi.ExtensionApprove{TextVersion: versions["P-SHARE"], NoticeVersion: ptr("PN-1.0")}}); !errors.As(err, &pr) || pr.Status != 404 {
		t.Fatalf("someone else's request: %#v", err)
	}
	ap := httpapi.ApproveAccessExtensionRequestObject{RequestId: asked.RequestId, Body: &httpapi.ExtensionApprove{TextVersion: versions["P-SHARE"], NoticeVersion: ptr("PN-1.0")}}
	if _, err := h.ApproveAccessExtension(pctx, ap); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ApproveAccessExtension(pctx, ap); !errors.As(err, &pr) || pr.Status != 409 || pr.Code != "records.request_closed" {
		t.Fatalf("approve twice: %#v", err)
	}
	if _, err := h.DeclineAccessExtension(pctx, httpapi.DeclineAccessExtensionRequestObject{RequestId: asked.RequestId}); !errors.As(err, &pr) || pr.Status != 409 {
		t.Fatalf("decline after approval: %#v", err)
	}
	if d := f.decide(t, f.doc); !d.Allowed {
		t.Fatalf("approved extension must open history: %+v", d)
	}
}

func TestS169DoctorDocumentListIsGatedAndAudited(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	f.upload(t, f.patientRef(f.patient, false), "synthetic lab report", pdf("one"))
	dc := rp.Caller{UserRef: f.doc, Role: "doctor", ClinicRef: f.clinic}

	// No share yet: the doctor has no relationship (404).
	if _, err := f.r.ListPatientDocuments(ctx, dc, f.patient); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("no relationship: %v", err)
	}
	share, err := f.s.Share(ctx, f.patient, f.shareIn())
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.r.ListPatientDocuments(ctx, dc, f.patient)
	if err != nil || len(got.Items) != 1 || got.Items[0].Title != "synthetic lab report" || got.AccessUntil == nil || !got.AccessUntil.Equal(share.ExpiresAt) {
		t.Fatalf("%+v %v", got, err)
	}
	// The patient and other roles cannot use the doctor route.
	if _, err := f.r.ListPatientDocuments(ctx, rp.Caller{UserRef: f.patient, Role: "patient"}, f.patient); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("patient: %v", err)
	}
	if _, err := f.r.ListPatientDocuments(ctx, rp.Caller{UserRef: ids.New(), Role: "front_desk", ClinicRef: f.clinic}, f.patient); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("front desk: %v", err)
	}
	// After expiry the read is denied (consent.required), with no silent extension.
	f.advance(8*day + time.Hour)
	if _, err := f.r.ListPatientDocuments(ctx, dc, f.patient); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("expired: %v", err)
	}
	if f.auditN(t, "records.read.documents", "allowed") != 1 || f.auditN(t, "records.read.documents", "denied") < 3 {
		t.Fatalf("allowed=%d denied=%d", f.auditN(t, "records.read.documents", "allowed"), f.auditN(t, "records.read.documents", "denied"))
	}
}

func TestS168PendingRequestsPageAndDocumentsPage(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		f.upload(t, f.patientRef(f.patient, false), "synthetic doc", pdf("n"+string(rune('a'+i))))
	}
	share, err := f.s.Share(ctx, f.patient, f.shareIn())
	if err != nil {
		t.Fatal(err)
	}
	_ = share
	got, err := f.r.ListPatientDocuments(ctx, rp.Caller{UserRef: f.doc, Role: "doctor", ClinicRef: f.clinic}, f.patient)
	if err != nil {
		t.Fatal(err)
	}
	p1, next := got.Page(2, "")
	p2, next2 := got.Page(2, next)
	if len(p1) != 2 || next == "" || len(p2) != 1 || next2 != "" || p2[0].ID == p1[0].ID || p2[0].ID == p1[1].ID {
		t.Fatalf("%d %q %d %q", len(p1), next, len(p2), next2)
	}
	r, _, err := f.s.PendingRequestsPage(ctx, f.patient, 5, "")
	if err != nil || len(r) != 0 {
		t.Fatalf("%v %v", r, err)
	}
}

// Review F10: five taps at once on the P-SHARE screen create one share; every tap gets it.
func TestReviewF10ConcurrentShareTapsCreateOneGrant(t *testing.T) {
	f := shareSetup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]rp.ShareResult, 5)
	errs := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = f.s.Share(ctx, f.patient, f.shareIn())
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i].GrantID != results[0].GrantID {
			t.Fatalf("tap %d: %+v %v (first %s)", i, results[i], errs[i], results[0].GrantID)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM records.permission_grant WHERE patient_ref=$1 AND tier=$2`, f.patient, string(rp.T3HistoryShare)); n != 1 {
		t.Fatalf("one share expected, got %d", n)
	}
}
