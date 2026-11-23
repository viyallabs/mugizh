// Package blobstore is the thin wrapper over object storage (adapter-interfaces §14).
// Module code depends on this interface; the S3-compatible implementation lives under
// internal/adapters (dual-cloud rule: S3-compatible storage, no cloud SDK in app code).
package blobstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrExists is returned by PutWriteOnce when the key already exists.
var ErrExists = errors.New("blobstore: object already exists")

// ErrNotFound is returned when the key does not exist.
var ErrNotFound = errors.New("blobstore: not found")

// Store is object storage. Content is already encrypted by the caller where the data is
// sensitive (encryption-design §8); the store never sees plaintext health files.
type Store interface {
	// PutWriteOnce stores data under key and fails with ErrExists if the key is taken.
	// Production uses a bucket with object lock (write-once, audit anchors).
	PutWriteOnce(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns the keys that start with prefix, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
}

var reKey = regexp.MustCompile(`^[a-z0-9][a-z0-9/_\-.]{0,200}$`)

// LocalDir is a filesystem store for local development and tests only.
type LocalDir struct{ Root string }

func (l LocalDir) path(key string) (string, error) {
	// reKey forbids a leading dot or slash; Clean rejects "..", "./" and "//" segments.
	if !reKey.MatchString(key) || filepath.ToSlash(filepath.Clean(key)) != key {
		return "", fmt.Errorf("blobstore: invalid key %q", key)
	}
	return filepath.Join(l.Root, filepath.FromSlash(key)), nil
}

// PutWriteOnce implements Store.
func (l LocalDir) PutWriteOnce(_ context.Context, key string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Get implements Store.
func (l LocalDir) Get(_ context.Context, key string) ([]byte, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p) // #nosec G304 -- key validated against reKey above
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// List implements Store.
func (l LocalDir) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(l.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(l.Root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Delete removes an object (used when evidence must be hard-deleted, e.g. a prohibited
// identity document, AAD-04). Missing objects are not an error. The S3-compatible
// adapter must implement the same (objects under retention lock are not deletable by
// design; evidence buckets must not use object lock).
func (l LocalDir) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
