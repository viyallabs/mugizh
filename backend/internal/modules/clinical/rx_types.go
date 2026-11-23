package clinical

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// ---- consumer interfaces (other modules implement them; Opus wires them) ----

// ErrConsultNotFound is returned by ConsultInfo for an unknown appointment.
var ErrConsultNotFound = errors.New("clinical: appointment not found")

// DoctorFacts are the prescriber details printed on the prescription (spec section 8).
type DoctorFacts struct {
	Name               string
	Qualification      string
	RegistrationNumber string
	Council            string
}

// ConsultFacts is what clinical needs to know about one appointment. The booking, consult,
// identity and directory modules supply it through one adapter written at wiring time.
// Mode is the consult mode AT SIGNING (E-RX-06): a video call that dropped to audio is audio.
type ConsultFacts struct {
	AppointmentRef string
	PatientRef     string
	DoctorRef      string
	ClinicRef      string

	Mode            rules.Mode
	ConsultAt       time.Time
	PatientTimeZone string // IANA zone (K-ARC-04)
	// PatientCountry is the patient's "I am in India now" declaration for remote consults
	// (P10), as a country code the pack lists. Empty means not declared.
	PatientCountry    string
	Initiator         string
	ExplicitConsent   bool
	IdentityConfirmed bool // P02: the doctor confirmed the patient's identity

	PatientName     string
	PatientAgeYears int
	PatientSex      string

	FollowUp rules.FollowUp
	// PriorDrugRefs are drugs this doctor has already prescribed to this patient. The server
	// uses them, never the client's claim, to decide whether an item is a refill.
	PriorDrugRefs []string

	Doctor DoctorFacts
}

// ConsultInfo returns the facts of an appointment, or ErrConsultNotFound.
type ConsultInfo interface {
	Consult(ctx context.Context, appointmentRef string) (ConsultFacts, error)
}

// T2Granter creates the treating doctor's own-records permission (records.Module.GrantT2).
// It must be idempotent.
type T2Granter interface {
	GrantT2(ctx context.Context, patientRef, doctorRef, clinicRef string) (string, error)
}

// ---- configuration ----

// Config is the module configuration. Country values come from the pack through the caller.
type Config struct {
	// ESignEnabled is true only when the pack's adapters.esign names a vendor. When false,
	// signing returns clinical.esign_unavailable and nothing is issued (P09 fails closed, N5).
	ESignEnabled bool
	// ProhibitedValidators are the pack's identifier validator names (K-DP-05).
	ProhibitedValidators []string
	// ClassificationVersion is the drug classification version in force (pack versions).
	ClassificationVersion string

	AppRef            string // owning app key for scheduler-core activities
	ReminderPurpose   string // purpose consent needed for reminders (P-REMIND)
	ReminderStartHour int    // first reminder hour of the day (local)
	ReminderEndHour   int    // last reminder hour of the day (local)
	MaxTestRepeats    int

	// Notification template keys; an empty key sends nothing.
	SignedTemplateKey  string
	AmendedTemplateKey string

	// Reminder wording. NeutralReminderText names nothing. NamedReminderFormat has one %s
	// for the medicine; it is used only when P-MEDNAMES is on (K-DP-09).
	NeutralReminderText string
	NamedReminderFormat string
	NeutralTestText     string

	// Verification page rate limit per client IP.
	VerifyPerMinute int
	VerifyBurst     int

	// VolumeAlertPerDay: when a doctor's signed prescriptions in the last 24 hours reach this number, an alert is
	// raised once (TM-04: a taken-over doctor account issuing prescriptions). 0 turns the alert off.
	VolumeAlertPerDay int
}

// DefaultConfig returns safe defaults. ESignEnabled is false.
func DefaultConfig() Config {
	return Config{
		AppRef: "core", ReminderPurpose: "P-REMIND", ReminderStartHour: 8, ReminderEndHour: 20, MaxTestRepeats: 52,
		SignedTemplateKey: "prescription.ready", AmendedTemplateKey: "prescription.amended",
		NeutralReminderText: "It is time for your medicine.", NamedReminderFormat: "Time to take %s.",
		NeutralTestText: "You have a health task due.",
		VerifyPerMinute: 30, VerifyBurst: 10, VolumeAlertPerDay: 120, // [CONFIRM] the threshold
	}
}

// ---- caller ----

// Caller is the authenticated doctor or patient, built by the HTTP layer.
type Caller struct {
	UserID     string
	Role       string
	ClinicRef  string
	StepUpAt   *time.Time
	Restricted bool
	RequestID  string
	Client     string
	IPHash     string
}

// ---- content ----

// NoteContent is a consult note (S-139). All four fields are free text written by the doctor.
type NoteContent struct {
	Complaint string `json:"complaint"`
	Findings  string `json:"findings"`
	Diagnosis string `json:"diagnosis"`
	Advice    string `json:"advice"`
}

// DoseInput is a structured amount.
type DoseInput struct {
	Amount float64 `json:"amount"`
	Unit   string  `json:"unit"`
}

// FrequencyInput is "Times per Period".
type FrequencyInput struct {
	Times  int    `json:"times"`
	Period string `json:"period"`
}

// DurationInput is "Value Unit".
type DurationInput struct {
	Value int    `json:"value"`
	Unit  string `json:"unit"`
}

// TaperStepInput is one step of a tapering schedule (E-RX-05).
type TaperStepInput struct {
	Dose      DoseInput      `json:"dose"`
	Frequency FrequencyInput `json:"frequency"`
	Duration  DurationInput  `json:"duration"`
}

// ItemInput is one prescription line exactly as the doctor entered it (K-CS-03). The only
// free-text field is Instructions (E-RX-02).
type ItemInput struct {
	Ref             string           `json:"ref"`
	DrugRef         string           `json:"drugRef"`
	Context         string           `json:"context"`
	ForNewCondition bool             `json:"forNewCondition,omitempty"`
	Dose            DoseInput        `json:"dose"`
	Frequency       FrequencyInput   `json:"frequency"`
	Duration        DurationInput    `json:"duration"`
	Route           string           `json:"route"`
	AsNeeded        bool             `json:"asNeeded,omitempty"`
	Taper           []TaperStepInput `json:"taper,omitempty"`
	Instructions    string           `json:"instructions,omitempty"`
}

// TestInput is a test or scan with a due date or a repeat interval.
type TestInput struct {
	Ref          string `json:"ref"`
	Name         string `json:"name"`
	DueOn        string `json:"dueOn,omitempty"` // YYYY-MM-DD in the patient's zone
	IntervalDays int    `json:"intervalDays,omitempty"`
	Repeats      int    `json:"repeats,omitempty"` // total occurrences when an interval is given
}

// DraftContent is the whole draft prescription.
type DraftContent struct {
	Items      []ItemInput `json:"items"`
	Tests      []TestInput `json:"tests,omitempty"`
	Advice     string      `json:"advice,omitempty"`
	FollowUpOn string      `json:"followUpOn,omitempty"` // YYYY-MM-DD
}

// InvalidError names the invalid field.
type InvalidError struct{ Field, Code string }

func (e *InvalidError) Error() string { return "clinical: invalid " + e.Field + ": " + e.Code }

var (
	reRef   = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,80}$`)
	reDrug  = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,79}$`)
	reToken = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_/%.\-]{0,19}$`)
	reDay   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

const (
	maxText  = 4000
	maxInstr = 500
	maxName  = 100
	maxItems = 100
)

func bad(f, c string) error { return &InvalidError{Field: f, Code: c} }

// checkText rejects over-long text and Aadhaar-like numbers (K-DP-05).
func (r *Rx) checkText(field, s string, limit int) error {
	if len(s) > limit {
		return bad(field, "too_long")
	}
	if s != "" && scrub.ContainsProhibitedID(s, r.cfg.ProhibitedValidators) {
		return bad(field, "prohibited_identifier")
	}
	return nil
}

func validDay(s string) bool {
	if !reDay.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// Validate checks shape only. Missing dose fields are allowed in a draft (the rules engine
// blocks them at check and sign, P07); what is present must be a token, never free text.
func (r *Rx) validateNote(n NoteContent) error {
	for f, v := range map[string]string{"complaint": n.Complaint, "findings": n.Findings, "diagnosis": n.Diagnosis, "advice": n.Advice} {
		if err := r.checkText(f, v, maxText); err != nil {
			return err
		}
	}
	return nil
}

func (r *Rx) validateDraft(d *DraftContent) error {
	if len(d.Items) > maxItems || len(d.Tests) > maxItems {
		return bad("items", "too_many")
	}
	seen := map[string]bool{}
	tok := func(field, v string) error {
		if v != "" && !reToken.MatchString(v) {
			return bad(field, "not_a_token")
		}
		return nil
	}
	for i, it := range d.Items {
		p := fmt.Sprintf("items[%d].", i)
		if !reRef.MatchString(it.Ref) || seen[it.Ref] {
			return bad(p+"ref", "invalid")
		}
		seen[it.Ref] = true
		if !reDrug.MatchString(it.DrugRef) {
			return bad(p+"drugRef", "invalid")
		}
		switch rules.ItemContext(it.Context) {
		case rules.ContextNewDrug, rules.ContextRefill, rules.ContextAddOn:
		default:
			return bad(p+"context", "invalid")
		}
		for f, v := range map[string]string{"dose.unit": it.Dose.Unit, "frequency.period": it.Frequency.Period, "duration.unit": it.Duration.Unit, "route": it.Route} {
			if err := tok(p+f, v); err != nil {
				return err
			}
		}
		for j, s := range it.Taper {
			q := fmt.Sprintf("%staper[%d].", p, j)
			for f, v := range map[string]string{"dose.unit": s.Dose.Unit, "frequency.period": s.Frequency.Period, "duration.unit": s.Duration.Unit} {
				if err := tok(q+f, v); err != nil {
					return err
				}
			}
		}
		if err := r.checkText(p+"instructions", it.Instructions, maxInstr); err != nil {
			return err
		}
	}
	tseen := map[string]bool{}
	for i, t := range d.Tests {
		p := fmt.Sprintf("tests[%d].", i)
		if !reRef.MatchString(t.Ref) || tseen[t.Ref] {
			return bad(p+"ref", "invalid")
		}
		tseen[t.Ref] = true
		if t.Name == "" {
			return bad(p+"name", "required")
		}
		if err := r.checkText(p+"name", t.Name, maxName); err != nil {
			return err
		}
		if (t.DueOn == "") == (t.IntervalDays == 0) {
			return bad(p+"dueOn", "due_date_or_interval")
		}
		if t.DueOn != "" && !validDay(t.DueOn) {
			return bad(p+"dueOn", "invalid")
		}
		if t.IntervalDays != 0 && (t.IntervalDays < 1 || t.IntervalDays > 365 || t.Repeats < 1 || t.Repeats > r.cfg.MaxTestRepeats) {
			return bad(p+"intervalDays", "out_of_range")
		}
	}
	if d.FollowUpOn != "" && !validDay(d.FollowUpOn) {
		return bad("followUpOn", "invalid")
	}
	return r.checkText("advice", d.Advice, maxText)
}

// toRules converts items to engine input. PriorPrescribed comes from the server's history.
func toRules(items []ItemInput, prior map[string]bool) []rules.Item {
	out := make([]rules.Item, 0, len(items))
	for _, it := range items {
		ri := rules.Item{Ref: it.Ref, DrugRef: it.DrugRef, Context: rules.ItemContext(it.Context), PriorPrescribed: prior[it.DrugRef],
			ForNewCondition: it.ForNewCondition,
			Dose:            rules.Dose{Amount: it.Dose.Amount, Unit: it.Dose.Unit}, Frequency: rules.Frequency{Times: it.Frequency.Times, Period: it.Frequency.Period},
			Duration: rules.Duration{Value: it.Duration.Value, Unit: it.Duration.Unit}, Route: it.Route, AsNeeded: it.AsNeeded, Instructions: it.Instructions}
		for _, s := range it.Taper {
			ri.Taper = append(ri.Taper, rules.TaperStep{Dose: rules.Dose{Amount: s.Dose.Amount, Unit: s.Dose.Unit},
				Frequency: rules.Frequency{Times: s.Frequency.Times, Period: s.Frequency.Period}, Duration: rules.Duration{Value: s.Duration.Value, Unit: s.Duration.Unit}})
		}
		out = append(out, ri)
	}
	return out
}

// ---- errors ----

// Service errors, mapped to problems by the HTTP layer.
var (
	ErrNotFound          = errors.New("clinical: not found")
	ErrDoctorNotVerified = errors.New("clinical: RX_DOCTOR_NOT_VERIFIED")
	ErrStepUp            = errors.New("clinical: step-up required")
	ErrESignUnavailable  = errors.New("clinical: electronic signature unavailable")
	ErrESignFailed       = errors.New("clinical: electronic signature did not complete")
	ErrNoDraft           = errors.New("clinical: no draft to sign")
	ErrAlreadySigned     = errors.New("clinical: this version is already signed")
	ErrDraftChanged      = errors.New("clinical: the draft changed while signing")
	ErrOpenDraft         = errors.New("clinical: an unsigned draft already exists")
	ErrNothingToAmend    = errors.New("clinical: no signed version to amend")
	ErrRateLimited       = errors.New("clinical: rate limited")
	// ErrPatientDetails: the patient's name or age is not available (profile missing or not
	// confirmed), so no prescription document can be signed (prescription format).
	ErrPatientDetails = errors.New("clinical: patient name or age missing")
)

// DeniedError is a refusal by the records gate. Hide means answer 404 (K-SEC-04).
type DeniedError struct {
	Hide   bool
	Reason string
}

func (e *DeniedError) Error() string { return "clinical: access denied: " + e.Reason }

// BlockedError is a rules-engine refusal (fail closed). Result holds the per-item reasons.
type BlockedError struct{ Result rules.Result }

func (e *BlockedError) Error() string { return "clinical: blocked by the prescribing rules" }
