package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// S-189: a signed-in patient sends feedback through HTTP and gets 201 with a reference; the
// message is theirs alone; a link is refused with 422 and a field code; unknown diagnostics
// fields are refused; the staff triage routes are not open to a patient.
func TestJourneyFeedback(t *testing.T) {
	j := newJourney(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	pat := j.signIn("+919876500401", ip.AppPatient, "")
	other := j.signIn("+919876500402", ip.AppPatient, "")

	r := j.must(j.call(http.MethodPost, "/v1/me/feedback", pat.token, map[string]any{
		"kind": "bug", "message": "The slot list is empty on Monday", "screen": "booking.slots", "appVersion": "1.0.0", "platform": "android",
	}), http.StatusCreated, "submit feedback")
	if !strings.HasPrefix(r.str("reference"), "FB-") || r.str("feedbackId") == "" {
		t.Fatalf("receipt: %s", r.raw)
	}
	l := j.must(j.call(http.MethodGet, "/v1/me/feedback", pat.token, nil), http.StatusOK, "list own")
	if !strings.Contains(string(l.raw), "The slot list is empty on Monday") || strings.Contains(string(l.raw), "diagnostics") {
		t.Fatalf("own list: %s", l.raw)
	}
	if o := j.must(j.call(http.MethodGet, "/v1/me/feedback", other.token, nil), http.StatusOK, "other list"); strings.Contains(string(o.raw), "slot list") {
		t.Fatalf("another user sees it: %s", o.raw)
	}

	if x := j.call(http.MethodPost, "/v1/me/feedback", pat.token, map[string]any{"kind": "bug", "message": "open https://phish.example now"}); x.status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(x.raw), "contains_link_or_markup") {
		t.Fatalf("link: %d %s", x.status, x.raw)
	}
	if x := j.call(http.MethodPost, "/v1/me/feedback", pat.token, map[string]any{"kind": "bug", "message": "ok", "diagnostics": map[string]any{"deviceId": "abc"}}); x.status != http.StatusBadRequest {
		t.Fatalf("unknown diagnostics field: %d %s", x.status, x.raw)
	}
	if x := j.call(http.MethodPost, "/v1/me/feedback", "", map[string]any{"kind": "bug", "message": "ok"}); x.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", x.status)
	}
	if x := j.call(http.MethodGet, "/admin/v1/feedback", pat.token, nil); x.status != http.StatusForbidden {
		t.Fatalf("patient on the triage route: %d %s", x.status, x.raw)
	}
}
