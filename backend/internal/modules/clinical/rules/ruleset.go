// Package rules is the prescription rules engine (S-138, K-CS-02, K-CS-03).
//
// It is a pure library: no database, no clock, no logging. It parses the country pack's
// prescribing rule tables (strict YAML decoding) and decides, for each prescription item,
// whether the item may be issued in the given consult. It only ALLOWS or BLOCKS, with
// reason codes. It never suggests, infers or auto-fills a drug, dose or alternative
// (K-CS-03). Everything unknown fails closed.
//
// No country values live here (K-GLB-01): drug lists, windows, drug names and messages come
// from the rule file and the drug master.
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Reason codes the engine itself raises when no table row speaks (fail closed). They are
// not in the rule file, so their translation keys are fixed here. The codes that come from
// the spec (RX_PROHIBITED, RX_NEEDS_VIDEO_FIRST, ...) are read from the rule file.
const (
	CodeNoRuleMatched   = "RX_NO_RULE_MATCHED"
	CodeInputInvalid    = "RX_INPUT_INVALID"
	CodeUnknownDrug     = "RX_UNKNOWN_DRUG"
	CodeRuleSetMismatch = "RX_RULESET_MISMATCH"
	CodeInternalError   = "RX_INTERNAL_ERROR"
)

var engineReasonKeys = map[string]string{
	CodeNoRuleMatched:   "rx.block.no_rule_matched",
	CodeInputInvalid:    "rx.block.input_invalid",
	CodeUnknownDrug:     "rx.block.unknown_drug",
	CodeRuleSetMismatch: "rx.block.ruleset_mismatch",
	CodeInternalError:   "rx.block.internal_error",
}

// stringList decodes a YAML scalar or a sequence into a list.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*s = nil
			return nil
		}
		*s = stringList{n.Value}
	case yaml.SequenceNode:
		out := make(stringList, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a text value", c.Line)
			}
			out = append(out, c.Value)
		}
		*s = out
	default:
		return fmt.Errorf("line %d: expected a text value or a list", n.Line)
	}
	return nil
}

// appliesTo is a platform rule selector: a keyword (scalar) or a list of generic names.
type appliesTo struct {
	Category string   // "prohibited" or "unclassified"
	Names    []string // lower-case generic names
}

func (a *appliesTo) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		a.Category = n.Value
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode || strings.TrimSpace(c.Value) == "" {
				return fmt.Errorf("line %d: expected a drug name", c.Line)
			}
			a.Names = append(a.Names, NormalizeName(c.Value))
		}
	default:
		return fmt.Errorf("line %d: applies_to must be a keyword or a list of drug names", n.Line)
	}
	return nil
}

// NormalizeName lower-cases and trims a generic drug name for comparison.
func NormalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

type amendment struct {
	ID               string `yaml:"id"`
	Date             string `yaml:"date"`
	IssuedBy         string `yaml:"issued_by"`
	Summary          string `yaml:"summary"`
	AppliedInVersion string `yaml:"applied_in_version"`
	Validate         bool   `yaml:"validate"`
}

type sourceInfo struct {
	Title                  string      `yaml:"title"`
	IssuedBy               string      `yaml:"issued_by"`
	Date                   string      `yaml:"date"`
	StillCitedAsOf         string      `yaml:"still_cited_as_of"`
	Amendments             []amendment `yaml:"amendments"`
	ReviewForNewAmendments string      `yaml:"review_for_new_amendments"`
}

type drugClassificationInfo struct {
	Source           string   `yaml:"source"`
	VersionRef       string   `yaml:"version_ref"`
	Membership       string   `yaml:"membership"`
	Precedence       []any    `yaml:"precedence"`
	ImportValidation []string `yaml:"import_validation"`
}

type patientLocation struct {
	RemoteModesAllowedFor []string `yaml:"remote_modes_allowed_for"`
	Evidence              []string `yaml:"evidence"`
	InPerson              string   `yaml:"in_person"`
}

type followUpWindow struct {
	Months       int    `yaml:"months"`
	MeasuredFrom string `yaml:"measured_from"`
	TimeZone     string `yaml:"time_zone"`
	MonthEnd     string `yaml:"month_end"`
	Inclusive    bool   `yaml:"inclusive"`
}

type followUpDef struct {
	SameDoctor                   bool           `yaml:"same_doctor"`
	RequiresPriorInPersonConsult bool           `yaml:"requires_prior_in_person_consult"`
	Window                       followUpWindow `yaml:"window"`
	SameHealthCondition          string         `yaml:"same_health_condition"`
	Exclusions                   []string       `yaml:"exclusions"`
	PriorInPersonEvidence        []string       `yaml:"prior_in_person_evidence"`
}

type consultDef struct {
	Modes                  []string        `yaml:"modes"`
	RemoteModes            []string        `yaml:"remote_modes"`
	V1EnabledModes         []string        `yaml:"v1_enabled_modes"`
	ModeForRules           string          `yaml:"mode_for_rules"`
	PatientLocation        patientLocation `yaml:"patient_location"`
	Types                  []string        `yaml:"types"`
	FollowUp               followUpDef     `yaml:"follow_up"`
	ItemConditionScope     []string        `yaml:"item_condition_scope"`
	NewConditionItemEvalAs string          `yaml:"new_condition_item_evaluated_as"`
	Initiators             []string        `yaml:"initiators"`
}

type categoryDef struct {
	Description       string         `yaml:"description"`
	Match             map[string]any `yaml:"match"`
	ExceptByAmendment []string       `yaml:"except_by_amendment"`
}

type regulatoryRule struct {
	ID          string     `yaml:"id"`
	Category    stringList `yaml:"category"`
	ConsultType stringList `yaml:"consult_type"`
	Mode        stringList `yaml:"mode"`
	Context     stringList `yaml:"context"`
	Outcome     string     `yaml:"outcome"`
	Reason      string     `yaml:"reason"`
}

type platformRule struct {
	ID        string     `yaml:"id"`
	AppliesTo appliesTo  `yaml:"applies_to"`
	Mode      stringList `yaml:"mode"`
	Outcome   string     `yaml:"outcome"`
	Reason    string     `yaml:"reason"`
	Decision  string     `yaml:"decision"`
	Basis     string     `yaml:"basis"`
}

type checkDef struct {
	ID     string `yaml:"id"`
	Check  string `yaml:"check"`
	Reason string `yaml:"reason"`
}

type reasonDef struct {
	Key string `yaml:"key"`
	EN  string `yaml:"en"`
}

type consentDef struct {
	ImpliedWhenInitiator  []string `yaml:"implied_when_initiator"`
	ExplicitWhenInitiator []string `yaml:"explicit_when_initiator"`
	ExplicitForms         []string `yaml:"explicit_forms"`
	RecordFields          []string `yaml:"record_fields"`
}

// ruleFile is the whole rule file. Strict decoding: an unknown key anywhere in a typed
// section is an error. Sections the engine does not execute are kept as raw nodes.
type ruleFile struct {
	Pack                 string                 `yaml:"pack"`
	Ruleset              string                 `yaml:"ruleset"`
	Version              string                 `yaml:"version"`
	Status               string                 `yaml:"status"`
	Source               sourceInfo             `yaml:"source"`
	ReviewedBy           any                    `yaml:"reviewed_by"`
	LegalReviewedBy      any                    `yaml:"legal_reviewed_by"`
	DrugClassification   drugClassificationInfo `yaml:"drug_classification"`
	Consult              consultDef             `yaml:"consult"`
	Categories           map[string]categoryDef `yaml:"categories"`
	RegulatoryRules      []regulatoryRule       `yaml:"regulatory_rules"`
	PlatformRules        []platformRule         `yaml:"platform_rules"`
	PrescriptionChecks   []checkDef             `yaml:"prescription_checks"`
	Audit                yaml.Node              `yaml:"audit"`
	DoctorJudgement      yaml.Node              `yaml:"doctor_judgement"`
	Consent              consentDef             `yaml:"consent"`
	PharmacyTransmission yaml.Node              `yaml:"pharmacy_transmission"`
	Identification       yaml.Node              `yaml:"identification"`
	Records              yaml.Node              `yaml:"records"`
	Reasons              map[string]reasonDef   `yaml:"reasons"`
}

// Rule-file status values (draft -> doctor_reviewed -> legal_reviewed -> approved).
const (
	StatusDraft          = "draft"
	StatusDoctorReviewed = "doctor_reviewed"
	StatusLegalReviewed  = "legal_reviewed"
	StatusApproved       = "approved"
)

// Options for Load.
type Options struct {
	// RequireApproved makes Load refuse a rule file whose status is not "approved"
	// (production). Drafts load only in synthetic-data environments.
	RequireApproved bool
	// ExpectedSHA256, when set, must equal the SHA-256 (hex) of the rule file bytes
	// (TC-59: a rule file with a bad checksum is refused).
	ExpectedSHA256 string
}

// Names of the prescription-level checks the engine executes (the YAML "check" value).
const (
	chkDoctorVerified  = "doctor_verified_and_active"
	chkIdentity        = "patient_identity_confirmed_by_doctor"
	chkAge             = "patient_age_present"
	chkConsent         = "teleconsult_consent_recorded"
	chkDiagnosis       = "diagnosis_or_provisional_diagnosis_present"
	chkFollowUp        = "follow_up_attestations_present_if_follow_up"
	chkStructure       = "structured_dose_fields_complete"
	chkStepUp          = "doctor_reauthenticated_step_up"
	chkESign           = "prescription_signed_with_approved_signature_method"
	chkPatientLocation = "patient_declared_in_india_for_remote_consult"
)

var requiredChecks = []string{chkDoctorVerified, chkIdentity, chkAge, chkConsent, chkDiagnosis, chkFollowUp,
	chkStructure, chkStepUp, chkESign, chkPatientLocation}

// Category names used by the tables.
const (
	catProhibited   = "prohibited"
	catListO        = "list_o"
	catListA        = "list_a"
	catListB        = "list_b"
	catAny          = "any"
	catUnclassified = "unclassified"
)

var permittedCategories = []string{catListO, catListA, catListB}

// compiled forms (built once at load)
type regRule struct {
	id       string
	cats     []string
	types    []string
	modes    []string
	contexts []string
	allow    bool
	reason   string
}

type platRule struct {
	id       string
	category string
	names    []string
	modes    []string
	reason   string
}

type checkRule struct {
	id     string
	check  string
	reason string
}

// Engine evaluates prescriptions against one loaded rule file. It is immutable after Load
// and safe for concurrent use.
type Engine struct {
	f                         *ruleFile
	checksum                  string
	reg                       []regRule
	plat                      []platRule
	checks                    []checkRule
	remote                    map[string]bool
	modes                     map[string]bool
	except                    map[string]bool // amendment exception drug names (lower case)
	allowedCountries          map[string]bool
	evidence                  map[string]bool
	initImplied, initExplicit map[string]bool
	months                    int
}

// Version of the rule set (stored with every prescription).
func (e *Engine) Version() string { return e.f.Version }

// Checksum is the SHA-256 (hex) of the rule file bytes that were loaded.
func (e *Engine) Checksum() string { return e.checksum }

// Status of the rule file (draft, doctor_reviewed, legal_reviewed, approved).
func (e *Engine) Status() string { return e.f.Status }

// Load parses and validates the rule file. Any problem is an error and the caller must
// refuse to start (fail closed, TC-59).
func Load(data []byte, opt Options) (*Engine, error) {
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	if opt.ExpectedSHA256 != "" && !strings.EqualFold(opt.ExpectedSHA256, checksum) {
		return nil, errors.New("rules: rule file checksum mismatch")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f ruleFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("rules: parse: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("rules: the rule file must hold exactly one YAML document")
	}
	e := &Engine{f: &f, checksum: checksum}
	if err := e.compile(opt); err != nil {
		return nil, err
	}
	return e, nil
}

func set(vs []string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

func (e *Engine) compile(opt Options) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf("rules: "+format, a...)) }
	f := e.f

	if f.Pack == "" || f.Version == "" {
		add("pack and version are required")
	}
	if f.Ruleset != "telemedicine-prescribing" {
		add("ruleset must be telemedicine-prescribing")
	}
	switch f.Status {
	case StatusDraft, StatusDoctorReviewed, StatusLegalReviewed:
		if opt.RequireApproved {
			add("status %q: only an approved rule file may load here", f.Status)
		}
	case StatusApproved:
		if f.ReviewedBy == nil || f.LegalReviewedBy == nil {
			add("an approved rule file needs reviewed_by and legal_reviewed_by (K-CS-07)")
		}
	default:
		add("status %q is not draft, doctor_reviewed, legal_reviewed or approved", f.Status)
	}

	c := f.Consult
	if len(c.Modes) == 0 || len(c.RemoteModes) == 0 {
		add("consult.modes and consult.remote_modes are required")
	}
	e.modes, e.remote = set(c.Modes), set(c.RemoteModes)
	for _, m := range c.RemoteModes {
		if !e.modes[m] {
			add("consult.remote_modes: %q is not a mode", m)
		}
	}
	if c.ModeForRules != "mode_at_signing" {
		add("consult.mode_for_rules must be mode_at_signing")
	}
	if len(c.PatientLocation.RemoteModesAllowedFor) == 0 {
		add("consult.patient_location.remote_modes_allowed_for is required")
	}
	e.allowedCountries = set(c.PatientLocation.RemoteModesAllowedFor)
	if c.NewConditionItemEvalAs != "first" {
		add("consult.new_condition_item_evaluated_as must be first")
	}
	w := c.FollowUp.Window
	if w.Months <= 0 || w.Months > 60 || w.MeasuredFrom != "last_in_person_consult" || w.TimeZone != "patient" ||
		w.MonthEnd != "clamp" || !w.Inclusive {
		add("consult.follow_up.window: the engine implements calendar months measured from the last in-person consult, in the patient's time zone, month end clamped, inclusive")
	}
	if !c.FollowUp.SameDoctor || !c.FollowUp.RequiresPriorInPersonConsult {
		add("consult.follow_up: same_doctor and requires_prior_in_person_consult must be true")
	}
	e.months = w.Months
	if len(c.FollowUp.PriorInPersonEvidence) == 0 {
		add("consult.follow_up.prior_in_person_evidence is required")
	}
	e.evidence = set(c.FollowUp.PriorInPersonEvidence)
	e.initImplied, e.initExplicit = set(f.Consent.ImpliedWhenInitiator), set(f.Consent.ExplicitWhenInitiator)
	if len(e.initImplied)+len(e.initExplicit) == 0 {
		add("consent initiators are required")
	}

	for _, name := range append([]string{catProhibited}, permittedCategories...) {
		if _, ok := f.Categories[name]; !ok {
			add("categories.%s is required", name)
		}
	}
	e.except = map[string]bool{}
	for _, n := range f.Categories[catProhibited].ExceptByAmendment {
		e.except[NormalizeName(n)] = true
	}

	reasonOK := func(where, code string) {
		if code == "" {
			add("%s: a block needs a reason", where)
			return
		}
		if r, ok := f.Reasons[code]; !ok || r.Key == "" {
			add("%s: reason %q has no entry (with a key) in reasons", where, code)
		}
	}

	if len(f.RegulatoryRules) == 0 {
		add("regulatory_rules is required")
	}
	seen := map[string]bool{}
	validCat := set(append([]string{catAny, catProhibited}, permittedCategories...))
	validType := set([]string{"any", "first", "follow_up"})
	validCtx := set([]string{"any", "new_drug", "refill", "add_on"})
	validMode := set(append([]string{"any", "remote"}, c.Modes...))
	checkVals := func(where string, vals []string, ok map[string]bool) {
		if len(vals) == 0 {
			add("%s: value required", where)
		}
		for _, v := range vals {
			if !ok[v] {
				add("%s: unknown value %q", where, v)
			}
		}
	}
	for _, r := range f.RegulatoryRules {
		where := "regulatory_rules " + r.ID
		if r.ID == "" || seen[r.ID] {
			add("%s: id missing or duplicated", where)
		}
		seen[r.ID] = true
		checkVals(where+" category", r.Category, validCat)
		checkVals(where+" consult_type", r.ConsultType, validType)
		checkVals(where+" mode", r.Mode, validMode)
		checkVals(where+" context", r.Context, validCtx)
		rr := regRule{id: r.ID, cats: r.Category, types: r.ConsultType, modes: r.Mode, contexts: r.Context, reason: r.Reason}
		switch r.Outcome {
		case "allow":
			rr.allow = true
			if r.Reason != "" {
				add("%s: an allow row has no reason", where)
			}
		case "block":
			reasonOK(where, r.Reason)
		default:
			add("%s: outcome must be allow or block", where)
		}
		e.reg = append(e.reg, rr)
	}

	if len(f.PlatformRules) == 0 {
		add("platform_rules is required")
	}
	for _, r := range f.PlatformRules {
		where := "platform_rules " + r.ID
		if r.ID == "" || seen[r.ID] {
			add("%s: id missing or duplicated", where)
		}
		seen[r.ID] = true
		if r.Outcome != "block" {
			add("%s: platform rules are stricter limits and may only block", where)
		}
		reasonOK(where, r.Reason)
		checkVals(where+" mode", r.Mode, validMode)
		pr := platRule{id: r.ID, category: r.AppliesTo.Category, names: r.AppliesTo.Names, modes: r.Mode, reason: r.Reason}
		if pr.category != "" && pr.category != catProhibited && pr.category != catUnclassified {
			add("%s: applies_to %q is not prohibited, unclassified or a list of drug names", where, pr.category)
		}
		if pr.category == "" && len(pr.names) == 0 {
			add("%s: applies_to is required", where)
		}
		e.plat = append(e.plat, pr)
	}

	got := map[string]bool{}
	for _, ch := range f.PrescriptionChecks {
		if !slices.Contains(requiredChecks, ch.Check) {
			add("prescription_checks %s: unknown check %q", ch.ID, ch.Check)
			continue
		}
		if got[ch.Check] {
			add("prescription_checks: duplicate check %q", ch.Check)
		}
		got[ch.Check] = true
		reasonOK("prescription_checks "+ch.ID, ch.Reason)
		e.checks = append(e.checks, checkRule{id: ch.ID, check: ch.Check, reason: ch.Reason})
	}
	for _, need := range requiredChecks {
		if !got[need] {
			add("prescription_checks: required check %q is missing", need)
		}
	}
	if len(f.Reasons) == 0 {
		add("reasons is required")
	}
	return errors.Join(errs...)
}

func (e *Engine) reason(code string) Reason {
	if r, ok := e.f.Reasons[code]; ok && r.Key != "" {
		return Reason{Code: code, Key: r.Key}
	}
	if k, ok := engineReasonKeys[code]; ok {
		return Reason{Code: code, Key: k}
	}
	return Reason{Code: code, Key: engineReasonKeys[CodeInternalError]}
}
