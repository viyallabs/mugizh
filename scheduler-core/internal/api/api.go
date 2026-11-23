// Package api is scheduler-core's HTTP layer. The handlers match api/scheduler/scheduler-core.v1.yaml
// (K-ARC-06). It is called only by the Core API with a service credential (deny by default). Request bodies
// are decoded with DisallowUnknownFields, so any field outside the spec, such as a medicine name, is rejected
// (K-ARC-02, TC-61 runtime half).
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

// MaxBodyBytes caps request bodies (API standards section 8 allows 1 MB; scheduler bodies are tiny).
const MaxBodyBytes = 64 << 10

// Problems owned by the API layer (the others live in package sched).
var (
	errKeyRequired     = problem.New(http.StatusBadRequest, "idempotency.key_required", "An Idempotency-Key header (UUID) is required")
	errPreconditionReq = problem.New(http.StatusPreconditionRequired, "precondition.required", "An If-Match header is required")
	errUnauthenticated = problem.Unauthenticated
)

// Server holds the handlers' dependencies.
type Server struct {
	Svc *sched.Service
	Log *slog.Logger
	// TokenHashes are SHA-256 digests of the accepted service tokens (several allow rotation). Empty means every
	// request is refused.
	TokenHashes [][]byte
	// Ready reports whether the service can take traffic (database reachable).
	Ready func(ctx context.Context) error
}

type traceKey struct{}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	problem.TraceIDFunc = func(r *http.Request) string {
		v, _ := r.Context().Value(traceKey{}).(string)
		return v
	}
	r := chi.NewRouter()
	r.Use(s.base)
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/health/ready", func(w http.ResponseWriter, req *http.Request) {
		if s.Ready != nil {
			if err := s.Ready(req.Context()); err != nil {
				problem.Write(w, req, problem.Unavailable)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	r.Route("/v1", func(r chi.Router) {
		r.Use(s.auth)
		r.Post("/activities", s.createActivity)
		r.Get("/activities/{activityId}", s.getActivity)
		r.Patch("/activities/{activityId}", s.updateActivity)
		r.Post("/activities/{activityId}/pause", s.activityAction((*sched.Service).Pause))
		r.Post("/activities/{activityId}/resume", s.activityAction((*sched.Service).Resume))
		r.Post("/activities/{activityId}/cancel", s.activityAction((*sched.Service).Cancel))
		r.Get("/occurrences", s.listOccurrences)
		r.Post("/occurrences/{occurrenceId}/actions", s.recordAction)
		r.Post("/occurrences/{occurrenceId}/acknowledge", s.occurrenceAction((*sched.Service).Acknowledge))
		r.Post("/occurrences/{occurrenceId}/miss", s.occurrenceAction((*sched.Service).Miss))
		r.Post("/occurrences/{occurrenceId}/cancel", s.occurrenceAction((*sched.Service).CancelOccurrence))
		r.Post("/subjects/{subjectRef}/erase", s.eraseSubject)
	})
	r.NotFound(func(w http.ResponseWriter, req *http.Request) { problem.Write(w, req, problem.NotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, req, problem.New(http.StatusMethodNotAllowed, "request.invalid", "Method not allowed"))
	})
	return r
}

// base adds the trace ID, security headers, panic recovery and the access log (constant event name; the route
// pattern, never the query string or body).
func (s *Server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		trace := traceID(req)
		ctx := context.WithValue(req.Context(), traceKey{}, trace)
		req = req.WithContext(ctx)
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
		defer func() {
			if p := recover(); p != nil {
				s.Log.Error("http.panic", "traceId", trace)
				if ww.Status() == 0 {
					problem.Write(ww, req, problem.Internal)
				}
			}
			route := chi.RouteContext(req.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			s.Log.Info("http.request", "method", req.Method, "route", route, "status", ww.Status(), "durationMs", time.Since(start).Milliseconds(), "traceId", trace)
		}()
		next.ServeHTTP(ww, req)
	})
}

var traceparentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)

func traceID(r *http.Request) string {
	if m := traceparentRe.FindStringSubmatch(r.Header.Get("traceparent")); m != nil && m[1] != strings.Repeat("0", 32) {
		return m[1]
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// auth checks the bearer service token in constant time. Every configured digest is compared, with no early
// exit, so timing does not reveal which (if any) matched. No token configured means deny.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" || !s.tokenOK(tok) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			problem.Write(w, req, errUnauthenticated)
			return
		}
		next.ServeHTTP(w, req)
	})
}

func (s *Server) tokenOK(tok string) bool {
	sum := sha256.Sum256([]byte(tok))
	match := 0
	for _, h := range s.TokenHashes {
		match |= subtle.ConstantTimeCompare(sum[:], h)
	}
	return match == 1
}

// HashToken returns the SHA-256 digest of a token (the form stored in the environment).
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// ---- JSON shapes (match the OpenAPI schemas) ----

type activityJSON struct {
	ID                 uuid.UUID  `json:"id"`
	SubjectRef         uuid.UUID  `json:"subjectRef"`
	AppRef             string     `json:"appRef"`
	ExternalRef        string     `json:"externalRef"`
	TypeKey            string     `json:"typeKey"`
	RRule              string     `json:"rrule"`
	TimeZone           string     `json:"timeZone"`
	StartsAt           time.Time  `json:"startsAt"`
	EndsAt             *time.Time `json:"endsAt,omitempty"`
	State              string     `json:"state"`
	GeneratedThroughAt time.Time  `json:"generatedThroughAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type occurrenceJSON struct {
	ID             uuid.UUID  `json:"id"`
	ActivityID     uuid.UUID  `json:"activityId"`
	SubjectRef     uuid.UUID  `json:"subjectRef"`
	ExternalRef    string     `json:"externalRef"`
	TypeKey        string     `json:"typeKey"`
	TimeZone       string     `json:"timeZone"`
	DueAt          time.Time  `json:"dueAt"`
	State          string     `json:"state"`
	DispatchedAt   *time.Time `json:"dispatchedAt,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`
	Action         *string    `json:"action,omitempty"`
	ActedAt        *time.Time `json:"actedAt,omitempty"`
	NotifyAt       *time.Time `json:"notifyAt,omitempty"`
	SnoozeCount    int        `json:"snoozeCount"`
}

func toActivity(a *sched.Activity) activityJSON {
	return activityJSON{a.ID, a.SubjectRef, a.AppRef, a.ExternalRef, a.TypeKey, a.RRule, a.TimeZone, a.StartsAt.UTC(), utcPtr(a.EndsAt),
		a.State, a.GeneratedThrough.UTC(), a.CreatedAt.UTC(), a.UpdatedAt.UTC()}
}

func toOccurrence(o *sched.Occurrence) occurrenceJSON {
	return occurrenceJSON{o.ID, o.ActivityID, o.SubjectRef, o.ExternalRef, o.TypeKey, o.TimeZone, o.DueAt.UTC(), o.State, utcPtr(o.DispatchedAt), utcPtr(o.AcknowledgedAt),
		o.Action, utcPtr(o.ActedAt), utcPtr(o.NotifyAt), o.SnoozeCount}
}

type actionBody struct {
	ClientActionID uuid.UUID `json:"clientActionId"`
	SubjectRef     uuid.UUID `json:"subjectRef"`
	Action         string    `json:"action"`
	ActedAt        time.Time `json:"actedAt"`
	SnoozeMinutes  int       `json:"snoozeMinutes"`
}

func (s *Server) recordAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "occurrenceId")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var b actionBody
	if err := decode(w, r, &b); err != nil {
		s.fail(w, r, err)
		return
	}
	o, replayed, err := s.Svc.RecordAction(r.Context(), sched.ActionInput{ClientActionID: b.ClientActionID, SubjectRef: b.SubjectRef, OccurrenceID: id,
		Action: b.Action, ActedAt: b.ActedAt, SnoozeMinutes: b.SnoozeMinutes})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusOK, toOccurrence(o))
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func writeActivity(w http.ResponseWriter, status int, a *sched.Activity) {
	w.Header().Set("ETag", etag(a.Version))
	writeJSON(w, status, toActivity(a))
}

// decode reads one JSON object, refusing unknown fields, trailing data and oversized bodies.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			return problem.TooLarge
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			// The field name is not echoed back (it could be anything the caller sent).
			return problem.BadRequest.WithFields(problem.FieldError{Field: "body", Code: "unknown_field"})
		default:
			return problem.BadRequest.WithFields(problem.FieldError{Field: "body", Code: "invalid"})
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return problem.BadRequest.WithFields(problem.FieldError{Field: "body", Code: "invalid"})
	}
	return nil
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, problem.NotFound // a malformed ID cannot exist
	}
	return id, nil
}

// fail writes err as a problem; unknown errors become a generic 500 and are logged without detail.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *problem.Problem
	if !errors.As(err, &p) {
		s.Log.Error("http.internal_error", "traceId", r.Context().Value(traceKey{}), "reason", err.Error())
	}
	problem.Write(w, r, err)
}

// ---- handlers ----

func (s *Server) createActivity(w http.ResponseWriter, r *http.Request) {
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, errKeyRequired)
		return
	}
	var in sched.CreateInput
	if err := decode(w, r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	a, replayed, err := s.Svc.Create(r.Context(), key, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeActivity(w, http.StatusCreated, a)
}

func (s *Server) getActivity(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "activityId")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.Svc.Get(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeActivity(w, http.StatusOK, a)
}

type updateBody struct {
	TimeZone *string    `json:"timeZone"`
	RRule    *string    `json:"rrule"`
	EndsAt   *time.Time `json:"endsAt"`
}

func (s *Server) updateActivity(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "activityId")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	im := strings.TrimPrefix(strings.TrimSpace(r.Header.Get("If-Match")), "W/")
	if im == "" {
		s.fail(w, r, errPreconditionReq)
		return
	}
	version, err := strconv.Atoi(strings.Trim(im, `"`))
	if err != nil {
		s.fail(w, r, sched.ErrStale) // a tag we never issued is a stale tag
		return
	}
	var b updateBody
	if err := decode(w, r, &b); err != nil {
		s.fail(w, r, err)
		return
	}
	if b.TimeZone == nil && b.RRule == nil && b.EndsAt == nil {
		s.fail(w, r, problem.BadRequest.WithFields(problem.FieldError{Field: "body", Code: "required"}))
		return
	}
	a, err := s.Svc.Update(r.Context(), id, version, sched.UpdateInput{TimeZone: b.TimeZone, RRule: b.RRule, EndsAt: b.EndsAt})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeActivity(w, http.StatusOK, a)
}

func (s *Server) activityAction(fn func(*sched.Service, context.Context, uuid.UUID) (*sched.Activity, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "activityId")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		a, err := fn(s.Svc, r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeActivity(w, http.StatusOK, a)
	}
}

func (s *Server) occurrenceAction(fn func(*sched.Service, context.Context, uuid.UUID) (*sched.Occurrence, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathUUID(r, "occurrenceId")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		o, err := fn(s.Svc, r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toOccurrence(o))
	}
}

var listParams = map[string]bool{"subjectRef": true, "from": true, "to": true, "limit": true, "cursor": true}

func (s *Server) listOccurrences(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bad := func(field, code string) {
		s.fail(w, r, problem.BadRequest.WithFields(problem.FieldError{Field: field, Code: code}))
	}
	for k := range q {
		if !listParams[k] {
			bad("body", "unknown_field")
			return
		}
	}
	subject, err := uuid.Parse(q.Get("subjectRef"))
	if err != nil {
		bad("subjectRef", "invalid")
		return
	}
	p := sched.ListParams{SubjectRef: subject, Limit: 20, Cursor: q.Get("cursor")}
	if len(p.Cursor) > 200 {
		bad("cursor", "too_long")
		return
	}
	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"from", &p.From}, {"to", &p.To}} {
		if v := q.Get(f.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad(f.name, "invalid")
				return
			}
			*f.dst = &t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			bad("limit", "out_of_range")
			return
		}
		p.Limit = n
	}
	items, next, err := s.Svc.List(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := struct {
		Items      []occurrenceJSON `json:"items"`
		NextCursor string           `json:"nextCursor,omitempty"`
	}{Items: make([]occurrenceJSON, 0, len(items)), NextCursor: next}
	for _, o := range items {
		out.Items = append(out.Items, toOccurrence(o))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) eraseSubject(w http.ResponseWriter, r *http.Request) {
	subject, err := uuid.Parse(chi.URLParam(r, "subjectRef"))
	if err != nil {
		s.fail(w, r, problem.BadRequest.WithFields(problem.FieldError{Field: "subjectRef", Code: "invalid"}))
		return
	}
	rep, err := s.Svc.Erase(r.Context(), subject)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Info("subject.erased", "subjectRef", subject, "activities", rep.Activities, "occurrences", rep.Occurrences, "queueJobs", rep.QueueJobs)
	writeJSON(w, http.StatusOK, map[string]int{"subjects": rep.Subjects, "activities": rep.Activities, "occurrences": rep.Occurrences, "queueJobs": rep.QueueJobs})
}
