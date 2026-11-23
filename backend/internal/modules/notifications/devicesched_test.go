package notifications_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// S-165 (E-REM-02, TC-40): the on-device backup schedule for the next 72 hours, and the silent refresh push.

func key(at time.Time, critical bool) string {
	c := "n"
	if critical {
		c = "c"
	}
	return "rem-" + strconv.FormatInt(at.UTC().Unix()/60, 10) + "-" + c
}

func (f *fx) silent() []push.Message {
	var out []push.Message
	for _, m := range f.push.Sent() {
		if m.Silent {
			out = append(out, m)
		}
	}
	return out
}

func (f *fx) syncRow(t *testing.T, user string) (pending bool, attempts int, ok bool) {
	t.Helper()
	err := f.env.Owner.QueryRow(context.Background(), `SELECT pending, attempts FROM notifications.device_sync WHERE recipient_ref=$1`, user).Scan(&pending, &attempts)
	if err != nil {
		return false, 0, false
	}
	return pending, attempts, true
}

func defaultPrefs() notifications.Preferences {
	return notifications.Preferences{Channels: map[string][]notifications.Channel{}, QuietHoursEnabled: true, TimeZone: "Asia/Kolkata", Locale: "en"}
}

// The schedule holds exactly what the server would send in the next 72 hours, when it would send it.
func TestS165ScheduleMirrorsWhatTheServerSends(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	now := f.now // 2026-11-17 06:30Z, 12:00 in the patient's zone; quiet hours 21:00-08:00 there
	a := f.seed(me, "med.dose", now.Add(time.Hour), "act-a")
	b := f.seed(me, "med.dose", now.Add(time.Hour), "act-b") // same minute: one grouped notification (E-REM-03)
	h := f.seed(me, "injection.dose", now.Add(2*time.Hour), "act-h")
	c := f.seed(me, "med.dose", now.Add(71*time.Hour), "act-c")
	f.seed(me, "med.dose", now.Add(73*time.Hour), "act-d") // beyond 72 hours
	f.seed(me, "med.dose", now.Add(-2*time.Hour), "act-e") // already past
	for state, at := range map[string]time.Duration{"acknowledged": 5 * time.Hour, "cancelled": 6 * time.Hour, "missed": 7 * time.Hour, "failed": 8 * time.Hour} {
		f.sched.SetState(f.seed(me, "med.dose", now.Add(at), "act-"+state), state)
	}
	s := f.seed(me, "med.dose", now.Add(-10*time.Minute), "act-s") // due 10 minutes ago, snoozed now for 10 minutes
	if _, err := f.sched.RecordAction(ctx, schedulercore.ActionRequest{OccurrenceID: s, SubjectRef: me, ClientActionID: ids.New(), Action: "snoozed", ActedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Follow-up (a test, not critical): at 23:00 it would wait until 08:00, too late, so the server never sends it;
	// at 07:30 it waits until 08:00 and goes then.
	f.seed(me, "test.due", time.Date(2026, 11, 17, 17, 30, 0, 0, time.UTC), "act-q1")
	q2Due := time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)
	q2 := f.seed(me, "test.due", q2Due, "act-q2")
	f.seed(me, "appointment", now.Add(3*time.Hour), "act-ap") // appointments are not reminders here
	f.seed(f.user(true), "med.dose", now.Add(time.Hour), "act-other")

	got, err := f.m.DeviceSchedule(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemindersOn || got.Truncated || !got.GeneratedAt.Equal(now) || !got.ValidUntil.Equal(now.Add(72*time.Hour)) || !got.RefreshAfter.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("%+v", got)
	}
	type want struct {
		key    string
		fire   time.Time
		body   string
		screen string
		occs   []string
	}
	ab := []string{a, b}
	if b < a {
		ab = []string{b, a}
	}
	wants := []want{
		{key(now.Add(10*time.Minute), true), now.Add(10 * time.Minute), "Time for your medicine", "reminder", []string{s}},
		{key(now.Add(time.Hour), true), now.Add(time.Hour), "You have 2 reminders due now", "reminders", ab},
		{key(now.Add(2*time.Hour), true), now.Add(2 * time.Hour), "Time for your medicine", "reminder", []string{h}},
		{key(q2Due, false), time.Date(2026, 11, 18, 2, 30, 0, 0, time.UTC), "Time for your medicine", "reminder", []string{q2}},
		{key(now.Add(71*time.Hour), true), now.Add(71 * time.Hour), "Time for your medicine", "reminder", []string{c}},
	}
	if len(got.Entries) != len(wants) {
		t.Fatalf("entries: %+v", got.Entries)
	}
	for i, w := range wants {
		e := got.Entries[i]
		if e.ReplaceKey != w.key || !e.FireAt.Equal(w.fire) || e.Body != w.body || e.Screen != w.screen || e.TitleKey != "notif.title" ||
			strings.Join(e.OccurrenceIDs, ",") != strings.Join(w.occs, ",") {
			t.Fatalf("entry %d: %+v want %+v", i, e, w)
		}
		if e.FireAt.Before(now) || !e.FireAt.Before(got.ValidUntil) {
			t.Fatalf("entry %d outside the window", i)
		}
	}
	// The scheduler was asked only about the caller, far enough back to see a dose snoozed late (24 hours).
	for _, l := range f.sched.Listed {
		if l.SubjectRef != me || l.From.After(now.Add(-24*time.Hour)) || !l.To.Equal(now.Add(72*time.Hour)) {
			t.Fatalf("listed %+v", l)
		}
	}
}

// The push the server sends for a minute carries the replace key of the backup entry for that minute, so an
// online phone replaces one with the other instead of showing both.
func TestS165ServerPushCarriesTheSameReplaceKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	single, grouped, follow := f.now.Add(time.Hour), f.now.Add(2*time.Hour), f.now.Add(6*time.Hour) // 18:00 local: not quiet
	f.seed(me, "med.dose", single, "act-1")
	f.seed(me, "med.dose", grouped, "act-2")
	f.seed(me, "med.dose", grouped, "act-3")
	f.seed(me, "test.due", follow, "act-4")
	sch, err := f.m.DeviceSchedule(ctx, me)
	if err != nil || len(sch.Entries) != 3 {
		t.Fatalf("%v %+v", err, sch.Entries)
	}
	send := func(typeKey string, due time.Time, n int) push.Message {
		t.Helper()
		f.now = due
		for i := 0; i < n; i++ {
			if rec, _ := f.event(t, me, typeKey, due); rec.Code != http.StatusAccepted {
				t.Fatalf("event %d", rec.Code)
			}
		}
		before := len(f.visible())
		f.run(t, due)
		v := f.visible()
		if len(v) != before+1 {
			t.Fatalf("one push expected, got %d", len(v)-before)
		}
		return v[len(v)-1]
	}
	for i, p := range []push.Message{send("med.dose", single, 1), send("med.dose", grouped, 2), send("test.due", follow, 1)} {
		if p.ReplaceKey == "" || p.ReplaceKey != sch.Entries[i].ReplaceKey || p.Body != sch.Entries[i].Body {
			t.Fatalf("push %d: key %q body %q, schedule %+v", i, p.ReplaceKey, p.Body, sch.Entries[i])
		}
	}
	if !strings.HasSuffix(sch.Entries[2].ReplaceKey, "-n") || !strings.HasSuffix(sch.Entries[0].ReplaceKey, "-c") {
		t.Fatalf("classes: %+v", sch.Entries)
	}
}

// K-DP-09: the backup names a medicine only with P-MEDNAMES and discreet mode off, never denylisted text.
func TestS165WordingFollowsMedNamesAndDiscreet(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	f.seed(me, "med.dose", f.now.Add(time.Hour), "act-1")
	body := func() string {
		t.Helper()
		s, err := f.m.DeviceSchedule(ctx, me)
		if err != nil || len(s.Entries) != 1 {
			t.Fatalf("%v %+v", err, s.Entries)
		}
		return s.Entries[0].Body
	}
	if b := body(); b != "Time for your medicine" || f.res.allowed[len(f.res.allowed)-1] {
		t.Fatalf("without P-MEDNAMES: %q (resolver asked for names: %v)", b, f.res.allowed)
	}
	f.gate.set(me, notifications.PurposeMedNames, true)
	if b := body(); b != "Metformin 500" {
		t.Fatalf("with P-MEDNAMES: %q", b)
	}
	p := defaultPrefs()
	p.Discreet = true
	if err := f.m.SetPreferences(ctx, me, p); err != nil {
		t.Fatal(err)
	}
	if b := body(); b != "Time for your medicine" {
		t.Fatalf("discreet: %q", b)
	}
	if err := f.m.SetPreferences(ctx, me, defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	f.res.named = "Diabetes tablets"
	if b := body(); b != "Time for your medicine" {
		t.Fatalf("denylisted named text must fall back to neutral: %q", b)
	}
}

// Without P-REMIND the device holds nothing; a failed consent check never yields a schedule.
func TestS165NoRemindConsentMeansEmptyAndFailedCheckIs503(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.seed(me, "med.dose", f.now.Add(time.Hour), "act-1")
	s, err := f.m.DeviceSchedule(ctx, me)
	if err != nil || s.RemindersOn || len(s.Entries) != 0 || s.ETag == "" {
		t.Fatalf("%v %+v", err, s)
	}
	if len(f.sched.Listed) != 0 {
		t.Fatal("without consent the scheduler is not even asked")
	}
	f.gate.err = errors.New("records down")
	if _, err := f.m.DeviceSchedule(ctx, me); problemOf(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("%v", err)
	}
	f.gate.err = nil
	f.gate.set(me, notifications.PurposeRemind, true)
	f.sched.FailList = adapters.E(adapters.Unavailable, "DOWN")
	if _, err := f.m.DeviceSchedule(ctx, me); problemOf(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("%v", err)
	}
}

// At most 60 entries (iOS keeps 64 pending local notifications per app); the app is told to refresh before the
// last one fires.
func TestS165TruncatesAndRefreshesBeforeTheLastEntry(t *testing.T) {
	f := setup(t)
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	for i := 1; i <= 70; i++ {
		f.seed(me, "med.dose", f.now.Add(time.Duration(i)*10*time.Minute), "act-"+strconv.Itoa(i))
	}
	s, err := f.m.DeviceSchedule(context.Background(), me)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != notifications.MaxDeviceScheduleEntries || !s.Truncated || !s.RefreshAfter.Equal(s.Entries[59].FireAt) {
		t.Fatalf("%d entries, truncated %v, refresh %v", len(s.Entries), s.Truncated, s.RefreshAfter)
	}
}

// The ETag is stable while nothing changes and changes when a dose is taken; only the patient may ask.
func TestS165ETagAndHandler(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	occ := f.seed(me, "med.dose", f.now.Add(time.Hour), "act-1")
	f.seed(me, "med.dose", f.now.Add(3*time.Hour), "act-2")
	h := httpapi.Handlers{M: f.m}
	res, err := h.GetMyReminderDeviceSchedule(patientCtx(me), httpapi.GetMyReminderDeviceScheduleRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(httpapi.GetMyReminderDeviceSchedule200JSONResponse)
	if !isOK || ok.Headers.ETag == nil || len(ok.Body.Entries) != 2 || len(ok.Body.Entries[0].OccurrenceIds) != 1 || ok.Body.Entries[0].OccurrenceIds[0].String() != occ {
		t.Fatalf("%T %+v", res, res)
	}
	etag := *ok.Headers.ETag
	res, err = h.GetMyReminderDeviceSchedule(patientCtx(me), httpapi.GetMyReminderDeviceScheduleRequestObject{Params: httpapi.GetMyReminderDeviceScheduleParams{IfNoneMatch: &etag}})
	if _, is304 := res.(httpapi.GetMyReminderDeviceSchedule304Response); err != nil || !is304 {
		t.Fatalf("same schedule: %T %v", res, err)
	}
	if _, err := f.m.RecordReminderAction(ctx, me, occ, notifications.ReminderAction{ClientActionID: ids.New(), Action: "taken", ActedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	res, _ = h.GetMyReminderDeviceSchedule(patientCtx(me), httpapi.GetMyReminderDeviceScheduleRequestObject{Params: httpapi.GetMyReminderDeviceScheduleParams{IfNoneMatch: &etag}})
	if changed, isOK := res.(httpapi.GetMyReminderDeviceSchedule200JSONResponse); !isOK || *changed.Headers.ETag == etag || len(changed.Body.Entries) != 1 {
		t.Fatalf("after taking a dose: %T %+v", res, res)
	}
	doctor := ip.WithPrincipal(ctx, &ip.Principal{UserID: me, ActingRole: ip.RoleDoctor})
	if _, err := h.GetMyReminderDeviceSchedule(doctor, httpapi.GetMyReminderDeviceScheduleRequestObject{}); problemOf(t, err).Status != http.StatusForbidden {
		t.Fatalf("%v", err)
	}
}

// Changes that shape the schedule send one silent, content-free push to the patient's devices; requests merge and
// are spaced; consent changes for other purposes are ignored; erasure removes the row.
func TestS165SilentRefreshPush(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	t0 := f.now
	if err := f.m.SetPreferences(ctx, me, defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.SetPreferences(ctx, me, defaultPrefs()); err != nil { // merges with the first
		t.Fatal(err)
	}
	f.run(t, t0)
	sl := f.silent()
	if len(sl) != 1 || len(f.visible()) != 0 {
		t.Fatalf("one silent push: %d silent, %d visible", len(sl), len(f.visible()))
	}
	m := sl[0]
	if m.TitleKey != "" || m.Body != "" || len(m.Data) != 1 || m.Data["sync"] != "reminders" || m.ReplaceKey != "" ||
		strings.Contains(m.IdempotencyKey, me) || m.Target.Token != "tok-"+me {
		t.Fatalf("%+v", m)
	}
	if pending, _, ok := f.syncRow(t, me); !ok || pending {
		t.Fatal("finished")
	}

	// A second request within 30 seconds waits for the interval.
	f.now = t0.Add(5 * time.Second)
	if err := f.m.SetPreferences(ctx, me, defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	f.run(t, t0.Add(6*time.Second))
	if len(f.silent()) != 1 {
		t.Fatal("spaced at least 30 seconds apart")
	}
	f.run(t, t0.Add(31*time.Second))
	if len(f.silent()) != 2 {
		t.Fatal("sent after the interval")
	}

	// Consent: only P-REMIND and P-MEDNAMES matter; a redelivered event is handled once.
	f.now = t0.Add(10 * time.Minute)
	for _, e := range []outbox.Event{
		{ID: ids.New(), Type: "consent.revoked", SubjectRef: me, Payload: map[string]string{"purposeId": "P-NEWS"}},
		{ID: ids.New(), Type: "consent.granted", SubjectRef: me, Payload: map[string]string{"grantId": ids.New(), "tier": "T3"}},
		{ID: ids.New(), Type: "consent.revoked", SubjectRef: "not-a-uuid", Payload: map[string]string{"purposeId": "P-MEDNAMES"}},
	} {
		if err := f.m.OnConsentChanged(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if pending, _, _ := f.syncRow(t, me); pending {
		t.Fatal("other purposes do not touch the device schedule")
	}
	medNames := outbox.Event{ID: ids.New(), Type: "consent.revoked", SubjectRef: me, Payload: map[string]string{"purposeId": "P-MEDNAMES"}}
	if err := f.m.OnConsentChanged(ctx, medNames); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now)
	if len(f.silent()) != 3 {
		t.Fatal("P-MEDNAMES withdrawal reaches the devices")
	}
	f.now = f.now.Add(time.Minute)
	if err := f.m.OnConsentChanged(ctx, medNames); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := f.syncRow(t, me); pending {
		t.Fatal("a redelivered event is handled once")
	}

	// Clinical changed the reminders; a dose was taken on another device.
	if err := f.m.OnRemindersChanged(ctx, outbox.Event{ID: ids.New(), Type: "reminders.changed", SubjectRef: me}); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := f.syncRow(t, me); !pending {
		t.Fatal("reminders.changed")
	}
	f.run(t, f.now)
	f.now = f.now.Add(time.Minute)
	occ := f.seed(me, "med.dose", f.now, "act-1")
	if _, err := f.m.RecordReminderAction(ctx, me, occ, notifications.ReminderAction{ClientActionID: ids.New(), Action: "taken", ActedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := f.syncRow(t, me); !pending {
		t.Fatal("dose action")
	}
	f.run(t, f.now)
	if len(f.silent()) != 5 {
		t.Fatalf("silent pushes: %d", len(f.silent()))
	}

	// A dead token is removed; transient failures retry, then the hint is dropped.
	f.now = f.now.Add(time.Minute)
	f.push.InvalidTokens = map[string]bool{"tok-" + me: true}
	_ = f.m.SetPreferences(ctx, me, defaultPrefs())
	f.run(t, f.now)
	if len(f.tg.invalidated) != 1 {
		t.Fatal("dead token removed")
	}
	if pending, _, _ := f.syncRow(t, me); pending {
		t.Fatal("nothing left to retry")
	}
	f.push.InvalidTokens = nil
	f.push.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	f.now = f.now.Add(time.Minute)
	_ = f.m.SetPreferences(ctx, me, defaultPrefs())
	for i := 0; i < 5; i++ {
		f.run(t, f.now.Add(time.Duration(i)*2*time.Minute))
		pending, att, _ := f.syncRow(t, me)
		if i < 4 && (!pending || att != i+1) {
			t.Fatalf("attempt %d: pending %v attempts %d", i+1, pending, att)
		}
		if i == 4 && pending {
			t.Fatal("dropped after five attempts")
		}
	}
	f.push.FailWith = nil

	// Purge keeps a finished row a day; erasure removes it at once.
	if _, err := f.m.PurgeDeliveryLogs(ctx, f.now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.syncRow(t, me); ok {
		t.Fatal("finished row purged after a day")
	}
	_ = f.m.SetPreferences(ctx, me, defaultPrefs())
	if err := f.m.EraseUser(ctx, me); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := f.syncRow(t, me); ok {
		t.Fatal("erasure removes the row")
	}
}

// TC-40: the phone is offline for 48 hours. The schedule it fetched before going offline already holds every
// reminder of those 48 hours, at the time the server would have sent it; meanwhile the server's pushes get no
// receipt, so every critical one also goes by SMS, and the non-critical follow-up does not (no SMS permission).
func TestTC40OfflineFor48HoursBackupCoversItAndCriticalFallsBackToSMS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	f.gate.set(me, notifications.PurposeRemind, true)
	t0 := f.now
	type due struct {
		typeKey string
		at      time.Time
	}
	var dues []due
	for i := 0; i < 6; i++ { // 13:30, 21:30 and 05:30 local: med reminders are never held for quiet hours
		dues = append(dues, due{"med.dose", t0.Add(time.Hour + time.Duration(i)*8*time.Hour)})
	}
	dues = append(dues, due{"test.due", t0.Add(30 * time.Hour)}) // 18:00 local the next day
	for i, d := range dues {
		f.seed(me, d.typeKey, d.at, "act-"+strconv.Itoa(i))
	}
	sch, err := f.m.DeviceSchedule(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	fires := map[time.Time]bool{}
	for _, e := range sch.Entries {
		fires[e.FireAt] = true
	}
	for _, d := range dues {
		if !fires[d.at] {
			t.Fatalf("the backup schedule misses the reminder at %v", d.at)
		}
	}
	// Offline: pushes go out, no receipt comes back.
	for _, d := range dues {
		f.now = d.at
		if rec, _ := f.event(t, me, d.typeKey, d.at); rec.Code != http.StatusAccepted {
			t.Fatalf("event %d", rec.Code)
		}
		f.run(t, d.at)
		f.run(t, d.at.Add(6*time.Minute))
		f.run(t, d.at.Add(6*time.Minute+time.Second))
	}
	if len(f.visible()) != len(dues) {
		t.Fatalf("pushes: %d", len(f.visible()))
	}
	if f.sms.Count() != 6 {
		t.Fatalf("every critical reminder falls back to SMS, the follow-up does not: %d", f.sms.Count())
	}
}

// A device signed out remotely gets one silent "wipe" push, then its token is dropped; a transient failure is retried;
// an unknown device or a malformed event does nothing.
func TestS165RemoteSignOutTellsTheLostPhoneToWipe(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	lost := ids.New()
	f.tg.revoked = map[string]public.PushTarget{me + "|" + lost: {DeviceID: lost, Platform: "ios", Token: "lost-phone-token"}}
	ev := outbox.Event{ID: ids.New(), Type: ip.EventDeviceRevoked, SubjectRef: me, Payload: map[string]string{"deviceRef": lost}}

	f.push.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	if err := f.m.OnDeviceRevoked(ctx, ev); err == nil {
		t.Fatal("a transient failure must be retried")
	}
	if len(f.tg.invalidated) != 0 {
		t.Fatal("the token is kept until the device was told")
	}
	f.push.FailWith = nil
	if err := f.m.OnDeviceRevoked(ctx, ev); err != nil {
		t.Fatal(err)
	}
	sl := f.silent()
	if len(sl) != 1 || sl[0].Target.Token != "lost-phone-token" || sl[0].Data["signout"] != lost || len(sl[0].Data) != 1 || sl[0].Body != "" {
		t.Fatalf("%+v", sl)
	}
	if len(f.tg.invalidated) != 1 || f.tg.invalidated[0] != lost {
		t.Fatalf("token dropped: %v", f.tg.invalidated)
	}
	if err := f.m.OnDeviceRevoked(ctx, ev); err != nil || len(f.silent()) != 1 {
		t.Fatal("a redelivered event is handled once")
	}
	for _, e := range []outbox.Event{
		{ID: ids.New(), Type: ip.EventDeviceRevoked, SubjectRef: me, Payload: map[string]string{"deviceRef": ids.New()}},
		{ID: ids.New(), Type: ip.EventDeviceRevoked, SubjectRef: me, Payload: map[string]string{"deviceRef": "x"}},
		{ID: ids.New(), Type: ip.EventDeviceRevoked, SubjectRef: "x", Payload: map[string]string{"deviceRef": lost}},
	} {
		if err := f.m.OnDeviceRevoked(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.silent()) != 1 {
		t.Fatal("nothing for unknown devices or malformed events")
	}
}

// D-73: a patient message reaches only the patient-app device, a professional message only the pro-app device, and
// the reminder refresh hint only the patient app; a reminder template for another audience is refused at start.
func TestD73PushGoesOnlyToTheAudiencesApp(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	me := f.user(true)
	patientDev := f.tg.by[me][0].DeviceID
	proDev := ids.New()
	f.tg.by[me] = append(f.tg.by[me], public.PushTarget{DeviceID: proDev, Platform: "android", Token: "pro-tok-" + me})
	f.tg.appOf = map[string]string{patientDev: "patient", proDev: "pro"}
	if _, err := f.m.Send(ctx, booking(me, "b-1")); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now)
	v := f.visible()
	if len(v) != 1 || v[0].Target.Token != "tok-"+me {
		t.Fatalf("patient message to the patient app only: %+v", v)
	}
	if err := f.m.SetPreferences(ctx, me, defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now)
	if s := f.silent(); len(s) != 1 || s[0].Target.Token != "tok-"+me {
		t.Fatalf("refresh hint to the patient app only: %+v", s)
	}

	reg, _ := sms.ParseRegistry([]byte(registryYAML))
	cfg := config(reg)
	cfg.Templates = append(cfg.Templates, notifications.Template{Key: "share.decided", MessageType: "sharing", Category: public.Service, Priority: public.Normal,
		Channels: []notifications.Channel{notifications.Push}, TitleKey: "notif.title", BodyKey: "notif.security.body", LinkScreen: "sharing",
		Audience: notifications.AudiencePro})
	f.build(t, cfg)
	if _, err := f.m.Send(ctx, public.Request{TemplateKey: "share.decided", RecipientRef: me, Reason: "sharing", DedupeKey: "s-1"}); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(time.Minute))
	v = f.visible()
	if len(v) != 2 || v[1].Target.Token != "pro-tok-"+me {
		t.Fatalf("professional message to the pro app only: %+v", v)
	}

	for _, bad := range []notifications.Template{
		{Key: "reminder.pro", MessageType: "reminder", Category: public.Service, Priority: public.Critical, Reminder: true, Channels: []notifications.Channel{notifications.Push},
			TitleKey: "notif.title", BodyKey: "notif.reminder.body", LinkScreen: "reminder", Audience: notifications.AudiencePro},
		{Key: "odd.audience", MessageType: "booking", Category: public.Service, Priority: public.Normal, Channels: []notifications.Channel{notifications.Push},
			TitleKey: "notif.title", BodyKey: "notif.security.body", LinkScreen: "x", Audience: "nurse"},
	} {
		c := config(reg)
		c.Templates = append(c.Templates, bad)
		if _, err := notifications.New(f.env.Pools.For("notifications"), c, notifications.Deps{Gate: f.gate, Contacts: f.phones, Push: f.tg, Catalog: cat{}},
			slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Fatalf("template %s must be refused", bad.Key)
		}
	}
}
