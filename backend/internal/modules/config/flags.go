package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ErrNonFlaggable is returned for a flag that would switch off a compliance control.
// Consent, the rules engine, the prescribing gate, audit and encryption can never be
// behind a flag (CLAUDE.md section 5.5, release-and-versioning section 5).
var ErrNonFlaggable = errors.New("config: flag refers to a non-flaggable control")

var (
	reFlagName    = regexp.MustCompile(`^[a-z][a-z0-9_.\-]{0,63}$`)
	reAdapterFlag = regexp.MustCompile(`^adapter\.([a-z_]+)\.enabled$`)
	reSeparators  = regexp.MustCompile(`[^a-z0-9]+`)
)

// nonFlaggable are fragments of a normalised flag name (lower case, separators removed).
var nonFlaggable = []string{"consent", "rulesengine", "ruleengine", "prescrib", "audit", "encrypt"}

// ValidateFlagName refuses malformed names and names of non-flaggable controls. Per-adapter
// kill switches (adapter.<kind>.enabled, adapter rule A9) are allowed for known kinds.
func ValidateFlagName(name string) error {
	norm := reSeparators.ReplaceAllString(strings.ToLower(name), "")
	for _, frag := range nonFlaggable {
		if strings.Contains(norm, frag) && !reAdapterFlag.MatchString(name) {
			return fmt.Errorf("%w: %q", ErrNonFlaggable, name)
		}
	}
	if !reFlagName.MatchString(name) {
		return fmt.Errorf("config: invalid flag name %q", name)
	}
	if strings.HasPrefix(name, "adapter.") && !reAdapterFlag.MatchString(name) {
		return fmt.Errorf("config: invalid kill switch name %q (want adapter.<kind>.enabled)", name)
	}
	if m := reAdapterFlag.FindStringSubmatch(name); m != nil {
		if !isAdapterKind(m[1]) {
			return fmt.Errorf("config: kill switch %q names an unknown adapter kind", name)
		}
		return nil
	}
	return nil
}

// Flags is an immutable set of feature flags. An unknown flag is off (fail closed).
type Flags struct{ on map[string]bool }

// NewFlags builds flags from the pack's features map plus optional overrides (the flag
// service, local config). Any invalid or non-flaggable name fails the whole set.
func NewFlags(base, overrides map[string]bool) (*Flags, error) {
	f := &Flags{on: make(map[string]bool, len(base)+len(overrides))}
	var errs []error
	for _, src := range []map[string]bool{base, overrides} {
		for name, v := range src {
			if err := ValidateFlagName(name); err != nil {
				errs = append(errs, err)
				continue
			}
			f.on[name] = v
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return f, nil
}

// Enabled reports whether the flag is on. Unknown flags are off.
func (f *Flags) Enabled(name string) bool {
	if f == nil {
		return false
	}
	return f.on[name]
}

// Names returns the known flag names, sorted.
func (f *Flags) Names() []string {
	out := make([]string, 0, len(f.on))
	for n := range f.on {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
