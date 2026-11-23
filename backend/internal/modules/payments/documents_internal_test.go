package payments

import (
	"testing"
	"time"
)

// Review F13: financial-year labels for an April start and for a calendar year.
func TestFinancialYearLabels(t *testing.T) {
	utc := time.UTC
	april := &DocumentConfig{FYStartMonth: time.April, Zone: utc}
	if got := april.fyLabel(time.Date(2027, 3, 31, 12, 0, 0, 0, utc)); got != "2026-27" {
		t.Fatalf("31 March: %s", got)
	}
	if got := april.fyLabel(time.Date(2027, 4, 1, 0, 0, 0, 0, utc)); got != "2027-28" {
		t.Fatalf("1 April: %s", got)
	}
	cal := &DocumentConfig{FYStartMonth: time.January, Zone: utc}
	if got := cal.fyLabel(time.Date(2026, 12, 31, 23, 0, 0, 0, utc)); got != "2026-CY" {
		t.Fatalf("calendar year: %s", got)
	}
	if got := april.fyLabel(time.Date(2099, 4, 1, 0, 0, 0, 0, utc)); got != "2099-00" {
		t.Fatalf("century turn: %s", got)
	}
}
