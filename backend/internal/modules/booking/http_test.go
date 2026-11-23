package booking_test

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
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/httpapi"
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

func patientPrincipal(id string) *ip.Principal {
	return &ip.Principal{UserID: id, App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}
}

func proPrincipal(id, role, clinic string) *ip.Principal {
	now := time.Now()
	return &ip.Principal{UserID: id, App: ip.AppPro, Roles: []ip.RoleGrant{{Role: role, ClinicRef: clinic}}, MFAAt: &now}
}

func TestHTTPBookingFlowAndOwnership(t *testing.T) {
	h := newHTTP(t)
	p := h.practice(t)
	pat, other := ids.New(), ids.New()
	h.auth["pat"], h.auth["other"] = patientPrincipal(pat), patientPrincipal(other)
	h.auth["doc"] = proPrincipal(p.ProfessionalRef, ip.RoleDoctor, "")
	h.auth["desk"] = proPrincipal(ids.New(), ip.RoleFrontDesk, p.ClinicRef)
	slots := h.openSlots(t, p, booking.ModeInPerson)

	st, body := h.do(t, "GET", "/v1/practices/"+p.ID+"/slots?limit=5", "pat", nil)
	if items, _ := body["items"].([]any); st != 200 || len(items) != 5 || body["nextCursor"] == nil {
		t.Fatalf("slots: %d %v", st, body)
	}
	req := map[string]any{"slotId": slots[8].ID, "mode": "in_person", "patientTimeZone": abroadZone}
	st, a := h.do(t, "POST", "/v1/practices/"+p.ID+"/holds", "pat", req)
	if st != 201 || a["state"] != "held" || a["refundPolicyVersion"] != testPolicy || a["feeAmount"] != float64(fee) || a["patientTimeZone"] != abroadZone {
		t.Fatalf("hold: %d %v", st, a)
	}
	id := a["appointmentId"].(string)
	// The slot is gone: 409 with next slots.
	st, body = h.do(t, "POST", "/v1/practices/"+p.ID+"/holds", "other", req)
	if next, _ := body["nextSlots"].([]any); st != 409 || body["code"] != "booking.slot_taken" || len(next) == 0 {
		t.Fatalf("slot taken: %d %v", st, body)
	}
	// Online without the India confirmation: 422.
	v := h.openSlots(t, p, booking.ModeVideo)[1]
	st, body = h.do(t, "POST", "/v1/practices/"+p.ID+"/holds", "other", map[string]any{"slotId": v.ID, "mode": "video", "patientTimeZone": abroadZone})
	if st != 422 || body["code"] != "booking.patient_outside_india" {
		t.Fatalf("P10: %d %v", st, body)
	}
	// Others' appointments are 404 on every patient route.
	for _, c := range [][2]string{{"GET", "/v1/me/appointments/" + id}, {"POST", "/v1/me/appointments/" + id + "/checkout"},
		{"GET", "/v1/me/appointments/" + id + "/cancel-preview"}} {
		if st, _ := h.do(t, c[0], c[1], "other", nil); st != 404 {
			t.Errorf("%s %s: %d", c[0], c[1], st)
		}
	}
	if st, _ := h.do(t, "POST", "/v1/me/appointments/"+id+"/cancel", "other", map[string]any{"acknowledgedRefundAmount": 0}); st != 404 {
		t.Errorf("cancel by other: %d", st)
	}
	st, body = h.do(t, "POST", "/v1/me/appointments/"+id+"/checkout", "pat", nil)
	if st != 200 || body["checkoutUrl"] == nil {
		t.Fatalf("checkout: %d %v", st, body)
	}
	// A patient token cannot use staff routes; front desk cannot change availability.
	if st, _ := h.do(t, "GET", "/v1/practices/"+p.ID+"/availability", "pat", nil); st != 403 {
		t.Errorf("patient on staff route: %d", st)
	}
	if st, _ := h.do(t, "PUT", "/v1/practices/"+p.ID+"/availability", "desk", map[string]any{"rules": []any{}}); st != 403 {
		t.Errorf("front desk write: %d", st)
	}
	if st, _ := h.do(t, "GET", "/v1/practices/"+p.ID+"/availability", "desk", nil); st != 200 {
		t.Errorf("front desk read: %d", st)
	}
	if st, _ := h.do(t, "GET", "/v1/practices/"+ids.New()+"/availability", "doc", nil); st != 404 {
		t.Errorf("unknown practice: %d", st)
	}
	// Day view for front desk: booking fields only (no fee in the schema).
	loc, _ := time.LoadLocation(practiceZone)
	st, body = h.do(t, "GET", "/v1/practices/"+p.ID+"/day?date="+slots[8].StartsAt.In(loc).Format("2006-01-02"), "desk", nil)
	if st != 200 {
		t.Fatalf("day: %d %v", st, body)
	}
}
