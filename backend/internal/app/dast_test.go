//go:build dast

package app_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// TestDASTServer is the target for the OWASP ZAP scan (S-179; tools/ci/zap-scan.sh). It is
// built only with -tags dast and never ships. It assembles the whole API on a test database
// with the fake vendors and synthetic people, signs in a verified doctor with a practice and
// a patient exactly as the apps do, writes their access tokens to MUGIZH_DAST_TOKENS (a
// file inside the container, never the repo), and serves on MUGIZH_DAST_ADDR until
// MUGIZH_DAST_FOR passes. The clock is frozen, so the tokens stay valid for the scan.
func TestDASTServer(t *testing.T) {
	if os.Getenv("MUGIZH_DAST_ADDR") == "" || os.Getenv("MUGIZH_DAST_TOKENS") == "" {
		t.Skip("set MUGIZH_DAST_ADDR and MUGIZH_DAST_TOKENS")
	}
	serve, err := time.ParseDuration(os.Getenv("MUGIZH_DAST_FOR"))
	if err != nil || serve <= 0 || serve > 3*time.Hour {
		serve = 45 * time.Minute
	}
	j := newJourney(t, time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC))
	doc := j.pro("+919876500901")
	j.verifyDoctor(doc, "Dr Asha Rao", "SYN-90901")
	clinic, practice, _ := j.clinicWithPractice(doc)
	pat := j.signIn("+919876500902", ip.AppPatient, "")
	for _, p := range []string{"P-ACCOUNT", "P-CARE", "P-RECORD"} {
		j.consent(pat, p, j.c.Config.Pack().Legal().ConsentTextVersions[p])
	}
	j.pump()
	out, _ := json.Marshal(map[string]string{"patient": pat.token, "doctor": doc.token, "clinic": clinic, "practice": practice})
	if err := os.WriteFile(os.Getenv("MUGIZH_DAST_TOKENS"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("DAST target ready on %s for %s", os.Getenv("MUGIZH_DAST_ADDR"), serve)
	ctx, cancel := context.WithTimeout(context.Background(), serve)
	defer cancel()
	<-ctx.Done()
}
