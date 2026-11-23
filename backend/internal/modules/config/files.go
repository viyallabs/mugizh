package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ManifestFile is the manifest's file name inside a pack directory.
const ManifestFile = "pack.yaml"

const maxFileBytes = 4 << 20

// strictDecode decodes one YAML document into v, rejecting unknown keys, duplicate keys
// and trailing documents.
func strictDecode(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("more than one YAML document")
	}
	return nil
}

func parseNode(data []byte) (*yaml.Node, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	if n.Kind == 0 {
		return nil, errors.New("empty document")
	}
	return &n, nil
}

// checkRelPath refuses empty, absolute, parent-escaping and backslash paths.
func checkRelPath(p string) error {
	if p == "" || strings.ContainsRune(p, '\\') || strings.ContainsRune(p, 0) ||
		strings.HasPrefix(p, "/") || !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("path %q is not a relative path inside the pack directory", p)
	}
	return nil
}

// readPackFile reads a file inside dir. os.Root keeps the read inside dir even when a
// symbolic link points outside it.
func readPackFile(dir, rel string) ([]byte, error) {
	if err := checkRelPath(rel); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", rel, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", rel, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("file %q is larger than %d bytes", rel, maxFileBytes)
	}
	return data, nil
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// referenced returns the pack files named by the manifest, sorted.
func (m *manifest) referenced() []string {
	set := map[string]bool{}
	for _, p := range []string{
		m.Money.Tax, m.Clinical.PrescribingRules, m.Clinical.DrugClassification,
		m.Payments.RefundPolicy, m.Messaging.SMSRegistry,
	} {
		if p != "" {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ComputeChecksums reads the manifest in dir and returns the SHA-256 of every file it
// references (used by cmd/packsum and CI). It does not validate anything else.
func ComputeChecksums(dir string) (map[string]string, error) {
	data, err := readPackFile(dir, ManifestFile)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	out := map[string]string{}
	for _, rel := range m.referenced() {
		b, err := readPackFile(dir, rel)
		if err != nil {
			return nil, err
		}
		out[rel] = sum(b)
	}
	return out, nil
}
