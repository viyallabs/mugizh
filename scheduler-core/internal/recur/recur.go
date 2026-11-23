// Package recur expands RFC 5545 recurrence rules in an activity's own IANA time zone (K-ARC-04).
//
// The rule is expanded on "floating" wall-clock times (the local date and time as if it were UTC), which is
// what RFC 5545 means by a DTSTART with a TZID, and every wall-clock time is then resolved to an instant
// with explicit, deterministic daylight-saving rules:
//
//   - a local time that does not exist (spring-forward gap) fires once, at the first valid instant after the
//     gap (the moment the clocks change);
//   - a local time that happens twice (fall-back overlap) fires once, at its first occurrence.
//
// So an 08:00 reminder stays at 08:00 local across a change of offset. Instants are deduplicated, so two
// wall-clock times that collapse onto the same instant fire once.
package recur

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// MaxPerDay is the most occurrences one rule may produce on one local day.
const MaxPerDay = 48

// MaxCount is the largest COUNT accepted.
const MaxCount = 10000

// Validation errors. Callers map them to problem codes; messages are fixed text with no input echoed.
var (
	ErrRule = errors.New("rrule is not valid")
	ErrZone = errors.New("time zone is not valid")
)

var zoneName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$`)

// LoadZone returns the IANA location, refusing the empty name, "Local" and anything that is not a plain zone name.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || len(name) > 64 || strings.EqualFold(name, "local") || !zoneName.MatchString(name) {
		return nil, ErrZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrZone
	}
	return loc, nil
}

// Rule is a parsed recurrence anchored at a start instant in a zone.
type Rule struct {
	rr     *rrule.RRule
	loc    *time.Location
	finite bool
	end    *time.Time
}

// floating returns the local wall-clock fields of t in loc as a UTC time.
func floating(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

var (
	untilZ        = regexp.MustCompile(`(?i)UNTIL=[0-9T]+Z`)
	untilDateOnly = regexp.MustCompile(`(?i)UNTIL=[0-9]{8}(;|$)`)
	badPart       = regexp.MustCompile(`(?i)DTSTART|TZID|EXRULE|EXDATE|RDATE|[\r\n]`)
)

// Parse validates rule for a start instant and zone and returns a Rule. end, when not nil, is the last
// instant at which an occurrence may fire. Rules that could fire more than MaxPerDay times a day, use
// SECONDLY or MINUTELY frequency, or never produce an occurrence are refused.
func Parse(ruleText, zone string, startsAt time.Time, end *time.Time) (*Rule, error) {
	loc, err := LoadZone(zone)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(ruleText)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "RRULE:"), "rrule:")
	if s == "" || len(s) > 512 || badPart.MatchString(s) {
		return nil, ErrRule
	}
	opt, err := rrule.StrToROptionInLocation(s, time.UTC)
	if err != nil || opt == nil {
		return nil, ErrRule
	}
	if opt.Freq >= rrule.MINUTELY {
		return nil, ErrRule
	}
	if opt.Count < 0 || opt.Count > MaxCount || opt.Interval < 0 {
		return nil, ErrRule
	}
	// Wall-clock UNTIL: a UTC "Z" value is an instant and is converted to local wall time; a floating value is
	// already wall time; a date-only value includes the whole day (RFC 5545).
	finite := opt.Count > 0
	if !opt.Until.IsZero() {
		finite = true
		switch {
		case untilZ.MatchString(s):
			opt.Until = floating(opt.Until, loc)
		case untilDateOnly.MatchString(s):
			opt.Until = opt.Until.Add(24*time.Hour - time.Second)
		}
	}
	if perDayBound(opt) > MaxPerDay {
		return nil, ErrRule
	}
	opt.Dtstart = floating(startsAt, loc)
	rr, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, ErrRule
	}
	r := &Rule{rr: rr, loc: loc, finite: finite, end: end}
	if !r.hasOccurrence() {
		return nil, ErrRule
	}
	return r, nil
}

// perDayBound is an upper bound on occurrences per local day from the time-of-day parts of the rule.
func perDayBound(o *rrule.ROption) int {
	hours := len(o.Byhour)
	if hours == 0 {
		hours = 1
		if o.Freq == rrule.HOURLY {
			iv := o.Interval
			if iv < 1 {
				iv = 1
			}
			hours = (24 + iv - 1) / iv
		}
	}
	minutes, seconds := len(o.Byminute), len(o.Bysecond)
	if minutes == 0 {
		minutes = 1
	}
	if seconds == 0 {
		seconds = 1
	}
	return hours * minutes * seconds
}

// hasOccurrence guards against rules that never match (for example 30 February): the search is bounded in
// time so a pathological rule is refused instead of spinning.
func (r *Rule) hasOccurrence() bool {
	done := make(chan bool, 1)
	go func() {
		done <- !r.rr.After(r.rr.OrigOptions.Dtstart.Add(-time.Second), false).IsZero()
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(750 * time.Millisecond):
		return false
	}
}

// Between returns the instants of occurrences with from < instant <= to, in ascending order, no duplicates,
// and never after the rule's end instant.
func (r *Rule) Between(from, to time.Time) []time.Time {
	if !to.After(from) {
		return nil
	}
	lo := floating(from, r.loc).Add(-36 * time.Hour)
	hi := floating(to, r.loc).Add(36 * time.Hour)
	var out []time.Time
	for _, w := range r.rr.Between(lo, hi, true) {
		t := Resolve(w, r.loc)
		if !t.After(from) || t.After(to) {
			continue
		}
		if r.end != nil && t.After(*r.end) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	dedup := out[:0]
	for i, t := range out {
		if i == 0 || !t.Equal(out[i-1]) {
			dedup = append(dedup, t)
		}
	}
	return dedup
}

// Exhausted reports whether no occurrence can fire after instant t. Unbounded rules are never exhausted
// unless an end instant has passed.
func (r *Rule) Exhausted(t time.Time) bool {
	if r.end != nil && !r.end.After(t) {
		return true
	}
	if !r.finite {
		return false
	}
	w := r.rr.After(floating(t, r.loc).Add(-36*time.Hour), false)
	for !w.IsZero() {
		inst := Resolve(w, r.loc)
		if inst.After(t) && (r.end == nil || !inst.After(*r.end)) {
			return false
		}
		if r.end != nil && inst.After(*r.end) {
			return true
		}
		w = r.rr.After(w, false)
	}
	return true
}

// Resolve turns a floating wall-clock time (UTC fields = local fields) into an instant in loc following the
// gap and overlap rules in the package comment.
func Resolve(w time.Time, loc *time.Location) time.Time {
	_, off1 := w.Add(-24 * time.Hour).In(loc).Zone() // offset about a day before (as an approximation of the instant)
	_, off2 := w.Add(24 * time.Hour).In(loc).Zone()
	u1 := w.Add(-time.Duration(off1) * time.Second)
	u2 := w.Add(-time.Duration(off2) * time.Second)
	ok1 := floating(u1, loc).Equal(w)
	ok2 := floating(u2, loc).Equal(w)
	switch {
	case ok1 && ok2:
		if u2.Before(u1) {
			return u2
		}
		return u1
	case ok1:
		return u1
	case ok2:
		return u2
	}
	// Gap: the local time does not exist. Fire at the instant the clocks change.
	lo, hi := w.Add(-24*time.Hour), w.Add(24*time.Hour)
	_, offLo := lo.In(loc).Zone()
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if _, o := mid.In(loc).Zone(); o == offLo {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi.Truncate(time.Second)
}
