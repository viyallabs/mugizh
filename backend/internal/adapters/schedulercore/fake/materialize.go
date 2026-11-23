package fake

import (
	"strconv"
	"strings"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Materialize turns the active activities into occurrences due in [from, to), as scheduler-core would, and cancels
// the pending occurrences of cancelled activities (end-to-end tests). Only the daily forms clinical creates are
// expanded ("FREQ=DAILY;BYHOUR=h1,h2;BYMINUTE=0;BYSECOND=0" between StartsAt and EndsAt, in the activity's zone);
// other rules are left alone. Calling it again adds only what is new.
func (c *Client) Materialize(from, to time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	have := map[string]bool{}
	for _, s := range c.occs {
		have[s.occ.ActivityID+"|"+s.occ.DueAt.UTC().Format(time.RFC3339)] = true
	}
	for _, r := range c.Items {
		a := c.byKey[r.IdempotencyKey]
		if c.State[a.ID] != "active" {
			for _, s := range c.occs {
				if s.occ.ActivityID == a.ID && s.occ.State == "pending" {
					s.occ.State = "cancelled"
				}
			}
			continue
		}
		hours, ok := dailyHours(r.RRule)
		loc, err := time.LoadLocation(r.TimeZone)
		if !ok || err != nil {
			continue
		}
		start := r.StartsAt.In(loc)
		for d := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
			for _, h := range hours {
				t := time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, loc).UTC()
				if t.Before(r.StartsAt) || t.Before(from) || !t.Before(to) || (r.EndsAt != nil && t.After(*r.EndsAt)) {
					continue
				}
				k := a.ID + "|" + t.Format(time.RFC3339)
				if have[k] {
					continue
				}
				have[k] = true
				c.occs = append(c.occs, &seeded{subject: r.SubjectRef, occ: schedulercore.Occurrence{ID: ids.New(), ActivityID: a.ID, ExternalRef: r.ExternalRef,
					TypeKey: r.TypeKey, TimeZone: r.TimeZone, DueAt: t, State: "pending"}})
			}
		}
	}
}

func dailyHours(rrule string) ([]int, bool) {
	parts := map[string]string{}
	for _, p := range strings.Split(rrule, ";") {
		k, v, _ := strings.Cut(p, "=")
		parts[k] = v
	}
	if parts["FREQ"] != "DAILY" || parts["BYHOUR"] == "" || parts["COUNT"] != "" || (parts["INTERVAL"] != "" && parts["INTERVAL"] != "1") {
		return nil, false
	}
	var out []int
	for _, h := range strings.Split(parts["BYHOUR"], ",") {
		n, err := strconv.Atoi(h)
		if err != nil || n < 0 || n > 23 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
