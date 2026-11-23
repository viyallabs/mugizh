package scrub

import "testing"

// Review of S-189: identity and phone numbers are found in other scripts' digits and across
// line breaks everywhere, and across short punctuation in the strict checks.
func TestDigitHardening(t *testing.T) {
	names := []string{"verhoeff_12_digit_first_2_to_9"}
	id := "234567890124" // a Verhoeff-valid synthetic test number, first digit 2
	if !ContainsProhibitedID(id, names) {
		t.Fatal("precondition: the synthetic number is Verhoeff-valid")
	}
	dev := map[rune]rune{'0': '०', '1': '१', '2': '२', '3': '३', '4': '४', '5': '५', '6': '६', '7': '७', '8': '८', '9': '९'}
	var devID []rune
	for _, r := range id {
		devID = append(devID, dev[r])
	}
	for _, s := range []string{string(devID), "2345\n6789\n0124", "2345\t6789 0124"} {
		if !ContainsProhibitedID(s, names) {
			t.Errorf("normal check misses %q", s)
		}
	}
	for _, s := range []string{"2345.6789.0124", "2345/6789/0124", "2345,6789,0124", "(2345)6789-0124"} {
		if ContainsProhibitedID(s, names) {
			t.Errorf("the normal check must not join punctuation (dates): %q", s)
		}
		if !ContainsProhibitedIDStrict(s, names) {
			t.Errorf("strict check misses %q", s)
		}
	}
	for _, s := range []string{"98765.43210", "(98765)43210", "98765\n43210", "९८७६५४३२१०"} {
		if !ContainsPhoneNumberStrict(s) {
			t.Errorf("strict phone check misses %q", s)
		}
	}
	for _, s := range []string{"Visit on 23.11.2026, 10.30 am, room 12", "Mon-Fri 09:00-13:00", "Paid 500, waited 20 min"} {
		if ContainsProhibitedIDStrict(s, names) || ContainsPhoneNumberStrict(s) {
			t.Errorf("ordinary text refused: %q", s)
		}
	}
}
