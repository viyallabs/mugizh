// Package scrub removes personal data patterns from text before it reaches logs, error
// reports or metrics (K-DP-04, K-DP-05, audit-logging-design §3, AAD-03).
//
// It is the second line of defence. The first is the logging allow-list (package
// logging): only known keys are written, and messages must be event names.
package scrub

import (
	"regexp"
	"strings"
	"sync/atomic"
)

const marker = "[REDACTED]"

// Validator decides whether a digit string is a prohibited identifier. The country pack
// names which validators apply (people.prohibited_identifiers); the algorithms are
// generic and live here. Until a pack is loaded, every registered validator applies,
// because redacting too much is harmless and redacting too little is a breach.
type Validator func(digits string) bool

var validators = map[string]Validator{
	"verhoeff_12_digit_first_2_to_9": func(d string) bool {
		return len(d) == 12 && d[0] >= '2' && d[0] <= '9' && Verhoeff(d)
	},
	"digits_16": func(d string) bool { return len(d) == 16 },
}

// ValidatorByName returns a registered validator.
func ValidatorByName(name string) (Validator, bool) { v, ok := validators[name]; return v, ok }

var (
	reEmail     = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reJWT       = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*`)
	reBearer    = regexp.MustCompile(`(?i)(bearer|token|secret|password|passwd|apikey|api_key|otp)(\s*[:=]\s*|\s+)\S+`)
	reLongHex   = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
	reLongB64   = regexp.MustCompile(`[A-Za-z0-9+/_\-]{40,}={0,2}`)
	reDigitRun  = regexp.MustCompile(`\+?\d[\d \-]{6,22}\d`)
	reShortCode = regexp.MustCompile(`\b\d{4,8}\b`)
)

var hits atomic.Int64

// Hits returns how many redactions happened since start (exported as a metric; any
// increase raises an alert, audit-logging-design §7).
func Hits() int64 { return hits.Load() }

// Count records one redaction made elsewhere (for example a log key that is not on the
// allow-list), so a single alert covers every kind of near-leak.
func Count() { hits.Add(1) }

// Text scrubs a free-text value: emails, tokens, long secrets, phone-like digit runs,
// Aadhaar-like and VID-like numbers.
func Text(s string) string { return scrub(s, false) }

// FreeText is Text plus removal of short standalone numbers (4 to 8 digits), which could
// be OTPs. Used for error strings.
func FreeText(s string) string { return scrub(s, true) }

func scrub(s string, shortCodes bool) string {
	if s == "" {
		return s
	}
	n := 0
	rep := func(re *regexp.Regexp, in string) string {
		return re.ReplaceAllStringFunc(in, func(string) string { n++; return marker })
	}
	out := rep(reJWT, s)
	out = rep(reBearer, out)
	out = rep(reEmail, out)
	out = rep(reLongHex, out)
	out = rep(reLongB64, out)
	out = reDigitRun.ReplaceAllStringFunc(out, func(m string) string {
		d := digitsOnly(m)
		if len(d) >= 10 { // phone numbers, Aadhaar (12), VID (16), card-like numbers
			n++
			return marker
		}
		return m
	})
	if shortCodes {
		out = rep(reShortCode, out)
	}
	if n > 0 {
		hits.Add(int64(n))
	}
	return out
}

// ContainsProhibitedID reports whether s holds a digit sequence that any of the named
// validators flags (for example a Verhoeff-valid 12-digit number). Used to reject input
// (TC-20) as well as to scrub. Separators (space, hyphen) are ignored.
func ContainsProhibitedID(s string, names []string) bool {
	check := func(d string) bool {
		for _, name := range names {
			if v, ok := validators[name]; ok && v(d) {
				return true
			}
		}
		return false
	}
	for _, m := range reDigitRun.FindAllString(s, -1) {
		d := digitsOnly(m)
		if check(d) {
			return true
		}
		// A run may join two numbers ("2345 6789 0124 9876543210"); also test every
		// window of 12 and 16 digits so a prohibited number inside a longer run is caught.
		for _, w := range []int{12, 16} {
			for i := 0; i+w <= len(d) && len(d) > w; i++ {
				if check(d[i : i+w]) {
					return true
				}
			}
		}
	}
	return false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Verhoeff checksum (used by Aadhaar numbers). Generic algorithm; which identifiers it
// guards is set by the country pack.
var (
	vD = [10][10]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
		{3, 4, 0, 1, 2, 8, 9, 5, 6, 7}, {4, 0, 1, 2, 3, 9, 5, 6, 7, 8}, {5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
		{6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3}, {8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
		{9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
	}
	vP = [8][10]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
		{8, 9, 1, 6, 0, 4, 3, 5, 2, 7}, {9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
		{2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8},
	}
)

// Verhoeff reports whether the digit string has a valid Verhoeff check digit.
func Verhoeff(d string) bool {
	if d == "" {
		return false
	}
	c := 0
	for i := 0; i < len(d); i++ {
		ch := d[len(d)-1-i]
		if ch < '0' || ch > '9' {
			return false
		}
		c = vD[c][vP[i%8][int(ch-'0')]]
	}
	return c == 0
}
