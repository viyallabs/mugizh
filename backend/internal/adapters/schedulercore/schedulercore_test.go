package schedulercore_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

var (
	_ schedulercore.Client = (*schedulercore.HTTP)(nil)
	_ schedulercore.Client = (*fake.Client)(nil)
)

// The HTTP client sends the bearer token, the idempotency key and only the fields of the
// scheduler-core spec (K-ARC-02).
func TestHTTPClient_SendsOnlyAllowedFields(t *testing.T) {
	var body map[string]any
	var auth, idem, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, idem, path = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.URL.Path
		if r.Method == http.MethodPost && r.URL.Path == "/v1/activities" {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + ids.New() + `","externalRef":"x","state":"active"}`))
			return
		}
		if r.URL.Path == "/v1/activities/gone/cancel" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := schedulercore.NewHTTP(srv.URL, "service-token")
	if err != nil {
		t.Fatal(err)
	}
	key := ids.New()
	end := time.Now().Add(time.Hour)
	a, err := c.CreateActivity(context.Background(), schedulercore.CreateRequest{IdempotencyKey: key, SubjectRef: ids.New(), AppRef: "core", ExternalRef: ids.New(),
		TypeKey: schedulercore.MedDose, RRule: "FREQ=DAILY", TimeZone: "Europe/London", StartsAt: time.Now(), EndsAt: &end})
	if err != nil || a.ID == "" {
		t.Fatalf("%+v %v", a, err)
	}
	if auth != "Bearer service-token" || idem != key {
		t.Fatalf("headers %q %q", auth, idem)
	}
	allowed := map[string]bool{"subjectRef": true, "appRef": true, "externalRef": true, "typeKey": true, "rrule": true, "timeZone": true, "startsAt": true, "endsAt": true}
	for k := range body {
		if !allowed[k] {
			t.Fatalf("unexpected field %s", k)
		}
	}
	if err := c.CancelActivity(context.Background(), "gone"); adapters.KindOf(err) != adapters.NotFound {
		t.Fatalf("%v", err)
	}
	if path == "" {
		t.Fatal("no request")
	}
	if _, err := schedulercore.NewHTTP("", ""); err == nil {
		t.Fatal("a token is required")
	}
}

func TestFake_ReplaysByIdempotencyKey(t *testing.T) {
	f := &fake.Client{}
	r := schedulercore.CreateRequest{IdempotencyKey: ids.New(), ExternalRef: "e"}
	a, _ := f.CreateActivity(context.Background(), r)
	b, _ := f.CreateActivity(context.Background(), r)
	if a.ID != b.ID || len(f.Items) != 1 {
		t.Fatal("not idempotent")
	}
}

var (
	_ schedulercore.Occurrences = (*schedulercore.HTTP)(nil)
	_ schedulercore.Occurrences = (*fake.Client)(nil)
)

// S-153/S-154: the client lists with paging and sends only the spec's action fields; problem codes become reasons.
func TestHTTPClient_OccurrencesAndActions(t *testing.T) {
	var gotBody map[string]any
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/occurrences":
			pages++
			q := r.URL.Query()
			if q.Get("subjectRef") == "" || q.Get("from") == "" || q.Get("to") == "" || q.Get("limit") != "100" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if q.Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"items":[{"id":"o1","activityId":"a1","externalRef":"x","typeKey":"med.dose","timeZone":"UTC","dueAt":"2026-06-01T07:00:00Z","state":"pending","snoozeCount":0}],"nextCursor":"c2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"o2","activityId":"a1","externalRef":"x","typeKey":"med.dose","timeZone":"UTC","dueAt":"2026-06-02T07:00:00Z","state":"acknowledged","action":"taken","actedAt":"2026-06-02T07:01:00Z","snoozeCount":1}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/occurrences/o1/actions":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Idempotent-Replayed", "true")
			_, _ = w.Write([]byte(`{"id":"o1","activityId":"a1","externalRef":"x","typeKey":"med.dose","timeZone":"UTC","dueAt":"2026-06-01T07:00:00Z","state":"acknowledged","action":"taken","snoozeCount":0}`))
		case r.URL.Path == "/v1/occurrences/o2/actions":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"code":"occurrence.action_conflict"}`))
		case r.URL.Path == "/v1/occurrences/o3/actions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":400,"code":"request.invalid","errors":[{"field":"actedAt","code":"out_of_range"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := schedulercore.NewHTTP(srv.URL, "service-token")
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	occs, err := c.ListOccurrences(context.Background(), schedulercore.ListOccurrencesRequest{SubjectRef: ids.New(), From: from, To: from.Add(48 * time.Hour)})
	if err != nil || len(occs) != 2 || pages != 2 || occs[1].Action != "taken" || occs[1].SnoozeCount != 1 || occs[0].TimeZone != "UTC" {
		t.Fatalf("%+v pages=%d %v", occs, pages, err)
	}
	res, err := c.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: "o1", SubjectRef: ids.New(), ClientActionID: ids.New(),
		Action: schedulercore.ActionTaken, ActedAt: from})
	if err != nil || !res.Replayed || res.Occurrence.Action != "taken" {
		t.Fatalf("%+v %v", res, err)
	}
	allowed := map[string]bool{"clientActionId": true, "subjectRef": true, "action": true, "actedAt": true, "snoozeMinutes": true}
	for k := range gotBody {
		if !allowed[k] {
			t.Fatalf("unexpected field %s", k)
		}
	}
	_, err = c.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: "o2", Action: "taken"})
	var ae *adapters.Error
	if !errors.As(err, &ae) || ae.Kind != adapters.Rejected || ae.Reason != schedulercore.ReasonActionConflict {
		t.Fatalf("conflict: %v", err)
	}
	_, err = c.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: "o3", Action: "taken"})
	if !errors.As(err, &ae) || ae.Kind != adapters.Invalid || ae.Reason != schedulercore.ReasonActedAtRange {
		t.Fatalf("range: %v", err)
	}
	_, err = c.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: "zz", Action: "taken"})
	if adapters.KindOf(err) != adapters.NotFound {
		t.Fatalf("not found: %v", err)
	}
}

func TestFake_ActionRulesMatchScheduler(t *testing.T) {
	now := time.Date(2026, 6, 1, 7, 5, 0, 0, time.UTC)
	f := &fake.Client{Now: func() time.Time { return now }}
	subj := ids.New()
	f.AddOccurrence(subj, schedulercore.Occurrence{ID: "o1", DueAt: now.Add(-5 * time.Minute), State: "dispatched"})
	do := func(subject, action string, at time.Time) (schedulercore.ActionResult, error) {
		return f.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: "o1", SubjectRef: subject, ClientActionID: ids.New(), Action: action, ActedAt: at})
	}
	if _, err := do(ids.New(), "taken", now); adapters.KindOf(err) != adapters.NotFound {
		t.Fatalf("other subject: %v", err)
	}
	if _, err := do(subj, "taken", now.Add(time.Hour)); adapters.KindOf(err) != adapters.Invalid {
		t.Fatalf("future: %v", err)
	}
	if r, err := do(subj, "taken", now); err != nil || r.Occurrence.State != "acknowledged" {
		t.Fatalf("taken: %v %+v", err, r)
	}
	if _, err := do(subj, "skipped", now); adapters.KindOf(err) != adapters.Rejected {
		t.Fatalf("conflict: %v", err)
	}
}
