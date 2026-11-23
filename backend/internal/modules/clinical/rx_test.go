package clinical_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	esignfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign/fake"
	schfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	vp "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

// ---- fakes of the consumer interfaces ----

var (
	_ np.ReminderContentResolver = (*clinical.Rx)(nil)
	_ clinical.T2Granter         = (*fakeGranter)(nil)
	_ vp.Reader                  = (*fakeVerifier)(nil)
	_ rp.Gate                    = (*fakeGate)(nil)
	_ np.Sender                  = (*fakeNotifier)(nil)
)

type fakeGate struct {
	mu      sync.Mutex
	deny    map[rp.DataKind]bool
	hide    bool
	consent map[string]bool
	reqs    []rp.Request
}

func (g *fakeGate) Authorize(_ context.Context, r rp.Request) (rp.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = append(g.reqs, r)
	if g.deny[r.Kind] || g.deny["*"] {
		return rp.Decision{Reason: "no_active_grant", Hide: g.hide}, nil
	}
	return rp.Decision{Allowed: true, Tier: rp.T1CurrentCare}, nil
}

func (g *fakeGate) HasConsent(_ context.Context, subject, purpose string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.consent[subject+"|"+purpose], nil
}

type fakeVerifier struct {
	mu  sync.Mutex
	bad map[string]bool
}

func (v *fakeVerifier) Status(context.Context, string) (vp.Status, error) {
	return vp.StatusVerified, nil
}
func (v *fakeVerifier) IsVerified(_ context.Context, ref string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.bad[ref], nil
}

type fakeGranter struct {
	mu    sync.Mutex
	calls [][3]string
}

func (g *fakeGranter) GrantT2(_ context.Context, p, d, c string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, [3]string{p, d, c})
	return ids.New(), nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	reqs []np.Request
}

func (n *fakeNotifier) Send(_ context.Context, r np.Request) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reqs = append(n.reqs, r)
	return ids.New(), nil
}

type fakeConsults struct {
	mu sync.Mutex
	m  map[string]clinical.ConsultFacts
}

func (c *fakeConsults) Consult(_ context.Context, appt string) (clinical.ConsultFacts, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.m[appt]
	if !ok {
		return clinical.ConsultFacts{}, clinical.ErrConsultNotFound
	}
	return f, nil
}

// ---- harness ----

type rxfx struct {
	*fx
	rx   *clinical.Rx
	gate *fakeGate
	ver  *fakeVerifier
	cons *fakeConsults
	gr   *fakeGranter
	sig  *esignfake.Signer
	sch  *schfake.Client
	not  *fakeNotifier
	cr   *crypto.Service

	pat, doc, appt, clinic string
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

const aadhaarNames = "verhoeff_12_digit_first_2_to_9"

func rxSetup(t *testing.T, esignOn bool) *rxfx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), clinical.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(packDir + "rules/telemedicine-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, err := rules.Load(b, rules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := clinical.New(env.Pools.For("clinical"), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportClassification(context.Background(), actor, goodFile("2026.12.1")); err != nil {
		t.Fatal(err)
	}
	f := &rxfx{fx: &fx{env: env, m: m, e: e}, gate: &fakeGate{deny: map[rp.DataKind]bool{}, consent: map[string]bool{}}, ver: &fakeVerifier{bad: map[string]bool{}},
		cons: &fakeConsults{m: map[string]clinical.ConsultFacts{}}, gr: &fakeGranter{}, sig: &esignfake.Signer{}, sch: &schfake.Client{}, not: &fakeNotifier{}, cr: cr,
		pat: ids.New(), doc: ids.New(), appt: ids.New(), clinic: ids.New()}
	cfg := clinical.DefaultConfig()
	cfg.ESignEnabled = esignOn
	cfg.ClassificationVersion = "2026.12.1"
	cfg.ProhibitedValidators = []string{aadhaarNames}
	cfg.VolumeAlertPerDay = 1 // TM-04: every test doctor crosses the threshold with their first signature
	f.cons.m[f.appt] = f.facts(rules.ModeVideo)
	rx, err := clinical.NewRx(m, cfg, clinical.Deps{Crypto: cr, Gate: f.gate, Granter: f.gr, Verifier: f.ver, Consults: f.cons, Signer: f.sig,
		Scheduler: f.sch, Notifier: f.not})
	if err != nil {
		t.Fatal(err)
	}
	f.rx = rx
	return f
}

func (f *rxfx) facts(mode rules.Mode) clinical.ConsultFacts {
	return clinical.ConsultFacts{AppointmentRef: f.appt, PatientRef: f.pat, DoctorRef: f.doc, ClinicRef: f.clinic, Mode: mode, ConsultAt: time.Now(),
		PatientTimeZone: "Europe/London", PatientCountry: "IN", Initiator: "patient", IdentityConfirmed: true, PatientAgeYears: 40,
		PatientName: "Test Patient", PatientSex: "f",
		Doctor: clinical.DoctorFacts{Name: "Dr Test", Qualification: "MBBS", RegistrationNumber: "REG-12345", Council: "Test Council"}}
}

func (f *rxfx) caller(stepUp bool) clinical.Caller {
	c := clinical.Caller{UserID: f.doc, Role: "doctor", ClinicRef: f.clinic, RequestID: ids.New()}
	if stepUp {
		now := time.Now()
		c.StepUpAt = &now
	}
	return c
}

func (f *rxfx) patientCaller() clinical.Caller {
	return clinical.Caller{UserID: f.pat, Role: "patient", RequestID: ids.New()}
}

func item(ref, drug string) clinical.ItemInput {
	return clinical.ItemInput{Ref: ref, DrugRef: drug, Context: "new_drug", Dose: clinical.DoseInput{Amount: 500, Unit: "mg"},
		Frequency: clinical.FrequencyInput{Times: 3, Period: "day"}, Duration: clinical.DurationInput{Value: 5, Unit: "day"}, Route: "oral"}
}

const noteText = "synthetic headache complaint"

// prepare saves a note with a diagnosis and a one-item draft.
func (f *rxfx) prepare(t *testing.T, items ...clinical.ItemInput) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Complaint: noteText, Findings: "synthetic findings", Diagnosis: "synthetic diagnosis", Advice: "rest"}); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		items = []clinical.ItemInput{item("l1", "paracetamol")}
	}
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: items}); err != nil {
		t.Fatal(err)
	}
}

func (f *rxfx) signed(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM clinical.prescription_version WHERE state = 'signed'`)
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// ---- tests ----

func TestTC36_StructuredDoseFreeTextOnlyInInstructions(t *testing.T) {
	f := rxSetup(t, true)
	ctx := context.Background()
	f.prepare(t)

	// Free text in a dose unit is refused: only structured tokens.
	bad := item("l1", "paracetamol")
	bad.Dose.Unit = "one tablet after food"
	var inv *clinical.InvalidError
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{bad}}); !errors.As(err, &inv) {
		t.Fatalf("free text unit: %v", err)
	}
	// An Aadhaar-like number is refused in any text field, including instructions and the note.
	var num string
	for i := 0; i < 1000000; i++ {
		c := "2345" + strings.Repeat("0", 8-len(itoa(i))) + itoa(i)
		if scrub.Verhoeff(c) {
			num = c
			break
		}
	}
	withNum := item("l1", "paracetamol")
	withNum.Instructions = "call " + num
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{withNum}}); !errors.As(err, &inv) || inv.Code != "prohibited_identifier" {
		t.Fatalf("id in instructions: %v", err)
	}
	if _, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Advice: num}); !errors.As(err, &inv) {
		t.Fatalf("id in note: %v", err)
	}
	// Instructions are stored exactly as typed.
	ok := item("l1", "paracetamol")
	ok.Instructions = "  after food,  twice  "
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{ok}}); err != nil {
		t.Fatal(err)
	}
	v, err := f.rx.GetPrescription(ctx, f.caller(false), f.appt)
	if err != nil || v.Content.Items[0].Instructions != "  after food,  twice  " || v.Content.Items[0].Dose.Amount != 500 {
		t.Fatalf("%+v %v", v, err)
	}

	// A missing dose is allowed in a draft but the check and signing block it (P07).
	missing := item("l1", "paracetamol")
	missing.Dose = clinical.DoseInput{}
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{missing}}); err != nil {
		t.Fatal(err)
	}
	cv, err := f.rx.Check(ctx, f.caller(false), f.appt)
	if err != nil || cv.Result.Allowed || !cv.Result.HasReason("RX_DOSE_INCOMPLETE") {
		t.Fatalf("%+v %v", cv.Result, err)
	}
	var be *clinical.BlockedError
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); !errors.As(err, &be) || !be.Result.HasReason("RX_DOSE_INCOMPLETE") {
		t.Fatalf("sign: %v", err)
	}
	if f.signed(t) != 0 || f.sig.Starts != 0 {
		t.Fatal("nothing may be issued or sent for signing")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestS150_SignIssuesImmutableEncryptedVersion(t *testing.T) {
	f := rxSetup(t, true)
	ctx := context.Background()
	f.prepare(t)

	cv, err := f.rx.Check(ctx, f.caller(false), f.appt)
	if err != nil || !cv.Result.Allowed || cv.Result.Stage != rules.StageDraft {
		t.Fatalf("check: %+v %v", cv.Result, err)
	}
	res, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil || res.Version != 1 || len(res.VerificationReference) != 64 {
		t.Fatalf("%+v %v", res, err)
	}
	// Only the 32-byte hash went to the signature provider.
	if len(f.sig.Hashes) != 1 || len(f.sig.Hashes[0]) != 32 || hex.EncodeToString(f.sig.Hashes[0]) != res.DocumentSHA256 {
		t.Fatal("hash not sent")
	}
	// T2 was granted; the gate was asked for T1 with the booking reference.
	if len(f.gr.calls) != 1 || f.gr.calls[0] != [3]string{f.pat, f.doc, f.clinic} {
		t.Fatalf("T2: %v", f.gr.calls)
	}
	var sawT1 bool
	for _, r := range f.gate.reqs {
		if r.Kind == rp.KindClinicalCurrent && r.BookingRef == f.appt && r.PatientRef == f.pat {
			sawT1 = true
		}
	}
	if !sawT1 {
		t.Fatal("gate not consulted with T1 current care")
	}
	// Stored facts.
	var state, rsv, cls, mode string
	var sum, vh []byte
	if err := f.env.Owner.QueryRow(ctx, `SELECT state, ruleset_version, classification_version, consult_mode, document_sha256, verification_hash FROM clinical.prescription_version`).
		Scan(&state, &rsv, &cls, &mode, &sum, &vh); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(res.VerificationReference))
	if state != "signed" || rsv != f.e.Version() || cls != "2026.12.1" || mode != "video" || hex.EncodeToString(sum) != res.DocumentSHA256 || hex.EncodeToString(vh) != hex.EncodeToString(want[:]) {
		t.Fatalf("%s %s %s %s", state, rsv, cls, mode)
	}
	// The note was signed with it.
	if n := f.count(t, `SELECT count(*) FROM clinical.note_version WHERE state = 'signed' AND signed_at IS NOT NULL`); n != 1 {
		t.Fatalf("note not signed")
	}
	// Care-key encryption: no plaintext drug, note text, patient name or token in the database.
	var dump string
	if err := f.env.Owner.QueryRow(ctx, `SELECT coalesce((SELECT string_agg(encode(content,'escape')||encode(coalesce(document,''::bytea),'escape')||encode(coalesce(rule_result,''::bytea),'escape')||encode(coalesce(signature,''::bytea),'escape'),'') FROM clinical.prescription_version),'')
		|| coalesce((SELECT string_agg(encode(content,'escape'),'') FROM clinical.note_version),'')`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"paracetamol", "headache", "Test Patient", "REG-12345", res.VerificationReference, "synthetic diagnosis"} {
		if strings.Contains(dump, s) {
			t.Fatalf("plaintext %q in the database", s)
		}
	}
	// Event and audit.
	if n := f.count(t, `SELECT count(*) FROM clinical.event_outbox WHERE type = 'prescription.signed' AND payload->>'version' = '1' AND subject_ref = $1`, f.pat); n != 1 {
		t.Fatalf("event %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.prescription.sign' AND result = 'allowed' AND actor_ref = $1`, f.doc); n != 1 {
		t.Fatalf("audit %d", n)
	}
	// Full rule result is stored (sealed) with the prescription.
	if n := f.count(t, `SELECT count(*) FROM clinical.prescription_version WHERE rule_result IS NOT NULL AND length(ruleset_checksum) = 64`); n != 1 {
		t.Fatal("rule result missing")
	}

	// Immutable: neither the module role nor the owner can change or delete a signed row.
	pool := f.env.Pools.For("clinical")
	for _, q := range []string{
		`UPDATE prescription_version SET consult_mode = 'audio'`,
		`UPDATE note_version SET has_diagnosis = false`,
	} {
		if _, err := pool.Exec(ctx, q); pgCode(err) != "23001" {
			t.Errorf("%s: want restrict violation, got %v", q, err)
		}
		if _, err := f.env.Owner.Exec(ctx, strings.Replace(q, "UPDATE ", "UPDATE clinical.", 1)); pgCode(err) != "23001" {
			t.Errorf("owner %s: %v", q, err)
		}
	}
	for _, q := range []string{`DELETE FROM prescription_version`, `DELETE FROM note_version`, `DELETE FROM prescription`, `DELETE FROM note`, `TRUNCATE prescription_version`} {
		if _, err := pool.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%s: want insufficient privilege, got %v", q, err)
		}
	}
	// A second signing of the same version is refused (one signed version per draft).
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); !errors.Is(err, clinical.ErrAlreadySigned) {
		t.Fatalf("second sign: %v", err)
	}
	if f.signed(t) != 1 {
		t.Fatal("two signed versions")
	}
	// The draft cannot be saved over a signed version without amending.
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{item("l1", "paracetamol")}}); !errors.Is(err, clinical.ErrAlreadySigned) {
		t.Fatalf("save over signed: %v", err)
	}
}

func TestS150_ConcurrentSignIssuesOnce(t *testing.T) {
	f := rxSetup(t, true)
	f.prepare(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.rx.Sign(context.Background(), f.caller(true), f.appt); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 || f.signed(t) != 1 {
		t.Fatalf("successes %d signed rows %d", ok, f.signed(t))
	}
}

func TestTC44_UnverifiedDoctorCannotSign(t *testing.T) {
	f := rxSetup(t, true)
	f.prepare(t)
	f.ver.bad[f.doc] = true // licence suspended
	_, err := f.rx.Sign(context.Background(), f.caller(true), f.appt)
	if !errors.Is(err, clinical.ErrDoctorNotVerified) {
		t.Fatalf("got %v", err)
	}
	if f.signed(t) != 0 || f.sig.Starts != 0 || len(f.gr.calls) != 0 {
		t.Fatal("nothing may be issued")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.prescription.sign' AND result = 'denied' AND reason_code = 'RX_DOCTOR_NOT_VERIFIED' AND actor_ref = $1`, f.doc); n != 1 {
		t.Fatalf("audit %d", n)
	}
}

// The prescription format needs the doctor's reviewed registration details and the
// patient's name and age: signing refuses (and audits) when any is missing, and nothing is
// issued.
func TestSigningRefusesMissingPrescriberOrPatientDetails(t *testing.T) {
	for name, mut := range map[string]func(*clinical.ConsultFacts){
		"no registration snapshot": func(c *clinical.ConsultFacts) { c.Doctor = clinical.DoctorFacts{} },
		"no council":               func(c *clinical.ConsultFacts) { c.Doctor.Council = "" },
		"no patient name":          func(c *clinical.ConsultFacts) { c.PatientName = "" },
		"no patient age":           func(c *clinical.ConsultFacts) { c.PatientAgeYears = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			f := rxSetup(t, true)
			f.prepare(t)
			c := f.facts(rules.ModeVideo)
			mut(&c)
			f.cons.mu.Lock()
			f.cons.m[f.appt] = c
			f.cons.mu.Unlock()
			_, err := f.rx.Sign(context.Background(), f.caller(true), f.appt)
			if !errors.Is(err, clinical.ErrDoctorNotVerified) && !errors.Is(err, clinical.ErrPatientDetails) {
				t.Fatalf("got %v", err)
			}
			if f.signed(t) != 0 || f.sig.Starts != 0 || len(f.gr.calls) != 0 {
				t.Fatal("nothing may be issued")
			}
			if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.prescription.sign' AND result = 'denied' AND actor_ref = $1`, f.doc); n != 1 {
				t.Fatalf("audit %d", n)
			}
		})
	}
}

func TestTC56_SigningNeedsStepUpAndESignature(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.prepare(t)
	// No step-up, and a stale step-up: refused.
	if _, err := f.rx.Sign(ctx, f.caller(false), f.appt); !errors.Is(err, clinical.ErrStepUp) {
		t.Fatalf("no step-up: %v", err)
	}
	old := time.Now().Add(-6 * time.Minute)
	c := f.caller(false)
	c.StepUpAt = &old
	if _, err := f.rx.Sign(ctx, c, f.appt); !errors.Is(err, clinical.ErrStepUp) {
		t.Fatalf("stale step-up: %v", err)
	}
	// The provider times out: unsigned and not issued.
	f.sig.FailComplete = adapters.E(adapters.Timeout, "T")
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); !errors.Is(err, clinical.ErrESignFailed) {
		t.Fatalf("timeout: %v", err)
	}
	f.sig.FailComplete = nil
	if f.signed(t) != 0 || f.count(t, `SELECT count(*) FROM clinical.event_outbox`) != 0 || len(f.gr.calls) != 0 {
		t.Fatal("nothing may be issued after a failed signature")
	}

	// Production configuration: no e-signature method (N5). Everything else is fine, nothing is issued.
	g := rxSetup(t, false)
	g.prepare(t)
	if g.rx.ESignEnabled() {
		t.Fatal("ESignEnabled must default to false in this setup")
	}
	if _, err := g.rx.Sign(ctx, g.caller(true), g.appt); !errors.Is(err, clinical.ErrESignUnavailable) {
		t.Fatalf("esign off: %v", err)
	}
	if g.signed(t) != 0 || g.sig.Starts != 0 || g.count(t, `SELECT count(*) FROM clinical.event_outbox`) != 0 {
		t.Fatal("nothing may be issued without an e-signature method")
	}
	if _, err := clinical.NewRx(g.m, clinical.Config{ESignEnabled: true, ClassificationVersion: "x", AppRef: "core"}, clinical.Deps{Crypto: g.cr, Gate: g.gate, Granter: g.gr,
		Verifier: g.ver, Consults: g.cons, Scheduler: g.sch}); err == nil {
		t.Fatal("ESignEnabled without a signer must be refused")
	}
}

func TestTC59_BlockedDrugIsRefusedAtSigning(t *testing.T) {
	f := rxSetup(t, true)
	ctx := context.Background()
	// The client has no way to mark an item allowed: the server decides from the drug master.
	f.prepare(t, item("l1", "paracetamol"), item("l2", "fakeprohibitol"), item("l3", "fakeunknol"))
	var be *clinical.BlockedError
	_, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if !errors.As(err, &be) || !be.Result.HasReason("RX_PROHIBITED") || !be.Result.HasReason("RX_NOT_CLASSIFIED") {
		t.Fatalf("%v", err)
	}
	for _, it := range be.Result.Items {
		if it.Ref == "l1" && !it.Allowed {
			t.Fatal("the allowed item must stay allowed")
		}
	}
	if f.signed(t) != 0 || f.sig.Starts != 0 {
		t.Fatal("a blocked prescription must not reach the signature provider")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.rules.check' AND result = 'denied'`); n < 1 {
		t.Fatal("blocked evaluation not audited")
	}
	// Remote consult from outside the declared country fails P10 too.
	f.prepare(t)
	c := f.facts(rules.ModeVideo)
	c.PatientCountry = ""
	f.cons.m[f.appt] = c
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); !errors.As(err, &be) || !be.Result.HasReason("RX_PATIENT_OUTSIDE_INDIA") {
		t.Fatalf("p10: %v", err)
	}
}

func TestS150_AccessIsDeniedByTheGateAndOthersGet404(t *testing.T) {
	f := rxSetup(t, true)
	ctx := context.Background()
	f.prepare(t)
	f.gate.deny["*"] = true
	var de *clinical.DeniedError
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); !errors.As(err, &de) || de.Hide {
		t.Fatalf("%v", err)
	}
	f.gate.hide = true
	if _, err := f.rx.GetPrescription(ctx, f.caller(false), f.appt); !errors.As(err, &de) || !de.Hide {
		t.Fatalf("%v", err)
	}
	f.gate.deny = map[rp.DataKind]bool{}
	// Another doctor, an unknown appointment and a patient caller all see nothing.
	other := f.caller(true)
	other.UserID = ids.New()
	if _, err := f.rx.Sign(ctx, other, f.appt); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("other doctor: %v", err)
	}
	if _, err := f.rx.GetNote(ctx, f.caller(false), ids.New()); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := f.rx.SaveNote(ctx, f.patientCaller(), f.appt, clinical.NoteContent{}); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("patient as doctor: %v", err)
	}
	if f.signed(t) != 0 {
		t.Fatal("nothing may be issued")
	}
}

func TestTC60_VerificationPage(t *testing.T) {
	f := rxSetup(t, true)
	ctx := context.Background()
	f.prepare(t)
	res, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	v := f.rx.VerifyReference(ctx, res.VerificationReference)
	if !v.Valid || v.RegistrationNumber != "REG-12345" || v.SignedAt.IsZero() {
		t.Fatalf("%+v", v)
	}
	// Exact document: valid. One changed byte: not valid.
	pv, err := f.rx.GetPrescription(ctx, f.caller(false), f.appt)
	if err != nil || len(pv.Document) == 0 {
		t.Fatalf("%v", err)
	}
	if !f.rx.VerifyDocument(ctx, res.VerificationReference, pv.Document).Valid {
		t.Fatal("the exact document must verify")
	}
	changed := append([]byte(nil), pv.Document...)
	changed[len(changed)/2] ^= 1
	if f.rx.VerifyDocument(ctx, res.VerificationReference, changed).Valid {
		t.Fatal("one changed byte must not verify")
	}
	// Unknown, malformed and wrongly-cased references reveal nothing and look the same.
	for _, ref := range []string{strings.Repeat("a", 64), "", "short", strings.ToUpper(res.VerificationReference) + "x", res.VerificationReference[:63]} {
		if got := f.rx.VerifyReference(ctx, ref); got != (clinical.VerifyResult{}) {
			t.Fatalf("%q: %+v", ref, got)
		}
	}
	// A tampered stored document (trigger bypassed by the owner, as a database attacker would) is not valid.
	if _, err := f.env.Owner.Exec(ctx, `ALTER TABLE clinical.prescription_version DISABLE TRIGGER prescription_version_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Owner.Exec(ctx, `UPDATE clinical.prescription_version SET document = set_byte(document, 40, get_byte(document, 40) # 1)`); err != nil {
		t.Fatal(err)
	}
	if f.rx.VerifyReference(ctx, res.VerificationReference).Valid {
		t.Fatal("tampered document must not verify")
	}
	// A revoked signature is not valid either.
	g := rxSetup(t, true)
	g.prepare(t)
	r2, _ := g.rx.Sign(ctx, g.caller(true), g.appt)
	h, _ := hex.DecodeString(r2.DocumentSHA256)
	g.sig.Revoke(h)
	if g.rx.VerifyReference(ctx, r2.VerificationReference).Valid {
		t.Fatal("revoked must not verify")
	}
	// Rate limit per IP.
	allowed := 0
	for i := 0; i < 100; i++ {
		if ok, _ := f.rx.AllowVerify("192.0.2.9"); ok {
			allowed++
		}
	}
	if allowed < 1 || allowed > 20 {
		t.Fatalf("allowed %d", allowed)
	}
	if ok, _ := f.rx.AllowVerify("192.0.2.10"); !ok {
		t.Fatal("another IP has its own bucket")
	}
}

func signedEvent(rxRef string, version string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: "prescription.signed", Payload: map[string]string{"prescriptionRef": rxRef, "version": version}}
}

// S-155: with P-REMIND every item's reminders are on by default; the patient turns one off (its
// activities are cancelled), the choice survives an amendment, and turning it on again schedules
// new activities. Turning on needs P-REMIND; someone else's prescription looks missing.
func TestS155_PerItemReminderSwitch(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.gate.consent[f.pat+"|P-REMIND"] = true
	f.prepare(t, item("l1", "paracetamol"), item("l2", "paracetamol"))
	res, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rx.HandleSigned(ctx, signedEvent(res.PrescriptionRef, "1")); err != nil {
		t.Fatal(err)
	}
	pc := f.patientCaller()
	states, err := f.rx.ItemReminders(ctx, pc, res.PrescriptionRef)
	if err != nil || len(states) != 2 || !states[0].Enabled || !states[0].Active || !states[1].Active {
		t.Fatalf("on by default: %+v %v", states, err)
	}
	if len(f.sch.Active()) != 2 {
		t.Fatalf("two active activities: %d", len(f.sch.Active()))
	}

	// Off: l1's activity is cancelled; l2 keeps reminding. Repeating is harmless.
	for i := 0; i < 2; i++ {
		if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l1", false); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sch.Active()) != 1 {
		t.Fatalf("l1 off: %d active", len(f.sch.Active()))
	}
	if st, _ := f.rx.ItemReminders(ctx, pc, res.PrescriptionRef); st[0].Enabled || st[0].Active || !st[1].Active {
		t.Fatalf("state after off: %+v", st)
	}

	// On again needs P-REMIND now.
	delete(f.gate.consent, f.pat+"|P-REMIND")
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l1", true); !errors.Is(err, clinical.ErrReminderConsent) {
		t.Fatalf("on without P-REMIND: %v", err)
	}
	f.gate.consent[f.pat+"|P-REMIND"] = true
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l1", true); err != nil {
		t.Fatal(err)
	}
	if len(f.sch.Active()) != 2 || len(f.sch.Items) != 3 {
		t.Fatalf("on again creates a new activity: active %d, created %d", len(f.sch.Active()), len(f.sch.Items))
	}

	// Review F2: if scheduling fails when turning on, the switch stays off and a retry works.
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l2", false); err != nil {
		t.Fatal(err)
	}
	f.sch.FailCreate = errors.New("scheduler down")
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l2", true); err == nil {
		t.Fatal("a scheduler failure must be reported")
	}
	if st, _ := f.rx.ItemReminders(ctx, pc, res.PrescriptionRef); st[1].Enabled {
		t.Fatalf("the switch must stay off after a failed turn-on: %+v", st)
	}
	f.sch.FailCreate = nil
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "l2", true); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.rx.ItemReminders(ctx, pc, res.PrescriptionRef); !st[1].Enabled || !st[1].Active {
		t.Fatalf("retry turns it on with reminders: %+v", st)
	}

	// Ownership and input checks.
	other := clinical.Caller{UserID: ids.New(), Role: "patient", RequestID: ids.New()}
	if err := f.rx.SetItemReminders(ctx, other, res.PrescriptionRef, "l1", false); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("someone else's prescription: %v", err)
	}
	if err := f.rx.SetItemReminders(ctx, pc, res.PrescriptionRef, "no-such-item", false); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("unknown item: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.reminder_item_set' AND actor_ref = $1`, f.pat); n != 4 {
		t.Fatalf("each change audited once (l1 off and on, l2 off and on): %d", n)
	}
	// S-165: each change (the signing follow-up, then l1 off, l1 on, l2 off, l2 on) tells the devices to refresh their
	// on-device schedule once; the repeated "off" and the failed "on" change nothing and say nothing.
	if n := f.count(t, `SELECT count(*) FROM clinical.event_outbox WHERE type = 'reminders.changed' AND subject_ref = $1`, f.pat); n != 5 {
		t.Fatalf("reminders.changed events: %d", n)
	}
}

// Review F1 (K-DP-09): a test's name is the doctor's free text and can disclose a diagnosis, so
// a test reminder is always neutral, even when medicine names are allowed (P-MEDNAMES).
func TestReviewF1TestRemindersStayNeutral(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.gate.consent[f.pat+"|P-REMIND"] = true
	if _, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Complaint: noteText, Findings: "synthetic findings",
		Diagnosis: "synthetic diagnosis", Advice: "rest"}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{item("l1", "paracetamol")},
		Tests: []clinical.TestInput{{Ref: "t1", Name: "Synthetic condition-revealing test", DueOn: due}}}); err != nil {
		t.Fatal(err)
	}
	res, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rx.HandleSigned(ctx, signedEvent(res.PrescriptionRef, "1")); err != nil {
		t.Fatal(err)
	}
	var testExt string
	for _, a := range f.sch.Items {
		if a.TypeKey == "test.due" {
			testExt = a.ExternalRef
		}
	}
	if testExt == "" {
		t.Fatal("a test reminder must be scheduled")
	}
	neutral, named, err := f.rx.Resolve(ctx, ids.New(), testExt, "test.due", f.pat, true)
	if err != nil || named != "" || strings.Contains(neutral, "Synthetic") {
		t.Fatalf("test reminders never carry the test name: %q %q %v", neutral, named, err)
	}
}

func TestS152_RemindersOnlyWithConsentAndNoDrugNames(t *testing.T) {
	ctx := context.Background()

	// P-REMIND absent: no scheduler activities, outcome says "offer once".
	f := rxSetup(t, true)
	asNeeded := item("l2", "paracetamol")
	asNeeded.AsNeeded = true
	f.prepare(t, item("l1", "paracetamol"), asNeeded)
	res, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rx.HandleSigned(ctx, signedEvent(res.PrescriptionRef, "1")); err != nil {
		t.Fatal(err)
	}
	if len(f.sch.Items) != 0 {
		t.Fatalf("activities without consent: %d", len(f.sch.Items))
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.reminder_run WHERE outcome = 'consent_missing'`); n != 1 {
		t.Fatal("outcome not recorded")
	}
	if len(f.gr.calls) < 2 {
		t.Fatal("the handler must repeat the idempotent T2 grant")
	}

	// With P-REMIND: one activity for the regular item, none for the as-needed one (E-RX-04).
	g := rxSetup(t, true)
	g.gate.consent[g.pat+"|P-REMIND"] = true
	g.prepare(t, item("l1", "paracetamol"), asNeeded)
	r2, err := g.rx.Sign(ctx, g.caller(true), g.appt)
	if err != nil {
		t.Fatal(err)
	}
	ev := signedEvent(r2.PrescriptionRef, "1")
	if err := g.rx.HandleSigned(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := g.rx.HandleSigned(ctx, ev); err != nil { // redelivery creates nothing new
		t.Fatal(err)
	}
	if len(g.sch.Items) != 1 || g.count(t, `SELECT count(*) FROM clinical.reminder_link`) != 1 {
		t.Fatalf("activities %d", len(g.sch.Items))
	}
	a := g.sch.Items[0]
	if a.TypeKey != "med.dose" || !strings.HasPrefix(a.RRule, "FREQ=DAILY;BYHOUR=8,14,20") || a.TimeZone != "Europe/London" || a.SubjectRef != g.pat || a.EndsAt == nil || !ids.Valid(a.ExternalRef) {
		t.Fatalf("%+v", a)
	}
	// Nothing in the payload names a drug, the doctor's text or the patient (K-ARC-02, TC-61).
	blob := strings.ToLower(a.AppRef + a.ExternalRef + string(a.TypeKey) + a.RRule + a.TimeZone + a.SubjectRef)
	for _, s := range []string{"paracetamol", "fakecalpol", "headache", "mg", "test patient"} {
		if strings.Contains(blob, s) {
			t.Fatalf("scheduler payload contains %q", s)
		}
	}

	// Resolver: neutral text always, named text only when allowed, never for someone else.
	neutral, named, err := g.rx.Resolve(ctx, ids.New(), a.ExternalRef, "med.dose", g.pat, false)
	if err != nil || named != "" || neutral == "" || strings.Contains(strings.ToLower(neutral), "paracetamol") {
		t.Fatalf("%q %q %v", neutral, named, err)
	}
	neutral, named, err = g.rx.Resolve(ctx, ids.New(), a.ExternalRef, "med.dose", g.pat, true)
	if err != nil || !strings.Contains(named, "paracetamol") || strings.Contains(neutral, "paracetamol") {
		t.Fatalf("%q %q %v", neutral, named, err)
	}
	if _, _, err := g.rx.Resolve(ctx, ids.New(), a.ExternalRef, "med.dose", ids.New(), true); err == nil {
		t.Fatal("another recipient must not resolve")
	}
	if _, _, err := g.rx.Resolve(ctx, ids.New(), ids.New(), "med.dose", g.pat, true); err == nil {
		t.Fatal("unknown reference must fail")
	}
}

func TestTC37_AmendCreatesNewVersionAndRefreshesReminders(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.gate.consent[f.pat+"|P-REMIND"] = true
	f.prepare(t)
	r1, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rx.HandleSigned(ctx, signedEvent(r1.PrescriptionRef, "1")); err != nil {
		t.Fatal(err)
	}
	if len(f.sch.Active()) != 1 {
		t.Fatal("reminder expected")
	}

	// Amend: a draft copy of v1; v1 is untouched.
	d, err := f.rx.Amend(ctx, f.caller(false), r1.PrescriptionRef)
	if err != nil || d.Version != 2 || d.State != "draft" || d.Content.Items[0].Dose.Amount != 500 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := f.rx.Amend(ctx, f.caller(false), r1.PrescriptionRef); !errors.Is(err, clinical.ErrOpenDraft) {
		t.Fatalf("second amend: %v", err)
	}
	changed := item("l1", "paracetamol")
	changed.Dose.Amount = 250
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{changed}}); err != nil {
		t.Fatal(err)
	}
	r2, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil || r2.Version != 2 || r2.PrescriptionRef != r1.PrescriptionRef || r2.VerificationReference == r1.VerificationReference {
		t.Fatalf("%+v %v", r2, err)
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.event_outbox WHERE type = 'prescription.amended' AND payload->>'version' = '2'`); n != 1 {
		t.Fatal("amended event missing")
	}
	am := outbox.Event{ID: ids.New(), Type: "prescription.amended", Payload: map[string]string{"prescriptionRef": r2.PrescriptionRef, "version": "2"}}
	if err := f.rx.HandleAmended(ctx, am); err != nil {
		t.Fatal(err)
	}
	if len(f.sch.Cancel) != 1 || len(f.sch.Active()) != 1 {
		t.Fatalf("cancelled %d active %d", len(f.sch.Cancel), len(f.sch.Active()))
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.reminder_link WHERE state = 'cancelled'`); n != 1 {
		t.Fatal("old link not cancelled")
	}
	if len(f.not.reqs) != 2 || f.not.reqs[1].TemplateKey != "prescription.amended" || f.not.reqs[1].RecipientRef != f.pat || f.not.reqs[1].Category != np.Service {
		t.Fatalf("patient not notified: %+v", f.not.reqs)
	}
	// Both versions stay readable by the patient, the old one unchanged.
	old, err := f.rx.PatientPrescription(ctx, f.patientCaller(), r1.PrescriptionRef, 1)
	if err != nil || old.Version != 1 || old.Content.Items[0].Dose.Amount != 500 || old.State != "signed" {
		t.Fatalf("%+v %v", old, err)
	}
	latest, err := f.rx.PatientPrescription(ctx, f.patientCaller(), r1.PrescriptionRef, 0)
	if err != nil || latest.Version != 2 || latest.Content.Items[0].Dose.Amount != 250 {
		t.Fatalf("%+v %v", latest, err)
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.prescription_version WHERE state = 'signed'`); n != 2 {
		t.Fatalf("signed versions %d", n)
	}
}

func TestS151_PatientReadsOwnRecordsOnly(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.prepare(t)
	// A draft is invisible to the patient.
	if _, err := f.rx.PatientNote(ctx, f.patientCaller(), f.appt); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("draft note: %v", err)
	}
	r, err := f.rx.Sign(ctx, f.caller(true), f.appt)
	if err != nil {
		t.Fatal(err)
	}
	list, next, err := f.rx.PatientPrescriptions(ctx, f.patientCaller(), 20, "")
	if err != nil || len(list) != 1 || next != "" || list[0].PrescriptionRef != r.PrescriptionRef || list[0].Version != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	v, err := f.rx.PatientPrescription(ctx, f.patientCaller(), r.PrescriptionRef, 0)
	if err != nil || v.Content.Items[0].DrugRef != "paracetamol" || len(v.Document) == 0 {
		t.Fatalf("%+v %v", v, err)
	}
	n, err := f.rx.PatientNote(ctx, f.patientCaller(), f.appt)
	if err != nil || n.Content.Complaint != noteText || n.State != "signed" {
		t.Fatalf("%+v %v", n, err)
	}
	// Another patient sees nothing, and nothing in the list.
	other := clinical.Caller{UserID: ids.New(), Role: "patient"}
	if _, err := f.rx.PatientPrescription(ctx, other, r.PrescriptionRef, 0); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if l, _, _ := f.rx.PatientPrescriptions(ctx, other, 20, ""); len(l) != 0 {
		t.Fatal("list leaks")
	}
	if _, err := f.rx.PatientNote(ctx, other, f.appt); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// A restricted device is refused by the gate (E-DP-22).
	f.gate.deny["*"] = true
	if _, _, err := f.rx.PatientPrescriptions(ctx, f.patientCaller(), 20, ""); err == nil {
		t.Fatal("gate denial must stop the read")
	}
	// A doctor role cannot use the patient routes.
	if _, _, err := f.rx.PatientPrescriptions(ctx, f.caller(false), 20, ""); !errors.Is(err, clinical.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestS139_NoteVersions(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	n1, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Complaint: "a", Diagnosis: "d"})
	if err != nil || n1.Version != 1 || n1.State != "draft" {
		t.Fatalf("%+v %v", n1, err)
	}
	n1b, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Complaint: "b", Diagnosis: "d"})
	if err != nil || n1b.Version != 1 || n1b.NoteRef != n1.NoteRef {
		t.Fatalf("%+v %v", n1b, err)
	}
	got, err := f.rx.GetNote(ctx, f.caller(false), f.appt)
	if err != nil || got.Content.Complaint != "b" {
		t.Fatalf("%+v %v", got, err)
	}
	if f.count(t, `SELECT count(*) FROM clinical.note_version`) != 1 {
		t.Fatal("a draft is replaced, not duplicated")
	}
	if _, err := f.rx.SaveDraft(ctx, f.caller(false), f.appt, clinical.DraftContent{Items: []clinical.ItemInput{item("l1", "paracetamol")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); err != nil {
		t.Fatal(err)
	}
	// After signing, a change is a new version; the signed one stays.
	n2, err := f.rx.SaveNote(ctx, f.caller(false), f.appt, clinical.NoteContent{Complaint: "c", Diagnosis: "d"})
	if err != nil || n2.Version != 2 {
		t.Fatalf("%+v %v", n2, err)
	}
	if f.count(t, `SELECT count(*) FROM clinical.note_version WHERE state = 'signed' AND version = 1`) != 1 {
		t.Fatal("signed note version changed")
	}
	// Without a diagnosis the rules block signing (P05).
	g := rxSetup(t, true)
	if _, err := g.rx.SaveNote(ctx, g.caller(false), g.appt, clinical.NoteContent{Complaint: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.rx.SaveDraft(ctx, g.caller(false), g.appt, clinical.DraftContent{Items: []clinical.ItemInput{item("l1", "paracetamol")}}); err != nil {
		t.Fatal(err)
	}
	var be *clinical.BlockedError
	if _, err := g.rx.Sign(ctx, g.caller(true), g.appt); !errors.As(err, &be) || !be.Result.HasReason("RX_DIAGNOSIS_MISSING") {
		t.Fatalf("%v", err)
	}
}

// TM-04: a doctor reaching the daily prescription threshold raises one alert (audit event, references only); signing
// itself is never blocked by it.
func TestTM04PrescriptionVolumeAlert(t *testing.T) {
	ctx := context.Background()
	f := rxSetup(t, true)
	f.prepare(t, item("l1", "paracetamol"))
	if _, err := f.rx.Sign(ctx, f.caller(true), f.appt); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='clinical.alert_prescription_volume' AND subject_ref=$1`, f.doc); n != 1 {
		t.Fatalf("one alert at the threshold: %d", n)
	}
}
