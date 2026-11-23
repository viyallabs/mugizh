package blobstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// WormPrefixes are the key prefixes of write-once evidence: crypto-shredding tombstones,
// the erasure journal and audit anchors. They live in the WORM store (a bucket with Object
// Lock in staging and production). Adding a write-once prefix to the code means adding it
// here; TestEveryBlobPrefixIsRouted fails otherwise.
var WormPrefixes = []string{"key-destruction/", "erasure-journal/", "audit-anchors/"}

// ErrWormDelete is returned when something tries to delete write-once evidence.
var ErrWormDelete = errors.New("blobstore: write-once evidence is never deleted")

// ErrAmbiguousPrefix is returned by List when a prefix could span the WORM and data stores.
var ErrAmbiguousPrefix = errors.New("blobstore: list prefix spans write-once and data storage")

// Split routes keys under WormPrefixes to Worm and every other key to Data.
type Split struct {
	Worm Store
	Data Store
}

func isWorm(key string) bool {
	for _, p := range WormPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func (s Split) route(key string) (Store, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	if isWorm(key) {
		return s.Worm, nil
	}
	return s.Data, nil
}

// PutWriteOnce implements Store.
func (s Split) PutWriteOnce(ctx context.Context, key string, data []byte) error {
	st, err := s.route(key)
	if err != nil {
		return err
	}
	return st.PutWriteOnce(ctx, key, data)
}

// Get implements Store.
func (s Split) Get(ctx context.Context, key string) ([]byte, error) {
	st, err := s.route(key)
	if err != nil {
		return nil, err
	}
	return st.Get(ctx, key)
}

// List implements Store. A prefix must lie wholly inside one store: a prefix that is a
// proper prefix of a WORM prefix (including the empty prefix) is refused.
func (s Split) List(ctx context.Context, prefix string) ([]string, error) {
	if isWorm(prefix) {
		return s.Worm.List(ctx, prefix)
	}
	for _, p := range WormPrefixes {
		if strings.HasPrefix(p, prefix) {
			return nil, fmt.Errorf("%w: %q", ErrAmbiguousPrefix, prefix)
		}
	}
	return s.Data.List(ctx, prefix)
}

// Delete removes an object from the data store. Write-once keys are refused. The data
// store must itself support Delete.
func (s Split) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if isWorm(key) {
		return ErrWormDelete
	}
	d, ok := s.Data.(interface {
		Delete(ctx context.Context, key string) error
	})
	if !ok {
		return errors.New("blobstore: data store cannot delete")
	}
	return d.Delete(ctx, key)
}
