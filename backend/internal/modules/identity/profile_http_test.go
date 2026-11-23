package identity_test

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
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/httpapi"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

type profileAuth map[string]*ip.Principal

func (a profileAuth) Authenticate(_ context.Context, bearer string) (*ip.Principal, error) {
	if p, ok := a[bearer]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, problem.Unauthenticated
}

func TestS108ProfileHTTP(t *testing.T) {
	f, uid := profileSetup(t)
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
	auth := profileAuth{"pat": {UserID: uid, App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}}
	now := time.Now()
	auth["doc"] = &ip.Principal{UserID: ids.New(), App: ip.AppPro, Roles: []ip.RoleGrant{{Role: ip.RoleDoctor}}, MFAAt: &now}
	r := chi.NewRouter()
	r.Use(app.Authn(auth))
	si := httpapi.NewStrictHandlerWithOptions(httpapi.Handlers{M: f.m},
		[]httpapi.StrictMiddlewareFunc{httpapi.StrictMiddlewareFunc(app.PolicyMiddleware[httpapi.StrictHandlerFunc](pol, time.Now))},
		httpapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: app.RequestError, ResponseErrorHandlerFunc: app.ResponseError(log)})
	httpapi.HandlerWithOptions(si, httpapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: app.RequestError})
	srv := httptest.NewServer(r)
	defer srv.Close()
	do := func(method, token string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			j, _ := json.Marshal(body)
			rd = bytes.NewReader(j)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+"/v1/me/profile", rd)
		req.Header.Set("Content-Type", "application/json")
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
	if st, body := do("GET", "pat", nil); st != 404 || body["code"] != "identity.profile_missing" {
		t.Fatalf("empty: %d %v", st, body)
	}
	good := map[string]any{"fullName": "Test Patient", "dateOfBirth": f.now.AddDate(-30, 0, -3).Format("2006-01-02"), "sex": "male",
		"preferredLanguage": "ta", "bloodGroup": "B+", "allergies": []string{"dust"}, "emergencyContact": map[string]any{"name": "Test Relative", "phone": phone("78")}}
	if st, body := do("PUT", "pat", good); st != 204 {
		t.Fatalf("put: %d %v", st, body)
	}
	if st, body := do("GET", "pat", nil); st != 200 || body["fullName"] != "Test Patient" || body["bloodGroup"] != "B+" {
		t.Fatalf("get: %d %v", st, body)
	}
	young := map[string]any{"fullName": "Test Child", "dateOfBirth": f.now.AddDate(-10, 0, 0).Format("2006-01-02"), "sex": "male"}
	if st, body := do("PUT", "pat", young); st != 422 || body["code"] != "identity.profile_underage" {
		t.Fatalf("underage: %d %v", st, body)
	}
	// A doctor cannot read or write a patient profile through this route (K-SEC-04).
	if st, _ := do("GET", "doc", nil); st != 403 {
		t.Fatalf("doctor get: %d", st)
	}
	if st, _ := do("PUT", "doc", good); st != 403 {
		t.Fatalf("doctor put: %d", st)
	}
}
