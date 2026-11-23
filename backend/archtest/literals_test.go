package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// forEachLiteral calls fn for every string literal (which includes struct tags) and identifier in the
// given parsed files. Comments are never visited because the files are parsed without comments.
func forEachNode(p parsedFile, fn func(n ast.Node, kind, text string)) {
	// Comments are not parsed (SkipObjectResolution only), so detect the standard
	// "Code generated ... DO NOT EDIT." marker in the source text.
	generated := reGenerated.MatchString(p.Src)
	ast.Inspect(p.ast, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				s, err := strconv.Unquote(x.Value)
				if err != nil {
					s = x.Value
				}
				// oapi-codegen embeds the spec as base64 chunks; random substrings of
				// base64 are not data. The spec itself (and the generated identifiers and
				// tags) are still checked.
				if generated && reBase64Chunk.MatchString(s) {
					return true
				}
				fn(n, "string", s)
			}
		case *ast.Ident:
			fn(n, "identifier", x.Name)
		}
		return true
	})
}

// codeFiles returns parsed non-test Go files (shipped code), skipping archtest itself.
func shippedGo(files []srcFile) ([]parsedFile, []string) {
	var keep []srcFile
	for _, f := range files {
		if f.isGo() && !f.isTest() && !f.inArchtest() {
			keep = append(keep, f)
		}
	}
	return parseGoFiles(keep)
}

// anyGo returns parsed Go files including tests (but not archtest).
func anyGo(files []srcFile) ([]parsedFile, []string) {
	var keep []srcFile
	for _, f := range files {
		if f.isGo() && !f.inArchtest() {
			keep = append(keep, f)
		}
	}
	return parseGoFiles(keep)
}

var (
	reBase64Chunk = regexp.MustCompile(`^[A-Za-z0-9+/=]{40,}$`)
	reGenerated   = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT.$`)
)

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func stripSQLComments(s string) string {
	return sqlLineComment.ReplaceAllString(sqlBlockComment.ReplaceAllString(s, " "), "")
}

func sqlFiles(files []srcFile) []srcFile {
	var out []srcFile
	for _, f := range files {
		if f.isSQL() {
			out = append(out, f)
		}
	}
	return out
}

// ---- K-GLB-01: no country constants in shipped code ----

var (
	countryExact    = map[string]bool{"112": true, "108": true}
	countryContains = []string{"+91", `\+91`, "Asia/Kolkata", "en-IN", "hi-IN", "ta-IN", "Schedule X", "Schedule H"}
	countryWord     = regexp.MustCompile(`\b(INR|NDPS|DLT)\b`)
)

func checkNoCountryLiterals(files []srcFile) []string {
	parsed, v := shippedGo(files)
	for _, p := range parsed {
		forEachNode(p, func(n ast.Node, kind, text string) {
			if kind != "string" {
				return
			}
			hit := countryExact[text] || countryWord.MatchString(text)
			for _, c := range countryContains {
				if strings.Contains(text, c) {
					hit = true
				}
			}
			if hit {
				v = append(v, fmt.Sprintf("%s: K-GLB-01 country value in string literal %q; use the country pack", p.at(n), text))
			}
		})
	}
	return v
}

func TestKGLB01_NoCountryLiterals(t *testing.T) { requireClean(t, checkNoCountryLiterals(loadTree(t))) }

func TestKGLB01_NoCountryLiterals_Negative(t *testing.T) {
	for _, lit := range []string{`"+91"`, `"112"`, `"108"`, `"INR"`, `"Asia/Kolkata"`, `"en-IN"`, `"hi-IN"`, `"ta-IN"`, `"Schedule X"`, `"Schedule H"`, `"NDPS"`, `"DLT"`, `"tel:+919999999999"`, `"currency=INR"`} {
		bad := []srcFile{be("internal/modules/booking/a.go", "package x\n\nvar v = "+lit+"\n")}
		requireFires(t, checkNoCountryLiterals(bad), "K-GLB-01")
	}
	ok := []srcFile{
		be("internal/modules/booking/a.go", "package x\n\n// +91 and 112 and INR in a comment are fine\nvar v = \"1120\"\nvar w = \"INRIA\"\n"),
		be("internal/modules/booking/a_test.go", "package x\n\nvar v = \"+91\"\n"),
	}
	requireClean(t, checkNoCountryLiterals(ok))
}

// ---- K-DP-05: no Aadhaar number or VID anywhere (code identifiers, tags, strings, SQL) ----

var (
	aadhaarRe = regexp.MustCompile(`(?i)aadhaar|aadhar|uidai`)
	vidWordRe = regexp.MustCompile(`(?i)\bvid\b`)
)

// Test files are exempt from the identifier/string check: the scrubber and logging tests must contain synthetic
// Aadhaar-like values and the words to prove rejection. SQL files (migrations, fixtures) are never exempt.
func checkNoAadhaar(files []srcFile) []string {
	parsed, v := shippedGo(files)
	for _, p := range parsed {
		forEachNode(p, func(n ast.Node, kind, text string) {
			switch {
			case aadhaarRe.MatchString(text):
				v = append(v, fmt.Sprintf("%s: K-DP-05 %s mentions Aadhaar/UIDAI: %q", p.at(n), kind, text))
			case kind == "identifier" && strings.EqualFold(text, "vid"):
				v = append(v, fmt.Sprintf("%s: K-DP-05 identifier named vid", p.at(n)))
			case kind == "string" && vidWordRe.MatchString(text):
				v = append(v, fmt.Sprintf("%s: K-DP-05 string/tag names a vid field: %q", p.at(n), text))
			}
		})
	}
	for _, f := range sqlFiles(files) {
		if f.inArchtest() {
			continue
		}
		s := stripSQLComments(f.Src)
		if aadhaarRe.MatchString(s) || vidWordRe.MatchString(s) {
			v = append(v, f.display()+": K-DP-05 SQL mentions Aadhaar/UIDAI/vid")
		}
	}
	return v
}

func TestKDP05_NoAadhaar(t *testing.T) { requireClean(t, checkNoAadhaar(loadTree(t))) }

func TestKDP05_NoAadhaar_Negative(t *testing.T) {
	cases := []string{
		"package x\n\ntype U struct{ AadhaarNumber string }\n",
		"package x\n\nvar aadhar = 1\n",
		"package x\n\ntype U struct{ N string `json:\"uidai_ref\"` }\n",
		"package x\n\ntype U struct{ V string `json:\"vid\"` }\n",
		"package x\n\nvar vid = 1\n",
		"package x\n\nconst q = \"SELECT aadhaar FROM t\"\n",
	}
	for _, c := range cases {
		requireFires(t, checkNoAadhaar([]srcFile{be("internal/modules/identity/a.go", c)}), "K-DP-05")
	}
	requireFires(t, checkNoAadhaar([]srcFile{be("internal/modules/identity/migrations/0001_x.up.sql", "CREATE TABLE identity.u (aadhaar_no text);")}), "K-DP-05")
	requireFires(t, checkNoAadhaar([]srcFile{be("internal/modules/identity/migrations/0001_x.up.sql", "CREATE TABLE identity.u (vid text);")}), "K-DP-05")
	requireFires(t, checkNoAadhaar([]srcFile{{Mod: modScheduler, Rel: "a.go", Src: "package x\n\nvar aadhaar = 1\n"}}), "K-DP-05")
	ok := []srcFile{
		be("internal/modules/identity/a.go", "package x\n\n// Aadhaar and VID are never stored (comment)\nvar video = \"invalid\"\n"),
		be("internal/modules/identity/migrations/0001_x.up.sql", "-- no aadhaar here\n/* nor vid */\nCREATE TABLE identity.u (video text);"),
	}
	requireClean(t, checkNoAadhaar(ok))
}

// ---- K-PAY-01: no card, UPI PIN or bank account data fields ----

var cardRe = regexp.MustCompile(`(?i)card_?number|cvv|cvc|card_?expiry|upi_?pin|bank_?account_?number`)

func checkNoCardData(files []srcFile) []string {
	parsed, v := anyGo(files)
	for _, p := range parsed {
		forEachNode(p, func(n ast.Node, kind, text string) {
			if cardRe.MatchString(text) {
				v = append(v, fmt.Sprintf("%s: K-PAY-01 %s names card/bank data: %q", p.at(n), kind, text))
			}
		})
	}
	for _, f := range sqlFiles(files) {
		if cardRe.MatchString(stripSQLComments(f.Src)) {
			v = append(v, f.display()+": K-PAY-01 SQL names card/bank data")
		}
	}
	return v
}

func TestKPAY01_NoCardData(t *testing.T) { requireClean(t, checkNoCardData(loadTree(t))) }

func TestKPAY01_NoCardData_Negative(t *testing.T) {
	cases := []string{
		"package x\n\ntype P struct{ CardNumber string }\n",
		"package x\n\ntype P struct{ X string `json:\"cvv\"` }\n",
		"package x\n\nvar card_expiry = 1\n",
		"package x\n\nconst c = \"upi_pin\"\n",
		"package x\n\ntype P struct{ BankAccountNumber string }\n",
		"package x\n\nvar cvc = 1\n",
	}
	for _, c := range cases {
		requireFires(t, checkNoCardData([]srcFile{be("internal/modules/payments/a.go", c)}), "K-PAY-01")
	}
	requireFires(t, checkNoCardData([]srcFile{be("internal/modules/payments/migrations/0001_x.up.sql", "CREATE TABLE payments.p (card_number text);")}), "K-PAY-01")
	requireClean(t, checkNoCardData([]srcFile{
		be("internal/modules/payments/a.go", "package x\n\n// card number is never held\nvar checkoutURL = \"x\"\n"),
		be("internal/modules/payments/migrations/0001_x.up.sql", "-- cvv never stored\nCREATE TABLE payments.p (id uuid);"),
	}))
}

// ---- K-DP-04: logging discipline ----

var slogMethods = map[string]int{
	"Debug": 0, "Info": 0, "Warn": 0, "Error": 0,
	"DebugContext": 1, "InfoContext": 1, "WarnContext": 1, "ErrorContext": 1,
	"Log": 2, "LogAttrs": 2,
}

func isLoggerExpr(e ast.Expr) bool {
	s := strings.ToLower(types.ExprString(e))
	return strings.Contains(s, "log") || s == "l" || strings.HasSuffix(s, ".l")
}

func checkLoggingDiscipline(files []srcFile) []string {
	parsed, v := shippedGo(files)
	for _, p := range parsed {
		if hasPathPrefix(p.Rel, "cmd") {
			continue
		}
		for _, s := range importPaths(p) {
			if impPath(s) == "log" {
				v = append(v, fmt.Sprintf("%s: K-DP-04 std log package is banned outside cmd/; use platform/logging", p.at(s)))
			}
		}
		ast.Inspect(p.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "print" || fn.Name == "println" {
					v = append(v, fmt.Sprintf("%s: K-DP-04 builtin %s is banned outside cmd/", p.at(call), fn.Name))
				}
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "fmt" {
					if strings.HasPrefix(fn.Sel.Name, "Print") {
						v = append(v, fmt.Sprintf("%s: K-DP-04 fmt.%s is banned outside cmd/", p.at(call), fn.Sel.Name))
					}
					if strings.HasPrefix(fn.Sel.Name, "Fprint") && len(call.Args) > 0 {
						if a := types.ExprString(call.Args[0]); a == "os.Stdout" || a == "os.Stderr" {
							v = append(v, fmt.Sprintf("%s: K-DP-04 fmt.%s to %s is banned outside cmd/", p.at(call), fn.Sel.Name, a))
						}
					}
				}
				idx, isLog := slogMethods[fn.Sel.Name]
				if !isLog || !isLoggerExpr(fn.X) || len(call.Args) <= idx {
					return true
				}
				if lit, ok := call.Args[idx].(*ast.BasicLit); !ok || lit.Kind != token.STRING {
					v = append(v, fmt.Sprintf("%s: K-DP-04 log message of %s must be a constant string event name", p.at(call), types.ExprString(fn)))
				}
			}
			return true
		})
	}
	return v
}

func TestKDP04_LoggingDiscipline(t *testing.T) { requireClean(t, checkLoggingDiscipline(loadTree(t))) }

func TestKDP04_LoggingDiscipline_Negative(t *testing.T) {
	cases := map[string]string{
		"std log":      "package x\n\nimport \"log\"\n\nvar _ = log.Println\n",
		"fmt.Println":  "package x\n\nimport \"fmt\"\n\nfunc f() { fmt.Println(\"x\") }\n",
		"fmt.Printf":   "package x\n\nimport \"fmt\"\n\nfunc f() { fmt.Printf(\"x\") }\n",
		"fmt.Fprint":   "package x\n\nimport (\"fmt\"; \"os\")\n\nfunc f() { fmt.Fprintln(os.Stderr, \"x\") }\n",
		"println":      "package x\n\nfunc f() { println(\"x\") }\n",
		"dynamic msg":  "package x\n\nimport \"log/slog\"\n\nfunc f(l *slog.Logger, s string) { l.Info(s) }\n",
		"slog dynamic": "package x\n\nimport \"log/slog\"\n\nfunc f(s string) { slog.Error(s) }\n",
		"concat msg":   "package x\n\nimport \"log/slog\"\n\nfunc f(l *slog.Logger, s string) { l.Warn(\"a\" + s) }\n",
		"ctx dynamic":  "package x\n\nimport (\"context\"; \"log/slog\")\n\nfunc f(l *slog.Logger, s string) { l.InfoContext(context.Background(), s) }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			requireFires(t, checkLoggingDiscipline([]srcFile{be("internal/platform/x/a.go", src)}), "K-DP-04")
		})
	}
	ok := []srcFile{
		be("internal/platform/x/a.go", "package x\n\nimport \"log/slog\"\n\nfunc f(l *slog.Logger, err error) {\n\tl.Info(\"booking.hold_expired\", \"ref\", 1)\n\tl.Error(\"x.failed\", \"err\", err.Error())\n\tslog.Warn(\"a.b\")\n}\n"),
		be("cmd/api/main.go", "package main\n\nimport (\"fmt\"; \"log\")\n\nfunc main() { fmt.Println(\"x\"); log.Println(\"y\") }\n"),
		be("internal/platform/x/a_test.go", "package x\n\nimport \"fmt\"\n\nfunc f() { fmt.Println(\"tests may\") }\n"),
	}
	requireClean(t, checkLoggingDiscipline(ok))
}

// ---- Release policy §5: consent, audit, encryption, prescribing and the rules engine cannot be flagged off ----

var (
	flagFuncs     = map[string]bool{"Flag": true, "Enabled": true}
	protectedFlag = regexp.MustCompile(`(?i)consent|audit|encrypt|prescrib|rules_?engine`)
)

func checkNonFlaggableControls(files []srcFile) []string {
	parsed, v := anyGo(files)
	for _, p := range parsed {
		ast.Inspect(p.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if !flagFuncs[name] {
				return true
			}
			for _, a := range call.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					if protectedFlag.MatchString(s) {
						v = append(v, fmt.Sprintf("%s: release-policy §5 / CLAUDE.md §5.5 feature flag %q would switch off a protected control", p.at(call), s))
					}
				}
			}
			return true
		})
	}
	return v
}

func TestReleasePolicy5_NonFlaggableControls(t *testing.T) {
	requireClean(t, checkNonFlaggableControls(loadTree(t)))
}

func TestReleasePolicy5_NonFlaggableControls_Negative(t *testing.T) {
	for _, name := range []string{"consent_gate", "audit.enabled", "field_encryption", "prescribing_gate", "rules_engine"} {
		src := "package x\n\nfunc f(flags F) bool { return flags.Enabled(\"" + name + "\") }\n"
		requireFires(t, checkNonFlaggableControls([]srcFile{be("internal/app/a.go", src)}), "release-policy")
	}
	requireFires(t, checkNonFlaggableControls([]srcFile{be("internal/app/a.go", "package x\n\nvar b = Flag(\"Consent\")\n")}), "release-policy")
	requireClean(t, checkNonFlaggableControls([]srcFile{be("internal/app/a.go", "package x\n\nfunc f(flags F) bool { return flags.Enabled(\"booking_new_ui\") }\n")}))
}
