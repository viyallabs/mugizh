// Package public is the config module's interface (S-099, K-GLB-01, K-GLB-03).
//
// Everything that changes by country is read from a Pack, never from code constants.
// A Pack is immutable: it is built once by the loader (which has already validated it
// and failed closed on any problem) and every accessor returns a copy, so a caller can
// not change the rules another caller sees.
package public

import "slices"

// Provider gives modules the loaded country pack and the feature flags.
type Provider interface {
	Pack() *Pack
	// Flag reports whether a feature flag is on. An unknown flag is off (fail closed).
	Flag(name string) bool
}

// ResidencyException is a known, approved exception to the residency rule.
type ResidencyException struct {
	Vendor     string
	Data       []string
	Reason     string
	ApprovedBy string // empty means not approved
}

// Residency is where data and vendor processing must stay (K-DP-01, K-GLB-03).
type Residency struct {
	RegionPrimary            string
	RegionDR                 string
	VendorProcessingMustBeIn []string
	Exceptions               []ResidencyException
}

// Locale holds languages, legal-text languages and the default time zone (K-ARC-04).
type Locale struct {
	DefaultLocale      string
	SupportedLocales   []string
	LaunchLocales      []string
	LegalTextLanguages []string
	DefaultTimeZone    string
	DateFormat         string
	Units              string
}

// Money holds the currency and the tax configuration file.
type Money struct {
	Currency   string
	MinorUnits int
	Tax        []byte // raw tax configuration file
}

// ProhibitedIdentifier is an identifier that must be rejected and scrubbed (K-DP-05).
type ProhibitedIdentifier struct {
	ID        string
	Validator string // name understood by platform/scrub.ValidatorByName
	Action    string
}

// People holds age and identity rules.
type People struct {
	AgeOfMajority         int
	MinAccountAge         int
	PhoneCountryCodes     []string
	ProhibitedIdentifiers []ProhibitedIdentifier
	// AcceptedPhotoIDTypes are the identity documents doctors may submit (S-110). They never
	// include a prohibited identifier (K-DP-05, AAD-04).
	AcceptedPhotoIDTypes []string
	// PhoneNumberPattern is an anchored regular expression for a full sign-in number.
	PhoneNumberPattern string
}

// EmergencyNumber is a secondary emergency number.
type EmergencyNumber struct {
	Number string
	Label  string
	Note   string
}

// Emergency numbers are dialled directly by the phone (K-CS-06).
type Emergency struct {
	PrimaryNumber string
	OtherNumbers  []EmergencyNumber
}

// Clinical holds the prescribing rules and the drug table (K-CS-02, K-CS-07).
type Clinical struct {
	MinorsSupported               bool
	OnlineConsultPatientCountries []string
	PrescribingRules              []byte // raw rules file
	DrugClassification            []byte // raw drug classification file
}

// Payments holds settlement modes, the refund policy and the payments adapter id.
type Payments struct {
	SettlementModesAllowed []string
	RefundPolicy           []byte // raw refund policy file
	Adapter                string
}

// QuietHours is the window in which non-critical messages are held (K-MSG-03). HH:MM.
type QuietHours struct {
	Start string
	End   string
}

// Messaging holds channel switches and the SMS (DLT) registry (K-MSG-01).
type Messaging struct {
	ChannelsEnabled      map[string]bool
	SMSRegistry          []byte // raw SMS template registry file
	SMSPrincipalEntityID string
	QuietHours           QuietHours
}

// Grievance holds the grievance officer and the legal timers.
type Grievance struct {
	Officer                     string
	AcknowledgeHours            int
	ResolveDays                 int
	UnlawfulContentRemovalHours int
	OrderComplianceHours        int
	IntimateImageRemovalHours   int
	DataRightsResponseDays      int
	MedicalRecordCopyHours      int
	Appeal                      string
}

// Legal holds grievance details and the versions of notices and consent texts in force.
type Legal struct {
	Grievance           Grievance
	NoticeVersions      map[string]string
	ConsentTextVersions map[string]int
}

// RuleSetVersions are the versions to store with each prescription, booking and consent
// (country-pack-model section 3, K-CS-02, K-PAY-05).
type RuleSetVersions struct {
	Pack                string
	PrescribingRules    string
	DrugClassification  string
	RefundPolicy        string
	SMSRegistry         string
	Tax                 string
	NoticeVersions      map[string]string
	ConsentTextVersions map[string]int
	// FileSHA256 maps each referenced pack file to its SHA-256 (hex).
	FileSHA256 map[string]string
}

// Data is the plain content of a pack. The loader fills it; NewPack copies it.
type Data struct {
	Code      string
	Version   string
	Status    string
	Residency Residency
	Locale    Locale
	Money     Money
	People    People
	Emergency Emergency
	Clinical  Clinical
	// Retention maps a retention rule ID to its fields (values are scalars or maps).
	Retention map[string]map[string]any
	Payments  Payments
	Messaging Messaging
	Legal     Legal
	// Adapters maps adapter kind to vendor id ("none" when not used).
	Adapters map[string]string
	Features map[string]bool
	Versions RuleSetVersions
}

// Pack is an immutable, validated country pack.
type Pack struct{ d Data }

// NewPack copies d into an immutable Pack.
func NewPack(d Data) *Pack { return &Pack{d: cloneData(d)} }

func (p *Pack) Code() string     { return p.d.Code }
func (p *Pack) Version() string  { return p.d.Version }
func (p *Pack) Status() string   { return p.d.Status }
func (p *Pack) IsApproved() bool { return p.d.Status == "approved" }

func (p *Pack) Residency() Residency {
	r := p.d.Residency
	r.VendorProcessingMustBeIn = slices.Clone(r.VendorProcessingMustBeIn)
	r.Exceptions = slices.Clone(r.Exceptions)
	for i := range r.Exceptions {
		r.Exceptions[i].Data = slices.Clone(r.Exceptions[i].Data)
	}
	return r
}

func (p *Pack) Locale() Locale {
	l := p.d.Locale
	l.SupportedLocales = slices.Clone(l.SupportedLocales)
	l.LaunchLocales = slices.Clone(l.LaunchLocales)
	l.LegalTextLanguages = slices.Clone(l.LegalTextLanguages)
	return l
}

func (p *Pack) Money() Money {
	m := p.d.Money
	m.Tax = slices.Clone(m.Tax)
	return m
}

func (p *Pack) People() People {
	x := p.d.People
	x.PhoneCountryCodes = slices.Clone(x.PhoneCountryCodes)
	x.ProhibitedIdentifiers = slices.Clone(x.ProhibitedIdentifiers)
	x.AcceptedPhotoIDTypes = slices.Clone(x.AcceptedPhotoIDTypes)
	return x
}

func (p *Pack) Emergency() Emergency {
	e := p.d.Emergency
	e.OtherNumbers = slices.Clone(e.OtherNumbers)
	return e
}

func (p *Pack) Clinical() Clinical {
	c := p.d.Clinical
	c.OnlineConsultPatientCountries = slices.Clone(c.OnlineConsultPatientCountries)
	c.PrescribingRules = slices.Clone(c.PrescribingRules)
	c.DrugClassification = slices.Clone(c.DrugClassification)
	return c
}

// Retention returns a deep copy of the retention rules.
func (p *Pack) Retention() map[string]map[string]any { return cloneRetention(p.d.Retention) }

func (p *Pack) Payments() Payments {
	x := p.d.Payments
	x.SettlementModesAllowed = slices.Clone(x.SettlementModesAllowed)
	x.RefundPolicy = slices.Clone(x.RefundPolicy)
	return x
}

func (p *Pack) Messaging() Messaging {
	m := p.d.Messaging
	m.ChannelsEnabled = cloneMap(m.ChannelsEnabled)
	m.SMSRegistry = slices.Clone(m.SMSRegistry)
	return m
}

func (p *Pack) Legal() Legal {
	l := p.d.Legal
	l.NoticeVersions = cloneMap(l.NoticeVersions)
	l.ConsentTextVersions = cloneMap(l.ConsentTextVersions)
	return l
}

// Adapters returns kind to vendor id.
func (p *Pack) Adapters() map[string]string { return cloneMap(p.d.Adapters) }

// Adapter returns the vendor id for a kind, or "" if the kind is unknown.
func (p *Pack) Adapter(kind string) string { return p.d.Adapters[kind] }

// Features returns the country-level feature availability.
func (p *Pack) Features() map[string]bool { return cloneMap(p.d.Features) }

// RuleSetVersions returns the versions to store with bookings, prescriptions and consents.
func (p *Pack) RuleSetVersions() RuleSetVersions { return cloneVersions(p.d.Versions) }

func cloneMap[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = cloneAny(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneAny(x)
		}
		return out
	default:
		return v
	}
}

func cloneRetention(m map[string]map[string]any) map[string]map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(m))
	for k, rule := range m {
		out[k] = cloneAny(rule).(map[string]any)
	}
	return out
}

func cloneVersions(v RuleSetVersions) RuleSetVersions {
	v.NoticeVersions = cloneMap(v.NoticeVersions)
	v.ConsentTextVersions = cloneMap(v.ConsentTextVersions)
	v.FileSHA256 = cloneMap(v.FileSHA256)
	return v
}

func cloneData(d Data) Data {
	p := &Pack{d: d}
	d.Residency = p.Residency()
	d.Locale = p.Locale()
	d.Money = p.Money()
	d.People = p.People()
	d.Emergency = p.Emergency()
	d.Clinical = p.Clinical()
	d.Retention = p.Retention()
	d.Payments = p.Payments()
	d.Messaging = p.Messaging()
	d.Legal = p.Legal()
	d.Adapters = p.Adapters()
	d.Features = p.Features()
	d.Versions = p.RuleSetVersions()
	return d
}
