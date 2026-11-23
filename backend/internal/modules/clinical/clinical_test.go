package clinical_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

var ts = time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)

const packDir = "../../../../config/country-packs/IN/"

type fx struct {
	env *testkit.Env
	m   *clinical.Module
	e   *rules.Engine
}

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), clinical.Source()}})
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
	return &fx{env: env, m: m, e: e}
}

var actor = clinical.Actor{Ref: "svc_drug_import", Role: "clinical_admin"}

func entry(name, brands, lists, exception, tags string) string {
	return "  - ref: " + strings.ReplaceAll(name, " ", "-") + "\n    generic_name: " + name + "\n    brand_names: " + brands +
		"\n    strength: 250 mg\n    form: tablet\n    route: oral\n    lists: " + lists +
		"\n    amendment_exception: " + exception + "\n    condition_tags: " + tags + "\n    source: Synthetic test source\n"
}

func file(version string, entries ...string) []byte {
	return []byte("version: " + version + "\nstatus: draft\nentries:\n" + strings.Join(entries, ""))
}

// All drug names here are synthetic except those the rule file itself names.
func goodFile(version string) []byte {
	return file(version,
		entry("paracetamol", "[Fakecalpol, Fakepyrin]", "[list_o]", "false", "[]"),
		entry("fakeamycin", "[Fakemycetin]", "[list_a]", "false", "[]"),
		entry("fakebloc", "[]", "[list_b]", "false", "[cond_x]"),
		entry("fakeprohibitol", "[]", "[prohibited]", "false", "[]"),
		entry("fakeunknol", "[]", "[]", "false", "[]"),
		entry("phenobarbitone", "[]", "[prohibited, list_a]", "true", "[]"),
		entry("clobazam", "[]", "[prohibited, list_a]", "true", "[]"),
		entry("clonazepam", "[]", "[prohibited, list_a]", "true", "[]"),
	)
}

func (f *fx) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestS126_ImportWritesImmutableVersionAndAudits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, err := f.m.ImportClassification(ctx, actor, goodFile("2026.12.1"))
	if err != nil || !res.Created || res.EntryCount != 8 || !ids.Valid(res.VersionID) {
		t.Fatalf("%+v %v", res, err)
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.drug_master_entry WHERE classification_id = $1`, res.VersionID); n != 8 {
		t.Fatalf("entries %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.classification.import'
		AND result = 'allowed' AND resource_type = 'drug_classification_version' AND resource_ref = $1 AND actor_ref = $2`, res.VersionID, actor.Ref); n != 1 {
		t.Fatalf("audit events %d", n)
	}

	// Identical bytes again: the same version, no second audit event.
	again, err := f.m.ImportClassification(ctx, actor, goodFile("2026.12.1"))
	if err != nil || again.Created || again.VersionID != res.VersionID {
		t.Fatalf("%+v %v", again, err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE result = 'allowed' AND action = 'clinical.classification.import'`); n != 1 {
		t.Fatalf("audit events %d", n)
	}
	// Same label, different content: refused (versions are immutable).
	changed := file("2026.12.1", entry("paracetamol", "[]", "[list_o]", "false", "[]"), entry("phenobarbitone", "[]", "[prohibited, list_a]", "true", "[]"),
		entry("clobazam", "[]", "[prohibited, list_a]", "true", "[]"), entry("clonazepam", "[]", "[prohibited, list_a]", "true", "[]"))
	if _, err := f.m.ImportClassification(ctx, actor, changed); !errors.Is(err, clinical.ErrVersionConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	// A new label is a new version beside the old one.
	next, err := f.m.ImportClassification(ctx, actor, goodFile("2026.12.2"))
	if err != nil || !next.Created || next.VersionID == res.VersionID {
		t.Fatalf("%+v %v", next, err)
	}

	// The module's own role can neither change nor delete a version.
	pool := f.env.Pools.For("clinical")
	for _, q := range []string{
		`UPDATE drug_master_entry SET in_list_o = true`,
		`DELETE FROM drug_master_entry`,
		`UPDATE drug_classification_version SET status = 'approved'`,
		`DELETE FROM drug_classification_version`,
		`DELETE FROM drug_search_key`,
	} {
		if _, err := pool.Exec(ctx, q); !db.IsInsufficientPrivilege(err) {
			t.Errorf("%s: want insufficient privilege, got %v", q, err)
		}
	}
}

func TestS126_PacksEmptyClassificationImports(t *testing.T) {
	f := setup(t)
	b, err := os.ReadFile(packDir + "drugs/classification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.m.ImportClassification(context.Background(), actor, b)
	if err != nil || !res.Created || res.EntryCount != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	cls, err := f.m.LoadClassification(context.Background(), res.Version)
	if err != nil || len(cls.Drugs) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestT30_ImportRejectedWritesNoVersion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bad := file("2026.12.9", entry("fakeamycin", "[]", "[prohibited, list_a]", "false", "[]"),
		entry("phenobarbitone", "[]", "[prohibited, list_a]", "true", "[]"), entry("clobazam", "[]", "[prohibited, list_a]", "true", "[]"),
		entry("clonazepam", "[]", "[prohibited, list_a]", "true", "[]"))
	_, err := f.m.ImportClassification(ctx, actor, bad)
	if !errors.Is(err, rules.ErrClassificationInvalid) || !strings.Contains(err.Error(), "without an amendment exception") {
		t.Fatalf("want rejection, got %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.drug_classification_version`); n != 0 {
		t.Fatalf("%d versions written", n)
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.drug_master_entry`); n != 0 {
		t.Fatalf("%d entries written", n)
	}
	// The rejection is audited as a failure.
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.classification.import' AND result = 'failed' AND reason_code = 'import_rejected'`); n != 1 {
		t.Fatalf("failed audit events %d", n)
	}
	// The database itself also refuses the contradiction.
	_, err = f.env.Owner.Exec(ctx, `INSERT INTO clinical.drug_classification_version (id, version, status, checksum, ruleset_version, entry_count, imported_by)
		VALUES ('00000000-0000-7000-8000-000000000001','v1','draft',repeat('0',64),'r',1,'svc')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.env.Owner.Exec(ctx, `INSERT INTO clinical.drug_master_entry (classification_id, drug_ref, generic_name, strength, form, route, in_prohibited, in_list_a, source_ref)
		VALUES ('00000000-0000-7000-8000-000000000001','fake-x','fakex','1 mg','tablet','oral',true,true,'synthetic')`)
	if err == nil || !strings.Contains(err.Error(), "drug_not_prohibited_and_permitted") {
		t.Fatalf("want check violation, got %v", err)
	}
}

func TestS126_ImportAuditFailureRollsBack(t *testing.T) {
	f := setup(t)
	// An actor reference that audit refuses: no version may be written without its audit event.
	_, err := f.m.ImportClassification(context.Background(), clinical.Actor{Ref: "bad actor!", Role: "clinical_admin"}, goodFile("2026.12.3"))
	if err == nil {
		t.Fatal("want an error")
	}
	if n := f.count(t, `SELECT count(*) FROM clinical.drug_classification_version`); n != 0 {
		t.Fatalf("%d versions written without an audit event", n)
	}
}

func TestS126_SearchByGenericOrBrandPrefix(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.m.ImportClassification(ctx, actor, goodFile("2026.12.1")); err != nil {
		t.Fatal(err)
	}
	refs := func(q string) []string {
		t.Helper()
		got, err := f.m.Search(ctx, "2026.12.1", q, 20)
		if err != nil {
			t.Fatal(err)
		}
		var r []string
		for _, s := range got {
			r = append(r, s.Ref)
		}
		return r
	}
	if got := refs("para"); len(got) != 1 || got[0] != "paracetamol" {
		t.Fatalf("generic prefix: %v", got)
	}
	if got := refs("FAKEPY"); len(got) != 1 || got[0] != "paracetamol" {
		t.Fatalf("brand prefix, case-insensitive: %v", got)
	}
	if got := refs("fake"); len(got) != 5 { // fakeamycin, fakebloc, fakeprohibitol, fakeunknol, and paracetamol through its brands
		t.Fatalf("shared prefix: %v", got)
	}
	got, _ := f.m.Search(ctx, "2026.12.1", "fakem", 20)
	if len(got) != 1 || got[0].GenericName != "fakeamycin" || got[0].BrandNames[0] != "Fakemycetin" || got[0].Strength != "250 mg" {
		t.Fatalf("brand hit carries identity only: %+v", got)
	}
	if got := refs("zzz"); len(got) != 0 {
		t.Fatalf("no match: %v", got)
	}
	if lim, _ := f.m.Search(ctx, "2026.12.1", "fake", 2); len(lim) != 2 {
		t.Fatalf("limit: %d", len(lim))
	}
	if got, err := f.m.Search(ctx, "no-such-version", "para", 5); err != nil || len(got) != 0 {
		t.Fatalf("unknown version: %v %v", got, err)
	}
	for _, bad := range []string{"", "a", "pa%", "pa_", `pa\`, "para'; DROP TABLE x;--", strings.Repeat("a", 41)} {
		if _, err := f.m.Search(ctx, "2026.12.1", bad, 5); !errors.Is(err, clinical.ErrBadSearch) {
			t.Errorf("%q: want ErrBadSearch, got %v", bad, err)
		}
	}
}

func TestS126_EngineReadsClassificationFromTheDatabase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.m.ImportClassification(ctx, actor, goodFile("2026.12.1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.LoadClassification(ctx, "nope"); !errors.Is(err, clinical.ErrUnknownVersion) {
		t.Fatalf("%v", err)
	}
	cls, err := f.m.LoadClassification(ctx, "2026.12.1")
	if err != nil {
		t.Fatal(err)
	}
	item := func(ref string) rules.Item {
		return rules.Item{Ref: "l1", DrugRef: ref, Context: rules.ContextNewDrug, Dose: rules.Dose{Amount: 1, Unit: "mg"},
			Frequency: rules.Frequency{Times: 1, Period: "day"}, Duration: rules.Duration{Value: 1, Unit: "day"}, Route: "oral"}
	}
	run := func(ref string) rules.Result {
		return f.e.Check(rules.Input{RuleSetVersion: f.e.Version(), Mode: rules.ModeVideo, ConsultAt: ts, PatientTimeZone: "UTC", PatientCountry: "IN",
			Initiator: "patient", PrescriberVerified: true, IdentityConfirmed: true, PatientAgeYears: 30, DiagnosisPresent: true,
			StepUpDone: true, ESignatureApplied: true, Classification: cls, Items: []rules.Item{item(ref)}})
	}
	if r := run("paracetamol"); !r.Allowed || r.DrugClassificationVersion != "2026.12.1" {
		t.Fatalf("%+v", r)
	}
	if r := run("fakeamycin"); !r.Allowed {
		t.Fatalf("list A first video: %+v", r)
	}
	for ref, code := range map[string]string{"fakeprohibitol": "RX_PROHIBITED", "fakeunknol": "RX_NOT_CLASSIFIED", "clonazepam": "RX_CONTROLLED_IN_PRODUCT", "fakebloc": "RX_NO_RULE_MATCHED"} {
		if r := run(ref); r.Allowed || !r.HasReason(code) {
			t.Errorf("%s: want %s, got %+v", ref, code, r.Items)
		}
	}
}

func TestT18_BlockIsAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cls := rules.NewMapClassifier("CLS-T18", rules.Drug{Ref: "x-fake", GenericName: "fakeprohibitol", Prohibited: true})
	res := f.e.Check(rules.Input{RuleSetVersion: f.e.Version(), Mode: rules.ModeVideo, ConsultAt: ts, PatientTimeZone: "UTC", PatientCountry: "IN",
		Initiator: "patient", PrescriberVerified: true, IdentityConfirmed: true, PatientAgeYears: 30, DiagnosisPresent: true,
		StepUpDone: true, ESignatureApplied: true, Classification: cls,
		Items: []rules.Item{{Ref: "l1", DrugRef: "x-fake", Context: rules.ContextNewDrug, Dose: rules.Dose{Amount: 1, Unit: "mg"},
			Frequency: rules.Frequency{Times: 1, Period: "day"}, Duration: rules.Duration{Value: 1, Unit: "day"}, Route: "oral"}}})
	if res.Allowed || res.Items[0].Reasons[0].Key == "" {
		t.Fatalf("%+v", res)
	}
	subject, rx := ids.New(), ids.New()
	err := db.InTx(ctx, f.env.Pools.For("clinical"), pgx.TxOptions{}, func(tx pgx.Tx) error {
		return f.m.RecordCheck(ctx, tx, clinical.Actor{Ref: "doc_1", Role: "doctor"}, subject, rx, res)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.rules.check' AND result = 'denied'
		AND reason_code = 'RX_PROHIBITED' AND resource_ref = $1 AND subject_ref = $2`, rx, subject); n != 1 {
		t.Fatalf("audit events %d", n)
	}
	// An allowed evaluation is audited as allowed, with no reason.
	err = db.InTx(ctx, f.env.Pools.For("clinical"), pgx.TxOptions{}, func(tx pgx.Tx) error {
		return f.m.RecordCheck(ctx, tx, clinical.Actor{Ref: "doc_1", Role: "doctor"}, subject, rx, rules.Result{Allowed: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action = 'clinical.rules.check' AND result = 'allowed' AND reason_code = ''`); n != 1 {
		t.Fatalf("allowed audit events %d", n)
	}
}
