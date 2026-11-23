package schedulercore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
)

// Patient actions on an occurrence (S-154; scheduler-core POST /v1/occurrences/{id}/actions).
const (
	ActionTaken   = "taken"
	ActionSkipped = "skipped"
	ActionSnoozed = "snoozed"
)

// Reason codes of a Rejected or Invalid error from RecordAction: scheduler-core's own problem codes.
const (
	ReasonActionConflict = "occurrence.action_conflict"
	ReasonSnoozeActive   = "occurrence.snooze_active"
	ReasonSnoozeLimit    = "occurrence.snooze_limit"
	ReasonInvalidState   = "occurrence.invalid_state"
	ReasonKeyReused      = "idempotency.key_reused"
	// ReasonActedAtRange is an Invalid error: the acting time is outside the accepted bounds.
	ReasonActedAtRange = "request.invalid:actedAt"
)

// MaxOccurrencePages bounds ListOccurrences (100 per page).
const MaxOccurrencePages = 30

// Occurrence is one due instant of an activity, as scheduler-core reports it (references and state only).
type Occurrence struct {
	ID          string
	ActivityID  string
	ExternalRef string
	TypeKey     TypeKey
	TimeZone    string // IANA zone of the activity
	DueAt       time.Time
	// State is pending, dispatched, acknowledged, missed, cancelled or failed.
	State string
	// Action is "taken" or "skipped" once the patient acted ("" before).
	Action      string
	ActedAt     *time.Time
	NotifyAt    *time.Time // when a snoozed occurrence is notified again
	SnoozeCount int
}

// ListOccurrencesRequest selects one subject's occurrences with From <= dueAt < To.
type ListOccurrencesRequest struct {
	SubjectRef string
	From, To   time.Time
}

// ActionRequest records the subject's action. ClientActionID (a UUID) makes it idempotent.
type ActionRequest struct {
	OccurrenceID   string
	SubjectRef     string // must own the occurrence, otherwise NotFound
	ClientActionID string
	Action         string
	ActedAt        time.Time
	SnoozeMinutes  int // snoozed only; 0 takes scheduler-core's default
}

// ActionResult is the occurrence after the action.
type ActionResult struct {
	Occurrence Occurrence
	Replayed   bool
}

// Occurrences is the port for reading and acting on a subject's occurrences. Errors: NotFound (not the
// subject's, or unknown), Rejected (Reason is one of the Reason* codes), Invalid (bad content or acting time),
// Unavailable and Timeout.
type Occurrences interface {
	ListOccurrences(ctx context.Context, r ListOccurrencesRequest) ([]Occurrence, error)
	RecordAction(ctx context.Context, r ActionRequest) (ActionResult, error)
}

var reProblemCode = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)
var reField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)

// problemReason returns scheduler-core's problem code (and the first invalid field, when it names one) from an
// error body, or def when the body is not a problem.
func problemReason(body []byte, def string) string {
	var p struct {
		Code   string `json:"code"`
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &p) != nil || !reProblemCode.MatchString(p.Code) {
		return def
	}
	if len(p.Errors) > 0 && reField.MatchString(p.Errors[0].Field) {
		return p.Code + ":" + p.Errors[0].Field
	}
	return p.Code
}

type occurrenceJSON struct {
	ID          string     `json:"id"`
	ActivityID  string     `json:"activityId"`
	ExternalRef string     `json:"externalRef"`
	TypeKey     TypeKey    `json:"typeKey"`
	TimeZone    string     `json:"timeZone"`
	DueAt       time.Time  `json:"dueAt"`
	State       string     `json:"state"`
	Action      string     `json:"action"`
	ActedAt     *time.Time `json:"actedAt"`
	NotifyAt    *time.Time `json:"notifyAt"`
	SnoozeCount int        `json:"snoozeCount"`
}

func (o occurrenceJSON) toPort() Occurrence {
	return Occurrence{ID: o.ID, ActivityID: o.ActivityID, ExternalRef: o.ExternalRef, TypeKey: o.TypeKey, TimeZone: o.TimeZone, DueAt: o.DueAt.UTC(),
		State: o.State, Action: o.Action, ActedAt: o.ActedAt, NotifyAt: o.NotifyAt, SnoozeCount: o.SnoozeCount}
}

// ListOccurrences implements Occurrences. It follows the cursor to the end of the range, up to MaxOccurrencePages
// pages; a longer range is an Invalid error (the caller bounds its ranges).
func (c *HTTP) ListOccurrences(ctx context.Context, r ListOccurrencesRequest) ([]Occurrence, error) {
	var out []Occurrence
	cursor := ""
	for page := 0; page < MaxOccurrencePages; page++ {
		q := url.Values{"subjectRef": {r.SubjectRef}, "from": {r.From.UTC().Format(time.RFC3339)}, "to": {r.To.UTC().Format(time.RFC3339)}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			Items      []occurrenceJSON `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/occurrences?"+q.Encode(), "", nil, &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			out = append(out, it.toPort())
		}
		if resp.NextCursor == "" {
			return out, nil
		}
		cursor = resp.NextCursor
	}
	return nil, adapters.E(adapters.Invalid, "TOO_MANY_OCCURRENCES")
}

type actionBody struct {
	ClientActionID string    `json:"clientActionId"`
	SubjectRef     string    `json:"subjectRef"`
	Action         string    `json:"action"`
	ActedAt        time.Time `json:"actedAt"`
	SnoozeMinutes  *int      `json:"snoozeMinutes,omitempty"`
}

// RecordAction implements Occurrences.
func (c *HTTP) RecordAction(ctx context.Context, r ActionRequest) (ActionResult, error) {
	b := actionBody{ClientActionID: r.ClientActionID, SubjectRef: r.SubjectRef, Action: r.Action, ActedAt: r.ActedAt.UTC()}
	if r.SnoozeMinutes != 0 {
		n := r.SnoozeMinutes
		b.SnoozeMinutes = &n
	}
	var o occurrenceJSON
	hdr := http.Header{}
	if err := c.doHeaders(ctx, http.MethodPost, "/v1/occurrences/"+url.PathEscape(r.OccurrenceID)+"/actions", "", b, &o, hdr); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Occurrence: o.toPort(), Replayed: hdr.Get("Idempotent-Replayed") == strconv.FormatBool(true)}, nil
}
