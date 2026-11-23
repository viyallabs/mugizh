// Package schedulercore is the client port for scheduler-core's HTTP API
// (api/scheduler/scheduler-core.v1.yaml). It sends opaque references, RRULEs, IANA zones
// and coarse type keys only; never a drug name or any health text (K-ARC-02, TC-61).
// The service credential is a bearer token supplied by the composition root (K-SEC-05).
package schedulercore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
)

// TypeKey is the coarse activity type.
type TypeKey string

// Type keys defined by the scheduler-core API.
const (
	MedDose       TypeKey = "med.dose"
	InjectionDose TypeKey = "injection.dose"
	TestDue       TypeKey = "test.due"
	Appointment   TypeKey = "appointment"
	Custom        TypeKey = "custom"
)

// CreateRequest creates one activity. IdempotencyKey is a UUID.
type CreateRequest struct {
	IdempotencyKey string
	SubjectRef     string
	AppRef         string
	ExternalRef    string
	TypeKey        TypeKey
	RRule          string
	TimeZone       string
	StartsAt       time.Time
	EndsAt         *time.Time
}

// Activity is the created activity (references only).
type Activity struct {
	ID          string
	ExternalRef string
	State       string
}

// Client is the port.
type Client interface {
	CreateActivity(ctx context.Context, r CreateRequest) (Activity, error)
	CancelActivity(ctx context.Context, activityID string) error
}

// HTTP is the real client.
type HTTP struct {
	BaseURL string
	Token   string
	Doer    *http.Client
}

// NewHTTP builds the client with a 10 s timeout.
func NewHTTP(baseURL, token string) (*HTTP, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || token == "" {
		return nil, errors.New("schedulercore: a base URL and a service token are required")
	}
	return &HTTP{BaseURL: baseURL, Token: token, Doer: &http.Client{Timeout: 10 * time.Second}}, nil
}

type createBody struct {
	SubjectRef  string     `json:"subjectRef"`
	AppRef      string     `json:"appRef"`
	ExternalRef string     `json:"externalRef"`
	TypeKey     TypeKey    `json:"typeKey"`
	RRule       string     `json:"rrule"`
	TimeZone    string     `json:"timeZone"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      *time.Time `json:"endsAt,omitempty"`
}

type activityJSON struct {
	ID          string `json:"id"`
	ExternalRef string `json:"externalRef"`
	State       string `json:"state"`
}

func (c *HTTP) do(ctx context.Context, method, path, idem string, body any, out any) error {
	return c.doHeaders(ctx, method, path, idem, body, out, nil)
}

// doHeaders is do that also copies the response headers into respHdr (when not nil).
func (c *HTTP) doHeaders(ctx context.Context, method, path, idem string, body any, out any, respHdr http.Header) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return adapters.E(adapters.Invalid, "ENCODE")
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return adapters.E(adapters.Invalid, "REQUEST")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	doer := c.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return adapters.E(adapters.Timeout, "SCHEDULER_TIMEOUT")
		}
		return adapters.E(adapters.Unavailable, "SCHEDULER_UNREACHABLE")
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if respHdr != nil {
			respHdr[k] = v
		}
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return adapters.E(adapters.Invalid, "DECODE")
			}
		}
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return adapters.E(adapters.AuthFailed, "SCHEDULER_AUTH")
	case resp.StatusCode == http.StatusNotFound:
		return adapters.E(adapters.NotFound, "SCHEDULER_NOT_FOUND")
	case resp.StatusCode == http.StatusConflict:
		return adapters.E(adapters.Rejected, problemReason(data, "SCHEDULER_CONFLICT"))
	case resp.StatusCode >= 500:
		return adapters.E(adapters.Unavailable, "SCHEDULER_ERROR")
	}
	return adapters.E(adapters.Invalid, problemReason(data, "SCHEDULER_REJECTED"))
}

// CreateActivity implements Client.
func (c *HTTP) CreateActivity(ctx context.Context, r CreateRequest) (Activity, error) {
	var a activityJSON
	var ends *time.Time
	if r.EndsAt != nil {
		u := r.EndsAt.UTC()
		ends = &u
	}
	err := c.do(ctx, http.MethodPost, "/v1/activities", r.IdempotencyKey, createBody{SubjectRef: r.SubjectRef, AppRef: r.AppRef,
		ExternalRef: r.ExternalRef, TypeKey: r.TypeKey, RRule: r.RRule, TimeZone: r.TimeZone, StartsAt: r.StartsAt.UTC(), EndsAt: ends}, &a)
	if err != nil {
		return Activity{}, err
	}
	return Activity{ID: a.ID, ExternalRef: a.ExternalRef, State: a.State}, nil
}

// CancelActivity implements Client. Cancelling a cancelled activity succeeds.
func (c *HTTP) CancelActivity(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/activities/"+url.PathEscape(id)+"/cancel", "", nil, nil)
}

// EraseSubject deletes everything scheduler-core holds for a subject (K-DP-07; scheduler-core
// POST /v1/subjects/{subjectRef}/erase). Erasing an unknown subject succeeds.
func (c *HTTP) EraseSubject(ctx context.Context, subjectRef string) error {
	return c.do(ctx, http.MethodPost, "/v1/subjects/"+url.PathEscape(subjectRef)+"/erase", "erase:"+subjectRef, nil, nil)
}
