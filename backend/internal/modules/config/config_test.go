package config

import (
	"errors"
	"go.yaml.in/yaml/v3"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config/public"
)

const (
	inDir    = "../../../../config/country-packs/IN"
	xxDir    = "../../../../config/country-packs/XX"
	registry = "../../../../config/vendor-register.yaml"
)

// --- helpers ---

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// tempPack copies a real pack into a temp dir for negative tests.
func tempPack(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	copyDir(t, src, dst)
	return dst
}

// edit replaces old with new (once) in dir/rel; the test fails if old is absent.
func edit(t *testing.T, dir, rel, old, repl string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q", rel, old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, repl, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// realisticPack is a copy of XX that uses ordinary registered vendors instead of fakes, so
// it can load in staging and production. Returns the pack dir and its vendor register.
func realisticPack(t *testing.T) (dir, reg string) {
	t.Helper()
	dir = tempPack(t, xxDir)
	b, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(strings.ReplaceAll(string(b), "fake-", "acme-")), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("vendors:\n")
	for _, kv := range [][2]string{{"payments", "acme-payments"}, {"sms", "acme-sms"}, {"push", "acme-push"}, {"email", "acme-email"},
		{"video", "acme-video"}, {"geocoding", "acme-geocoding"}, {"esign", "acme-esign"}} {
		sb.WriteString("  - { id: " + kv[1] + ", kind: " + kv[0] + ", processor_agreement: true, processing_countries: [PT], status: approved }\n")
	}
	sb.WriteString("  - { id: manual, kind: verification, processor_agreement: true, processing_countries: [], status: internal }\n")
	reg = filepath.Join(t.TempDir(), "vendors.yaml")
	if err := os.WriteFile(reg, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, reg
}

func load(dir, reg string, env Env) (*Config, error) {
	return Load(Options{PackDir: dir, VendorRegisterPath: reg, Env: env})
}

func mustLoad(t *testing.T, dir, reg string, env Env) *Config {
	t.Helper()
	c, err := load(dir, reg, env)
	if err != nil {
		t.Fatalf("Load(%s, %s): %v", dir, env, err)
	}
	return c
}

func mustRefuse(t *testing.T, dir, reg string, env Env, substr string) {
	t.Helper()
	c, err := load(dir, reg, env)
	if err == nil {
		t.Fatalf("Load(%s) succeeded, want refusal containing %q", env, substr)
	}
	if c != nil {
		t.Fatal("a refused load must return no config")
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v\nwant it to contain %q", err, substr)
	}
}

// --- India pack and synthetic pack ---

func TestLoadIndiaDraftPack(t *testing.T) {
	c := mustLoad(t, inDir, registry, EnvTest)
	p := c.Pack()
	if p.Code() != "IN" || p.Status() != "draft" || p.IsApproved() {
		t.Fatalf("unexpected pack header: %s %s", p.Code(), p.Status())
	}
	if p.Money().MinorUnits != 2 || p.Locale().DefaultTimeZone == "" || p.Emergency().PrimaryNumber == "" {
		t.Fatal("pack values not loaded")
	}
	if got := p.Messaging().ChannelsEnabled["whatsapp"]; got {
		t.Fatal("WhatsApp must be off")
	}
	if p.Adapter("esign") != "none" || p.Adapter("whatsapp") != "none" {
		t.Fatal("esign and whatsapp must be none")
	}
	if p.Adapter("payments") != "none" || p.Adapter("sms") != "none" {
		t.Fatal("unfinished adapter placeholders must read as none")
	}
	if len(c.Warnings()) == 0 {
		t.Fatal("a draft India pack in test must carry warnings")
	}
	if len(p.Clinical().PrescribingRules) == 0 || len(p.Payments().RefundPolicy) == 0 || len(p.Messaging().SMSRegistry) == 0 {
		t.Fatal("referenced files not loaded")
	}
	if p.Retention()["RET-18"]["state"] != "restricted_no_staff_access" {
		t.Fatalf("RET-18 = %v", p.Retention()["RET-18"])
	}
	if p.Legal().Grievance.ResolveDays != 7 || p.Legal().ConsentTextVersions["P-CARE"] != 2 {
		t.Fatal("legal block not loaded")
	}
	if c.Flag("symptom_search") || c.Flag("unknown_flag") {
		t.Fatal("flags must be off")
	}
	v := p.RuleSetVersions()
	if v.Pack != "0.1.0-draft" || v.PrescribingRules != "1.1.0-draft" || v.RefundPolicy != "1.0.0-draft" || len(v.FileSHA256) != 5 {
		t.Fatalf("RuleSetVersions = %+v", v)
	}
}

func TestTC53SyntheticPackXX(t *testing.T) {
	xx := mustLoad(t, xxDir, registry, EnvTest).Pack()
	in := mustLoad(t, inDir, registry, EnvTest).Pack()

	if xx.Code() != "XX" || !xx.IsApproved() {
		t.Fatalf("XX header: %s %s", xx.Code(), xx.Status())
	}
	// Every country value comes from the pack. The expected values are written here, in test
	// code, and must differ from the India pack so that a hard-coded India value would fail.
	if xx.Money().Currency != "EUR" || xx.Emergency().PrimaryNumber != "999" ||
		xx.Locale().DefaultTimeZone != "Europe/Lisbon" || xx.People().PhoneCountryCodes[0] != "+351" ||
		xx.Locale().DefaultLocale != "pt-PT" {
		t.Fatalf("XX values not taken from the pack: %+v %+v", xx.Money(), xx.Emergency())
	}
	if xx.Money().Currency == in.Money().Currency || xx.Emergency().PrimaryNumber == in.Emergency().PrimaryNumber ||
		xx.Locale().DefaultTimeZone == in.Locale().DefaultTimeZone ||
		xx.People().PhoneCountryCodes[0] == in.People().PhoneCountryCodes[0] {
		t.Fatal("XX must differ from IN in every country value")
	}
	if !strings.Contains(string(xx.Clinical().DrugClassification), "xxdrugalpha") {
		t.Fatal("XX drug table not loaded")
	}
	if strings.Contains(string(xx.Clinical().DrugClassification), "xxdrugalpha") == strings.Contains(string(in.Clinical().DrugClassification), "xxdrugalpha") {
		t.Fatal("drug tables must differ")
	}
	if xx.RuleSetVersions().Pack != "1.0.0" || xx.RuleSetVersions().RefundPolicy != "1.0.0" {
		t.Fatalf("XX versions: %+v", xx.RuleSetVersions())
	}
	// Fake adapters never run outside local, test and dev.
	mustRefuse(t, xxDir, registry, EnvProduction, "fake adapter")
	mustRefuse(t, xxDir, registry, EnvStaging, "fake adapter")
	// The same pack with ordinary registered vendors loads in production.
	dir, reg := realisticPack(t)
	mustLoad(t, dir, reg, EnvProduction)
}

func TestCommittedPackChecksumsAreCurrent(t *testing.T) {
	for _, dir := range []string{inDir, xxDir} {
		want, err := ComputeChecksums(dir)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
		var m manifest
		if err := strictDecode(raw, &m); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.Pack.Checksums, want) {
			t.Fatalf("%s: checksums are stale; run cmd/packsum", dir)
		}
	}
}

func TestSMSRegistryVariablesMatchText(t *testing.T) {
	for _, dir := range []string{inDir, xxDir} {
		b, err := os.ReadFile(filepath.Join(dir, "messaging/sms-templates.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Templates []struct {
				ID        string `yaml:"id"`
				Text      string `yaml:"text"`
				Variables []struct {
					Name string `yaml:"name"`
					Type string `yaml:"type"`
				} `yaml:"variables"`
			} `yaml:"templates"`
		}
		if err := yaml.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		if len(f.Templates) == 0 {
			t.Fatal("no templates")
		}
		for _, tpl := range f.Templates {
			if n := strings.Count(tpl.Text, "{#var#}"); n != len(tpl.Variables) || n > 2 {
				t.Errorf("%s: %d placeholders, %d typed variables", tpl.ID, n, len(tpl.Variables))
			}
			for _, v := range tpl.Variables {
				if v.Type != "numeric" && v.Type != "alphanumeric" {
					t.Errorf("%s: variable %q has type %q", tpl.ID, v.Name, v.Type)
				}
			}
		}
	}
}

// --- TC-59 and checksum rules ---

func TestTC59BadChecksumRefused(t *testing.T) {
	for _, rel := range []string{"rules/telemedicine-rules.yaml", "payments/refund-policy.yaml", "drugs/classification.yaml"} {
		dir := tempPack(t, xxDir)
		f, err := os.OpenFile(filepath.Join(dir, rel), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("# tampered\n")
		f.Close()
		mustRefuse(t, dir, registry, EnvTest, "checksum mismatch")
	}
}

func TestMissingChecksumRefused(t *testing.T) {
	dir := tempPack(t, xxDir)
	raw, _ := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	re := regexp.MustCompile(`(?m)^    tax/xx-tax.yaml: .*\n`)
	os.WriteFile(filepath.Join(dir, "pack.yaml"), re.ReplaceAll(raw, nil), 0o644)
	mustRefuse(t, dir, registry, EnvTest, "no checksum for referenced file")
}

func TestExtraChecksumRefused(t *testing.T) {
	dir := tempPack(t, xxDir)
	edit(t, dir, "pack.yaml", "    tax/xx-tax.yaml:", "    other.yaml: "+strings.Repeat("0", 64)+"\n    tax/xx-tax.yaml:")
	mustRefuse(t, dir, registry, EnvTest, "not a file referenced")
}

func TestMissingReferencedFileRefused(t *testing.T) {
	dir := tempPack(t, xxDir)
	os.Remove(filepath.Join(dir, "tax/xx-tax.yaml"))
	mustRefuse(t, dir, registry, EnvTest, "xx-tax.yaml")
}

func TestReferencedFileMustParseAsYAML(t *testing.T) {
	dir := tempPack(t, xxDir)
	os.WriteFile(filepath.Join(dir, "tax/xx-tax.yaml"), []byte("version: [unclosed\n"), 0o644)
	sums, _ := ComputeChecksums(dir)
	edit(t, dir, "pack.yaml", "tax/xx-tax.yaml: "+mustChecksum(t, xxDir, "tax/xx-tax.yaml"), "tax/xx-tax.yaml: "+sums["tax/xx-tax.yaml"])
	mustRefuse(t, dir, registry, EnvTest, "xx-tax.yaml")
}

func mustChecksum(t *testing.T, dir, rel string) string {
	t.Helper()
	s, err := ComputeChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s[rel]
}

func TestPathTraversalRefused(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("version: 1.0.0\n"), 0o644)
	cases := map[string]string{
		"parent":    "../" + filepath.Base(outside) + "/evil.yaml",
		"deep":      "tax/../../evil.yaml",
		"absolute":  filepath.ToSlash(filepath.Join(outside, "evil.yaml")),
		"slash":     "/etc/passwd",
		"backslash": `tax\..\evil.yaml`,
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			dir := tempPack(t, xxDir)
			edit(t, dir, "pack.yaml", "tax: tax/xx-tax.yaml", "tax: \""+strings.ReplaceAll(ref, `\`, `\\`)+"\"")
			mustRefuse(t, dir, registry, EnvTest, "not a relative path inside the pack directory")
		})
	}
}

func TestSymlinkEscapeRefused(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "evil.yaml"), []byte("version: 1.0.0\n"), 0o644)
	dir := tempPack(t, xxDir)
	os.Remove(filepath.Join(dir, "tax/xx-tax.yaml"))
	if err := os.Symlink(filepath.Join(outside, "evil.yaml"), filepath.Join(dir, "tax/xx-tax.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mustRefuse(t, dir, registry, EnvTest, "xx-tax.yaml")
}

// --- strict schema ---

func TestStrictDecodeAndRequiredKeys(t *testing.T) {
	t.Run("unknown key", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "  units: metric", "  units: metric\n  surprise: 1")
		mustRefuse(t, dir, registry, EnvTest, "surprise")
	})
	t.Run("duplicate key", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "  units: metric", "  units: metric\n  units: imperial")
		mustRefuse(t, dir, registry, EnvTest, "already defined")
	})
	t.Run("wrong type", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "minor_units: 2", "minor_units: two")
		mustRefuse(t, dir, registry, EnvTest, "pack.yaml")
	})
	t.Run("feature not bool", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "sos: true", "sos: maybe")
		mustRefuse(t, dir, registry, EnvTest, "pack.yaml")
	})
	t.Run("two documents", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		f, _ := os.OpenFile(filepath.Join(dir, "pack.yaml"), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString("---\nextra: 1\n")
		f.Close()
		mustRefuse(t, dir, registry, EnvTest, "more than one YAML document")
	})
	for _, missing := range []struct{ name, line string }{
		{"currency", "  currency: EUR\n"},
		{"minors_supported", "  minors_supported: false\n"},
		{"quiet_hours", "  quiet_hours: { start: \"22:00\", end: \"07:30\" }\n"},
		{"features", "features:\n  symptom_search: true\n  sos: true\n  family_profiles: false\n  prescription_scan: false\n"},
		{"adapters", "adapters:\n"},
	} {
		t.Run("missing "+missing.name, func(t *testing.T) {
			dir := tempPack(t, xxDir)
			edit(t, dir, "pack.yaml", missing.line, "")
			c, err := load(dir, registry, EnvTest)
			if err == nil || c != nil {
				t.Fatalf("a pack without %s must be refused", missing.name)
			}
		})
	}
	t.Run("empty required value", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "region_primary: xx-region-1", "region_primary:")
		mustRefuse(t, dir, registry, EnvTest, "region_primary")
	})
	t.Run("missing version in file", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		os.WriteFile(filepath.Join(dir, "tax/xx-tax.yaml"), []byte("status: approved\n"), 0o644)
		edit(t, dir, "pack.yaml", "tax/xx-tax.yaml: "+mustChecksum(t, xxDir, "tax/xx-tax.yaml"), "tax/xx-tax.yaml: "+mustChecksum(t, dir, "tax/xx-tax.yaml"))
		mustRefuse(t, dir, registry, EnvTest, "version")
	})
	t.Run("file disagrees with manifest", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "payments/refund-policy.yaml", "currency: EUR", "currency: GBP")
		edit(t, dir, "pack.yaml", "payments/refund-policy.yaml: "+mustChecksum(t, xxDir, "payments/refund-policy.yaml"), "payments/refund-policy.yaml: "+mustChecksum(t, dir, "payments/refund-policy.yaml"))
		mustRefuse(t, dir, registry, EnvTest, "currency")
	})
}

func TestValueRules(t *testing.T) {
	cases := []struct{ name, old, repl, want string }{
		{"phone code", `["+351"]`, `["351"]`, "+NN"},
		{"phone code letters", `["+351"]`, `["+ab"]`, "+NN"},
		{"time zone", "Europe/Lisbon", "Mars/Olympus", "IANA"},
		{"time zone UTC", "default_time_zone: Europe/Lisbon", "default_time_zone: UTC", "IANA"},
		{"currency lower", "currency: EUR", "currency: eur", "three upper-case"},
		{"currency long", "currency: EUR", "currency: EURO", "three upper-case"},
		{"minor units high", "minor_units: 2", "minor_units: 5", "0 to 4"},
		{"minor units negative", "minor_units: 2", "minor_units: -1", "0 to 4"},
		{"quiet start", `start: "22:00"`, `start: "25:00"`, "HH:MM"},
		{"quiet end", `end: "07:30"`, `end: "7:30"`, "HH:MM"},
		{"locale", "default_locale: pt-PT", "default_locale: p", "locale"},
		{"default locale unsupported", "default_locale: pt-PT", "default_locale: fr-FR", "supported_locales"},
		{"unknown validator", "validator: digits_16", "validator: made_up", "unknown validator"},
		{"country code", "vendor_processing_must_be_in: [PT]", "vendor_processing_must_be_in: [Portugal]", "country code"},
		{"pack code", "code: XX", "code: xx1", "pack.code"},
		{"semver", "version: 1.0.0\n  status", "version: one\n  status", "semver"},
		{"status", "status: approved\n  checksums", "status: maybe\n  checksums", "pack.status"},
		{"retired", "status: approved\n  checksums", "status: retired\n  checksums", "retired"},
		{"emergency", `primary_number: "999"`, `primary_number: "nine"`, "emergency.primary_number"},
		{"adults only", "min_account_age: 18", "min_account_age: 16", "K-DP-10"},
		{"grievance timer", "acknowledge_hours: 48", "acknowledge_hours: 0", "acknowledge_hours"},
		{"unknown adapter kind", "  ocr: none", "  ocr: none\n  teleport: none", "teleport"},
		{"payments adapter mismatch", "  adapter: fake-payments", "  adapter: fake-sms", "differs"},
		{"unknown channel", "whatsapp: false }", "whatsapp: false, telegram: true }", "telegram"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempPack(t, xxDir)
			edit(t, dir, "pack.yaml", tc.old, tc.repl)
			mustRefuse(t, dir, registry, EnvTest, tc.want)
		})
	}
	t.Run("unknown environment", func(t *testing.T) {
		mustRefuse(t, xxDir, registry, Env("prod"), "unknown environment")
	})
	t.Run("missing pack dir", func(t *testing.T) {
		mustRefuse(t, filepath.Join(t.TempDir(), "nope"), registry, EnvTest, "config:")
	})
}

// --- status, placeholders ---

func TestDraftPackRefusedInProduction(t *testing.T) {
	mustRefuse(t, inDir, registry, EnvProduction, "production loads only approved")
	dir, reg := realisticPack(t)
	edit(t, dir, "pack.yaml", "status: approved", "status: draft")
	mustRefuse(t, dir, reg, EnvProduction, "production loads only approved")
	mustLoad(t, dir, reg, EnvStaging) // staging may load drafts
}

func TestPlaceholderRefusedInStaging(t *testing.T) {
	mustRefuse(t, inDir, registry, EnvStaging, "unfinished value")
	// Missing values ([FILL, [S-0..]) are refused in staging; pending approvals ([CONFIRM,
	// [VALIDATE, [CA, [CLINICAL) are allowed there (drafts may load in staging) but never in production.
	for _, marker := range []string{"[FILL: region]", "[S-040]", "[CONFIRM founder]", "[VALIDATE lawyer]", "[CA]", "[CLINICAL]"} {
		dir, reg := realisticPack(t)
		edit(t, dir, "pack.yaml", "region_primary: xx-region-1", `region_primary: "`+marker+`"`)
		if strings.HasPrefix(marker, "[FILL") || strings.HasPrefix(marker, "[S-0") {
			mustRefuse(t, dir, reg, EnvStaging, "unfinished value")
		} else {
			mustLoad(t, dir, reg, EnvStaging)
		}
		mustRefuse(t, dir, reg, EnvProduction, "unfinished value")
		mustLoad(t, dir, reg, EnvDev) // synthetic data only: allowed
	}
	// A placeholder inside a referenced file counts too.
	dir, reg := realisticPack(t)
	edit(t, dir, "tax/xx-tax.yaml", "rate_pct: 23", `rate_pct: "[CA: confirm]"`)
	edit(t, dir, "pack.yaml", "tax/xx-tax.yaml: "+mustChecksum(t, xxDir, "tax/xx-tax.yaml"), "tax/xx-tax.yaml: "+mustChecksum(t, dir, "tax/xx-tax.yaml"))
	mustRefuse(t, dir, reg, EnvProduction, "xx-tax.yaml")
}

func TestStagingAndProductionLoadFinishedPack(t *testing.T) {
	dir, reg := realisticPack(t)
	for _, env := range []Env{EnvStaging, EnvProduction} {
		c := mustLoad(t, dir, reg, env)
		if len(c.Warnings()) != 0 {
			t.Fatalf("%s: unexpected warnings %v", env, c.Warnings())
		}
		if c.Pack().Adapter("sms") != "acme-sms" {
			t.Fatal("adapter id lost")
		}
	}
}

// --- vendor register ---

func TestUnknownVendorRefused(t *testing.T) {
	for _, env := range []Env{EnvTest, EnvStaging} {
		dir, reg := realisticPack(t)
		edit(t, dir, "pack.yaml", "  sms: acme-sms", "  sms: mystery-sms")
		mustRefuse(t, dir, reg, env, "not in the vendor register")
	}
}

func TestVendorWithoutAgreementOrWrongCountry(t *testing.T) {
	t.Run("no agreement", func(t *testing.T) {
		dir, reg := realisticPack(t)
		edit(t, filepath.Dir(reg), filepath.Base(reg), "id: acme-sms, kind: sms, processor_agreement: true", "id: acme-sms, kind: sms, processor_agreement: false")
		mustRefuse(t, dir, reg, EnvProduction, "no processor agreement")
		mustRefuse(t, dir, reg, EnvStaging, "no processor agreement")
		c := mustLoad(t, dir, reg, EnvDev) // synthetic data only
		if len(c.Warnings()) == 0 {
			t.Fatal("expected a warning in dev")
		}
	})
	t.Run("processes outside the allowed countries", func(t *testing.T) {
		dir, reg := realisticPack(t)
		edit(t, filepath.Dir(reg), filepath.Base(reg), "id: acme-push, kind: push, processor_agreement: true, processing_countries: [PT]", "id: acme-push, kind: push, processor_agreement: true, processing_countries: [PT, US]")
		mustRefuse(t, dir, reg, EnvProduction, "outside")
	})
	t.Run("approved exception allows it", func(t *testing.T) {
		dir, reg := realisticPack(t)
		edit(t, filepath.Dir(reg), filepath.Base(reg), "id: acme-push, kind: push, processor_agreement: true, processing_countries: [PT]", "id: acme-push, kind: push, processor_agreement: false, processing_countries: [US]")
		edit(t, dir, "pack.yaml", "  exceptions: []", "  exceptions:\n    - { vendor: acme-push, data: [push_token], reason: no health data, approved_by: lawyer }")
		mustLoad(t, dir, reg, EnvProduction)
	})
	t.Run("unapproved exception does not", func(t *testing.T) {
		dir, reg := realisticPack(t)
		edit(t, filepath.Dir(reg), filepath.Base(reg), "id: acme-push, kind: push, processor_agreement: true, processing_countries: [PT]", "id: acme-push, kind: push, processor_agreement: false, processing_countries: [US]")
		edit(t, dir, "pack.yaml", "  exceptions: []", "  exceptions:\n    - { vendor: acme-push, data: [push_token], reason: no health data, approved_by: null }")
		mustRefuse(t, dir, reg, EnvProduction, "no processor agreement")
	})
	t.Run("kind mismatch and retired", func(t *testing.T) {
		dir, reg := realisticPack(t)
		edit(t, dir, "pack.yaml", "  sms: acme-sms", "  sms: acme-email")
		mustRefuse(t, dir, reg, EnvTest, "registered as kind")
		dir, reg = realisticPack(t)
		edit(t, filepath.Dir(reg), filepath.Base(reg), "id: acme-sms, kind: sms, processor_agreement: true, processing_countries: [PT], status: approved", "id: acme-sms, kind: sms, processor_agreement: true, processing_countries: [PT], status: retired")
		mustRefuse(t, dir, reg, EnvTest, "retired")
	})
}

func TestIndiaPushVendorBlockedUntilLawyerApproves(t *testing.T) {
	// The real draft pack and register: push-fcm-apns has no processor agreement and its
	// exception has no approver, so it can only be used with synthetic data (local, test, dev).
	c := mustLoad(t, inDir, registry, EnvLocal)
	found := false
	for _, w := range c.Warnings() {
		if strings.Contains(w, "push-fcm-apns") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a push-fcm-apns warning, got %v", c.Warnings())
	}
}

func TestFakeAdaptersOnlyInLocalTestDev(t *testing.T) {
	for _, env := range []Env{EnvLocal, EnvTest, EnvDev} {
		mustLoad(t, xxDir, registry, env)
	}
	for _, env := range []Env{EnvStaging, EnvProduction} {
		mustRefuse(t, xxDir, registry, env, "fake adapter")
	}
	// A fake vendor whose register entry has no environment list is still refused by name.
	dir, reg := realisticPack(t)
	edit(t, dir, "pack.yaml", "  sms: acme-sms", "  sms: fake-sms")
	os.WriteFile(reg, append(mustRead(t, reg), []byte("  - { id: fake-sms, kind: sms, processor_agreement: true, processing_countries: [PT], status: approved }\n")...), 0o644)
	mustRefuse(t, dir, reg, EnvProduction, "fake adapter")
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWhatsAppOnHold(t *testing.T) {
	t.Run("channel true", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "whatsapp: false }", "whatsapp: true }")
		mustRefuse(t, dir, registry, EnvTest, "WhatsApp is on hold")
	})
	t.Run("adapter set", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", "  whatsapp: none", "  whatsapp: acme-whatsapp")
		mustRefuse(t, dir, registry, EnvTest, "WhatsApp is on hold")
	})
	t.Run("channel must be stated", func(t *testing.T) {
		dir := tempPack(t, xxDir)
		edit(t, dir, "pack.yaml", ", whatsapp: false }", " }")
		mustRefuse(t, dir, registry, EnvTest, "whatsapp")
	})
	t.Run("repo register has no WhatsApp vendor", func(t *testing.T) {
		reg, err := loadRegister(registry)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range reg {
			if v.Kind == "whatsapp" {
				t.Fatalf("vendor %s: WhatsApp is on hold", v.ID)
			}
		}
	})
}

func TestVendorRegisterIsStrict(t *testing.T) {
	dir, _ := realisticPack(t)
	bad := map[string]string{
		"unknown key":    "vendors:\n  - { id: a, kind: sms, status: approved, processor_agreement: true, processing_countries: [], extra: 1 }\n",
		"unknown kind":   "vendors:\n  - { id: a, kind: telepathy, status: approved, processor_agreement: true, processing_countries: [] }\n",
		"unknown status": "vendors:\n  - { id: a, kind: sms, status: great, processor_agreement: true, processing_countries: [] }\n",
		"duplicate":      "vendors:\n  - { id: a, kind: sms, status: approved, processor_agreement: true, processing_countries: [] }\n  - { id: a, kind: sms, status: approved, processor_agreement: true, processing_countries: [] }\n",
		"bad env":        "vendors:\n  - { id: a, kind: sms, status: approved, processor_agreement: true, processing_countries: [], environments: [moon] }\n",
		"no id":          "vendors:\n  - { kind: sms, status: approved, processor_agreement: true, processing_countries: [] }\n",
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "v.yaml")
			os.WriteFile(p, []byte(content), 0o644)
			mustRefuse(t, dir, p, EnvTest, "vendor register")
		})
	}
	t.Run("missing file", func(t *testing.T) {
		mustRefuse(t, dir, filepath.Join(t.TempDir(), "none.yaml"), EnvTest, "vendor register")
	})
}

// --- immutability and provider ---

func TestPackViewsAreCopies(t *testing.T) {
	c := mustLoad(t, xxDir, registry, EnvTest)
	p := c.Pack()
	p.People().PhoneCountryCodes[0] = "+00"
	p.Locale().SupportedLocales[0] = "zz"
	p.Clinical().PrescribingRules[0] = 'X'
	p.Messaging().ChannelsEnabled["whatsapp"] = true
	p.Adapters()["sms"] = "evil"
	p.Features()["sos"] = false
	p.Retention()["RET-01"]["period"] = "never"
	p.RuleSetVersions().FileSHA256["x"] = "y"
	p.Legal().ConsentTextVersions["P-CARE"] = 99
	if p.People().PhoneCountryCodes[0] != "+351" || p.Locale().SupportedLocales[0] != "pt-PT" ||
		p.Clinical().PrescribingRules[0] == 'X' || p.Messaging().ChannelsEnabled["whatsapp"] ||
		p.Adapter("sms") != "fake-sms" || !p.Features()["sos"] || p.Retention()["RET-01"]["period"] != "account_life" ||
		len(p.RuleSetVersions().FileSHA256) != 5 || p.Legal().ConsentTextVersions["P-CARE"] != 1 {
		t.Fatal("a caller changed the pack")
	}
}

func TestConfigIsAProvider(t *testing.T) {
	var p public.Provider = mustLoad(t, xxDir, registry, EnvTest)
	if p.Pack().Code() != "XX" || !p.Flag("sos") || p.Flag("nope") {
		t.Fatal("provider misbehaves")
	}
}

func TestFlagOverridesOnLoad(t *testing.T) {
	c, err := Load(Options{PackDir: xxDir, VendorRegisterPath: registry, Env: EnvTest, FlagOverrides: map[string]bool{"sos": false, "new_booking_ui": true, "adapter.sms.enabled": false}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Flag("sos") || !c.Flag("new_booking_ui") || c.Flag("adapter.sms.enabled") {
		t.Fatal("overrides not applied")
	}
	_, err = Load(Options{PackDir: xxDir, VendorRegisterPath: registry, Env: EnvTest, FlagOverrides: map[string]bool{"consent_checks": false}})
	if !errors.Is(err, ErrNonFlaggable) {
		t.Fatalf("err = %v, want ErrNonFlaggable", err)
	}
}

// --- feature flags ---

func TestNonFlaggableFlagRefused(t *testing.T) {
	bad := []string{
		"consent", "consent_gate", "skip-consent", "disable.consent.checks", "ConsentBypass",
		"rules_engine", "rules_engine_off", "rulesengine", "rules-engine", "rule_engine",
		"prescribing_gate", "prescribing_gate_off", "prescribe_bypass", "prescription_signing_prescribing",
		"audit", "audit_logging", "audit.disabled", "no_audit",
		"encryption", "field_encryption", "encrypt_pii", "disable_encryption",
	}
	for _, name := range bad {
		if _, err := NewFlags(map[string]bool{name: true}, nil); !errors.Is(err, ErrNonFlaggable) {
			t.Errorf("base flag %q: err = %v, want ErrNonFlaggable", name, err)
		}
		if _, err := NewFlags(nil, map[string]bool{name: false}); !errors.Is(err, ErrNonFlaggable) {
			t.Errorf("override %q: err = %v, want ErrNonFlaggable", name, err)
		}
	}
}

func TestNonFlaggableFlagInPackRefused(t *testing.T) {
	dir := tempPack(t, xxDir)
	edit(t, dir, "pack.yaml", "  sos: true", "  sos: true\n  consent_enforcement: false")
	mustRefuse(t, dir, registry, EnvTest, "non-flaggable")
}

func TestAllowedFlags(t *testing.T) {
	good := []string{"symptom_search", "sos", "family_profiles", "prescription_scan", "new_booking_ui", "adapter.sms.enabled", "adapter.payments.enabled", "adapter.age_verification.enabled"}
	f, err := NewFlags(map[string]bool{good[0]: true, good[1]: false}, map[string]bool{good[2]: true, good[3]: true, good[4]: true, good[5]: false, good[6]: true, good[7]: true})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Enabled("symptom_search") || f.Enabled("sos") || f.Enabled("adapter.sms.enabled") || !f.Enabled("adapter.payments.enabled") {
		t.Fatal("flag values wrong")
	}
	if f.Enabled("never_declared") {
		t.Fatal("unknown flag must be off")
	}
	var nilFlags *Flags
	if nilFlags.Enabled("x") {
		t.Fatal("nil flags must be off")
	}
	if len(f.Names()) != 8 {
		t.Fatalf("names = %v", f.Names())
	}
}

func TestBadFlagNamesRefused(t *testing.T) {
	for _, name := range []string{"", "Upper", "1abc", "has space", "adapter.teleport.enabled", "adapter..enabled", strings.Repeat("a", 65)} {
		if _, err := NewFlags(map[string]bool{name: true}, nil); err == nil {
			t.Errorf("flag %q accepted", name)
		}
	}
}

// --- K-GLB-01: no country values in code ---

func TestNoCountryLiteralsInGoCode(t *testing.T) {
	banned := regexp.MustCompile(`"\+91"|"INR"|"Asia/Kolkata"|"112"|"108"|"en-IN"`)
	for _, root := range []string{".", "public", "../../../cmd/packsum"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b := mustRead(t, filepath.Join(root, e.Name()))
			if m := banned.Find(b); m != nil {
				t.Errorf("%s contains country literal %s (K-GLB-01)", e.Name(), m)
			}
		}
	}
}

// Doctors' identity evidence can never be a prohibited identifier (K-DP-05, AAD-04).
func TestAcceptedPhotoIDsExcludeProhibited(t *testing.T) {
	dir, reg := realisticPack(t)
	edit(t, dir, "pack.yaml", "accepted_photo_id_types: [national_id_card, passport]", "accepted_photo_id_types: [passport, fake_national_id]")
	mustRefuse(t, dir, reg, EnvDev, "accepted_photo_id_types")
	dir2, reg2 := realisticPack(t)
	edit(t, dir2, "pack.yaml", "accepted_photo_id_types: [national_id_card, passport]", "accepted_photo_id_types: []")
	mustRefuse(t, dir2, reg2, EnvDev, "accepted_photo_id_types")
}
