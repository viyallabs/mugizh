// Package fake is the SMS test double (adapter rule A10). It validates every message
// against the registry, like a real adapter, and keeps sent messages in memory for tests.
// It is refused outside local, test and dev by the config loader.
package fake

import (
	"context"
	"sync"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Sender is the fake.
type Sender struct {
	Registry *sms.Registry
	mu       sync.Mutex
	sent     []sms.Message
	FailWith error // when set, Send returns it (fault injection)
	seen     map[string]string
}

// Send implements sms.Sender.
func (s *Sender) Send(_ context.Context, m sms.Message) (string, error) {
	if err := s.Registry.Validate(m); err != nil {
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
	if m.IdempotencyKey != "" {
		if id, ok := s.seen[m.IdempotencyKey]; ok {
			return id, nil // same key: no second message
		}
	}
	id := ids.New()
	if m.IdempotencyKey != "" {
		s.seen[m.IdempotencyKey] = id
	}
	s.sent = append(s.sent, m)
	return id, nil
}

// Sent returns a copy of the messages sent to `to`.
func (s *Sender) Sent(to string) []sms.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sms.Message
	for _, m := range s.sent {
		if m.To == to {
			out = append(out, m)
		}
	}
	return out
}

// Count returns how many messages were sent.
func (s *Sender) Count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) }

var _ sms.Sender = (*Sender)(nil)
