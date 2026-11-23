// Package fake is the push test double (adapter rule A10). It validates like a real
// adapter and keeps sent messages in memory. Refused outside local, test and dev.
package fake

import (
	"context"
	"sync"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Sender is the fake.
type Sender struct {
	mu            sync.Mutex
	sent          []push.Message
	seen          map[string]string
	FailWith      error           // when set, Send returns it (fault injection)
	InvalidTokens map[string]bool // tokens answered with TOKEN_INVALID
}

// Send implements push.Sender.
func (s *Sender) Send(_ context.Context, m push.Message) (string, error) {
	if err := push.Validate(m); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.InvalidTokens[m.Target.Token] {
		return "", adapters.E(adapters.NotFound, push.ReasonTokenInvalid)
	}
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
func (s *Sender) Sent() []push.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]push.Message(nil), s.sent...)
}

var _ push.Sender = (*Sender)(nil)
