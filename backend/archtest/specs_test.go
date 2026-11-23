package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The OpenAPI specs are the source of truth (K-ARC-06), so card data (K-PAY-01) and
// Aadhaar (K-DP-05) fields are checked in the specs too, not only in generated code.
var (
	reSpecCard    = regexp.MustCompile(`(?i)^\s*-?\s*(card_?number|cardnumber|cvv|cvc|card_?expiry|upi_?pin|bank_?account_?number)\s*:`)
	reSpecAadhaar = regexp.MustCompile(`(?i)^\s*-?\s*[a-z_]*(aadhaar|aadhar|uidai)[a-z_]*\s*:`)
)

func checkSpecs(files map[string]string) []string {
	var v []string
	for name, src := range files {
		for i, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if reSpecCard.MatchString(line) {
				v = append(v, name+":"+itoa(i+1)+": K-PAY-01 card or bank data field in an API spec")
			}
			if reSpecAadhaar.MatchString(line) {
				v = append(v, name+":"+itoa(i+1)+": K-DP-05 Aadhaar field in an API spec")
			}
		}
	}
	return v
}

func itoa(n int) string {
	s := ""
	for n > 0 || s == "" {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestSpecs_NoCardOrAadhaarFields(t *testing.T) {
	root := filepath.Join("..", "..", "api")
	files := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".spectral.yaml") {
			b, _ := os.ReadFile(p) // #nosec G304 -- repo files
			files[p] = string(b)
		}
		return nil
	})
	if len(files) == 0 {
		t.Fatal("no API specs found")
	}
	requireClean(t, checkSpecs(files))
}

func TestSpecs_NoCardOrAadhaarFields_Negative(t *testing.T) {
	requireFires(t, checkSpecs(map[string]string{"x.yaml": "properties:\n  cardNumber:\n    type: string\n"}), "K-PAY-01")
	requireFires(t, checkSpecs(map[string]string{"x.yaml": "properties:\n  aadhaarNumber:\n    type: string\n"}), "K-DP-05")
}
