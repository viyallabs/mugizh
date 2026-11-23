package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// C2 (S-178): every module's alert hook reaches the operational alert sink in the
// assembled core (TM-25, TM-35, TM-38, TM-51). Alerts are code-only log lines.
func TestS178AlertHooksAreWired(t *testing.T) {
	var buf bytes.Buffer
	journeyLog = &buf
	t.Cleanup(func() { journeyLog = io.Discard })
	j := newJourney(t, time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC))
	ctx := context.Background()
	before := app.AlertsRaised()
	j.c.Identity.Alert(ctx, "identity.test_one")
	j.c.Records.Alert(ctx, "access.break_glass_test")
	j.c.Payments.Alert(ctx, "payments.test_two")
	j.c.Audit.Alert(ctx, "audit.test_three")
	j.c.Notify.Alert(ctx, "0190d7c4-1111-7000-8000-000000000001", "rejected")
	j.c.Identity.Alert(ctx, "free text with +919876500999") // never written as given
	if got := app.AlertsRaised() - before; got != 6 {
		t.Fatalf("alerts counted: %d", got)
	}
	out := buf.String()
	for _, code := range []string{"identity.test_one", "access.break_glass_test", "payments.test_two", "audit.test_three", "notifications.dead_letter.rejected", "alert.invalid_code"} {
		if !strings.Contains(out, app.AlertEvent) || !strings.Contains(out, code) {
			t.Fatalf("alert %s missing from the log:\n%s", code, out)
		}
	}
	if strings.Contains(out, "9876500999") {
		t.Fatal("free text must never reach an alert line")
	}
}

// TM-39: a key-service outage is a clear 503 with Retry-After (never a plaintext fallback,
// never a bare 500), and raises an alert at most once a minute.
func TestTM39KeyServiceOutageIs503AndAlerts(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, 0)
	h := app.ResponseError(log)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/v1/me/profile", nil), fmt.Errorf("open: %w", fmt.Errorf("%w: kms timeout", crypto.ErrKeyUnavailable)))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "30" || !strings.Contains(rec.Body.String(), "server.key_service_unavailable") {
			t.Fatalf("want 503 with Retry-After: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "kms") {
			t.Fatal("no internal detail in the answer")
		}
	}
	if n := strings.Count(buf.String(), "crypto.key_unavailable"); n != 1 {
		t.Fatalf("one alert per minute, got %d:\n%s", n, buf.String())
	}
	// Any other error stays a generic 500.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/x", nil), errors.New("boom"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("other errors: %d", rec.Code)
	}
}
