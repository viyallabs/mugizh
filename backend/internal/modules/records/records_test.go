package records_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

var versions = map[string]int{"P-ACCOUNT": 1, "P-CARE": 2, "P-RECORD": 2, "P-REMIND": 2, "P-SHARE": 1, "P-MEDNAMES": 1}

type fx struct {
	env    *testkit.Env
	m      *records.Module
	now    time.Time
	alerts []string
	roles  fakeRoles
}

// fakeRoles stands in for identity's role table.
type fakeRoles map[string]bool

func (f fakeRoles) HasRole(_ context.Context, user, role string) (bool, error) {
	return f[user+"|"+role], nil
}

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), records.Source()}})
	f := &fx{env: env, now: time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC), roles: fakeRoles{}}
	f.m = f.newInstance(t, 5*time.Second)
	return f
}

func (f *fx) newInstance(t *testing.T, ttl time.Duration) *records.Module {
	t.Helper()
	m, err := records.New(f.env.Pools.For("records"), records.Config{ConsentVersions: versions, T1Window: 24 * time.Hour,
		JITMax: 4 * time.Hour, CacheTTL: ttl}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(func() time.Time { return f.now })
	m.Alert = func(_ context.Context, code string) { f.alerts = append(f.alerts, code) }
	m.SetRoleChecker(f.roles)
	return m
}

func (f *fx) consent(t *testing.T, patient string, purposes ...string) {
	t.Helper()
	for _, p := range purposes {
		if err := f.m.GrantConsent(context.Background(), rp.ConsentChange{SubjectRef: patient, PurposeID: p, TextVersion: versions[p],
			NoticeVersion: "PN-1.0", Channel: "app"}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fx) auditCount(t *testing.T, result string) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_outbox WHERE action LIKE 'records.read.%' AND result=$1`, result).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func doctor(id, clinic string) rp.Actor { return rp.Actor{Ref: id, Role: "doctor", ClinicRef: clinic} }

func mustDecide(t *testing.T, m *records.Module, r rp.Request) rp.Decision {
	t.Helper()
	d, err := m.Authorize(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TC-01 (E-DP-01): a doctor viewing a shared record; the patient revokes; the next
// request is denied with consent.required (not hidden), on this instance and, through
// NOTIFY, on another instance whose cache would otherwise still hold the grant.
func TestTC01RevokeWhileDoctorHasRecordOpen(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	gid, err := f.m.ShareT3(ctx, patient, doc, clinic, booking, f.now.Add(7*24*time.Hour), versions["P-SHARE"], "PN-1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	req := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory, ResourceType: "note", ResourceRef: ids.New()}

	// Second API instance with a long TTL, listening for changes.
	other := f.newInstance(t, 60*time.Second)
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go other.Listen(lctx)
	time.Sleep(300 * time.Millisecond) // let the listener connect

	if d := mustDecide(t, f.m, req); !d.Allowed || d.Tier != rp.T3HistoryShare || d.GrantID != gid {
		t.Fatalf("shared record must be readable: %+v", d)
	}
	if d := mustDecide(t, other, req); !d.Allowed {
		t.Fatalf("other instance: %+v", d)
	}
	if err := f.m.Revoke(ctx, gid, patient, true, ""); err != nil {
		t.Fatal(err)
	}
	if d := mustDecide(t, f.m, req); d.Allowed || d.Hide || d.Reason != "grant_expired" {
		t.Fatalf("same instance after revoke: want denied (consent.required), got %+v", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		d := mustDecide(t, other, req)
		if !d.Allowed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("other instance still allows access after revoke (NOTIFY not applied)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if f.auditCount(t, "allowed") < 2 || f.auditCount(t, "denied") < 1 {
		t.Fatal("allowed reads and denials must be audited")
	}
}

// TC-02 (E-DP-02): sharing expires mid-consultation; reads fail at the exact time with
// no silent extension; T1 current-care data still works within its own window.
func TestTC02ShareExpiresMidConsultation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	if _, err := f.m.GrantT1(ctx, patient, doc, clinic, booking, f.now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.ShareT3(ctx, patient, doc, clinic, booking, f.now.Add(10*time.Minute), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	hist := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory}
	cur := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}
	if d := mustDecide(t, f.m, hist); !d.Allowed {
		t.Fatalf("before expiry: %+v", d)
	}
	f.now = f.now.Add(10 * time.Minute) // exactly at expiry
	f.m.FlushCache()
	if d := mustDecide(t, f.m, hist); d.Allowed || d.Reason != "grant_expired" {
		t.Fatalf("at expiry: want grant_expired, got %+v", d)
	}
	if d := mustDecide(t, f.m, cur); !d.Allowed || d.Tier != rp.T1CurrentCare {
		t.Fatalf("T1 must still cover the current consult: %+v", d)
	}
	f.now = f.now.Add(25 * time.Hour) // past slot end + 24 h window
	f.m.FlushCache()
	if d := mustDecide(t, f.m, cur); d.Allowed {
		t.Fatalf("T1 must end after the completion window: %+v", d)
	}
}

func TestTierScopes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, otherDoc, clinic, otherClinic, booking := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	if _, err := f.m.GrantT1(ctx, patient, doc, clinic, booking, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.GrantT2(ctx, patient, doc, clinic); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		req    rp.Request
		allow  bool
		hide   bool
		reason string
	}{
		{"T1 current consult", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}, true, false, ""},
		{"T1 other booking", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: ids.New()}, false, false, "out_of_scope"},
		{"T2 own records", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: doc}, true, false, ""},
		{"T2 clinic's records", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: clinic}, true, false, ""},
		{"T2 another doctor's records", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: otherDoc}, false, false, "out_of_scope"},
		{"history without share", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory}, false, false, "out_of_scope"},
		{"front desk booking fields", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "front_desk", ClinicRef: clinic}, PatientRef: patient, Kind: rp.KindBooking, BookingRef: booking}, true, false, ""},
		{"front desk clinical data", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "front_desk", ClinicRef: clinic}, PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}, false, true, "role_not_allowed"},
		{"front desk of another clinic", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "front_desk", ClinicRef: otherClinic}, PatientRef: patient, Kind: rp.KindBooking, BookingRef: booking}, false, true, "no_relationship"},
		{"unrelated doctor (IDOR)", rp.Request{Actor: doctor(otherDoc, otherClinic), PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: otherDoc}, false, true, "no_relationship"},
		{"reviewer role", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "reviewer"}, PatientRef: patient, Kind: rp.KindBooking}, false, true, "role_not_allowed"},
		{"reserved nurse role", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "nurse"}, PatientRef: patient, Kind: rp.KindClinicalCurrent}, false, true, "role_not_allowed"},
		{"another patient", rp.Request{Actor: rp.Actor{Ref: ids.New(), Role: "patient"}, PatientRef: patient, Kind: rp.KindBooking}, false, true, "role_not_allowed"},
		{"purpose not granted", rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking, Purpose: "P-REMIND"}, false, false, "purpose_not_granted"},
	}
	for _, c := range cases {
		d := mustDecide(t, f.m, c.req)
		if d.Allowed != c.allow || d.Hide != c.hide || (c.reason != "" && d.Reason != c.reason) {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
	// The patient reads their own data without a grant.
	if d := mustDecide(t, f.m, rp.Request{Actor: rp.Actor{Ref: patient, Role: "patient"}, PatientRef: patient, Kind: rp.KindClinicalHistory}); !d.Allowed || d.Tier != rp.Self {
		t.Fatalf("self read: %+v", d)
	}
}

func TestT1NeedsCareConsent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	if _, err := f.m.GrantT1(ctx, patient, doc, clinic, booking, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	d := mustDecide(t, f.m, rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking})
	if d.Allowed {
		t.Fatal("T1 must not work without P-CARE")
	}
}

// TC-11 (E-DP-16): staff without a JIT grant are denied and audited; with a grant only in
// scope and time; break-glass raises an alert; no self-approval; support never gets
// clinical data.
func TestTC11StaffJustInTimeAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, staff, approver := ids.New(), ids.New(), ids.New()
	f.roles[staff+"|grievance_officer"], f.roles[approver+"|security_owner"] = true, true
	go1 := rp.Actor{Ref: staff, Role: "grievance_officer"}
	req := rp.Request{Actor: go1, PatientRef: patient, Kind: rp.KindClinicalOwn, OwnerRef: ids.New()}

	if d := mustDecide(t, f.m, req); d.Allowed || !d.Hide {
		t.Fatalf("no grant: want hidden deny, got %+v", d)
	}
	if f.auditCount(t, "denied") != 1 {
		t.Fatal("staff denial must be audited")
	}
	_, err := f.m.GrantJIT(ctx, rp.JITRequest{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn},
		ReasonCode: "complaint", TicketRef: "GRV-1", ApproverRef: staff, Duration: time.Hour})
	if !errors.Is(err, rp.ErrSelfApproval) {
		t.Fatalf("self-approval: want ErrSelfApproval, got %v", err)
	}
	for _, bad := range []rp.JITRequest{
		{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "other", TicketRef: "T", ApproverRef: approver, Duration: time.Hour},
		{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "complaint", TicketRef: "T", ApproverRef: approver, Duration: 5 * time.Hour},
		{StaffRef: staff, StaffRole: "support", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "complaint", TicketRef: "T", ApproverRef: approver, Duration: time.Hour},
		{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "payment_dispute", TicketRef: "T", ApproverRef: approver, Duration: time.Hour},
		{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "complaint", TicketRef: "", ApproverRef: approver, Duration: time.Hour},
		{StaffRef: staff, StaffRole: "doctor", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn}, ReasonCode: "complaint", TicketRef: "T", ApproverRef: approver, Duration: time.Hour},
	} {
		if _, err := f.m.GrantJIT(ctx, bad); err == nil {
			t.Errorf("JIT request must be refused: %+v", bad)
		}
	}
	gid, err := f.m.GrantJIT(ctx, rp.JITRequest{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn},
		ReasonCode: "complaint", TicketRef: "GRV-1", ApproverRef: approver, Duration: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if d := mustDecide(t, f.m, req); !d.Allowed || d.GrantID != gid || d.Tier != rp.JITStaff {
		t.Fatalf("in scope: %+v", d)
	}
	if d := mustDecide(t, f.m, rp.Request{Actor: go1, PatientRef: patient, Kind: rp.KindDocuments}); d.Allowed {
		t.Fatalf("out of scope kind must be denied: %+v", d)
	}
	f.now = f.now.Add(time.Hour)
	f.m.FlushCache()
	if d := mustDecide(t, f.m, req); d.Allowed {
		t.Fatalf("after expiry: %+v", d)
	}
	// Break-glass: security owner only, during an incident; alerts.
	sec := ids.New()
	f.roles[sec+"|security_owner"] = true
	if _, err := f.m.GrantJIT(ctx, rp.JITRequest{StaffRef: sec, StaffRole: "security_owner", PatientRef: patient, Kinds: []rp.DataKind{rp.KindBooking},
		ReasonCode: "security_incident", TicketRef: "INC-7", Duration: time.Hour, BreakGlass: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.alerts) != 1 || f.alerts[0] != "access.break_glass" {
		t.Fatalf("break-glass must alert: %v", f.alerts)
	}
	if d := mustDecide(t, f.m, rp.Request{Actor: rp.Actor{Ref: sec, Role: "security_owner"}, PatientRef: patient, Kind: rp.KindBooking}); !d.Allowed || d.Tier != rp.BreakGlass {
		t.Fatalf("break-glass read: %+v", d)
	}
}

func TestConsentRules(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := ids.New()
	f.consent(t, p, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-REMIND")
	if ok, _ := f.m.HasConsent(ctx, p, "P-REMIND"); !ok {
		t.Fatal("P-REMIND granted")
	}
	if err := f.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-CARE", Channel: "app", NoticeVersion: "PN-1.0"}); !errors.Is(err, rp.ErrNeededConsent) {
		t.Fatalf("needed purpose withdrawal: want ErrNeededConsent, got %v", err)
	}
	if err := f.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-REMIND", Channel: "app", NoticeVersion: "PN-1.0"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.m.HasConsent(ctx, p, "P-REMIND"); ok {
		t.Fatal("withdrawal must take effect immediately")
	}
	for _, c := range []rp.ConsentChange{
		{SubjectRef: p, PurposeID: "P-REMIND", TextVersion: 1, NoticeVersion: "PN-1.0", Channel: "app"}, // old text version
		{SubjectRef: p, PurposeID: "P-UNKNOWN", TextVersion: 1, NoticeVersion: "PN-1.0", Channel: "app"},
		{SubjectRef: p, GivenByRef: ids.New(), PurposeID: "P-REMIND", TextVersion: 2, NoticeVersion: "PN-1.0", Channel: "app"}, // on behalf (adults only)
		{SubjectRef: p, PurposeID: "P-REMIND", TextVersion: 2, NoticeVersion: "PN-1.0", Channel: "fax"},
	} {
		if err := f.m.GrantConsent(ctx, c); !errors.Is(err, rp.ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", c, err)
		}
	}
	// The consent log is append-only for the module role.
	if _, err := f.env.Pools.For("records").Exec(ctx, `DELETE FROM consent_record`); err == nil {
		t.Fatal("module role must not delete consent records")
	}
	// Patients cannot revoke T1/T2 (legal duty) or someone else's grant.
	doc, clinic := ids.New(), ids.New()
	t2, _ := f.m.GrantT2(ctx, p, doc, clinic)
	if err := f.m.Revoke(ctx, t2, p, true, ""); err == nil {
		t.Fatal("patient must not revoke T2")
	}
	if err := f.m.Revoke(ctx, t2, ids.New(), true, ""); !errors.Is(err, rp.ErrInvalid) {
		t.Fatal("revoking someone else's grant must look like 'not found'")
	}
}

func TestAuditFailureRefusesRead(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(t, patient, "P-ACCOUNT", "P-CARE")
	if _, err := f.m.GrantT1(ctx, patient, doc, clinic, booking, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Owner.Exec(ctx, `REVOKE INSERT ON audit.audit_outbox FROM mugizh_mod_records`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Authorize(ctx, rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}); err == nil {
		t.Fatal("a read whose audit event cannot be written must fail")
	}
}

// Review M5: a staff grant needs a requester who really holds the staff role and an
// approver who really is a security owner; without a role checker nothing is granted.
// Review L8: a restricted (not yet re-verified) session is refused by the gate itself,
// even for the patient's own data, and the denial is audited.
func TestReviewFixesRecords(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, staff, approver, impostor := ids.New(), ids.New(), ids.New(), ids.New()
	f.roles[staff+"|grievance_officer"] = true
	f.roles[approver+"|security_owner"] = true
	req := rp.JITRequest{StaffRef: staff, StaffRole: "grievance_officer", PatientRef: patient, Kinds: []rp.DataKind{rp.KindClinicalOwn},
		ReasonCode: "complaint", TicketRef: "GRV-9", ApproverRef: impostor, Duration: time.Hour}
	if _, err := f.m.GrantJIT(ctx, req); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("approver without the security-owner role: %v", err)
	}
	req.ApproverRef = approver
	req.StaffRef = impostor
	if _, err := f.m.GrantJIT(ctx, req); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("requester without the claimed role: %v", err)
	}
	bare := f.newInstance(t, time.Second)
	bare.SetRoleChecker(nil)
	req.StaffRef = staff
	if _, err := bare.GrantJIT(ctx, req); err == nil {
		t.Fatal("staff grant without a role checker must be refused")
	}
	if _, err := f.m.GrantJIT(ctx, req); err != nil {
		t.Fatalf("valid staff grant: %v", err)
	}

	before := f.auditCount(t, "denied")
	d := mustDecide(t, f.m, rp.Request{Actor: rp.Actor{Ref: patient, Role: "patient", Restricted: true}, PatientRef: patient, Kind: rp.KindDocuments})
	if d.Allowed || d.Reason != "session_restricted" {
		t.Fatalf("restricted self read: %+v", d)
	}
	if f.auditCount(t, "denied") != before+1 {
		t.Fatal("restricted denial must be audited")
	}
}

// A cancelled or expired appointment ends the doctor's current-care access (T1) at once.
func TestRevokeForBookingEndsT1(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(t, patient, "P-ACCOUNT", "P-CARE")
	if _, err := f.m.GrantT1(ctx, patient, doc, clinic, booking, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}
	if d := mustDecide(t, f.m, req); !d.Allowed {
		t.Fatalf("before cancellation: %+v", d)
	}
	if err := f.m.RevokeForBooking(ctx, booking); err != nil {
		t.Fatal(err)
	}
	if d := mustDecide(t, f.m, req); d.Allowed {
		t.Fatalf("after cancellation T1 must be gone: %+v", d)
	}
	if err := f.m.RevokeForBooking(ctx, booking); err != nil {
		t.Fatalf("second revoke must be a no-op: %v", err)
	}
}
