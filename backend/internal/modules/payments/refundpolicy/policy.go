// Package refundpolicy is the refund policy engine (S-133, K-PAY-05). It is a PURE
// function over a versioned policy file from the country pack: the same input always
// gives the same refund amount and rule ID, and nothing is read from code constants.
//
//   - "Policy shown before payment equals policy applied": the caller loads the policy
//     version stored with the booking and applies exactly that.
//   - Rules RF08 and RF09 (late cancellation, no-show) are blocked until FAIRNESS-01 is
//     decided. They are refused even when a file lists them as unblocked: any rule that
//     names blocked_by, and these two IDs regardless, return ErrBlocked.
//   - A clinic override may only be MORE generous (v1.0). A stricter one is refused.
package refundpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// Trigger is what happened to the booking.
type Trigger string

// Triggers, as named in the policy file.
const (
	PaymentDebitedBookingFailed Trigger = "payment_debited_booking_failed"
	DuplicatePayment            Trigger = "duplicate_payment"
	DoctorNotJoined             Trigger = "doctor_not_joined"
	ClinicOrDoctorCancelled     Trigger = "clinic_or_doctor_cancelled"
	TechnicalFailureRecorded    Trigger = "technical_failure_recorded"
	ConvertedToInPerson         Trigger = "converted_to_in_person"
	PatientCancelled            Trigger = "patient_cancelled"
	PatientNoShow               Trigger = "patient_no_show"
)

// Mode of the appointment.
type Mode string

// Modes. Unknown is used when the caller does not know: only "any" rules then match.
const (
	Online   Mode = "online"
	InPerson Mode = "in_person"
	Unknown  Mode = ""
)

// Errors.
var (
	// ErrBlocked: the matching rule is blocked (FAIRNESS-01). No refund decision is made.
	ErrBlocked = errors.New("refundpolicy: rule is blocked pending FAIRNESS-01")
	// ErrNoRule: no rule matches (fail closed, never guess).
	ErrNoRule = errors.New("refundpolicy: no rule matches")
	// ErrOverrideStricter: a clinic override must be more generous only.
	ErrOverrideStricter = errors.New("refundpolicy: clinic override is stricter than the policy")
	// ErrInvalid: bad policy file or bad input.
	ErrInvalid = errors.New("refundpolicy: invalid")
)

// blockedIDs are always refused, whatever the file says (K-PAY-05, FAIRNESS-01).
var blockedIDs = map[string]bool{"RF08": true, "RF09": true}

// Rule is one policy rule.
type Rule struct {
	ID              string
	Trigger         Trigger
	AppliesTo       string // any | online | in_person
	RefundPct       int
	Auto            bool
	Alt             string
	BeforeMinutesGE *int
	BeforeMinutesLT *int
	BlockedBy       string
}

// Blocked reports whether the rule may not be applied.
func (r Rule) Blocked() bool { return r.BlockedBy != "" || blockedIDs[r.ID] }

// Windows are the policy's time windows.
type Windows struct {
	FreeCancelBeforeMinutes int
}

// Policy is a parsed, validated policy version.
type Policy struct {
	ID       string
	Version  string
	Status   string
	Currency string
	Windows  Windows
	Rules    []Rule
	// Override is the policy-level rule for clinic overrides ("more_generous_only").
	OverrideMode string
}

type file struct {
	PolicyID string `yaml:"policy_id"`
	Version  string `yaml:"version"`
	Status   string `yaml:"status"`
	Currency string `yaml:"currency"`
	Override string `yaml:"clinic_override"`
	Windows  struct {
		FreeCancel int `yaml:"free_cancel_before_minutes"`
	} `yaml:"windows"`
	Rules []struct {
		ID        string `yaml:"id"`
		Trigger   string `yaml:"trigger"`
		AppliesTo string `yaml:"applies_to"`
		Pct       *int   `yaml:"refund_pct"`
		Auto      bool   `yaml:"auto"`
		Alt       string `yaml:"alt"`
		GE        *int   `yaml:"before_minutes_gte"`
		LT        *int   `yaml:"before_minutes_lt"`
		BlockedBy string `yaml:"blocked_by"`
	} `yaml:"rules"`
}

var (
	reVersion = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,40}$`)
	reRuleID  = regexp.MustCompile(`^RF[0-9]{2}$`)
)

var knownTriggers = map[Trigger]bool{
	PaymentDebitedBookingFailed: true, DuplicatePayment: true, DoctorNotJoined: true, ClinicOrDoctorCancelled: true,
	TechnicalFailureRecorded: true, ConvertedToInPerson: true, PatientCancelled: true, PatientNoShow: true,
}

// Parse reads a policy file from the country pack. It fails closed on anything unknown.
func Parse(b []byte) (*Policy, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if f.PolicyID == "" || !reVersion.MatchString(f.Version) || f.Currency == "" || len(f.Rules) == 0 {
		return nil, fmt.Errorf("%w: policy id, version, currency and rules are required", ErrInvalid)
	}
	if f.Override != "more_generous_only" {
		return nil, fmt.Errorf("%w: clinic_override must be more_generous_only in v1.0", ErrInvalid)
	}
	if f.Windows.FreeCancel <= 0 {
		return nil, fmt.Errorf("%w: free_cancel_before_minutes", ErrInvalid)
	}
	p := &Policy{ID: f.PolicyID, Version: f.Version, Status: f.Status, Currency: f.Currency, OverrideMode: f.Override,
		Windows: Windows{FreeCancelBeforeMinutes: f.Windows.FreeCancel}}
	seen := map[string]bool{}
	for _, r := range f.Rules {
		t := Trigger(r.Trigger)
		if !reRuleID.MatchString(r.ID) || seen[r.ID] || !knownTriggers[t] || r.Pct == nil || *r.Pct < 0 || *r.Pct > 100 {
			return nil, fmt.Errorf("%w: rule %q", ErrInvalid, r.ID)
		}
		if r.AppliesTo != "any" && r.AppliesTo != "online" && r.AppliesTo != "in_person" {
			return nil, fmt.Errorf("%w: rule %s applies_to", ErrInvalid, r.ID)
		}
		seen[r.ID] = true
		p.Rules = append(p.Rules, Rule{ID: r.ID, Trigger: t, AppliesTo: r.AppliesTo, RefundPct: *r.Pct, Auto: r.Auto, Alt: r.Alt,
			BeforeMinutesGE: r.GE, BeforeMinutesLT: r.LT, BlockedBy: r.BlockedBy})
	}
	return p, nil
}

// Override is a clinic's own setting. Nil fields mean "use the policy".
type Override struct {
	// FreeCancelBeforeMinutes is the clinic's free-cancellation window; it must be
	// SHORTER than or equal to the policy's (a shorter window is more generous).
	FreeCancelBeforeMinutes *int
	// RefundPct raises the refund percentage of a rule (by ID); it must be >= the policy's.
	RefundPct map[string]int
}

// Validate checks that the override is not stricter than the policy.
func (p *Policy) Validate(o *Override) error {
	if o == nil {
		return nil
	}
	if o.FreeCancelBeforeMinutes != nil && (*o.FreeCancelBeforeMinutes < 0 || *o.FreeCancelBeforeMinutes > p.Windows.FreeCancelBeforeMinutes) {
		return ErrOverrideStricter
	}
	for id, pct := range o.RefundPct {
		r, ok := p.rule(id)
		if !ok || pct > 100 || pct < r.RefundPct {
			return ErrOverrideStricter
		}
	}
	return nil
}

func (p *Policy) rule(id string) (Rule, bool) {
	for _, r := range p.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// RuleTrigger returns the trigger a rule ID is for.
func (p *Policy) RuleTrigger(id string) (Trigger, bool) {
	r, ok := p.rule(id)
	return r.Trigger, ok
}

// Input is everything the decision depends on.
type Input struct {
	Trigger      Trigger
	Mode         Mode
	PaidMinor    int64
	MinutesPrior int // minutes before the slot start (patient_cancelled only)
	Override     *Override
}

// Decision is the engine output: refund amount and rule ID.
type Decision struct {
	RuleID      string
	RefundMinor int64
	Pct         int
	Auto        bool
	Alt         string
}

// Decide applies the first matching rule. A blocked rule returns ErrBlocked (and a
// Decision carrying its ID so the caller can record why). No match returns ErrNoRule.
func (p *Policy) Decide(in Input) (Decision, error) {
	if in.PaidMinor <= 0 || !knownTriggers[in.Trigger] || in.MinutesPrior < 0 {
		return Decision{}, fmt.Errorf("%w: input", ErrInvalid)
	}
	if err := p.Validate(in.Override); err != nil {
		return Decision{}, err
	}
	eff := p.Windows.FreeCancelBeforeMinutes
	if in.Override != nil && in.Override.FreeCancelBeforeMinutes != nil {
		eff = *in.Override.FreeCancelBeforeMinutes
	}
	for _, r := range p.Rules {
		if r.Trigger != in.Trigger || !modeMatches(r.AppliesTo, in.Mode) {
			continue
		}
		if r.BeforeMinutesGE != nil && in.MinutesPrior < threshold(*r.BeforeMinutesGE, p.Windows.FreeCancelBeforeMinutes, eff) {
			continue
		}
		if r.BeforeMinutesLT != nil && in.MinutesPrior >= threshold(*r.BeforeMinutesLT, p.Windows.FreeCancelBeforeMinutes, eff) {
			continue
		}
		if r.Blocked() {
			return Decision{RuleID: r.ID}, fmt.Errorf("%w: %s", ErrBlocked, r.ID)
		}
		pct := r.RefundPct
		if in.Override != nil {
			if o, ok := in.Override.RefundPct[r.ID]; ok && o > pct {
				pct = o
			}
		}
		return Decision{RuleID: r.ID, Pct: pct, RefundMinor: share(in.PaidMinor, pct), Auto: r.Auto, Alt: r.Alt}, nil
	}
	return Decision{}, ErrNoRule
}

// threshold swaps the policy's free-cancel window for the clinic's, wherever a rule uses it.
func threshold(v, policyWindow, effective int) int {
	if v == policyWindow {
		return effective
	}
	return v
}

func modeMatches(appliesTo string, m Mode) bool {
	switch appliesTo {
	case "any":
		return true
	case "online":
		return m == Online
	case "in_person":
		return m == InPerson
	}
	return false
}

// share returns pct percent of paid in minor units, rounded UP (in the patient's favour)
// and never more than paid. Integer arithmetic only.
func share(paid int64, pct int) int64 {
	if pct >= 100 {
		return paid
	}
	n := paid * int64(pct)
	out := n / 100
	if n%100 != 0 {
		out++
	}
	if out > paid {
		out = paid
	}
	return out
}
