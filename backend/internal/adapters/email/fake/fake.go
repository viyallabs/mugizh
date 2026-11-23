// Package fake is the email test double (adapter rule A10). Refused outside local, test and dev.
package fake

import (
	"context"
	"sync"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/email"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Sender is the fake.
type Sender struct {
	mu       sync.Mutex
	sent     []email.Message
	seen     map[string]string
	FailWith error
}

// Send implements email.Sender.
func (s *Sender) Send(_ context.Context, m email.Message) (string, error) {
	if err := email.Validate(m); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWith != nil {
		return "", s.FailWith
	}
	if s.seen == nil {
		s.seen = map[string]string{}
	}
	if id, ok := s.seen[m.IdempotencyKey]; ok {
		return id, nil
	}
	id := ids.New()
	s.seen[m.IdempotencyKey] = id
	s.sent = append(s.sent, m)
	return id, nil
}

// Sent returns a copy of every message sent.
func (s *Sender) Sent() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]email.Message(nil), s.sent...)
}

var _ email.Sender = (*Sender)(nil)
