package refundpolicy_test

import (
	"errors"
	"os"
	"testing"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
)

const packFile = "../../../../../config/country-packs/IN/payments/refund-policy.yaml"

func load(t *testing.T) *rp.Policy {
	t.Helper()
	b, err := os.ReadFile(packFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err := rp.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ip(n int) *int { return &n }

// S-133: one table test per rule; output = refund amount + rule ID.
func TestS133_EveryRule(t *testing.T) {
	p := load(t)
	cases := []struct {
		name    string
		in      rp.Input
		rule    string
		refund  int64
		blocked bool
	}{
		{"RF01 debited but booking failed", rp.Input{Trigger: rp.PaymentDebitedBookingFailed, Mode: rp.Online, PaidMinor: 50000}, "RF01", 50000, false},
		{"RF01 in person, mode irrelevant", rp.Input{Trigger: rp.PaymentDebitedBookingFailed, Mode: rp.InPerson, PaidMinor: 50000}, "RF01", 50000, false},
		{"RF02 duplicate payment", rp.Input{Trigger: rp.DuplicatePayment, Mode: rp.InPerson, PaidMinor: 30000}, "RF02", 30000, false},
		{"RF03 doctor not joined online", rp.Input{Trigger: rp.DoctorNotJoined, Mode: rp.Online, PaidMinor: 40000}, "RF03", 40000, false},
		{"RF04 clinic cancelled in person", rp.Input{Trigger: rp.ClinicOrDoctorCancelled, Mode: rp.InPerson, PaidMinor: 40000}, "RF04", 40000, false},
		{"RF04 doctor cancelled online", rp.Input{Trigger: rp.ClinicOrDoctorCancelled, Mode: rp.Online, PaidMinor: 40000}, "RF04", 40000, false},
		{"RF05 technical failure online", rp.Input{Trigger: rp.TechnicalFailureRecorded, Mode: rp.Online, PaidMinor: 40000}, "RF05", 40000, false},
		{"RF06 converted to in person: 50 percent", rp.Input{Trigger: rp.ConvertedToInPerson, Mode: rp.Online, PaidMinor: 40000}, "RF06", 20000, false},
		{"RF06 rounds up for odd paise", rp.Input{Trigger: rp.ConvertedToInPerson, Mode: rp.Online, PaidMinor: 40001}, "RF06", 20001, false},
		{"RF07 cancel exactly 120 minutes before", rp.Input{Trigger: rp.PatientCancelled, Mode: rp.Online, PaidMinor: 40000, MinutesPrior: 120}, "RF07", 40000, false},
		{"RF07 cancel long before", rp.Input{Trigger: rp.PatientCancelled, Mode: rp.InPerson, PaidMinor: 40000, MinutesPrior: 5000}, "RF07", 40000, false},
		{"RF08 late cancel is blocked", rp.Input{Trigger: rp.PatientCancelled, Mode: rp.Online, PaidMinor: 40000, MinutesPrior: 119}, "RF08", 0, true},
		{"RF09 no-show is blocked", rp.Input{Trigger: rp.PatientNoShow, Mode: rp.InPerson, PaidMinor: 40000}, "RF09", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := p.Decide(c.in)
			if c.blocked {
				if !errors.Is(err, rp.ErrBlocked) || d.RuleID != c.rule || d.RefundMinor != 0 {
					t.Fatalf("want blocked %s, got %+v err=%v", c.rule, d, err)
				}
				return
			}
			if err != nil || d.RuleID != c.rule || d.RefundMinor != c.refund {
				t.Fatalf("got %+v err=%v, want %s %d", d, err, c.rule, c.refund)
			}
		})
	}
}

// Rules that apply to online consults only must not match an in-person or unknown mode (fail closed).
func TestS133_OnlineOnlyRulesFailClosed(t *testing.T) {
	p := load(t)
	for _, m := range []rp.Mode{rp.InPerson, rp.Unknown} {
		for _, tr := range []rp.Trigger{rp.DoctorNotJoined, rp.TechnicalFailureRecorded, rp.ConvertedToInPerson} {
			if _, err := p.Decide(rp.Input{Trigger: tr, Mode: m, PaidMinor: 100}); !errors.Is(err, rp.ErrNoRule) {
				t.Fatalf("%s/%q: want ErrNoRule, got %v", tr, m, err)
			}
		}
	}
}

// RF08 and RF09 are refused even if a file lists them without a blocked_by marker.
func TestS133_RF08RF09RefusedEvenIfUnblockedInFile(t *testing.T) {
	src := `
policy_id: X
version: 9.9.9
status: approved
currency: XXX
clinic_override: more_generous_only
windows: { free_cancel_before_minutes: 60 }
rules:
  - { id: RF07, trigger: patient_cancelled, before_minutes_gte: 60, applies_to: any, refund_pct: 100, auto: true }
  - { id: RF08, trigger: patient_cancelled, before_minutes_lt: 60, applies_to: any, refund_pct: 0, auto: false }
  - { id: RF09, trigger: patient_no_show, applies_to: any, refund_pct: 0, auto: false }
`
	p, err := rp.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []rp.Input{
		{Trigger: rp.PatientCancelled, PaidMinor: 100, MinutesPrior: 10},
		{Trigger: rp.PatientNoShow, PaidMinor: 100},
	} {
		if _, err := p.Decide(in); !errors.Is(err, rp.ErrBlocked) {
			t.Fatalf("want ErrBlocked, got %v", err)
		}
	}
}

func TestS133_ClinicOverrideOnlyMoreGenerous(t *testing.T) {
	p := load(t)
	// Shorter window (60 min) is more generous: a cancel 90 minutes before is now free.
	d, err := p.Decide(rp.Input{Trigger: rp.PatientCancelled, Mode: rp.Online, PaidMinor: 1000, MinutesPrior: 90,
		Override: &rp.Override{FreeCancelBeforeMinutes: ip(60)}})
	if err != nil || d.RuleID != "RF07" || d.RefundMinor != 1000 {
		t.Fatalf("got %+v %v", d, err)
	}
	// Still inside the clinic's own window: the late rule (blocked) applies.
	if _, err := p.Decide(rp.Input{Trigger: rp.PatientCancelled, Mode: rp.Online, PaidMinor: 1000, MinutesPrior: 30,
		Override: &rp.Override{FreeCancelBeforeMinutes: ip(60)}}); !errors.Is(err, rp.ErrBlocked) {
		t.Fatalf("want blocked, got %v", err)
	}
	// A higher refund percentage on RF06 is allowed.
	d, err = p.Decide(rp.Input{Trigger: rp.ConvertedToInPerson, Mode: rp.Online, PaidMinor: 1000,
		Override: &rp.Override{RefundPct: map[string]int{"RF06": 80}}})
	if err != nil || d.RefundMinor != 800 {
		t.Fatalf("got %+v %v", d, err)
	}
	// Stricter overrides are refused.
	for _, o := range []*rp.Override{
		{FreeCancelBeforeMinutes: ip(240)},
		{RefundPct: map[string]int{"RF06": 10}},
		{RefundPct: map[string]int{"RF03": 99}},
		{RefundPct: map[string]int{"RF99": 100}},
	} {
		if _, err := p.Decide(rp.Input{Trigger: rp.DoctorNotJoined, Mode: rp.Online, PaidMinor: 1000, Override: o}); !errors.Is(err, rp.ErrOverrideStricter) {
			t.Fatalf("override %+v: want ErrOverrideStricter, got %v", o, err)
		}
	}
}

func TestS133_ParseFailsClosed(t *testing.T) {
	for name, src := range map[string]string{
		"empty":             "",
		"no rules":          "policy_id: A\nversion: 1\ncurrency: X\nclinic_override: more_generous_only\nwindows: {free_cancel_before_minutes: 1}\nrules: []\n",
		"strict override":   "policy_id: A\nversion: 1\ncurrency: X\nclinic_override: anything\nwindows: {free_cancel_before_minutes: 1}\nrules:\n  - {id: RF01, trigger: duplicate_payment, applies_to: any, refund_pct: 100}\n",
		"unknown trigger":   "policy_id: A\nversion: 1\ncurrency: X\nclinic_override: more_generous_only\nwindows: {free_cancel_before_minutes: 1}\nrules:\n  - {id: RF01, trigger: made_up, applies_to: any, refund_pct: 100}\n",
		"pct over 100":      "policy_id: A\nversion: 1\ncurrency: X\nclinic_override: more_generous_only\nwindows: {free_cancel_before_minutes: 1}\nrules:\n  - {id: RF01, trigger: duplicate_payment, applies_to: any, refund_pct: 101}\n",
		"duplicate rule id": "policy_id: A\nversion: 1\ncurrency: X\nclinic_override: more_generous_only\nwindows: {free_cancel_before_minutes: 1}\nrules:\n  - {id: RF01, trigger: duplicate_payment, applies_to: any, refund_pct: 100}\n  - {id: RF01, trigger: duplicate_payment, applies_to: any, refund_pct: 100}\n",
	} {
		if _, err := rp.Parse([]byte(src)); !errors.Is(err, rp.ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestS133_PackPolicyVersion(t *testing.T) {
	p := load(t)
	if p.Version == "" || p.ID != "IN-REFUND" {
		t.Fatalf("unexpected policy %+v", p)
	}
	if tr, ok := p.RuleTrigger("RF03"); !ok || tr != rp.DoctorNotJoined {
		t.Fatal("RuleTrigger")
	}
}
