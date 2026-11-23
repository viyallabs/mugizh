// Package keys is the `keys` interface (ADR-31 dual-cloud rule): it wraps and unwraps
// data keys with a root key that never leaves the key management service.
//
// Implementations: Local (this file; local, test and dev only, refused in staging and
// production) and cloud KMS adapters under internal/adapters/keys/<cloud>/ (AWS KMS for
// staging, production and DR; OCI Vault for dev). Application code never imports a cloud
// SDK (encryption-design §10.1).
package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

// ErrUnavailable means the key service could not be reached or refused the call.
// Callers fail closed: no plaintext fallback (encryption-design §7, E-DP-06).
var ErrUnavailable = errors.New("keys: key service unavailable")

// ErrRefused means this provider may not be used in the current environment.
var ErrRefused = errors.New("keys: provider refused in this environment")

// Provider wraps data keys under a root key.
type Provider interface {
	Name() string
	// CurrentRoot identifies the root key (and version) new wraps use.
	CurrentRoot() string
	// Wrap encrypts a data key. aad binds the wrapped key to its key-store row.
	Wrap(ctx context.Context, dataKey, aad []byte) (wrapped []byte, root string, err error)
	// Unwrap decrypts a wrapped data key produced by Wrap with the same aad.
	Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error)
}

// Local is a provider with root keys held in process memory. It exists so developers and
// tests can run without a cloud account. It refuses to run in staging or production.
type Local struct {
	mu      sync.RWMutex
	roots   map[uint32]cipher.AEAD
	current uint32
	down    bool
}

// NewLocal returns a Local provider for env, with one root key (version 1).
func NewLocal(env string, root []byte) (*Local, error) {
	switch env {
	case "local", "test", "dev":
	default:
		return nil, fmt.Errorf("%w: local key provider in %q", ErrRefused, env)
	}
	l := &Local{roots: map[uint32]cipher.AEAD{}}
	if err := l.AddRoot(1, root); err != nil {
		return nil, err
	}
	return l, nil
}

// AddRoot adds a root key version and makes it current (simulates yearly KMS rotation).
func (l *Local) AddRoot(version uint32, root []byte) error {
	if len(root) != 32 {
		return errors.New("keys: root key must be 32 bytes")
	}
	b, err := aes.NewCipher(root)
	if err != nil {
		return err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roots[version] = g
	l.current = version
	return nil
}

// SetUnavailable simulates a KMS outage (tests for E-DP-06).
func (l *Local) SetUnavailable(down bool) { l.mu.Lock(); l.down = down; l.mu.Unlock() }

// Name implements Provider.
func (l *Local) Name() string { return "local" }

// CurrentRoot implements Provider.
func (l *Local) CurrentRoot() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return fmt.Sprintf("local:%d", l.current)
}

// Wrap implements Provider. Output: version(4) | nonce(12) | ciphertext+tag.
func (l *Local) Wrap(_ context.Context, dataKey, aad []byte) ([]byte, string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.down {
		return nil, "", ErrUnavailable
	}
	g := l.roots[l.current]
	out := make([]byte, 4, 4+12+len(dataKey)+16)
	binary.BigEndian.PutUint32(out, l.current)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	out = append(out, nonce...)
	out = g.Seal(out, nonce, dataKey, aad)
	return out, fmt.Sprintf("local:%d", l.current), nil
}

// Unwrap implements Provider.
func (l *Local) Unwrap(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.down {
		return nil, ErrUnavailable
	}
	if len(wrapped) < 4+12+16 {
		return nil, errors.New("keys: wrapped key too short")
	}
	g, ok := l.roots[binary.BigEndian.Uint32(wrapped)]
	if !ok {
		return nil, fmt.Errorf("%w: unknown root version", ErrUnavailable)
	}
	pt, err := g.Open(nil, wrapped[4:16], wrapped[16:], aad)
	if err != nil {
		return nil, errors.New("keys: unwrap failed (wrong key or tampered)")
	}
	return pt, nil
}
