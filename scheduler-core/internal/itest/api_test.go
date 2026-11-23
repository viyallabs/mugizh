package itest

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/api"
)

const specPath = "../../../api/scheduler/scheduler-core.v1.yaml"

func auth() map[string]string { return map[string]string{"Authorization": "Bearer " + testToken} }

func withKey(h map[string]string, key string) map[string]string {
	out := map[string]string{"Idempotency-Key": key}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func createBody(subject uuid.UUID, extra string) string {
	return `{"subjectRef":"` + subject.String() + `","appRef":"core","externalRef":"rx-item-1","typeKey":"med.dose","rrule":"FREQ=DAILY","timeZone":"Europe/London","startsAt":"2026-06-01T07:00:00Z"` + extra + `}`
}

// expectProblem checks status, content type, the code, and that the code is documented in the OpenAPI spec.
func expectProblem(t *testing.T, resp *http.Response, body string, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status %d, want %d (%s)", resp.StatusCode, status, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	var p struct {
		Code    string `json:"code"`
		Status  int    `json:"status"`
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != code || p.Status != status || p.TraceID == "" {
		t.Fatalf("problem %+v, want code %s", p, code)
	}
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spec), "- "+code+"\n") {
		t.Fatalf("code %s is not listed in the OpenAPI spec", code)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("Cache-Control: no-store missing")
	}
}

// TC-61 runtime half: a health field in the create body is rejected, whatever it is called.
func TestTC61UnknownFieldRejected(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	ts := apiServer(t, svc)
	for _, extra := range []string{`,"medicineName":"x"`, `,"notes":"y"`, `,"diagnosis":"z"`, `,"externalRef2":"a"`} {
		resp, body := do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), createBody(uuid.New(), extra))
		expectProblem(t, resp, body, 400, "request.invalid")
		if strings.Contains(body, "medicineName") || strings.Contains(body, "diagnosis") {
			t.Fatalf("the rejected field name must not be echoed: %s", body)
		}
	}
	// PATCH too.
	resp, body := do(t, "PATCH", ts.URL+"/v1/activities/"+uuid.NewString(), withKey(map[string]string{"Authorization": "Bearer " + testToken, "If-Match": `"1"`}, uuid.NewString()), `{"medicineName":"x"}`)
	expectProblem(t, resp, body, 400, "request.invalid")
	// Trailing data and wrong types.
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), createBody(uuid.New(), "")+` {}`)
	expectProblem(t, resp, body, 400, "request.invalid")
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), `{"subjectRef":5}`)
	expectProblem(t, resp, body, 400, "request.invalid")
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), `{"rrule":"`+strings.Repeat("A", 70000)+`"}`)
	expectProblem(t, resp, body, 413, "request.too_large")
}

func TestAPIAuthDeniesByDefault(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	ts := apiServer(t, svc)
	subject := uuid.New()
	cases := map[string]map[string]string{
		"missing":      {},
		"wrong":        {"Authorization": "Bearer wrong-token"},
		"empty":        {"Authorization": "Bearer "},
		"wrong scheme": {"Authorization": "Basic " + testToken},
		"prefix only":  {"Authorization": "Bearer " + testToken[:10]},
		"suffix":       {"Authorization": "Bearer " + testToken + "x"},
	}
	for name, h := range cases {
		for _, route := range []struct{ m, p string }{
			{"POST", "/v1/activities"}, {"GET", "/v1/activities/" + uuid.NewString()}, {"GET", "/v1/occurrences?subjectRef=" + subject.String()},
			{"POST", "/v1/subjects/" + subject.String() + "/erase"}, {"POST", "/v1/occurrences/" + uuid.NewString() + "/acknowledge"},
		} {
			resp, body := do(t, route.m, ts.URL+route.p, withKey(h, uuid.NewString()), createBody(subject, ""))
			if resp.StatusCode != 401 {
				t.Fatalf("%s %s %s: status %d", name, route.m, route.p, resp.StatusCode)
			}
			expectProblem(t, resp, body, 401, "auth.unauthenticated")
		}
	}
	// A server with no configured tokens refuses everything.
	s := &api.Server{Svc: svc, Log: quietLog()}
	if s.TokenHashes != nil {
		t.Fatal("test setup")
	}
	// Health endpoints need no token.
	if resp, _ := do(t, "GET", ts.URL+"/health/live", nil, ""); resp.StatusCode != 200 {
		t.Fatal("live")
	}
}

func TestAPIFlow(t *testing.T) {
	svc, _, _ := newService(t, utc("2026-06-01T00:00:00Z"))
	ts := apiServer(t, svc)
	subject := uuid.New()
	key := uuid.NewString()

	// Idempotency-Key is required.
	resp, body := do(t, "POST", ts.URL+"/v1/activities", auth(), createBody(subject, ""))
	expectProblem(t, resp, body, 400, "idempotency.key_required")

	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), key), createBody(subject, ""))
	if resp.StatusCode != 201 || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s etag=%q", resp.StatusCode, body, resp.Header.Get("ETag"))
	}
	var act struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		TimeZone  string `json:"timeZone"`
		StartsAt  string `json:"startsAt"`
		Generated string `json:"generatedThroughAt"`
	}
	if err := json.Unmarshal([]byte(body), &act); err != nil || act.State != "active" || act.Generated == "" {
		t.Fatalf("body %s %v", body, err)
	}

	// Replay: same key and body returns the same activity; a different body is 409.
	resp, body2 := do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), key), createBody(subject, ""))
	if resp.StatusCode != 201 || resp.Header.Get("Idempotent-Replayed") != "true" || !strings.Contains(body2, act.ID) {
		t.Fatalf("replay: %d %s", resp.StatusCode, body2)
	}
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), key), strings.Replace(createBody(subject, ""), "FREQ=DAILY", "FREQ=DAILY;BYHOUR=9", 1))
	expectProblem(t, resp, body, 409, "idempotency.key_reused")

	// Validation problems carry their own codes.
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), strings.Replace(createBody(subject, ""), "FREQ=DAILY", "FREQ=SECONDLY", 1))
	expectProblem(t, resp, body, 400, "activity.rrule_invalid")
	resp, body = do(t, "POST", ts.URL+"/v1/activities", withKey(auth(), uuid.NewString()), strings.Replace(createBody(subject, ""), "Europe/London", "Local", 1))
	expectProblem(t, resp, body, 400, "activity.time_zone_invalid")

	// GET, then PATCH with If-Match.
	resp, body = do(t, "GET", ts.URL+"/v1/activities/"+act.ID, auth(), "")
	if resp.StatusCode != 200 || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, "PATCH", ts.URL+"/v1/activities/"+act.ID, auth(), `{"timeZone":"America/New_York"}`)
	expectProblem(t, resp, body, 428, "precondition.required")
	h := auth()
	h["If-Match"] = `"7"`
	resp, body = do(t, "PATCH", ts.URL+"/v1/activities/"+act.ID, h, `{"timeZone":"America/New_York"}`)
	expectProblem(t, resp, body, 412, "resource.precondition_failed")
	h["If-Match"] = `"1"`
	resp, body = do(t, "PATCH", ts.URL+"/v1/activities/"+act.ID, h, `{}`)
	expectProblem(t, resp, body, 400, "request.invalid")
	resp, body = do(t, "PATCH", ts.URL+"/v1/activities/"+act.ID, h, `{"timeZone":"America/New_York"}`)
	if resp.StatusCode != 200 || resp.Header.Get("ETag") != `"2"` || !strings.Contains(body, "America/New_York") {
		t.Fatalf("patch: %d %s", resp.StatusCode, body)
	}

	// List with cursor pagination.
	resp, body = do(t, "GET", ts.URL+"/v1/occurrences?subjectRef="+subject.String()+"&limit=5", auth(), "")
	var page struct {
		Items []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil || resp.StatusCode != 200 || len(page.Items) != 5 || page.NextCursor == "" {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", ts.URL+"/v1/occurrences?subjectRef="+subject.String()+"&limit=100&cursor="+page.NextCursor, auth(), "")
	if err := json.Unmarshal([]byte(body), &struct{}{}); err != nil || resp.StatusCode != 200 {
		t.Fatalf("page 2: %d %s", resp.StatusCode, body)
	}
	for _, q := range []string{"", "?subjectRef=x", "?subjectRef=" + subject.String() + "&limit=0", "?subjectRef=" + subject.String() + "&from=yesterday", "?subjectRef=" + subject.String() + "&phone=1"} {
		resp, body = do(t, "GET", ts.URL+"/v1/occurrences"+q, auth(), "")
		expectProblem(t, resp, body, 400, "request.invalid")
	}

	// Acknowledge, miss, cancel.
	var pend []string
	for _, it := range page.Items {
		if it.State == "pending" {
			pend = append(pend, it.ID)
		}
	}
	if len(pend) < 2 {
		t.Fatalf("need two pending items, got %v", page.Items)
	}
	occ := pend[0]
	resp, body = do(t, "POST", ts.URL+"/v1/occurrences/"+occ+"/acknowledge", auth(), "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"acknowledged"`) {
		t.Fatalf("ack: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, "POST", ts.URL+"/v1/occurrences/"+occ+"/cancel", auth(), "")
	expectProblem(t, resp, body, 409, "occurrence.invalid_state")
	resp, body = do(t, "POST", ts.URL+"/v1/occurrences/"+pend[1]+"/miss", auth(), "")
	expectProblem(t, resp, body, 409, "occurrence.invalid_state")
	resp, body = do(t, "POST", ts.URL+"/v1/occurrences/not-a-uuid/acknowledge", auth(), "")
	expectProblem(t, resp, body, 404, "resource.not_found")

	// Pause / resume / cancel.
	for _, step := range []struct{ path, state string }{{"pause", "paused"}, {"resume", "active"}, {"cancel", "cancelled"}} {
		resp, body = do(t, "POST", ts.URL+"/v1/activities/"+act.ID+"/"+step.path, auth(), "")
		if resp.StatusCode != 200 || !strings.Contains(body, `"`+step.state+`"`) {
			t.Fatalf("%s: %d %s", step.path, resp.StatusCode, body)
		}
	}
	resp, body = do(t, "POST", ts.URL+"/v1/activities/"+act.ID+"/resume", auth(), "")
	expectProblem(t, resp, body, 409, "activity.invalid_state")
	resp, body = do(t, "GET", ts.URL+"/v1/activities/"+uuid.NewString(), auth(), "")
	expectProblem(t, resp, body, 404, "resource.not_found")
	resp, body = do(t, "GET", ts.URL+"/v1/nope", auth(), "")
	expectProblem(t, resp, body, 404, "resource.not_found")

	// Erasure.
	resp, body = do(t, "POST", ts.URL+"/v1/subjects/"+subject.String()+"/erase", auth(), "")
	var rep map[string]int
	if err := json.Unmarshal([]byte(body), &rep); err != nil || resp.StatusCode != 200 || rep["subjects"] != 1 || rep["activities"] != 1 || rep["occurrences"] == 0 {
		t.Fatalf("erase: %d %s", resp.StatusCode, body)
	}
	resp, body = do(t, "GET", ts.URL+"/v1/activities/"+act.ID, auth(), "")
	expectProblem(t, resp, body, 404, "resource.not_found")
	resp, body = do(t, "POST", ts.URL+"/v1/subjects/not-a-uuid/erase", auth(), "")
	expectProblem(t, resp, body, 400, "request.invalid")
}

// The router and the OpenAPI document describe the same operations (K-ARC-06).
func TestSpecMatchesRoutes(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/\S+):\s*$`)
	methodRe := regexp.MustCompile(`^    (get|post|patch|put|delete):\s*$`)
	var inSpec []string
	cur := ""
	for _, line := range strings.Split(string(spec), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
		} else if m := methodRe.FindStringSubmatch(line); m != nil && cur != "" {
			inSpec = append(inSpec, strings.ToUpper(m[1])+" "+cur)
		} else if strings.HasPrefix(line, "components:") {
			cur = ""
		}
	}
	s := &api.Server{Svc: nil, Log: quietLog()}
	var inRouter []string
	err = chi.Walk(s.Handler().(chi.Router), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if strings.HasPrefix(route, "/v1/") {
			inRouter = append(inRouter, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(inSpec)
	sort.Strings(inRouter)
	if strings.Join(inSpec, "\n") != strings.Join(inRouter, "\n") {
		t.Fatalf("spec and router differ\nspec:\n%s\nrouter:\n%s", strings.Join(inSpec, "\n"), strings.Join(inRouter, "\n"))
	}
	if len(inSpec) != 12 {
		t.Fatalf("expected 12 operations, found %d", len(inSpec))
	}
}
