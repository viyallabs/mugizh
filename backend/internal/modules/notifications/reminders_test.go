package notifications_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// S-153 (calendar) and S-154 (actions), TC-40 (a dose taken stops the fallback).

func patientCtx(user string) context.Context {
	return ip.WithPrincipal(context.Background(), &ip.Principal{UserID: user, ActingRole: ip.RolePatient})
}

func problemOf(t *testing.T, err error) *problem.Problem {
	t.Helper()
	p, ok := err.(*problem.Problem)
	if !ok {
		t.Fatalf("want a problem, got %v", err)
	}
	return p
}

// seed adds an occurrence due `at` for a subject and returns its ID.
func (f *fx) seed(subject, typeKey string, at time.Time, ext string) string {
	id := ids.New()
	f.sched.AddOccurrence(subject, schedulercore.Occurrence{ID: id, ActivityID: ids.New(), ExternalRef: ext, TypeKey: schedulercore.TypeKey(typeKey),
		TimeZone: "Asia/Kolkata", DueAt: at, State: "pending"})
	return id
}

func TestS153OwnOccurrencesOnlyWithKindsStatesAndZone(t *testing.T) {
	f := setup(t)
	me, other := f.user(false), f.user(false)
	d := f.now // 2026-11-17 06:30Z
	a := f.seed(me, "med.dose", d.Add(time.Hour), "act-1")
	b := f.seed(me, "injection.dose", d.Add(2*time.Hour), "act-2")
	c := f.seed(me, "test.due", d.Add(3*time.Hour), "act-3")
	f.seed(me, "appointment", d.Add(4*time.Hour), "act-4") // appointments come from booking
	f.seed(other, "med.dose", d.Add(time.Hour), "act-5")
	f.sched.SetState(b, "acknowledged")
	got, err := f.m.Reminders(context.Background(), me, d, d.Add(24*time.Hour))
	if err != nil || len(got.Items) != 3 {
		t.Fatalf("%v %+v", err, got.Items)
	}
	if got.Items[0].ID != a || got.Items[0].Kind != "medicine" || got.Items[0].State != "due" || got.Items[0].TimeZone != "Asia/Kolkata" ||
		got.Items[1].ID != b || got.Items[1].Kind != "injection" || got.Items[1].State != "taken" || got.Items[2].ID != c || got.Items[2].Kind != "test" {
		t.Fatalf("%+v", got.Items)
	}
	if got.ETag == "" {
		t.Fatal("etag")
	}
	for _, it := range got.Items {
		if it.ID == "" || strings.Contains(it.Label, "act-") {
			t.Fatalf("%+v", it)
		}
	}
	// The other patient sees only their own, and the range is half open.
	theirs, _ := f.m.Reminders(context.Background(), other, d, d.Add(24*time.Hour))
	if len(theirs.Items) != 1 || theirs.Items[0].ID == a {
		t.Fatalf("%+v", theirs.Items)
	}
	short, _ := f.m.Reminders(context.Background(), me, d, d.Add(2*time.Hour))
	if len(short.Items) != 1 || short.Items[0].ID != a {
		t.Fatalf("%+v", short.Items)
	}
	// The scheduler was only ever asked about the caller.
	for _, l := range f.sched.Listed {
		if l.SubjectRef != me && l.SubjectRef != other {
			t.Fatalf("subject %s", l.SubjectRef)
		}
	}
}

func TestS153SnoozedMissedSkippedStates(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	d := f.now
	s := f.seed(me, "med.dose", d.Add(-time.Minute), "a")
	m := f.seed(me, "med.dose", d.Add(-2*time.Hour), "a")
	k := f.seed(me, "med.dose", d.Add(-3*time.Hour), "a")
	f.sched.SetState(m, "missed")
	if _, err := f.sched.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: s, SubjectRef: me, ClientActionID: ids.New(), Action: "snoozed", ActedAt: d}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sched.RecordAction(context.Background(), schedulercore.ActionRequest{OccurrenceID: k, SubjectRef: me, ClientActionID: ids.New(), Action: "skipped", ActedAt: d.Add(-170 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got, err := f.m.Reminders(context.Background(), me, d.Add(-4*time.Hour), d.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{s: "snoozed", m: "missed", k: "skipped"}
	for _, it := range got.Items {
		if it.State != want[it.ID] {
			t.Fatalf("%s is %s want %s", it.ID, it.State, want[it.ID])
		}
		if it.State == "snoozed" && (it.SnoozedUntil == nil || it.SnoozeCount != 1) {
			t.Fatalf("%+v", it)
		}
		if it.State == "skipped" && it.ActedAt == nil {
			t.Fatalf("%+v", it)
		}
	}
}

func TestS153RangeCapAndValidation(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	ctx := context.Background()
	d := f.now
	for name, r := range map[string][2]time.Time{
		"too long": {d, d.Add(31*24*time.Hour + time.Second)}, "reversed": {d, d.Add(-time.Hour)}, "empty": {d, d}, "zero": {{}, d},
	} {
		if _, err := f.m.Reminders(ctx, me, r[0], r[1]); problemOf(t, err).Code != "reminders.invalid_range" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := f.m.Reminders(ctx, me, d, d.Add(31*24*time.Hour)); err != nil {
		t.Fatalf("31 days is allowed: %v", err)
	}
	f.sched.FailList = errUnavailable()
	if _, err := f.m.Reminders(ctx, me, d, d.Add(time.Hour)); problemOf(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("scheduler down: %v", err)
	}
}

func TestS153WordingMedicineNameOnlyWithMedNamesInNotificationText(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	f.seed(me, "med.dose", f.now.Add(time.Hour), "a")
	get := func() httpapi.ReminderOccurrence {
		t.Helper()
		rem, err := f.m.Reminders(context.Background(), me, f.now, f.now.Add(24*time.Hour))
		if err != nil || len(rem.Items) != 1 {
			t.Fatalf("%v %+v", err, rem)
		}
		return httpapi.ReminderOccurrence{Label: rem.Items[0].Label, NotificationText: rem.Items[0].NotificationText}
	}
	// No P-MEDNAMES: the app label may name the medicine (own data, signed in); the notification text may not.
	r := get()
	if r.Label != "Metformin 500" || r.NotificationText != "Time for your medicine" {
		t.Fatalf("%+v", r)
	}
	// With consent the notification text names it too.
	f.gate.set(me, "P-MEDNAMES", true)
	if r = get(); r.NotificationText != "Metformin 500" {
		t.Fatalf("%+v", r)
	}
	// Discreet mode beats the consent.
	prefs, _ := f.m.Preferences(context.Background(), me)
	prefs.Discreet = true
	if err := f.m.SetPreferences(context.Background(), me, prefs); err != nil {
		t.Fatal(err)
	}
	if r = get(); r.NotificationText != "Time for your medicine" {
		t.Fatalf("%+v", r)
	}
	// A resolver that returns a condition: both texts fall back to the generic wording.
	f.res.neutral, f.res.named = "Time for your diabetes tablet", "diabetes tablet"
	if r = get(); r.NotificationText != "Time for your medicine" || r.Label != "Time for your medicine" {
		t.Fatalf("%+v", r)
	}
}

func TestS153ETagChangesWithContent(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	id := f.seed(me, "med.dose", f.now.Add(time.Hour), "a")
	h := httpapi.Handlers{M: f.m}
	call := func(inm *string) httpapi.ListMyRemindersResponseObject {
		t.Helper()
		r, err := h.ListMyReminders(patientCtx(me), httpapi.ListMyRemindersRequestObject{Params: httpapi.ListMyRemindersParams{From: f.now, To: f.now.Add(24 * time.Hour), IfNoneMatch: inm}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, ok := call(nil).(httpapi.ListMyReminders200JSONResponse)
	if !ok || first.Headers.ETag == nil || len(first.Body.Reminders) != 1 || first.Body.Reminders[0].Id.String() != id {
		t.Fatalf("%+v", first)
	}
	if _, ok := call(first.Headers.ETag).(httpapi.ListMyReminders304Response); !ok {
		t.Fatal("same tag must give 304")
	}
	f.sched.SetState(id, "missed")
	if _, ok := call(first.Headers.ETag).(httpapi.ListMyReminders200JSONResponse); !ok {
		t.Fatal("a changed calendar must give 200")
	}
	// Only patients; a doctor acting role is refused.
	doc := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: me, ActingRole: "doctor"})
	if _, err := h.ListMyReminders(doc, httpapi.ListMyRemindersRequestObject{}); problemOf(t, err).Status != http.StatusForbidden {
		t.Fatal("role")
	}
}

func (f *fx) act(t *testing.T, user, occ, action string, at time.Time, key string) (httpapi.ReminderActionResult, error) {
	t.Helper()
	if key == "" {
		key = ids.New()
	}
	res, err := f.m.RecordReminderAction(context.Background(), user, occ, notificationsAction(key, action, at))
	return httpapi.ReminderActionResult{State: httpapi.ReminderActionResultState(res.State), SnoozeCount: res.SnoozeCount}, err
}

func TestS154ActionsAreIdempotentAndAuditedOnce(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	occ := f.seed(me, "med.dose", f.now.Add(-5*time.Minute), "a")
	key := ids.New()
	r, err := f.act(t, me, occ, "taken", f.now.Add(-time.Minute), key)
	if err != nil || r.State != "taken" {
		t.Fatalf("%v %+v", err, r)
	}
	// The phone retries after losing the answer: same answer, still one audit event and one row.
	r, err = f.act(t, me, occ, "taken", f.now.Add(-time.Minute), key)
	if err != nil || r.State != "taken" {
		t.Fatalf("retry: %v %+v", err, r)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='reminders.action' AND result='allowed' AND actor_ref=$1 AND resource_ref=$2`, me, occ); n != 1 {
		t.Fatalf("audit %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.reminder_action WHERE user_ref=$1`, me); n != 1 {
		t.Fatalf("rows %d", n)
	}
	// The same key with other content is refused.
	if _, err := f.act(t, me, occ, "skipped", f.now.Add(-time.Minute), key); problemOf(t, err).Code != "idempotency.key_reused" {
		t.Fatalf("%v", err)
	}
}

func TestS154ConflictAndActingTimeBounds(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	occ := f.seed(me, "med.dose", f.now.Add(-5*time.Minute), "a")
	for name, at := range map[string]time.Time{"future": f.now.Add(time.Hour), "before window": f.now.Add(-2 * time.Hour), "too late": f.now.Add(-30 * time.Hour)} {
		_, err := f.act(t, me, occ, "taken", at, "")
		if p := problemOf(t, err); p.Code != "reminders.acted_at_out_of_range" || p.Status != 400 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := f.act(t, me, occ, "skipped", f.now, ""); err != nil {
		t.Fatal(err)
	}
	// First final action wins; a different one is a conflict, the same again is fine.
	if _, err := f.act(t, me, occ, "taken", f.now, ""); problemOf(t, err).Code != "reminders.action_conflict" {
		t.Fatalf("%v", err)
	}
	if _, err := f.act(t, me, occ, "skipped", f.now, ""); err != nil {
		t.Fatalf("same again: %v", err)
	}
	if _, err := f.act(t, me, occ, "snoozed", f.now, ""); problemOf(t, err).Code != "reminders.not_actionable" {
		t.Fatalf("snooze after final: %v", err)
	}
	if _, err := f.act(t, me, occ, "ate", f.now, ""); problemOf(t, err).Code != "reminders.invalid_action" {
		t.Fatalf("%v", err)
	}
}

func TestS154SnoozeOneAtATimeAndLimit(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	occ := f.seed(me, "med.dose", f.now.Add(-5*time.Minute), "a")
	f.sched.SetState(occ, "dispatched")
	r, err := f.m.RecordReminderAction(context.Background(), me, occ, notificationsAction(ids.New(), "snoozed", f.now))
	if err != nil || r.State != "snoozed" || r.SnoozedUntil == nil || !r.SnoozedUntil.After(f.now) || r.SnoozeCount != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := f.act(t, me, occ, "snoozed", f.now, ""); problemOf(t, err).Code != "reminders.snooze_active" {
		t.Fatalf("%v", err)
	}
	for i := 2; i <= 3; i++ {
		f.now = f.now.Add(20 * time.Minute)
		f.sched.SetState(occ, "dispatched")
		if _, err := f.act(t, me, occ, "snoozed", f.now, ""); err != nil {
			t.Fatalf("snooze %d: %v", i, err)
		}
	}
	f.now = f.now.Add(20 * time.Minute)
	f.sched.SetState(occ, "dispatched")
	if _, err := f.act(t, me, occ, "snoozed", f.now, ""); problemOf(t, err).Code != "reminders.snooze_limit" {
		t.Fatalf("%v", err)
	}
	// Snooze needs a delay in range.
	a := notificationsAction(ids.New(), "snoozed", f.now)
	a.SnoozeMinutes = 500
	if _, err := f.m.RecordReminderAction(context.Background(), me, occ, a); err == nil {
		t.Fatal("delay out of range must fail")
	}
}

func TestS154OthersOccurrenceIs404AndDeniedIsAudited(t *testing.T) {
	f := setup(t)
	me, other := f.user(false), f.user(false)
	theirs := f.seed(other, "med.dose", f.now.Add(-5*time.Minute), "a")
	for _, id := range []string{theirs, ids.New(), "not-a-uuid"} {
		_, err := f.act(t, me, id, "taken", f.now, "")
		if p := problemOf(t, err); p.Status != 404 {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if o, _ := f.sched.Occurrence(theirs); o.State == "acknowledged" {
		t.Fatal("someone else's occurrence changed")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='reminders.action' AND result='denied' AND actor_ref=$1`, me); n != 2 {
		t.Fatalf("denied audit %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.reminder_action`); n != 0 {
		t.Fatal("a refused action leaves no row")
	}
	// Audit rows hold references only.
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='reminders.action' AND (reason_code ~ ' ' OR purpose ~ ' ')`); n != 0 {
		t.Fatal("free text in audit")
	}
}

// TC-40: a dose marked taken after the push went out (and before the receipt wait ends) must not fall back to SMS.
func TestTC40TakenStopsSMSFallback(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	occ := ids.New()
	eid := ids.New()
	f.sched.AddOccurrence(u, schedulercore.Occurrence{ID: occ, ExternalRef: "act-1", TypeKey: "med.dose", TimeZone: "Asia/Kolkata", DueAt: f.now, State: "dispatched"})
	body := `{"occurrenceId":"` + occ + `","activityExternalRef":"act-1","typeKey":"medicine","dueAt":"` + f.now.Format(time.RFC3339) + `","subjectRef":"` + u + `","eventId":"` + eid + `"}`
	if rec := f.post(t, []byte(body), tsNow(f), eid, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("%d", rec.Code)
	}
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 1 {
		t.Fatal("push expected")
	}
	if _, err := f.act(t, u, occ, "taken", f.now, ""); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(10*time.Minute)) // well past the receipt wait
	if n := f.sms.Count(); n != 0 {
		t.Fatalf("SMS fallback sent for a dose already taken: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1 AND state='acted'`, u); n != 1 {
		t.Fatalf("acted %d", n)
	}
	// Control: without the action the fallback does go out.
	u2 := f.user(true)
	f.gate.set(u2, "P-REMIND", true)
	f.event(t, u2, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	f.run(t, f.now.Add(10*time.Minute))
	if n := f.sms.Count(); n != 1 {
		t.Fatalf("control: SMS fallback expected, got %d", n)
	}
}

func TestS154TakenBeforeSendDropsQueuedReminderAndSnoozeRenotifies(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	occ := ids.New()
	f.sched.AddOccurrence(u, schedulercore.Occurrence{ID: occ, ExternalRef: "act-1", TypeKey: "med.dose", TimeZone: "Asia/Kolkata", DueAt: f.now, State: "pending"})
	eid := ids.New()
	body := `{"occurrenceId":"` + occ + `","activityExternalRef":"act-1","typeKey":"medicine","dueAt":"` + f.now.Format(time.RFC3339) + `","subjectRef":"` + u + `","eventId":"` + eid + `"}`
	f.post(t, []byte(body), tsNow(f), eid, "")
	if _, err := f.act(t, u, occ, "taken", f.now, ""); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 0 {
		t.Fatal("a queued reminder for a dose already taken must not be sent")
	}

	// A snoozed re-notification carries notifyAt: it is sent (lateness counts from notifyAt) even though the
	// original due time is hours old.
	u2 := f.user(true)
	f.gate.set(u2, "P-REMIND", true)
	occ2, eid2 := ids.New(), ids.New()
	due := f.now.Add(-3 * time.Hour)
	body = `{"occurrenceId":"` + occ2 + `","activityExternalRef":"act-1","typeKey":"medicine","dueAt":"` + due.Format(time.RFC3339) + `","notifyAt":"` + f.now.Format(time.RFC3339) +
		`","subjectRef":"` + u2 + `","eventId":"` + eid2 + `"}`
	if rec := f.post(t, []byte(body), tsNow(f), eid2, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("%d", rec.Code)
	}
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 1 {
		t.Fatalf("snoozed re-notification not sent: %d", len(f.visible()))
	}
	// notifyAt before dueAt is a bad event.
	eid3 := ids.New()
	bad := `{"occurrenceId":"` + ids.New() + `","activityExternalRef":"act-1","typeKey":"medicine","dueAt":"` + f.now.Format(time.RFC3339) + `","notifyAt":"` + due.Format(time.RFC3339) +
		`","subjectRef":"` + u2 + `","eventId":"` + eid3 + `"}`
	if rec := f.post(t, []byte(bad), tsNow(f), eid3, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
}

func TestS154ErasureRemovesActionRows(t *testing.T) {
	f := setup(t)
	me := f.user(false)
	occ := f.seed(me, "med.dose", f.now.Add(-5*time.Minute), "a")
	if _, err := f.act(t, me, occ, "taken", f.now, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.m.EraseUser(context.Background(), me); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.reminder_action WHERE user_ref=$1`, me); n != 0 {
		t.Fatal("action rows must be erased")
	}
}

func errUnavailable() error { return adapters.E(adapters.Unavailable, "SCHEDULER_UNREACHABLE") }

func notificationsAction(key, action string, at time.Time) notifications.ReminderAction {
	return notifications.ReminderAction{ClientActionID: key, Action: action, ActedAt: at}
}

func tsNow(f *fx) string { return strconv.FormatInt(f.now.Unix(), 10) }
