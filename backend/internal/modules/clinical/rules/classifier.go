package rules

// Drug is one drug master entry as the engine sees it: list memberships only.
// Unclassified means no list membership at all.
type Drug struct {
	Ref         string
	GenericName string

	Prohibited bool
	ListO      bool
	ListA      bool
	ListB      bool

	// AmendmentException marks a drug the rule file's amendment exception names; only then
	// may a prohibited drug also sit in a permitted list.
	AmendmentException bool
}

// MapClassifier is an in-memory Classifier: the drug master loads one version into it, and
// tests supply synthetic tables.
type MapClassifier struct {
	VersionLabel string
	Drugs        map[string]Drug
}

// NewMapClassifier builds a classifier from drugs keyed by their Ref.
func NewMapClassifier(version string, drugs ...Drug) *MapClassifier {
	m := &MapClassifier{VersionLabel: version, Drugs: make(map[string]Drug, len(drugs))}
	for _, d := range drugs {
		m.Drugs[d.Ref] = d
	}
	return m
}

// Version implements Classifier.
func (m *MapClassifier) Version() string { return m.VersionLabel }

// Lookup implements Classifier.
func (m *MapClassifier) Lookup(ref string) (Drug, bool) {
	d, ok := m.Drugs[ref]
	return d, ok
}
