package verification_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"image"
	stdjpeg "image/jpeg"
	stdpng "image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	msfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan/fake"
	vadapter "gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification"
	vfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

// ---- test doubles ----

// gate is a records gate double: only HasConsent matters to this module.
type gate struct {
	granted map[string]bool
	err     error
}

func (g *gate) Authorize(context.Context, rp.Request) (rp.Decision, error) {
	return rp.Decision{}, errors.New("not used")
}
func (g *gate) HasConsent(_ context.Context, subject, purpose string) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	return g.granted[subject+"|"+purpose], nil
}

// delStore is a local object store that can delete (the Deleter extension).
type delStore struct{ blobstore.LocalDir }

func (d delStore) Delete(_ context.Context, key string) error {
	err := os.Remove(filepath.Join(d.Root, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return blobstore.ErrNotFound
	}
	return err
}

// ---- fixture ----

type fx struct {
	env   *testkit.Env
	m     *verification.Module
	chk   *vfake.Checker
	scan  *msfake.Scanner
	gate  *gate
	store delStore
	cr    *crypto.Service
	cfg   verification.Config
	now   time.Time
}

var idTypes = []string{"pan", "passport", "driving_licence", "voter_id"}

const council = "Delhi Medical Council"

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func setup(t *testing.T, tweak ...func(*verification.Config)) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), verification.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := verification.DefaultConfig()
	cfg.ProhibitedIDValidators = []string{"verhoeff_12_digit_first_2_to_9", "digits_16"}
	cfg.IDTypes = idTypes
	cfg.CheckTimeout = 300 * time.Millisecond
	for _, f := range tweak {
		f(&cfg)
	}
	f := &fx{env: env, chk: &vfake.Checker{}, scan: &msfake.Scanner{}, gate: &gate{granted: map[string]bool{}}, store: delStore{blobstore.LocalDir{Root: t.TempDir()}},
		cr: cr, cfg: cfg, now: time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)}
	f.m, err = verification.New(env.Pools.For("verification"), cfg,
		verification.Deps{Crypto: cr, Blob: f.store, Checker: f.chk, Scanner: f.scan, Consent: f.gate}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f.m.SetClock(func() time.Time { return f.now })
	return f
}

func ctx() context.Context { return context.Background() }

var marker = []byte("SYNTHETIC-TEST-IMAGE-BYTES")

// jpeg and png are real, decodable images: uploads are re-encoded (TM-42), so magic bytes alone are refused.
func jpeg() []byte {
	var b bytes.Buffer
	_ = stdjpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 8)), nil)
	return b.Bytes()
}
func png() []byte {
	var b bytes.Buffer
	_ = stdpng.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 8)))
	return b.Bytes()
}

func (f *fx) consent(doc string) { f.gate.granted[doc+"|"+f.cfg.ConsentPurpose] = true }

func draft(number, name string) verification.DraftInput {
	return verification.DraftInput{CouncilCode: council, RegistrationNumber: number, RegistrationYear: 2010, RegisteredName: name,
		Qualifications: []string{"MBBS"}, SpecialtyClaim: "General Medicine", IDType: "pan"}
}

// ready creates a doctor with consent, a full draft and both required files, not yet submitted.
func (f *fx) ready(t *testing.T, doc string, d verification.DraftInput) {
	t.Helper()
	f.consent(doc)
	if _, err := f.m.SaveDraft(ctx(), doc, d); err != nil {
		t.Fatalf("draft: %v", err)
	}
	for _, k := range []string{verification.EvidenceRegistrationCertificate, verification.EvidencePhotoID} {
		if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: k, ContentType: "image/jpeg", Data: jpeg()}); err != nil {
			t.Fatalf("upload %s: %v", k, err)
		}
	}
}

func (f *fx) submitted(t *testing.T, doc string, d verification.DraftInput) *verification.Case {
	t.Helper()
	f.ready(t, doc, d)
	c, err := f.m.Submit(ctx(), doc)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return c
}

// registered adds the doctor to the fake register so the automatic check passes.
func (f *fx) registered(number, name string) { f.chk.Add(council, number, name) }

// verified takes a doctor all the way to Verified with a clean register match.
func (f *fx) verified(t *testing.T, doc, number, name string, licence *time.Time) *verification.Case {
	t.Helper()
	f.registered(number, name)
	c := f.submitted(t, doc, draft(number, name))
	if c.Status != public.StatusChecking {
		t.Fatalf("status after submit = %s", c.Status)
	}
	r, err := f.m.RecordDecision(ctx(), verification.DecisionInput{CaseID: c.ID, ReviewerRef: ids.New(), Outcome: verification.OutcomeVerified,
		ReasonCode: "checks_passed", LicenceExpiresOn: licence})
	if err != nil || !r.Final {
		t.Fatalf("decide: %v %+v", err, r)
	}
	return c
}

func (f *fx) status(t *testing.T, doc string) public.Status {
	t.Helper()
	s, err := f.m.Status(ctx(), doc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fx) isVerified(t *testing.T, doc string) bool {
	t.Helper()
	ok, err := f.m.IsVerified(ctx(), doc)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

type event struct {
	Type, Subject string
	Payload       map[string]string
}

func (f *fx) events(t *testing.T) []event {
	t.Helper()
	rs, err := f.env.Owner.Query(ctx(), `SELECT type, subject_ref, payload FROM verification.event_outbox ORDER BY occurred_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []event
	for rs.Next() {
		var e event
		if err := rs.Scan(&e.Type, &e.Subject, &e.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func (f *fx) statusEvents(t *testing.T, doc string) []string {
	var out []string
	for _, e := range f.events(t) {
		if e.Type == public.EventStatusChanged && e.Payload[public.PayloadProfessional] == doc {
			out = append(out, e.Payload[public.PayloadStatus])
		}
	}
	return out
}

func (f *fx) audits(t *testing.T) []string {
	t.Helper()
	rs, err := f.env.Owner.Query(ctx(), `SELECT action FROM audit.audit_outbox ORDER BY occurred_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var a string
		if err := rs.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func (f *fx) reviewerView(t *testing.T, caseID string) *verification.Case {
	t.Helper()
	c, err := f.m.OpenCase(ctx(), ids.New(), caseID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// prohibitedNumber builds a synthetic Verhoeff-valid 12-digit number (never a real one).
func prohibitedNumber(t *testing.T) string {
	t.Helper()
	for d := 0; d < 10; d++ {
		s := "23456789012" + string(rune('0'+d))
		if scrub.Verhoeff(s) {
			return s
		}
	}
	t.Fatal("no valid check digit")
	return ""
}

func decide(f *fx, caseID, reviewer, outcome, reason string) (*verification.DecisionResult, error) {
	return f.m.RecordDecision(ctx(), verification.DecisionInput{CaseID: caseID, ReviewerRef: reviewer, Outcome: outcome, ReasonCode: reason})
}

// ---- S-110: submission ----

// TC-43 (E-VER-02): a registration number without a council is rejected.
func TestTC43_CouncilRequired(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.consent(doc)
	if _, err := f.m.SaveDraft(ctx(), doc, verification.DraftInput{RegistrationNumber: "DMC/12345"}); !errors.Is(err, verification.ErrCouncilRequired) {
		t.Fatalf("number without council: %v", err)
	}
	// A council that normalises to nothing is no council.
	if _, err := f.m.SaveDraft(ctx(), doc, verification.DraftInput{CouncilCode: " .. ", RegistrationNumber: "DMC/12345"}); !errors.Is(err, verification.ErrCouncilRequired) {
		t.Fatalf("blank council: %v", err)
	}
	// A draft with neither can be saved but never submitted.
	if _, err := f.m.SaveDraft(ctx(), doc, verification.DraftInput{RegisteredName: "Asha Rao"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Submit(ctx(), doc); !errors.Is(err, verification.ErrCouncilRequired) {
		t.Fatalf("submit without council: %v", err)
	}
}

func TestS110_CouncilIsNormalised(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.consent(doc)
	c, err := f.m.SaveDraft(ctx(), doc, verification.DraftInput{CouncilCode: "  delhi   medical council. ", RegistrationNumber: " dmc / 12345 "})
	if err != nil {
		t.Fatal(err)
	}
	if c.CouncilCode != "DELHI MEDICAL COUNCIL" || c.RegistrationNumber != "DMC/12345" {
		t.Fatalf("normalised = %q %q", c.CouncilCode, c.RegistrationNumber)
	}
}

// TC-43 (E-VER-02): the same number under another council goes to manual review.
func TestTC43_SameNumberInTwoCouncilsGoesToManualReview(t *testing.T) {
	f := setup(t)
	a, b := ids.New(), ids.New()
	f.verified(t, a, "DMC/12345", "Asha Rao", nil)

	f.chk.Add("Maharashtra Medical Council", "DMC/12345", "Bimal Shah") // the register even matches B
	d := draft("DMC/12345", "Bimal Shah")
	d.CouncilCode = "Maharashtra Medical Council"
	c := f.submitted(t, b, d)
	if c.Status != public.StatusChecking {
		t.Fatalf("status = %s", c.Status)
	}
	v := f.reviewerView(t, c.ID)
	if v.AutoCheck != "failed" || !slices.Contains(v.ManualFlags, "registration_in_other_council") {
		t.Fatalf("auto=%s flags=%v", v.AutoCheck, v.ManualFlags)
	}
	if len(c.ManualFlags) != 0 || c.AutoCheck != "" {
		t.Fatal("the doctor must not see the flags")
	}
	if f.isVerified(t, b) {
		t.Fatal("never auto-verified")
	}
}

// TC-20 (E-DP-26): a prohibited identifier is rejected; nothing is stored.
func TestTC20_ProhibitedIdentifierRejectedNothingStored(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.consent(doc)
	n := prohibitedNumber(t)
	for name, d := range map[string]verification.DraftInput{
		"number":    {CouncilCode: council, RegistrationNumber: n},
		"name":      {RegisteredName: "Asha " + n},
		"spaced":    {SpecialtyClaim: n[:4] + " " + n[4:8] + " " + n[8:]},
		"qualified": {Qualifications: []string{"MBBS " + n}},
	} {
		if _, err := f.m.SaveDraft(ctx(), doc, d); !errors.Is(err, verification.ErrProhibitedID) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	var rows int
	if err := f.env.Owner.QueryRow(ctx(), `SELECT count(*) FROM verification.verification_case`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a rejected input created a case: %d %v", rows, err)
	}
}

func TestS110_ConsentRequired(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao")); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("draft without consent: %v", err)
	}
	f.consent(doc)
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao")); err != nil {
		t.Fatal(err)
	}
	f.gate.granted[doc+"|"+f.cfg.ConsentPurpose] = false // withdrawn
	if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: verification.EvidencePhotoID, Data: jpeg()}); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("upload without consent: %v", err)
	}
	if _, err := f.m.Submit(ctx(), doc); !errors.Is(err, verification.ErrConsentRequired) {
		t.Fatalf("submit without consent: %v", err)
	}
	// A failing consent check fails closed.
	f.gate.err = errors.New("gate down")
	f.consent(doc)
	if _, err := f.m.Submit(ctx(), doc); err == nil || errors.Is(err, verification.ErrState) {
		t.Fatalf("submit with failing gate: %v", err)
	}
}

func TestS110_PhotoIDAllowList(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.consent(doc)
	for _, bad := range []string{"national_id_card", "ID Card", "", "pan "} {
		d := draft("DMC/1", "Asha Rao")
		d.IDType = bad
		_, err := f.m.SaveDraft(ctx(), doc, d)
		if bad == "" {
			if err != nil {
				t.Fatalf("empty id type is allowed in a draft: %v", err)
			}
			continue
		}
		if !errors.Is(err, verification.ErrIDTypeNotAllowed) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	for _, ok := range idTypes {
		d := draft("DMC/1", "Asha Rao")
		d.IDType = ok
		if _, err := f.m.SaveDraft(ctx(), doc, d); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	// Submitting needs both required evidence kinds and an ID type.
	if _, err := f.m.Submit(ctx(), doc); !errors.Is(err, verification.ErrIncomplete) {
		t.Fatalf("submit without evidence: %v", err)
	}
}

func TestS110_FieldValidation(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.consent(doc)
	bad := []verification.DraftInput{
		{RegistrationYear: 1850},
		{RegistrationYear: f.now.Year() + 1},
		{RegisteredName: "<script>"},
		{Qualifications: make([]string, 11)},
		{Qualifications: []string{"x"}},
		{CouncilCode: council, RegistrationNumber: "!!"},
	}
	for i, d := range bad {
		if _, err := f.m.SaveDraft(ctx(), doc, d); !errors.Is(err, verification.ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

// ---- evidence ----

// Review M7: every evidence file is malware-scanned before it is encrypted or stored, and a
// scanner that fails or hangs rejects the file (fail closed). Nothing is stored either way.
func TestReviewM7EvidenceMalwareScan(t *testing.T) {
	f := setup(t, func(c *verification.Config) { c.ScanTimeout = 50 * time.Millisecond })
	doc := ids.New()
	f.consent(doc)
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/7", "Asha Rao")); err != nil {
		t.Fatal(err)
	}
	up := func(data []byte) error {
		_, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: verification.EvidenceDegree, ContentType: "application/pdf", Data: data})
		return err
	}
	infected := append([]byte("%PDF-1.7\n"), []byte(msfake.EICAR)...)
	if err := up(infected); !errors.Is(err, verification.ErrEvidenceMalware) {
		t.Fatalf("infected file: %v", err)
	}
	f.scan.FailWith = errors.New("scanner down")
	if err := up([]byte("%PDF-1.7\nclean")); !errors.Is(err, verification.ErrEvidenceScanUnavailable) {
		t.Fatalf("scanner error must reject: %v", err)
	}
	f.scan.FailWith = nil
	f.scan.Hang = true
	if err := up([]byte("%PDF-1.7\nclean")); !errors.Is(err, verification.ErrEvidenceScanUnavailable) {
		t.Fatalf("scanner timeout must reject: %v", err)
	}
	if left, _ := f.store.List(ctx(), "verification/"); len(left) != 0 {
		t.Fatalf("rejected files must not be stored: %v", left)
	}
	f.scan.Hang = false
	if err := up([]byte("%PDF-1.7\nclean")); err != nil {
		t.Fatalf("clean file: %v", err)
	}
	if f.scan.Calls() != 4 {
		t.Fatalf("every upload is scanned, got %d scans", f.scan.Calls())
	}
}

func TestEvidence_ChecksAndEncryption(t *testing.T) {
	f := setup(t, func(c *verification.Config) { c.MaxEvidenceBytes = 1024; c.MaxEvidencePerCase = 3 })
	doc := ids.New()
	f.consent(doc)
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao")); err != nil {
		t.Fatal(err)
	}
	up := func(kind, ct string, data []byte) error {
		_, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: kind, ContentType: ct, Data: data})
		return err
	}
	if err := up(verification.EvidencePhotoID, "image/jpeg", append(jpeg(), make([]byte, 2000)...)); !errors.Is(err, verification.ErrEvidenceTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if err := up(verification.EvidencePhotoID, "image/png", jpeg()); !errors.Is(err, verification.ErrEvidenceType) {
		t.Fatalf("declared png, bytes jpeg: %v", err)
	}
	if err := up(verification.EvidencePhotoID, "image/jpeg", []byte("plain text pretending to be an image")); !errors.Is(err, verification.ErrEvidenceType) {
		t.Fatalf("text file: %v", err)
	}
	if err := up(verification.EvidencePhotoID, "", []byte("MZ\x90\x00 an executable")); !errors.Is(err, verification.ErrEvidenceType) {
		t.Fatalf("executable: %v", err)
	}
	if err := up("selfie_video", "", jpeg()); !errors.Is(err, verification.ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	if err := up(verification.EvidencePhotoID, "", nil); !errors.Is(err, verification.ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
	for _, d := range [][]byte{jpeg(), png(), append([]byte("%PDF-1.7\n"), marker...)} {
		if err := up(verification.EvidenceDegree, "", d); err != nil {
			t.Fatalf("valid file refused: %v", err)
		}
	}
	if err := up(verification.EvidenceDegree, "", jpeg()); !errors.Is(err, verification.ErrEvidenceLimit) {
		t.Fatalf("limit: %v", err)
	}
	// Stored encrypted: no object holds the plaintext marker.
	keysList, err := f.store.List(ctx(), "verification/")
	if err != nil || len(keysList) != 3 {
		t.Fatalf("objects = %v %v", keysList, err)
	}
	for _, k := range keysList {
		b, _ := f.store.Get(ctx(), k)
		if bytes.Contains(b, marker) {
			t.Fatalf("object %s holds plaintext", k)
		}
	}
}

func TestEvidence_UploadNeedsEditableDraft(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: verification.EvidenceDegree, Data: jpeg()}); !errors.Is(err, verification.ErrState) {
		t.Fatalf("upload while in review: %v", err)
	}
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao")); !errors.Is(err, verification.ErrState) {
		t.Fatalf("edit while in review: %v", err)
	}
	if _, err := f.m.Submit(ctx(), doc); !errors.Is(err, verification.ErrState) {
		t.Fatalf("second submit: %v", err)
	}
}

// A doctor reading or deleting another doctor's data gets "not found".
func TestOwnership_OtherDoctorsDataIsNotFound(t *testing.T) {
	f := setup(t)
	a, b := ids.New(), ids.New()
	f.consent(a)
	f.consent(b)
	if _, err := f.m.SaveDraft(ctx(), a, draft("DMC/1", "Asha Rao")); err != nil {
		t.Fatal(err)
	}
	ev, err := f.m.UploadEvidence(ctx(), a, verification.EvidenceInput{Kind: verification.EvidenceDegree, Data: jpeg()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.GetMine(ctx(), b); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("B has no case: %v", err)
	}
	if _, err := f.m.SaveDraft(ctx(), b, draft("DMC/2", "Bimal Shah")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.DeleteEvidence(ctx(), b, ev.ID); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("B deletes A's evidence: %v", err)
	}
	mine, _ := f.m.GetMine(ctx(), b)
	if mine.SubjectRef != b || len(mine.Evidence) != 0 {
		t.Fatalf("B sees %+v", mine)
	}
	if err := f.m.DeleteEvidence(ctx(), a, ev.ID); err != nil {
		t.Fatalf("A withdraws own upload: %v", err)
	}
}

// ---- S-109 / S-129: automatic checks never approve ----

// TC-42 (E-VER-01): even a clean register match only reaches manual review.
func TestTC42_CleanMatchStillNeedsAHuman(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/12345", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/12345", "Asha Rao"))
	if c.Status != public.StatusChecking || f.isVerified(t, doc) {
		t.Fatalf("status=%s verified=%v", c.Status, f.isVerified(t, doc))
	}
	v := f.reviewerView(t, c.ID)
	if v.AutoCheck != "passed" || len(v.ManualFlags) != 0 {
		t.Fatalf("auto=%s flags=%v", v.AutoCheck, v.ManualFlags)
	}
	if len(v.Checks) != 1 || v.Checks[0].Outcome != "match" || v.Checks[0].RegisteredName != "Asha Rao" {
		t.Fatalf("checks = %+v", v.Checks)
	}
	if got := f.statusEvents(t, doc); !slices.Equal(got, []string{"submitted", "checking"}) {
		t.Fatalf("events = %v", got)
	}
}

// TC-42 / TC-62: a register name that differs goes to manual review and cannot be approved by one person.
func TestTC42_TC62_NameMismatchNeverApprovedByOneReviewer(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/12345", "Bimal Shah") // the real holder of the number
	c := f.submitted(t, doc, draft("DMC/12345", "Asha Rao"))
	v := f.reviewerView(t, c.ID)
	if v.AutoCheck != "failed" || !slices.Contains(v.ManualFlags, "registry_no_match") {
		t.Fatalf("auto=%s flags=%v", v.AutoCheck, v.ManualFlags)
	}
	r1 := ids.New()
	res, err := decide(f, c.ID, r1, verification.OutcomeVerified, "register_confirmed_manually")
	if err != nil || res.Final || !res.AwaitingSecond {
		t.Fatalf("override must wait for a second reviewer: %v %+v", err, res)
	}
	if f.isVerified(t, doc) || f.status(t, doc) != public.StatusChecking {
		t.Fatal("one reviewer must not verify over a failed check")
	}
	if _, err := decide(f, c.ID, r1, verification.OutcomeVerified, "register_confirmed_manually"); !errors.Is(err, verification.ErrFourEyes) {
		t.Fatalf("same reviewer confirming own proposal: %v", err)
	}
}

// A register that returns a match with a different name is also a failed check.
func TestTC42_MatchWithDifferentNameIsAFailedCheck(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/12345", "Asha Rao")
	// The fake compares names itself; force the case where the vendor says match but names differ.
	f.chk.ForceOutcome = vadapter.Match
	c := f.submitted(t, doc, draft("DMC/12345", "Asha Rao"))
	v := f.reviewerView(t, c.ID)
	if v.AutoCheck != "failed" || !slices.Contains(v.ManualFlags, "name_mismatch") {
		t.Fatalf("auto=%s flags=%v", v.AutoCheck, v.ManualFlags)
	}
}

// TC-45 (E-VER-04): registry or vendor trouble queues the case for manual review.
func TestTC45_RegistryProblemsGoToManualReview(t *testing.T) {
	cases := map[string]struct {
		set  func(*vfake.Checker)
		flag string
	}{
		"unavailable":  {func(c *vfake.Checker) { c.FailWith = adapters.E(adapters.Unavailable, "DOWN") }, "check_unavailable"},
		"rate limited": {func(c *vfake.Checker) { c.FailWith = adapters.E(adapters.RateLimited, "SLOW") }, "check_unavailable"},
		"auth failed":  {func(c *vfake.Checker) { c.FailWith = adapters.E(adapters.AuthFailed, "KEY") }, "check_unavailable"},
		"timeout":      {func(c *vfake.Checker) { c.Block = true }, "check_timeout"},
		"inconclusive": {func(c *vfake.Checker) { c.ForceOutcome = vadapter.Inconclusive }, "registry_inconclusive"},
		"not found":    {func(c *vfake.Checker) {}, "registry_not_found"},
		"unknown":      {func(c *vfake.Checker) { c.ForceOutcome = "something_new" }, "registry_inconclusive"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			doc := ids.New()
			tc.set(f.chk)
			c := f.submitted(t, doc, draft("DMC/777", "Asha Rao"))
			if c.Status != public.StatusChecking {
				t.Fatalf("status = %s", c.Status)
			}
			v := f.reviewerView(t, c.ID)
			if v.AutoCheck != "failed" || !slices.Contains(v.ManualFlags, tc.flag) {
				t.Fatalf("auto=%s flags=%v want %s", v.AutoCheck, v.ManualFlags, tc.flag)
			}
			if f.isVerified(t, doc) {
				t.Fatal("never auto-verified")
			}
		})
	}
}

// If the checks could not run at submit time, the job picks the case up later.
func TestTC45_ProcessPendingRunsDeferredChecks(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	if _, err := f.env.Owner.Exec(ctx(), `UPDATE verification.verification_case SET status='submitted', round_state='submitted', auto_check='', manual_flags='{}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Owner.Exec(ctx(), `DELETE FROM verification.check_result`); err != nil {
		t.Fatal(err)
	}
	if f.isVerified(t, doc) {
		t.Fatal("not verified while submitted")
	}
	n, err := f.m.ProcessPending(ctx(), 10)
	if err != nil || n != 1 {
		t.Fatalf("ProcessPending = %d %v", n, err)
	}
	if got := f.reviewerView(t, c.ID); got.Status != public.StatusChecking || got.AutoCheck != "passed" {
		t.Fatalf("after job: %s %s", got.Status, got.AutoCheck)
	}
	if n, _ := f.m.ProcessPending(ctx(), 10); n != 0 {
		t.Fatalf("second run = %d", n)
	}
}

// ---- S-129: decisions and four-eyes ----

func TestS129_DecisionNeedsAReasonCode(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	for _, reason := range []string{"", "because", "CHECKS_PASSED"} {
		if _, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, reason); !errors.Is(err, verification.ErrReasonRequired) {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	if _, err := decide(f, c.ID, ids.New(), "approved", "checks_passed"); !errors.Is(err, verification.ErrInvalid) {
		t.Fatalf("unknown outcome: %v", err)
	}
}

func TestS129_VerifiedAfterCleanChecksNeedsOneReviewer(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	if !f.isVerified(t, doc) || f.status(t, doc) != public.StatusVerified {
		t.Fatal("expected verified")
	}
	if got := f.statusEvents(t, doc); !slices.Equal(got, []string{"submitted", "checking", "verified"}) {
		t.Fatalf("events = %v", got)
	}
	for _, e := range f.events(t) {
		if e.Type == public.EventStatusChanged && (e.Subject != doc || e.Payload[public.PayloadSubjectKind] != verification.KindDoctor) {
			t.Fatalf("event envelope = %+v", e)
		}
	}
}

func TestS129_RejectionNeedsASecondDifferentReviewer(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	r1, r2 := ids.New(), ids.New()

	res, err := decide(f, c.ID, r1, verification.OutcomeRejected, "registration_invalid")
	if err != nil || res.Final || !res.AwaitingSecond {
		t.Fatalf("first rejection: %v %+v", err, res)
	}
	if got := f.status(t, doc); got != public.StatusChecking {
		t.Fatalf("status after proposal = %s", got)
	}
	if _, err := decide(f, c.ID, r1, verification.OutcomeRejected, "registration_invalid"); !errors.Is(err, verification.ErrFourEyes) {
		t.Fatalf("same reviewer: %v", err)
	}
	v := f.reviewerView(t, c.ID)
	if v.PendingOutcome != "rejected" || v.PendingBy != r1 {
		t.Fatalf("pending = %s %s", v.PendingOutcome, v.PendingBy)
	}
	res, err = decide(f, c.ID, r2, verification.OutcomeRejected, "identity_mismatch")
	if err != nil || !res.Final || res.Status != public.StatusRejected {
		t.Fatalf("second reviewer: %v %+v", err, res)
	}
	if f.isVerified(t, doc) || f.status(t, doc) != public.StatusRejected {
		t.Fatal("rejected")
	}
	if got := f.statusEvents(t, doc); got[len(got)-1] != "rejected" {
		t.Fatalf("events = %v", got)
	}
	// The log keeps both steps.
	v = f.reviewerView(t, c.ID)
	if len(v.Decisions) != 2 || v.Decisions[0].Stage != "proposed" || v.Decisions[1].Stage != "final" || v.Decisions[0].ReviewerRef == v.Decisions[1].ReviewerRef {
		t.Fatalf("decisions = %+v", v.Decisions)
	}
	// A decided case accepts no further decision.
	if _, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, "checks_passed"); !errors.Is(err, verification.ErrState) {
		t.Fatalf("after decision: %v", err)
	}
}

func TestS129_OverrideOfFailedCheckNeedsSecondReviewer(t *testing.T) {
	f := setup(t) // register has no entry: the automatic check fails
	doc := ids.New()
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	r1, r2 := ids.New(), ids.New()
	if res, err := decide(f, c.ID, r1, verification.OutcomeVerified, "register_confirmed_manually"); err != nil || !res.AwaitingSecond {
		t.Fatalf("%v %+v", err, res)
	}
	// A different outcome from the second reviewer replaces the proposal; it is not a confirmation.
	if res, err := decide(f, c.ID, r2, verification.OutcomeRejected, "identity_mismatch"); err != nil || res.Final || !res.AwaitingSecond {
		t.Fatalf("disagreement: %v %+v", err, res)
	}
	if f.isVerified(t, doc) {
		t.Fatal("disagreement must not verify")
	}
	// Now r1 confirms r2's rejection.
	if res, err := decide(f, c.ID, r1, verification.OutcomeRejected, "identity_mismatch"); err != nil || !res.Final || res.Status != public.StatusRejected {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestS129_OverrideConfirmedVerifiesAndMarksOverride(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	r1, r2 := ids.New(), ids.New()
	if _, err := decide(f, c.ID, r1, verification.OutcomeVerified, "register_confirmed_manually"); err != nil {
		t.Fatal(err)
	}
	res, err := decide(f, c.ID, r2, verification.OutcomeVerified, "register_confirmed_manually")
	if err != nil || !res.Final || res.Status != public.StatusVerified || !f.isVerified(t, doc) {
		t.Fatalf("%v %+v", err, res)
	}
	v := f.reviewerView(t, c.ID)
	if !v.Decisions[0].Override || !v.Decisions[1].Override {
		t.Fatalf("override not recorded: %+v", v.Decisions)
	}
}

func TestS129_ReviewerNeverDecidesOwnCase(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	if _, err := decide(f, c.ID, doc, verification.OutcomeVerified, "checks_passed"); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("decide: %v", err)
	}
	if _, err := f.m.OpenCase(ctx(), doc, c.ID); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("open: %v", err)
	}
	ev := f.reviewerView(t, c.ID).Evidence[0]
	if _, _, err := f.m.ReadEvidence(ctx(), doc, c.ID, ev.ID); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("read evidence: %v", err)
	}
	if err := f.m.ReportProhibitedID(ctx(), doc, c.ID, ev.ID); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("checklist action: %v", err)
	}
	q, _, err := f.m.ListQueue(ctx(), doc, 10, "")
	if err != nil || len(q) != 0 {
		t.Fatalf("own case in own queue: %v %v", q, err)
	}
	// The database refuses it too.
	if _, err := f.env.Pools.For("verification").Exec(ctx(), `INSERT INTO decision (id, case_id, subject_ref, round, reviewer_ref, outcome, stage, reason_code)
		VALUES ($1,$2,$3,1,$3,'verified','final','checks_passed')`, ids.New(), c.ID, doc); err == nil {
		t.Fatal("database accepted a self decision")
	}
}

func TestS129_DecisionLogIsAppendOnly(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	p := f.env.Pools.For("verification")
	if _, err := p.Exec(ctx(), `UPDATE decision SET reason_code = 'x'`); err == nil {
		t.Fatal("update allowed")
	}
	if _, err := p.Exec(ctx(), `DELETE FROM decision`); err == nil {
		t.Fatal("delete allowed")
	}
	if _, err := p.Exec(ctx(), `DELETE FROM check_result`); err == nil {
		t.Fatal("check delete allowed")
	}
}

func TestS129_NeedsInfoThenResubmit(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	res, err := decide(f, c.ID, ids.New(), verification.OutcomeNeedsInfo, "documents_unreadable")
	if err != nil || !res.Final || res.Status != public.StatusNeedsInfo {
		t.Fatalf("%v %+v", err, res)
	}
	mine, _ := f.m.GetMine(ctx(), doc)
	if mine.InfoRequested != "documents_unreadable" || mine.Status != public.StatusNeedsInfo {
		t.Fatalf("doctor sees %+v", mine)
	}
	if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: verification.EvidenceDegree, Data: png()}); err != nil {
		t.Fatal(err)
	}
	again, err := f.m.Submit(ctx(), doc)
	if err != nil || again.Status != public.StatusChecking || again.InfoRequested != "" {
		t.Fatalf("%v %+v", err, again)
	}
}

func TestS129_QueueOrderPagingAndSLA(t *testing.T) {
	f := setup(t)
	var docs []string
	for i := 0; i < 3; i++ {
		doc := ids.New()
		docs = append(docs, doc)
		f.registered("DMC/"+string(rune('1'+i)), "Asha Rao")
		f.submitted(t, doc, draft("DMC/"+string(rune('1'+i)), "Asha Rao"))
		f.now = f.now.Add(time.Hour)
	}
	reviewer := ids.New()
	page1, next, err := f.m.ListQueue(ctx(), reviewer, 2, "")
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page1 = %d %q %v", len(page1), next, err)
	}
	page2, next2, err := f.m.ListQueue(ctx(), reviewer, 2, next)
	if err != nil || len(page2) != 1 || next2 != "" {
		t.Fatalf("page2 = %d %q %v", len(page2), next2, err)
	}
	if page1[0].SubjectRef != docs[0] || page1[1].SubjectRef != docs[1] || page2[0].SubjectRef != docs[2] {
		t.Fatal("not oldest first")
	}
	if page1[0].Overdue {
		t.Fatal("not overdue after 3 hours")
	}
	if n := slices.Index(f.audits(t), "verification.queue_listed"); n < 0 {
		t.Fatal("reading the queue must be audited")
	}
	f.now = f.now.Add(49 * time.Hour)
	q, _, _ := f.m.ListQueue(ctx(), reviewer, 10, "")
	if !q[0].Overdue || !q[0].DueBy.Equal(page1[0].SubmittedAt.Add(48*time.Hour)) {
		t.Fatalf("SLA = %+v", q[0])
	}
	if _, _, err := f.m.ListQueue(ctx(), reviewer, 10, "garbage"); !errors.Is(err, verification.ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
}

// ---- TC-21 (E-DP-27): a prohibited identity document is hard-deleted at once ----

func TestTC21_ProhibitedIDDocumentDeletedAtOnce(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.registered("DMC/1", "Asha Rao")
	c := f.submitted(t, doc, draft("DMC/1", "Asha Rao"))
	var idEv verification.EvidenceMeta
	for _, e := range f.reviewerView(t, c.ID).Evidence {
		if e.Kind == verification.EvidencePhotoID {
			idEv = e
		}
	}
	keysBefore, _ := f.store.List(ctx(), "verification/")
	reviewer := ids.New()

	// Wrong evidence ID in the case: not found.
	if err := f.m.ReportProhibitedID(ctx(), reviewer, c.ID, ids.New()); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("unknown evidence: %v", err)
	}
	if err := f.m.ReportProhibitedID(ctx(), reviewer, c.ID, idEv.ID); err != nil {
		t.Fatal(err)
	}
	// Object gone, key destroyed, row marked, nothing readable.
	keysAfter, _ := f.store.List(ctx(), "verification/")
	if len(keysAfter) != len(keysBefore)-1 {
		t.Fatalf("objects %d -> %d", len(keysBefore), len(keysAfter))
	}
	if _, _, err := f.m.ReadEvidence(ctx(), reviewer, c.ID, idEv.ID); !errors.Is(err, verification.ErrEvidenceGone) {
		t.Fatalf("read after delete: %v", err)
	}
	var state, reason string
	if err := f.env.Owner.QueryRow(ctx(), `SELECT state, delete_reason FROM verification.evidence WHERE id = $1`, idEv.ID).Scan(&state, &reason); err != nil || state != "deleted" || reason != "prohibited_id" {
		t.Fatalf("row = %s %s %v", state, reason, err)
	}
	var sealedBlob int
	_ = f.env.Owner.QueryRow(ctx(), `SELECT count(*) FROM verification.evidence WHERE id = $1 AND state = 'stored'`, idEv.ID).Scan(&sealedBlob)
	if sealedBlob != 0 {
		t.Fatal("evidence still stored")
	}
	// Audited, and the doctor is asked for another ID.
	if !slices.Contains(f.audits(t), "verification.prohibited_id_deleted") {
		t.Fatalf("audits = %v", f.audits(t))
	}
	mine, _ := f.m.GetMine(ctx(), doc)
	if mine.Status != public.StatusNeedsInfo || mine.InfoRequested != "prohibited_id" {
		t.Fatalf("doctor sees %s %s", mine.Status, mine.InfoRequested)
	}
	for _, e := range mine.Evidence {
		if e.ID == idEv.ID {
			t.Fatal("deleted file still listed to the doctor")
		}
	}
	// Reporting it again is refused (nothing left, and the case is no longer in checking).
	if err := f.m.ReportProhibitedID(ctx(), reviewer, c.ID, idEv.ID); err == nil {
		t.Fatal("second report accepted")
	}
	// The doctor uploads another ID and resubmits.
	if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: verification.EvidencePhotoID, Data: png()}); err != nil {
		t.Fatal(err)
	}
	if again, err := f.m.Submit(ctx(), doc); err != nil || again.Status != public.StatusChecking {
		t.Fatalf("%v %+v", err, again)
	}
}

// ---- RET-08: ID images are purged 90 days after the final decision ----

func TestRET08_PurgeEvidenceNinetyDaysAfterDecision(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	c := f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	ev := f.reviewerView(t, c.ID).Evidence
	reviewer := ids.New()

	n, err := f.m.PurgeEvidence(ctx(), f.now.Add(89*24*time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("early purge = %d %v", n, err)
	}
	// The stored image is the re-encoded one (TM-42): the same 8x8 JPEG, without whatever else the upload carried.
	if b, _, err := f.m.ReadEvidence(ctx(), reviewer, c.ID, ev[0].ID); err != nil {
		t.Fatalf("evidence must be readable before the deadline: %v", err)
	} else if cfg, format, derr := image.DecodeConfig(bytes.NewReader(b)); derr != nil || format != "jpeg" || cfg.Width != 8 {
		t.Fatalf("readable evidence is the re-encoded image: %v %s %+v", derr, format, cfg)
	}
	n, err = f.m.PurgeEvidence(ctx(), f.now.Add(91*24*time.Hour))
	if err != nil || n != len(ev) {
		t.Fatalf("purge = %d %v", n, err)
	}
	for _, e := range ev {
		if _, _, err := f.m.ReadEvidence(ctx(), reviewer, c.ID, e.ID); !errors.Is(err, verification.ErrEvidenceGone) {
			t.Fatalf("after purge: %v", err)
		}
	}
	if left, _ := f.store.List(ctx(), "verification/"); len(left) != 0 {
		t.Fatalf("objects left: %v", left)
	}
	if !slices.Contains(f.audits(t), "verification.evidence_purged") {
		t.Fatal("purge not audited")
	}
	// The case itself stays, and a second run finds nothing.
	if !f.isVerified(t, doc) {
		t.Fatal("purging evidence must not change the verification")
	}
	if n, _ := f.m.PurgeEvidence(ctx(), f.now.Add(92*24*time.Hour)); n != 0 {
		t.Fatalf("second purge = %d", n)
	}
}

func TestRET08_AbandonedDraftEvidenceIsPurged(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.ready(t, doc, draft("DMC/1", "Asha Rao"))
	if n, _ := f.m.PurgeEvidence(ctx(), f.now.Add(10*24*time.Hour)); n != 0 {
		t.Fatalf("early = %d", n)
	}
	if n, err := f.m.PurgeEvidence(ctx(), f.now.Add(91*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("abandoned = %d %v", n, err)
	}
}

// ---- S-118 / TC-44: the prescribing gate fails closed ----

func TestTC44_GateIsClosedUnlessVerified(t *testing.T) {
	f := setup(t)
	if ok, err := f.m.IsVerified(ctx(), ids.New()); ok || err != nil {
		t.Fatalf("unknown professional: %v %v", ok, err)
	}
	if ok, err := f.m.IsVerified(ctx(), "not-an-id"); ok || err != nil {
		t.Fatalf("malformed ref: %v %v", ok, err)
	}
	if st := f.status(t, ids.New()); st != public.StatusNone {
		t.Fatalf("unknown status = %s", st)
	}
	drafted, submitted, rejected, suspended := ids.New(), ids.New(), ids.New(), ids.New()
	f.consent(drafted)
	if _, err := f.m.SaveDraft(ctx(), drafted, draft("DMC/9", "Draft Doctor")); err != nil {
		t.Fatal(err)
	}
	f.submitted(t, submitted, draft("DMC/8", "Submitted Doctor"))
	rc := f.submitted(t, rejected, draft("DMC/7", "Rejected Doctor"))
	_, _ = decide(f, rc.ID, ids.New(), verification.OutcomeRejected, "not_eligible")
	_, _ = decide(f, rc.ID, ids.New(), verification.OutcomeRejected, "not_eligible")
	f.verified(t, suspended, "DMC/6", "Suspended Doctor", nil)
	if !f.isVerified(t, suspended) {
		t.Fatal("verified before suspension")
	}
	if err := f.m.Suspend(ctx(), ids.New(), suspended, "licence_suspended"); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{"draft": drafted, "submitted": submitted, "rejected": rejected, "suspended": suspended} {
		if f.isVerified(t, doc) {
			t.Fatalf("%s must not pass the gate", name)
		}
	}
	if f.status(t, suspended) != public.StatusSuspended || f.status(t, rejected) != public.StatusRejected {
		t.Fatal("statuses")
	}
	// Suspension is announced for booking (E-VER-06, TC-66).
	if got := f.statusEvents(t, suspended); got[len(got)-1] != "suspended" {
		t.Fatalf("events = %v", got)
	}
}

func TestTC44_SuspendRules(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	if err := f.m.Suspend(ctx(), ids.New(), doc, "free text"); !errors.Is(err, verification.ErrReasonRequired) {
		t.Fatalf("reason: %v", err)
	}
	if err := f.m.Suspend(ctx(), doc, doc, "licence_suspended"); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("self: %v", err)
	}
	if err := f.m.Suspend(ctx(), ids.New(), ids.New(), "licence_suspended"); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := f.m.Suspend(ctx(), ids.New(), doc, "licence_suspended"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Suspend(ctx(), ids.New(), doc, "licence_suspended"); !errors.Is(err, verification.ErrState) {
		t.Fatalf("twice: %v", err)
	}
}

func TestTC44_GateFailsClosedOnError(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	f.env.Pools.Close()
	ok, err := f.m.IsVerified(ctx(), doc)
	if ok || err == nil {
		t.Fatalf("database down: %v %v", ok, err)
	}
}

// ---- S-160 / TC-44 / TC-66: re-verification ----

func TestS160_DueDateIsTheEarlierOfLicenceAndTwelveMonths(t *testing.T) {
	f := setup(t)
	soon := time.Date(2027, 5, 17, 0, 0, 0, 0, time.UTC)
	a := ids.New()
	ca := f.verified(t, a, "DMC/1", "Asha Rao", &soon)
	if v := f.reviewerView(t, ca.ID); v.ReVerifyDueAt == nil || !v.ReVerifyDueAt.Equal(soon) {
		t.Fatalf("due = %v, want licence expiry %v", v.ReVerifyDueAt, soon)
	}
	late := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	b := ids.New()
	cb := f.verified(t, b, "DMC/2", "Bimal Shah", &late)
	want := f.now.AddDate(0, 12, 0)
	if v := f.reviewerView(t, cb.ID); v.ReVerifyDueAt == nil || !v.ReVerifyDueAt.Equal(want) {
		t.Fatalf("due = %v, want %v", v.ReVerifyDueAt, want)
	}
	// A licence that has already ended cannot be verified.
	past := f.now.Add(-24 * time.Hour)
	f.registered("DMC/3", "Chitra Iyer")
	cc := f.submitted(t, ids.New(), draft("DMC/3", "Chitra Iyer"))
	if _, err := f.m.RecordDecision(ctx(), verification.DecisionInput{CaseID: cc.ID, ReviewerRef: ids.New(), Outcome: verification.OutcomeVerified,
		ReasonCode: "checks_passed", LicenceExpiresOn: &past}); !errors.Is(err, verification.ErrInvalid) {
		t.Fatalf("past licence: %v", err)
	}
}

func TestS160_TwelveMonthsThenGraceThenSuspended(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	due := f.now.AddDate(0, 12, 0)
	grace := f.cfg.ReverifyGrace

	if r, err := f.m.RunReverification(ctx(), due.Add(-time.Hour)); err != nil || r != (verification.ReverifyResult{}) {
		t.Fatalf("before due: %+v %v", r, err)
	}
	f.now = due
	r, err := f.m.RunReverification(ctx(), due)
	if err != nil || r.Due != 1 || r.Suspended != 0 {
		t.Fatalf("at due: %+v %v", r, err)
	}
	if f.status(t, doc) != public.StatusReVerifyDue || !f.isVerified(t, doc) {
		t.Fatal("still verified during the grace period")
	}
	if r, _ := f.m.RunReverification(ctx(), due); r != (verification.ReverifyResult{}) {
		t.Fatalf("idempotent: %+v", r)
	}
	f.now = due.Add(grace - time.Second)
	if r, _ := f.m.RunReverification(ctx(), f.now); r.Suspended != 0 || !f.isVerified(t, doc) {
		t.Fatalf("inside grace: %+v", r)
	}
	f.now = due.Add(grace)
	r, err = f.m.RunReverification(ctx(), f.now)
	if err != nil || r.Suspended != 1 {
		t.Fatalf("after grace: %+v %v", r, err)
	}
	if f.isVerified(t, doc) || f.status(t, doc) != public.StatusSuspended {
		t.Fatal("must be suspended")
	}
	// TC-66: the suspension is announced so booking can cancel and refund.
	got := f.statusEvents(t, doc)
	if !slices.Equal(got[len(got)-2:], []string{"re_verify_due", "suspended"}) {
		t.Fatalf("events = %v", got)
	}
	if !slices.Contains(f.audits(t), "verification.suspended") {
		t.Fatal("suspension not audited")
	}
}

// TC-44: the gate is closed even when the job has not run.
func TestS160_GateFailsClosedBeforeTheJobRuns(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	f.now = f.now.AddDate(0, 12, 0).Add(f.cfg.ReverifyGrace).Add(time.Second)
	if f.isVerified(t, doc) {
		t.Fatal("grace elapsed: must not be verified, job or not")
	}
	if got := f.status(t, doc); got != public.StatusSuspended {
		t.Fatalf("status = %s", got)
	}
}

func TestS160_ExpiredLicenceIsExpiredAtOnce(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	exp := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	f.verified(t, doc, "DMC/1", "Asha Rao", &exp)
	f.now = exp.Add(-time.Second)
	if r, _ := f.m.RunReverification(ctx(), f.now); r.Expired != 0 || !f.isVerified(t, doc) {
		t.Fatalf("before expiry: %+v", r)
	}
	f.now = exp
	if f.isVerified(t, doc) || f.status(t, doc) != public.StatusExpired {
		t.Fatal("the gate must close at licence expiry even before the job")
	}
	r, err := f.m.RunReverification(ctx(), f.now)
	if err != nil || r.Expired != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	got := f.statusEvents(t, doc)
	if got[len(got)-1] != "expired" {
		t.Fatalf("events = %v", got)
	}
}

func TestS160_ComplaintTriggersReverification(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	if err := f.m.TriggerReverification(ctx(), doc, doc, "complaint"); !errors.Is(err, verification.ErrSelfReview) {
		t.Fatalf("self: %v", err)
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "no_such_reason"); !errors.Is(err, verification.ErrReasonRequired) {
		t.Fatalf("reason: %v", err)
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "complaint"); err != nil {
		t.Fatal(err)
	}
	if f.status(t, doc) != public.StatusReVerifyDue || !f.isVerified(t, doc) {
		t.Fatal("due, still verified during grace")
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "complaint"); err != nil {
		t.Fatalf("repeat must be a no-op: %v", err)
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), ids.New(), "complaint"); !errors.Is(err, verification.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	f.now = f.now.Add(f.cfg.ReverifyGrace)
	if r, err := f.m.RunReverification(ctx(), f.now); err != nil || r.Suspended != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if f.isVerified(t, doc) {
		t.Fatal("suspended after grace")
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "complaint"); !errors.Is(err, verification.ErrState) {
		t.Fatalf("suspended: %v", err)
	}
	if !slices.Contains(f.audits(t), "verification.reverify_triggered") {
		t.Fatal("not audited")
	}
}

// Prescriptions print only the registration details reviewers approved: a re-verification
// draft never changes them until it is decided, and a doctor who is not verified gets none.
func TestPrescriberFactsAreTheReviewedSnapshot(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	if _, err := f.m.PrescriberFacts(ctx(), doc); !errors.Is(err, public.ErrNotVerified) {
		t.Fatalf("unknown doctor: %v", err)
	}
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	pf, err := f.m.PrescriberFacts(ctx(), doc)
	if err != nil || pf.RegisteredName != "Asha Rao" || pf.RegistrationNumber == "" || pf.CouncilCode == "" || len(pf.Qualifications) == 0 {
		t.Fatalf("facts after verification: %+v %v", pf, err)
	}
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "complaint"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/999", "Someone Else")); err != nil {
		t.Fatal(err)
	}
	again, err := f.m.PrescriberFacts(ctx(), doc)
	if err != nil || again.RegisteredName != "Asha Rao" || again.RegistrationNumber != pf.RegistrationNumber {
		t.Fatalf("an unreviewed draft must not reach prescriptions: %+v %v", again, err)
	}
	f.now = f.now.Add(f.cfg.ReverifyGrace)
	if _, err := f.m.RunReverification(ctx(), f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.PrescriberFacts(ctx(), doc); !errors.Is(err, public.ErrNotVerified) {
		t.Fatalf("suspended doctor: %v", err)
	}
}

// A lapsed professional starts a new round; the standing holds until a reviewer decides.
func TestS160_ReverificationRoundRestoresVerification(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	f.now = f.now.AddDate(0, 12, 0).Add(f.cfg.ReverifyGrace)
	if _, err := f.m.RunReverification(ctx(), f.now); err != nil {
		t.Fatal(err)
	}
	if f.status(t, doc) != public.StatusSuspended {
		t.Fatal("suspended")
	}
	// New round: draft, evidence, submit. Status stays suspended while in review.
	d, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao"))
	if err != nil || d.Round != 2 || d.RoundState != "draft" || len(d.Evidence) != 0 {
		t.Fatalf("round 2 draft: %v %+v", err, d)
	}
	for _, k := range []string{verification.EvidenceRegistrationCertificate, verification.EvidencePhotoID} {
		if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: k, Data: jpeg()}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := f.m.Submit(ctx(), doc)
	if err != nil || c.Status != public.StatusSuspended || c.RoundState != "checking" {
		t.Fatalf("%v %+v", err, c)
	}
	if f.isVerified(t, doc) {
		t.Fatal("not verified while the new round is in review")
	}
	q, _, _ := f.m.ListQueue(ctx(), ids.New(), 10, "")
	if len(q) != 1 || !q[0].Reverification || q[0].CaseID != c.ID {
		t.Fatalf("queue = %+v", q)
	}
	res, err := decide(f, c.ID, ids.New(), verification.OutcomeVerified, "checks_passed")
	if err != nil || !res.Final || res.Status != public.StatusVerified || !f.isVerified(t, doc) {
		t.Fatalf("%v %+v", err, res)
	}
	if got := f.statusEvents(t, doc); got[len(got)-1] != "verified" {
		t.Fatalf("events = %v", got)
	}
	v := f.reviewerView(t, c.ID)
	if v.ReVerifyDueAt == nil || !v.ReVerifyDueAt.Equal(f.now.AddDate(0, 12, 0)) {
		t.Fatalf("new due = %v", v.ReVerifyDueAt)
	}
}

func TestS160_FailedReverificationSuspendsAtOnce(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	if err := f.m.TriggerReverification(ctx(), ids.New(), doc, "complaint"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.SaveDraft(ctx(), doc, draft("DMC/1", "Asha Rao")); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{verification.EvidenceRegistrationCertificate, verification.EvidencePhotoID} {
		if _, err := f.m.UploadEvidence(ctx(), doc, verification.EvidenceInput{Kind: k, Data: jpeg()}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := f.m.Submit(ctx(), doc)
	if err != nil || c.Status != public.StatusReVerifyDue {
		t.Fatalf("%v %+v", err, c)
	}
	if res, err := decide(f, c.ID, ids.New(), verification.OutcomeRejected, "licence_suspended"); err != nil || res.Final {
		t.Fatalf("%v %+v", err, res)
	}
	res, err := decide(f, c.ID, ids.New(), verification.OutcomeRejected, "licence_suspended")
	if err != nil || !res.Final || res.Status != public.StatusSuspended || f.isVerified(t, doc) {
		t.Fatalf("%v %+v", err, res)
	}
}

// ---- S-109: audit and events ----

func TestS109_EveryTransitionIsAudited(t *testing.T) {
	f := setup(t)
	doc := ids.New()
	f.verified(t, doc, "DMC/1", "Asha Rao", nil)
	_ = f.m.Suspend(ctx(), ids.New(), doc, "licence_suspended")
	have := f.audits(t)
	for _, want := range []string{"verification.case_created", "verification.draft_saved", "verification.evidence_added", "verification.submitted",
		"verification.checks_recorded", "verification.decided", "verification.suspended"} {
		if !slices.Contains(have, want) {
			t.Fatalf("missing audit %s in %v", want, have)
		}
	}
	// Reviewer access is audited too.
	c, _ := f.m.GetMine(ctx(), doc)
	reviewer := ids.New()
	if _, err := f.m.OpenCase(ctx(), reviewer, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.ReadEvidence(ctx(), reviewer, c.ID, c.Evidence[0].ID); err != nil {
		t.Fatal(err)
	}
	have = f.audits(t)
	for _, want := range []string{"verification.case_opened", "verification.evidence_read"} {
		if !slices.Contains(have, want) {
			t.Fatalf("missing audit %s", want)
		}
	}
	// Events carry references and codes only.
	for _, e := range f.events(t) {
		for k, v := range e.Payload {
			if len(v) > 60 {
				t.Fatalf("payload %s too long", k)
			}
		}
	}
}

func TestS109_ConfigValidation(t *testing.T) {
	f := setup(t)
	deps := verification.Deps{Crypto: f.cr, Blob: f.store, Checker: f.chk, Scanner: f.scan, Consent: f.gate}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := f.env.Pools.For("verification")
	bad := f.cfg
	bad.IDTypes = nil
	if _, err := verification.New(pool, bad, deps, log); err == nil {
		t.Fatal("ID allow-list is required (country pack)")
	}
	bad = f.cfg
	bad.ProhibitedIDValidators = nil
	if _, err := verification.New(pool, bad, deps, log); err == nil {
		t.Fatal("prohibited identifier validators are required")
	}
	bad = f.cfg
	bad.RequiredEvidenceKinds = []string{"unknown_kind"}
	if _, err := verification.New(pool, bad, deps, log); err == nil {
		t.Fatal("required kinds must be allowed kinds")
	}
	if _, err := verification.New(pool, f.cfg, verification.Deps{}, log); err == nil {
		t.Fatal("dependencies are required")
	}
}
