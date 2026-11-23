// Package archtest turns the contract rules that can be checked mechanically into Go tests (S-092, K-DOD-01).
// Standard library only. Every rule is a checker function over in-memory sources, so the real-tree test and
// the negative test (which proves the rule fires) run the SAME code. All files here are _test.go on purpose:
// the rule scans exempt backend/archtest/ (it must name the forbidden things to detect them) and ignore testdata/.
// A rule that cannot find any source fails closed (see TestLoaderFindsSources).
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	backendModule   = "gitlab.com/viyallabs/mugizh/backend"
	schedulerModule = "gitlab.com/viyallabs/mugizh/scheduler-core"
	modBackend      = "backend"
	modScheduler    = "scheduler-core"
)

// srcFile is one source file. Rel is slash-separated and relative to its Go module root.
type srcFile struct {
	Mod string
	Rel string
	Src string
}

func (f srcFile) display() string { return f.Mod + "/" + f.Rel }
func (f srcFile) isGo() bool      { return strings.HasSuffix(f.Rel, ".go") }
func (f srcFile) isTest() bool    { return strings.HasSuffix(f.Rel, "_test.go") }
func (f srcFile) isSQL() bool     { return strings.HasSuffix(f.Rel, ".sql") }
func (f srcFile) inArchtest() bool {
	return f.Mod == modBackend && strings.HasPrefix(f.Rel, "archtest/")
}

type parsedFile struct {
	srcFile
	fset *token.FileSet
	ast  *ast.File
}

func (p parsedFile) at(n ast.Node) string {
	return fmt.Sprintf("%s:%d", p.display(), p.fset.Position(n.Pos()).Line)
}

// parseGoFiles parses every .go file. A file that does not parse is itself a violation (fail closed).
func parseGoFiles(files []srcFile) ([]parsedFile, []string) {
	var out []parsedFile
	var errs []string
	for _, f := range files {
		if !f.isGo() {
			continue
		}
		fset := token.NewFileSet()
		a, err := parser.ParseFile(fset, f.display(), f.Src, parser.SkipObjectResolution)
		if err != nil {
			errs = append(errs, f.display()+": cannot parse: "+err.Error())
			continue
		}
		out = append(out, parsedFile{f, fset, a})
	}
	return out, errs
}

func importPaths(p parsedFile) []*ast.ImportSpec { return p.ast.Imports }

func impPath(s *ast.ImportSpec) string { return strings.Trim(s.Path.Value, "`\"") }

// backendRel maps an import path to a path relative to the backend module root.
func backendRel(imp string) (string, bool) {
	if imp == backendModule {
		return "", true
	}
	if strings.HasPrefix(imp, backendModule+"/") {
		return strings.TrimPrefix(imp, backendModule+"/"), true
	}
	return "", false
}

func hasPathPrefix(p, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

var (
	treeOnce  sync.Once
	treeFiles []srcFile
	treeErr   error
)

func repoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("go.work not found above " + dir)
		}
		dir = parent
	}
}

// loadTree reads every .go, .sql and go.mod file of backend/ and scheduler-core/ (skipping testdata, vendor, bin).
func loadTree(tb testing.TB) []srcFile {
	tb.Helper()
	root := repoRoot(tb)
	treeOnce.Do(func() {
		for _, mod := range []string{modBackend, modScheduler} {
			base := filepath.Join(root, mod)
			treeErr = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					switch d.Name() {
					case "testdata", "vendor", "node_modules", ".git", "bin":
						return filepath.SkipDir
					}
					return nil
				}
				name := d.Name()
				if !(strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".sql") || name == "go.mod") {
					return nil
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(base, path)
				treeFiles = append(treeFiles, srcFile{Mod: mod, Rel: filepath.ToSlash(rel), Src: string(b)})
				return nil
			})
			if treeErr != nil {
				return
			}
		}
	})
	if treeErr != nil {
		tb.Fatal(treeErr)
	}
	return treeFiles
}

func TestLoaderFindsSources(t *testing.T) {
	files := loadTree(t)
	var goBackend, mods, sqlN int
	for _, f := range files {
		if f.Mod == modBackend && f.isGo() {
			goBackend++
		}
		if f.isSQL() {
			sqlN++
		}
		if strings.HasPrefix(f.Rel, "internal/modules/") {
			mods++
		}
	}
	if goBackend < 5 || mods < 1 || sqlN < 1 {
		t.Fatalf("loader found too little (go=%d modules files=%d sql=%d): rules would pass vacuously", goBackend, mods, sqlN)
	}
	if _, errs := parseGoFiles(files); len(errs) > 0 {
		t.Fatalf("unparseable files:\n%s", strings.Join(errs, "\n"))
	}
}

func requireClean(t *testing.T, v []string) {
	t.Helper()
	if len(v) > 0 {
		t.Errorf("%d violation(s):\n  %s", len(v), strings.Join(v, "\n  "))
	}
}

// requireFires asserts the checker reported at least one violation containing every given substring.
func requireFires(t *testing.T, v []string, substrs ...string) {
	t.Helper()
	if len(v) == 0 {
		t.Fatal("rule did not fire on a known violation")
	}
	joined := strings.Join(v, "\n")
	for _, s := range substrs {
		if !strings.Contains(joined, s) {
			t.Fatalf("rule fired but not on %q:\n%s", s, joined)
		}
	}
}

func goSrc(imports ...string) string {
	var b strings.Builder
	b.WriteString("package x\n\nimport (\n")
	for _, i := range imports {
		b.WriteString("\t_ \"" + i + "\"\n")
	}
	b.WriteString(")\n")
	return b.String()
}

func be(rel, src string) srcFile { return srcFile{Mod: modBackend, Rel: rel, Src: src} }
