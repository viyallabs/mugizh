package admin_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

func (f *fx) user() pub.User { return pub.User{Ref: ids.New(), Role: "patient"} }

func fbInput(msg string) pub.FeedbackInput {
	return pub.FeedbackInput{Kind: "bug", Message: msg, Screen: "booking.confirm", AppVersion: "1.2.3+45", Platform: "android"}
}

func (f *fx) fbCount(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// syntheticAadhaarLike builds a 12-digit number that passes the Verhoeff check (synthetic).
func syntheticVerhoeff12(t *testing.T) string {
	t.Helper()
	for c := 0; c < 10; c++ {
		s := "23456789012" + string(rune('0'+c))
		if scrub.Verhoeff(s) {
			return s
		}
	}
	t.Fatal("no check digit")
	return ""
}

func wantField(t *testing.T, err error, status int, field, code string) {
	t.Helper()
	var p *problem.Problem
	if !errors.As(err, &p) || p.Status != status {
		t.Fatalf("want problem %d, got %v", status, err)
	}
	if field == "" {
		return
	}
	for _, e := range p.Errors {
		if e.Field == field && e.Code == code {
			return
		}
	}
	t.Fatalf("want %s/%s in %+v", field, code, p.Errors)
}

// S-189: submit, list own, nobody else sees it, the message is sealed at rest, the audit event
// has no message, and diagnostics are absent unless sent.
func TestS189SubmitListSealedAndPrivate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u, other := f.user(), f.user()
	const msg = "The confirm button does nothing after I pick a slot"
	r, err := f.a.SubmitFeedback(ctx, u, fbInput(msg))
	if err != nil || !strings.HasPrefix(r.Reference, "FB-") || r.ID == "" {
		t.Fatalf("submit: %+v %v", r, err)
	}
	items, _, err := f.a.MyFeedback(ctx, u, 10, "")
	if err != nil || len(items) != 1 || items[0].Message != msg || items[0].Diagnostics != nil || items[0].Status != "new" || items[0].Platform != "android" {
		t.Fatalf("own list: %+v %v", items, err)
	}
	if got, _, _ := f.a.MyFeedback(ctx, other, 10, ""); len(got) != 0 {
		t.Fatalf("another user sees it: %+v", got)
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE position(convert_to($1,'UTF8') in message_sealed) > 0 OR position(convert_to('confirm button','UTF8') in message_sealed) > 0`, msg); n != 0 {
		t.Fatal("plaintext found in the table")
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE diagnostics IS NOT NULL`); n != 0 {
		t.Fatal("diagnostics stored without opt-in")
	}
	if n := f.auditCount(t, `action='admin.feedback_submitted' AND actor_ref='`+u.Ref+`'`); n != 1 {
		t.Fatalf("audit events: %d", n)
	}
	// With the opt-in the closed codes are kept and returned.
	in := fbInput("Crash on the receipt page")
	in.Diagnostics = &pub.FeedbackDiagnostics{OSVersion: "14", NetworkType: "wifi", LastErrorCode: "payments.timeout"}
	if _, err := f.a.SubmitFeedback(ctx, u, in); err != nil {
		t.Fatal(err)
	}
	items, _, _ = f.a.MyFeedback(ctx, u, 10, "")
	if len(items) != 2 || items[0].Diagnostics == nil || items[0].Diagnostics.NetworkType != "wifi" || items[0].Diagnostics.LastErrorCode != "payments.timeout" {
		t.Fatalf("diagnostics: %+v", items)
	}
}

// Paging newest first.
func TestS189CursorPaging(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(time.Minute)
		if _, err := f.a.SubmitFeedback(ctx, u, fbInput("note "+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	p1, next, err := f.a.MyFeedback(ctx, u, 2, "")
	if err != nil || len(p1) != 2 || next == "" || p1[0].Message != "note c" {
		t.Fatalf("page 1: %+v %q %v", p1, next, err)
	}
	p2, next, err := f.a.MyFeedback(ctx, u, 2, next)
	if err != nil || len(p2) != 1 || next != "" || p2[0].Message != "note a" {
		t.Fatalf("page 2: %+v %q %v", p2, next, err)
	}
	if _, _, err := f.a.MyFeedback(ctx, u, 2, "junk"); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
}

// Each refusal gives 422 with a field code and stores nothing (K-DP-05, K-DP-04).
func TestS189RefusedMessages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	cases := map[string]string{
		"contains_identifier":     "my number is " + syntheticVerhoeff12(t),
		"contains_contact_detail": "call me on 9876543210",
		"contains_link_or_markup": "see https://phish.example/login for the screenshot",
		"control_characters":      "hidden​chars",
		"too_long":                strings.Repeat("a", 2001),
		"required":                "   ",
	}
	for code, msg := range cases {
		_, err := f.a.SubmitFeedback(ctx, u, fbInput(msg))
		wantField(t, err, http.StatusUnprocessableEntity, "message", code)
	}
	_, err := f.a.SubmitFeedback(ctx, u, fbInput("write to me at someone@example.in"))
	wantField(t, err, http.StatusUnprocessableEntity, "message", "contains_contact_detail")
	_, err = f.a.SubmitFeedback(ctx, u, fbInput("a <b>bold</b> claim"))
	wantField(t, err, http.StatusUnprocessableEntity, "message", "contains_link_or_markup")

	bad := fbInput("fine")
	bad.Kind, bad.Screen, bad.Platform = "rant", "Bad Screen!", "symbian"
	bad.Diagnostics = &pub.FeedbackDiagnostics{OSVersion: "has spaces", NetworkType: "5g", LastErrorCode: "free text here"}
	_, err = f.a.SubmitFeedback(ctx, u, bad)
	for _, fld := range []string{"kind", "screen", "platform", "diagnostics.osVersion", "diagnostics.networkType", "diagnostics.lastErrorCode"} {
		wantField(t, err, http.StatusUnprocessableEntity, fld, "invalid")
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.feedback`); n != 0 {
		t.Fatalf("refused items were stored: %d", n)
	}
}

// At most 10 a day per person; another person and the next day are unaffected.
func TestS189DailyLimit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	for i := 0; i < 10; i++ {
		if _, err := f.a.SubmitFeedback(ctx, u, fbInput("item")); err != nil {
			t.Fatalf("item %d: %v", i+1, err)
		}
	}
	_, err := f.a.SubmitFeedback(ctx, u, fbInput("item"))
	wantField(t, err, http.StatusTooManyRequests, "", "")
	if _, err := f.a.SubmitFeedback(ctx, f.user(), fbInput("item")); err != nil {
		t.Fatalf("another person: %v", err)
	}
	f.now = f.now.Add(25 * time.Hour)
	if _, err := f.a.SubmitFeedback(ctx, u, fbInput("item")); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

// Triage: support only, no message in the list, status change audited, closed is final.
func TestS189TriageListAndStatus(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	r, err := f.a.SubmitFeedback(ctx, u, fbInput("secret words here"))
	if err != nil {
		t.Fatal(err)
	}
	support, ops := f.staff("support"), f.staff("ops")
	if _, _, err := f.a.FeedbackQueue(ctx, ops, "", 10); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("ops: %v", err)
	}
	if _, _, err := f.a.FeedbackQueue(ctx, pub.Staff{Ref: u.Ref, Role: "support"}, "", 10); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("a user who does not hold the role: %v", err)
	}
	q, _, err := f.a.FeedbackQueue(ctx, support, "", 10)
	if err != nil || len(q) != 1 || q[0].ID != r.ID || q[0].Kind != "bug" || q[0].Reference != r.Reference || q[0].Status != "new" {
		t.Fatalf("queue: %+v %v", q, err)
	}
	if err := f.a.SetFeedbackStatus(ctx, ops, r.ID, "triaged"); !errors.Is(err, pub.ErrForbidden) {
		t.Fatalf("ops status: %v", err)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, r.ID, "new"); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("status new: %v", err)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, ids.New(), "triaged"); !errors.Is(err, pub.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, r.ID, "triaged"); err != nil {
		t.Fatal(err)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, r.ID, "triaged"); err != nil { // same status: no change
		t.Fatal(err)
	}
	if n := f.auditCount(t, `action='admin.feedback_status_set' AND actor_ref='`+support.Ref+`' AND resource_ref='`+r.ID+`'`); n != 1 {
		t.Fatalf("status audit events: %d", n)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, r.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	if err := f.a.SetFeedbackStatus(ctx, support, r.ID, "triaged"); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("closed is final: %v", err)
	}
	mine, _, _ := f.a.MyFeedback(ctx, u, 10, "")
	if len(mine) != 1 || mine[0].Status != "closed" {
		t.Fatalf("the user sees the status: %+v", mine)
	}
}

// K-DP-07: step 1 deletes the subject's feedback (and only theirs), idempotent on double
// delivery, step 2 sweeps what arrived since; cancel is a no-op. Retention purge after 1 year.
func TestKDP07FeedbackErasureAndPurge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u, other := f.user(), f.user()
	for _, x := range []pub.User{u, u, other} {
		if _, err := f.a.SubmitFeedback(ctx, x, fbInput("hello")); err != nil {
			t.Fatal(err)
		}
	}
	req := ids.New()
	ev := func(typ string) outbox.Event {
		return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: u.Ref, Payload: map[string]string{"requestRef": req, "subjectRef": u.Ref}}
	}
	step1 := ev("rights.erasure_requested")
	for i := 0; i < 2; i++ {
		if err := f.a.OnErasureRequested(ctx, step1); err != nil {
			t.Fatal(err)
		}
	}
	if f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE user_ref=$1`, u.Ref) != 0 || f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE user_ref=$1`, other.Ref) != 1 {
		t.Fatal("step 1 deletes the subject's feedback only")
	}
	if err := f.a.OnErasureCancelled(ctx, ev("rights.erasure_cancelled")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.SubmitFeedback(ctx, u, fbInput("late")); err != nil {
		t.Fatal(err)
	}
	if err := f.a.OnErasureDue(ctx, ev("rights.erasure_due")); err != nil {
		t.Fatal(err)
	}
	if err := f.a.OnErasureDue(ctx, ev("rights.erasure_due")); err != nil { // nothing left: "none"
		t.Fatal(err)
	}
	if f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE user_ref=$1`, u.Ref) != 0 {
		t.Fatal("step 2 sweeps")
	}
	got := map[string]int{}
	rows, err := f.env.Owner.Query(ctx, `SELECT payload->>'outcome' FROM admin.event_outbox WHERE type='rights.task_done' AND payload->>'requestRef'=$1 AND payload->>'module'='admin'`, req)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var o string
		_ = rows.Scan(&o)
		got[o]++
	}
	rows.Close()
	if got["deleted"] != 1 || got["erased"] != 1 || got["none"] != 1 || len(got) != 3 {
		t.Fatalf("outcomes: %v", got)
	}

	// Retention: items older than a year go.
	f.now = f.now.Add(366 * 24 * time.Hour)
	n, err := f.a.PurgeFeedback(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
}

// Only grievances held: step 1 reports restricted under RET-09 (kept by the legal rule).
func TestKDP07ErasureWithOnlyGrievances(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	if _, err := f.a.OpenGrievance(ctx, u.Ref, "complaint text"); err != nil {
		t.Fatal(err)
	}
	req := ids.New()
	e := outbox.Event{ID: ids.New(), Type: "rights.erasure_requested", SubjectRef: u.Ref, Payload: map[string]string{"requestRef": req, "subjectRef": u.Ref}}
	if err := f.a.OnErasureRequested(ctx, e); err != nil {
		t.Fatal(err)
	}
	if f.fbCount(t, `SELECT count(*) FROM admin.event_outbox WHERE type='rights.task_done' AND payload->>'outcome'='restricted' AND payload->>'retentionRule'='RET-09'`) != 1 {
		t.Fatal("restricted RET-09 expected")
	}
	if f.fbCount(t, `SELECT count(*) FROM admin.grievance WHERE submitter_ref=$1`, u.Ref) != 1 {
		t.Fatal("the grievance is kept")
	}
}

// Review of S-189: numbers split by punctuation or line breaks, or written in Devanagari
// digits, are still refused; so are bare domains, and numbers in the code fields.
func TestS189ReviewFixesRefuseHiddenNumbersAndDomains(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := f.user()
	id := syntheticVerhoeff12(t)
	dev := map[rune]rune{'0': '०', '1': '१', '2': '२', '3': '३', '4': '४', '5': '५', '6': '६', '7': '७', '8': '८', '9': '९'}
	var devID []rune
	for _, r := range id {
		devID = append(devID, dev[r])
	}
	for _, msg := range []string{id[:4] + "\n" + id[4:8] + "\n" + id[8:], id[:4] + "." + id[4:8] + "." + id[8:], id[:4] + "/" + id[4:8] + "/" + id[8:], "id " + string(devID)} {
		_, err := f.a.SubmitFeedback(ctx, u, fbInput(msg))
		wantField(t, err, http.StatusUnprocessableEntity, "message", "contains_identifier")
	}
	for _, msg := range []string{"call 98765.43210", "call (98765)43210", "call 98765\n43210"} {
		_, err := f.a.SubmitFeedback(ctx, u, fbInput(msg))
		wantField(t, err, http.StatusUnprocessableEntity, "message", "contains_contact_detail")
	}
	_, err := f.a.SubmitFeedback(ctx, u, fbInput("go to evil.org now"))
	wantField(t, err, http.StatusUnprocessableEntity, "message", "contains_link_or_markup")
	in := fbInput("crash on the calendar")
	in.AppVersion = id
	_, err = f.a.SubmitFeedback(ctx, u, in)
	wantField(t, err, http.StatusUnprocessableEntity, "appVersion", "contains_identifier")
	if _, err := f.a.SubmitFeedback(ctx, u, fbInput("The app froze on 23.11.2026, 10.30 am, twice.")); err != nil {
		t.Fatalf("ordinary text with a date and time is accepted: %v", err)
	}
}

// Review of S-189: one rule for the erasure report. Feedback and grievances: restricted under
// RET-09 (the grievance decides). Nothing at all: none.
func TestS189ErasureOutcomeIsConsistent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	both, nothing := f.user(), f.user()
	if _, err := f.a.SubmitFeedback(ctx, both, fbInput("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.OpenGrievance(ctx, both.Ref, "complaint text"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []pub.User{both, nothing} {
		e := outbox.Event{ID: ids.New(), Type: "rights.erasure_requested", SubjectRef: u.Ref, Payload: map[string]string{"requestRef": ids.New(), "subjectRef": u.Ref}}
		if err := f.a.OnErasureRequested(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.event_outbox WHERE type='rights.task_done' AND subject_ref=$1 AND payload->>'outcome'='restricted' AND payload->>'retentionRule'='RET-09'`, both.Ref); n != 1 {
		t.Fatalf("feedback and grievance: restricted RET-09, got %d", n)
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.event_outbox WHERE type='rights.task_done' AND subject_ref=$1 AND payload->>'outcome'='none'`, nothing.Ref); n != 1 {
		t.Fatalf("nothing held: none, got %d", n)
	}
	if f.fbCount(t, `SELECT count(*) FROM admin.feedback WHERE user_ref=$1`, both.Ref) != 0 {
		t.Fatal("the feedback is deleted either way")
	}
}
