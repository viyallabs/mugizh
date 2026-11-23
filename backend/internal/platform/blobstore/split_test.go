package blobstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newSplit(t *testing.T) (Split, LocalDir, LocalDir) {
	t.Helper()
	w, d := LocalDir{Root: t.TempDir()}, LocalDir{Root: t.TempDir()}
	return Split{Worm: w, Data: d}, w, d
}

func TestSplitRoutesByPrefix(t *testing.T) {
	ctx := context.Background()
	s, w, d := newSplit(t)
	for _, k := range []string{"key-destruction/2026/a.json", "erasure-journal/r1/requested.json", "audit-anchors/2026-01-01.json"} {
		if err := s.PutWriteOnce(ctx, k, []byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Get(ctx, k); err != nil {
			t.Errorf("%s not in WORM store", k)
		}
		if _, err := d.Get(ctx, k); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s leaked into data store", k)
		}
	}
	for _, k := range []string{"documents/u/1.bin", "exports/u/1.bin", "verification/c/1"} {
		if err := s.PutWriteOnce(ctx, k, []byte("y")); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Get(ctx, k); err != nil {
			t.Errorf("%s not in data store", k)
		}
		if _, err := w.Get(ctx, k); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s leaked into WORM store", k)
		}
		if b, err := s.Get(ctx, k); err != nil || string(b) != "y" {
			t.Errorf("get %s: %v", k, err)
		}
	}
	if err := s.PutWriteOnce(ctx, "documents/u/1.bin", nil); !errors.Is(err, ErrExists) {
		t.Errorf("write-once not preserved: %v", err)
	}
}

func TestSplitDelete(t *testing.T) {
	ctx := context.Background()
	s, _, d := newSplit(t)
	_ = s.PutWriteOnce(ctx, "key-destruction/a.json", []byte("x"))
	if err := s.Delete(ctx, "key-destruction/a.json"); !errors.Is(err, ErrWormDelete) {
		t.Fatalf("delete on WORM: %v", err)
	}
	if _, err := s.Get(ctx, "key-destruction/a.json"); err != nil {
		t.Fatal("WORM object was affected")
	}
	_ = s.PutWriteOnce(ctx, "documents/u/1.bin", []byte("x"))
	if err := s.Delete(ctx, "documents/u/1.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(ctx, "documents/u/1.bin"); !errors.Is(err, ErrNotFound) {
		t.Fatal("not deleted")
	}
	if err := s.Delete(ctx, "../x"); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestSplitList(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSplit(t)
	_ = s.PutWriteOnce(ctx, "erasure-journal/r1/a.json", nil)
	_ = s.PutWriteOnce(ctx, "documents/u/1.bin", nil)
	if k, err := s.List(ctx, "erasure-journal/"); err != nil || len(k) != 1 {
		t.Fatalf("worm list: %v %v", k, err)
	}
	if k, err := s.List(ctx, "erasure-journal/r1/"); err != nil || len(k) != 1 {
		t.Fatalf("worm sub-list: %v %v", k, err)
	}
	if k, err := s.List(ctx, "documents/"); err != nil || len(k) != 1 {
		t.Fatalf("data list: %v %v", k, err)
	}
	for _, p := range []string{"", "e", "k", "a", "erasure-"} {
		if _, err := s.List(ctx, p); !errors.Is(err, ErrAmbiguousPrefix) {
			t.Errorf("prefix %q: want ErrAmbiguousPrefix, got %v", p, err)
		}
	}
}

// knownDataPrefixes are the first path segments of keys that go to the data store.
var knownDataPrefixes = []string{"documents/", "exports/", "verification/"}

var reBlobLiteral = regexp.MustCompile(`"([a-z][a-z0-9_-]*/)`)

// reImportLine matches an import spec; mimeTypes are media-type literals, not blob prefixes.
var reImportLine = regexp.MustCompile(`^\s*([A-Za-z_]\w*\s+)?"[^"]*"\s*$`)

var mimeTypes = map[string]bool{"image/": true, "application/": true, "text/": true}

// TestEveryBlobPrefixIsRouted fails when code that writes blobs uses a key prefix that is
// neither a known data prefix nor in WormPrefixes: a new write-once prefix must be routed
// to the WORM store deliberately, not fall through to the data store by accident.
func TestEveryBlobPrefixIsRouted(t *testing.T) {
	known := map[string]bool{}
	for _, p := range append(append([]string{}, WormPrefixes...), knownDataPrefixes...) {
		known[p] = true
	}
	root := filepath.Join("..", "..") // backend/internal
	scanned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		if strings.Contains(filepath.ToSlash(p), "/adapters/") || strings.Contains(filepath.ToSlash(p), "/platform/blobstore/") {
			return nil
		}
		b, err := os.ReadFile(p) // #nosec G304 -- walking our own source tree
		if err != nil {
			return err
		}
		src := string(b)
		if !strings.Contains(src, ".PutWriteOnce(") {
			return nil
		}
		scanned++
		for _, line := range strings.Split(src, "\n") {
			if reImportLine.MatchString(line) {
				continue
			}
			for _, m := range reBlobLiteral.FindAllStringSubmatch(line, -1) {
				if !known[m[1]] && !mimeTypes[m[1]] {
					t.Errorf("%s: blob prefix %q is not routed; add it to WormPrefixes (write-once evidence) or knownDataPrefixes", p, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 4 {
		t.Fatalf("scanned only %d files that write blobs; the scan is broken", scanned)
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"a/b.json", "documents/u/1.bin"} {
		if err := ValidKey(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
	for _, k := range []string{"", "/a", "a//b", "a/../b", "./a", "A/b", ".a", strings.Repeat("a", 202)} {
		if ValidKey(k) == nil {
			t.Errorf("%q accepted", k)
		}
	}
}
