package scrub

import (
	"strings"
	"testing"
)

// validAadhaarLike builds a synthetic Verhoeff-valid 12-digit number (never a real one:
// test data is synthetic only, CLAUDE.md §5.3).
func validAadhaarLike(t *testing.T, prefix11 string) string {
	t.Helper()
	for d := '0'; d <= '9'; d++ {
		s := prefix11 + string(d)
		if Verhoeff(s) {
			return s
		}
	}
	t.Fatalf("no check digit for %s", prefix11)
	return ""
}

func TestVerhoeffKnownVectors(t *testing.T) {
	if !Verhoeff("2363") {
		t.Fatal("2363 is the textbook valid Verhoeff number")
	}
	if Verhoeff("2364") {
		t.Fatal("2364 must fail")
	}
	if Verhoeff("") || Verhoeff("12a4") {
		t.Fatal("empty and non-digit input must fail")
	}
}

// TC-20 (E-DP-26): a Verhoeff-valid 12-digit number in free text is detected, with or
// without separators; an invalid one is not.
func TestTC20ProhibitedIDDetection(t *testing.T) {
	names := []string{"verhoeff_12_digit_first_2_to_9", "digits_16"}
	a := validAadhaarLike(t, "23456789012")
	spaced := a[:4] + " " + a[4:8] + " " + a[8:]
	hyph := a[:4] + "-" + a[4:8] + "-" + a[8:]
	for _, in := range []string{a, "my id is " + spaced, hyph, "x " + a + " 9876543210"} {
		if !ContainsProhibitedID(in, names) {
			t.Errorf("not detected: %q", in)
		}
	}
	bad := a[:11] + string('0'+(a[11]-'0'+1)%10)
	if ContainsProhibitedID("ref "+bad, names[:1]) {
		t.Errorf("checksum-invalid number flagged: %s", bad)
	}
	if ContainsProhibitedID("1"+a[1:], names[:1]) && a[0] != '1' {
		// numbers starting with 0 or 1 are never Aadhaar
		if Verhoeff("1" + a[1:]) {
			t.Errorf("number starting with 1 flagged")
		}
	}
	if !ContainsProhibitedID("vid 1234 5678 9012 3456", names) {
		t.Error("16-digit VID-like number not detected")
	}
	if ContainsProhibitedID("fee 50000 paise", names) {
		t.Error("short numbers must not be flagged")
	}
}

func TestTextRemovesPersonalPatterns(t *testing.T) {
	a := validAadhaarLike(t, "34567890123")
	in := "call +91 98765 43210 or mail ravi.k@example.com token=abc.def aadhaar " + a +
		" jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig hex " + strings.Repeat("ab", 32)
	out := Text(in)
	for _, leak := range []string{"98765", "43210", "ravi.k", "example.com", "abc.def", a, "eyJ", "abab"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %q", leak, out)
		}
	}
	before := Hits()
	_ = Text("+919876543210")
	if Hits() <= before {
		t.Error("hit counter must increase on redaction")
	}
}

func TestFreeTextRemovesShortCodes(t *testing.T) {
	out := FreeText("otp check failed for code 482913")
	if strings.Contains(out, "482913") {
		t.Fatalf("OTP-like code leaked: %q", out)
	}
	if Text("status 200 in 15 ms") != "status 200 in 15 ms" {
		t.Fatal("harmless short numbers must survive Text")
	}
}
