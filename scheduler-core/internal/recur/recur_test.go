package recur

import (
	"testing"
	"time"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func local(t *testing.T, zone, wall string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02T15:04:05", wall, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func between(t *testing.T, rule, zone string, start time.Time, from, to string) []time.Time {
	t.Helper()
	r, err := Parse(rule, zone, start, nil)
	if err != nil {
		t.Fatalf("parse %q: %v", rule, err)
	}
	return r.Between(utc(from), utc(to))
}

func eq(t *testing.T, got []time.Time, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d instants %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if !got[i].Equal(utc(want[i])) {
			t.Fatalf("instant %d: got %s, want %s", i, got[i].UTC().Format(time.RFC3339), want[i])
		}
	}
}

func TestDailyStaysAtLocalTimeAcrossSpringForward(t *testing.T) {
	start := local(t, "America/New_York", "2026-03-06T08:00:00")
	got := between(t, "FREQ=DAILY", "America/New_York", start, "2026-03-06T00:00:00Z", "2026-03-10T23:59:59Z")
	// 08:00 EST = 13:00Z until 7 March; 08:00 EDT = 12:00Z from 8 March.
	eq(t, got, "2026-03-06T13:00:00Z", "2026-03-07T13:00:00Z", "2026-03-08T12:00:00Z", "2026-03-09T12:00:00Z", "2026-03-10T12:00:00Z")
}

func TestSpringForwardGapFiresOnceAtFirstValidInstant(t *testing.T) {
	// 02:30 does not exist on 8 March 2026 in New York (clocks go 02:00 to 03:00 at 07:00Z).
	start := local(t, "America/New_York", "2026-03-07T02:30:00")
	got := between(t, "FREQ=DAILY", "America/New_York", start, "2026-03-07T00:00:00Z", "2026-03-09T23:59:59Z")
	eq(t, got, "2026-03-07T07:30:00Z", "2026-03-08T07:00:00Z", "2026-03-09T06:30:00Z")
}

func TestFallBackOverlapFiresOnceAtFirstOccurrence(t *testing.T) {
	// 01:30 happens twice on 1 November 2026 in New York (EDT 05:30Z, then EST 06:30Z). Only the first fires.
	start := local(t, "America/New_York", "2026-10-31T01:30:00")
	got := between(t, "FREQ=DAILY", "America/New_York", start, "2026-10-31T00:00:00Z", "2026-11-02T23:59:59Z")
	eq(t, got, "2026-10-31T05:30:00Z", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z")
}

func TestEuropeLondonTransitions(t *testing.T) {
	start := local(t, "Europe/London", "2026-03-28T01:30:00")
	got := between(t, "FREQ=DAILY", "Europe/London", start, "2026-03-28T00:00:00Z", "2026-03-30T23:59:59Z")
	// 29 March: 01:30 does not exist (01:00Z is the change); 30 March 01:30 BST = 00:30Z.
	eq(t, got, "2026-03-28T01:30:00Z", "2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z")
	start = local(t, "Europe/London", "2026-10-24T01:30:00")
	got = between(t, "FREQ=DAILY", "Europe/London", start, "2026-10-24T00:00:00Z", "2026-10-26T23:59:59Z")
	// 25 October: 01:30 happens twice; the first is BST = 00:30Z.
	eq(t, got, "2026-10-24T00:30:00Z", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z")
}

func TestTwoTimesInsideAGapCollapseToOneInstant(t *testing.T) {
	start := local(t, "America/New_York", "2026-03-08T02:15:00")
	got := between(t, "FREQ=DAILY;BYMINUTE=15,45;BYHOUR=2", "America/New_York", start, "2026-03-08T00:00:00Z", "2026-03-08T23:59:59Z")
	eq(t, got, "2026-03-08T07:00:00Z")
}

func TestFixedOffsetZone(t *testing.T) {
	start := local(t, "Asia/Kolkata", "2026-11-17T08:00:00")
	got := between(t, "FREQ=DAILY;BYHOUR=8,20", "Asia/Kolkata", start, "2026-11-17T00:00:00Z", "2026-11-18T23:59:59Z")
	eq(t, got, "2026-11-17T02:30:00Z", "2026-11-17T14:30:00Z", "2026-11-18T02:30:00Z", "2026-11-18T14:30:00Z")
}

func TestMonthEndSkipsShortMonths(t *testing.T) {
	start := local(t, "America/New_York", "2026-01-31T09:00:00")
	got := between(t, "FREQ=MONTHLY;BYMONTHDAY=31", "America/New_York", start, "2026-01-01T00:00:00Z", "2026-06-30T00:00:00Z")
	eq(t, got, "2026-01-31T14:00:00Z", "2026-03-31T13:00:00Z", "2026-05-31T13:00:00Z")
}

func TestCountAndUntil(t *testing.T) {
	start := local(t, "Europe/London", "2026-06-01T08:00:00")
	got := between(t, "FREQ=DAILY;COUNT=3", "Europe/London", start, "2026-05-30T00:00:00Z", "2026-07-30T00:00:00Z")
	eq(t, got, "2026-06-01T07:00:00Z", "2026-06-02T07:00:00Z", "2026-06-03T07:00:00Z")
	// UNTIL as a UTC instant, as floating wall time, and as a date (the whole day is included).
	for _, rule := range []string{"FREQ=DAILY;UNTIL=20260602T070000Z", "FREQ=DAILY;UNTIL=20260602T080000", "FREQ=DAILY;UNTIL=20260602"} {
		got = between(t, rule, "Europe/London", start, "2026-05-30T00:00:00Z", "2026-07-30T00:00:00Z")
		eq(t, got, "2026-06-01T07:00:00Z", "2026-06-02T07:00:00Z")
	}
}

func TestEndInstantAndExhausted(t *testing.T) {
	start := local(t, "Europe/London", "2026-06-01T08:00:00")
	end := utc("2026-06-02T07:00:00Z")
	r, err := Parse("FREQ=DAILY", "Europe/London", start, &end)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, r.Between(utc("2026-05-30T00:00:00Z"), utc("2026-07-01T00:00:00Z")), "2026-06-01T07:00:00Z", "2026-06-02T07:00:00Z")
	if !r.Exhausted(utc("2026-06-02T07:00:00Z")) {
		t.Fatal("exhausted at the end instant")
	}
	r2, _ := Parse("FREQ=DAILY;COUNT=2", "Europe/London", start, nil)
	if r2.Exhausted(utc("2026-06-01T12:00:00Z")) || !r2.Exhausted(utc("2026-06-02T12:00:00Z")) {
		t.Fatal("COUNT=2 is exhausted only after the second occurrence")
	}
	r3, _ := Parse("FREQ=DAILY", "Europe/London", start, nil)
	if r3.Exhausted(utc("2030-01-01T00:00:00Z")) {
		t.Fatal("an unbounded rule is never exhausted")
	}
}

func TestSlicingWindowsGivesNoDuplicatesOrGaps(t *testing.T) {
	start := local(t, "America/New_York", "2026-10-25T01:30:00")
	r, err := Parse("FREQ=DAILY;BYHOUR=1,3", "America/New_York", start, nil)
	if err != nil {
		t.Fatal(err)
	}
	whole := r.Between(utc("2026-10-25T00:00:00Z"), utc("2026-11-04T00:00:00Z"))
	var pieces []time.Time
	for from := utc("2026-10-25T00:00:00Z"); from.Before(utc("2026-11-04T00:00:00Z")); from = from.Add(7 * time.Hour) {
		pieces = append(pieces, r.Between(from, from.Add(7*time.Hour))...)
	}
	if len(whole) != len(pieces) {
		t.Fatalf("whole %d vs pieces %d", len(whole), len(pieces))
	}
	for i := range whole {
		if !whole[i].Equal(pieces[i]) {
			t.Fatalf("mismatch at %d: %v vs %v", i, whole[i], pieces[i])
		}
	}
}

func TestParseRefuses(t *testing.T) {
	start := local(t, "America/New_York", "2026-03-06T08:00:00")
	bad := []string{
		"", "FREQ=SECONDLY", "FREQ=MINUTELY;INTERVAL=30", "FREQ=HOURLY;INTERVAL=1;BYMINUTE=0,30,15",
		"FREQ=DAILY;BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23;BYMINUTE=0,30,15",
		"DTSTART:20260101T000000\nRRULE:FREQ=DAILY", "FREQ=DAILY;DTSTART=20260101", "FREQ=NEVER", "garbage",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30", "FREQ=DAILY;COUNT=100000", "FREQ=DAILY;TZID=America/New_York",
	}
	for _, rule := range bad {
		if _, err := Parse(rule, "America/New_York", start, nil); err == nil {
			t.Errorf("rule %q should be refused", rule)
		}
	}
	for _, z := range []string{"", "Local", "local", "../etc/passwd", "Not/AZone", " America/New_York", "America/New_York\n"} {
		if _, err := Parse("FREQ=DAILY", z, start, nil); err == nil {
			t.Errorf("zone %q should be refused", z)
		}
	}
	for _, rule := range []string{"FREQ=DAILY", "RRULE:FREQ=DAILY;BYHOUR=8,14,20", "FREQ=HOURLY;INTERVAL=2", "FREQ=WEEKLY;BYDAY=MO,TH;COUNT=10"} {
		if _, err := Parse(rule, "UTC", start, nil); err != nil {
			t.Errorf("rule %q should be accepted: %v", rule, err)
		}
	}
}
