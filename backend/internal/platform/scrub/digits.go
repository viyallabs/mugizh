package scrub

import (
	"strings"
	"unicode"
)

// Digit hardening (review of S-189). Identity and phone numbers must be found however they
// are typed: in other scripts' digits (Devanagari ९८७६…, Arabic-Indic, …) and split across
// lines. prepare maps every Unicode decimal digit to ASCII and turns line breaks and tabs
// into spaces, which the digit-run pattern already treats as separators. It adds no false
// positives: dates and times written with dots, slashes or colons are still not joined.

// prepare normalises digits and whitespace for the digit-run checks.
func prepare(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f':
			return ' '
		case unicode.Is(unicode.Nd, r):
			return '0' + digitValue(r)
		}
		return r
	}, s)
}

// digitValue is the value of a Unicode decimal digit (Nd ranges start at each script's zero).
func digitValue(r rune) rune {
	for _, rg := range unicode.Nd.R16 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return (r - lo) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return (r - lo) % 10
		}
	}
	return 0
}

// digitGroups splits s into groups of digits that are joined across short separators: up to
// maxGap characters that are neither letters nor digits ("98765.43210", "2345/6789/0124",
// "(98765)43210", "2345\n6789\n0124"). A letter or a longer gap ends a group.
func digitGroups(s string, maxGap int) []string {
	var out []string
	var cur strings.Builder
	gap := 0
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		gap = 0
	}
	for _, r := range prepare(s) {
		switch {
		case r >= '0' && r <= '9':
			cur.WriteRune(r)
			gap = 0
		case unicode.IsLetter(r) || r == ' ':
			flush() // a space ends a group; spaced numbers are the normal check's job
		default:
			gap++
			if gap > maxGap {
				flush()
			}
		}
	}
	flush()
	return out
}

// ContainsProhibitedIDStrict is ContainsProhibitedID for free text that staff read or that is
// published (feedback, reviews): digits split by any short punctuation are joined before the
// check. A false positive only asks the writer to rephrase. Clinical and profile fields keep
// ContainsProhibitedID, so dates such as "23.11.2026 10.30" never block a doctor's note.
func ContainsProhibitedIDStrict(s string, names []string) bool {
	if ContainsProhibitedID(s, names) {
		return true
	}
	for _, d := range digitGroups(s, 1) {
		if idInDigits(d, names) {
			return true
		}
	}
	return false
}

// ContainsPhoneNumberStrict is ContainsPhoneNumber with the same joining as
// ContainsProhibitedIDStrict.
func ContainsPhoneNumberStrict(s string) bool {
	if ContainsPhoneNumber(s) {
		return true
	}
	for _, d := range digitGroups(s, 1) {
		if len(d) >= 10 {
			return true
		}
	}
	return false
}
