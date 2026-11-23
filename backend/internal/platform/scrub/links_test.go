package scrub

import "testing"

func TestHasLinkOrMarkup(t *testing.T) {
	bad := []string{
		"http://x", "https://x.y", "HTTPS://EVIL", "ftp://a", "custom+x://a", "javascript:alert(1)", "data:text/html,x",
		"see www.x", "WWW.x", "a@b", "x@y.com", "bit.ly/abc", "t.me/abc", "evil.org", "a.io", "xn--p1ai", "foo.xn--p1ai",
		"Clinic evil.org now", "<script>", "a>b", "{x}", "{other}", "[x]", `a\b`, `say "hi"`, "back`tick", "mailto:a",
		"tinyurl.com/x", "my-clinic.in", "Clinic.Near",
	}
	for _, s := range bad {
		if !HasLinkOrMarkup(s) {
			t.Errorf("expected hit: %q", s)
		}
	}
	good := []string{
		"Dr. A. Kumar", "St. John's Clinic", "Sector 5, Phase 2", "M.B.B.S.", "R.K. Nagar", "Dr. R.K. Nagar, M.D.",
		"Apollo Clinic (Main)", "Anna Nagar - 2nd Street", "Dr. Priya", "Tab. Paracetamol 500 mg", "Vitamin B.12",
		"No. 5, 3rd Cross.", "Metadata review", "Ward 2.", "Mary-Ann Clinic", "Dr. S. Ravi Kumar, MBBS, M.D. (Gen. Med.)",
		"12/B, Gandhi Rd.", "Smt. Lakshmi Hospital", "", "Priya Sharma", "Kumar & Sons", "முருகன் மருத்துவமனை",
	}
	for _, s := range good {
		if HasLinkOrMarkup(s) {
			t.Errorf("unexpected hit: %q", s)
		}
	}
}

// Fields that never reach a message accept dotted abbreviations (qualifications,
// addresses) but still refuse links.
func TestHasLinkOrMarkupLoose(t *testing.T) {
	for _, ok := range []string{"MBBS, M.Ch (Urology)", "M.D. (Gen.Med.)", "D.Ortho", "Opp.Bus Stand, Nr.Temple Rd.", "No.12, 3rd Main", "Mon-Fri 09:00-13:00"} {
		if HasLinkOrMarkupLoose(ok) {
			t.Errorf("%q must pass the loose check", ok)
		}
	}
	for _, bad := range []string{"see bit.ly/x", "t.me/abc", "https://x.y", "www.evil", "a@b.c", "<b>", "{x}", "javascript:alert(1)"} {
		if !HasLinkOrMarkupLoose(bad) {
			t.Errorf("%q must fail the loose check", bad)
		}
	}
	if !HasLinkOrMarkup("M.Ch") {
		t.Error("the strict check (names) still refuses a bare domain-like token")
	}
}
