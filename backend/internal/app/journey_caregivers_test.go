package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// stepUp completes the patient's SMS step-up through the API (valid for five minutes).
func (j *journey) stepUp(p person) {
	j.t.Helper()
	ch := j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", p.token, nil), http.StatusAccepted, "request step-up code")
	code, _ := j.lastCode(p.phone)
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", p.token, map[string]any{"challengeId": ch.str("challengeId"), "code": code}), http.StatusNoContent, "step-up")
}

// TestJourneyCaregiversXX runs caregiver access, phase 1 (v1.1), on the synthetic country XX whose pack turns the
// "caregivers" flag on: the patient consents to P-CAREGIVER, steps up and invites; another signed-in user redeems the
// code; both list the link; the patient narrows and widens the scopes (step-up only for a sensitive scope) and
// revokes. The flag is applied here exactly as the composition root does (Records.SetCaregiversEnabled with
// Config.Flag("caregivers")).
func TestJourneyCaregiversXX(t *testing.T) {
	j := newJourneyIn(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), "XX")
	if !j.c.Config.Flag("caregivers") {
		t.Fatal("the XX pack turns caregivers on")
	}
	legal := j.c.Config.Pack().Legal()
	pat := j.patient("+351912340301", "push-token-synthetic-xx-care-01")
	cg := j.signIn("+351912340302", ip.AppPatient, "")
	stranger := j.signIn("+351912340303", ip.AppPatient, "")

	// Granting access to another person needs a fresh step-up, then the P-CAREGIVER consent.
	r := j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{}), http.StatusForbidden, "no step-up")
	if r.str("code") != "auth.step_up_required" {
		t.Fatalf("step-up: %s", r.raw)
	}
	j.stepUp(pat)
	r = j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{}), http.StatusForbidden, "no consent")
	if r.str("code") != "consent.required" {
		t.Fatalf("consent: %s", r.raw)
	}
	j.consent(pat, "P-CAREGIVER", legal.ConsentTextVersions["P-CAREGIVER"])
	j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{"scopes": []string{"notes"}}), http.StatusBadRequest, "unknown scope")
	j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{"label": "call 98765 43210"}), http.StatusBadRequest, "a phone number in the label")
	inv := j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{"label": "Mum"}), http.StatusCreated, "invitation")
	code := inv.str("code")
	if len(code) != 8 || inv.str("expiresAt") == "" {
		t.Fatalf("invitation: %s", inv.raw)
	}
	if scopes, _ := inv.body["scopes"].([]any); len(scopes) != 2 {
		t.Fatalf("default scopes: %s", inv.raw)
	}

	// Redeeming: the patient cannot use their own code; the country must be confirmed; a wrong code is 404.
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", pat.token, map[string]any{"code": code, "inCountryConfirmed": true}), http.StatusNotFound, "own code")
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", cg.token, map[string]any{"code": code, "inCountryConfirmed": false}), http.StatusBadRequest, "country not confirmed")
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", cg.token, map[string]any{"code": "ABCDEFGH", "inCountryConfirmed": true}), http.StatusNotFound, "wrong code")
	link := j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", cg.token, map[string]any{"code": strings.ToLower(code), "inCountryConfirmed": true}), http.StatusCreated, "redeem")
	if link.str("patientId") != pat.id || link.str("linkId") == "" {
		t.Fatalf("redeem: %s", link.raw)
	}
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", stranger.token, map[string]any{"code": code, "inCountryConfirmed": true}), http.StatusNotFound, "a used code")

	// Both sides see the link; the patient sees the caregiver by an opaque reference only.
	mine := j.must(j.call(http.MethodGet, "/v1/caregiving/patients", cg.token, nil), http.StatusOK, "my patients")
	if items, _ := mine.body["items"].([]any); len(items) != 1 || !strings.Contains(string(mine.raw), pat.id) || strings.Contains(string(mine.raw), "Mum") {
		t.Fatalf("my patients: %s", mine.raw)
	}
	cgs := j.must(j.call(http.MethodGet, "/v1/me/caregivers", pat.token, nil), http.StatusOK, "my caregivers")
	items, _ := cgs.body["items"].([]any)
	if len(items) != 1 || !strings.Contains(string(cgs.raw), cg.id) || strings.Contains(string(cgs.raw), "351912340302") || !strings.Contains(string(cgs.raw), "Mum") {
		t.Fatalf("my caregivers (opaque reference, never the phone): %s", cgs.raw)
	}
	linkID := link.str("linkId")

	// The patient is told, neutrally, that someone accepted: no name, no scope.
	j.pump()
	j.deliverNotifications()
	pushes := j.pushBodies("push-token-synthetic-xx-care-01")
	if len(pushes) != 1 || !strings.Contains(pushes[0], "Someone accepted your caregiver invitation") || strings.Contains(pushes[0], "Mum") || strings.Contains(pushes[0], "reminders") {
		t.Fatalf("the patient notice: %q", pushes)
	}
	if got := j.pushBodies("push-token-synthetic-xx-care-02"); len(got) != 0 {
		t.Fatalf("the caregiver gets no such push: %q", got)
	}

	// Scopes: adding a sensitive scope needs a fresh step-up; removing one does not. Five minutes later it has lapsed.
	j.at(j.clock().Add(6 * time.Minute))
	j.refresh(&pat)
	j.refresh(&cg)
	r = j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+linkID+"/scopes", pat.token, map[string]any{"scopes": []string{"reminders", "appointments", "documents"}}), http.StatusForbidden, "add without step-up")
	if r.str("code") != "auth.step_up_required" {
		t.Fatalf("add scope: %s", r.raw)
	}
	j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+linkID+"/scopes", pat.token, map[string]any{"scopes": []string{"reminders"}}), http.StatusOK, "remove a scope")
	j.stepUp(pat)
	upd := j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+linkID+"/scopes", pat.token, map[string]any{"scopes": []string{"reminders", "documents"}}), http.StatusOK, "add a sensitive scope")
	if sc, _ := upd.body["scopes"].([]any); len(sc) != 2 {
		t.Fatalf("scopes: %s", upd.raw)
	}
	j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+linkID+"/scopes", pat.token, map[string]any{"scopes": []string{}}), http.StatusBadRequest, "empty scopes")
	j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+ids.New()+"/scopes", pat.token, map[string]any{"scopes": []string{"reminders"}}), http.StatusNotFound, "unknown link")
	j.must(j.call(http.MethodPut, "/v1/me/caregivers/"+linkID+"/scopes", stranger.token, map[string]any{"scopes": []string{"reminders"}}), http.StatusNotFound, "another patient's link")

	// An unused invitation can be cancelled; its code then no longer works.
	inv2 := j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{"scopes": []string{"appointments"}}), http.StatusCreated, "second invitation")
	j.must(j.call(http.MethodDelete, "/v1/me/caregiver-invitations/"+inv2.str("invitationId"), pat.token, nil), http.StatusNoContent, "cancel")
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", stranger.token, map[string]any{"code": inv2.str("code"), "inCountryConfirmed": true}), http.StatusNotFound, "cancelled code")

	// The patient revokes; the caregiver's list is empty at once and leaving again is 404.
	j.must(j.call(http.MethodPost, "/v1/me/caregivers/"+linkID+"/revoke", pat.token, nil), http.StatusNoContent, "revoke")
	if items, _ := j.must(j.call(http.MethodGet, "/v1/caregiving/patients", cg.token, nil), http.StatusOK, "after revoke").body["items"].([]any); len(items) != 0 {
		t.Fatal("a revoked caregiver sees no patients")
	}
	if all, _ := j.must(j.call(http.MethodGet, "/v1/me/caregivers", pat.token, nil), http.StatusOK, "history").body["items"].([]any); len(all) != 1 {
		t.Fatalf("the revoked link stays in the patient's list as history: %d", len(all))
	}
	j.must(j.call(http.MethodPost, "/v1/caregiving/patients/"+pat.id+"/leave", cg.token, nil), http.StatusNotFound, "leave after revoke")

	// Another link, ended by the caregiver.
	inv3 := j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{}), http.StatusCreated, "third invitation")
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", cg.token, map[string]any{"code": inv3.str("code"), "inCountryConfirmed": true}), http.StatusCreated, "redeem again")
	j.must(j.call(http.MethodPost, "/v1/caregiving/patients/"+pat.id+"/leave", cg.token, nil), http.StatusNoContent, "leave")

	// Withdrawing P-CAREGIVER ends everything: an open invitation no longer works.
	inv4 := j.must(j.call(http.MethodPost, "/v1/me/caregiver-invitations", pat.token, map[string]any{}), http.StatusCreated, "fourth invitation")
	j.must(j.call(http.MethodPost, "/v1/me/consents/P-CAREGIVER/withdraw", pat.token, nil), http.StatusNoContent, "withdraw P-CAREGIVER")
	j.must(j.call(http.MethodPost, "/v1/caregiving/redeem", cg.token, map[string]any{"code": inv4.str("code"), "inCountryConfirmed": true}), http.StatusNotFound, "code after withdrawal")
}

// On the India pack the "caregivers" flag is off: every caregiver route answers 404 before sign-in, step-up and body
// parsing, so anonymous callers, callers without a step-up and callers with a malformed body all get the same 404 as
// for a route that does not exist. The tables still exist so erasure reaches them (K-DP-07).
func TestJourneyCaregiverRoutesAre404OnTheINPack(t *testing.T) {
	j := newJourney(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	if j.c.Config.Flag("caregivers") || j.c.Records.CaregiversEnabled() {
		t.Fatal("IN: the caregivers flag is off")
	}
	pat := j.signIn("+919876500311", ip.AppPatient, "")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/me/caregiver-invitations"},
		{http.MethodDelete, "/v1/me/caregiver-invitations/" + ids.New()},
		{http.MethodGet, "/v1/me/caregivers"},
		{http.MethodPut, "/v1/me/caregivers/" + ids.New() + "/scopes"},
		{http.MethodPost, "/v1/me/caregivers/" + ids.New() + "/revoke"},
		{http.MethodPost, "/v1/caregiving/redeem"},
		{http.MethodGet, "/v1/caregiving/patients"},
		{http.MethodPost, "/v1/caregiving/patients/" + ids.New() + "/leave"},
	} {
		for _, c := range []struct {
			who, token string
			body       any
		}{
			{"anonymous", "", nil},
			{"signed in, no step-up, no body", pat.token, nil},
			{"signed in, malformed body", pat.token, "{not an object"},
			{"anonymous, malformed body", "", "{not an object"},
			{"bad token", "not-a-token", nil},
		} {
			if r := j.call(tc.method, tc.path, c.token, c.body); r.status != http.StatusNotFound || r.str("code") != "resource.not_found" {
				t.Fatalf("%s %s (%s) with the flag off: want 404 resource.not_found, got %d %s", tc.method, tc.path, c.who, r.status, r.raw)
			}
		}
	}
	// A neighbouring route is not caught by the gate.
	if r := j.call(http.MethodGet, "/v1/me/access-grants", "", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("other routes are untouched: %d %s", r.status, r.raw)
	}
}
