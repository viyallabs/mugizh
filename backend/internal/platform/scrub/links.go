package scrub

import (
	"regexp"
	"strings"
)

// Link and markup detection for text that can reach an SMS, push or e-mail variable or the
// public profile (threat TM-27, TM-41). It is dependency-free and deliberately strict: a
// refused name can be re-typed, a link in a trusted message cannot be recalled.
//
// Rules (any one is a hit):
//   - a URL scheme: "[a-z][a-z0-9+.-]*://", or a bare javascript:, data:, vbscript:, ftp:,
//     http:, https:, mailto:, tel:, sms:, file: prefix;
//   - "www." or "xn--" (punycode) at a word start;
//   - an e-mail-like "x@y";
//   - a "label.tld" domain: a letter or digit run (hyphens allowed inside) directly followed
//     by a dot and 2 to 24 letters (or a punycode "xn--" label). Space after the dot ends the
//     match, so "Dr. A. Kumar", "St. John's Clinic", "M.B.B.S." and "R.K. Nagar" pass, while
//     "bit.ly/x", "t.me/abc", "evil.org" and "a.io" do not. Consequence: "Dr.Kumar" (no space)
//     is refused; write "Dr. Kumar";
//   - markup characters < > { } [ ] \ " and the backtick.
var (
	reScheme     = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://`)
	reBareScheme = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:javascript|data|vbscript|ftp|https?|mailto|tel|sms|file):`)
	reWWW        = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])www\.`)
	reXN         = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])xn--`)
	reAt         = regexp.MustCompile(`\S@\S`)
	reDomain     = regexp.MustCompile(`(?i)[\p{L}\p{N}][\p{L}\p{N}\-]*\.(?:xn--[a-z0-9\-]+|\p{L}{2,24})`)
)

const markupChars = "<>{}[]\\\"`"

// reDomainPath is a domain followed by a path ("bit.ly/x", "t.me/abc"): the loose check.
var reDomainPath = regexp.MustCompile(`(?i)[\p{L}\p{N}][\p{L}\p{N}\-]*\.(?:xn--[a-z0-9\-]+|\p{L}{2,24})/`)

// HasLinkOrMarkupLoose is HasLinkOrMarkup without the bare "label.tld" rule, for text that
// never reaches a message variable but where dotted abbreviations are normal: Indian
// qualifications ("M.Ch", "M.D. (Gen.Med.)", "D.Ortho") and addresses ("Opp.Bus Stand",
// "Nr.Temple"). Schemes, www., e-mail, markup and a domain with a path are still refused.
func HasLinkOrMarkupLoose(s string) bool {
	if strings.ContainsAny(s, markupChars) {
		return true
	}
	return reScheme.MatchString(s) || reBareScheme.MatchString(s) || reWWW.MatchString(s) || reXN.MatchString(s) ||
		reAt.MatchString(s) || reDomainPath.MatchString(s)
}

// HasLinkOrMarkup reports whether s contains a link, domain, e-mail-like token or markup
// character.
func HasLinkOrMarkup(s string) bool {
	if strings.ContainsAny(s, markupChars) {
		return true
	}
	return reScheme.MatchString(s) || reBareScheme.MatchString(s) || reWWW.MatchString(s) || reXN.MatchString(s) ||
		reAt.MatchString(s) || reDomain.MatchString(s)
}
