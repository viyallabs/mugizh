package admin_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/admin"
	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

const day = 24 * time.Hour

// roles is a fake role checker: user ref to roles held.
type roles map[string][]string

func (r roles) HasRole(_ context.Context, u, role string) (bool, error) {
	return slices.Contains(r[u], role), nil
}

type fx struct {
	env    *testkit.Env
	a      *admin.Module
	rec    *records.Module
	roles  roles
	accts  accounts
	now    time.Time
	alerts []string
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), records.Source(), admin.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{env: env, roles: roles{}, accts: accounts{}, now: time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.rec, err = records.New(env.Pools.For("records"), records.Config{ConsentVersions: map[string]int{"P-ACCOUNT": 1, "P-CARE": 2, "P-RECORD": 2},
		T1Window: day, JITMax: 4 * time.Hour, CacheTTL: time.Second}, log)
	if err != nil {
		t.Fatal(err)
	}
	f.rec.SetClock(func() time.Time { return f.now })
	f.rec.SetRoleChecker(f.roles)
	f.rec.Alert = func(_ context.Context, code string) { f.alerts = append(f.alerts, code) }
	f.a, err = admin.New(env.Pools.For("admin"), admin.Config{
		JITMax: 4 * time.Hour, ApprovalWindow: day, JustificationWithin: day, AckWithin: day, ResolveWithin: 7 * day, ContentTakedown: 36 * time.Hour,
		OrderCompliance: 3 * time.Hour, IntimateImage: 2 * time.Hour, LawfulRequest: 72 * time.Hour, MedicalRecordCopy: 72 * time.Hour,
		DataRightsResponse: 15 * day, DataRightsAlertAt: 10 * day, AlertFraction: 0.8, ProhibitedValidators: []string{"verhoeff_12_digit_first_2_to_9", "digits_16"},
		LookupsPerHour: 3,
	}, admin.Deps{Records: f.rec, Roles: f.roles, Crypto: cr, Accounts: f.accts}, log)
	if err != nil {
		t.Fatal(err)
	}
	f.a.SetClock(func() time.Time { return f.now })
	return f
}

func (f *fx) staff(role string) pub.Staff {
	id := ids.New()
	f.roles[id] = []string{role}
	return pub.Staff{Ref: id, Role: role}
}

func (f *fx) read(t *testing.T, s pub.Staff, patient string, k rp.DataKind) rp.Decision {
	t.Helper()
	d, err := f.rec.Authorize(context.Background(), rp.Request{Actor: rp.Actor{Ref: s.Ref, Role: s.Role}, PatientRef: patient, Kind: k, ResourceType: "booking", ResourceRef: ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *fx) auditCount(t *testing.T, where string) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_outbox WHERE `+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TC-11 (E-DP-16), end to end through admin and the real records module: no grant, no
// access (denied and logged); a request needs a second person; with the grant, access is
// limited to the scope and the time; self-approval and weak requests are refused.
func TestTC11StaffAccessThroughAdminAndRecords(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	support, owner, otherOwner := f.staff("support"), f.staff("security_owner"), f.staff("security_owner")

	if d := f.read(t, support, patient, rp.KindBooking); d.Allowed {
		t.Fatalf("no grant: %+v", d)
	}
	if f.auditCount(t, `action='records.read.booking' AND result='denied'`) != 1 {
		t.Fatal("the denial must be audited")
	}
	in := pub.AccessRequestInput{PatientRef: patient, Kinds: []rp.DataKind{rp.KindBooking}, ReasonCode: pub.ReasonComplaint, TicketRef: "TKT-100", Duration: 2 * time.Hour}
	req, err := f.a.RequestAccess(ctx, support, in)
	if err != nil || req.Status != "pending" {
		t.Fatalf("%+v %v", req, err)
	}
	if d := f.read(t, support, patient, rp.KindBooking); d.Allowed {
		t.Fatal("a pending request grants nothing")
	}
	// Weak or malformed requests.
	bad := in
	bad.Duration = 5 * time.Hour
	if _, err := f.a.RequestAccess(ctx, support, bad); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("duration over 4 h: %v", err)
	}
	bad = in
	bad.ReasonCode = "other"
	if _, err := f.a.RequestAccess(ctx, support, bad); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("free-form reason: %v", err)
	}
	bad = in
	bad.TicketRef = ""
	if _, err := f.a.RequestAccess(ctx, support, bad); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("no ticket: %v", err)
	}
	if _, err := f.a.RequestAccess(ctx, pub.Staff{Ref: ids.New(), Role: "support"}, in); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("a person who does not hold the role: %v", err)
	}
	// Approval rules.
	selfOwner := pub.Staff{Ref: support.Ref, Role: "security_owner"}
	f.roles[support.Ref] = []string{"support", "security_owner"}
	if _, err := f.a.ApproveAccess(ctx, selfOwner, req.ID); !errors.Is(err, pub.ErrSelfApproval) {
		t.Fatalf("self-approval: %v", err)
	}
	if _, err := f.a.ApproveAccess(ctx, f.staff("support"), req.ID); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("approval needs a security owner: %v", err)
	}
	got, err := f.a.ApproveAccess(ctx, owner, req.ID)
	if err != nil || got.Status != "approved" || got.GrantID == "" || got.ApproverRef != owner.Ref {
		t.Fatalf("approve: %+v %v", got, err)
	}
	if _, err := f.a.ApproveAccess(ctx, otherOwner, req.ID); !errors.Is(err, pub.ErrNotPending) {
		t.Fatalf("second approval: %v", err)
	}
	// In scope and time.
	if d := f.read(t, support, patient, rp.KindBooking); !d.Allowed || d.Tier != rp.JITStaff || d.GrantID != got.GrantID {
		t.Fatalf("in scope: %+v", d)
	}
	if d := f.read(t, support, patient, rp.KindPayment); d.Allowed {
		t.Fatalf("payment data is outside the granted scope: %+v", d)
	}
	if d := f.read(t, support, ids.New(), rp.KindBooking); d.Allowed {
		t.Fatal("another patient is outside the grant")
	}
	f.now = f.now.Add(2*time.Hour + time.Minute)
	if d := f.read(t, support, patient, rp.KindBooking); d.Allowed {
		t.Fatalf("after expiry: %+v", d)
	}
	// Records enforces its own rules through admin: clinical data needs a clinical reason.
	f.now = f.now.Add(time.Minute)
	clin := pub.AccessRequestInput{PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalHistory}, ReasonCode: pub.ReasonPaymentDispute, TicketRef: "TKT-101", Duration: time.Hour}
	gr := f.staff("grievance_officer")
	r2, err := f.a.RequestAccess(ctx, gr, clin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ApproveAccess(ctx, owner, r2.ID); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("clinical data for a payment dispute: %v", err)
	}
	// Support may never be granted clinical kinds.
	r3, _ := f.a.RequestAccess(ctx, support, pub.AccessRequestInput{PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalHistory}, ReasonCode: pub.ReasonComplaint, TicketRef: "TKT-102", Duration: time.Hour})
	if _, err := f.a.ApproveAccess(ctx, owner, r3.ID); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("support and clinical data: %v", err)
	}
	// A request not approved in time expires.
	r4, _ := f.a.RequestAccess(ctx, support, in)
	f.now = f.now.Add(25 * time.Hour)
	if _, err := f.a.ApproveAccess(ctx, owner, r4.ID); !errors.Is(err, pub.ErrExpired) {
		t.Fatalf("stale request: %v", err)
	}
	// Rejection.
	r5, _ := f.a.RequestAccess(ctx, support, in)
	if err := f.a.RejectAccess(ctx, owner, r5.ID, "no_ticket_match"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ApproveAccess(ctx, owner, r5.ID); !errors.Is(err, pub.ErrNotPending) {
		t.Fatalf("approve after reject: %v", err)
	}
	if f.auditCount(t, `action='admin.access_approved'`) != 1 || f.auditCount(t, `action='permission.created' AND ticket_ref='TKT-100'`) != 1 {
		t.Fatal("approval and grant must both be audited with the ticket")
	}
}

// TC-11: break-glass is for the security owner in a security incident. It raises an alert
// at once, emits access.break_glass, needs a written justification within 24 hours, and
// appears in the monthly review.
func TestTC11BreakGlassAlertsAndNeedsJustification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	owner, support := f.staff("security_owner"), f.staff("support")
	in := pub.AccessRequestInput{PatientRef: patient, Kinds: []rp.DataKind{rp.KindBooking, rp.KindPayment}, TicketRef: "INC-7", Duration: time.Hour}
	if _, err := f.a.BreakGlass(ctx, support, in); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("support cannot break glass: %v", err)
	}
	req, err := f.a.BreakGlass(ctx, owner, in)
	if err != nil || !req.BreakGlass || req.JustificationDueAt == nil || !req.JustificationDueAt.Equal(f.now.Add(day)) {
		t.Fatalf("%+v %v", req, err)
	}
	if !slices.Contains(f.alerts, "access.break_glass") {
		t.Fatalf("break-glass must raise an alert: %v", f.alerts)
	}
	d, err := f.rec.Authorize(ctx, rp.Request{Actor: rp.Actor{Ref: owner.Ref, Role: "security_owner"}, PatientRef: patient, Kind: rp.KindPayment, ResourceType: "payment", ResourceRef: ids.New()})
	if err != nil || !d.Allowed || d.Tier != rp.BreakGlass {
		t.Fatalf("break-glass access: %+v %v", d, err)
	}
	var events int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM admin.event_outbox WHERE type='access.break_glass'`).Scan(&events)
	if events != 1 {
		t.Fatalf("access.break_glass events: %d", events)
	}
	f.now = f.now.Add(25 * time.Hour)
	al, err := f.a.DueSoon(ctx, f.now)
	if err != nil || len(al) != 1 || al[0].Reason != "justification_overdue" {
		t.Fatalf("overdue justification: %+v %v", al, err)
	}
	if err := f.a.Justify(ctx, f.staff("security_owner"), req.ID, "x"); err == nil {
		t.Fatal("only the person who broke glass can justify it")
	}
	if err := f.a.Justify(ctx, owner, req.ID, "Live incident INC-7: confirming scope of exposure"); err != nil {
		t.Fatal(err)
	}
	if al, _ := f.a.DueSoon(ctx, f.now); len(al) != 0 {
		t.Fatalf("justified: %+v", al)
	}
	// Monthly review lists it with the reason, and the review is recorded.
	rows, err := f.a.MonthlyReview(ctx, owner, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].BreakGlass || rows[0].ReasonCode != pub.ReasonSecurityIncident || rows[0].TicketRef != "INC-7" || !rows[0].Justified {
		t.Fatalf("review rows: %+v", rows)
	}
	if _, err := f.a.MonthlyReview(ctx, support, f.now); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("review is for the security owner: %v", err)
	}
	var reviews int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM admin.access_review`).Scan(&reviews)
	if reviews != 1 {
		t.Fatalf("review evidence rows: %d", reviews)
	}
}

// Grievance queue: timers per kind come from config; the evidence trail is append-only.
func TestGrievanceTimersAndEvidence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	g, err := f.a.OpenGrievance(ctx, patient, "The clinic refused to refund my cancelled booking")
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "acknowledged" || !g.DueAt.Equal(f.now.Add(7*day)) || g.Reference == "" || g.Kind != pub.KindComplaint {
		t.Fatalf("complaint: %+v", g)
	}
	if _, err := f.a.OpenGrievance(ctx, patient, ""); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("empty text: %v", err)
	}
	officer := f.staff("grievance_officer")
	want := map[pub.GrievanceKind]time.Duration{pub.KindContentTakedown: 36 * time.Hour, pub.KindOrderTakedown: 3 * time.Hour, pub.KindIntimateImage: 2 * time.Hour,
		pub.KindLawfulRequest: 72 * time.Hour, pub.KindMedicalRecordCopy: 72 * time.Hour, pub.KindDataRights: 15 * day}
	got := map[pub.GrievanceKind]pub.Grievance{}
	for k, w := range want {
		it, err := f.a.Intake(ctx, officer, k, "SRC-"+string(k), "", f.now)
		if err != nil {
			t.Fatal(err)
		}
		if !it.DueAt.Equal(f.now.Add(w)) || it.Status != "open" {
			t.Fatalf("%s: %+v", k, it)
		}
		got[k] = it
	}
	// Short windows are acknowledged within the window, not later.
	if !got[pub.KindIntimateImage].AckDueAt.Equal(f.now.Add(2*time.Hour)) || !got[pub.KindContentTakedown].AckDueAt.Equal(f.now.Add(day)) {
		t.Fatalf("ack due: %+v %+v", got[pub.KindIntimateImage], got[pub.KindContentTakedown])
	}
	if _, err := f.a.Intake(ctx, f.staff("support"), pub.KindLawfulRequest, "X", "", f.now); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("only the grievance officer logs intake: %v", err)
	}
	if _, err := f.a.Intake(ctx, officer, "nonsense", "X", "", f.now); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	// The officer opens the complaint and sees the text; it is sealed at rest.
	_, text, err := f.a.Open(ctx, officer, g.ID)
	if err != nil || text != "The clinic refused to refund my cancelled booking" {
		t.Fatalf("open: %q %v", text, err)
	}
	var sealed []byte
	_ = f.env.Owner.QueryRow(ctx, `SELECT text_sealed FROM admin.grievance WHERE id=$1`, g.ID).Scan(&sealed)
	if len(sealed) == 0 || string(sealed) == text {
		t.Fatal("the complaint text must be sealed")
	}
	// Alerts.
	f.now = f.now.Add(2*time.Hour + 30*time.Minute)
	al, _ := f.a.DueSoon(ctx, f.now)
	reasons := map[string]bool{}
	for _, a := range al {
		reasons[string(a.Kind)+"/"+a.Reason] = true
	}
	for _, k := range []string{"intimate_image/breached", "intimate_image/ack_overdue"} {
		if !reasons[k] {
			t.Fatalf("missing alert %s in %v", k, reasons)
		}
	}
	if reasons["order_takedown/breached"] || !reasons["order_takedown/alert_80_percent"] {
		t.Fatalf("3 h item at 150 min of 180: %v", reasons)
	}
	f.now = f.now.Add(10 * day)
	al, _ = f.a.DueSoon(ctx, f.now)
	reasons = map[string]bool{}
	for _, a := range al {
		reasons[string(a.Kind)+"/"+a.Reason] = true
	}
	if !reasons["data_rights/alert_10_days"] || !reasons["data_rights/ack_overdue"] || !reasons["complaint/breached"] {
		t.Fatalf("day 10: %v", reasons)
	}
	// Acknowledge, resolve, evidence.
	if err := f.a.Acknowledge(ctx, officer, got[pub.KindDataRights].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Acknowledge(ctx, officer, got[pub.KindDataRights].ID); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("second acknowledgement: %v", err)
	}
	if err := f.a.Resolve(ctx, officer, got[pub.KindDataRights].ID, "completed"); err != nil {
		t.Fatal(err)
	}
	ev, err := f.a.Evidence(ctx, officer, got[pub.KindDataRights].ID)
	if err != nil || len(ev) != 3 || ev[2] != "resolved:completed" {
		t.Fatalf("evidence: %v %v", ev, err)
	}
	q, _, err := f.a.Queue(ctx, officer, "", 3)
	if err != nil || len(q) != 3 || q[0].DueAt.After(q[1].DueAt) {
		t.Fatalf("queue most urgent first: %+v %v", q, err)
	}
}

// Data-rights requests from the records module appear in the queue with the right
// timers; the same event twice creates one item.
func TestRightsRequestEventsEnterTheQueue(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, r1, r2 := ids.New(), ids.New(), ids.New()
	ev := func(ref, typ string) outbox.Event {
		return outbox.Event{ID: ids.New(), Type: "rights.request_received", Version: 1, OccurredAt: f.now, Producer: "records", SubjectRef: patient,
			Payload: map[string]string{"requestRef": ref, "rightsType": typ}}
	}
	e1 := ev(r1, "export")
	for i := 0; i < 2; i++ {
		if err := f.a.OnRightsRequestReceived(ctx, e1); err != nil {
			t.Fatal(err)
		}
	}
	// A re-sent request with a new event ID must not create a second item either.
	if err := f.a.OnRightsRequestReceived(ctx, ev(r1, "export")); err != nil {
		t.Fatal(err)
	}
	if err := f.a.OnRightsRequestReceived(ctx, ev(r2, "erasure")); err != nil {
		t.Fatal(err)
	}
	if err := f.a.OnRightsRequestReceived(ctx, ev(r2, "bogus")); err == nil {
		t.Fatal("unknown rights type must be refused")
	}
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM admin.grievance`).Scan(&n)
	if n != 2 {
		t.Fatalf("queue items: %d", n)
	}
	var kindDue string
	_ = f.env.Owner.QueryRow(ctx, `SELECT kind || ':' || extract(epoch FROM due_at - received_at)::int FROM admin.grievance WHERE source_ref=$1`, "rights:"+r1).Scan(&kindDue)
	if kindDue != "medical_record_copy:259200" {
		t.Fatalf("export is a 72 h medical-record copy: %s", kindDue)
	}
	_ = f.env.Owner.QueryRow(ctx, `SELECT kind || ':' || extract(epoch FROM due_at - received_at)::int FROM admin.grievance WHERE source_ref=$1`, "rights:"+r2).Scan(&kindDue)
	if kindDue != "data_rights:1296000" {
		t.Fatalf("erasure is a 15-day data right: %s", kindDue)
	}
}

// TC-48 / CH-12: a doctor's minor flag opens one support case per session, holding
// references only; malformed events are refused.
func TestMinorFlagOpensChildAccountCase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, session := ids.New(), ids.New()
	ev := outbox.Event{ID: ids.New(), Type: "consult.minor_flagged", Version: 1, OccurredAt: f.now, Producer: "consult", SubjectRef: patient,
		Payload: map[string]string{"patientRef": patient, "sessionRef": session, "appointmentRef": ids.New()}}
	for i := 0; i < 2; i++ {
		if err := f.a.OnMinorFlagged(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	ev.ID = ids.New() // re-sent with a new event ID
	if err := f.a.OnMinorFlagged(ctx, ev); err != nil {
		t.Fatal(err)
	}
	bad := ev
	bad.ID, bad.Payload = ids.New(), map[string]string{"patientRef": "x", "sessionRef": session}
	if err := f.a.OnMinorFlagged(ctx, bad); err == nil {
		t.Fatal("malformed refs must be refused")
	}
	var kind, ref, sub string
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM admin.grievance`).Scan(&n)
	_ = f.env.Owner.QueryRow(ctx, `SELECT kind, reference, submitter_ref::text FROM admin.grievance WHERE source_ref=$1`, "consult:"+session).Scan(&kind, &ref, &sub)
	if n != 1 || kind != "child_account" || !strings.HasPrefix(ref, "CA-") || sub != patient {
		t.Fatalf("one CH-12 case expected: n=%d kind=%s ref=%s", n, kind, ref)
	}
}

func TestNewRefusesMissingTimers(t *testing.T) {
	if _, err := admin.New(nil, admin.Config{}, admin.Deps{}, slog.Default()); err == nil {
		t.Fatal("missing timers must be a start-up error")
	}
}

// Review H2: two concurrent approvals of one request create exactly one grant and one
// approval audit event; the loser is told the request is no longer pending.
func TestReviewH2ConcurrentApproval(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	support, o1, o2 := f.staff("support"), f.staff("security_owner"), f.staff("security_owner")
	req, err := f.a.RequestAccess(ctx, support, pub.AccessRequestInput{PatientRef: patient, Kinds: []rp.DataKind{rp.KindBooking}, ReasonCode: pub.ReasonComplaint, TicketRef: "TKT-H2", Duration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, o := range []pub.Staff{o1, o2} {
		go func() { _, err := f.a.ApproveAccess(ctx, o, req.ID); errs <- err }()
	}
	ok, notPending := 0, 0
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, pub.ErrNotPending):
			notPending++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || notPending != 1 {
		t.Fatalf("ok=%d notPending=%d", ok, notPending)
	}
	var grants int
	if err := f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.permission_grant WHERE patient_ref=$1`, patient).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("grants=%d %v", grants, err)
	}
	if n := f.auditCount(t, `action='admin.access_approved'`); n != 1 {
		t.Fatalf("approval audit events=%d", n)
	}
}

// Review H2: a failed grant leaves the request pending (no grant, no approval record).
func TestReviewH2FailedGrantReturnsToPending(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	support, owner := f.staff("support"), f.staff("security_owner")
	// Support may not hold clinical kinds: records refuses the grant.
	req, _ := f.a.RequestAccess(ctx, support, pub.AccessRequestInput{PatientRef: ids.New(), Kinds: []rp.DataKind{rp.KindClinicalHistory}, ReasonCode: pub.ReasonComplaint, TicketRef: "TKT-H2B", Duration: time.Hour})
	if _, err := f.a.ApproveAccess(ctx, owner, req.ID); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("%v", err)
	}
	got, err := f.a.GetAccessRequest(ctx, req.ID)
	if err != nil || got.Status != "pending" || got.GrantID != "" {
		t.Fatalf("%+v %v", got, err)
	}
}

// Review LOW items: bad IDs are not found, free text is scrubbed, the monthly review row
// is insert-only.
func TestReviewAdminLowItems(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.staff("security_owner")
	if _, err := f.a.GetAccessRequest(ctx, "not-a-uuid"); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := f.a.ApproveAccess(ctx, owner, "'; drop table x;--"); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("approve bad id: %v", err)
	}
	if err := f.a.Justify(ctx, owner, "bad", "text"); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("justify bad id: %v", err)
	}
	bg, err := f.a.BreakGlass(ctx, owner, pub.AccessRequestInput{PatientRef: ids.New(), Kinds: []rp.DataKind{rp.KindBooking}, TicketRef: "TKT-L1", Duration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Justify(ctx, owner, bg.ID, "card 4111111111111111 was involved"); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("justification with a 16-digit number: %v", err)
	}
	if err := f.a.Justify(ctx, owner, bg.ID, "incident response"); err != nil {
		t.Fatal(err)
	}
	other := f.staff("security_owner")
	if _, err := f.a.MonthlyReview(ctx, owner, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.MonthlyReview(ctx, other, f.now); err != nil {
		t.Fatal(err)
	}
	var reviewer string
	if err := f.env.Owner.QueryRow(ctx, `SELECT reviewer_ref::text FROM admin.access_review`).Scan(&reviewer); err != nil || reviewer != owner.Ref {
		t.Fatalf("reviewer overwritten: %s %v", reviewer, err)
	}
}

// accounts is a fake identity lookup: phone → summary.
type accounts map[string]ip.AccountSummary

func (a accounts) LookupByPhone(_ context.Context, phone string) (ip.AccountSummary, error) {
	if s, ok := a[phone]; ok {
		return s, nil
	}
	return ip.AccountSummary{}, ip.ErrAccountNotFound
}

// S-172, E-DP-16: only support, ops and the Grievance Officer look up accounts, always for a
// ticket; every lookup is recorded and audited (found or not) without the number; lookups are
// limited per staff member; the answer carries no personal or health details.
func TestS172SupportAccountLookup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	f.accts["+00055500001"] = ip.AccountSummary{UserID: patient, CreatedOn: "2026-11-01", Roles: []string{"patient"}, ActiveDevices: 1}
	sup := f.staff("support")
	for _, s := range []pub.Staff{f.staff("doctor"), f.staff("reviewer"), {Ref: ids.New(), Role: "support"}} { // last: role not actually held
		if _, err := f.a.LookupAccount(ctx, s, "+00055500001", "TCK-1"); !errors.Is(err, pub.ErrForbidden) {
			t.Fatalf("%s must not look up: %v", s.Role, err)
		}
	}
	if _, err := f.a.LookupAccount(ctx, sup, "+00055500001", ""); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("a ticket is required: %v", err)
	}
	got, err := f.a.LookupAccount(ctx, sup, "+00055500001", "TCK-1")
	if err != nil || got.UserID != patient {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if _, err := f.a.LookupAccount(ctx, sup, "+00055500099", "TCK-1"); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("unknown number: %v", err)
	}
	var found, notFound int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE result='allowed' AND subject_ref=$1), count(*) FILTER (WHERE result='denied' AND reason_code='not_found')
		FROM audit.audit_outbox WHERE action='admin.account_lookup' AND actor_ref=$2 AND ticket_ref='TCK-1'`, patient, sup.Ref).Scan(&found, &notFound)
	if found != 1 || notFound != 1 {
		t.Fatalf("audit: found %d not found %d", found, notFound)
	}
	var cols int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='admin' AND table_name='account_lookup' AND column_name LIKE '%phone%'`).Scan(&cols)
	if cols != 0 {
		t.Fatal("the searched number must never be stored")
	}
	if _, err := f.a.LookupAccount(ctx, sup, "+00055500001", "TCK-2"); err != nil { // third lookup this hour
		t.Fatal(err)
	}
	if _, err := f.a.LookupAccount(ctx, sup, "+00055500001", "TCK-3"); !errors.Is(err, pub.ErrRateLimited) {
		t.Fatalf("the fourth lookup in an hour is refused: %v", err)
	}
	var limited int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action='admin.account_lookup' AND reason_code='rate_limited' AND actor_ref=$1`, sup.Ref).Scan(&limited)
	if limited != 1 {
		t.Fatalf("a refused lookup is audited too (review F7): %d", limited)
	}
	f.now = f.now.Add(61 * time.Minute)
	if _, err := f.a.LookupAccount(ctx, sup, "+00055500001", "TCK-3"); err != nil {
		t.Fatalf("allowed again after an hour: %v", err)
	}
}
