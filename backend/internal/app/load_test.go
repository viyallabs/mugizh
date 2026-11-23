//go:build load

package app_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// TestLoadServer is the target for the k6 smoke load test (S-175 stand-in; tests/load,
// docs/test/load-test-plan.md). Built only with -tags load; it never ships. It assembles the
// whole API on a test database with fake vendors and synthetic people: one verified doctor
// with a video practice, and MUGIZH_LOAD_PATIENTS patients (default 100) signed in with
// P-ACCOUNT and P-CARE, so k6 can drive search, slots, reads, and many parallel holds on
// one slot (L3). Tokens go to MUGIZH_LOAD_OUT inside the container, never the repo.
func TestLoadServer(t *testing.T) {
	if os.Getenv("MUGIZH_DAST_ADDR") == "" || os.Getenv("MUGIZH_LOAD_OUT") == "" {
		t.Skip("set MUGIZH_DAST_ADDR and MUGIZH_LOAD_OUT")
	}
	j := newJourney(t, time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC))
	legal := j.c.Config.Pack().Legal()
	doc := j.pro("+919876500801")
	j.verifyDoctor(doc, "Dr Load Synthetic", "SYN-80801")
	_, practice, _ := j.clinicWithPractice(doc)
	n := 100
	if v := os.Getenv("MUGIZH_LOAD_PATIENTS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &n)
	}
	tokens := make([]string, 0, n)
	ctx := context.Background()
	for i := 0; i < n; i++ {
		phone := fmt.Sprintf("+9198770%05d", i)
		ipHash := fmt.Sprintf("load-%d", i) // one synthetic network per patient: the per-IP OTP limit is not under test here
		ch, err := j.c.Identity.RequestOTP(ctx, identity.OTPRequest{Phone: phone, App: ip.AppPatient, IPHash: ipHash})
		if err != nil {
			t.Fatal(err)
		}
		sent := j.c.SMS.(*smsfake.Sender).Sent(phone)
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
		digest := sha256.Sum256(identity.SignInMessage(ch.ID))
		sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		tok, err := j.c.Identity.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch.ID, Phone: phone, Code: sent[len(sent)-1].Vars[0].Value,
			App: ip.AppPatient, IPHash: ipHash, Device: identity.DeviceInfo{PublicKey: der, Platform: "android", Proof: sig}})
		if err != nil {
			t.Fatal(err)
		}
		p := person{id: tok.UserID, token: tok.AccessToken, phone: phone}
		j.consent(p, "P-ACCOUNT", legal.ConsentTextVersions["P-ACCOUNT"])
		j.consent(p, "P-CARE", legal.ConsentTextVersions["P-CARE"])
		tokens = append(tokens, p.token)
	}
	sl := j.must(j.call(http.MethodGet, "/v1/practices/"+practice+"/slots", tokens[0], nil), http.StatusOK, "slots")
	slots, _ := sl.body["items"].([]any)
	if len(slots) == 0 {
		t.Fatalf("no open slots: %s", sl.raw)
	}
	first, _ := slots[0].(map[string]any)
	out, _ := json.Marshal(map[string]any{"practice": practice, "slot": first["slotId"], "patients": tokens, "lat": 12.97, "lng": 77.59})
	if err := os.WriteFile(os.Getenv("MUGIZH_LOAD_OUT"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("load target ready with %d patients", n)
	serve, err := time.ParseDuration(os.Getenv("MUGIZH_DAST_FOR"))
	if err != nil || serve <= 0 {
		serve = 30 * time.Minute
	}
	c, cancel := context.WithTimeout(ctx, serve)
	defer cancel()
	<-c.Done()
}
