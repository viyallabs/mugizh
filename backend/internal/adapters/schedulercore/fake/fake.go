// Package fake is the in-memory scheduler-core client (adapter rule A10). It replays by
// idempotency key like the real service.
package fake

import (
	"context"
	"sync"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Client is the fake.
type Client struct {
	mu     sync.Mutex
	byKey  map[string]schedulercore.Activity
	Items  []schedulercore.CreateRequest // every distinct activity created
	State  map[string]string             // activity id -> state
	Cancel []string
	// Erased lists subjects whose activities EraseSubject removed.
	Erased []string
	// FailCreate, when set, is returned by CreateActivity.
	FailCreate error
	// FailErase, when set, is returned by EraseSubject (erasure fail-closed tests).
	FailErase error
	// Occurrences and actions (S-153, S-154): see occurrences.go.
	Now        func() time.Time
	FailList   error
	FailAction error
	Listed     []schedulercore.ListOccurrencesRequest
	Actions    []schedulercore.ActionRequest
	occs       []*seeded
	done       map[string]recorded
}

// CreateActivity implements schedulercore.Client.
func (c *Client) CreateActivity(_ context.Context, r schedulercore.CreateRequest) (schedulercore.Activity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailCreate != nil {
		return schedulercore.Activity{}, c.FailCreate
	}
	if c.byKey == nil {
		c.byKey, c.State = map[string]schedulercore.Activity{}, map[string]string{}
	}
	if a, ok := c.byKey[r.IdempotencyKey]; ok {
		return a, nil
	}
	a := schedulercore.Activity{ID: ids.New(), ExternalRef: r.ExternalRef, State: "active"}
	c.byKey[r.IdempotencyKey] = a
	c.State[a.ID] = "active"
	c.Items = append(c.Items, r)
	return a, nil
}

// CancelActivity implements schedulercore.Client.
func (c *Client) CancelActivity(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.State[id]; !ok {
		return adapters.E(adapters.NotFound, "NO_ACTIVITY")
	}
	c.State[id] = "cancelled"
	c.Cancel = append(c.Cancel, id)
	return nil
}

// Active returns the activities not cancelled.
func (c *Client) Active() []schedulercore.CreateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []schedulercore.CreateRequest
	for _, r := range c.Items {
		if a := c.byKey[r.IdempotencyKey]; c.State[a.ID] == "active" {
			out = append(out, r)
		}
	}
	return out
}

// EraseSubject implements the subject-erase call: every activity of the subject is removed.
func (c *Client) EraseSubject(_ context.Context, subjectRef string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.FailErase != nil {
		return c.FailErase
	}
	kept := c.Items[:0]
	for _, it := range c.Items {
		if it.SubjectRef == subjectRef {
			c.Erased = append(c.Erased, subjectRef)
			continue
		}
		kept = append(kept, it)
	}
	c.Items = kept
	return nil
}
