package archtest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// ---- B4 / K-ARC-01: a module imports another module only via .../public ----

func checkB4ModuleImports(files []srcFile) []string {
	parsed, v := parseGoFiles(files)
	const pre = "internal/modules/"
	for _, p := range parsed {
		if p.Mod != modBackend || !strings.HasPrefix(p.Rel, pre) {
			continue
		}
		own, _, _ := strings.Cut(strings.TrimPrefix(p.Rel, pre), "/")
		for _, s := range importPaths(p) {
			rel, ok := backendRel(impPath(s))
			if !ok || !strings.HasPrefix(rel, pre) {
				continue
			}
			other, rest, _ := strings.Cut(strings.TrimPrefix(rel, pre), "/")
			if other == own {
				continue
			}
			// Tests may import another module's root package only to apply its migrations
			// (Source()) when building a test database; never its internals or HTTP layer.
			if p.isTest() && rest == "" {
				continue
			}
			if rest != "public" && !strings.HasPrefix(rest, "public/") {
				v = append(v, fmt.Sprintf("%s: B4/K-ARC-01 module %q imports %q; only modules/%s/public is allowed", p.at(s), own, impPath(s), other))
			}
		}
	}
	return v
}

func TestB4_ModuleImports(t *testing.T) { requireClean(t, checkB4ModuleImports(loadTree(t))) }

func TestB4_ModuleImports_Negative(t *testing.T) {
	bad := []srcFile{be("internal/modules/booking/a.go", goSrc(backendModule+"/internal/modules/payments/internal/db"))}
	requireFires(t, checkB4ModuleImports(bad), "B4/K-ARC-01")
	bad = []srcFile{be("internal/modules/booking/a.go", goSrc(backendModule+"/internal/modules/payments"))}
	requireFires(t, checkB4ModuleImports(bad), "B4/K-ARC-01")
	ok := []srcFile{
		be("internal/modules/booking/a.go", goSrc(backendModule+"/internal/modules/payments/public", backendModule+"/internal/modules/booking/store")),
		be("internal/modules/booking/b.go", goSrc(backendModule+"/internal/platform/ids")),
	}
	requireClean(t, checkB4ModuleImports(ok))
}

// ---- B7: platform imports no module, adapter or app ----

func checkB7PlatformImports(files []srcFile) []string {
	parsed, v := parseGoFiles(files)
	for _, p := range parsed {
		if p.Mod != modBackend || !strings.HasPrefix(p.Rel, "internal/platform/") {
			continue
		}
		for _, s := range importPaths(p) {
			rel, ok := backendRel(impPath(s))
			if !ok {
				continue
			}
			for _, banned := range []string{"internal/modules", "internal/adapters", "internal/app"} {
				if hasPathPrefix(rel, banned) {
					v = append(v, fmt.Sprintf("%s: B7 internal/platform must not import %s (%q)", p.at(s), banned, impPath(s)))
				}
			}
		}
	}
	return v
}

func TestB7_PlatformImports(t *testing.T) { requireClean(t, checkB7PlatformImports(loadTree(t))) }

func TestB7_PlatformImports_Negative(t *testing.T) {
	for _, imp := range []string{"/internal/modules/audit/public", "/internal/adapters/sms", "/internal/app"} {
		bad := []srcFile{be("internal/platform/ids/a.go", goSrc(backendModule+imp))}
		requireFires(t, checkB7PlatformImports(bad), "B7")
	}
	requireClean(t, checkB7PlatformImports([]srcFile{be("internal/platform/ids/a.go", goSrc(backendModule+"/internal/platform/problem"))}))
}

// ---- A1 / D-19: only internal/app and cmd import vendor adapter packages ----

func checkA1VendorAdapters(files []srcFile) []string {
	parsed, v := parseGoFiles(files)
	const pre = "internal/adapters/"
	for _, p := range parsed {
		if p.Mod != modBackend || hasPathPrefix(p.Rel, "internal/app") || hasPathPrefix(p.Rel, "cmd") {
			continue
		}
		for _, s := range importPaths(p) {
			rel, ok := backendRel(impPath(s))
			if !ok || !strings.HasPrefix(rel, pre) {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(rel, pre), "/")
			if len(parts) < 2 {
				continue // port package internal/adapters/<kind>
			}
			own := pre + parts[0] + "/" + parts[1] + "/"
			if strings.HasPrefix(p.Rel, own) {
				continue // the vendor package itself
			}
			// Tests may use the test doubles (adapter rule A10), never a real vendor.
			if p.isTest() && parts[1] == "fake" {
				continue
			}
			v = append(v, fmt.Sprintf("%s: A1/D-19 only internal/app and cmd may import vendor adapter %q", p.at(s), impPath(s)))
		}
	}
	return v
}

func TestA1_VendorAdapters(t *testing.T) { requireClean(t, checkA1VendorAdapters(loadTree(t))) }

func TestA1_VendorAdapters_Negative(t *testing.T) {
	bad := []srcFile{be("internal/modules/notifications/a.go", goSrc(backendModule+"/internal/adapters/sms/msg91"))}
	requireFires(t, checkA1VendorAdapters(bad), "A1/D-19")
	ok := []srcFile{
		be("internal/modules/notifications/a.go", goSrc(backendModule+"/internal/adapters/sms")),
		be("internal/app/a.go", goSrc(backendModule+"/internal/adapters/sms/msg91")),
		be("cmd/api/main.go", goSrc(backendModule+"/internal/adapters/sms/msg91")),
		be("internal/adapters/sms/msg91/a.go", goSrc(backendModule+"/internal/adapters/sms", backendModule+"/internal/adapters/sms/msg91/wire")),
	}
	requireClean(t, checkA1VendorAdapters(ok))
}

// ---- D-13 dependency allow-list (docs/arch/dependencies.md) and ADR-31 cloud SDK placement ----

// allowedThirdParty mirrors docs/arch/dependencies.md ("Backend and scheduler-core"). Keep the two in step:
// adding a line is a reviewed change, a new library family or vendor needs an ADR.
var allowedThirdParty = []string{
	"github.com/go-chi/chi/v5",
	"github.com/oapi-codegen/runtime",
	"github.com/oapi-codegen/nethttp-middleware",
	"github.com/getkin/kin-openapi",
	"github.com/jackc/pgx/v5",
	"github.com/riverqueue/river",
	"github.com/google/uuid",
	"go.yaml.in/yaml/v3",
	"github.com/pquerna/otp",
	"github.com/teambition/rrule-go",
	"go.opentelemetry.io/otel",
	"golang.org/x/crypto", // only inside the crypto paths, see checkCryptoImports
}

// testOnlyThirdParty may appear only in _test.go files and internal/testkit (ADR-25).
var testOnlyThirdParty = []string{"github.com/testcontainers/testcontainers-go"}

// cloudSDKs may appear only under internal/adapters/<kind>/<vendor>/ (dual-cloud rule, ADR-31, K-ARC-03).
var cloudSDKs = []string{"github.com/aws/", "github.com/oracle/oci-go-sdk", "cloud.google.com/", "github.com/Azure/"}

var vendorAdapterPath = regexp.MustCompile(`^internal/adapters/[^/]+/[^/]+/`)

func isThirdParty(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return strings.Contains(first, ".")
}

func matchesAny(imp string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(imp, p) {
				return true
			}
			continue
		}
		if hasPathPrefix(imp, p) {
			return true
		}
	}
	return false
}

func checkD13Dependencies(files []srcFile) []string {
	parsed, v := parseGoFiles(files)
	for _, p := range parsed {
		own := backendModule
		if p.Mod == modScheduler {
			own = schedulerModule
		}
		for _, s := range importPaths(p) {
			imp := impPath(s)
			if !isThirdParty(imp) || hasPathPrefix(imp, own) {
				continue
			}
			switch {
			case matchesAny(imp, cloudSDKs):
				if !(p.Mod == modBackend && vendorAdapterPath.MatchString(p.Rel)) {
					v = append(v, fmt.Sprintf("%s: D-13/ADR-31 cloud SDK %q only under internal/adapters/<kind>/<vendor>/", p.at(s), imp))
				}
			case matchesAny(imp, testOnlyThirdParty):
				if !(p.isTest() || (p.Mod == modBackend && hasPathPrefix(p.Rel, "internal/testkit"))) {
					v = append(v, fmt.Sprintf("%s: D-13 %q only in _test.go files and internal/testkit", p.at(s), imp))
				}
			case p.isTest():
				// D-13 constrains shipped code; test files are still bound by the cloud and crypto rules.
			case !matchesAny(imp, allowedThirdParty):
				v = append(v, fmt.Sprintf("%s: D-13 %q is not in docs/arch/dependencies.md", p.at(s), imp))
			}
		}
	}
	return v
}

func TestD13_DependencyAllowList(t *testing.T) { requireClean(t, checkD13Dependencies(loadTree(t))) }

func TestD13_DependencyAllowList_Negative(t *testing.T) {
	requireFires(t, checkD13Dependencies([]srcFile{be("internal/platform/x/a.go", goSrc("github.com/redis/go-redis/v9"))}), "D-13")
	requireFires(t, checkD13Dependencies([]srcFile{be("internal/platform/x/a.go", goSrc("github.com/testcontainers/testcontainers-go"))}), "testkit")
	requireFires(t, checkD13Dependencies([]srcFile{be("internal/modules/a/a.go", goSrc("github.com/aws/aws-sdk-go-v2/service/s3"))}), "cloud SDK")
	requireFires(t, checkD13Dependencies([]srcFile{be("internal/modules/a/a_test.go", goSrc("cloud.google.com/go/storage"))}), "cloud SDK")
	requireFires(t, checkD13Dependencies([]srcFile{{Mod: modScheduler, Rel: "a.go", Src: goSrc("github.com/oracle/oci-go-sdk/v65/common")}}), "cloud SDK")
	ok := []srcFile{
		be("internal/platform/x/a.go", goSrc("github.com/google/uuid", "github.com/jackc/pgx/v5/pgxpool", "go.opentelemetry.io/otel/sdk/trace", "fmt", backendModule+"/internal/platform/ids")),
		be("internal/testkit/a.go", goSrc("github.com/testcontainers/testcontainers-go/modules/postgres")),
		be("internal/modules/a/a_test.go", goSrc("github.com/testcontainers/testcontainers-go")),
		be("internal/adapters/storage/s3/a.go", goSrc("github.com/aws/aws-sdk-go-v2/service/s3")),
	}
	requireClean(t, checkD13Dependencies(ok))
}

// ---- encryption-design §10.1: raw crypto primitives only in the crypto and keys packages ----

var cryptoHomes = []string{"internal/platform/crypto", "internal/platform/keys", "internal/adapters/keys"}

func checkCryptoImports(files []srcFile) []string {
	parsed, v := parseGoFiles(files)
	for _, p := range parsed {
		for _, s := range importPaths(p) {
			imp := impPath(s)
			if imp != "crypto/aes" && imp != "crypto/cipher" && !hasPathPrefix(imp, "golang.org/x/crypto") {
				continue
			}
			allowed := false
			for _, h := range cryptoHomes {
				if p.Mod == modBackend && hasPathPrefix(p.Rel, h) {
					allowed = true
				}
			}
			if !allowed {
				v = append(v, fmt.Sprintf("%s: encryption-design §10.1 / K-DP-03 %q only inside platform/crypto, platform/keys, adapters/keys", p.at(s), imp))
			}
		}
	}
	return v
}

func TestEncryptionDesign10_CryptoImports(t *testing.T) {
	requireClean(t, checkCryptoImports(loadTree(t)))
}

func TestEncryptionDesign10_CryptoImports_Negative(t *testing.T) {
	for _, imp := range []string{"crypto/aes", "crypto/cipher", "golang.org/x/crypto/hkdf"} {
		requireFires(t, checkCryptoImports([]srcFile{be("internal/modules/records/a.go", goSrc(imp))}), "K-DP-03")
	}
	requireFires(t, checkCryptoImports([]srcFile{{Mod: modScheduler, Rel: "a.go", Src: goSrc("crypto/aes")}}), "K-DP-03")
	requireClean(t, checkCryptoImports([]srcFile{
		be("internal/platform/crypto/a.go", goSrc("crypto/aes", "crypto/cipher", "golang.org/x/crypto/hkdf")),
		be("internal/adapters/keys/aws/a.go", goSrc("crypto/cipher")),
	}))
}

// ---- B10 / K-ARC-02: scheduler-core never depends on the backend module ----

func checkB10SchedulerIsolated(files []srcFile) []string {
	var sched []srcFile
	var v []string
	for _, f := range files {
		if f.Mod != modScheduler {
			continue
		}
		sched = append(sched, f)
		if f.Rel == "go.mod" && strings.Contains(f.Src, backendModule) {
			v = append(v, f.display()+": B10/K-ARC-02 go.mod references the backend module")
		}
	}
	parsed, errs := parseGoFiles(sched)
	v = append(v, errs...)
	for _, p := range parsed {
		for _, s := range importPaths(p) {
			if hasPathPrefix(impPath(s), backendModule) {
				v = append(v, fmt.Sprintf("%s: B10/K-ARC-02 scheduler-core imports backend %q", p.at(s), impPath(s)))
			}
		}
	}
	return v
}

func TestB10_SchedulerCoreIsolated(t *testing.T) {
	requireClean(t, checkB10SchedulerIsolated(loadTree(t)))
}

func TestB10_SchedulerCoreIsolated_Negative(t *testing.T) {
	requireFires(t, checkB10SchedulerIsolated([]srcFile{{Mod: modScheduler, Rel: "go.mod", Src: "module x\nrequire " + backendModule + " v0.0.0\n"}}), "go.mod")
	requireFires(t, checkB10SchedulerIsolated([]srcFile{{Mod: modScheduler, Rel: "a.go", Src: goSrc(backendModule + "/internal/platform/ids")}}), "imports backend")
	requireClean(t, checkB10SchedulerIsolated([]srcFile{{Mod: modScheduler, Rel: "a.go", Src: goSrc("fmt")}}))
}

// Test files get two narrow exemptions; everything else still fires.
func TestTestFileExemptions(t *testing.T) {
	ok := []srcFile{
		be("internal/modules/identity/identity_test.go", goSrc(backendModule+"/internal/modules/audit")),
		be("internal/modules/identity/identity_test.go", goSrc(backendModule+"/internal/adapters/sms/fake")),
	}
	requireClean(t, checkB4ModuleImports(ok))
	requireClean(t, checkA1VendorAdapters(ok))
	requireFires(t, checkB4ModuleImports([]srcFile{be("internal/modules/identity/x_test.go", goSrc(backendModule+"/internal/modules/records/httpapi"))}), "B4/K-ARC-01")
	requireFires(t, checkA1VendorAdapters([]srcFile{be("internal/modules/identity/x_test.go", goSrc(backendModule+"/internal/adapters/sms/msg91"))}), "A1/D-19")
	requireFires(t, checkA1VendorAdapters([]srcFile{be("internal/modules/identity/x.go", goSrc(backendModule+"/internal/adapters/sms/fake"))}), "A1/D-19")
}

// Review H1: clinical records must stay readable for their legal retention (RET-02) after
// a patient's erasable data is crypto-shredded, so clinical code seals only with care keys
// (encryption-design §3) and never with the patient key.
func checkClinicalUsesCareKeys(files []srcFile) []string {
	var v []string
	for _, f := range files {
		if f.Mod != modBackend || !strings.HasPrefix(f.Rel, "internal/modules/clinical/") || f.isTest() || !f.isGo() {
			continue
		}
		if strings.Contains(f.Src, "SealPatient(") {
			v = append(v, f.display()+": clinical data must be sealed with the care key, not the patient key (RET-02 vs RET-18)")
		}
	}
	return v
}

func TestH1_ClinicalUsesCareKeys(t *testing.T) {
	requireClean(t, checkClinicalUsesCareKeys(loadTree(t)))
}

func TestH1_ClinicalUsesCareKeys_Negative(t *testing.T) {
	requireFires(t, checkClinicalUsesCareKeys([]srcFile{be("internal/modules/clinical/x.go", "package clinical\nfunc f() { crypto.SealPatient(nil) }\n")}), "care key")
}
