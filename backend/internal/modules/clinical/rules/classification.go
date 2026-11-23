package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"
)

// ClassificationEntry is one drug in the country pack's drugs/classification.yaml.
type ClassificationEntry struct {
	Ref                string   `yaml:"ref"`
	GenericName        string   `yaml:"generic_name"`
	BrandNames         []string `yaml:"brand_names"`
	Strength           string   `yaml:"strength"`
	Form               string   `yaml:"form"`
	Route              string   `yaml:"route"`
	Lists              []string `yaml:"lists"` // prohibited, list_o, list_a, list_b
	AmendmentException bool     `yaml:"amendment_exception"`
	ConditionTags      []string `yaml:"condition_tags"` // required for list_b
	Source             string   `yaml:"source"`         // where the classification comes from (required)
}

// Classification is a parsed, validated classification file.
type Classification struct {
	Version  string
	Status   string
	Entries  []ClassificationEntry
	Checksum string // SHA-256 (hex) of the file bytes
}

type classificationFile struct {
	Version string                 `yaml:"version"`
	Status  string                 `yaml:"status"`
	Entries *[]ClassificationEntry `yaml:"entries"`
}

// Drug converts an entry to the engine's view.
func (en ClassificationEntry) Drug() Drug {
	return Drug{
		Ref: en.Ref, GenericName: en.GenericName,
		Prohibited:         slices.Contains(en.Lists, catProhibited),
		ListO:              slices.Contains(en.Lists, catListO),
		ListA:              slices.Contains(en.Lists, catListA),
		ListB:              slices.Contains(en.Lists, catListB),
		AmendmentException: en.AmendmentException,
	}
}

// ErrClassificationInvalid wraps every import rejection.
var ErrClassificationInvalid = errors.New("rules: drug classification rejected")

var (
	reClsVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,39}$`)
	reClsRef     = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{1,79}$`)
	reClsName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 \-,/().+]{0,99}$`)
	reClsText    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 .,/%+()\-]{0,59}$`)
	reClsTag     = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)

// ParseClassification parses and validates a drug classification file against this rule
// file (spec section 4 precedence, T30). It rejects, with no partial result:
//   - unknown keys, a missing "entries" list or version;
//   - duplicate refs or duplicate drugs, and entries without a source;
//   - a drug that is prohibited and also in a permitted list without an amendment exception;
//   - an amendment exception on a drug the rule file's amendment does not name;
//   - when the table is not empty, an amendment drug the rule file names that is missing;
//   - a List B drug without a condition tag.
//
// An empty table is valid: it blocks every drug (fail closed).
func (e *Engine) ParseClassification(data []byte) (*Classification, error) {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f classificationFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrClassificationInvalid, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: the file must hold exactly one YAML document", ErrClassificationInvalid)
	}
	if !reClsVersion.MatchString(f.Version) {
		add("version is missing or malformed")
	}
	switch f.Status {
	case StatusDraft, StatusDoctorReviewed, StatusLegalReviewed, StatusApproved:
	default:
		add("status %q is not draft, doctor_reviewed, legal_reviewed or approved", f.Status)
	}
	if f.Entries == nil {
		add("entries must be a list (an empty list blocks every drug)")
		return nil, fmt.Errorf("%w: %w", ErrClassificationInvalid, errors.Join(errs...))
	}

	refs, drugs := map[string]bool{}, map[string]bool{}
	byName := map[string]bool{}
	for i, en := range *f.Entries {
		w := fmt.Sprintf("entry %d (%s)", i+1, en.Ref)
		if !reClsRef.MatchString(en.Ref) {
			add("entry %d: ref is missing or malformed", i+1)
		} else if refs[en.Ref] {
			add("%s: duplicate ref", w)
		}
		refs[en.Ref] = true
		if !reClsName.MatchString(en.GenericName) {
			add("%s: generic_name is missing or malformed", w)
		}
		for _, b := range en.BrandNames {
			if !reClsName.MatchString(b) {
				add("%s: brand name %q is malformed", w, b)
			}
		}
		for name, v := range map[string]string{"strength": en.Strength, "form": en.Form, "route": en.Route} {
			if !reClsText.MatchString(v) {
				add("%s: %s is missing or malformed", w, name)
			}
		}
		key := NormalizeName(en.GenericName) + "|" + NormalizeName(en.Strength) + "|" + NormalizeName(en.Form) + "|" + NormalizeName(en.Route)
		if drugs[key] {
			add("%s: duplicate drug (same generic name, strength, form and route)", w)
		}
		drugs[key] = true
		byName[NormalizeName(en.GenericName)] = true
		if en.Source == "" || len(en.Source) > 300 {
			add("%s: a source reference is required", w)
		}
		prohibited, permitted := false, false
		seenList := map[string]bool{}
		for _, l := range en.Lists {
			switch l {
			case catProhibited:
				prohibited = true
			case catListO, catListA, catListB:
				permitted = true
			default:
				add("%s: unknown list %q", w, l)
			}
			if seenList[l] {
				add("%s: list %q repeated", w, l)
			}
			seenList[l] = true
		}
		if prohibited && permitted && !en.AmendmentException {
			add("%s: prohibited and also in a permitted list without an amendment exception", w)
		}
		if en.AmendmentException && !e.except[NormalizeName(en.GenericName)] {
			add("%s: amendment exception, but the rule file's amendment does not name this drug", w)
		}
		if seenList[catListB] && len(en.ConditionTags) == 0 {
			add("%s: a List B drug needs a condition tag", w)
		}
		for _, t := range en.ConditionTags {
			if !reClsTag.MatchString(t) {
				add("%s: condition tag %q is malformed", w, t)
			}
		}
	}
	if len(*f.Entries) > 0 {
		for name := range e.except {
			if !byName[name] {
				add("the amendment exception names %q, which is not in the drug master", name)
			}
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrClassificationInvalid, errors.Join(errs...))
	}
	sum := sha256.Sum256(data)
	return &Classification{Version: f.Version, Status: f.Status, Entries: *f.Entries, Checksum: hex.EncodeToString(sum[:])}, nil
}
