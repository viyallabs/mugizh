package sms

import (
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
)

const testRegistry = `
templates:
  - id: SMS-01
    category: service_implicit_authentication
    variables: [{ name: otp, type: numeric }]
  - id: SMS-02
    category: service_implicit
    variables: [{ name: doctor_name, type: alphanumeric }, { name: date_time, type: alphanumeric }]
`

func reg(t *testing.T) *Registry {
	t.Helper()
	r, err := ParseRegistry([]byte(testRegistry))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func booking(doctor, when string) Message {
	return Message{To: "+000", Template: "SMS-02", Vars: []Var{{Name: "doctor_name", Value: doctor}, {Name: "date_time", Value: when}}}
}

func kindCode(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestValidateAcceptsOrdinaryVariables(t *testing.T) {
	r := reg(t)
	for _, name := range []string{"Dr Rao", "Dr. A. Kumar", "St. John's Clinic", "R.K. Nagar"} {
		if err := r.Validate(booking(name, "Tue 17 Nov, 10:30 AM")); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	if err := r.Validate(Message{Template: "SMS-01", Vars: []Var{{Name: "otp", Value: "123456"}}}); err != nil {
		t.Fatal(err)
	}
}

// TM-27: links, bare domains, short links, e-mail-like tokens and markup never reach a DLT variable.
func TestValidateRefusesLinksAndMarkup(t *testing.T) {
	r := reg(t)
	for _, p := range []string{"http://x", "https://x.y", "bit.ly/abc", "evil.org", "t.me/abc", "a.io", "www.x", "a@b", "<b>", "{x}", "xn--p1ai"} {
		for i := 0; i < 2; i++ {
			m := booking("Dr Rao", "Tue 17 Nov")
			m.Vars[i].Value = p
			err := r.Validate(m)
			if err == nil {
				t.Errorf("var %d %q accepted", i, p)
			}
		}
	}
}

func TestValidateStructuralRules(t *testing.T) {
	r := reg(t)
	cases := map[string]Message{
		"unknown template": {Template: "SMS-99"},
		"count":            {Template: "SMS-02", Vars: []Var{{Name: "doctor_name", Value: "Dr Rao"}}},
		"name":             {Template: "SMS-02", Vars: []Var{{Name: "date_time", Value: "x"}, {Name: "doctor_name", Value: "y"}}},
		"numeric type":     {Template: "SMS-01", Vars: []Var{{Name: "otp", Value: "12ab"}}},
		"long digit run":   booking("Dr Rao", "call 9876543210"),
	}
	for name, m := range cases {
		err := r.Validate(m)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		if adapters.KindOf(err) != adapters.Invalid {
			t.Errorf("%s: kind %v (%s)", name, adapters.KindOf(err), kindCode(err))
		}
	}
}
