package app_test

import (
	"net/http"
	"testing"
	"time"

	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// lastCode returns the latest SMS-01 code sent to a phone by the SMS fake.
func (j *journey) lastCode(phone string) (string, int) {
	j.t.Helper()
	sent := j.c.SMS.(*smsfake.Sender).Sent(phone)
	if len(sent) == 0 {
		j.t.Fatalf("no SMS sent to the phone on file")
	}
	m := sent[len(sent)-1]
	if m.Template != "SMS-01" {
		j.t.Fatalf("step-up code must use SMS-01, got %s", m.Template)
	}
	return m.Vars[0].Value, len(sent)
}

// TM-32, TM-33 (S-178): a patient can step up only with a code sent to the phone on file,
// bound to the session that asked for it; export, erasure and undo work through the real
// API only after it, and only for 5 minutes.
func TestTM32PatientStepUpThroughTheAPI(t *testing.T) {
	j := newJourney(t, time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC))
	pat := j.signIn("+919876500301", ip.AppPatient, "")
	j.consent(pat, "P-ACCOUNT", 1)

	// Without a step-up the sensitive operations are refused before anything happens.
	r := j.must(j.call(http.MethodPost, "/v1/me/account-erasure", pat.token, nil), http.StatusForbidden, "erasure without step-up")
	if r.str("code") != "auth.step_up_required" {
		t.Fatalf("want auth.step_up_required: %s", r.raw)
	}
	j.must(j.call(http.MethodPost, "/v1/me/data-exports", pat.token, nil), http.StatusForbidden, "export without step-up")

	// The code goes to the phone on file; the request carries no number.
	ch := j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", pat.token, nil), http.StatusAccepted, "request step-up code")
	challenge := ch.str("challengeId")
	code, _ := j.lastCode(pat.phone)

	// Another session of someone else cannot use this challenge, even with the right code.
	other := j.signIn("+919876500302", ip.AppPatient, "")
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", other.token, map[string]any{"challengeId": challenge, "code": code}),
		http.StatusUnauthorized, "someone else's challenge")
	// A second session of the same patient (another phone) cannot use it either.
	second := j.signIn(pat.phone, ip.AppPatient, "")
	if r := j.call(http.MethodPost, "/v1/auth/step-up/otp", second.token, map[string]any{"challengeId": challenge, "code": code}); r.status < 400 {
		t.Fatalf("a challenge is bound to its session: %d %s", r.status, r.raw)
	}
	// A pro-app session cannot ask for an SMS step-up (professionals use the authenticator).
	doc := j.pro("+919876500303")
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", doc.token, nil), http.StatusForbidden, "pro app")

	// A wrong code is refused; the right one then works once.
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", pat.token, map[string]any{"challengeId": challenge, "code": wrong}), http.StatusUnauthorized, "wrong code")
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", pat.token, map[string]any{"challengeId": challenge, "code": code}), http.StatusNoContent, "step-up")
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", pat.token, map[string]any{"challengeId": challenge, "code": code}), http.StatusUnauthorized, "a code works once")

	// Erasure now goes through, and undo works inside the step-up window.
	er := j.must(j.call(http.MethodPost, "/v1/me/account-erasure", pat.token, nil), http.StatusAccepted, "erasure after step-up")
	reqID := er.str("requestId")
	j.must(j.call(http.MethodPost, "/v1/me/account-erasure/"+reqID+"/undo", pat.token, nil), http.StatusNoContent, "undo")

	// Five minutes later the step-up has lapsed again.
	j.at(j.clock().Add(6 * time.Minute))
	j.refresh(&pat)
	j.must(j.call(http.MethodPost, "/v1/me/data-exports", pat.token, nil), http.StatusForbidden, "stale step-up")

	// Step-up codes share the sign-in send limit: two sign-ins and one step-up used this
	// number's three codes per 10 minutes (TM-02).
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", pat.token, nil), http.StatusTooManyRequests, "send limit")
	j.at(j.clock().Add(10 * time.Minute))
	j.refresh(&pat)

	// Five wrong codes burn the challenge and lock the number (shared with sign-in, TM-01).
	ch = j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", pat.token, nil), http.StatusAccepted, "second code")
	challenge = ch.str("challengeId")
	code, _ = j.lastCode(pat.phone)
	for i := 0; i < 5; i++ {
		j.call(http.MethodPost, "/v1/auth/step-up/otp", pat.token, map[string]any{"challengeId": challenge, "code": wrong})
	}
	if r := j.call(http.MethodPost, "/v1/auth/step-up/otp", pat.token, map[string]any{"challengeId": challenge, "code": code}); r.status != http.StatusUnauthorized && r.status != http.StatusTooManyRequests {
		t.Fatalf("after five wrong codes even the right one fails: %d %s", r.status, r.raw)
	}
	if r := j.call(http.MethodPost, "/v1/auth/step-up/otp-challenges", pat.token, nil); r.status != http.StatusTooManyRequests {
		t.Fatalf("the number is locked: %d %s", r.status, r.raw)
	}
	// An unknown challenge looks like a wrong code.
	j.must(j.call(http.MethodPost, "/v1/auth/step-up/otp", other.token, map[string]any{"challengeId": ids.New(), "code": "123456"}), http.StatusUnauthorized, "unknown challenge")
}
