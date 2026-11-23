package directory_test

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
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/httpapi"
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

func (h *httpFx) token(name string, p *ip.Principal) string { h.auth[name] = p; return name }

func proPrincipal(user string, grants ...ip.RoleGrant) *ip.Principal {
	now := time.Now()
	return &ip.Principal{UserID: user, App: ip.AppPro, Roles: grants, MFAAt: &now}
}

type httpResp struct {
	status int
	body   map[string]any
}

func (h *httpFx) do(t *testing.T, method, path, token string, body any) httpResp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := httpResp{status: resp.StatusCode, body: map[string]any{}}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out.body)
	return out
}

func TestHTTPPublicSearchAndProfile(t *testing.T) {
	h := newHTTP(t)
	pr, doc, clinic := h.listedPractice(t, "Doctor H", baseLat+kmLat, baseLng, 45000, directory.ModeInPerson, directory.ModeVideo)

	r := h.do(t, "POST", "/v1/practices/search", "", map[string]any{"location": map[string]any{"lat": baseLat, "lng": baseLng}, "radiusKm": 5})
	items, _ := r.body["items"].([]any)
	if r.status != 200 || len(items) != 1 {
		t.Fatalf("search: %d %v", r.status, r.body)
	}
	it := items[0].(map[string]any)
	fee := it["fee"].(map[string]any)
	if it["practiceId"] != pr || it["verified"] != true || it["distanceKm"] != 1.0 || fee["amount"] != 45000.0 || fee["currency"] != "TST" || fee["version"] != "1" {
		t.Fatalf("item: %v", it)
	}
	if it["professional"].(map[string]any)["specialtyLabelKey"] != "directory.general_practice_mbbs" {
		t.Fatalf("professional: %v", it["professional"])
	}
	// Location is in the body, never the URL: a GET with coordinates finds nothing.
	if g := h.do(t, "GET", "/v1/practices/search?lat=20&lng=77", "", nil); g.status == 200 {
		t.Fatalf("GET search answered 200: %v", g.body)
	}
	if r := h.do(t, "POST", "/v1/practices/search", "", `{"location":{"lat":1,"lng":1},"patientName":"x"}`); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}
	if r := h.do(t, "POST", "/v1/practices/search", "", map[string]any{"radiusKm": 5}); r.status != 400 || r.body["code"] != "directory.invalid" {
		t.Fatalf("missing location: %d %v", r.status, r.body)
	}
	if r := h.do(t, "POST", "/v1/practices/search", "", map[string]any{"location": map[string]any{"lat": 1, "lng": 1}, "availableWithinDays": 2}); r.status != 422 {
		t.Fatalf("availability filter: %d %v", r.status, r.body)
	}

	p := h.do(t, "GET", "/v1/practices/"+pr, "", nil)
	if p.status != 200 || p.body["timeZone"] != "UTC" {
		t.Fatalf("profile: %d %v", p.status, p.body)
	}
	v := p.body["verification"].(map[string]any)
	if v["verified"] != true || len(v["checks"].([]any)) != 1 || v["verifiedAt"] == nil {
		t.Fatalf("verification: %v", v)
	}
	h.status(t, doc, "suspended", h.tick())
	if p := h.do(t, "GET", "/v1/practices/"+pr, "", nil); p.status != 404 {
		t.Fatalf("suspended profile: %d", p.status)
	}
	h.verify(t, doc)
	h.status(t, clinic, "suspended", h.tick())
	if p := h.do(t, "GET", "/v1/practices/"+pr, "", nil); p.status != 404 {
		t.Fatalf("suspended clinic profile: %d", p.status)
	}
	if p := h.do(t, "GET", "/v1/practices/"+ids.New(), "", nil); p.status != 404 {
		t.Fatalf("unknown profile: %d", p.status)
	}
}

func TestHTTPManagementPolicyAndOwnership(t *testing.T) {
	h := newHTTP(t)
	docID := ids.New()
	pr, _, c1 := h.listedPractice(t, "Doctor M1", baseLat, baseLng, 1000)
	_, _, c2 := h.listedPractice(t, "Doctor M2", baseLat+1, baseLng, 1000)
	docTok := h.token("doc", proPrincipal(docID, ip.RoleGrant{Role: ip.RoleDoctor}))
	adm1 := h.token("adm1", proPrincipal(ids.New(), ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: c1}))
	adm2 := h.token("adm2", proPrincipal(ids.New(), ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: c2}))
	patient := h.token("pat", &ip.Principal{UserID: ids.New(), App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}})
	nomfa := h.token("nomfa", &ip.Principal{UserID: docID, App: ip.AppPro, Roles: []ip.RoleGrant{{Role: ip.RoleDoctor}}})

	profile := map[string]any{"displayName": "Doctor Nine", "languages": []string{"English", "Tamil"}}
	if r := h.do(t, "PUT", "/v1/me/professional", "", profile); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := h.do(t, "PUT", "/v1/me/professional", patient, profile); r.status != 403 {
		t.Fatalf("patient: %d %v", r.status, r.body)
	}
	if r := h.do(t, "PUT", "/v1/me/professional", nomfa, profile); r.status != 403 || r.body["code"] != "auth.mfa_required" {
		t.Fatalf("no MFA: %d %v", r.status, r.body)
	}
	r := h.do(t, "PUT", "/v1/me/professional", docTok, profile)
	if r.status != 200 || r.body["professionalId"] != docID || r.body["listed"] != false || r.body["specialtyLabelKey"] != "directory.general_practice_mbbs" {
		t.Fatalf("doctor upsert: %d %v", r.status, r.body)
	}
	bad := h.do(t, "PUT", "/v1/me/professional", docTok, map[string]any{"displayName": "The Best Doctor", "languages": []string{"English"}})
	if bad.status != 400 || bad.body["code"] != "directory.invalid" {
		t.Fatalf("claim: %d %v", bad.status, bad.body)
	}
	if r := h.do(t, "PUT", "/v1/me/professional", docTok, map[string]any{"displayName": "Doctor Nine", "languages": []string{"English"}, "specialty": "cardiology"}); r.status != 400 {
		t.Fatalf("specialty without qualification: %d %v", r.status, r.body)
	}

	if r := h.do(t, "PATCH", "/v1/practices/"+pr, adm2, map[string]any{"feeAmount": 5}); r.status != 404 {
		t.Fatalf("foreign clinic admin: %d %v", r.status, r.body)
	}
	if r := h.do(t, "PATCH", "/v1/practices/"+pr, docTok, map[string]any{"feeAmount": 5}); r.status != 404 {
		t.Fatalf("foreign doctor: %d %v", r.status, r.body)
	}
	if r := h.do(t, "PATCH", "/v1/practices/"+pr, patient, map[string]any{"feeAmount": 5}); r.status != 403 {
		t.Fatalf("patient: %d", r.status)
	}
	if r := h.do(t, "PATCH", "/v1/practices/"+pr, "", map[string]any{"feeAmount": 5}); r.status != 401 {
		t.Fatalf("anonymous: %d", r.status)
	}
	ok := h.do(t, "PATCH", "/v1/practices/"+pr, adm1, map[string]any{"feeAmount": 5})
	fee, _ := ok.body["fee"].(map[string]any)
	if ok.status != 200 || fee["amount"] != 5.0 || fee["version"] != "2" || ok.body["listed"] != true {
		t.Fatalf("owner patch: %d %v", ok.status, ok.body)
	}
	if r := h.do(t, "PATCH", "/v1/practices/"+pr, adm1, `{}`); r.status != 400 {
		t.Fatalf("empty patch: %d", r.status)
	}
	if r := h.do(t, "PATCH", "/v1/practices/"+pr, adm1, `{"feeAmount":5,"verified":true}`); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}

	newDoc := h.doctor(t, "Doctor New")
	body := map[string]any{"doctorId": newDoc, "modes": []string{"video"}, "feeAmount": 700, "timings": "Mon 10:00-12:00"}
	if r := h.do(t, "POST", "/v1/clinics/"+c1+"/practices", adm2, body); r.status != 404 {
		t.Fatalf("foreign clinic create: %d %v", r.status, r.body)
	}
	if r := h.do(t, "POST", "/v1/clinics/"+c1+"/practices", adm1, body); r.status != 201 || r.body["listed"] != false {
		t.Fatalf("create: %d %v", r.status, r.body)
	}
}

func TestHTTPSubmitClinic(t *testing.T) {
	h := newHTTP(t)
	lead := h.doctor(t, "Lead H")
	body := map[string]any{"name": "Http Clinic", "address": "3 Sample Road", "pin": map[string]any{"lat": 10.5, "lng": 20.5}, "leadDoctorId": lead}
	user := ids.New()
	patient := h.token("pat", &ip.Principal{UserID: user, App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}})
	nomfa := h.token("nomfa", &ip.Principal{UserID: user, App: ip.AppPro})
	pro := h.token("pro", proPrincipal(user))

	if r := h.do(t, "POST", "/v1/clinics", "", body); r.status != 401 {
		t.Fatalf("anonymous: %d", r.status)
	}
	if r := h.do(t, "POST", "/v1/clinics", patient, body); r.status != 403 || r.body["code"] != "auth.wrong_app" {
		t.Fatalf("patient app: %d %v", r.status, r.body)
	}
	if r := h.do(t, "POST", "/v1/clinics", nomfa, body); r.status != 403 {
		t.Fatalf("no MFA: %d %v", r.status, r.body)
	}
	r := h.do(t, "POST", "/v1/clinics", pro, body)
	if r.status != 201 || r.body["listed"] != false || r.body["locationSource"] != "manual_pin" {
		t.Fatalf("submit: %d %v", r.status, r.body)
	}
	if len(h.roles.assigned) != 1 || h.roles.assigned[0][1] != ip.RoleClinicAdmin || h.roles.assigned[0][2] != r.body["clinicId"] {
		t.Fatalf("role assignment: %v", h.roles.assigned)
	}
}
