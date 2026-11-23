package rules_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
)

const rulesPath = "../../../../../config/country-packs/IN/rules/telemedicine-rules.yaml"

func loadFile(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func engine(t testing.TB) *rules.Engine {
	t.Helper()
	e, err := rules.Load(loadFile(t), rules.Options{})
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	return e
}

// Synthetic drug table. Names are fake except those the spec itself names
// (paracetamol, clonazepam).
var cls = rules.NewMapClassifier("CLS-TEST-1",
	rules.Drug{Ref: "x-fake", GenericName: "fakeprohibitol", Prohibited: true},
	rules.Drug{Ref: "ndps-fake", GenericName: "fakenarcotin", Prohibited: true},
	rules.Drug{Ref: "para", GenericName: "paracetamol", ListO: true},
	rules.Drug{Ref: "a-fake", GenericName: "fakeamycin", ListA: true},
	rules.Drug{Ref: "b-fake", GenericName: "fakebloc", ListB: true},
	rules.Drug{Ref: "ab-fake", GenericName: "fakeabol", ListA: true, ListB: true},
	rules.Drug{Ref: "none-fake", GenericName: "fakeunknol"},
	rules.Drug{Ref: "clonaz", GenericName: "Clonazepam", Prohibited: true, ListA: true, AmendmentException: true},
	rules.Drug{Ref: "contra-fake", GenericName: "fakecontra", Prohibited: true, ListA: true},                         // contradictory data
	rules.Drug{Ref: "claim-fake", GenericName: "fakeclaim", Prohibited: true, ListA: true, AmendmentException: true}, // exception the rules do not name
)

var consultAt = time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)

func item(ref, drug string, ctx rules.ItemContext) rules.Item {
	return rules.Item{Ref: ref, DrugRef: drug, Context: ctx,
		Dose: rules.Dose{Amount: 250, Unit: "mg"}, Frequency: rules.Frequency{Times: 2, Period: "day"},
		Duration: rules.Duration{Value: 5, Unit: "day"}, Route: "oral", Instructions: "after food"}
}

// good returns an input that is allowed: video, first consult, one List O item, everything verified.
func good(e *rules.Engine) rules.Input {
	return rules.Input{
		RuleSetVersion: e.Version(), Stage: rules.StageSign,
		Mode: rules.ModeVideo, ConsultAt: consultAt, PatientTimeZone: "UTC",
		PatientCountry: "IN", Initiator: "patient",
		PrescriberVerified: true, IdentityConfirmed: true, PatientAgeYears: 41, DiagnosisPresent: true,
		StepUpDone: true, ESignatureApplied: true,
		Classification: cls,
		Items:          []rules.Item{item("l1", "para", rules.ContextNewDrug)},
	}
}

func followUpFrom(last time.Time) rules.FollowUp {
	return rules.FollowUp{Claimed: true, SameDoctor: true, LastInPersonAt: last, Evidence: "platform_in_person_consult_record",
		SameConditionAttested: true, NoNewSymptomsAttested: true, RecallsContextAttested: true}
}

func allowed(t *testing.T, r rules.Result) {
	t.Helper()
	if !r.Allowed {
		t.Fatalf("want allow, got block: reasons=%v items=%+v", r.Reasons, r.Items)
	}
}

// blocked asserts a block whose item (or prescription) reasons include code and, when primary is set, lead with it.
func blocked(t *testing.T, r rules.Result, code string) {
	t.Helper()
	if r.Allowed {
		t.Fatalf("want block %s, got allow", code)
	}
	if !r.HasReason(code) {
		t.Fatalf("want reason %s, got reasons=%v items=%+v", code, r.Reasons, r.Items)
	}
}

func primary(t *testing.T, r rules.Result, code string) {
	t.Helper()
	blocked(t, r, code)
	if len(r.Items) == 0 || len(r.Items[0].Reasons) == 0 || r.Items[0].Reasons[0].Code != code {
		t.Fatalf("want primary item reason %s, got %+v", code, r.Items)
	}
}

func hasHit(it rules.ItemResult, layer rules.Layer, id string) bool {
	for _, h := range it.Hits {
		if h.Layer == layer && h.RuleID == id {
			return true
		}
	}
	return false
}

// ---- loading (TC-59, fail closed) ----

func TestLoad_RealDraftFile(t *testing.T) {
	e := engine(t)
	if e.Version() != "1.1.0-draft" || e.Status() != "draft" || len(e.Checksum()) != 64 {
		t.Fatalf("version=%s status=%s sum=%s", e.Version(), e.Status(), e.Checksum())
	}
}

func TestTC59_LoadRefusals(t *testing.T) {
	data := loadFile(t)
	if _, err := rules.Load(data, rules.Options{RequireApproved: true}); err == nil {
		t.Fatal("a draft rule file must not load where approval is required")
	}
	e := engine(t)
	if _, err := rules.Load(data, rules.Options{ExpectedSHA256: e.Checksum()}); err != nil {
		t.Fatalf("matching checksum must load: %v", err)
	}
	if _, err := rules.Load(data, rules.Options{ExpectedSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("bad checksum must be refused")
	}
	s := string(data)
	cases := map[string]string{
		"unknown top-level key":   s + "\nsurprise: true\n",
		"unknown key in a row":    strings.Replace(s, "{ id: R01, category: prohibited,", "{ id: R01, bonus: 1, category: prohibited,", 1),
		"missing regulatory":      cut(s, "regulatory_rules:", "# ---------- Platform rules"),
		"missing platform":        cut(s, "platform_rules:", "# ---------- Prescription-level"),
		"unknown reason":          strings.Replace(s, "reason: RX_PROHIBITED }", "reason: RX_MISSING }", 1),
		"unknown outcome":         strings.Replace(s, "outcome: allow, reason: null }   # the telemedicine", "outcome: maybe, reason: null }   # the telemedicine", 1),
		"unknown mode value":      strings.Replace(s, "mode: [audio, text], context: any", "mode: [carrier_pigeon], context: any", 1),
		"bad status":              strings.Replace(s, "status: draft            #", "status: wip            #", 1),
		"approved no reviewers":   strings.Replace(s, "status: draft            #", "status: approved            #", 1),
		"two documents":           s + "\n---\nfoo: bar\n",
		"not yaml":                "regulatory_rules: [",
		"platform may only block": strings.Replace(s, "{ id: PL02, applies_to: unclassified, mode: remote, outcome: block", "{ id: PL02, applies_to: unclassified, mode: remote, outcome: allow", 1),
		"missing check":           strings.Replace(s, "check: patient_age_present", "check: nothing", 1),
	}
	for name, bad := range cases {
		if bad == s {
			t.Fatalf("%s: test mutation did not change the file", name)
		}
		if _, err := rules.Load([]byte(bad), rules.Options{}); err == nil {
			t.Errorf("%s: want a load error", name)
		}
	}
	if _, err := rules.Load(nil, rules.Options{}); err == nil {
		t.Error("empty file must be refused")
	}
}

// cut removes the text from the line starting with from up to (not including) the line starting with to.
func cut(s, from, to string) string {
	i := strings.Index(s, "\n"+from)
	j := strings.Index(s, "\n"+to)
	if i < 0 || j < 0 || j < i {
		return s
	}
	return s[:i] + s[j:]
}

// ---- T01 to T31 (spec section 9) ----

func TestT01_ProhibitedFirstVideo(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "x-fake", rules.ContextNewDrug)}
	r := e.Check(in)
	primary(t, r, "RX_PROHIBITED")
	if !hasHit(r.Items[0], rules.LayerRegulatory, "R01") {
		t.Fatalf("want R01 hit: %+v", r.Items[0].Hits)
	}
	// No override: nothing the caller can set unlocks it (every verification flag is already on).
	in.Stage = rules.StageDraft
	in.Mode = rules.ModeVideo
	blocked(t, e.Check(in), "RX_PROHIBITED")
}

func TestT01_NoOverrideAndNoSuggestionFields(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(rules.Input{}), reflect.TypeOf(rules.Item{}), reflect.TypeOf(rules.Result{}),
		reflect.TypeOf(rules.ItemResult{}), reflect.TypeOf(rules.Reason{}), reflect.TypeOf(rules.Hit{})} {
		for i := 0; i < typ.NumField(); i++ {
			n := strings.ToLower(typ.Field(i).Name)
			for _, bad := range []string{"override", "bypass", "force", "suggest", "alternative", "recommend", "default", "autofill"} {
				if strings.Contains(n, bad) {
					t.Errorf("%s.%s: the engine has no override and never suggests (K-CS-03)", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}

func TestT02_InPersonNDPSDrugBlockedByPlatformRule(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeInPerson
	in.Items = []rules.Item{{Ref: "l1", DrugRef: "ndps-fake", Dose: rules.Dose{Amount: 1, Unit: "mg"}, Frequency: rules.Frequency{Times: 1, Period: "day"},
		Duration: rules.Duration{Value: 1, Unit: "day"}, Route: "oral"}}
	r := e.Check(in)
	primary(t, r, "RX_PROHIBITED_IN_PRODUCT")
	it := r.Items[0]
	if !hasHit(it, rules.LayerRegulatory, "R02") || !hasHit(it, rules.LayerPlatform, "PL01") {
		t.Fatalf("regulatory R02 allows, PL01 blocks: %+v", it.Hits)
	}
}

func TestT03_FirstAudioListO(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	allowed(t, e.Check(in))
}

func TestT04_FirstVideoNewListA(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextNewDrug)}
	r := e.Check(in)
	allowed(t, r)
	if !hasHit(r.Items[0], rules.LayerRegulatory, "R04") {
		t.Fatalf("want R04: %+v", r.Items[0].Hits)
	}
}

func TestT05_FirstAudioListA(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextNewDrug)}
	primary(t, e.Check(in), "RX_NEEDS_VIDEO_FIRST")
}

func TestT06_VideoDroppedToAudioBeforeSigning(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextNewDrug)}
	allowed(t, e.Check(in))   // started in video
	in.Mode = rules.ModeAudio // the mode at signing is audio
	primary(t, e.Check(in), "RX_NEEDS_VIDEO_FIRST")
}

func TestTC38_VideoDroppedToAudioListAFirst(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextNewDrug)}
	r := e.Check(in)
	primary(t, r, "RX_NEEDS_VIDEO_FIRST")
	if r.Items[0].Reasons[0].Key != "rx.block.needs_video" {
		t.Fatalf("key %q", r.Items[0].Reasons[0].Key)
	}
}

func TestT07_FollowUpRefillListAAudio(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -120))
	it := item("l1", "a-fake", rules.ContextRefill)
	it.PriorPrescribed = true
	in.Items = []rules.Item{it}
	r := e.Check(in)
	allowed(t, r)
	if r.ConsultType != rules.ConsultFollowUp || !hasHit(r.Items[0], rules.LayerRegulatory, "R07") {
		t.Fatalf("type=%s hits=%+v", r.ConsultType, r.Items[0].Hits)
	}
}

func TestT07b_RefillNotSupportedByHistoryIsTreatedAsNewDrug(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -120))
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextRefill)} // PriorPrescribed false
	r := e.Check(in)
	primary(t, r, "RX_NEEDS_VIDEO_FIRST")
	if r.Items[0].ContextUsed != rules.ContextNewDrug {
		t.Fatalf("context %s", r.Items[0].ContextUsed)
	}
}

func TestT08_FollowUpWindowExpiredTreatedAsFirst(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -200))
	it := item("l1", "a-fake", rules.ContextRefill)
	it.PriorPrescribed = true
	in.Items = []rules.Item{it}
	r := e.Check(in)
	primary(t, r, "RX_NEEDS_VIDEO_FIRST")
	if r.ConsultType != rules.ConsultFirst || r.HasReason("RX_FOLLOW_UP_NOT_ESTABLISHED") {
		t.Fatalf("treated as first, no P06 block: type=%s %+v", r.ConsultType, r.Reasons)
	}
}

func TestT09_FollowUpTextListBAddOn(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeText
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -30))
	in.Items = []rules.Item{item("l1", "b-fake", rules.ContextAddOn)}
	r := e.Check(in)
	allowed(t, r)
	if !hasHit(r.Items[0], rules.LayerRegulatory, "R08") {
		t.Fatalf("want R08: %+v", r.Items[0].Hits)
	}
}

func TestT10_FollowUpListANewConditionIsFirst(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -30))
	it := item("l1", "a-fake", rules.ContextNewDrug)
	it.ForNewCondition = true
	in.Items = []rules.Item{it}
	r := e.Check(in)
	primary(t, r, "RX_NEEDS_VIDEO_FIRST")
	if r.Items[0].EffectiveConsultType != rules.ConsultFirst || !hasHit(r.Items[0], rules.LayerRegulatory, "R05") {
		t.Fatalf("want first and R05: %+v", r.Items[0])
	}
}

func TestT11_UnclassifiedFirstVideo(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "none-fake", rules.ContextNewDrug)}
	r := e.Check(in)
	primary(t, r, "RX_NOT_CLASSIFIED")
	if !hasHit(r.Items[0], rules.LayerPlatform, "PL02") {
		t.Fatalf("want PL02: %+v", r.Items[0].Hits)
	}
}

func TestT12_UnclassifiedFirstAudio(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.Items = []rules.Item{item("l1", "none-fake", rules.ContextNewDrug)}
	primary(t, e.Check(in), "RX_NOT_CLASSIFIED")
}

func TestT13_DoctorNotVerified(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.PrescriberVerified = false
	r := e.Check(in)
	blocked(t, r, "RX_DOCTOR_NOT_VERIFIED")
	if len(r.Reasons) == 0 || r.Reasons[0].Code != "RX_DOCTOR_NOT_VERIFIED" {
		t.Fatalf("whole prescription block: %+v", r.Reasons)
	}
}

func TestT14_DiagnosisMissing(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.DiagnosisPresent = false
	blocked(t, e.Check(in), "RX_DIAGNOSIS_MISSING")
}

func TestT15_DoctorInitiatedWithoutExplicitConsent(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Initiator = "doctor"
	blocked(t, e.Check(in), "RX_CONSENT_MISSING")
	in.ExplicitConsent = true
	allowed(t, e.Check(in))
	in.Initiator = "stranger"
	in.ExplicitConsent = true
	blocked(t, e.Check(in), "RX_CONSENT_MISSING")
}

func TestT16_FollowUpAttestationsMissing(t *testing.T) {
	e := engine(t)
	for name, mut := range map[string]func(*rules.FollowUp){
		"condition": func(f *rules.FollowUp) { f.SameConditionAttested = false },
		"symptoms":  func(f *rules.FollowUp) { f.NoNewSymptomsAttested = false },
		"context":   func(f *rules.FollowUp) { f.RecallsContextAttested = false },
	} {
		in := good(e)
		in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -30))
		mut(&in.FollowUp)
		r := e.Check(in)
		if !r.HasReason("RX_FOLLOW_UP_NOT_ESTABLISHED") || r.Allowed {
			t.Errorf("%s: want RX_FOLLOW_UP_NOT_ESTABLISHED block", name)
		}
	}
}

func TestT17_DoseIncomplete(t *testing.T) {
	e := engine(t)
	mutations := map[string]func(*rules.Item){
		"dose empty":       func(i *rules.Item) { i.Dose = rules.Dose{} },
		"dose unit":        func(i *rules.Item) { i.Dose.Unit = "" },
		"dose free text":   func(i *rules.Item) { i.Dose.Unit = "one tablet after food" },
		"frequency empty":  func(i *rules.Item) { i.Frequency = rules.Frequency{} },
		"duration empty":   func(i *rules.Item) { i.Duration = rules.Duration{} },
		"route empty":      func(i *rules.Item) { i.Route = "" },
		"negative dose":    func(i *rules.Item) { i.Dose.Amount = -1 },
		"taper step empty": func(i *rules.Item) { i.Taper = []rules.TaperStep{{}} },
	}
	for name, mut := range mutations {
		in := good(e)
		it := item("l1", "para", rules.ContextNewDrug)
		mut(&it)
		in.Items = []rules.Item{it}
		r := e.Check(in)
		if r.Allowed || !r.HasReason("RX_DOSE_INCOMPLETE") {
			t.Errorf("%s: want RX_DOSE_INCOMPLETE block, got %+v", name, r.Items)
		}
	}
	// A complete tapering schedule, as-needed use and free-text instructions are fine.
	in := good(e)
	it := item("l1", "para", rules.ContextNewDrug)
	it.AsNeeded = true
	it.Taper = []rules.TaperStep{{Dose: rules.Dose{Amount: 125, Unit: "mg"}, Frequency: rules.Frequency{Times: 1, Period: "day"}, Duration: rules.Duration{Value: 3, Unit: "day"}}}
	it.Instructions = "any free text, ignored by the rules"
	in.Items = []rules.Item{it}
	allowed(t, e.Check(in))
}

func TestT18_EveryBlockHasAnI18nKey(t *testing.T) {
	e := engine(t)
	var all []rules.Result
	in := good(e)
	in.PrescriberVerified, in.DiagnosisPresent, in.StepUpDone, in.ESignatureApplied, in.IdentityConfirmed = false, false, false, false, false
	in.PatientAgeYears, in.PatientCountry, in.Initiator = 0, "", "x"
	in.Items = []rules.Item{item("a", "x-fake", rules.ContextNewDrug), item("b", "none-fake", rules.ContextNewDrug), item("c", "clonaz", rules.ContextNewDrug),
		item("d", "missing", rules.ContextNewDrug)}
	all = append(all, e.Check(in))
	in.Mode = rules.Mode("")
	all = append(all, e.Check(in))
	for _, r := range all {
		if r.Allowed {
			t.Fatal("must block")
		}
		reasons := append([]rules.Reason{}, r.Reasons...)
		for _, it := range r.Items {
			reasons = append(reasons, it.Reasons...)
		}
		if len(reasons) < 5 {
			t.Fatalf("expected many reasons, got %v", reasons)
		}
		for _, x := range reasons {
			if !strings.HasPrefix(x.Code, "RX_") || !strings.HasPrefix(x.Key, "rx.block.") {
				t.Errorf("reason without code or key: %+v", x)
			}
		}
	}
}

func TestT19_Performance(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = nil
	for i := 0; i < 30; i++ {
		in.Items = append(in.Items, item("l"+string(rune('a'+i%26))+string(rune('a'+i/26)), "para", rules.ContextNewDrug))
	}
	e.Check(in) // warm up
	start := time.Now()
	for i := 0; i < 100; i++ {
		e.Check(in)
	}
	if per := time.Since(start) / 100; per > 50*time.Millisecond {
		t.Fatalf("%v per prescription, limit 50 ms", per)
	}
}

func BenchmarkCheck(b *testing.B) {
	e := engine(b)
	in := good(e)
	for i := 0; i < b.N; i++ {
		e.Check(in)
	}
}

func TestT20_UnknownCombinationFailsClosed(t *testing.T) {
	e := engine(t)
	// List B only, first consult: no table row speaks.
	in := good(e)
	in.Items = []rules.Item{item("l1", "b-fake", rules.ContextNewDrug)}
	blocked(t, e.Check(in), "RX_NO_RULE_MATCHED")
	// List A first consult in video, but marked as a refill: no row speaks.
	in = good(e)
	it := item("l1", "a-fake", rules.ContextRefill)
	it.PriorPrescribed = true
	in.Items = []rules.Item{it}
	blocked(t, e.Check(in), "RX_NO_RULE_MATCHED")
	// Unknown drug reference, unknown mode, missing context, missing classification, wrong rule-set version.
	in = good(e)
	in.Items = []rules.Item{item("l1", "no-such-drug", rules.ContextNewDrug)}
	blocked(t, e.Check(in), "RX_UNKNOWN_DRUG")
	in = good(e)
	in.Mode = "hologram"
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.Items[0].Context = ""
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.Classification = nil
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.RuleSetVersion = "9.9.9"
	blocked(t, e.Check(in), "RX_RULESET_MISMATCH")
	in = good(e)
	in.RuleSetVersion = ""
	blocked(t, e.Check(in), "RX_RULESET_MISMATCH")
	in = good(e)
	in.Items = nil
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.Stage = "later"
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.Items = []rules.Item{item("dup", "para", rules.ContextNewDrug), item("dup", "para", rules.ContextNewDrug)}
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	in = good(e)
	in.Items[0].DrugRef = "Bad Ref!"
	blocked(t, e.Check(in), "RX_INPUT_INVALID")
	// A panic inside a classifier is an internal error, which blocks.
	in = good(e)
	in.Classification = panicking{}
	blocked(t, e.Check(in), "RX_INTERNAL_ERROR")
}

type panicking struct{}

func (panicking) Version() string                  { return "CLS-PANIC" }
func (panicking) Lookup(string) (rules.Drug, bool) { panic("boom") }

func TestT20b_ContradictoryDrugDataBlocksAsProhibited(t *testing.T) {
	e := engine(t)
	for _, ref := range []string{"contra-fake", "claim-fake"} {
		in := good(e)
		in.Items = []rules.Item{item("l1", ref, rules.ContextNewDrug)}
		primary(t, e.Check(in), "RX_PROHIBITED")
	}
}

func TestT21_FollowUpAudioAddOnListANotListB(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -30))
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextAddOn)}
	r := e.Check(in)
	primary(t, r, "RX_NEEDS_VIDEO_FIRST")
	if !hasHit(r.Items[0], rules.LayerRegulatory, "R10") {
		t.Fatalf("want R10: %+v", r.Items[0].Hits)
	}
	// In List B as well, the add-on is allowed (R08 comes before R10).
	in.Items = []rules.Item{item("l1", "ab-fake", rules.ContextAddOn)}
	allowed(t, e.Check(in))
	// On video the follow-up add-on of a List A drug is allowed (R09).
	in.Mode = rules.ModeVideo
	in.Items = []rules.Item{item("l1", "a-fake", rules.ContextAddOn)}
	r = e.Check(in)
	allowed(t, r)
	if !hasHit(r.Items[0], rules.LayerRegulatory, "R09") {
		t.Fatalf("want R09: %+v", r.Items[0].Hits)
	}
}

func TestT22_ClonazepamRegulatoryAllowPlatformBlock(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "clonaz", rules.ContextNewDrug)}
	r := e.Check(in)
	primary(t, r, "RX_CONTROLLED_IN_PRODUCT")
	it := r.Items[0]
	if !hasHit(it, rules.LayerRegulatory, "R04") || !hasHit(it, rules.LayerPlatform, "PL03") || hasHit(it, rules.LayerRegulatory, "R01") {
		t.Fatalf("regulatory R04 allow, PL03 block, never R01: %+v", it.Hits)
	}
	for _, h := range it.Hits {
		if h.Layer == rules.LayerRegulatory && !h.Allowed {
			t.Fatalf("regulatory layer must allow: %+v", h)
		}
	}
}

func TestTC35_ProhibitedNoOverrideAndClonazepam(t *testing.T) {
	e := engine(t)
	for _, mode := range []rules.Mode{rules.ModeVideo, rules.ModeAudio, rules.ModeText} {
		in := good(e)
		in.Mode = mode
		in.Items = []rules.Item{item("l1", "x-fake", rules.ContextNewDrug)}
		primary(t, e.Check(in), "RX_PROHIBITED")
	}
	// Clonazepam: blocked in every mode by PL03, also in person; on audio it also needs video.
	for _, mode := range []rules.Mode{rules.ModeVideo, rules.ModeAudio, rules.ModeInPerson} {
		in := good(e)
		in.Mode = mode
		in.Items = []rules.Item{item("l1", "clonaz", rules.ContextNewDrug)}
		blocked(t, e.Check(in), "RX_CONTROLLED_IN_PRODUCT")
	}
}

func TestT23_OtherNDPSFirstVideo(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Items = []rules.Item{item("l1", "ndps-fake", rules.ContextNewDrug)}
	primary(t, e.Check(in), "RX_PROHIBITED")
}

func TestT24_FollowUpWindowClampsToMonthEnd(t *testing.T) {
	e := engine(t)
	check := func(last, at time.Time, tz string) rules.ConsultType {
		in := good(e)
		in.Mode = rules.ModeAudio
		in.PatientTimeZone = tz
		in.ConsultAt = at
		in.FollowUp = followUpFrom(last)
		return e.Check(in).ConsultType
	}
	aug31 := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	if got := check(aug31, time.Date(2027, 2, 28, 12, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFollowUp {
		t.Errorf("last day of February: %s", got)
	}
	if got := check(aug31, time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFirst {
		t.Errorf("1 March: %s", got)
	}
	// Leap year: 31 Aug 2027 + 6 months = 29 Feb 2028.
	aug31b := time.Date(2027, 8, 31, 10, 0, 0, 0, time.UTC)
	if got := check(aug31b, time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFollowUp {
		t.Errorf("leap 29 Feb: %s", got)
	}
	if got := check(aug31b, time.Date(2028, 3, 1, 12, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFirst {
		t.Errorf("leap 1 March: %s", got)
	}
	// The day is included and a plain date works: 15 May + 6 months = 15 Nov.
	may15 := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	if got := check(may15, time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFollowUp {
		t.Errorf("same day-of-month: %s", got)
	}
	if got := check(may15, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFirst {
		t.Errorf("one day late: %s", got)
	}
	// A consult before the in-person visit is not a follow-up.
	if got := check(may15, may15.AddDate(0, 0, -1), "UTC"); got != rules.ConsultFirst {
		t.Errorf("before the visit: %s", got)
	}
	// The window is counted in the patient's zone: 20:00 UTC on 15 Nov is already 16 Nov in a zone ahead of UTC.
	if got := check(may15, time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC), "Pacific/Auckland"); got != rules.ConsultFirst {
		t.Errorf("zone ahead of UTC: %s", got)
	}
	if got := check(may15, time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC), "UTC"); got != rules.ConsultFollowUp {
		t.Errorf("UTC: %s", got)
	}
}

func TestT24b_FollowUpClaimNotEstablished(t *testing.T) {
	e := engine(t)
	base := func() rules.Input {
		in := good(e)
		in.Mode = rules.ModeAudio
		in.FollowUp = followUpFrom(consultAt.AddDate(0, 0, -30))
		return in
	}
	for name, mut := range map[string]func(*rules.Input){
		"other doctor":     func(in *rules.Input) { in.FollowUp.SameDoctor = false },
		"no prior visit":   func(in *rules.Input) { in.FollowUp.LastInPersonAt = time.Time{} },
		"unknown evidence": func(in *rules.Input) { in.FollowUp.Evidence = "hearsay" },
		"not claimed":      func(in *rules.Input) { in.FollowUp.Claimed = false },
	} {
		in := base()
		mut(&in)
		if got := e.Check(in).ConsultType; got != rules.ConsultFirst {
			t.Errorf("%s: treated as %s, want first", name, got)
		}
	}
	// Claimed without a usable time zone or consult time cannot be evaluated: block.
	for name, mut := range map[string]func(*rules.Input){
		"no zone":  func(in *rules.Input) { in.PatientTimeZone = "" },
		"bad zone": func(in *rules.Input) { in.PatientTimeZone = "Mars/Olympus" },
		"no time":  func(in *rules.Input) { in.ConsultAt = time.Time{} },
	} {
		in := base()
		mut(&in)
		if r := e.Check(in); r.Allowed || !r.HasReason("RX_INPUT_INVALID") {
			t.Errorf("%s: want RX_INPUT_INVALID block", name)
		}
	}
}

func TestT25_StepUpMissingAtSigning(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.StepUpDone = false
	r := e.Check(in)
	blocked(t, r, "RX_SIGNATURE_REQUIRED")
	// While drafting, P08 and P09 are not evaluated.
	in.Stage = rules.StageDraft
	in.ESignatureApplied = false
	allowed(t, e.Check(in))
	// An empty stage means signing, the strictest.
	in.Stage = ""
	blocked(t, e.Check(in), "RX_SIGNATURE_REQUIRED")
}

func TestT26_EvaluationCarriesVersions(t *testing.T) {
	e := engine(t)
	r := e.Check(good(e))
	if r.RuleSetVersion != e.Version() || r.RuleSetChecksum != e.Checksum() || r.DrugClassificationVersion != "CLS-TEST-1" {
		t.Fatalf("versions %q %q %q", r.RuleSetVersion, r.RuleSetChecksum, r.DrugClassificationVersion)
	}
	// Also on a block.
	in := good(e)
	in.RuleSetVersion = "x"
	if r := e.Check(in); r.RuleSetVersion != e.Version() || r.DrugClassificationVersion != "CLS-TEST-1" || r.RuleSetChecksum == "" {
		t.Fatalf("block carries versions: %+v", r)
	}
}

func TestT27_EsignMissingAfterStepUp(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.ESignatureApplied = false
	r := e.Check(in)
	blocked(t, r, "RX_ESIGN_REQUIRED")
	if r.HasReason("RX_SIGNATURE_REQUIRED") {
		t.Fatalf("step-up is done: %+v", r.Reasons)
	}
}

func TestT28_SignedPrescriptionTamperDetection(t *testing.T) {
	t.Skip("not in this story: the signature check on a changed byte belongs to the e-signature adapter and verification page (S-150, S-151, TC-60); the engine only takes the P08/P09 flags as input")
}

func TestT29_UnclassifiedStillBlockedWhenSwitchedToVideo(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.Mode = rules.ModeAudio
	in.Items = []rules.Item{item("l1", "none-fake", rules.ContextNewDrug)}
	primary(t, e.Check(in), "RX_NOT_CLASSIFIED")
	in.Mode = rules.ModeVideo
	primary(t, e.Check(in), "RX_NOT_CLASSIFIED")
	// Switching to video never unlocks a prohibited or platform-blocked drug either.
	in.Items = []rules.Item{item("l1", "x-fake", rules.ContextNewDrug), item("l2", "clonaz", rules.ContextNewDrug)}
	r := e.Check(in)
	if r.Allowed || r.Items[0].Allowed || r.Items[1].Allowed {
		t.Fatalf("%+v", r.Items)
	}
}

func TestT30_ImportRejectsProhibitedAndListedWithoutException(t *testing.T) {
	e := engine(t)
	bad := validFile(entry("fakeamycin", "[prohibited, list_a]", "false", "[]"))
	if _, err := e.ParseClassification([]byte(bad)); err == nil || !strings.Contains(err.Error(), "without an amendment exception") {
		t.Fatalf("want rejection, got %v", err)
	}
}

func TestT31_PatientOutsideIndia(t *testing.T) {
	e := engine(t)
	for _, mode := range []rules.Mode{rules.ModeVideo, rules.ModeAudio, rules.ModeText} {
		in := good(e)
		in.Mode = mode
		in.PatientCountry = "" // did not confirm
		blocked(t, e.Check(in), "RX_PATIENT_OUTSIDE_INDIA")
		in.PatientCountry = "AE"
		blocked(t, e.Check(in), "RX_PATIENT_OUTSIDE_INDIA")
	}
}

func TestTC24_P10OnlineRefusedAbroadInPersonAllowed(t *testing.T) {
	e := engine(t)
	in := good(e)
	in.PatientCountry = "AE"
	r := e.Check(in)
	if r.Allowed || len(r.Reasons) == 0 || r.Reasons[0].Code != "RX_PATIENT_OUTSIDE_INDIA" || r.Reasons[0].Key != "rx.block.outside_india" {
		t.Fatalf("%+v", r.Reasons)
	}
	in.Mode = rules.ModeInPerson // a patient travelling to India books in person
	allowed(t, e.Check(in))
	in.PatientCountry = ""
	allowed(t, e.Check(in))
}

func TestPrescriptionLevelChecksAreNotSkipped(t *testing.T) {
	e := engine(t)
	for name, mut := range map[string]func(*rules.Input){
		"identity": func(in *rules.Input) { in.IdentityConfirmed = false },
		"age":      func(in *rules.Input) { in.PatientAgeYears = 0 },
	} {
		in := good(e)
		mut(&in)
		in.Mode = rules.ModeInPerson
		if r := e.Check(in); r.Allowed {
			t.Errorf("%s: must block in person too", name)
		}
	}
	// One blocked item blocks the whole prescription, and the good item keeps its own result.
	in := good(e)
	in.Items = []rules.Item{item("a", "para", rules.ContextNewDrug), item("b", "x-fake", rules.ContextNewDrug)}
	r := e.Check(in)
	if r.Allowed || !r.Items[0].Allowed || r.Items[1].Allowed {
		t.Fatalf("%+v", r.Items)
	}
}

// ---- drug master import validation ----

func validFile(entries ...string) string {
	return "version: 2026.12.1\nstatus: draft\nentries:\n" + strings.Join(entries, "")
}

func entry(name, lists, exception, tags string) string {
	return "  - ref: " + strings.ReplaceAll(name, " ", "-") + "\n    generic_name: " + name +
		"\n    strength: 250 mg\n    form: tablet\n    route: oral\n    lists: " + lists +
		"\n    amendment_exception: " + exception + "\n    condition_tags: " + tags + "\n    source: Synthetic test source\n"
}

func TestImportValidation(t *testing.T) {
	e := engine(t)
	empty, err := e.ParseClassification([]byte("version: 0.1.0-draft\nstatus: draft\nentries: []\n"))
	if err != nil || len(empty.Entries) != 0 {
		t.Fatalf("the empty table must parse (everything is blocked): %v", err)
	}
	if _, err := e.ParseClassification(loadClassificationFile(t)); err != nil {
		t.Fatalf("the pack's file must parse: %v", err)
	}
	amend := entry("phenobarbitone", "[prohibited, list_a]", "true", "[]") + entry("clobazam", "[prohibited, list_a]", "true", "[]") +
		entry("clonazepam", "[prohibited, list_a]", "true", "[]")
	ok, err := e.ParseClassification([]byte(validFile(entry("fakeamycin", "[list_a]", "false", "[]"), entry("fakebloc", "[list_b]", "false", "[cond_x]"), amend)))
	if err != nil || len(ok.Entries) != 5 || len(ok.Checksum) != 64 {
		t.Fatalf("valid file: %v", err)
	}
	cases := map[string]string{
		"duplicate ref":            validFile(entry("fakeamycin", "[list_a]", "false", "[]"), entry("fakeamycin", "[list_o]", "false", "[]"), amend),
		"no source":                strings.Replace(validFile(entry("fakeamycin", "[list_a]", "false", "[]"), amend), "    source: Synthetic test source\n", "", 1),
		"exception for other drug": validFile(entry("fakeamycin", "[prohibited, list_a]", "true", "[]"), amend),
		"list b without tag":       validFile(entry("fakebloc", "[list_b]", "false", "[]"), amend),
		"missing amendment drug":   validFile(entry("fakeamycin", "[list_a]", "false", "[]"), entry("clobazam", "[prohibited, list_a]", "true", "[]")),
		"unknown list":             validFile(entry("fakeamycin", "[list_z]", "false", "[]"), amend),
		"repeated list":            validFile(entry("fakeamycin", "[list_a, list_a]", "false", "[]"), amend),
		"unknown key":              validFile(entry("fakeamycin", "[list_a]", "false", "[]"), amend) + "extra: 1\n",
		"no entries key":           "version: 1\nstatus: draft\n",
		"bad status":               strings.Replace(validFile(entry("fakeamycin", "[list_a]", "false", "[]"), amend), "status: draft", "status: ok", 1),
		"no version":               strings.Replace(validFile(entry("fakeamycin", "[list_a]", "false", "[]"), amend), "version: 2026.12.1\n", "", 1),
	}
	for name, bad := range cases {
		if _, err := e.ParseClassification([]byte(bad)); err == nil || !strings.Contains(err.Error(), "drug classification rejected") {
			t.Errorf("%s: want rejection, got %v", name, err)
		}
	}
	// Duplicate drug under another ref.
	dup := validFile(entry("fakeamycin", "[list_a]", "false", "[]"), strings.Replace(entry("fakeamycin", "[list_o]", "false", "[]"), "ref: fakeamycin", "ref: fakeamycin-2", 1), amend)
	if _, err := e.ParseClassification([]byte(dup)); err == nil {
		t.Error("duplicate drug must be rejected")
	}
}

func loadClassificationFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../../config/country-packs/IN/drugs/classification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// With the pack's empty classification nothing is prescribable (fail closed), in any mode but in person.
func TestEmptyClassificationBlocksEverythingRemote(t *testing.T) {
	e := engine(t)
	c, err := e.ParseClassification(loadClassificationFile(t))
	if err != nil {
		t.Fatal(err)
	}
	m := rules.NewMapClassifier(c.Version)
	in := good(e)
	in.Classification = m
	blocked(t, e.Check(in), "RX_UNKNOWN_DRUG")
}
