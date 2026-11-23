package app_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/idempotency"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { app.UseStrictJSON(); testkit.Main(m) }

const cc = "+999" // synthetic test country code

type harness struct {
	srv *httptest.Server
	sms *smsfake.Sender
	t   *testing.T
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func setup(t *testing.T) *harness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	core := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), idempotency.Source(), identity.Source(), records.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	regBytes, _ := os.ReadFile(filepath.Join("..", "..", "..", "config", "country-packs", "IN", "messaging", "sms-templates.yaml"))
	reg, err := sms.ParseRegistry(regBytes)
	if err != nil {
		t.Fatal(err)
	}
	fake := &smsfake.Sender{Registry: reg}
	icfg := identity.DefaultConfig()
	icfg.PhoneCountryCodes = []string{cc}
	icfg.PhonePattern = `^\+999[0-9]{7}$`
	idm, err := identity.New(core.Pools.For("identity"), icfg, cr, fake, log)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := records.New(core.Pools.For("records"), records.Config{ConsentVersions: map[string]int{"P-ACCOUNT": 1, "P-CARE": 2, "P-RECORD": 2, "P-REMIND": 2},
		T1Window: 24 * time.Hour, JITMax: 4 * time.Hour, NoticeVersion: "PN-1.0"}, log)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := app.Specs()
	if err != nil {
		t.Fatal(err)
	}
	h := app.NewRouter(app.RouterConfig{Log: log, Crypto: cr, Auth: idm, Idempotency: idempotency.New(core.Pools.For("platform"), cr),
		Mount: app.Mounts(app.Modules{Identity: idm, Records: rec}, pol, log, time.Now)})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, sms: fake, t: t}
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
}

func (h *harness) do(method, path, token, idemKey string, body any) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = bytes.NewBufferString(b)
		default:
			j, _ := json.Marshal(b)
			rd = bytes.NewReader(j)
		}
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer r.Body.Close()
	out := resp{status: r.StatusCode, header: r.Header, body: map[string]any{}}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &out.body)
	return out
}

func deviceKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (h *harness) signIn(phone string, k *ecdsa.PrivateKey) string {
	h.t.Helper()
	r := h.do("POST", "/v1/auth/otp-challenges", "", ids.New(), map[string]any{"phone": phone, "app": "patient"})
	if r.status != 202 {
		h.t.Fatalf("otp: %d %v", r.status, r.body)
	}
	sent := h.sms.Sent(phone)
	code := sent[len(sent)-1].Vars[0].Value
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	chID, _ := r.body["challengeId"].(string)
	digest := sha256.Sum256(identity.SignInMessage(chID))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	r = h.do("POST", "/v1/auth/sessions", "", "", map[string]any{"challengeId": chID, "phone": phone, "code": code, "app": "patient",
		"device": map[string]any{"publicKey": base64.StdEncoding.EncodeToString(der), "platform": "android", "proof": base64.StdEncoding.EncodeToString(sig)}})
	if r.status != 201 {
		h.t.Fatalf("session: %d %v", r.status, r.body)
	}
	return r.body["accessToken"].(string)
}

func TestHTTPEndToEnd(t *testing.T) {
	h := setup(t)
	phone := cc + "5551234"

	// Required Idempotency-Key on a message-sending operation.
	if r := h.do("POST", "/v1/auth/otp-challenges", "", "", map[string]any{"phone": phone, "app": "patient"}); r.status != 400 {
		t.Fatalf("missing Idempotency-Key: %d", r.status)
	}
	key := ids.New()
	r1 := h.do("POST", "/v1/auth/otp-challenges", "", key, map[string]any{"phone": phone, "app": "patient"})
	r2 := h.do("POST", "/v1/auth/otp-challenges", "", key, map[string]any{"phone": phone, "app": "patient"})
	if r1.status != 202 || r2.status != 202 || r2.header.Get("Idempotent-Replayed") != "true" || r1.body["challengeId"] != r2.body["challengeId"] {
		t.Fatalf("replay: %d %d %v %v", r1.status, r2.status, r1.body, r2.body)
	}
	if n := len(h.sms.Sent(phone)); n != 1 {
		t.Fatalf("replayed request sent %d SMS, want 1", n)
	}
	if r := h.do("POST", "/v1/auth/otp-challenges", "", key, map[string]any{"phone": cc + "5559999", "app": "patient"}); r.status != 409 || r.body["code"] != "idempotency.key_reused" {
		t.Fatalf("key reuse: %d %v", r.status, r.body)
	}
	// Unknown fields are refused (strict decoding).
	if r := h.do("POST", "/v1/auth/otp-challenges", "", ids.New(), `{"phone":"`+phone+`","app":"patient","idNumber":"x"}`); r.status != 400 {
		t.Fatalf("unknown field accepted: %d", r.status)
	}

	tok := h.signIn(phone, deviceKey(t))
	me := h.do("GET", "/v1/me", tok, "", nil)
	if me.status != 200 || me.body["restricted"] != false {
		t.Fatalf("me: %d %v", me.status, me.body)
	}
	if me.header.Get("Cache-Control") != "no-store" || me.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
	if r := h.do("GET", "/v1/me", "", "", nil); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := h.do("GET", "/v1/me", "at_bogus", "", nil); r.status != 401 {
		t.Fatalf("bad token: %d", r.status)
	}

	// Consent.
	if r := h.do("POST", "/v1/me/consents", tok, ids.New(), map[string]any{"purposeId": "P-REMIND", "textVersion": 2, "noticeVersion": "PN-1.0"}); r.status != 204 {
		t.Fatalf("grant consent: %d %v", r.status, r.body)
	}
	if r := h.do("POST", "/v1/me/consents", tok, ids.New(), map[string]any{"purposeId": "P-REMIND", "textVersion": 1, "noticeVersion": "PN-1.0"}); r.status != 400 {
		t.Fatalf("old text version accepted: %d", r.status)
	}
	list := h.do("GET", "/v1/me/consents", tok, "", nil)
	if list.status != 200 || len(list.body["items"].([]any)) != 1 {
		t.Fatalf("list consents: %d %v", list.status, list.body)
	}
	if r := h.do("POST", "/v1/me/consents/P-CARE/withdraw", tok, ids.New(), nil); r.status != 409 || r.body["code"] != "records.needed_consent" {
		t.Fatalf("withdraw needed consent: %d %v", r.status, r.body)
	}
	if r := h.do("POST", "/v1/me/consents/P-REMIND/withdraw", tok, ids.New(), nil); r.status != 204 {
		t.Fatalf("withdraw optional consent: %d %v", r.status, r.body)
	}

	// Deny by default: a patient calling a professional operation.
	if r := h.do("POST", "/v1/auth/mfa/totp", tok, ids.New(), nil); r.status != 403 {
		t.Fatalf("patient enrolling TOTP: %d %v", r.status, r.body)
	}
	// Another user's (or a random) grant: 404, never 403 (K-SEC-04).
	if r := h.do("POST", "/v1/me/access-grants/"+ids.New()+"/revoke", tok, ids.New(), nil); r.status != 404 {
		t.Fatalf("foreign grant: %d %v", r.status, r.body)
	}
	if r := h.do("GET", "/v1/no-such-route", tok, "", nil); r.status != 404 || r.body["code"] != "resource.not_found" {
		t.Fatalf("unknown route: %d %v", r.status, r.body)
	}

	// A new device on the same number is restricted (E-DP-22).
	tok2 := h.signIn(phone, deviceKey(t))
	if r := h.do("GET", "/v1/me", tok2, "", nil); r.status != 200 || r.body["restricted"] != true {
		t.Fatalf("restricted me: %d %v", r.status, r.body)
	}
	if r := h.do("GET", "/v1/me/consents", tok2, "", nil); r.status != 403 || r.body["code"] != "auth.reverification_required" {
		t.Fatalf("restricted session read consents: %d %v", r.status, r.body)
	}
}
