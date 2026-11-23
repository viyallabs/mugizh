package reviews

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Field codes for refused text (422 with errors[].code). The patient can edit and send again.
const (
	codeInvalid         = "invalid"                 // not valid UTF-8
	codeControl         = "control_characters"      // control or invisible formatting characters
	codeTooLong         = "too_long"                // over the configured length
	codeIdentifier      = "contains_identifier"     // an identity number the pack prohibits (K-DP-05)
	codeContactDetail   = "contains_contact_detail" // a phone number or e-mail address
	codeLinkOrMarkup    = "contains_link_or_markup" // a link, domain or markup (review policy: no links)
	fieldText, fieldRat = "text", "rating"
)

// checkText runs the automatic checks on a review text (already trimmed) and returns the code of
// the first one it fails, or "". The text is never echoed or logged (K-DP-04).
func (m *Module) checkText(s string) string {
	if !utf8.ValidString(s) {
		return codeInvalid
	}
	for _, r := range s {
		// Newlines and tabs are allowed; every other control character and every invisible
		// format character (zero-width, bidirectional override) is refused, because they hide links
		// and numbers from the checks below.
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return codeControl
		}
	}
	if utf8.RuneCountInString(s) > m.cfg.MaxTextRunes {
		return codeTooLong
	}
	if scrub.ContainsProhibitedIDStrict(s, m.cfg.ProhibitedIDValidators) {
		return codeIdentifier
	}
	if scrub.ContainsPhoneNumberStrict(s) || scrub.ContainsEmail(s) {
		return codeContactDetail
	}
	if scrub.HasLinkOrMarkup(s) {
		return codeLinkOrMarkup
	}
	return ""
}

func normaliseText(s string) string { return strings.TrimSpace(s) }
