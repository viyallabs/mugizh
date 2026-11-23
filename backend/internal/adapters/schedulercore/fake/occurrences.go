package fake

import (
	"context"
	"sort"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
)

// The fake follows scheduler-core's action rules (sched/actions.go) so module tests see the same answers:
// first final action wins, idempotent by client action ID, acting-time bounds, one snooze at a time.
const (
	windowBefore = time.Hour
	windowAfter  = 24 * time.Hour
	clockSkew    = 5 * time.Minute
	snoozeStale  = 15 * time.Minute
	maxSnoozes   = 3
)

type seeded struct {
	subject string
	occ     schedulercore.Occurrence
}

type recorded struct {
	occ    string
	action string
	at     time.Time
	mins   int
}

// AddOccurrence seeds an occurrence of a subject (tests).
func (c *Client) AddOccurrence(subject string, o schedulercore.Occurrence) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.occs = append(c.occs, &seeded{subject: subject, occ: o})
}

// Occurrence returns the current state of a seeded occurrence.
func (c *Client) Occurrence(id string) (schedulercore.Occurrence, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.occs {
		if s.occ.ID == id {
			return s.occ, true
		}
	}
	return schedulercore.Occurrence{}, false
}

func (c *Client) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ListOccurrences implements schedulercore.Occurrences.
func (c *Client) ListOccurrences(_ context.Context, r schedulercore.ListOccurrencesRequest) ([]schedulercore.Occurrence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailList != nil {
		return nil, c.FailList
	}
	c.Listed = append(c.Listed, r)
	var out []schedulercore.Occurrence
	for _, s := range c.occs {
		if s.subject == r.SubjectRef && !s.occ.DueAt.Before(r.From) && s.occ.DueAt.Before(r.To) {
			out = append(out, s.occ)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DueAt.Equal(out[j].DueAt) {
			return out[i].DueAt.Before(out[j].DueAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// RecordAction implements schedulercore.Occurrences.
func (c *Client) RecordAction(_ context.Context, r schedulercore.ActionRequest) (schedulercore.ActionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailAction != nil {
		return schedulercore.ActionResult{}, c.FailAction
	}
	c.Actions = append(c.Actions, r)
	var s *seeded
	for _, x := range c.occs {
		if x.occ.ID == r.OccurrenceID && x.subject == r.SubjectRef {
			s = x
		}
	}
	if s == nil {
		return schedulercore.ActionResult{}, adapters.E(adapters.NotFound, "SCHEDULER_NOT_FOUND")
	}
	mins := r.SnoozeMinutes
	switch r.Action {
	case schedulercore.ActionTaken, schedulercore.ActionSkipped:
	case schedulercore.ActionSnoozed:
		if mins == 0 {
			mins = 10
		}
		if mins < 5 || mins > 60 {
			return schedulercore.ActionResult{}, adapters.E(adapters.Invalid, "request.invalid:snoozeMinutes")
		}
	default:
		return schedulercore.ActionResult{}, adapters.E(adapters.Invalid, "request.invalid:action")
	}
	at := r.ActedAt.UTC().Truncate(time.Second)
	if c.done == nil {
		c.done = map[string]recorded{}
	}
	if prev, ok := c.done[r.ClientActionID]; ok {
		if prev.occ != s.occ.ID || prev.action != r.Action || !prev.at.Equal(at) || prev.mins != mins {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonKeyReused)
		}
		return schedulercore.ActionResult{Occurrence: s.occ, Replayed: true}, nil
	}
	now := c.clock().UTC()
	o := &s.occ
	if at.After(now.Add(clockSkew)) || at.Before(o.DueAt.Add(-windowBefore)) || at.After(o.DueAt.Add(windowAfter)) ||
		(r.Action == schedulercore.ActionSnoozed && now.Sub(at) > snoozeStale) {
		return schedulercore.ActionResult{}, adapters.E(adapters.Invalid, schedulercore.ReasonActedAtRange)
	}
	if o.State == "cancelled" {
		return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonInvalidState)
	}
	final := o.Action
	if final == "" && o.State == "acknowledged" {
		final = schedulercore.ActionTaken
	}
	switch {
	case final != "":
		if r.Action == schedulercore.ActionSnoozed {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonInvalidState)
		}
		if r.Action != final {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonActionConflict)
		}
	case r.Action == schedulercore.ActionSnoozed:
		if o.State == "missed" {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonInvalidState)
		}
		if o.State == "pending" && o.NotifyAt != nil && o.NotifyAt.After(now) {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonSnoozeActive)
		}
		if o.SnoozeCount >= maxSnoozes {
			return schedulercore.ActionResult{}, adapters.E(adapters.Rejected, schedulercore.ReasonSnoozeLimit)
		}
		base := now
		if o.DueAt.After(base) {
			base = o.DueAt
		}
		n := base.Add(time.Duration(mins) * time.Minute)
		o.State, o.NotifyAt, o.SnoozeCount = "pending", &n, o.SnoozeCount+1
	default:
		a := at
		o.State, o.Action, o.ActedAt, o.NotifyAt = "acknowledged", r.Action, &a, nil
	}
	c.done[r.ClientActionID] = recorded{occ: s.occ.ID, action: r.Action, at: at, mins: mins}
	return schedulercore.ActionResult{Occurrence: *o}, nil
}

// SetState sets the scheduler state of a seeded occurrence (tests: dispatched, missed, cancelled ...).
func (c *Client) SetState(id, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.occs {
		if s.occ.ID == id {
			s.occ.State = state
		}
	}
}
