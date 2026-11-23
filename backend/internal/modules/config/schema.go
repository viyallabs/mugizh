package config

import (
	"fmt"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The manifest schema (country-pack-model section 2). Every key is required unless the
// field carries the tag `opt:"true"`. Unknown keys are rejected by the strict decoder.

type manifest struct {
	Pack      packSection       `yaml:"pack"`
	Residency residencySection  `yaml:"residency"`
	Locale    localeSection     `yaml:"locale"`
	Money     moneySection      `yaml:"money"`
	People    peopleSection     `yaml:"people"`
	Emergency emergencySection  `yaml:"emergency"`
	Clinical  clinicalSection   `yaml:"clinical"`
	Retention retentionSection  `yaml:"retention"`
	Payments  paymentsSection   `yaml:"payments"`
	Messaging messagingSection  `yaml:"messaging"`
	Legal     legalSection      `yaml:"legal"`
	Adapters  map[string]string `yaml:"adapters"`
	Features  map[string]bool   `yaml:"features"`
}

type packSection struct {
	Code      string            `yaml:"code"`
	Version   string            `yaml:"version"`
	Status    string            `yaml:"status"`
	Checksums map[string]string `yaml:"checksums"`
}

type residencySection struct {
	RegionPrimary            string               `yaml:"region_primary"`
	RegionDR                 string               `yaml:"region_dr"`
	VendorProcessingMustBeIn []string             `yaml:"vendor_processing_must_be_in"`
	Exceptions               []residencyException `yaml:"exceptions"`
}

type residencyException struct {
	Vendor     string   `yaml:"vendor"`
	Data       []string `yaml:"data"`
	Reason     string   `yaml:"reason"`
	ApprovedBy *string  `yaml:"approved_by"`
}

type localeSection struct {
	DefaultLocale      string       `yaml:"default_locale"`
	SupportedLocales   []string     `yaml:"supported_locales"`
	LaunchLocales      []string     `yaml:"launch_locales"`
	LegalTextLanguages stringOrList `yaml:"legal_text_languages"`
	DefaultTimeZone    string       `yaml:"default_time_zone"`
	DateFormat         string       `yaml:"date_format"`
	Units              string       `yaml:"units"`
}

// stringOrList accepts `x` or `[x, y]` (legal_text_languages).
type stringOrList []string

func (s *stringOrList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var v string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = stringOrList{v}
		return nil
	case yaml.SequenceNode:
		var v []string
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = stringOrList(v)
		return nil
	default:
		return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
	}
}

type moneySection struct {
	Currency   string `yaml:"currency"`
	MinorUnits int    `yaml:"minor_units"`
	Tax        string `yaml:"tax"`
}

type peopleSection struct {
	AgeOfMajority         int                    `yaml:"age_of_majority"`
	MinAccountAge         int                    `yaml:"min_account_age"`
	PhoneCountryCodes     []string               `yaml:"phone_country_codes"`
	ProhibitedIdentifiers []prohibitedIdentifier `yaml:"prohibited_identifiers"`
	AcceptedPhotoIDTypes  []string               `yaml:"accepted_photo_id_types"`
	PhoneNumberPattern    string                 `yaml:"phone_number_pattern"`
}

type prohibitedIdentifier struct {
	ID        string `yaml:"id"`
	Validator string `yaml:"validator"`
	Action    string `yaml:"action"`
}

type emergencySection struct {
	PrimaryNumber string            `yaml:"primary_number"`
	OtherNumbers  []emergencyNumber `yaml:"other_numbers"`
}

type emergencyNumber struct {
	Number string `yaml:"number"`
	Label  string `yaml:"label"`
	Note   string `yaml:"note"`
}

type clinicalSection struct {
	PrescribingRules              string   `yaml:"prescribing_rules"`
	DrugClassification            string   `yaml:"drug_classification"`
	MinorsSupported               bool     `yaml:"minors_supported"`
	OnlineConsultPatientCountries []string `yaml:"online_consult_patient_countries"`
}

type retentionSection struct {
	Rules map[string]map[string]any `yaml:"rules"`
}

type paymentsSection struct {
	SettlementModesAllowed []string `yaml:"settlement_modes_allowed"`
	RefundPolicy           string   `yaml:"refund_policy"`
	Adapter                string   `yaml:"adapter"`
}

type messagingSection struct {
	ChannelsEnabled      map[string]bool `yaml:"channels_enabled"`
	SMSRegistry          string          `yaml:"sms_registry"`
	SMSPrincipalEntityID string          `yaml:"sms_principal_entity_id"`
	QuietHours           quietHours      `yaml:"quiet_hours"`
}

type quietHours struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type legalSection struct {
	Grievance           grievance         `yaml:"grievance"`
	NoticeVersions      map[string]string `yaml:"notice_versions"`
	ConsentTextVersions map[string]int    `yaml:"consent_text_versions"`
}

type grievance struct {
	Officer                     string `yaml:"officer"`
	AcknowledgeHours            int    `yaml:"acknowledge_hours"`
	ResolveDays                 int    `yaml:"resolve_days"`
	UnlawfulContentRemovalHours int    `yaml:"unlawful_content_removal_hours"`
	OrderComplianceHours        int    `yaml:"order_compliance_hours"`
	IntimateImageRemovalHours   int    `yaml:"intimate_image_removal_hours"`
	DataRightsResponseDays      int    `yaml:"data_rights_response_days"`
	MedicalRecordCopyHours      int    `yaml:"medical_record_copy_hours"`
	Appeal                      string `yaml:"appeal"`
}

// requiredKeys returns every required key path of t (dotted), skipping `opt:"true"` fields.
func requiredKeys(t reflect.Type, prefix string, out *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		if f.Tag.Get("opt") == "true" {
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			requiredKeys(f.Type, path+".", out)
			continue
		}
		*out = append(*out, path)
	}
}

// checkPresence verifies that every required key exists, is not null, and (for plain
// scalars) is not an empty string. Zero values such as `false` and `0` are valid.
func checkPresence(root *yaml.Node) []error {
	var paths []string
	requiredKeys(reflect.TypeOf(manifest{}), "", &paths)
	var errs []error
	for _, p := range paths {
		n := lookup(root, strings.Split(p, "."))
		switch {
		case n == nil:
			errs = append(errs, fmt.Errorf("pack.yaml: required key %q is missing", p))
		case n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || (n.Tag == "!!str" && strings.TrimSpace(n.Value) == "")):
			errs = append(errs, fmt.Errorf("pack.yaml: required key %q is empty (line %d)", p, n.Line))
		}
	}
	return errs
}

func lookup(n *yaml.Node, path []string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	for _, key := range path {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}
