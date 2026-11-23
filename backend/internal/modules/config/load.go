// Package config is the config module (S-099): it loads and validates the country pack
// and the feature flags, and hands modules an immutable view through config/public.
//
// Every rule here fails closed. Load returns an error for any problem and the caller
// (cmd/api, cmd/worker) must refuse to start; the service never runs on partial config
// (country-pack-model section 3, K-GLB-01, K-GLB-03, K-DP-01, K-DP-08, TC-53, TC-59).
//
// There are no country values in this package: phone prefixes, currency, zones and
// emergency numbers come from the pack.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Env is the deployment environment.
type Env string

const (
	EnvLocal      Env = "local"
	EnvTest       Env = "test"
	EnvDev        Env = "dev"
	EnvStaging    Env = "staging"
	EnvProduction Env = "production"
)

func (e Env) valid() bool {
	switch e {
	case EnvLocal, EnvTest, EnvDev, EnvStaging, EnvProduction:
		return true
	}
	return false
}

// lenient environments use synthetic data only (CLAUDE.md section 5.3), so drafts,
// placeholders, fake adapters and not-yet-approved vendors may load there.
func (e Env) lenient() bool { return e == EnvLocal || e == EnvTest || e == EnvDev }

// Options for Load.
type Options struct {
	PackDir            string // config/country-packs/<CODE>
	VendorRegisterPath string // config/vendor-register.yaml
	Env                Env
	// FlagOverrides are extra flags (flag service, local config). Non-flaggable names are refused.
	FlagOverrides map[string]bool
}

// Config is the loaded configuration. It implements public.Provider.
type Config struct {
	pack     *public.Pack
	flags    *Flags
	warnings []string
}

var _ public.Provider = (*Config)(nil)

// Pack returns the immutable country pack.
func (c *Config) Pack() *public.Pack { return c.pack }

// Flag reports whether a feature flag is on; unknown flags are off.
func (c *Config) Flag(name string) bool { return c.flags.Enabled(name) }

// Flags returns the flag set.
func (c *Config) Flags() *Flags { return c.flags }

// Warnings lists accepted-but-unfinished items (only ever non-empty in local, test, dev).
func (c *Config) Warnings() []string { return slices.Clone(c.warnings) }

// Adapter kinds a pack may name (country-pack-model section 2).
var adapterKinds = []string{
	"payments", "sms", "push", "email", "video", "geocoding",
	"verification", "esign", "age_verification", "ocr", "whatsapp",
}

func isAdapterKind(k string) bool { return slices.Contains(adapterKinds, k) }

var channels = []string{"push", "sms", "email", "whatsapp"}

// Markers of unfinished values. Staging and production refuse any value that has one.
// missingMarkers mean "no value yet": refused in staging and production.
var missingMarkers = []string{"[FILL", "[S-0"}

// placeholderMarkers also include pending approvals. Staging may load drafts (country-pack-model
// §3 rule 2), so approvals may still be pending there; production refuses every marker.
var placeholderMarkers = []string{"[FILL", "[S-0", "[CONFIRM", "[VALIDATE", "[CA", "[CLINICAL"}

const none = "none"

var (
	reSemver    = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?$`)
	reCountry   = regexp.MustCompile(`^[A-Z]{2}$`)
	reCurrency  = regexp.MustCompile(`^[A-Z]{3}$`)
	rePhoneCode = regexp.MustCompile(`^\+[1-9][0-9]{0,2}$`)
	reLocale    = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	reDialNum   = regexp.MustCompile(`^[0-9]{2,6}$`)
	reRetention = regexp.MustCompile(`^RET-[0-9]{2}$`)
	reHHMM      = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	reSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Load reads, validates and builds the configuration. Any error means: do not start.
func Load(opts Options) (*Config, error) {
	if !opts.Env.valid() {
		return nil, fmt.Errorf("config: unknown environment %q", opts.Env)
	}
	raw, err := readPackFile(opts.PackDir, ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var m manifest
	if err := strictDecode(raw, &m); err != nil {
		return nil, fmt.Errorf("config: %s: %w", ManifestFile, err)
	}
	root, err := parseNode(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", ManifestFile, err)
	}
	if errs := checkPresence(root); len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}

	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	validateScalars(&m, add)
	validateStatus(&m, opts.Env, add)

	files := loadReferenced(opts.PackDir, &m, add)
	if !opts.Env.lenient() {
		markers := missingMarkers
		if opts.Env == EnvProduction {
			markers = placeholderMarkers
		}
		checkPlaceholders(root, files, markers, add)
	}

	reg, err := loadRegister(opts.VendorRegisterPath)
	var warnings []string
	if err != nil {
		add("%v", err)
	} else {
		warnings = validateAdapters(&m, reg, opts.Env, add)
	}

	flags, err := NewFlags(m.Features, opts.FlagOverrides)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: country pack %q refused: %w", opts.PackDir, errors.Join(errs...))
	}
	return &Config{pack: public.NewPack(buildData(&m, files)), flags: flags, warnings: warnings}, nil
}

// refFile is a loaded, checksum-verified referenced file.
type refFile struct {
	data    []byte
	node    *yaml.Node
	version string
}

func loadReferenced(dir string, m *manifest, add func(string, ...any)) map[string]refFile {
	out := map[string]refFile{}
	refs := m.referenced()
	// Every referenced file needs a checksum and no checksum may name an unreferenced file.
	for _, r := range refs {
		if _, ok := m.Pack.Checksums[r]; !ok {
			add("pack.checksums: no checksum for referenced file %q", r)
		}
	}
	for name := range m.Pack.Checksums {
		if !slices.Contains(refs, name) {
			add("pack.checksums: %q is not a file referenced by the manifest", name)
		}
	}
	for _, rel := range refs {
		data, err := readPackFile(dir, rel)
		if err != nil {
			add("%v", err)
			continue
		}
		want, ok := m.Pack.Checksums[rel]
		if ok {
			if !reSHA256.MatchString(want) {
				add("pack.checksums[%q]: not a lower-case SHA-256", rel)
				continue
			}
			if got := sum(data); got != want {
				add("file %q: checksum mismatch (the file changed after approval); run cmd/packsum and get the approver named in country-pack-model section 2", rel)
				continue
			}
		}
		node, err := parseNode(data)
		if err != nil {
			add("file %q: %v", rel, err)
			continue
		}
		var head map[string]any
		if err := node.Decode(&head); err != nil || head == nil {
			add("file %q: must be a YAML mapping", rel)
			continue
		}
		ver, _ := head["version"].(string)
		if ver == "" {
			add("file %q: missing top-level string key \"version\"", rel)
			continue
		}
		if p, ok := head["pack"]; ok && p != m.Pack.Code {
			add("file %q: pack is %v, manifest says %s", rel, p, m.Pack.Code)
		}
		if c, ok := head["currency"]; ok && c != m.Money.Currency {
			add("file %q: currency is %v, manifest says %s", rel, c, m.Money.Currency)
		}
		if rel == m.Clinical.DrugClassification {
			if _, ok := head["entries"].([]any); !ok {
				add("file %q: \"entries\" must be a list (an empty list blocks every drug)", rel)
			}
		}
		out[rel] = refFile{data: data, node: node, version: ver}
	}
	return out
}

func validateStatus(m *manifest, env Env, add func(string, ...any)) {
	switch m.Pack.Status {
	case "approved":
	case "draft":
		if env == EnvProduction {
			add("pack.status is draft: production loads only approved packs")
		}
	case "retired":
		add("pack.status is retired: a retired pack never loads")
	default:
		add("pack.status %q must be draft, approved or retired", m.Pack.Status)
	}
}

func validateScalars(m *manifest, add func(string, ...any)) {
	if !reCountry.MatchString(m.Pack.Code) {
		add("pack.code %q must be two upper-case letters (ISO 3166-1 alpha-2)", m.Pack.Code)
	}
	if !reSemver.MatchString(m.Pack.Version) {
		add("pack.version %q is not semver", m.Pack.Version)
	}
	for _, c := range m.Residency.VendorProcessingMustBeIn {
		if !reCountry.MatchString(c) {
			add("residency.vendor_processing_must_be_in: %q is not a country code", c)
		}
	}
	if len(m.Residency.VendorProcessingMustBeIn) == 0 {
		add("residency.vendor_processing_must_be_in must not be empty")
	}
	for i, e := range m.Residency.Exceptions {
		if e.Vendor == "" || e.Reason == "" {
			add("residency.exceptions[%d]: vendor and reason are required", i)
		}
	}

	l := m.Locale
	for _, tag := range append(append([]string{l.DefaultLocale}, l.SupportedLocales...), append(slices.Clone(l.LaunchLocales), l.LegalTextLanguages...)...) {
		// legal_text_languages may also hold a policy keyword such as a language group; only
		// BCP-47-like tags and lower-case words with underscores are accepted.
		if !reLocale.MatchString(tag) && !reKeyword.MatchString(tag) {
			add("locale: %q is not a locale tag", tag)
		}
	}
	if len(l.SupportedLocales) == 0 || len(l.LaunchLocales) == 0 || len(l.LegalTextLanguages) == 0 {
		add("locale: supported_locales, launch_locales and legal_text_languages must not be empty")
	}
	if !slices.Contains(l.SupportedLocales, l.DefaultLocale) {
		add("locale.default_locale %q is not in supported_locales", l.DefaultLocale)
	}
	for _, x := range l.LaunchLocales {
		if !slices.Contains(l.SupportedLocales, x) {
			add("locale.launch_locales: %q is not in supported_locales", x)
		}
	}
	if l.DefaultTimeZone == "Local" || l.DefaultTimeZone == "UTC" {
		add("locale.default_time_zone %q must be a named IANA zone", l.DefaultTimeZone)
	} else if _, err := time.LoadLocation(l.DefaultTimeZone); err != nil {
		add("locale.default_time_zone %q is not an IANA zone: %v", l.DefaultTimeZone, err)
	}

	if !reCurrency.MatchString(m.Money.Currency) {
		add("money.currency %q must be three upper-case letters", m.Money.Currency)
	}
	if m.Money.MinorUnits < 0 || m.Money.MinorUnits > 4 {
		add("money.minor_units %d must be 0 to 4", m.Money.MinorUnits)
	}

	p := m.People
	if p.AgeOfMajority <= 0 || p.MinAccountAge <= 0 {
		add("people: age_of_majority and min_account_age must be positive")
	}
	if !m.Clinical.MinorsSupported && p.MinAccountAge < p.AgeOfMajority {
		add("people.min_account_age is below age_of_majority while clinical.minors_supported is false (K-DP-10)")
	}
	if len(p.PhoneCountryCodes) == 0 {
		add("people.phone_country_codes must not be empty")
	}
	for _, c := range p.PhoneCountryCodes {
		if !rePhoneCode.MatchString(c) {
			add("people.phone_country_codes: %q is not of the form +NN", c)
		}
	}
	if re, err := regexp.Compile(p.PhoneNumberPattern); err != nil || re == nil || !strings.HasPrefix(p.PhoneNumberPattern, "^") || !strings.HasSuffix(p.PhoneNumberPattern, "$") {
		add("people.phone_number_pattern must be an anchored regular expression")
	}
	if len(p.AcceptedPhotoIDTypes) == 0 {
		add("people.accepted_photo_id_types must not be empty (doctor identity evidence, S-110)")
	}
	for _, id := range p.AcceptedPhotoIDTypes {
		if !reKeyword.MatchString(id) {
			add("people.accepted_photo_id_types: %q is not a lower-case keyword", id)
		}
		// A prohibited identifier (e.g. the one guarded by the Verhoeff validator) can
		// never be an accepted identity document (K-DP-05, AAD-04).
		for _, pi := range p.ProhibitedIdentifiers {
			if id == pi.ID || strings.HasPrefix(pi.ID, id+"_") || strings.HasPrefix(id, pi.ID) {
				add("people.accepted_photo_id_types: %q is a prohibited identifier", id)
			}
		}
	}
	for i, pi := range p.ProhibitedIdentifiers {
		if pi.ID == "" || pi.Action == "" {
			add("people.prohibited_identifiers[%d]: id and action are required", i)
		}
		if _, ok := scrub.ValidatorByName(pi.Validator); !ok {
			add("people.prohibited_identifiers[%d]: unknown validator %q", i, pi.Validator)
		}
	}

	if !reDialNum.MatchString(m.Emergency.PrimaryNumber) {
		add("emergency.primary_number %q must be 2 to 6 digits", m.Emergency.PrimaryNumber)
	}
	for i, o := range m.Emergency.OtherNumbers {
		if !reDialNum.MatchString(o.Number) || o.Label == "" {
			add("emergency.other_numbers[%d]: number must be 2 to 6 digits and label is required", i)
		}
	}

	for _, c := range m.Clinical.OnlineConsultPatientCountries {
		if !reCountry.MatchString(c) {
			add("clinical.online_consult_patient_countries: %q is not a country code", c)
		}
	}

	if len(m.Retention.Rules) == 0 {
		add("retention.rules must not be empty")
	}
	for id, r := range m.Retention.Rules {
		if !reRetention.MatchString(id) || len(r) == 0 {
			add("retention.rules: %q must be RET-NN with at least one field", id)
		}
	}

	if len(m.Payments.SettlementModesAllowed) == 0 {
		add("payments.settlement_modes_allowed must not be empty")
	}

	for ch := range m.Messaging.ChannelsEnabled {
		if !slices.Contains(channels, ch) {
			add("messaging.channels_enabled: unknown channel %q", ch)
		}
	}
	for _, ch := range channels {
		if _, ok := m.Messaging.ChannelsEnabled[ch]; !ok {
			add("messaging.channels_enabled: channel %q must be stated", ch)
		}
	}
	for name, v := range map[string]string{"start": m.Messaging.QuietHours.Start, "end": m.Messaging.QuietHours.End} {
		if !reHHMM.MatchString(v) {
			add("messaging.quiet_hours.%s %q must be HH:MM", name, v)
		}
	}

	g := m.Legal.Grievance
	for name, v := range map[string]int{
		"acknowledge_hours": g.AcknowledgeHours, "resolve_days": g.ResolveDays,
		"unlawful_content_removal_hours": g.UnlawfulContentRemovalHours,
		"order_compliance_hours":         g.OrderComplianceHours,
		"intimate_image_removal_hours":   g.IntimateImageRemovalHours,
		"data_rights_response_days":      g.DataRightsResponseDays,
		"medical_record_copy_hours":      g.MedicalRecordCopyHours,
	} {
		if v <= 0 {
			add("legal.grievance.%s must be a positive whole number", name)
		}
	}
	if len(m.Legal.NoticeVersions) == 0 || len(m.Legal.ConsentTextVersions) == 0 {
		add("legal: notice_versions and consent_text_versions must not be empty")
	}

	for kind := range m.Adapters {
		if !isAdapterKind(kind) {
			add("adapters: unknown kind %q", kind)
		}
	}
	for _, kind := range adapterKinds {
		if m.Adapters[kind] == "" {
			add("adapters: kind %q must be stated (use %q if unused)", kind, none)
		}
	}
	if normaliseAdapter(m.Payments.Adapter) != normaliseAdapter(m.Adapters["payments"]) {
		add("payments.adapter %q differs from adapters.payments %q", m.Payments.Adapter, m.Adapters["payments"])
	}
}

var reKeyword = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkPlaceholders refuses unfinished values in staging and production.
func checkPlaceholders(root *yaml.Node, files map[string]refFile, markers []string, add func(string, ...any)) {
	scan := func(file string, n *yaml.Node) {
		walkValues(n, func(v *yaml.Node) {
			for _, mk := range markers {
				if strings.Contains(v.Value, mk) {
					add("%s line %d: unfinished value (%s) is not allowed in staging or production", file, v.Line, mk)
					return
				}
			}
		})
	}
	scan(ManifestFile, root)
	for name, f := range files {
		scan(name, f.node)
	}
}

// walkValues calls fn for every scalar value (not mapping keys).
func walkValues(n *yaml.Node, fn func(*yaml.Node)) {
	switch n.Kind {
	case yaml.ScalarNode:
		fn(n)
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			walkValues(n.Content[i], fn)
		}
	default:
		for _, c := range n.Content {
			walkValues(c, fn)
		}
	}
}

func buildData(m *manifest, files map[string]refFile) public.Data {
	d := public.Data{
		Code: m.Pack.Code, Version: m.Pack.Version, Status: m.Pack.Status,
		Residency: public.Residency{
			RegionPrimary: m.Residency.RegionPrimary, RegionDR: m.Residency.RegionDR,
			VendorProcessingMustBeIn: m.Residency.VendorProcessingMustBeIn,
		},
		Locale: public.Locale{
			DefaultLocale: m.Locale.DefaultLocale, SupportedLocales: m.Locale.SupportedLocales,
			LaunchLocales: m.Locale.LaunchLocales, LegalTextLanguages: m.Locale.LegalTextLanguages,
			DefaultTimeZone: m.Locale.DefaultTimeZone, DateFormat: m.Locale.DateFormat, Units: m.Locale.Units,
		},
		Money: public.Money{
			Currency: m.Money.Currency, MinorUnits: m.Money.MinorUnits, Tax: files[m.Money.Tax].data,
		},
		People: public.People{
			AgeOfMajority: m.People.AgeOfMajority, MinAccountAge: m.People.MinAccountAge,
			PhoneCountryCodes:    m.People.PhoneCountryCodes,
			AcceptedPhotoIDTypes: m.People.AcceptedPhotoIDTypes,
			PhoneNumberPattern:   m.People.PhoneNumberPattern,
		},
		Emergency: public.Emergency{PrimaryNumber: m.Emergency.PrimaryNumber},
		Clinical: public.Clinical{
			MinorsSupported:               m.Clinical.MinorsSupported,
			OnlineConsultPatientCountries: m.Clinical.OnlineConsultPatientCountries,
			PrescribingRules:              files[m.Clinical.PrescribingRules].data,
			DrugClassification:            files[m.Clinical.DrugClassification].data,
		},
		Retention: m.Retention.Rules,
		Payments: public.Payments{
			SettlementModesAllowed: m.Payments.SettlementModesAllowed,
			RefundPolicy:           files[m.Payments.RefundPolicy].data,
			Adapter:                normaliseAdapter(m.Payments.Adapter),
		},
		Messaging: public.Messaging{
			ChannelsEnabled: m.Messaging.ChannelsEnabled, SMSRegistry: files[m.Messaging.SMSRegistry].data,
			SMSPrincipalEntityID: m.Messaging.SMSPrincipalEntityID,
			QuietHours:           public.QuietHours{Start: m.Messaging.QuietHours.Start, End: m.Messaging.QuietHours.End},
		},
		Legal: public.Legal{
			Grievance: public.Grievance(m.Legal.Grievance), NoticeVersions: m.Legal.NoticeVersions,
			ConsentTextVersions: m.Legal.ConsentTextVersions,
		},
		Features: m.Features,
	}
	for _, e := range m.Residency.Exceptions {
		x := public.ResidencyException{Vendor: e.Vendor, Data: e.Data, Reason: e.Reason}
		if e.ApprovedBy != nil {
			x.ApprovedBy = *e.ApprovedBy
		}
		d.Residency.Exceptions = append(d.Residency.Exceptions, x)
	}
	for _, e := range m.Emergency.OtherNumbers {
		d.Emergency.OtherNumbers = append(d.Emergency.OtherNumbers, public.EmergencyNumber(e))
	}
	for _, pi := range m.People.ProhibitedIdentifiers {
		d.People.ProhibitedIdentifiers = append(d.People.ProhibitedIdentifiers, public.ProhibitedIdentifier(pi))
	}
	d.Adapters = make(map[string]string, len(m.Adapters))
	for k, v := range m.Adapters {
		d.Adapters[k] = normaliseAdapter(v)
	}
	sha := map[string]string{}
	for name, f := range files {
		sha[name] = sum(f.data)
	}
	d.Versions = public.RuleSetVersions{
		Pack:                m.Pack.Version,
		PrescribingRules:    files[m.Clinical.PrescribingRules].version,
		DrugClassification:  files[m.Clinical.DrugClassification].version,
		RefundPolicy:        files[m.Payments.RefundPolicy].version,
		SMSRegistry:         files[m.Messaging.SMSRegistry].version,
		Tax:                 files[m.Money.Tax].version,
		NoticeVersions:      m.Legal.NoticeVersions,
		ConsentTextVersions: m.Legal.ConsentTextVersions,
		FileSHA256:          sha,
	}
	return d
}

// normaliseAdapter turns an unfinished adapter id (a "[...]" placeholder, only possible
// in local, test and dev) into "none", so callers see an adapter that is not available.
func normaliseAdapter(id string) string {
	if strings.HasPrefix(id, "[") {
		return none
	}
	return id
}
