// Package problem writes RFC 9457 Problem Details (API standards §4). The `code` is the
// stable machine-readable field apps branch on. `detail` must never carry health data,
// other people's data or internal details (K-DP-04).
package problem

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// FieldError names one invalid input field.
type FieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Problem is the RFC 9457 body plus our extension members.
type Problem struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Code     string       `json:"code"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	TraceID  string       `json:"traceId,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`

	// RetryAfter, when set, is sent as the Retry-After header (seconds). Not serialised.
	RetryAfter int `json:"-"`
}

// Error lets a *Problem travel as an error through handlers.
func (p *Problem) Error() string { return p.Code }

// New builds a problem. code is `<module>.<reason>`; title is a fixed English summary
// for developers (apps translate from code, K-GLB-02).
func New(status int, code, title string) *Problem {
	return &Problem{Type: "/problems/" + strings.ReplaceAll(code, ".", "/"), Title: title, Status: status, Code: code}
}

// WithDetail returns a copy with a safe detail string.
func (p *Problem) WithDetail(d string) *Problem { c := *p; c.Detail = d; return &c }

// WithFields returns a copy listing invalid fields.
func (p *Problem) WithFields(f ...FieldError) *Problem {
	c := *p
	c.Errors = append(append([]FieldError(nil), p.Errors...), f...)
	return &c
}

// Common problems used across modules.
var (
	BadRequest      = New(http.StatusBadRequest, "request.invalid", "The request is not valid")
	Unauthenticated = New(http.StatusUnauthorized, "auth.unauthenticated", "Sign-in required")
	Forbidden       = New(http.StatusForbidden, "auth.forbidden", "Not allowed")
	ConsentRequired = New(http.StatusForbidden, "consent.required", "No active consent or permission")
	MFARequired     = New(http.StatusForbidden, "auth.mfa_required", "Multi-factor authentication required")
	StepUpRequired  = New(http.StatusForbidden, "auth.step_up_required", "Re-authentication required")
	Restricted      = New(http.StatusForbidden, "auth.reverification_required", "Re-verification required on this device")
	NotFound        = New(http.StatusNotFound, "resource.not_found", "Not found")
	Conflict        = New(http.StatusConflict, "resource.conflict", "Conflict")
	PreconditionErr = New(http.StatusPreconditionFailed, "resource.precondition_failed", "The resource changed")
	TooLarge        = New(http.StatusRequestEntityTooLarge, "request.too_large", "Request body too large")
	RateLimited     = New(http.StatusTooManyRequests, "request.rate_limited", "Too many requests")
	Internal        = New(http.StatusInternalServerError, "server.error", "Something went wrong")
	Unavailable     = New(http.StatusServiceUnavailable, "server.unavailable", "Temporarily unavailable")
)

// TraceIDFunc extracts the trace ID from a request; set by the httpx package.
var TraceIDFunc = func(*http.Request) string { return "" }

// Write sends p as application/problem+json. Unknown errors become a generic 500 so no
// internal detail leaks.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	var p *Problem
	if !errors.As(err, &p) {
		p = Internal
	}
	out := *p
	if r != nil {
		out.Instance = r.URL.Path
		out.TraceID = TraceIDFunc(r)
	}
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "no-store")
	if out.RetryAfter > 0 {
		h.Set("Retry-After", itoa(out.RetryAfter))
	}
	w.WriteHeader(out.Status)
	_ = json.NewEncoder(w).Encode(out)
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
