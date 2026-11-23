package directory_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	geofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { app.UseStrictJSON(); testkit.Main(m) }

// ---- fixtures (synthetic data only) ----

type roleFake struct {
	mu       sync.Mutex
	assigned [][4]string
	revoked  [][4]string
	failWith error
}

func (r *roleFake) AssignRole(_ context.Context, userID, role, clinicRef, byRef string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	r.assigned = append(r.assigned, [4]string{userID, role, clinicRef, byRef})
	return nil
}

func (r *roleFake) RevokeRole(_ context.Context, userID, role, clinicRef, byRef string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = append(r.revoked, [4]string{userID, role, clinicRef, byRef})
	return nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// aadhaarLike builds a synthetic Verhoeff-valid 12-digit number (never a real one).
func aadhaarLike(t testing.TB) string {
	t.Helper()
	for d := '0'; d <= '9'; d++ {
		if s := "29876543210" + string(d); scrub.Verhoeff(s) {
			return s
		}
	}
	t.Fatal("no check digit")
	return ""
}

func testConfig() directory.Config {
	return directory.Config{Currency: "TST", CountryCode: "XX", DefaultTimeZone: "UTC",
		ProhibitedIDValidators: []string{"verhoeff_12_digit_first_2_to_9"},
		MaxRadiusKm:            50, DefaultRadiusKm: 10, MaxFeeMinor: 1_000_000,
		ClaimWords: directory.DefaultClaimWords, MinGeocodeAccuracy: geocoding.Street,
		VerificationChecks: []string{"registration_active"}}
}

type fx struct {
	env   *testkit.Env
	m     *directory.Module
	roles *roleFake
	geo   *geofake.Geocoder
	logs  *syncBuf
	clock time.Time
}

func setupWith(t testing.TB, cfg directory.Config) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), directory.Source()}})
	f := &fx{env: env, roles: &roleFake{}, geo: &geofake.Geocoder{}, logs: &syncBuf{}, clock: time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)}
	m, err := directory.New(env.Pools.For("directory"), cfg, f.geo, f.roles, slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	return f
}

func setup(t testing.TB) *fx { return setupWith(t, testConfig()) }

func (f *fx) tick() time.Time { f.clock = f.clock.Add(time.Second); return f.clock }

func docActor(id string) directory.Actor { return directory.Actor{UserID: id, Role: ip.RoleDoctor} }

func adminActor(user, clinic string) directory.Actor {
	return directory.Actor{UserID: user, Role: ip.RoleClinicAdmin, ClinicRef: clinic}
}

// doctor creates a profile and returns the doctor's ID.
func (f *fx) doctor(t testing.TB, name string, langs ...string) string {
	t.Helper()
	id := ids.New()
	if len(langs) == 0 {
		langs = []string{"English"}
	}
	if _, err := f.m.UpsertProfessional(context.Background(), docActor(id), directory.ProfessionalInput{DisplayName: name, Languages: langs}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fx) status(t testing.TB, ref, status string, at time.Time) {
	t.Helper()
	e := outbox.Event{ID: ids.New(), Type: "verification.status_changed", OccurredAt: at, Payload: map[string]string{"professionalRef": ref, "status": status}}
	if err := f.m.OnVerificationStatusChanged(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) verify(t testing.TB, refs ...string) {
	t.Helper()
	for _, r := range refs {
		f.status(t, r, "verified", f.tick())
	}
}

// clinic creates a clinic with a manual pin and returns its ID and admin actor.
func (f *fx) clinic(t testing.TB, name string, lat, lng float64, lead string) (string, directory.Actor) {
	t.Helper()
	user := ids.New()
	c, err := f.m.SubmitClinic(context.Background(), directory.Actor{UserID: user, Role: "authenticated"}, directory.ClinicInput{
		Name: name, Address: "12 Sample Road, Testville", Pin: &directory.Point{Lat: lat, Lng: lng}, LeadDoctorRef: lead})
	if err != nil {
		t.Fatal(err)
	}
	f.acceptAll(t, lead) // review M5: the doctor accepts the lead link
	return c.ID, adminActor(user, c.ID)
}

func (f *fx) practice(t testing.TB, admin directory.Actor, doctor string, fee int64, modes ...string) string {
	t.Helper()
	if len(modes) == 0 {
		modes = []string{directory.ModeInPerson}
	}
	p, err := f.m.CreatePractice(context.Background(), admin, admin.ClinicRef, directory.PracticeInput{DoctorRef: doctor, Modes: modes, FeeAmount: fee, Timings: "Mon-Fri 09:00-13:00"})
	if err != nil {
		t.Fatal(err)
	}
	f.acceptAll(t, doctor)
	return p.ID
}

// listedPractice builds a doctor + clinic + practice, all Verified, at the given point.
func (f *fx) listedPractice(t testing.TB, name string, lat, lng float64, fee int64, modes ...string) (practice, doctor, clinic string) {
	t.Helper()
	doctor = f.doctor(t, name)
	clinic, admin := f.clinic(t, name+" Clinic", lat, lng, doctor)
	practice = f.practice(t, admin, doctor, fee, modes...)
	f.verify(t, doctor, clinic)
	return
}

func (f *fx) search(t testing.TB, q directory.SearchQuery) directory.SearchResult {
	t.Helper()
	res, err := f.m.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *fx) at(lat, lng, radius float64) directory.SearchQuery {
	return directory.SearchQuery{Location: &directory.Point{Lat: lat, Lng: lng}, RadiusKm: radius}
}

func practiceIDs(items []directory.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.PracticeID
	}
	return out
}

func wantProblem(t testing.TB, err error, code string, status int) *problem.Problem {
	t.Helper()
	var p *problem.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want problem %s, got %v", code, err)
	}
	if p.Code != code || p.Status != status {
		t.Fatalf("want %s/%d, got %s/%d", code, status, p.Code, p.Status)
	}
	return p
}

func hasField(p *problem.Problem, field, code string) bool {
	for _, e := range p.Errors {
		if e.Field == field && e.Code == code {
			return true
		}
	}
	return false
}

func (f *fx) count(t testing.TB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	baseLat, baseLng = 20.0, 77.0
	kmLat            = 0.0089932 // degrees of latitude per km (sphere)
)

// ---- TC-44 / K-CS-01: listed only when verified, server-side, immediately ----

func TestTC44ListedOnlyWhenVerified(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Lead Doctor")
	other := f.doctor(t, "Other Doctor")
	clinic, admin := f.clinic(t, "Sample Clinic", baseLat+kmLat, baseLng, lead)
	pr := f.practice(t, admin, other, 50000)

	visible := func() bool {
		res := f.search(t, f.at(baseLat, baseLng, 10))
		_, perr := f.m.GetProfile(ctx, pr)
		_, rerr := f.m.GetPractice(ctx, pr)
		inSearch := len(res.Items) == 1
		if inSearch != (perr == nil) || inSearch != (rerr == nil) {
			t.Fatalf("search (%v), profile (%v) and reader (%v) disagree", inSearch, perr, rerr)
		}
		if perr != nil {
			wantProblem(t, perr, "resource.not_found", 404)
		}
		if rerr != nil && !errors.Is(rerr, dp.ErrNotFound) {
			t.Fatalf("reader error: %v", rerr)
		}
		return inSearch
	}
	step := func(label string, want bool) {
		t.Helper()
		if got := visible(); got != want {
			t.Fatalf("%s: listed = %v, want %v", label, got, want)
		}
	}
	step("nothing verified", false)
	f.verify(t, other)
	step("doctor only", false)
	f.verify(t, clinic)
	step("doctor and clinic, lead not verified", false)
	f.verify(t, lead)
	step("doctor, clinic and lead verified", true)

	f.status(t, other, "suspended", f.tick())
	step("doctor suspended hides at once", false)
	f.verify(t, other)
	step("doctor verified again", true)
	f.status(t, lead, "suspended", f.tick())
	step("lead doctor suspended", false)
	f.verify(t, lead)
	f.status(t, clinic, "expired", f.tick())
	step("clinic expired", false)
	f.verify(t, clinic)
	step("all verified again", true)

	// Every non-verified status hides.
	for _, st := range []string{"draft", "submitted", "checking", "needs_info", "rejected", "suspended", "expired", "unheard_of"} {
		f.status(t, other, st, f.tick())
		step("status "+st, false)
	}
	f.verify(t, other)

	// The practice owner can switch it off.
	if _, err := f.m.UpdatePractice(ctx, docActor(other), pr, directory.PracticeChange{Active: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	step("inactive practice", false)
}

func ptr[T any](v T) *T { return &v }

func TestVerificationReadModelIdempotentAndOrdered(t *testing.T) {
	f := setup(t)
	ref := ids.New()
	t0 := f.tick()
	ev := func(id, status string, at time.Time) outbox.Event {
		return outbox.Event{ID: id, Type: "verification.status_changed", OccurredAt: at, Payload: map[string]string{"professionalRef": ref, "status": status}}
	}
	id1 := ids.New()
	for i := 0; i < 3; i++ { // at-least-once delivery
		if err := f.m.OnVerificationStatusChanged(context.Background(), ev(id1, "verified", t0)); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM directory.processed_event`); n != 1 {
		t.Fatalf("processed_event rows = %d, want 1", n)
	}
	// A newer suspension, then a late older "verified": the suspension must stand.
	if err := f.m.OnVerificationStatusChanged(context.Background(), ev(ids.New(), "suspended", t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnVerificationStatusChanged(context.Background(), ev(ids.New(), "verified", t0.Add(30*time.Second))); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT status FROM directory.verification_status_view WHERE professional_ref=$1`, ref).Scan(&st); err != nil || st != "suspended" {
		t.Fatalf("status = %q, %v; want suspended (stale event ignored)", st, err)
	}
	// Malformed events are errors (retry, then dead letter), never silent.
	bad := outbox.Event{ID: ids.New(), Type: "verification.status_changed", Payload: map[string]string{"professionalRef": "nope", "status": "verified"}}
	if err := f.m.OnVerificationStatusChanged(context.Background(), bad); err == nil {
		t.Fatal("malformed event accepted")
	}
}

func TestSuspensionEmitsPracticeUpdated(t *testing.T) {
	f := setup(t)
	pr, doc, _ := f.listedPractice(t, "Doc One", baseLat, baseLng, 10000)
	before := f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated' AND payload->>'practiceRef'=$1`, pr)
	f.status(t, doc, "suspended", f.tick())
	after := f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated' AND payload->>'practiceRef'=$1`, pr)
	if after != before+1 {
		t.Fatalf("practice.updated events %d -> %d, want +1", before, after)
	}
}

// ---- S-119: radius search ----

func TestRadiusSearchOrderDistanceAndFilters(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, docA, _ := f.listedPractice(t, "Doctor A", baseLat+1*kmLat, baseLng, 30000, directory.ModeInPerson)
	b, docB, _ := f.listedPractice(t, "Doctor B", baseLat+5*kmLat, baseLng, 60000, directory.ModeInPerson, directory.ModeVideo)
	c, _, _ := f.listedPractice(t, "Doctor C", baseLat+22*kmLat, baseLng, 90000, directory.ModeVideo)

	res := f.search(t, f.at(baseLat, baseLng, 10))
	if got := practiceIDs(res.Items); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("radius 10: got %v, want [%s %s]", got, a, b)
	}
	if d := res.Items[0].DistanceKm; d != 1.0 {
		t.Fatalf("distance A = %v, want 1.0 (rounded to 0.1 km)", d)
	}
	if d := res.Items[1].DistanceKm; d != 5.0 {
		t.Fatalf("distance B = %v, want 5.0", d)
	}
	if res.Items[0].Currency != "TST" || res.Items[0].FeeAmount != 30000 || res.Items[0].FeeVersion != "1" {
		t.Fatalf("fee fields: %+v", res.Items[0])
	}
	if got := practiceIDs(f.search(t, f.at(baseLat, baseLng, 30)).Items); len(got) != 3 || got[2] != c {
		t.Fatalf("radius 30: %v", got)
	}

	// Filters: mode, fee range, specialty (needs a recognised PG qualification, RG-04).
	q := f.at(baseLat, baseLng, 30)
	q.Mode = directory.ModeVideo
	if got := practiceIDs(f.search(t, q).Items); len(got) != 2 || got[0] != b || got[1] != c {
		t.Fatalf("mode video: %v", got)
	}
	q = f.at(baseLat, baseLng, 30)
	q.MinFee, q.MaxFee = ptr(int64(40000)), ptr(int64(70000))
	if got := practiceIDs(f.search(t, q).Items); len(got) != 1 || got[0] != b {
		t.Fatalf("fee range: %v", got)
	}
	if err := f.m.RecordQualification(ctx, docA, directory.Qualification{Degree: "MD Synthetic Medicine", Level: "pg", Specialty: "cardiology"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpsertProfessional(ctx, docActor(docA), directory.ProfessionalInput{DisplayName: "Doctor A", Specialty: "cardiology", Languages: []string{"English"}}); err != nil {
		t.Fatal(err)
	}
	q = f.at(baseLat, baseLng, 30)
	q.Specialty = "cardiology"
	got := f.search(t, q)
	if len(got.Items) != 1 || got.Items[0].PracticeID != a || got.Items[0].Doctor.Specialty != "cardiology" {
		t.Fatalf("specialty filter: %+v", got.Items)
	}
	_ = docB
}

func TestRadiusSearchValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cases := map[string]directory.SearchQuery{
		"no location":      {RadiusKm: 5},
		"both":             {Location: &directory.Point{Lat: 1, Lng: 1}, PostalCode: "ABC123"},
		"radius too large": {Location: &directory.Point{Lat: 1, Lng: 1}, RadiusKm: 51},
		"bad latitude":     {Location: &directory.Point{Lat: 91, Lng: 1}},
		"bad mode":         {Location: &directory.Point{Lat: 1, Lng: 1}, Mode: "carrier_pigeon"},
		"bad fee range":    {Location: &directory.Point{Lat: 1, Lng: 1}, MinFee: ptr(int64(5)), MaxFee: ptr(int64(1))},
		"limit":            {Location: &directory.Point{Lat: 1, Lng: 1}, Limit: 101},
		"bad cursor":       {Location: &directory.Point{Lat: 1, Lng: 1}, Cursor: "@@@"},
	}
	for name, q := range cases {
		if _, err := f.m.Search(ctx, q); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			wantProblem(t, err, "directory.invalid", 400)
		}
	}
}

func TestPostalCodeFallback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pr, _, _ := f.listedPractice(t, "Doctor P", baseLat+2*kmLat, baseLng, 20000)
	if err := f.m.LoadLocalities(ctx, ids.New(), []directory.Locality{{PostalCode: "tst 001", Lat: baseLat, Lng: baseLng}, {PostalCode: "TST002", Lat: baseLat + 3, Lng: baseLng}}); err != nil {
		t.Fatal(err)
	}
	res := f.search(t, directory.SearchQuery{PostalCode: " Tst001 ", RadiusKm: 5})
	if got := practiceIDs(res.Items); len(got) != 1 || got[0] != pr {
		t.Fatalf("postal search: %v", got)
	}
	if got := f.search(t, directory.SearchQuery{PostalCode: "TST002", RadiusKm: 5}).Items; len(got) != 0 {
		t.Fatalf("far postal code returned %d items", len(got))
	}
	_, err := f.m.Search(ctx, directory.SearchQuery{PostalCode: "TST999"})
	wantProblem(t, err, "directory.postal_code_unknown", 422)
	_, err = f.m.Search(ctx, directory.SearchQuery{PostalCode: "no way!"})
	wantProblem(t, err, "directory.invalid", 400)
	if err := f.m.LoadLocalities(ctx, ids.New(), []directory.Locality{{PostalCode: "??", Lat: 1, Lng: 1}}); err == nil {
		t.Fatal("bad locality accepted")
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='directory.locality.load'`); n != 1 {
		t.Fatalf("locality load audit events = %d, want 1", n)
	}
}

func TestCursorPaginationCoversEverythingOnce(t *testing.T) {
	f := setup(t)
	var want []string
	for i := 0; i < 7; i++ {
		pr, _, _ := f.listedPractice(t, fmt.Sprintf("Doctor %d", i), baseLat+float64(i+1)*kmLat, baseLng, 10000)
		want = append(want, pr)
	}
	// Two more at the SAME clinic distance as #0 to exercise tie-breaking.
	for i := 0; i < 2; i++ {
		pr, _, _ := f.listedPractice(t, fmt.Sprintf("Tie %d", i), baseLat+1*kmLat, baseLng, 10000)
		want = append(want, pr)
	}
	q := f.at(baseLat, baseLng, 20)
	q.Limit = 4
	var got []string
	pages := 0
	for {
		res := f.search(t, q)
		pages++
		got = append(got, practiceIDs(res.Items)...)
		if res.NextCursor == "" {
			break
		}
		q.Cursor = res.NextCursor
		if pages > 10 {
			t.Fatal("paging does not end")
		}
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate %s across pages", id)
		}
		seen[id] = true
	}
	if len(got) != len(want) || pages != 3 {
		t.Fatalf("got %d items in %d pages, want %d in 3", len(got), pages, len(want))
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("missing %s", id)
		}
	}
}

func TestTiesRotateDailyAndStayStableWhilePaging(t *testing.T) {
	f := setup(t)
	doc1 := f.doctor(t, "Tie One")
	doc2 := f.doctor(t, "Tie Two")
	clinic, admin := f.clinic(t, "Shared Clinic", baseLat+kmLat, baseLng, doc1)
	p1 := f.practice(t, admin, doc1, 10000)
	p2 := f.practice(t, admin, doc2, 10000)
	f.verify(t, doc1, doc2, clinic)

	first := map[string]int{}
	for day := 0; day < 30; day++ {
		f.m.SetClock(func() time.Time { return time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC).AddDate(0, 0, day) })
		a := practiceIDs(f.search(t, f.at(baseLat, baseLng, 5)).Items)
		b := practiceIDs(f.search(t, f.at(baseLat, baseLng, 5)).Items)
		if len(a) != 2 || a[0] != b[0] {
			t.Fatalf("day %d: order not stable within a day: %v %v", day, a, b)
		}
		first[a[0]]++
	}
	if first[p1] == 0 || first[p2] == 0 {
		t.Fatalf("ties did not rotate over 30 days: %v", first)
	}
	// A cursor keeps its day: page 2 after midnight continues the same order.
	f.m.SetClock(func() time.Time { return time.Date(2026, 12, 1, 23, 59, 0, 0, time.UTC) })
	q := f.at(baseLat, baseLng, 5)
	q.Limit = 1
	r1 := f.search(t, q)
	f.m.SetClock(func() time.Time { return time.Date(2026, 12, 2, 0, 1, 0, 0, time.UTC) })
	q.Cursor = r1.NextCursor
	r2 := f.search(t, q)
	if len(r1.Items) != 1 || len(r2.Items) != 1 || r1.Items[0].PracticeID == r2.Items[0].PracticeID {
		t.Fatalf("paging across midnight: %v %v", practiceIDs(r1.Items), practiceIDs(r2.Items))
	}
}

// ---- RET-12: the search location is never stored or logged ----

func TestE_GEO_SearchLocationNeverStoredOrLogged(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.listedPractice(t, "Doctor G", baseLat+kmLat, baseLng, 10000)
	if err := f.m.LoadLocalities(ctx, ids.New(), []directory.Locality{{PostalCode: "TST777", Lat: 20.00112233, Lng: 77.00445566}}); err != nil {
		t.Fatal(err)
	}
	f.search(t, directory.SearchQuery{Location: &directory.Point{Lat: 20.00123457, Lng: 77.00765433}, RadiusKm: 5})
	f.search(t, directory.SearchQuery{PostalCode: "TST777", RadiusKm: 5})
	if _, err := f.m.Search(ctx, directory.SearchQuery{Location: &directory.Point{Lat: 20.00123457, Lng: 77.00765433}, RadiusKm: 500}); err == nil {
		t.Fatal("radius over the maximum accepted")
	}

	needles := []string{"20.00123457", "77.00765433", "20.0012345", "77.0076543"}
	for _, tbl := range []string{"directory.professional", "directory.qualification", "directory.clinic", "directory.practice",
		"directory.verification_status_view", "directory.event_outbox", "directory.processed_event", "audit.audit_outbox", "platform.schema_migrations"} {
		rows, err := f.env.Owner.Query(ctx, fmt.Sprintf(`SELECT row_to_json(t)::text FROM %s t`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			for _, n := range needles {
				if strings.Contains(s, n) {
					t.Fatalf("search location found stored in %s", tbl)
				}
			}
		}
		rows.Close()
	}
	for _, n := range needles {
		if strings.Contains(f.logs.String(), n) {
			t.Fatalf("search location found in logs: %s", f.logs.String())
		}
	}
}

// ---- S-162: ranking v1 ----

func TestRankingFactorsArePublishedAndNeverPaid(t *testing.T) {
	var sum float64
	names := map[string]bool{}
	for _, fc := range directory.RankingFactors() {
		names[fc.Name] = true
		if fc.Plain == "" {
			t.Errorf("factor %s has no plain-words text (CD-05)", fc.Name)
		}
		if fc.Kind == "score" {
			sum += fc.Weight
		} else if fc.Weight != 0 {
			t.Errorf("gate factor %s has a weight", fc.Name)
		}
	}
	for _, banned := range []string{"payment", "subscription", "revenue", "sponsored", "featured", "reliability", "rating", "health", "plan", "tier"} {
		for n := range names {
			if strings.Contains(n, banned) {
				t.Errorf("ranking factor %q contains forbidden input %q (K-CS-08, A11)", n, banned)
			}
		}
	}
	if !names["distance"] || !names["availability"] || !names["verification"] || len(names) != 3 {
		t.Errorf("factors = %v, want distance, availability, verification", names)
	}
	if sum < 0.79 || sum > 0.81 {
		t.Errorf("score weights sum to %v, want 0.8 (0.45 + 0.35)", sum)
	}
}

type availFake struct {
	slots map[string]time.Time
	err   error
	calls int
}

func (a *availFake) EarliestSlots(_ context.Context, ids []string, _ time.Time) (map[string]time.Time, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	out := map[string]time.Time{}
	for _, id := range ids {
		if t, ok := a.slots[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func TestAvailabilityHookNotFakedAndRankedWhenPresent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	near, _, _ := f.listedPractice(t, "Near Doc", baseLat+1*kmLat, baseLng, 10000)
	far, _, _ := f.listedPractice(t, "Far Doc", baseLat+8*kmLat, baseLng, 10000)

	// No booking data: the filter is refused (never faked) and order is distance only.
	q := f.at(baseLat, baseLng, 10)
	q.AvailableWithinDays = 3
	_, err := f.m.Search(ctx, q)
	wantProblem(t, err, "directory.availability_filter_unavailable", 422)
	if got := practiceIDs(f.search(t, f.at(baseLat, baseLng, 10)).Items); got[0] != near || got[1] != far {
		t.Fatalf("distance-only order: %v", got)
	}

	// With a source: availability counts (0.35) against distance (0.45).
	src := &availFake{slots: map[string]time.Time{far: time.Now().UTC().Add(time.Hour)}}
	f.m.SetAvailability(src)
	res := f.search(t, f.at(baseLat, baseLng, 10))
	if got := practiceIDs(res.Items); len(got) != 2 || got[0] != far {
		t.Fatalf("far doctor with a slot in 1 h should outrank near doctor without one: %v", got)
	}
	q.AvailableWithinDays = 3
	if got := practiceIDs(f.search(t, q).Items); len(got) != 1 || got[0] != far {
		t.Fatalf("availableWithinDays filter: %v", got)
	}
	// A failing source fails closed: an error, never a silently unranked list.
	src.err = errors.New("booking down")
	if _, err := f.m.Search(ctx, f.at(baseLat, baseLng, 10)); err == nil {
		t.Fatal("search succeeded with a failing availability source")
	}
	f.m.SetAvailability(nil)
}

// ---- S-112: profile management ----

func TestProfileSpecialtyNeedsRecognisedPGQualification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.doctor(t, "Doctor S")
	in := directory.ProfessionalInput{DisplayName: "Doctor S", Specialty: "cardiology", Languages: []string{"English"}}
	_, err := f.m.UpsertProfessional(ctx, docActor(id), in)
	p := wantProblem(t, err, "directory.invalid", 400)
	if !hasField(p, "specialty", "specialty_not_recognised") {
		t.Fatalf("errors: %+v", p.Errors)
	}
	// An undergraduate or other-specialty qualification does not unlock it.
	if err := f.m.RecordQualification(ctx, id, directory.Qualification{Degree: "MBBS Synthetic", Level: "ug"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RecordQualification(ctx, id, directory.Qualification{Degree: "MD Synthetic Skin", Level: "pg", Specialty: "dermatology"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpsertProfessional(ctx, docActor(id), in); err == nil {
		t.Fatal("specialty accepted without a matching PG qualification")
	}
	if err := f.m.RecordQualification(ctx, id, directory.Qualification{Degree: "MD Synthetic Heart", Level: "pg", Specialty: "cardiology"}, ids.New()); err != nil {
		t.Fatal(err)
	}
	got, err := f.m.UpsertProfessional(ctx, docActor(id), in)
	if err != nil || got.Specialty != "cardiology" {
		t.Fatalf("with qualification: %+v %v", got, err)
	}
	if directory.SpecialtyLabelKey("") != "directory.general_practice_mbbs" || directory.SpecialtyLabelKey("cardiology") != "directory.specialty.cardiology" {
		t.Fatal("label keys")
	}
	// Doctors cannot add qualifications for others or unknown people.
	if err := f.m.RecordQualification(ctx, ids.New(), directory.Qualification{Degree: "MD Synthetic", Level: "pg", Specialty: "x_y"}, ids.New()); err == nil {
		t.Fatal("qualification recorded for an unknown professional")
	}
	// Only doctors may write a doctor profile; and only their own (the id is the caller's).
	_, err = f.m.UpsertProfessional(ctx, directory.Actor{UserID: id, Role: ip.RoleClinicAdmin}, in)
	wantProblem(t, err, "auth.forbidden", 403)
	other := f.doctor(t, "Doctor T")
	if _, err := f.m.UpsertProfessional(ctx, docActor(other), directory.ProfessionalInput{DisplayName: "Changed Name", Languages: []string{"Hindi"}}); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := f.env.Owner.QueryRow(ctx, `SELECT display_name FROM directory.professional WHERE id=$1`, id).Scan(&name); err != nil || name != "Doctor S" {
		t.Fatalf("another doctor's write touched this profile: %q %v", name, err)
	}
}

func TestCD06NoClaimsInFreeText(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.doctor(t, "Doctor C")
	lead := id
	_, admin := f.clinic(t, "Plain Clinic", baseLat, baseLng, lead)
	pr := f.practice(t, admin, id, 1000)

	bad := []string{"Best doctor in town", "TOP surgeon", "No. 1 clinic", "Number one in care", "Award-winning care", "98% success rate", "100%", "#1", "guaranteed cure", "Our leading team"}
	for _, s := range bad {
		if _, err := f.m.UpsertProfessional(ctx, docActor(id), directory.ProfessionalInput{DisplayName: s, Languages: []string{"English"}}); err == nil {
			t.Errorf("display name %q accepted", s)
		}
		_, err := f.m.UpdatePractice(ctx, docActor(id), pr, directory.PracticeChange{Timings: ptr(s)})
		if err == nil {
			t.Errorf("timings %q accepted", s)
			continue
		}
		if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "timings", "claim_not_allowed") {
			t.Errorf("timings %q: %+v", s, p.Errors)
		}
		if _, err := f.m.SubmitClinic(ctx, directory.Actor{UserID: ids.New(), Role: "authenticated"}, directory.ClinicInput{Name: s + " Clinic", Address: "5 Sample Street", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: id}); err == nil {
			t.Errorf("clinic name %q accepted", s)
		}
	}
	for _, s := range []string{"Mon-Fri 09:00-13:00, Sat 10:00-12:00", "Closed on public holidays", "By appointment"} {
		if _, err := f.m.UpdatePractice(ctx, docActor(id), pr, directory.PracticeChange{Timings: ptr(s)}); err != nil {
			t.Errorf("plain timings %q refused: %v", s, err)
		}
	}
	// The list is configuration: a module built with another list uses it.
	cfg := testConfig()
	cfg.ClaimWords = []string{"zzzword"}
	g := setupWith(t, cfg)
	gid := g.doctor(t, "Doctor Zeta")
	if _, err := g.m.UpsertProfessional(ctx, docActor(gid), directory.ProfessionalInput{DisplayName: "Best Zeta", Languages: []string{"English"}}); err != nil {
		t.Fatalf("custom list should not include 'best': %v", err)
	}
	if _, err := g.m.UpsertProfessional(ctx, docActor(gid), directory.ProfessionalInput{DisplayName: "Zzzword Zeta", Languages: []string{"English"}}); err == nil {
		t.Fatal("custom claim word accepted")
	}
}

func TestTC20IdentifierNumbersRefusedInProfileText(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.doctor(t, "Doctor I")
	num := aadhaarLike(t)
	spaced := num[:4] + " " + num[4:8] + " " + num[8:]
	for _, s := range []string{num, spaced, "Call " + num + " now"} {
		_, err := f.m.UpsertProfessional(ctx, docActor(id), directory.ProfessionalInput{DisplayName: "Doctor " + s, Languages: []string{"English"}})
		if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "displayName", "id_number_not_allowed") {
			t.Fatalf("%q: %+v", s, p.Errors)
		}
	}
	_, err := f.m.SubmitClinic(ctx, directory.Actor{UserID: ids.New(), Role: "authenticated"}, directory.ClinicInput{Name: "Plain Clinic", Address: "Plot " + num + " Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: id})
	if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "address", "id_number_not_allowed") {
		t.Fatalf("address: %+v", p.Errors)
	}
	if n := f.count(t, `SELECT count(*) FROM directory.clinic`); n != 0 {
		t.Fatalf("clinic stored despite refusal: %d", n)
	}
	if strings.Contains(f.logs.String(), num) {
		t.Fatal("identifier number reached the logs")
	}
}

func TestPracticeFeeAndModesValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doc := f.doctor(t, "Doctor F")
	_, admin := f.clinic(t, "Fee Clinic", baseLat, baseLng, doc)
	mk := func(fee int64, modes ...string) error {
		_, err := f.m.CreatePractice(ctx, admin, admin.ClinicRef, directory.PracticeInput{DoctorRef: doc, Modes: modes, FeeAmount: fee})
		return err
	}
	for name, err := range map[string]error{
		"negative fee":  mk(-1, "in_person"),
		"fee too large": mk(1_000_001, "in_person"),
		"no modes":      mk(100),
		"unknown mode":  mk(100, "telepathy"),
		"duplicate":     mk(100, "video", "video"),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := mk(0, "video", "audio"); err != nil {
		t.Fatalf("valid practice: %v", err)
	}
	wantProblem(t, mk(100, "video"), "directory.practice_exists", 409)
}

func TestFeeVersionBumpsOnlyWhenFeeChanges(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pr, doc, _ := f.listedPractice(t, "Doctor V", baseLat, baseLng, 50000)
	before, err := f.m.GetPractice(ctx, pr)
	if err != nil || before.FeeVersion != "1" || before.FeeAmount != 50000 || before.Currency != "TST" {
		t.Fatalf("initial: %+v %v", before, err)
	}
	up, err := f.m.UpdatePractice(ctx, docActor(doc), pr, directory.PracticeChange{Timings: ptr("Mon 09:00-12:00")})
	if err != nil || up.FeeVersion != "1" || !up.FeeUpdatedAt.Equal(before2(t, f, pr)) {
		t.Fatalf("non-fee change bumped the fee version: %+v %v", up, err)
	}
	same, err := f.m.UpdatePractice(ctx, docActor(doc), pr, directory.PracticeChange{FeeAmount: ptr(int64(50000))})
	if err != nil || same.FeeVersion != "1" {
		t.Fatalf("same fee bumped the version: %+v %v", same, err)
	}
	changed, err := f.m.UpdatePractice(ctx, docActor(doc), pr, directory.PracticeChange{FeeAmount: ptr(int64(65000))})
	if err != nil || changed.FeeVersion != "2" || changed.FeeAmount != 65000 || !changed.FeeUpdatedAt.After(up.FeeUpdatedAt) {
		t.Fatalf("fee change: %+v %v", changed, err)
	}
	after, _ := f.m.GetPractice(ctx, pr)
	if after.FeeVersion != "2" || after.FeeAmount != 65000 {
		t.Fatalf("reader after change: %+v", after)
	}
	// A booking that copied (50000, TST, "1") keeps its own copy: the old version is still
	// distinguishable from the new one.
	if before.FeeVersion == after.FeeVersion {
		t.Fatal("fee version did not change")
	}
}

func before2(t testing.TB, f *fx, pr string) time.Time {
	t.Helper()
	var at time.Time
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT fee_updated_at FROM directory.practice WHERE id=$1`, pr).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestEveryChangeAuditedAndPracticeUpdatedEmitted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doc := f.doctor(t, "Doctor A1")                                                          // professional.upsert
	clinic, admin := f.clinic(t, "Audit Clinic", 1, 1, doc)                                  // clinic.create
	pr := f.practice(t, admin, doc, 100)                                                     // practice.create + event
	f.m.UpdatePractice(ctx, admin, pr, directory.PracticeChange{FeeAmount: ptr(int64(200))}) //nolint:errcheck
	f.m.UpdatePractice(ctx, docActor(doc), pr, directory.PracticeChange{Active: ptr(false)}) //nolint:errcheck
	_ = clinic
	for action, want := range map[string]int{"directory.professional.upsert": 1, "directory.clinic.create": 1, "directory.practice.create": 1, "directory.practice.update": 2} {
		if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action=$1`, action); n != want {
			t.Errorf("%s audit events = %d, want %d", action, n, want)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated' AND payload->>'practiceRef'=$1`, pr); n != 4 { // create + doctor acceptance (review M5) + 2 updates
		t.Errorf("practice.updated events = %d, want 4", n)
	}
	// A failed change leaves no audit event and no outbox event.
	nA := f.count(t, `SELECT count(*) FROM audit.audit_outbox`)
	nE := f.count(t, `SELECT count(*) FROM directory.event_outbox`)
	if _, err := f.m.UpdatePractice(ctx, admin, pr, directory.PracticeChange{FeeAmount: ptr(int64(-5))}); err == nil {
		t.Fatal("negative fee accepted")
	}
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox`) != nA || f.count(t, `SELECT count(*) FROM directory.event_outbox`) != nE {
		t.Fatal("rejected change left audit or outbox rows")
	}
}

// ---- ownership (K-SEC-04) ----

func TestOwnershipOthersGet404(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pr1, doc1, c1 := f.listedPractice(t, "Doctor O1", baseLat, baseLng, 100)
	_, doc2, c2 := f.listedPractice(t, "Doctor O2", baseLat+1, baseLng, 100)
	admin1 := adminActor(ids.New(), c1)
	admin2 := adminActor(ids.New(), c2)

	change := directory.PracticeChange{FeeAmount: ptr(int64(1))}
	for name, a := range map[string]directory.Actor{
		"other doctor":                 docActor(doc2),
		"other clinic admin":           admin2,
		"admin of own clinic wrong id": adminActor(ids.New(), ids.New()),
		"authenticated user":           {UserID: ids.New(), Role: "authenticated"},
		"no role":                      {UserID: ids.New()},
	} {
		_, err := f.m.UpdatePractice(ctx, a, pr1, change)
		wantProblem(t, err, "resource.not_found", 404)
		_ = name
	}
	if _, err := f.m.UpdatePractice(ctx, docActor(doc1), pr1, change); err != nil {
		t.Fatalf("own doctor: %v", err)
	}
	if _, err := f.m.UpdatePractice(ctx, admin1, pr1, directory.PracticeChange{FeeAmount: ptr(int64(2))}); err != nil {
		t.Fatalf("clinic admin of the practice's clinic: %v", err)
	}
	// Unknown practice answers the same as someone else's.
	_, err := f.m.UpdatePractice(ctx, docActor(doc1), ids.New(), change)
	wantProblem(t, err, "resource.not_found", 404)
	_, err = f.m.UpdatePractice(ctx, docActor(doc1), "not-a-uuid", change)
	wantProblem(t, err, "resource.not_found", 404)

	// A clinic admin cannot add practices to someone else's clinic.
	_, err = f.m.CreatePractice(ctx, admin2, c1, directory.PracticeInput{DoctorRef: doc2, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "resource.not_found", 404)
	_, err = f.m.CreatePractice(ctx, docActor(doc1), c1, directory.PracticeInput{DoctorRef: doc1, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "resource.not_found", 404)
	var fee int64
	if err := f.env.Owner.QueryRow(ctx, `SELECT fee_amount FROM directory.practice WHERE id=$1`, pr1).Scan(&fee); err != nil || fee != 2 {
		t.Fatalf("fee = %d %v", fee, err)
	}
}

// ---- S-117: clinic submission ----

func TestSubmitClinicRolesGeocodingAndListing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Lead L")
	user := ids.New()
	a := directory.Actor{UserID: user, Role: "authenticated"}
	f.geo.Add("5 Sample Street, Testville", "XX", geocoding.Result{Lat: 20.5, Lng: 77.5, Accuracy: geocoding.Rooftop})

	c, err := f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Geo Clinic", Address: "5 Sample Street, Testville", LeadDoctorRef: lead})
	if err != nil {
		t.Fatal(err)
	}
	if c.LocationSource != "geocoded" || c.Lat != 20.5 || c.Listed {
		t.Fatalf("clinic: %+v", c)
	}
	if len(f.roles.assigned) != 1 || f.roles.assigned[0] != [4]string{user, ip.RoleClinicAdmin, c.ID, user} {
		t.Fatalf("role assignment: %v", f.roles.assigned)
	}
	// Not listed until BOTH the clinic and the lead are verified.
	doc := f.doctor(t, "Doc M")
	admin := adminActor(user, c.ID)
	pr := f.practice(t, admin, doc, 100)
	f.acceptAll(t, lead)
	f.verify(t, doc)
	if len(f.search(t, f.at(20.5, 77.5, 5)).Items) != 0 {
		t.Fatal("listed with an unverified clinic and lead")
	}
	f.verify(t, c.ID)
	if len(f.search(t, f.at(20.5, 77.5, 5)).Items) != 0 {
		t.Fatal("listed with an unverified lead doctor")
	}
	f.verify(t, lead)
	if got := practiceIDs(f.search(t, f.at(20.5, 77.5, 5)).Items); len(got) != 1 || got[0] != pr {
		t.Fatalf("not listed after everyone verified: %v", got)
	}
	// The doctor's own clinic-less lookups: profile shows what was checked.
	p, err := f.m.GetProfile(ctx, pr)
	if err != nil || len(p.Checks) != 1 || p.Checks[0] != "registration_active" || p.TimeZone != "UTC" || p.Clinic.Lat != 20.5 {
		t.Fatalf("profile: %+v %v", p, err)
	}

	// Manual pin wins and the geocoder is not called for it.
	calls := f.geo.Calls()
	pin, err := f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Pin Clinic", Address: "Nowhere Known 1", Pin: &directory.Point{Lat: 10, Lng: 10}, LeadDoctorRef: lead})
	if err != nil || pin.LocationSource != "manual_pin" || f.geo.Calls() != calls {
		t.Fatalf("manual pin: %+v %v (calls %d->%d)", pin, err, calls, f.geo.Calls())
	}
	// Geocoder trouble: ask for a manual pin; nothing is created and no role is granted.
	nRoles, nClinics := len(f.roles.assigned), f.count(t, `SELECT count(*) FROM directory.clinic`)
	_, err = f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Lost Clinic", Address: "Unknown Lane 9", LeadDoctorRef: lead})
	wantProblem(t, err, "directory.address_not_located", 422)
	f.geo.Add("7 Vague Road", "XX", geocoding.Result{Lat: 1, Lng: 1, Accuracy: geocoding.Region})
	_, err = f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Vague Clinic", Address: "7 Vague Road", LeadDoctorRef: lead})
	wantProblem(t, err, "directory.address_not_located", 422)
	f.geo.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	_, err = f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Down Clinic", Address: "Unknown Lane 9", LeadDoctorRef: lead})
	wantProblem(t, err, "directory.geocoding_unavailable", 503)
	f.geo.FailWith = nil
	if len(f.roles.assigned) != nRoles || f.count(t, `SELECT count(*) FROM directory.clinic`) != nClinics {
		t.Fatal("failed submissions created a clinic or a role")
	}
	// Unknown lead doctor; bad pin; bad time zone; HFR format.
	for name, in := range map[string]directory.ClinicInput{
		"unknown lead": {Name: "Clinic X", Address: "9 Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: ids.New()},
		"bad pin":      {Name: "Clinic X", Address: "9 Sample Road", Pin: &directory.Point{Lat: 100, Lng: 1}, LeadDoctorRef: lead},
		"bad zone":     {Name: "Clinic X", Address: "9 Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: lead, TimeZone: "Mars/Olympus"},
		"bad hfr":      {Name: "Clinic X", Address: "9 Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: lead, HFRID: "no spaces!"},
	} {
		if _, err := f.m.SubmitClinic(ctx, a, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A role-assignment failure creates no clinic.
	f.roles.failWith = errors.New("identity down")
	_, err = f.m.SubmitClinic(ctx, a, directory.ClinicInput{Name: "Role Clinic", Address: "9 Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: lead})
	wantProblem(t, err, "directory.role_assignment_failed", 503)
	if f.count(t, `SELECT count(*) FROM directory.clinic`) != nClinics {
		t.Fatal("clinic created although the role could not be assigned")
	}
}

func TestRegistrationDocRequiredWhenConfigured(t *testing.T) {
	cfg := testConfig()
	cfg.RequireRegistrationDoc = true
	f := setupWith(t, cfg)
	lead := f.doctor(t, "Lead R")
	in := directory.ClinicInput{Name: "Reg Clinic", Address: "9 Sample Road", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: lead}
	_, err := f.m.SubmitClinic(context.Background(), directory.Actor{UserID: ids.New(), Role: "authenticated"}, in)
	if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "registrationDocRef", "required") {
		t.Fatalf("%+v", p.Errors)
	}
	in.RegistrationDocRef = "7c1f0c3e-9d3a-4a43-8f2e-5b1d2a9e4c10"
	in.HFRID = "HFR-0001"
	if _, err := f.m.SubmitClinic(context.Background(), directory.Actor{UserID: ids.New(), Role: "authenticated"}, in); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), directory.Source()}})
	pool := env.Pools.For("directory")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mut := map[string]func(*directory.Config){
		"currency":   func(c *directory.Config) { c.Currency = "" },
		"country":    func(c *directory.Config) { c.CountryCode = "" },
		"validators": func(c *directory.Config) { c.ProhibitedIDValidators = nil },
		"radius":     func(c *directory.Config) { c.DefaultRadiusKm = c.MaxRadiusKm + 1 },
		"fee":        func(c *directory.Config) { c.MaxFeeMinor = 0 },
		"claims":     func(c *directory.Config) { c.ClaimWords = nil },
		"accuracy":   func(c *directory.Config) { c.MinGeocodeAccuracy = "" },
		"zone":       func(c *directory.Config) { c.DefaultTimeZone = "Mars/Base" },
	}
	for name, m := range mut {
		cfg := testConfig()
		m(&cfg)
		if _, err := directory.New(pool, cfg, &geofake.Geocoder{}, &roleFake{}, log); err == nil {
			t.Errorf("%s: config accepted", name)
		}
	}
	if _, err := directory.New(pool, testConfig(), nil, &roleFake{}, log); err == nil {
		t.Error("nil geocoder accepted")
	}
}

// acceptAll accepts every pending invitation of a doctor (the fixtures' stand-in for the
// doctor tapping Accept).
func (f *fx) acceptAll(t testing.TB, doctor string) {
	t.Helper()
	list, err := f.m.MyInvitations(context.Background(), docActor(doctor))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range list {
		if err := f.m.AcceptInvitation(context.Background(), docActor(doctor), i.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// Review M5: a lead-doctor link and a practice for another doctor do not list until that
// doctor accepts; a doctor creating a practice for themself is accepted at once; only the
// invited doctor can decide (others get 404); decisions are audited.
func TestReviewM5DoctorMustAcceptBeforeListing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Dr Lead")
	other := f.doctor(t, "Dr Other")
	intruder := f.doctor(t, "Dr Intruder")
	admin := ids.New()
	c, err := f.m.SubmitClinic(ctx, directory.Actor{UserID: admin, Role: "authenticated"}, directory.ClinicInput{
		Name: "Accept Clinic", Address: "12 Sample Road, Testville", Pin: &directory.Point{Lat: 12.9, Lng: 77.6}, LeadDoctorRef: lead})
	if err != nil {
		t.Fatal(err)
	}
	ad := adminActor(admin, c.ID)
	pr, err := f.m.CreatePractice(ctx, ad, c.ID, directory.PracticeInput{DoctorRef: other, Modes: []string{directory.ModeInPerson}, FeeAmount: 500, Timings: "Mon 09:00-12:00"})
	if err != nil {
		t.Fatal(err)
	}
	f.verify(t, lead, other, c.ID)
	listed := func() bool {
		res := f.search(t, f.at(12.9, 77.6, 5))
		return len(res.Items) > 0
	}
	if listed() {
		t.Fatal("nothing may list before the doctors accept")
	}
	if _, err := f.m.GetProfile(ctx, pr.ID); err == nil {
		t.Fatal("profile of an unaccepted practice")
	}
	if _, err := f.m.GetPractice(ctx, pr.ID); err == nil {
		t.Fatal("reader returned an unaccepted practice")
	}
	inv, err := f.m.MyInvitations(ctx, docActor(other))
	if err != nil || len(inv) != 1 || inv[0].Kind != "practice" {
		t.Fatalf("%+v %v", inv, err)
	}
	// Others (another doctor, a clinic admin) cannot decide: 404.
	if err := f.m.AcceptInvitation(ctx, docActor(intruder), inv[0].ID); !errors.Is(err, problem.NotFound) {
		t.Fatalf("intruder: %v", err)
	}
	if err := f.m.AcceptInvitation(ctx, ad, inv[0].ID); !errors.Is(err, problem.NotFound) {
		t.Fatalf("admin cannot accept for the doctor: %v", err)
	}
	if err := f.m.AcceptInvitation(ctx, docActor(other), inv[0].ID); err != nil {
		t.Fatal(err)
	}
	if listed() {
		t.Fatal("the lead doctor has not accepted yet")
	}
	li, _ := f.m.MyInvitations(ctx, docActor(lead))
	if len(li) != 1 || li[0].Kind != "clinic_lead" {
		t.Fatalf("%+v", li)
	}
	if err := f.m.AcceptInvitation(ctx, docActor(lead), li[0].ID); err != nil {
		t.Fatal(err)
	}
	if !listed() {
		t.Fatal("accepted and verified must list")
	}
	if err := f.m.AcceptInvitation(ctx, docActor(lead), li[0].ID); !errors.Is(err, problem.NotFound) {
		t.Fatalf("second decision: %v", err)
	}
	var n int
	if err := f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action IN ('directory.invitation.create','directory.invitation.accept')`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("audit events: %d %v", n, err)
	}
	// A declined invitation never lists.
	third := f.doctor(t, "Dr Third")
	p3, err := f.m.CreatePractice(ctx, ad, c.ID, directory.PracticeInput{DoctorRef: third, Modes: []string{directory.ModeInPerson}, FeeAmount: 500, Timings: "Tue"})
	if err != nil {
		t.Fatal(err)
	}
	f.verify(t, third)
	ti, _ := f.m.MyInvitations(ctx, docActor(third))
	if err := f.m.DeclineInvitation(ctx, docActor(third), ti[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.GetPractice(ctx, p3.ID); err == nil {
		t.Fatal("declined practice listed")
	}
}

func TestReviewM5SelfPracticeAcceptedAtOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doc := f.doctor(t, "Dr Self")
	c, err := f.m.SubmitClinic(ctx, directory.Actor{UserID: doc, Role: "authenticated"}, directory.ClinicInput{
		Name: "Self Clinic", Address: "12 Sample Road, Testville", Pin: &directory.Point{Lat: 12.9, Lng: 77.6}, LeadDoctorRef: doc})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.m.CreatePractice(ctx, adminActor(doc, c.ID), c.ID, directory.PracticeInput{DoctorRef: doc, Modes: []string{directory.ModeInPerson}, FeeAmount: 500, Timings: "Mon"})
	if err != nil {
		t.Fatal(err)
	}
	f.verify(t, doc, c.ID)
	if _, err := f.m.GetPractice(ctx, p.ID); err != nil {
		t.Fatalf("self-created practice should list: %v", err)
	}
	if inv, _ := f.m.MyInvitations(ctx, docActor(doc)); len(inv) != 0 {
		t.Fatalf("no invitations expected: %+v", inv)
	}
}

func TestReviewM5RefsMustBeOpaqueIDs(t *testing.T) {
	f := setup(t)
	id := ids.New()
	_, err := f.m.UpsertProfessional(context.Background(), docActor(id), directory.ProfessionalInput{DisplayName: "Dr Photo", PhotoRef: "../../etc/passwd"})
	if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "photoRef", "invalid_format") {
		t.Fatalf("%v", err)
	}
	if _, err := f.m.UpsertProfessional(context.Background(), docActor(id), directory.ProfessionalInput{DisplayName: "Dr Photo", PhotoRef: ids.New()}); err != nil {
		t.Fatal(err)
	}
}

// ---- D-38: clinic verification events (subjectKind = clinic) ----

func (f *fx) clinicStatus(t testing.TB, ref, status string, at time.Time) {
	t.Helper()
	e := outbox.Event{ID: ids.New(), Type: "verification.status_changed", OccurredAt: at,
		Payload: map[string]string{"professionalRef": ref, "status": status, "subjectKind": "clinic"}}
	if err := f.m.OnVerificationStatusChanged(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

// TC-44 / TC-66: a clinic event lists the clinic's practices once doctor and lead are
// Verified, and a clinic suspension delists every practice of the clinic at once and
// announces each of them (booking cancels from practice.updated and the status event).
func TestTC44ClinicSuspensionDelistsAllPractices(t *testing.T) {
	f := setup(t)
	lead := f.doctor(t, "Lead Doctor")
	d2 := f.doctor(t, "Second Doctor")
	clinic, admin := f.clinic(t, "Sample Clinic", baseLat+kmLat, baseLng, lead)
	p1 := f.practice(t, admin, lead, 40000)
	p2 := f.practice(t, admin, d2, 50000)
	f.verify(t, lead, d2)
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 0 {
		t.Fatalf("listed %d before the clinic is verified", n)
	}
	f.clinicStatus(t, clinic, "verified", f.tick())
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 2 {
		t.Fatalf("listed %d after the clinic is verified, want 2", n)
	}
	f.clinicStatus(t, clinic, "suspended", f.tick())
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 0 {
		t.Fatalf("listed %d after the clinic is suspended, want 0", n)
	}
	for _, p := range []string{p1, p2} {
		if _, err := f.m.GetProfile(context.Background(), p); err == nil {
			t.Fatalf("practice %s still readable after clinic suspension", p)
		}
		if n := f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated' AND payload->>'practiceRef'=$1`, p); n < 2 {
			t.Fatalf("practice %s: practice.updated events = %d, want one per clinic status change", p, n)
		}
	}
}

func TestVerificationEventUnknownSubjectKindFailsClosed(t *testing.T) {
	f := setup(t)
	e := outbox.Event{ID: ids.New(), Type: "verification.status_changed", OccurredAt: f.tick(),
		Payload: map[string]string{"professionalRef": ids.New(), "status": "verified", "subjectKind": "pharmacy"}}
	if err := f.m.OnVerificationStatusChanged(context.Background(), e); err == nil {
		t.Fatal("unknown subject kind accepted")
	}
}

// TM-27: a link, domain or markup in a doctor, clinic or branch name (or address) is refused;
// ordinary names with initials and abbreviations are accepted.
func TestTM27LinksAndMarkupRefusedInProfileNames(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := f.doctor(t, "Doctor L")
	_, admin := f.clinic(t, "Link Clinic", baseLat, baseLng, id)
	bad := []string{"Dr X https://x.y", "bit.ly/abc", "Clinic evil.org", "<script>alert(1)</script>", "Dr {x}", "t.me/abc", "a.io"}
	for _, s := range bad {
		_, err := f.m.UpsertProfessional(ctx, docActor(id), directory.ProfessionalInput{DisplayName: s, Languages: []string{"English"}})
		if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "displayName", "link_or_markup_not_allowed") {
			t.Errorf("displayName %q: %+v", s, p.Errors)
		}
		_, err = f.m.SubmitClinic(ctx, directory.Actor{UserID: ids.New(), Role: "authenticated"}, directory.ClinicInput{Name: s, Address: "5 Sample Street", Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: id})
		if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "name", "link_or_markup_not_allowed") {
			t.Errorf("clinic name %q: %+v", s, p.Errors)
		}
		// Addresses use the loose rule: a bare domain-like token is allowed there ("Opp.Bus Stand").
		if s != "Clinic evil.org" && s != "a.io" {
			_, err = f.m.SubmitClinic(ctx, directory.Actor{UserID: ids.New(), Role: "authenticated"}, directory.ClinicInput{Name: "Plain Clinic", Address: "5 Sample Street " + s, Pin: &directory.Point{Lat: 1, Lng: 1}, LeadDoctorRef: id})
			if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "address", "link_or_markup_not_allowed") {
				t.Errorf("clinic address %q: %+v", s, p.Errors)
			}
		}
		_, err = f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: s, Address: "9 Branch Road", Pin: &directory.Point{Lat: 1, Lng: 1}})
		if p := wantProblem(t, err, "directory.invalid", 400); !hasField(p, "name", "link_or_markup_not_allowed") {
			t.Errorf("branch name %q: %+v", s, p.Errors)
		}
		_, err = f.m.UpdateBranch(ctx, admin, admin.ClinicRef, admin.ClinicRef, directory.BranchChange{Name: ptr(s)})
		if err == nil {
			t.Errorf("branch rename %q accepted", s)
		}
	}
	for _, s := range []string{"Dr. A. Kumar", "St. John's Clinic", "R.K. Nagar Clinic", "Dr. S. Ravi, M.B.B.S."} {
		if _, err := f.m.UpsertProfessional(ctx, docActor(id), directory.ProfessionalInput{DisplayName: s, Languages: []string{"English"}}); err != nil {
			t.Errorf("display name %q refused: %v", s, err)
		}
		if _, err := f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: s, Address: "Sector 5, Phase 2", Pin: &directory.Point{Lat: 1, Lng: 1}}); err != nil {
			t.Errorf("branch name %q refused: %v", s, err)
		}
	}
	// Real Indian qualifications and addresses with dotted abbreviations are accepted.
	if err := f.m.RecordQualification(ctx, id, directory.Qualification{Degree: "M.Ch (Urology)", Level: "pg", Specialty: "cardiology"}, ids.New()); err != nil {
		t.Errorf("degree M.Ch refused: %v", err)
	}
	if _, err := f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "Opp Branch", Address: "Opp.Bus Stand, Nr.Temple Rd.", Pin: &directory.Point{Lat: 1, Lng: 1}}); err != nil {
		t.Errorf("address with abbreviations refused: %v", err)
	}
}
