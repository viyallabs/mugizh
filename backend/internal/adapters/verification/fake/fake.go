// Package fake is the verification adapter test double (adapter rule A10). It behaves like
// a register: entries are keyed by council and number, and failures can be injected. It is
// refused outside local, test and dev by the config loader.
package fake

import (
	"context"
	"strings"
	"sync"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification"
)

// Checker is the fake.
type Checker struct {
	mu      sync.Mutex
	entries map[string]string // council|number -> registered name
	// FailWith, when set, is returned by every call (vendor down, timeout).
	FailWith error
	// ForceOutcome, when set, replaces the computed outcome (for example Inconclusive).
	ForceOutcome verification.Outcome
	// Block, when set, makes calls wait until ctx ends (timeout tests).
	Block bool
	calls int
}

func key(council, number string) string {
	return strings.ToUpper(strings.TrimSpace(council)) + "|" + strings.ToUpper(strings.TrimSpace(number))
}

// Add registers a professional in the fake register.
func (c *Checker) Add(council, number, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]string{}
	}
	c.entries[key(council, number)] = name
}

// Calls returns how many registry lookups were made.
func (c *Checker) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// CheckRegistration implements verification.Checker.
func (c *Checker) CheckRegistration(ctx context.Context, q verification.RegistrationQuery) (verification.RegistrationResult, error) {
	c.mu.Lock()
	c.calls++
	fail, force, block := c.FailWith, c.ForceOutcome, c.Block
	name, found := c.entries[key(q.Council, q.Number)]
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return verification.RegistrationResult{}, adapters.E(adapters.Timeout, "FAKE_BLOCKED")
	}
	if fail != nil {
		return verification.RegistrationResult{}, fail
	}
	if force != "" {
		return verification.RegistrationResult{Outcome: force}, nil
	}
	if !found {
		return verification.RegistrationResult{Outcome: verification.NotFound}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(q.Name)) {
		return verification.RegistrationResult{Outcome: verification.NoMatch, RegisteredName: name}, nil
	}
	return verification.RegistrationResult{Outcome: verification.Match, RegisteredName: name}, nil
}

// CheckDocument implements verification.Checker. The fake returns no extracted fields and
// a neutral score; real extraction is vendor work (S-111, deferred).
func (c *Checker) CheckDocument(ctx context.Context, _ verification.DocumentQuery) (verification.DocumentResult, error) {
	c.mu.Lock()
	fail := c.FailWith
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return verification.DocumentResult{}, adapters.E(adapters.Timeout, "FAKE_CONTEXT")
	}
	if fail != nil {
		return verification.DocumentResult{}, fail
	}
	return verification.DocumentResult{Fields: map[string]string{}, Authenticity: 0.5}, nil
}

var _ verification.Checker = (*Checker)(nil)
