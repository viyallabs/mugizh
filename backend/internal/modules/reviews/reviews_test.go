package reviews_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews"
	rvp "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

func strp(s string) *string { return &s }

// TC-51 / E-REV-01: only the patient of a completed visit, inside the window, once.
func TestTC51_OnlyCompletedVisitsInsideTheWindowCanBeReviewedOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// An appointment nobody knows: 404, never "not eligible".
	if _, err := f.m.Create(ctx, reviews.Actor{UserID: ids.New(), Role: "patient"}, ids.New(), reviews.CreateInput{Rating: 5}); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("unknown appointment: %v", err)
	}
	// Another patient's completed visit: 404, the same answer.
	v := f.completed(t, "", f.clock().Add(-time.Hour))
	other := reviews.Actor{UserID: ids.New(), Role: "patient"}
	if _, err := f.m.Create(ctx, other, v.appt, reviews.CreateInput{Rating: 1}); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("someone else's visit: %v", err)
	}
	// Another patient's appointment that is not completed: 404 too (nothing is revealed).
	pending := visit{appt: ids.New(), patient: ids.New(), doctor: ids.New()}
	f.register(pending, "confirmed")
	if _, err := f.m.Create(ctx, other, pending.appt, reviews.CreateInput{Rating: 1}); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("someone else's open appointment: %v", err)
	}
	// My own appointment that is not completed: 409 review.not_eligible.
	if _, err := f.m.Create(ctx, pending.actor(), pending.appt, reviews.CreateInput{Rating: 5}); prob(t, err).Code != "review.not_eligible" || prob(t, err).Status != http.StatusConflict {
		t.Fatalf("not completed: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM reviews.review`) != 0 {
		t.Fatal("no review may exist yet")
	}
	// Completed 15 days ago: outside the 14-day window.
	old := f.completed(t, "", f.clock().Add(-15*24*time.Hour))
	if _, err := f.m.Create(ctx, old.actor(), old.appt, reviews.CreateInput{Rating: 5}); prob(t, err).Code != "review.not_eligible" {
		t.Fatalf("outside the window: %v", err)
	}
	// Completed 13 days ago: inside the window; the second review of the same visit is refused.
	ok := f.completed(t, "", f.clock().Add(-13*24*time.Hour))
	r, err := f.m.Create(ctx, ok.actor(), ok.appt, reviews.CreateInput{Rating: 4})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rating != 4 || r.AppointmentRef != ok.appt || r.DoctorRef != ok.doctor || r.TextState != rvp.TextStateNone || r.Text != nil {
		t.Fatalf("review: %+v", r)
	}
	if _, err := f.m.Create(ctx, ok.actor(), ok.appt, reviews.CreateInput{Rating: 5}); prob(t, err).Code != "review.already_reviewed" || prob(t, err).Status != http.StatusConflict {
		t.Fatalf("second review: %v", err)
	}
	if n, sum := f.aggregate(t, ok.doctor); n != 1 || sum != 4 {
		t.Fatalf("aggregate after one review: %d %d", n, sum)
	}
	// The window is configuration: a visit 14 days minus a minute ago is still inside it.
	again := f.completed(t, "", f.clock().Add(-14*24*time.Hour+time.Minute))
	if _, err := f.m.Create(ctx, again.actor(), again.appt, reviews.CreateInput{Rating: 3}); err != nil {
		t.Fatalf("just inside 14 days: %v", err)
	}
}

// E-REV-03: a rating is never forced; only 1 to 5 is accepted.
func TestRatingMustBeOneToFive(t *testing.T) {
	f := setup(t)
	v := f.completed(t, "", f.clock().Add(-time.Hour))
	for _, rating := range []int{0, 6, -1} {
		_, err := f.m.Create(context.Background(), v.actor(), v.appt, reviews.CreateInput{Rating: rating})
		p := prob(t, err)
		if p.Status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Field != "rating" {
			t.Fatalf("rating %d: %v", rating, err)
		}
	}
	if f.count(t, `SELECT count(*) FROM reviews.review`) != 0 {
		t.Fatal("nothing stored")
	}
}

// Rule B5: the eligibility read model comes from appointment.completed only.
func TestVisitReadModelFromCompletedEvent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := visit{appt: ids.New(), patient: ids.New(), doctor: ids.New()}
	at := t0.Add(-3 * time.Hour)
	e := completedEvent(v, at)
	for i := 0; i < 2; i++ { // delivered twice
		if err := f.m.OnAppointmentCompleted(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var got time.Time
	if err := f.env.Owner.QueryRow(ctx, `SELECT completed_at FROM reviews.visit WHERE appointment_ref=$1 AND patient_ref=$2 AND doctor_ref=$3`, v.appt, v.patient, v.doctor).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) || f.count(t, `SELECT count(*) FROM reviews.visit`) != 1 {
		t.Fatalf("completed_at %v, rows %d", got, f.count(t, `SELECT count(*) FROM reviews.visit`))
	}
	// The same visit again under a new event ID keeps the first completion time.
	e2 := completedEvent(v, at.Add(time.Hour))
	if err := f.m.OnAppointmentCompleted(ctx, e2); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Owner.QueryRow(ctx, `SELECT completed_at FROM reviews.visit WHERE appointment_ref=$1`, v.appt).Scan(&got); err != nil || !got.Equal(at) {
		t.Fatalf("completed_at moved: %v %v", got, err)
	}
	bad := outbox.Event{ID: ids.New(), OccurredAt: at, Payload: map[string]string{"appointmentRef": "x"}}
	if err := f.m.OnAppointmentCompleted(ctx, bad); err == nil {
		t.Fatal("a malformed event must be refused")
	}
}

func seedRating(t testing.TB, f *fx, doctor string, n, sum int) {
	t.Helper()
	if _, err := f.env.Owner.Exec(context.Background(), `INSERT INTO reviews.doctor_rating (doctor_ref, n, sum) VALUES ($1,$2,$3)
		ON CONFLICT (doctor_ref) DO UPDATE SET n=EXCLUDED.n, sum=EXCLUDED.sum`, doctor, n, sum); err != nil {
		t.Fatal(err)
	}
}

// S-214: with C = 10 and the prior mean, two 5-star ratings rank below 200 ratings averaging 4.6
// (docs/arch/algorithms.md); the count is always returned; no reviews means a null rating.
func TestBayesianAverage(t *testing.T) {
	f := setupWith(t, func(c *reviews.Config) { c.MinPlatformRatings = 1000 }) // below the minimum: the prior is used
	ctx := context.Background()
	few, many, none := ids.New(), ids.New(), ids.New()
	seedRating(t, f, few, 2, 10)
	seedRating(t, f, many, 200, 920)
	a, err := f.m.Rating(ctx, few)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.m.Rating(ctx, many)
	if err != nil {
		t.Fatal(err)
	}
	if a.Count != 2 || b.Count != 200 || a.Value == nil || b.Value == nil {
		t.Fatalf("%+v %+v", a, b)
	}
	// (10*3 + 10) / 12 = 3.33; (10*3 + 920) / 210 = 4.52.
	if *a.Value != 3.3 || *b.Value != 4.5 || !(*a.Value < *b.Value) {
		t.Fatalf("few %v many %v", *a.Value, *b.Value)
	}
	z, err := f.m.Rating(ctx, none)
	if err != nil || z.Count != 0 || z.Value != nil {
		t.Fatalf("no reviews: %+v %v", z, err)
	}
	// An unverified doctor (or an unknown one) is 404.
	f.ver.bad[few] = true
	if _, err := f.m.Rating(ctx, few); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("unverified doctor: %v", err)
	}
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// m is the platform mean once the platform has MinPlatformRatings ratings, else the prior.
func TestBayesianPlatformMeanSwitchesAtTheMinimum(t *testing.T) {
	f := setupWith(t, func(c *reviews.Config) { c.MinPlatformRatings = 50; c.PriorMean = 3.0; c.BayesianC = 10 })
	ctx := context.Background()
	a, b := ids.New(), ids.New()
	seedRating(t, f, a, 10, 50) // all fives
	seedRating(t, f, b, 39, 39) // 49 ratings in all: still the prior (3.0)
	r, err := f.m.Rating(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if want := round1((10*3.0 + 50) / 20); *r.Value != want {
		t.Fatalf("below the minimum: %v want %v", *r.Value, want)
	}
	seedRating(t, f, b, 40, 40) // 50 ratings in all: the platform mean is 90/50 = 1.8
	r, err = f.m.Rating(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if want := round1((10*1.8 + 50) / 20); *r.Value != want {
		t.Fatalf("at the minimum: %v want %v", *r.Value, want)
	}
}

func TestConfigIsValidated(t *testing.T) {
	f := setup(t)
	for _, bad := range []func(*reviews.Config){
		func(c *reviews.Config) { c.BayesianC = 0 }, func(c *reviews.Config) { c.PriorMean = 0.5 },
		func(c *reviews.Config) { c.PriorMean = 5.5 }, func(c *reviews.Config) { c.Window = 0 },
		func(c *reviews.Config) { c.MinPlatformRatings = 0 }, func(c *reviews.Config) { c.MaxTextRunes = 0 },
	} {
		c := testCfg()
		bad(&c)
		if _, err := reviews.New(f.env.Pools.For("reviews"), c, reviews.Deps{Booking: f.book, Verifier: f.ver, Crypto: f.cr}, slogDiscard()); err == nil {
			t.Fatalf("an invalid config must be refused: %+v", c)
		}
	}
}

// K-DP-03: the text is sealed with the patient key, never in plaintext, and only its author gets it back.
func TestTextIsSealedAndOnlyTheAuthorSeesIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.completed(t, "", f.clock().Add(-time.Hour))
	const text = "Friendly staff and a short wait."
	r, err := f.m.Create(ctx, v.actor(), v.appt, reviews.CreateInput{Rating: 5, Text: strp(text)})
	if err != nil {
		t.Fatal(err)
	}
	if r.TextState != rvp.TextStatePendingModeration || r.Text == nil || *r.Text != text {
		t.Fatalf("author's view: %+v", r)
	}
	var sealed []byte
	var state string
	if err := f.env.Owner.QueryRow(ctx, `SELECT text_sealed, text_state FROM reviews.review WHERE id=$1`, r.ID).Scan(&sealed, &state); err != nil {
		t.Fatal(err)
	}
	if len(sealed) == 0 || bytes.Contains(sealed, []byte("Friendly")) || bytes.Contains(sealed, []byte(text)) || state != "pending_moderation" {
		t.Fatalf("text must be sealed: state %q len %d", state, len(sealed))
	}
	// No column of the row holds the text.
	if n := f.count(t, `SELECT count(*) FROM reviews.review r WHERE r::text LIKE '%Friendly%'`); n != 0 {
		t.Fatal("plaintext found in the row")
	}
	// The author reads it back; nobody else gets it, and the public rating has no text at all.
	list, _, err := f.m.ListMine(ctx, v.actor(), 10, "")
	if err != nil || len(list) != 1 || list[0].Text == nil || *list[0].Text != text || list[0].TextState != rvp.TextStatePendingModeration {
		t.Fatalf("author's list: %+v %v", list, err)
	}
	others, _, err := f.m.ListMine(ctx, reviews.Actor{UserID: ids.New(), Role: "patient"}, 10, "")
	if err != nil || len(others) != 0 {
		t.Fatalf("another patient's list: %+v %v", others, err)
	}
	pub, err := f.m.Rating(ctx, v.doctor)
	if err != nil || pub.Count != 1 || strings.Contains(fmt.Sprintf("%+v", pub), "Friendly") {
		t.Fatalf("public rating: %+v %v", pub, err)
	}
	// The star rating counts at once, while the text waits for moderation.
	if n, sum := f.aggregate(t, v.doctor); n != 1 || sum != 5 {
		t.Fatalf("aggregate: %d %d", n, sum)
	}
	// Text of only spaces is no text.
	v2 := f.completed(t, "", f.clock().Add(-time.Hour))
	r2, err := f.m.Create(ctx, v2.actor(), v2.appt, reviews.CreateInput{Rating: 2, Text: strp("   \n ")})
	if err != nil || r2.Text != nil || r2.TextState != rvp.TextStateNone {
		t.Fatalf("blank text: %+v %v", r2, err)
	}
}

// aadhaarLike builds a Verhoeff-valid 12-digit number for the test; synthetic, never a real one.
func aadhaarLike(t testing.TB) string {
	t.Helper()
	for i := 0; i < 10; i++ {
		d := fmt.Sprintf("23456789012%d", i)
		if scrub.Verhoeff(d) {
			return d
		}
	}
	t.Fatal("no valid check digit")
	return ""
}

// Automatic checks refuse the text with 422 and a field code, so the patient can edit it.
func TestTextChecksRefuseWithFieldCodes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.completed(t, "", f.clock().Add(-time.Hour))
	aad := aadhaarLike(t)
	cases := []struct{ name, text, code string }{
		{"control character", "good\x00service", "control_characters"},
		{"bell", "good\aservice", "control_characters"},
		{"invalid utf-8", "good\xffservice", "invalid"},
		{"too long", strings.Repeat("a", 1001), "too_long"},
		{"identity number", "my number is " + aad[:4] + " " + aad[4:8] + " " + aad[8:], "contains_identifier"},
		{"phone", "call me on 98765 43210", "contains_contact_detail"},
		{"phone with code", "+91-98765-43210 is my number", "contains_contact_detail"},
		{"email", "write to someone@example.org", "contains_contact_detail"},
		{"link", "see http://example.org/x", "contains_link_or_markup"},
		{"bare domain", "visit evil.org today", "contains_link_or_markup"},
		{"markup", "great <b>doctor</b>", "contains_link_or_markup"},
	}
	for _, c := range cases {
		_, err := f.m.Create(ctx, v.actor(), v.appt, reviews.CreateInput{Rating: 3, Text: strp(c.text)})
		p := prob(t, err)
		if p.Status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Field != "text" || p.Errors[0].Code != c.code {
			t.Errorf("%s: %v %+v", c.name, err, p.Errors)
		}
		if strings.Contains(p.Detail+p.Title, "good") || strings.Contains(p.Detail+p.Title, "evil") {
			t.Errorf("%s: the problem must not echo the text", c.name)
		}
	}
	if f.count(t, `SELECT count(*) FROM reviews.review`) != 0 || f.count(t, `SELECT count(*) FROM reviews.doctor_rating`) != 0 {
		t.Fatal("a refused review leaves nothing behind")
	}
	// Edited, it passes; a 1000-character text is the longest allowed.
	if _, err := f.m.Create(ctx, v.actor(), v.appt, reviews.CreateInput{Rating: 3, Text: strp(strings.Repeat("é", 1000))}); err != nil {
		t.Fatalf("1000 characters: %v", err)
	}
}

func TestWithdrawRemovesTheReviewAndFixesTheAggregate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doctor := ids.New()
	a := f.completed(t, doctor, f.clock().Add(-time.Hour))
	b := f.completed(t, doctor, f.clock().Add(-time.Hour))
	ra, err := f.m.Create(ctx, a.actor(), a.appt, reviews.CreateInput{Rating: 5, Text: strp("Clear explanation")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Create(ctx, b.actor(), b.appt, reviews.CreateInput{Rating: 2}); err != nil {
		t.Fatal(err)
	}
	if n, sum := f.aggregate(t, doctor); n != 2 || sum != 7 {
		t.Fatalf("aggregate: %d %d", n, sum)
	}
	// Someone else's review, and an unknown one: 404, nothing changes.
	if err := f.m.Withdraw(ctx, b.actor(), ra.ID); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("someone else's review: %v", err)
	}
	if err := f.m.Withdraw(ctx, b.actor(), ids.New()); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("unknown review: %v", err)
	}
	if n, sum := f.aggregate(t, doctor); n != 2 || sum != 7 {
		t.Fatalf("aggregate must not move: %d %d", n, sum)
	}
	if err := f.m.Withdraw(ctx, a.actor(), ra.ID); err != nil {
		t.Fatal(err)
	}
	if n, sum := f.aggregate(t, doctor); n != 1 || sum != 2 {
		t.Fatalf("aggregate after withdrawal: %d %d", n, sum)
	}
	if f.count(t, `SELECT count(*) FROM reviews.review WHERE id=$1`, ra.ID) != 0 {
		t.Fatal("the review and its text are deleted")
	}
	if err := f.m.Withdraw(ctx, a.actor(), ra.ID); prob(t, err).Status != http.StatusNotFound {
		t.Fatalf("withdrawing twice: %v", err)
	}
	pub, err := f.m.Rating(ctx, doctor)
	if err != nil || pub.Count != 1 {
		t.Fatalf("public count: %+v %v", pub, err)
	}
}

func TestListMyReviewsPagesNewestFirst(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	var want []string
	for i := 0; i < 5; i++ {
		v := visit{appt: ids.New(), patient: patient, doctor: ids.New()}
		f.register(v, "completed")
		if err := f.m.OnAppointmentCompleted(ctx, completedEvent(v, f.clock().Add(-time.Hour))); err != nil {
			t.Fatal(err)
		}
		f.at(f.clock().Add(time.Minute))
		r, err := f.m.Create(ctx, v.actor(), v.appt, reviews.CreateInput{Rating: 1 + i%5})
		if err != nil {
			t.Fatal(err)
		}
		want = append([]string{r.ID}, want...)
	}
	a := reviews.Actor{UserID: patient, Role: "patient"}
	var got []string
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		items, next, err := f.m.ListMine(ctx, a, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range items {
			got = append(got, r.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
	if _, _, err := f.m.ListMine(ctx, a, 2, "garbage"); prob(t, err).Status != http.StatusBadRequest {
		t.Fatalf("bad cursor: %v", err)
	}
}

// Audit: creating and withdrawing is audited in the same transaction, with no text.
func TestAuditRowsForCreateAndWithdraw(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	v := f.completed(t, "", f.clock().Add(-time.Hour))
	const text = "Polite and on time"
	r, err := f.m.Create(ctx, reviews.Actor{UserID: v.patient, Role: "patient", RequestID: "req-1"}, v.appt, reviews.CreateInput{Rating: 4, Text: strp(text)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Withdraw(ctx, reviews.Actor{UserID: v.patient, Role: "patient", RequestID: "req-2"}, r.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.env.Owner.Query(ctx, `SELECT action, actor_ref, actor_role, subject_ref, resource_type, resource_ref, request_id, result, row_to_json(a)::text
		FROM audit.audit_outbox a ORDER BY inserted_at`)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for rows.Next() {
		var action, actor, role, subject, rtype, rref, req, result, whole string
		if err := rows.Scan(&action, &actor, &role, &subject, &rtype, &rref, &req, &result, &whole); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(whole, "Polite") {
			t.Fatal("the audit event must not hold the text")
		}
		if actor != v.patient || role != "patient" || subject != v.patient || rtype != "review" || rref != r.ID || result != "allowed" {
			t.Fatalf("audit row: %s %s %s %s %s %s", action, actor, role, subject, rtype, rref)
		}
		actions = append(actions, action+"/"+req)
	}
	rows.Close()
	if strings.Join(actions, ",") != "review.create/req-1,review.withdraw/req-2" {
		t.Fatalf("audit actions: %v", actions)
	}
	// A refused review leaves no audit row (same transaction as the write).
	v2 := f.completed(t, "", f.clock().Add(-time.Hour))
	if _, err := f.m.Create(ctx, v2.actor(), v2.appt, reviews.CreateInput{Rating: 3, Text: strp("call 98765 43210")}); err == nil {
		t.Fatal("refused")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox`); n != 2 {
		t.Fatalf("audit rows: %d", n)
	}
}

// CD-08 (S-209): a doctor who books their own practice as a patient cannot rate themselves
// (manipulated reviews; NMC guidelines). The visit is completed and inside the window, but
// the review is refused and the aggregate does not move.
func TestCD08DoctorCannotReviewThemselves(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	self := ids.New()
	v := visit{appt: ids.New(), patient: self, doctor: self}
	f.register(v, "completed")
	if err := f.m.OnAppointmentCompleted(ctx, completedEvent(v, f.clock())); err != nil {
		t.Fatal(err)
	}
	_, err := f.m.Create(ctx, v.actor(), v.appt, reviews.CreateInput{Rating: 5})
	if p := prob(t, err); p.Code != "review.not_eligible" {
		t.Fatalf("self-review: %v", err)
	}
	if n, _ := f.aggregate(t, self); n != 0 {
		t.Fatalf("aggregate moved: %d", n)
	}
}
