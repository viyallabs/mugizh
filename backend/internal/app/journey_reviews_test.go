package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	videofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// TestJourneyReviewsXX runs the review journey (S-209 backend, S-214) on the synthetic test country XX, whose
// pack turns the "reviews" flag on: a patient completes an online visit (the consult outcome completes the
// appointment and booking emits appointment.completed), reviews the doctor through HTTP, and the public rating
// shows the count and the Bayesian average without any text. Another patient cannot review the visit (TC-51),
// a second review is refused, and withdrawing the review empties the rating.
func TestJourneyReviewsXX(t *testing.T) {
	j := newJourneyIn(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), "XX")
	zone, err := time.LoadLocation(j.c.Config.Pack().Locale().DefaultTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	j.at(time.Date(2026, 11, 23, 9, 0, 0, 0, zone))
	ctx := context.Background()
	legal := j.c.Config.Pack().Legal()
	if !j.c.Config.Flag("reviews") || j.c.ReviewsRoutes() == nil {
		t.Fatal("the XX pack turns reviews on")
	}

	doc := j.pro("+351912340101")
	j.verifyDoctor(doc, "Ana Sousa", "XX/101")
	_, practice, vendor := j.clinicWithPractice(doc)
	pat := j.patient("+351912340201", "push-token-synthetic-xx-reviews-01")
	appt, start := j.bookAndPay(pat, practice, vendor, j.clock())
	other := j.signIn("+351912340202", ip.AppPatient, "")

	// Not completed yet: my own appointment is 409, someone else's is 404 (TC-51, E-REV-01).
	if r := j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", pat.token, map[string]any{"rating": 5}); r.status != http.StatusConflict || r.str("code") != "review.not_eligible" {
		t.Fatalf("review before the visit: %d %s", r.status, r.raw)
	}
	if r := j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", other.token, map[string]any{"rating": 5}); r.status != http.StatusNotFound {
		t.Fatalf("someone else's appointment: %d %s", r.status, r.raw)
	}

	// ---- the video consultation, completed by the doctor ----
	j.at(start.Add(-2 * time.Minute))
	j.refresh(&pat)
	j.proBack(&doc)
	cs := j.must(j.call(http.MethodGet, "/v1/appointments/"+appt+"/consult", pat.token, nil), http.StatusOK, "consult")
	consultID := cs.str("consultId")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/pre-join", pat.token, map[string]any{
		"consent":     map[string]any{"textId": "TELE-CONSENT", "textVersion": legal.ConsentTextVersions["TELE-CONSENT"], "inIndiaNow": true},
		"deviceCheck": map[string]any{"cameraOk": true, "microphoneOk": true, "networkOk": true}}), http.StatusOK, "pre-join")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/join", pat.token, nil), http.StatusOK, "patient join")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/join", doc.token, nil), http.StatusOK, "doctor join")
	room := j.sql("consult", `SELECT provider_room_ref FROM session WHERE id=$1`, consultID)
	vid := j.c.Video.(*videofake.Client)
	for _, role := range []string{"patient", "doctor"} {
		ref := j.sql("consult", `SELECT participant_ref::text FROM participant WHERE session_id=$1 AND role=$2`, consultID, role)
		vid.Simulate(room, ref, video.Joined, j.clock())
	}
	if _, err := j.c.Consult.SyncOpenSessions(ctx, j.clock()); err != nil {
		t.Fatal(err)
	}
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/identity-confirmation", doc.token, map[string]any{"nameConfirmed": true, "ageConfirmed": true}),
		http.StatusOK, "identity confirmation")
	j.at(start.Add(15 * time.Minute))
	j.refresh(&pat)
	j.refresh(&doc)
	j.refresh(&other)
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/outcome", doc.token, map[string]any{"outcome": "completed"}), http.StatusOK, "complete")
	j.pump() // booking completes the appointment and emits appointment.completed; reviews builds its read model
	if got := j.sql("reviews", `SELECT patient_ref::text FROM visit WHERE appointment_ref=$1`, appt); got != pat.id {
		t.Fatalf("the completed visit is in the reviews read model: %q", got)
	}

	// ---- the public rating before any review: the count is shown, the rating is null ----
	r0 := j.must(j.call(http.MethodGet, "/v1/doctors/"+doc.id+"/rating", "", nil), http.StatusOK, "public rating, no reviews")
	if j.num(r0, "count") != 0 {
		t.Fatalf("no reviews yet: %s", r0.raw)
	}
	if v, present := r0.body["rating"]; !present || v != nil {
		t.Fatalf("rating is null with no reviews: %s", r0.raw)
	}

	// ---- the review ----
	const text = "Clear explanation and a short wait."
	if r := j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", other.token, map[string]any{"rating": 1}); r.status != http.StatusNotFound {
		t.Fatalf("another patient cannot review this visit: %d %s", r.status, r.raw)
	}
	if r := j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", pat.token, map[string]any{"rating": 5, "text": "call me on 351 912 340 201"}); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a phone number in the text is refused: %d %s", r.status, r.raw)
	}
	rv := j.must(j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", pat.token, map[string]any{"rating": 5, "text": text}), http.StatusCreated, "review")
	if rv.str("textState") != "pending_moderation" || rv.str("text") != text || rv.str("doctorId") != doc.id || j.num(rv, "rating") != 5 {
		t.Fatalf("the author's view: %s", rv.raw)
	}
	if r := j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/review", pat.token, map[string]any{"rating": 4}); r.status != http.StatusConflict || r.str("code") != "review.already_reviewed" {
		t.Fatalf("second review: %d %s", r.status, r.raw)
	}

	// The star rating counts at once: (10 x 3.0 + 5) / (10 + 1) = 3.18, shown as 3.2. No text is public.
	pub := j.must(j.call(http.MethodGet, "/v1/doctors/"+doc.id+"/rating", "", nil), http.StatusOK, "public rating")
	if j.num(pub, "count") != 1 || j.num(pub, "rating") != 3.2 {
		t.Fatalf("public rating: %s", pub.raw)
	}
	if strings.Contains(string(pub.raw), "Clear") || strings.Contains(string(pub.raw), "text") {
		t.Fatalf("the public rating never carries review text: %s", pub.raw)
	}
	// Only the author's list shows the text.
	mine := j.must(j.call(http.MethodGet, "/v1/me/reviews", pat.token, nil), http.StatusOK, "my reviews")
	items, _ := mine.body["items"].([]any)
	if len(items) != 1 || !strings.Contains(string(mine.raw), text) {
		t.Fatalf("my reviews: %s", mine.raw)
	}
	if theirs := j.must(j.call(http.MethodGet, "/v1/me/reviews", other.token, nil), http.StatusOK, "other patient's reviews"); strings.Contains(string(theirs.raw), "Clear") {
		t.Fatalf("another patient never sees the text: %s", theirs.raw)
	}
	// An unknown doctor and a doctor who is not Verified are 404.
	if r := j.call(http.MethodGet, "/v1/doctors/"+ids.New()+"/rating", "", nil); r.status != http.StatusNotFound {
		t.Fatalf("unknown doctor: %d %s", r.status, r.raw)
	}

	// ---- withdrawal ----
	reviewID := rv.str("reviewId")
	if r := j.call(http.MethodDelete, "/v1/me/reviews/"+reviewID, other.token, nil); r.status != http.StatusNotFound {
		t.Fatalf("withdrawing someone else's review: %d %s", r.status, r.raw)
	}
	j.must(j.call(http.MethodDelete, "/v1/me/reviews/"+reviewID, pat.token, nil), http.StatusNoContent, "withdraw")
	after := j.must(j.call(http.MethodGet, "/v1/doctors/"+doc.id+"/rating", "", nil), http.StatusOK, "public rating after withdrawal")
	if j.num(after, "count") != 0 || after.body["rating"] != nil {
		t.Fatalf("withdrawn: %s", after.raw)
	}
}

// On the India pack the "reviews" flag is off: the review routes do not exist (404), signed in or not, while the
// module is still built so erasure reaches it (K-DP-07).
func TestJourneyReviewRoutesAre404OnTheINPack(t *testing.T) {
	j := newJourney(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC))
	if j.c.Config.Flag("reviews") || j.c.ReviewsRoutes() != nil || j.c.Reviews == nil {
		t.Fatal("IN: flag off, routes not offered, module built")
	}
	pat := j.signIn("+919876500301", ip.AppPatient, "")
	for _, tc := range []struct{ method, path, token string }{
		{http.MethodPost, "/v1/me/appointments/" + ids.New() + "/review", pat.token},
		{http.MethodGet, "/v1/me/reviews", pat.token},
		{http.MethodDelete, "/v1/me/reviews/" + ids.New(), pat.token},
		{http.MethodGet, "/v1/doctors/" + ids.New() + "/rating", ""},
	} {
		var body any
		if tc.method == http.MethodPost {
			body = map[string]any{"rating": 5}
		}
		if r := j.call(tc.method, tc.path, tc.token, body); r.status != http.StatusNotFound {
			t.Fatalf("%s %s with the flag off: want 404, got %d %s", tc.method, tc.path, r.status, r.raw)
		}
	}
}
