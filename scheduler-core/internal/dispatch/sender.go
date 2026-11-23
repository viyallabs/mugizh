// Package dispatch delivers occurrence.due events to the Core API and runs the queue (River on PostgreSQL).
package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/signing"
)

// Event is the occurrence.due payload. K-ARC-02: references and a coarse type only, no health content.
type Event struct {
	OccurrenceID        uuid.UUID `json:"occurrenceId"`
	ActivityExternalRef string    `json:"activityExternalRef"`
	TypeKey             string    `json:"typeKey"`
	DueAt               time.Time `json:"dueAt"`
	SubjectRef          uuid.UUID `json:"subjectRef"`
	EventID             uuid.UUID `json:"eventId"`
	// NotifyAt is set only on a snoozed re-notification (S-154): when the reminder was asked for again. DueAt
	// stays the occurrence's own due time.
	NotifyAt *time.Time `json:"notifyAt,omitempty"`
}

// eventNS is the namespace for deterministic event IDs: the same occurrence always has the same eventId, so
// a retry after a lost response is recognised by the receiver (idempotent by eventId).
var eventNS = uuid.MustParse("5f1e3a52-8c0e-4a56-9d4b-6f0c2d7a9b11")

// EventID returns the deterministic event ID of an occurrence.
func EventID(occurrence uuid.UUID) uuid.UUID { return uuid.NewSHA1(eventNS, occurrence[:]) }

// EventIDRound is the event ID of a delivery round: round 0 is the first delivery (EventID); every snooze starts
// a new round with its own ID, so the receiver does not mistake the re-notification for a repeat.
func EventIDRound(occurrence uuid.UUID, round int) uuid.UUID {
	if round <= 0 {
		return EventID(occurrence)
	}
	// The round is written in decimal, so no round number can wrap onto another (gosec G115).
	return uuid.NewSHA1(eventNS, append(occurrence[:], []byte("|r"+strconv.Itoa(round))...))
}

// SendError describes a failed delivery. Its text never contains the URL, secret or response body.
type SendError struct {
	Status    int  // 0 when the request did not complete
	Permanent bool // true: do not retry
}

func (e *SendError) Error() string {
	if e.Status == 0 {
		return "callback request failed"
	}
	return "callback returned status " + strconv.Itoa(e.Status)
}

// Sender posts signed events to the callback URL.
type Sender struct {
	URL    string
	Secret []byte // HMAC key; never logged
	Client *http.Client
	Now    func() time.Time
}

// NewSender returns a Sender with a pooled, redirect-free HTTP client.
func NewSender(url string, secret []byte, timeout time.Duration) *Sender {
	tr := &http.Transport{
		MaxIdleConns:        1024,
		MaxIdleConnsPerHost: 1024,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Sender{
		URL:    url,
		Secret: secret,
		Now:    time.Now,
		Client: &http.Client{
			Transport: tr,
			Timeout:   timeout,
			// A redirect would forward a signed body to another place: treat it as a failure instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Send posts the event. Success is any 2xx. 4xx other than 408 and 429 is permanent; everything else retries.
func (s *Sender) Send(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return &SendError{Permanent: true}
	}
	ts := strconv.FormatInt(s.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return &SendError{Permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(signing.HeaderTimestamp, ts)
	req.Header.Set(signing.HeaderSignature, signing.Sign(s.Secret, ts, body))
	req.Header.Set(signing.HeaderEventID, ev.EventID.String())
	req.Header.Set("Idempotency-Key", ev.EventID.String())
	resp, err := s.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &SendError{}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	perm := resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests
	return &SendError{Status: resp.StatusCode, Permanent: perm}
}

// ValidateCallbackURL requires https unless insecure is allowed (local and test environments only).
func ValidateCallbackURL(raw string, allowInsecure bool) error {
	if strings.HasPrefix(raw, "https://") || (allowInsecure && strings.HasPrefix(raw, "http://")) {
		return nil
	}
	return errors.New("dispatch: callback URL must be https")
}
