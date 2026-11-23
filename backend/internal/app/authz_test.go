package app_test

// S-116 / TC-57: the authorisation suite generated from the OpenAPI specs. For every
// operation of every spec it calls the real, fully assembled API (BuildCore + NewRouter,
// every module) as an anonymous caller, a patient and an MFA-verified doctor, using IDs
// that belong to nobody, and checks:
//   - not signed in → 401, unless the operation is public;
//   - a role the operation does not declare → 403 (deny by default, S-106);
//   - a declared role reading someone else's (or an unknown) resource → 404, never data;
//   - no call ever returns 500.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pquerna/otp/totp"

	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func buildFullCore(t *testing.T) *app.Core {
	t.Helper()
	ctx := context.Background()
	core := testkit.New(t, testkit.Spec{Key: "fullcore", Sources: app.MigrationSources()})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	s := app.Settings{Env: config.EnvTest, PackDir: filepath.Join("..", "..", "..", "config", "country-packs", "IN"),
		VendorRegister: filepath.Join("..", "..", "..", "config", "vendor-register.yaml"), DBURL: map[string]string{},
		KeystoreURL: ks.URL("keystore"), BlindIndexKey: rnd(32), LocalRootKey: rnd(32), BlobDir: t.TempDir(), SchedulerSecret: rnd(32)}
	for _, m := range db.Modules {
		s.DBURL[m] = core.URL(m)
	}
	c, err := app.BuildCore(ctx, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildCore: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

type actor struct {
	name  string
	token string
	roles []string
}

func signInDirect(t *testing.T, c *app.Core, phone string, appKind ip.App, intent string) (string, *ecdsa.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	ch, err := c.Identity.RequestOTP(ctx, identity.OTPRequest{Phone: phone, App: appKind, IPHash: "authz-suite"})
	if err != nil {
		t.Fatal(err)
	}
	sent := c.SMS.(*smsfake.Sender).Sent(phone)
	code := sent[len(sent)-1].Vars[0].Value
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	digest := sha256.Sum256(identity.SignInMessage(ch.ID))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	tok, err := c.Identity.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch.ID, Phone: phone, Code: code, App: appKind, ProIntent: intent,
		Device: identity.DeviceInfo{PublicKey: der, Platform: "android", Proof: sig}})
	if err != nil {
		t.Fatal(err)
	}
	return tok.AccessToken, k
}

var reIDParam = regexp.MustCompile(`\{([A-Za-z]+)\}`)

func fillPath(p string) string {
	return reIDParam.ReplaceAllStringFunc(p, func(m string) string {
		name := strings.Trim(m, "{}")
		switch {
		case name == "purposeId":
			return "P-REMIND"
		case strings.Contains(strings.ToLower(name), "token"), name == "reference":
			b := make([]byte, 32)
			_, _ = rand.Read(b)
			return hex.EncodeToString(b)
		default:
			return ids.New()
		}
	})
}

func TestS116GeneratedAuthorisationSuite(t *testing.T) {
	app.UseStrictJSON()
	c := buildFullCore(t)
	pol, err := app.Specs()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := app.SpecDocs()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := app.NewRouter(app.RouterConfig{Log: log, Crypto: c.Crypto, Auth: c.Identity, Idempotency: c.Idem, IPPerMinute: 1_000_000, UserPerMinute: 1_000_000,
		Mount: app.Mounts(app.Modules{Identity: c.Identity, Records: c.Records, Rights: c.Rights, Verification: c.Verification,
			Directory: c.Directory, Booking: c.Booking, Payments: c.Payments, Notify: c.Notify, Admin: c.Admin, Rx: c.Rx, Consult: c.Consult, Sharing: c.Sharing, History: c.History}, pol, log, time.Now)})
	srv := httptest.NewServer(h)
	defer srv.Close()

	patientTok, _ := signInDirect(t, c, "+919876500001", ip.AppPatient, "")
	doctorTok, _ := signInDirect(t, c, "+919876500002", ip.AppPro, "doctor")
	p, err := c.Identity.Authenticate(context.Background(), doctorTok)
	if err != nil {
		t.Fatal(err)
	}
	en, err := c.Identity.EnrolTOTP(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(en.Secret, time.Now())
	if err := c.Identity.ConfirmTOTP(context.Background(), p, code); err != nil {
		t.Fatal(err)
	}
	actors := []actor{
		{name: "anonymous"},
		{name: "patient", token: patientTok, roles: []string{ip.RolePatient, ip.RoleAuthenticated}},
		{name: "doctor", token: doctorTok, roles: []string{ip.RoleDoctor, ip.RoleAuthenticated}},
	}

	ops := 0
	for _, d := range docs {
		for path, item := range d.Paths.Map() {
			for method, op := range item.Operations() {
				if strings.EqualFold(op.OperationID, "signOut") {
					continue // would end the suite's own sessions; covered by identity tests
				}
				ops++
				name := strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:]
				policy := pol[name]
				public := slices.Contains(policy.Roles, ip.RolePublic)
				// {purposeId} names one of the caller's own consent purposes (a fixed code), not
				// someone else's resource; every other path parameter is a resource ID.
				hasParam := strings.Contains(strings.ReplaceAll(path, "{purposeId}", "P-REMIND"), "{")
				for _, a := range actors {
					status, code := call(t, srv.URL, method, fillPath(path), a.token, op)
					label := method + " " + path + " as " + a.name
					if testing.Verbose() {
						t.Logf("TRACE %s -> %d %s", label, status, code)
					}
					if status >= 500 {
						t.Errorf("%s: server error %d (%s)", label, status, code)
						continue
					}
					if public {
						continue
					}
					allowed := false
					for _, r := range a.roles {
						if slices.Contains(policy.Roles, r) {
							allowed = true
						}
					}
					switch {
					case a.token == "":
						// A malformed body or missing parameter may be refused (400) before the
						// sign-in check; it reveals nothing beyond the public spec.
						if status != http.StatusUnauthorized && code != "request.invalid" {
							t.Errorf("%s: want 401, got %d (%s)", label, status, code)
						}
					case !allowed:
						if status != http.StatusForbidden {
							t.Errorf("%s: undeclared role must be refused with 403, got %d (%s)", label, status, code)
						}
					case method == http.MethodGet && hasParam:
						// Someone else's or an unknown resource: never data, never a 403 that
						// confirms it exists (K-SEC-04, TC-57). Refusals that depend only on the caller (a
						// step-up demand; a doctor who is not verified, K-CS-01) answer the same for every
						// ID, so they confirm nothing and are acceptable.
						if status != http.StatusNotFound && status != http.StatusGone && !slices.Contains(callerOnlyCodes, code) {
							t.Errorf("%s: want 404 for a resource that is not the caller's, got %d (%s)", label, status, code)
						}
					default:
						if status >= 200 && status < 300 && hasParam {
							t.Errorf("%s: a write to a resource that is not the caller's succeeded (%d)", label, status)
						}
					}
				}
			}
		}
	}
	if ops < 50 {
		t.Fatalf("suite covered only %d operations; specs not loaded?", ops)
	}
	t.Logf("S-116: %d operations × %d callers checked", ops, len(actors))
}

func call(t *testing.T, base, method, path, token string, op *openapi3.Operation) (int, string) {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		body = bytes.NewBufferString("{}")
	}
	// Fill required query parameters with valid values so the ownership check, not request
	// validation, decides the answer.
	q := url.Values{}
	for _, pr := range op.Parameters {
		if pr.Value == nil || pr.Value.In != "query" || !pr.Value.Required {
			continue
		}
		v := "x"
		if s := pr.Value.Schema; s != nil && s.Value != nil {
			switch {
			case len(s.Value.Enum) > 0:
				v = fmt.Sprint(s.Value.Enum[0])
			case s.Value.Format == "date":
				v = time.Now().UTC().Format(time.DateOnly)
			case s.Value.Format == "date-time":
				v = time.Now().UTC().Format(time.RFC3339)
			case s.Value.Type.Is("integer"), s.Value.Type.Is("number"):
				v = "1"
			case s.Value.Type.Is("boolean"):
				v = "true"
			}
		}
		q.Set(pr.Value.Name, v)
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, base+path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", ids.New())
	}
	if method == http.MethodPatch || method == http.MethodPut {
		req.Header.Set("If-Match", `"0"`)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var pr struct {
		Code string `json:"code"`
	}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &pr)
	return r.StatusCode, pr.Code
}

// callerOnlyCodes are refusals decided by the caller's own state, identical for every resource ID.
var callerOnlyCodes = []string{"auth.step_up_required", "clinical.doctor_not_verified", "records.doctor_not_verified"}
