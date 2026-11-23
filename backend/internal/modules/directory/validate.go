package directory

import (
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Problems returned to clients. Details never carry user input.
var (
	errInvalid       = problem.New(http.StatusBadRequest, "directory.invalid", "The request is not valid")
	errNotFound      = problem.NotFound
	errPracticeDup   = problem.New(http.StatusConflict, "directory.practice_exists", "This doctor already has a practice at this clinic branch")
	errAddressVague  = problem.New(http.StatusUnprocessableEntity, "directory.address_not_located", "The address could not be located precisely; place a pin on the map")
	errGeocodeDown   = problem.New(http.StatusServiceUnavailable, "directory.geocoding_unavailable", "Address lookup is unavailable; place a pin on the map")
	errPinUnknown    = problem.New(http.StatusUnprocessableEntity, "directory.postal_code_unknown", "That postal code is not known")
	errNoAvailabilty = problem.New(http.StatusUnprocessableEntity, "directory.availability_filter_unavailable", "Filtering by next available slot is not available yet")
	errRoleAssign    = problem.New(http.StatusServiceUnavailable, "directory.role_assignment_failed", "The clinic could not be set up; try again")
)

// Field error codes.
const (
	codeRequired  = "required"
	codeFormat    = "invalid_format"
	codeRange     = "out_of_range"
	codeClaim     = "claim_not_allowed"          // CD-06
	codeIDNumber  = "id_number_not_allowed"      // K-DP-05
	codeLink      = "link_or_markup_not_allowed" // TM-27
	codeSpecialty = "specialty_not_recognised"   // RG-04
	codeNotFound  = "not_found"
)

type validation struct{ fields []problem.FieldError }

func (v *validation) add(field, code string) {
	v.fields = append(v.fields, problem.FieldError{Field: field, Code: code})
}

func (v *validation) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return errInvalid.WithFields(v.fields...)
}

// claimFilter rejects superlative, award and success-rate claims (CD-06). The word list is
// configuration.
type claimFilter struct{ phrases [][]string }

var (
	rePercent  = regexp.MustCompile(`[0-9]\s*%`)
	reRankOne  = regexp.MustCompile(`#\s*1\b`)
	reNonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func tokens(s string) []string {
	return strings.Fields(reNonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

func newClaimFilter(words []string) *claimFilter {
	f := &claimFilter{}
	for _, w := range words {
		if t := tokens(w); len(t) > 0 {
			f.phrases = append(f.phrases, t)
		}
	}
	return f
}

// violates reports whether s contains a listed phrase as whole words, a percentage, or "#1".
func (f *claimFilter) violates(s string) bool {
	low := strings.ToLower(s)
	if rePercent.MatchString(low) || reRankOne.MatchString(low) {
		return true
	}
	t := tokens(s)
	for _, p := range f.phrases {
	scan:
		for i := 0; i+len(p) <= len(t); i++ {
			for j := range p {
				if t[i+j] != p[j] {
					continue scan
				}
			}
			return true
		}
	}
	return false
}

// linkCheck picks the link rule for a field (TM-27). Names reach SMS and push variables
// (doctor name, clinic and branch name), so a bare "label.tld" is refused there; other
// fields (degree, address, timings) only show on the profile and use the loose rule, so
// dotted abbreviations such as "M.Ch" or "Opp.Bus Stand" are accepted.
func linkCheck(field string) func(string) bool {
	if field == "displayName" || field == "name" {
		return scrub.HasLinkOrMarkup
	}
	return scrub.HasLinkOrMarkupLoose
}

// text checks one free-text field: length, no control characters, no prohibited ID
// number (K-DP-05, TC-20) and, when claims is true, no marketing claims (CD-06).
func (m *Module) text(v *validation, field, s string, minLen, maxLen int, claims bool) string {
	s = strings.TrimSpace(s)
	n := len([]rune(s))
	switch {
	case n == 0 && minLen > 0:
		v.add(field, codeRequired)
	case n < minLen || n > maxLen:
		v.add(field, codeRange)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		v.add(field, codeFormat)
	case linkCheck(field)(s):
		v.add(field, codeLink)
	case scrub.ContainsProhibitedID(s, m.cfg.ProhibitedIDValidators):
		v.add(field, codeIDNumber)
	case claims && m.claims.violates(s):
		v.add(field, codeClaim)
	}
	return s
}

var (
	reSpecialty = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	reRef       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`) // opaque upload IDs (UUIDs)
	reHFR       = regexp.MustCompile(`^[A-Za-z0-9\-]{1,30}$`)
	reLanguage  = regexp.MustCompile(`^[\p{L}\p{N} _\-]{2,40}$`)
	reUUID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func validPoint(lat, lng float64) bool {
	return finite(lat) && finite(lng) && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// modes checks the consult modes: at least one, known, no duplicates.
func modes(v *validation, field string, in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, x := range in {
		if !validModes[x] || seen[x] {
			v.add(field, codeFormat)
			return in
		}
		seen[x] = true
		out = append(out, x)
	}
	if len(out) == 0 {
		v.add(field, codeRequired)
	}
	return out
}

func (m *Module) fee(v *validation, field string, amount int64) {
	if amount < 0 || amount > m.cfg.MaxFeeMinor {
		v.add(field, codeRange)
	}
}
