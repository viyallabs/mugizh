package records_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Caregiver access, phase 1 (v1.1; P-CAREGIVER, E-FAM-02, TC-47).

func setupCare(t *testing.T) *rx {
	t.Helper()
	x := setupRights(t)
	x.m.SetCaregiversEnabled(true)
	return x
}

func caregiverActor(ref string) rp.Actor { return rp.Actor{Ref: ref, Role: "caregiver"} }

// invite makes an invitation as a patient who has consented to P-CAREGIVER and just stepped up.
func (x *rx) invite(t *testing.T, patient string, scopes []string) rp.CaregiverInvitation {
	t.Helper()
	x.consent(t, patient, "P-CAREGIVER")
	inv, err := x.r.CreateCaregiverInvitation(context.Background(), x.patientRef(patient, true), scopes, "")
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func (x *rx) redeem(t *testing.T, caregiver, code string) rp.CaregiverLink {
	t.Helper()
	l, err := x.r.RedeemCaregiverCode(context.Background(), x.patientRef(caregiver, false), code, true)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (x *rx) read(t *testing.T, caregiver, patient string, kind rp.DataKind) rp.Decision {
	t.Helper()
	return mustDecide(t, x.m, rp.Request{Actor: caregiverActor(caregiver), PatientRef: patient, Kind: kind, ResourceType: "x", ResourceRef: ids.New()})
}

func (x *rx) caregiverAudit(t *testing.T, result string) int {
	t.Helper()
	var n int
	if err := x.env.Owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_outbox WHERE actor_role='caregiver' AND action LIKE 'records.read.%' AND result=$1`, result).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out: " + what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TC-47 (E-FAM-02): a caregiver with only reminders and appointments is denied prescriptions and documents until the
// patient adds them, and denied again the moment they are removed, on this instance and on another with a cache.
func TestTC47CaregiverSensitiveScopesNeedExplicitAdd(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	patient, cg := ids.New(), ids.New()
	inv := x.invite(t, patient, nil) // defaults
	if len(inv.Scopes) != 2 || inv.Scopes[0] != "reminders" || inv.Scopes[1] != "appointments" {
		t.Fatalf("default scopes: %v", inv.Scopes)
	}
	link := x.redeem(t, cg, inv.Code)

	other := x.newInstance(t, 60*time.Second)
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go other.Listen(lctx)
	time.Sleep(300 * time.Millisecond)
	req := func(k rp.DataKind) rp.Request {
		return rp.Request{Actor: caregiverActor(cg), PatientRef: patient, Kind: k, ResourceType: "x", ResourceRef: ids.New()}
	}

	for _, k := range []rp.DataKind{rp.KindReminders, rp.KindBooking} {
		if d := mustDecide(t, x.m, req(k)); !d.Allowed || d.Tier != rp.Caregiver || d.GrantID != link.ID {
			t.Fatalf("%s must be allowed: %+v", k, d)
		}
	}
	for _, k := range []rp.DataKind{rp.KindPrescriptions, rp.KindDocuments} {
		if d := mustDecide(t, x.m, req(k)); d.Allowed || d.Reason != "out_of_scope" || d.Hide {
			t.Fatalf("%s must be denied before the patient adds it: %+v", k, d)
		}
		if d := mustDecide(t, other, req(k)); d.Allowed {
			t.Fatalf("%s on the other instance: %+v", k, d)
		}
	}
	// Never for a caregiver: clinical notes, identity, payments and any unmapped kind (hidden).
	for _, k := range []rp.DataKind{rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindIdentityBasic, rp.KindPayment, "anything_new"} {
		if d := mustDecide(t, x.m, req(k)); d.Allowed || !d.Hide || d.Reason != "role_not_allowed" {
			t.Fatalf("%s must be refused to a caregiver: %+v", k, d)
		}
	}

	// Adding a sensitive scope needs a fresh step-up.
	if _, err := x.r.SetCaregiverScopes(ctx, x.patientRef(patient, false), link.ID, []string{"reminders", "appointments", "prescriptions"}); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("adding prescriptions without step-up: %v", err)
	}
	if d := mustDecide(t, x.m, req(rp.KindPrescriptions)); d.Allowed {
		t.Fatal("a refused change must not grant anything")
	}
	if _, err := x.r.SetCaregiverScopes(ctx, x.patientRef(patient, true), link.ID, []string{"reminders", "appointments", "prescriptions"}); err != nil {
		t.Fatal(err)
	}
	if d := mustDecide(t, x.m, req(rp.KindPrescriptions)); !d.Allowed {
		t.Fatalf("prescriptions after the patient adds them: %+v", d)
	}
	if d := mustDecide(t, x.m, req(rp.KindDocuments)); d.Allowed {
		t.Fatal("documents were not added")
	}
	waitFor(t, func() bool { return mustDecide(t, other, req(rp.KindPrescriptions)).Allowed }, "other instance sees the added scope")

	// Removing a scope needs no step-up and is immediate on both instances.
	if _, err := x.r.SetCaregiverScopes(ctx, x.patientRef(patient, false), link.ID, []string{"reminders", "appointments"}); err != nil {
		t.Fatal(err)
	}
	if d := mustDecide(t, x.m, req(rp.KindPrescriptions)); d.Allowed || d.Reason != "out_of_scope" {
		t.Fatalf("denied again after removal: %+v", d)
	}
	waitFor(t, func() bool { return !mustDecide(t, other, req(rp.KindPrescriptions)).Allowed }, "other instance denies after removal")
	if x.caregiverAudit(t, "allowed") < 3 || x.caregiverAudit(t, "denied") < 8 {
		t.Fatalf("every caregiver decision is audited: allowed=%d denied=%d", x.caregiverAudit(t, "allowed"), x.caregiverAudit(t, "denied"))
	}
}

func TestCaregiverInvitationNeedsConsentAndStepUp(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p := ids.New()
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, ""); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("no P-CAREGIVER consent: %v", err)
	}
	x.consent(t, p, "P-CAREGIVER")
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, false), nil, ""); !errors.Is(err, rp.ErrStepUpRequired) {
		t.Fatalf("no step-up: %v", err)
	}
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), []string{"notes"}, ""); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("scopes are a closed set: %v", err)
	}
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), []string{}, ""); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("empty scopes: %v", err)
	}
	if _, err := x.r.CreateCaregiverInvitation(ctx, rp.Caller{UserRef: p, Role: "patient", Restricted: true}, nil, ""); !errors.Is(err, rp.ErrDenied) {
		t.Fatalf("restricted session: %v", err)
	}
}

func TestCaregiverCodeIsHashOnlySingleUseAndExpires(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg, cg2 := ids.New(), ids.New(), ids.New()
	inv := x.invite(t, p, nil)
	if len(inv.Code) != 8 || strings.Trim(inv.Code, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
		t.Fatalf("code shape: %q", inv.Code)
	}
	if !inv.ExpiresAt.Equal(x.now.Add(48 * time.Hour)) {
		t.Fatalf("default life is 48 hours: %v", inv.ExpiresAt)
	}
	// Hash only at rest: the row holds no text that contains the code, and the hash is not the code.
	var hash []byte
	var rowText string
	if err := x.env.Owner.QueryRow(ctx, `SELECT code_hash, t::text FROM records.caregiver_invitation t WHERE id=$1`, inv.ID).Scan(&hash, &rowText); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 || bytes.Contains(hash, []byte(inv.Code)) || strings.Contains(strings.ToUpper(rowText), inv.Code) {
		t.Fatalf("the code must never be stored: %q", rowText)
	}
	// Case-insensitive and hyphen tolerant; single use.
	typed := strings.ToLower(inv.Code[:4]) + "-" + inv.Code[4:]
	l, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), typed, true)
	if err != nil || l.PatientRef != p || l.CaregiverRef != cg {
		t.Fatalf("redeem: %+v %v", l, err)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg2, false), inv.Code, true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("a used code: %v", err)
	}
	// Expiry.
	inv2 := x.invite(t, ids.New(), nil)
	x.now = x.now.Add(48*time.Hour + time.Second)
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg2, false), inv2.Code, true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("an expired code: %v", err)
	}
}

func TestCaregiverRedeemBadCodesAnswerTheSame(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	own := x.invite(t, p, nil)
	cancelled := x.invite(t, p, nil)
	if err := x.r.CancelCaregiverInvitation(ctx, x.patientRef(p, false), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.r.CancelCaregiverInvitation(ctx, x.patientRef(p, false), cancelled.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("cancelling twice: %v", err)
	}
	if err := x.r.CancelCaregiverInvitation(ctx, x.patientRef(ids.New(), false), own.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("someone else's invitation: %v", err)
	}
	// Own code (the patient cannot redeem), a cancelled code, a malformed code, an unknown code: one answer.
	for _, code := range []string{own.Code, cancelled.Code, "!!", "ABCDEFGH"} {
		who := cg
		if code == own.Code {
			who = p
		}
		if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(who, false), code, true); !errors.Is(err, rp.ErrNotFound) {
			t.Fatalf("code %q: %v", code, err)
		}
	}
	var n int
	if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("links: %d %v", n, err)
	}
	// The country must be confirmed.
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), own.Code, false); !errors.Is(err, rp.ErrCountryNotConfirmed) {
		t.Fatalf("country not confirmed: %v", err)
	}
	// Withdrawn consent makes an open invitation unusable (and revokes it).
	if err := x.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-CAREGIVER", NoticeVersion: "PN-1.0", Channel: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), own.Code, true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("after withdrawal: %v", err)
	}
}

func TestCaregiverRedeemRateLimit(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	inv := x.invite(t, p, nil)
	for i := 0; i < 5; i++ {
		if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), "ZZZZZZZ"+string("0123456789"[i]), true); !errors.Is(err, rp.ErrNotFound) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	// Locked, even with the right code.
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true); !errors.Is(err, rp.ErrRedeemLimited) {
		t.Fatalf("the sixth try: %v", err)
	}
	x.now = x.now.Add(30 * time.Minute)
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true); !errors.Is(err, rp.ErrRedeemLimited) {
		t.Fatalf("still locked inside the hour: %v", err)
	}
	x.now = x.now.Add(31 * time.Minute)
	if l := x.redeem(t, cg, inv.Code); l.PatientRef != p {
		t.Fatal("after the hour the right code works")
	}
}

func TestCaregiverLimits(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p := ids.New()
	var codes []string
	for i := 0; i < 5; i++ {
		codes = append(codes, x.invite(t, p, nil).Code)
	}
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, ""); !errors.Is(err, rp.ErrCaregiverLimit) {
		t.Fatalf("6th open invitation: %v", err)
	}
	for _, c := range codes {
		x.redeem(t, ids.New(), c)
	}
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, ""); !errors.Is(err, rp.ErrCaregiverLimit) {
		t.Fatalf("6th caregiver: %v", err)
	}
	// Per caregiver: 5 patients.
	cg := ids.New()
	for i := 0; i < 5; i++ {
		x.redeem(t, cg, x.invite(t, ids.New(), nil).Code)
	}
	extra := x.invite(t, ids.New(), nil)
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), extra.Code, true); !errors.Is(err, rp.ErrCaregiverLimit) {
		t.Fatalf("6th patient: %v", err)
	}
}

func TestCaregiverAlreadyLinked(t *testing.T) {
	x := setupCare(t)
	p, cg := ids.New(), ids.New()
	x.redeem(t, cg, x.invite(t, p, nil).Code)
	second := x.invite(t, p, nil)
	if _, err := x.r.RedeemCaregiverCode(context.Background(), x.patientRef(cg, false), second.Code, true); !errors.Is(err, rp.ErrAlreadyLinked) {
		t.Fatalf("already linked: %v", err)
	}
}

func TestCaregiverRevokeLeaveAndWithdrawalEndAccessAtOnce(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, a, b, c := ids.New(), ids.New(), ids.New(), ids.New()
	la := x.redeem(t, a, x.invite(t, p, nil).Code)
	x.redeem(t, b, x.invite(t, p, nil).Code)
	x.redeem(t, c, x.invite(t, p, nil).Code)
	for _, who := range []string{a, b, c} {
		if d := x.read(t, who, p, rp.KindReminders); !d.Allowed {
			t.Fatalf("before: %+v", d)
		}
	}
	// The patient revokes a (repeating is fine; someone else's link is not found).
	if err := x.r.RevokeCaregiver(ctx, x.patientRef(ids.New(), false), la.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("another patient's link: %v", err)
	}
	if err := x.r.RevokeCaregiver(ctx, x.patientRef(p, false), la.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.r.RevokeCaregiver(ctx, x.patientRef(p, false), la.ID); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if d := x.read(t, a, p, rp.KindReminders); d.Allowed || !d.Hide {
		t.Fatalf("revoked caregiver: %+v", d)
	}
	// b leaves.
	if err := x.r.LeaveCaregiving(ctx, x.patientRef(b, false), p); err != nil {
		t.Fatal(err)
	}
	if err := x.r.LeaveCaregiving(ctx, x.patientRef(b, false), p); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("leaving twice: %v", err)
	}
	if d := x.read(t, b, p, rp.KindReminders); d.Allowed {
		t.Fatalf("caregiver who left: %+v", d)
	}
	if ls, _, err := x.r.ListCaregivingPatients(ctx, x.patientRef(c, false), "", 20); err != nil || len(ls) != 1 || ls[0].PatientRef != p {
		t.Fatalf("my patients: %+v %v", ls, err)
	}
	ls1, next, err := x.r.ListMyCaregivers(ctx, x.patientRef(p, false), "", 2)
	if err != nil || len(ls1) != 2 || next == "" {
		t.Fatalf("my caregivers, page 1: %+v %q %v", ls1, next, err)
	}
	if ls2, next2, err := x.r.ListMyCaregivers(ctx, x.patientRef(p, false), next, 2); err != nil || len(ls2) != 1 || next2 != "" || ls2[0].ID == ls1[0].ID || ls2[0].ID == ls1[1].ID {
		t.Fatalf("my caregivers, page 2: %+v %q %v", ls2, next2, err)
	}
	// Withdrawing P-CAREGIVER ends every link at once.
	if d := x.read(t, c, p, rp.KindBooking); !d.Allowed {
		t.Fatalf("c before withdrawal: %+v", d)
	}
	if err := x.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-CAREGIVER", NoticeVersion: "PN-1.0", Channel: "app"}); err != nil {
		t.Fatal(err)
	}
	if d := x.read(t, c, p, rp.KindBooking); d.Allowed {
		t.Fatalf("c after withdrawal: %+v", d)
	}
	var active int
	if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link WHERE status='active'`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active links after withdrawal: %d %v", active, err)
	}
	// Consent given again does not bring the old link back.
	x.consent(t, p, "P-CAREGIVER")
	if d := x.read(t, c, p, rp.KindBooking); d.Allowed {
		t.Fatalf("re-consent must not revive a revoked link: %+v", d)
	}
	var revoked int
	if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action='caregiver.link_revoked'`).Scan(&revoked); err != nil || revoked != 3 {
		t.Fatalf("revocations are audited (revoke, leave, withdrawal): %d %v", revoked, err)
	}
}

// Without P-CAREGIVER consent an otherwise active link reads nothing (the gate re-checks it on every read).
func TestCaregiverGateNeedsConsentOnEveryRead(t *testing.T) {
	x := setupCare(t)
	p, cg := ids.New(), ids.New()
	x.redeem(t, cg, x.invite(t, p, nil).Code)
	if _, err := x.env.Owner.Exec(context.Background(), `UPDATE records.consent_state SET granted=false WHERE subject_ref=$1 AND purpose_id='P-CAREGIVER'`, p); err != nil {
		t.Fatal(err)
	}
	x.m.Invalidate(p)
	if d := x.read(t, cg, p, rp.KindReminders); d.Allowed || d.Reason != "purpose_not_granted" {
		t.Fatalf("link without consent: %+v", d)
	}
	// A caregiver cannot read the data of a patient they are not linked to, nor pose as the patient.
	if d := x.read(t, cg, ids.New(), rp.KindReminders); d.Allowed || !d.Hide {
		t.Fatalf("unlinked patient: %+v", d)
	}
	if d := x.read(t, p, p, rp.KindReminders); d.Allowed {
		t.Fatalf("role caregiver for oneself: %+v", d)
	}
}

func TestCaregiverErasureBothDirections(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg, p2, cg2 := ids.New(), ids.New(), ids.New(), ids.New()
	x.redeem(t, cg, x.invite(t, p, nil).Code)
	open := x.invite(t, p, nil) // an open invitation of the erasing patient
	x.redeem(t, cg2, x.invite(t, p2, nil).Code)
	x.redeem(t, cg, x.invite(t, p2, nil).Code)
	if d := x.read(t, cg, p, rp.KindReminders); !d.Allowed {
		t.Fatalf("before: %+v", d)
	}
	// A wrong code leaves a counter row for the caregiver, dropped at their erasure.
	_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), "ZZZZZZZZ", true)

	// The patient's erasure: links revoked, invitations deleted, link rows kept as restricted evidence, reads hidden.
	if _, err := x.r.RequestRights(ctx, x.patientRef(p, true), rp.RightsErasure, ""); err != nil {
		t.Fatal(err)
	}
	if d := x.read(t, cg, p, rp.KindReminders); d.Allowed || !d.Hide {
		t.Fatalf("after the patient's erasure request: %+v", d)
	}
	var invs, links, active int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_invitation WHERE patient_ref=$1`, p).Scan(&invs)
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link WHERE patient_ref=$1`, p).Scan(&links)
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link WHERE patient_ref=$1 AND status='active'`, p).Scan(&active)
	if invs != 0 || links != 1 || active != 0 {
		t.Fatalf("patient erasure: invitations=%d links=%d active=%d", invs, links, active)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), open.Code, true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("the erased patient's invitation: %v", err)
	}

	// The caregiver's erasure: their links are revoked and the patient's cache drops them at once.
	if d := x.read(t, cg, p2, rp.KindReminders); !d.Allowed {
		t.Fatalf("cg before own erasure: %+v", d)
	}
	if _, err := x.r.RequestRights(ctx, x.patientRef(cg, true), rp.RightsErasure, ""); err != nil {
		t.Fatal(err)
	}
	if d := x.read(t, cg, p2, rp.KindReminders); d.Allowed {
		t.Fatalf("after the caregiver's erasure: %+v", d)
	}
	if d := x.read(t, cg2, p2, rp.KindReminders); !d.Allowed {
		t.Fatalf("the other caregiver is unaffected: %+v", d)
	}
	var attempts int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link WHERE caregiver_ref=$1`, cg).Scan(&links)
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_redeem_attempt WHERE caregiver_ref=$1`, cg).Scan(&attempts)
	if links != 2 || attempts != 0 {
		t.Fatalf("caregiver erasure: links kept=%d attempts=%d", links, attempts)
	}
	// The module role cannot delete link rows (evidence).
	if _, err := x.env.Pools.For("records").Exec(ctx, `DELETE FROM caregiver_link`); err == nil {
		t.Fatal("link rows must not be deletable by the module role")
	}
}

func TestCaregiversOffAnswersNotFound(t *testing.T) {
	x := setupRights(t) // flag off by default
	ctx := context.Background()
	p := ids.New()
	x.consent(t, p, "P-CAREGIVER")
	if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, ""); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("flag off: %v", err)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), "ABCDEFGH", true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("flag off: %v", err)
	}
}
