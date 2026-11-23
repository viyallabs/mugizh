package consult_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/httpapi"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

type tokenAuth map[string]*ip.Principal

func (a tokenAuth) Authenticate(_ context.Context, bearer string) (*ip.Principal, error) {
	if p, ok := a[bearer]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, problem.Unauthenticated
}

type httpFx struct {
	*fx
	srv  *httptest.Server
	auth tokenAuth
}

func newHTTP(t *testing.T) *httpFx {
	t.Helper()
	f := setup(t)
	app.UseStrictJSON()
	spec, err := httpapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	pol, err := app.BuildPolicies(spec)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &httpFx{fx: f, auth: tokenAuth{}}
	r := chi.NewRouter()
	r.Use(app.Authn(h.auth))
	si := httpapi.NewStrictHandlerWithOptions(httpapi.Handlers{M: f.m},
		[]httpapi.StrictMiddlewareFunc{httpapi.StrictMiddlewareFunc(app.PolicyMiddleware[httpapi.StrictHandlerFunc](pol, time.Now))},
		httpapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: app.RequestError, ResponseErrorHandlerFunc: app.ResponseError(log)})
	httpapi.HandlerWithOptions(si, httpapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: app.RequestError})
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *httpFx) do(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", ids.New())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func TestHTTPConsultFlowRolesAndOwnership(t *testing.T) {
	h := newHTTP(t)
	c := h.confirm(t, "video")
	now := time.Now()
	h.auth["pat"] = &ip.Principal{UserID: c.patient, App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}
	h.auth["other"] = &ip.Principal{UserID: ids.New(), App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}
	h.auth["doc"] = &ip.Principal{UserID: c.doctor, App: ip.AppPro, Roles: []ip.RoleGrant{{Role: ip.RoleDoctor, ClinicRef: c.clinic}}, MFAAt: &now}
	h.auth["desk"] = &ip.Principal{UserID: ids.New(), App: ip.AppPro, Roles: []ip.RoleGrant{{Role: ip.RoleFrontDesk, ClinicRef: c.clinic}}, MFAAt: &now}
	base := "/v1/consults/" + c.session

	// Another patient and clinic staff get nothing (404 / 403).
	if st, _ := h.do(t, "GET", base, "other", nil); st != 404 {
		t.Fatalf("other patient: %d", st)
	}
	if st, _ := h.do(t, "GET", base, "desk", nil); st != 403 {
		t.Fatalf("front desk: %d", st)
	}
	// Join without the consent is refused.
	if st, body := h.do(t, "POST", base+"/join", "pat", nil); st != 403 || body["code"] != "consult.consent_required" {
		t.Fatalf("join without consent: %d %v", st, body)
	}
	pre := map[string]any{"deviceCheck": map[string]any{"cameraOk": true, "microphoneOk": true, "networkOk": true},
		"consent": map[string]any{"textId": "TELE-CONSENT", "textVersion": 2, "inIndiaNow": false}}
	if st, body := h.do(t, "POST", base+"/pre-join", "pat", pre); st != 403 || body["code"] != "consult.patient_location_not_allowed" {
		t.Fatalf("outside India: %d %v", st, body)
	}
	pre["consent"].(map[string]any)["inIndiaNow"] = true
	if st, body := h.do(t, "POST", base+"/pre-join", "pat", pre); st != 200 || body["consentRecorded"] != true || body["patientInIndiaDeclared"] != true {
		t.Fatalf("pre-join: %d %v", st, body)
	}
	// The patient cannot call doctor-only operations.
	if st, _ := h.do(t, "POST", base+"/identity-confirmation", "pat", map[string]any{"nameConfirmed": true, "ageConfirmed": true}); st != 403 {
		t.Fatalf("patient confirm: %d", st)
	}
	if st, body := h.do(t, "POST", base+"/join", "pat", nil); st != 200 || body["token"] == "" || body["token"] == nil {
		t.Fatalf("join: %d %v", st, body)
	}
	if st, body := h.do(t, "POST", base+"/join", "doc", nil); st != 200 || body["mode"] != "video" {
		t.Fatalf("doctor join: %d %v", st, body)
	}
	// Not both connected yet: the doctor cannot confirm identity or complete.
	if st, body := h.do(t, "POST", base+"/identity-confirmation", "doc", map[string]any{"nameConfirmed": true, "ageConfirmed": true}); st != 409 || body["code"] != "consult.not_connected" {
		t.Fatalf("confirm early: %d %v", st, body)
	}
	if st, body := h.do(t, "POST", base+"/outcome", "doc", map[string]any{"outcome": "completed"}); st != 409 || body["code"] != "consult.not_completable" {
		t.Fatalf("complete early: %d %v", st, body)
	}
	// By appointment, the doctor sees the patient's identity once the gate allows it.
	st, body := h.do(t, "GET", "/v1/appointments/"+c.appt+"/consult", "doc", nil)
	pt, _ := body["patient"].(map[string]any)
	if st != 200 || pt["fullName"] != "Test Patient" {
		t.Fatalf("doctor view: %d %v", st, body)
	}
	if st, body := h.do(t, "GET", base, "pat", nil); st != 200 || body["patient"] != nil {
		t.Fatalf("patient view: %d %v", st, body)
	}
}
