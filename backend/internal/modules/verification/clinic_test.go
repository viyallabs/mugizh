package verification_test

import (
	"errors"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// D-38 / S-117 part 2: clinic (facility) verification. Synthetic data only.
// That a Verified clinic makes its practices listed, and a suspended clinic delists them,
// is proved in the directory module's tests (TestTC44ClinicSuspensionDelistsAllPractices)
// from the event payload asserted here; modules may not import each other's internals.

func clinicDraft() verification.ClinicDraftInput {
	return verification.ClinicDraftInput{AuthorityName: "State Clinical Establishments Authority", RegistrationNumber: "CE/2019/0042",
		RegistrationYear: 2019, LegalName: "Sample Care Clinic Pvt Ltd"}
}

func pdf() []byte { return append([]byte("%PDF-1.4 "), marker...) }

// clinicReady gives the clinic admin consent, a full draft and the required document.
func (f *fx) clinicReady(t *testing.T, admin verification.ClinicActor, clinic string, number string) {
	t.Helper()
	f.consent(admin.UserID)
	d := clinicDraft()
	if number != "" {
		d.RegistrationNumber = number
	}
	if _, err := f.m.SaveClinicDraft(ctx(), admin, clinic, d); err != nil {
		t.Fatalf("clinic draft: %v", err)
	}
	if _, err := f.m.UploadClinicEvidence(ctx(), admin, clinic, verification.EvidenceInput{Kind: verification.EvidenceRegistrationCertificate, ContentType: "application/pdf", Data: pdf()}); err != nil {
		t.Fatalf("clinic evidence: %v", err)
	}
}

func newClinic() (verification.ClinicActor, string) {
	c := ids.New()
	return verification.ClinicActor{UserID: ids.New(), ClinicRef: c}, c
}

func (f *fx) lastStatusEvent(t *testing.T) event {
	t.Helper()
	var last event
	for _, e := range f.events(t) {
		if e.Type == public.EventStatusChanged {
			last = e
		}
	}
	return last
}

func (f *fx) clinicStatus(t *testing.T, clinic string) public.Status {
	t.Helper()
	st, err := f.m.ClinicStatus(ctx(), clinic)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestD38_ClinicSubmissionAndApproval(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.clinicReady(t, admin, clinic, "")
	c, err := f.m.SubmitClinic(ctx(), admin, clinic)
	if err != nil || c.Status != public.StatusChecking || c.SubjectKind != verification.KindClinic || c.SubjectRef != clinic {
		t.Fatalf("submit: %v %+v", err, c)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusChecking {
		t.Fatalf("clinic status = %s", st)
	}
	// The doctor gate does not see a clinic.
	if f.isVerified(t, clinic) {
		t.Fatal("a clinic case answered the doctor gate")
	}
	rv := f.reviewerView(t, c.ID)
	if rv.AutoCheck != "passed" || rv.SubjectKind != verification.KindClinic {
		t.Fatalf("review view = %+v", rv)
	}
	res, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, "register_confirmed_manually")
	if err != nil || !res.Final || res.Status != public.StatusVerified {
		t.Fatalf("decision: %v %+v", err, res)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusVerified {
		t.Fatalf("clinic status = %s", st)
	}
	// Event payload: the clinic ID in professionalRef and subjectKind=clinic.
	var n int
	for _, e := range f.events(t) {
		if e.Type != public.EventStatusChanged {
			continue
		}
		n++
		if e.Subject != clinic || e.Payload[public.PayloadProfessional] != clinic || e.Payload[public.PayloadSubjectKind] != "clinic" || len(e.Payload) != 3 {
			t.Fatalf("event = %+v", e)
		}
	}
	if n == 0 {
		t.Fatal("no status events")
	}
	if last := f.lastStatusEvent(t); last.Payload[public.PayloadStatus] != "verified" {
		t.Fatalf("last event = %+v", last)
	}
}

// TC-44 / TC-66: a clinic suspension is announced so directory delists and booking cancels.
func TestTC44_ClinicSuspensionEmitsEvent(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.clinicReady(t, admin, clinic, "")
	c, _ := f.m.SubmitClinic(ctx(), admin, clinic)
	if _, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, "register_confirmed_manually"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.SuspendClinic(ctx(), clinic, clinic, "complaint"); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("self suspend: %v", err)
	}
	// A doctor suspend cannot reach a clinic.
	if err := f.m.Suspend(ctx(), ids.New(), clinic, "complaint"); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("doctor suspend on a clinic: %v", err)
	}
	if err := f.m.SuspendClinic(ctx(), ids.New(), clinic, "complaint"); err != nil {
		t.Fatal(err)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusSuspended {
		t.Fatalf("clinic status = %s", st)
	}
	last := f.lastStatusEvent(t)
	if last.Payload[public.PayloadProfessional] != clinic || last.Payload[public.PayloadStatus] != "suspended" || last.Payload[public.PayloadSubjectKind] != "clinic" {
		t.Fatalf("event = %+v", last)
	}
}

func TestD38_ClinicReverificationAndSuspensionByJob(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.clinicReady(t, admin, clinic, "")
	c, _ := f.m.SubmitClinic(ctx(), admin, clinic)
	if _, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, "register_confirmed_manually"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.TriggerClinicReverification(ctx(), ids.New(), clinic, "complaint"); err != nil {
		t.Fatal(err)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusReVerifyDue {
		t.Fatalf("status = %s", st)
	}
	f.now = f.now.Add(f.cfg.ReverifyGrace + time.Hour)
	if _, err := f.m.RunReverification(ctx(), f.now); err != nil {
		t.Fatal(err)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusSuspended {
		t.Fatalf("status = %s", st)
	}
	if last := f.lastStatusEvent(t); last.Payload[public.PayloadSubjectKind] != "clinic" || last.Payload[public.PayloadStatus] != "suspended" {
		t.Fatalf("event = %+v", last)
	}
	// The admin starts again: a new round.
	v, err := f.m.SaveClinicDraft(ctx(), admin, clinic, clinicDraft())
	if err != nil || v.Round != 2 {
		t.Fatalf("new round: %v %+v", err, v)
	}
}

// K-SEC-04: a clinic admin of another clinic gets not-found, for every operation.
func TestD38_OtherClinicsAdminGetsNotFound(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.clinicReady(t, admin, clinic, "")
	meta, err := f.m.UploadClinicEvidence(ctx(), admin, clinic, verification.EvidenceInput{Kind: verification.EvidenceEstablishmentProof, Data: jpeg()})
	if err != nil {
		t.Fatal(err)
	}
	other := verification.ClinicActor{UserID: ids.New(), ClinicRef: ids.New()}
	f.consent(other.UserID)
	nf := func(label string, err error) {
		t.Helper()
		if !errors.Is(err, verification.ErrNotFound) {
			t.Fatalf("%s: %v", label, err)
		}
	}
	_, err = f.m.GetClinicCase(ctx(), other, clinic)
	nf("get", err)
	_, err = f.m.SaveClinicDraft(ctx(), other, clinic, clinicDraft())
	nf("draft", err)
	_, err = f.m.UploadClinicEvidence(ctx(), other, clinic, verification.EvidenceInput{Kind: verification.EvidenceRegistrationCertificate, Data: jpeg()})
	nf("upload", err)
	_, err = f.m.SubmitClinic(ctx(), other, clinic)
	nf("submit", err)
	nf("delete", f.m.DeleteClinicEvidence(ctx(), other, clinic, meta.ID))
	// An admin cannot withdraw another clinic's evidence through their own clinic either.
	nf("delete via own clinic", f.m.DeleteClinicEvidence(ctx(), other, other.ClinicRef, meta.ID))
	// No acting clinic at all is the same.
	_, err = f.m.GetClinicCase(ctx(), verification.ClinicActor{UserID: ids.New()}, clinic)
	nf("no scope", err)
	if st := f.clinicStatus(t, clinic); st != public.StatusDraft {
		t.Fatalf("status = %s", st)
	}
}

// TC-20 / K-DP-05: a prohibited identifier is refused and nothing is stored.
func TestTC20_ClinicProhibitedIdentifierRefused(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.consent(admin.UserID)
	n := prohibitedNumber(t)
	for name, d := range map[string]verification.ClinicDraftInput{
		"number": {RegistrationNumber: n},
		"name":   {LegalName: "Clinic " + n},
		"auth":   {AuthorityName: "Authority " + n},
		"spaced": {LegalName: n[:4] + " " + n[4:8] + " " + n[8:]},
	} {
		if _, err := f.m.SaveClinicDraft(ctx(), admin, clinic, d); !errors.Is(err, verification.ErrProhibitedID) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var rows int
	if err := f.env.Owner.QueryRow(ctx(), `SELECT count(*) FROM verification.verification_case`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a refused input created a case: %d %v", rows, err)
	}
}

// A clinic sends registration documents only: no photo ID or doctor-only kinds.
func TestD38_ClinicEvidenceKindsAndCompleteness(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.consent(admin.UserID)
	if _, err := f.m.SaveClinicDraft(ctx(), admin, clinic, clinicDraft()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{verification.EvidencePhotoID, verification.EvidenceDegree, verification.EvidenceVerificationPhoto} {
		_, err := f.m.UploadClinicEvidence(ctx(), admin, clinic, verification.EvidenceInput{Kind: k, Data: jpeg()})
		var inv *verification.InvalidError
		if !errors.As(err, &inv) || inv.Field != "kind" {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if _, err := f.m.SubmitClinic(ctx(), admin, clinic); !errors.Is(err, verification.ErrIncomplete) {
		t.Fatalf("submit without document: %v", err)
	}
	// The doctor path cannot touch a clinic case.
	f.consent(clinic)
	if _, err := f.m.UploadEvidence(ctx(), clinic, verification.EvidenceInput{Kind: verification.EvidenceRegistrationCertificate, Data: jpeg()}); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("clinic case reached through the doctor path: %v", err)
	}
}

// D-VERIFY: the clinic admin needs consent, like a doctor.
func TestD38_ClinicAdminNeedsConsent(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	if _, err := f.m.SaveClinicDraft(ctx(), admin, clinic, clinicDraft()); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("draft: %v", err)
	}
	if _, err := f.m.UploadClinicEvidence(ctx(), admin, clinic, verification.EvidenceInput{Kind: verification.EvidenceRegistrationCertificate, Data: jpeg()}); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("upload: %v", err)
	}
	if _, err := f.m.SubmitClinic(ctx(), admin, clinic); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("submit: %v", err)
	}
}

// Four-eyes (TM-29): a clinic rejection needs a second, different reviewer.
func TestS129_ClinicRejectionNeedsSecondReviewer(t *testing.T) {
	f := setup(t)
	admin, clinic := newClinic()
	f.clinicReady(t, admin, clinic, "")
	c, _ := f.m.SubmitClinic(ctx(), admin, clinic)
	r1, r2 := ids.New(), ids.New()
	res, err := decide(f, c.ID, r1, verification.OutcomeRejected, "documents_not_genuine")
	if err != nil || res.Final || !res.AwaitingSecond {
		t.Fatalf("first: %v %+v", err, res)
	}
	if st := f.clinicStatus(t, clinic); st != public.StatusChecking {
		t.Fatalf("status after proposal = %s", st)
	}
	if _, err := decide(f, c.ID, r1, verification.OutcomeRejected, "documents_not_genuine"); !errors.Is(err, verification.ErrFourEyes) {
		t.Fatalf("same reviewer: %v", err)
	}
	res, err = decide(f, c.ID, r2, verification.OutcomeRejected, "documents_not_genuine")
	if err != nil || !res.Final || res.Status != public.StatusRejected {
		t.Fatalf("second: %v %+v", err, res)
	}
	last := f.lastStatusEvent(t)
	if last.Payload[public.PayloadStatus] != "rejected" || last.Payload[public.PayloadSubjectKind] != "clinic" {
		t.Fatalf("event = %+v", last)
	}
	if _, err := decide(f, c.ID, clinic, verification.OutcomeVerified, "checks_passed"); err == nil {
		t.Fatal("subject decided its own case")
	}
}

// A duplicate clinic registration number fails the automatic check, so Verified over it is
// an override that needs a second reviewer.
func TestD38_DuplicateClinicRegistrationFlagged(t *testing.T) {
	f := setup(t)
	a1, c1 := newClinic()
	f.clinicReady(t, a1, c1, "")
	first, _ := f.m.SubmitClinic(ctx(), a1, c1)
	if f.reviewerView(t, first.ID).AutoCheck != "passed" {
		t.Fatal("first clinic should pass the duplicate check")
	}
	a2, c2 := newClinic()
	f.clinicReady(t, a2, c2, "")
	second, _ := f.m.SubmitClinic(ctx(), a2, c2)
	v := f.reviewerView(t, second.ID)
	if v.AutoCheck != "failed" || len(v.ManualFlags) == 0 {
		t.Fatalf("duplicate not flagged: %+v", v)
	}
	res, err := decide(f, second.ID, ids.New(), verification.OutcomeVerified, "register_confirmed_manually")
	if err != nil || res.Final {
		t.Fatalf("override must wait for a second reviewer: %v %+v", err, res)
	}
}
