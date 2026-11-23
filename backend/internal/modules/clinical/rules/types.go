package rules

import "time"

// Mode is the consult mode in effect when the prescription is signed (E-RX-06): a video
// call that dropped to audio before signing is audio.
type Mode string

const (
	ModeVideo    Mode = "video"
	ModeAudio    Mode = "audio"
	ModeText     Mode = "text"
	ModeInPerson Mode = "in_person"
)

// ConsultType is first or follow-up.
type ConsultType string

const (
	ConsultFirst    ConsultType = "first"
	ConsultFollowUp ConsultType = "follow_up"
)

// ItemContext is how the doctor relates the item to the patient's history with this doctor.
type ItemContext string

const (
	ContextNewDrug ItemContext = "new_drug"
	ContextRefill  ItemContext = "refill"
	ContextAddOn   ItemContext = "add_on"
)

// Stage tells the engine whether the prescription is being drafted or signed. Signing
// runs every check, including step-up (P08) and the electronic signature (P09). The zero
// value is treated as StageSign (the strictest).
type Stage string

const (
	StageDraft Stage = "draft"
	StageSign  Stage = "sign"
)

// Layer a rule hit came from.
type Layer string

const (
	LayerRegulatory Layer = "regulatory"
	LayerPlatform   Layer = "platform"
	LayerCheck      Layer = "check"
	LayerEngine     Layer = "engine"
)

// Dose is a structured amount. Free text is never accepted here (K-CS-03).
type Dose struct {
	Amount float64 // greater than zero
	Unit   string  // token such as "mg"; letters, digits, "_", "/" and "%" only
}

// Frequency is "Times per Period" (for as-needed items, the most the patient may take).
type Frequency struct {
	Times  int
	Period string // token such as "day"
}

// Duration is "Value Unit".
type Duration struct {
	Value int
	Unit  string // token such as "day"
}

// TaperStep is one step of a tapering schedule; every step is as complete as the main dose.
type TaperStep struct {
	Dose      Dose
	Frequency Frequency
	Duration  Duration
}

// Item is one prescription line as entered by the doctor, exactly (K-CS-03).
type Item struct {
	Ref     string // opaque line reference chosen by the caller (letters, digits, "_", "-")
	DrugRef string // drug master reference

	// Context is the doctor's selection. PriorPrescribed is history: this drug was
	// previously prescribed to this patient by this doctor for this condition. A refill
	// the history does not support is treated as a new drug (the stricter reading).
	Context         ItemContext
	PriorPrescribed bool
	// ForNewCondition: in a follow-up, the doctor marks the item as for a new condition;
	// it is then evaluated as a first consult.
	ForNewCondition bool

	Dose      Dose
	Frequency Frequency
	Duration  Duration
	Route     string
	AsNeeded  bool
	Taper     []TaperStep

	// Instructions is the only free-text field. The rules never read it.
	Instructions string
}

// FollowUp carries what the engine needs to decide whether a consult is a follow-up
// (spec section 3). The caller derives the facts from records or the doctor's attestations.
type FollowUp struct {
	Claimed bool // the doctor says this is a follow-up

	SameDoctor     bool
	LastInPersonAt time.Time // instant of the previous in-person consult; zero if none
	Evidence       string    // how it is known: a value of prior_in_person_evidence in the rule file

	// Doctor attestations (audited by the caller).
	SameConditionAttested  bool
	NoNewSymptomsAttested  bool
	RecallsContextAttested bool
}

// Classifier is the engine's read-only view of one drug classification version.
type Classifier interface {
	Version() string
	Lookup(drugRef string) (Drug, bool)
}

// Input is everything the engine needs. It holds no personal data: flags, references and
// structured values only (K-DP-04).
type Input struct {
	// RuleSetVersion is the rule-set version the caller expects; it must equal the loaded one.
	RuleSetVersion string
	Stage          Stage

	Mode      Mode      // mode at signing
	ConsultAt time.Time // instant of the consult (used for the follow-up window)
	// PatientTimeZone is the patient's IANA zone (K-ARC-04); required when a follow-up is claimed.
	PatientTimeZone string
	FollowUp        FollowUp

	// PatientCountry is the patient's declared country now ("I am in India now"), from the
	// booking and pre-join declarations. Empty means not declared (P10).
	PatientCountry string

	Initiator       string // patient, doctor, caregiver, health_worker
	ExplicitConsent bool   // explicit consent recorded (needed when the doctor etc. started it)

	PrescriberVerified bool // P01: registration verified and active (K-CS-01)
	IdentityConfirmed  bool // P02
	PatientAgeYears    int  // P03; 0 = missing
	DiagnosisPresent   bool // P05: a diagnosis or provisional diagnosis exists (the text is not passed in)
	StepUpDone         bool // P08 (input only)
	ESignatureApplied  bool // P09 (input only)

	Classification Classifier
	Items          []Item
}

// Reason is a stable code plus the translation key to show it with (K-GLB-02).
type Reason struct {
	Code string
	Key  string
}

// Hit records one rule that spoke, for the stored evaluation (RET-02).
type Hit struct {
	Layer   Layer
	RuleID  string
	Allowed bool
	Reason  string // code, empty when allowed
}

// CheckResult is one prescription-level check (P01 to P10 except P07, which is per item).
type CheckResult struct {
	ID     string
	Passed bool
	Reason Reason // empty when passed
}

// ItemResult is the decision for one item. It never carries a suggested drug, dose or
// alternative (K-CS-03).
type ItemResult struct {
	Ref                  string
	DrugRef              string
	Allowed              bool
	EffectiveConsultType ConsultType
	ContextUsed          ItemContext
	// Reasons: the first is the primary reason. Empty when allowed.
	Reasons []Reason
	Hits    []Hit
}

// Result is the decision for the whole prescription. Allowed is true only when every
// check passes and every item is allowed. There is no override.
type Result struct {
	Allowed bool

	RuleSetVersion            string
	RuleSetChecksum           string
	DrugClassificationVersion string

	Mode        Mode
	ConsultType ConsultType // follow_up only when established
	Stage       Stage

	Checks  []CheckResult
	Reasons []Reason // failed prescription-level checks
	Items   []ItemResult
}

// Blocked reports whether the prescription is blocked.
func (r Result) Blocked() bool { return !r.Allowed }

// HasReason reports whether code appears at prescription level or on any item.
func (r Result) HasReason(code string) bool {
	for _, x := range r.Reasons {
		if x.Code == code {
			return true
		}
	}
	for _, it := range r.Items {
		for _, x := range it.Reasons {
			if x.Code == code {
				return true
			}
		}
	}
	return false
}
