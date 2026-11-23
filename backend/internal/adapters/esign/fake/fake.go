// Package fake is the e-signature test double (adapter rule A10). The signature is an
// HMAC of the document hash, so any changed byte fails verification. Refused outside local,
// test and dev by the config loader.
package fake

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign"
)

// Signer is the fake.
type Signer struct {
	mu       sync.Mutex
	secret   []byte
	sessions map[string][]byte // session -> hash
	revoked  map[string]bool   // hex(hash)
	// FailStart / FailComplete, when set, are returned (timeout tests).
	FailStart    error
	FailComplete error
	// Hashes records every hash sent, so tests can prove only hashes leave.
	Hashes [][]byte
	Starts int
}

func (s *Signer) init() {
	if s.secret == nil {
		s.secret = make([]byte, 32)
		_, _ = rand.Read(s.secret)
		s.sessions = map[string][]byte{}
		s.revoked = map[string]bool{}
	}
}

func (s *Signer) mac(h []byte) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write(h)
	return m.Sum(nil)
}

// StartSigning implements esign.Signer.
func (s *Signer) StartSigning(_ context.Context, r esign.StartRequest) (esign.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.Starts++
	if s.FailStart != nil {
		return esign.Session{}, s.FailStart
	}
	if len(r.DocumentHash) != sha256.Size || r.SignerRef == "" {
		return esign.Session{}, adapters.E(adapters.Invalid, "BAD_HASH")
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	sid := hex.EncodeToString(id)
	s.sessions[sid] = append([]byte(nil), r.DocumentHash...)
	s.Hashes = append(s.Hashes, append([]byte(nil), r.DocumentHash...))
	return esign.Session{ID: sid}, nil
}

// CompleteSigning implements esign.Signer.
func (s *Signer) CompleteSigning(_ context.Context, sid string) (esign.Signature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if s.FailComplete != nil {
		return esign.Signature{}, s.FailComplete
	}
	h, ok := s.sessions[sid]
	if !ok {
		return esign.Signature{}, adapters.E(adapters.NotFound, "NO_SESSION")
	}
	return esign.Signature{Value: s.mac(h), Certificate: []byte("fake-chain"), SignedAt: time.Now().UTC(), Provider: "fake-esign"}, nil
}

// Verify implements esign.Signer.
func (s *Signer) Verify(_ context.Context, hash []byte, sig esign.Signature) (esign.Verdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if s.revoked[hex.EncodeToString(hash)] {
		return esign.Revoked, nil
	}
	if hmac.Equal(s.mac(hash), sig.Value) {
		return esign.Valid, nil
	}
	return esign.Invalid, nil
}

// Revoke marks a document hash as revoked (tests).
func (s *Signer) Revoke(hash []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.revoked[hex.EncodeToString(hash)] = true
}
