package directory_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// branch adds a branch with a manual pin to the clinic and returns its ID.
func (f *fx) branch(t testing.TB, admin directory.Actor, name string, lat, lng float64) string {
	t.Helper()
	b, err := f.m.CreateBranch(context.Background(), admin, admin.ClinicRef, directory.BranchInput{
		Name: name, Address: "1 Branch Road, Testville", Pin: &directory.Point{Lat: lat, Lng: lng}})
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

// branchPractice lists a doctor at a branch of the admin's clinic.
func (f *fx) branchPractice(t testing.TB, admin directory.Actor, doctor, branch string, fee int64) string {
	t.Helper()
	p, err := f.m.CreatePractice(context.Background(), admin, admin.ClinicRef, directory.PracticeInput{
		DoctorRef: doctor, BranchRef: branch, Modes: []string{directory.ModeInPerson}, FeeAmount: fee})
	if err != nil {
		t.Fatal(err)
	}
	f.acceptAll(t, doctor)
	return p.ID
}

// ---- S-130 / E-GEO-02: each branch is located separately ----

func TestEGEO02EachBranchLocatedAndSearchedSeparately(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead, other := f.doctor(t, "Lead Doctor"), f.doctor(t, "Branch Doctor")
	clinic, admin := f.clinic(t, "Multi Clinic", baseLat, baseLng, lead)
	mainPr := f.practice(t, admin, lead, 100)
	// The second branch is about 20 km away.
	b2 := f.branch(t, admin, "Northside", baseLat+20*kmLat, baseLng)
	branchPr := f.branchPractice(t, admin, other, b2, 200)
	f.verify(t, lead, other, clinic)

	// Near the main branch only its practice is found, with the main branch's location.
	res := f.search(t, f.at(baseLat, baseLng, 5))
	if got := practiceIDs(res.Items); len(got) != 1 || got[0] != mainPr {
		t.Fatalf("near main: %v", got)
	}
	if res.Items[0].Clinic.BranchID != clinic || res.Items[0].Clinic.Lat != baseLat {
		t.Fatalf("main branch card: %+v", res.Items[0].Clinic)
	}
	// Near the second branch only that branch's practice is found, at its own address and point.
	res = f.search(t, f.at(baseLat+20*kmLat, baseLng, 5))
	if got := practiceIDs(res.Items); len(got) != 1 || got[0] != branchPr {
		t.Fatalf("near branch: %v", got)
	}
	c := res.Items[0].Clinic
	if c.ID != clinic || c.BranchID != b2 || c.BranchName != "Northside" || c.Address != "1 Branch Road, Testville" || c.Lat == baseLat {
		t.Fatalf("branch card: %+v", c)
	}
	if res.Items[0].DistanceKm > 0.2 {
		t.Fatalf("distance is measured to the branch, got %v km", res.Items[0].DistanceKm)
	}
	// A wide search returns both, each at its own location, nearest first.
	res = f.search(t, f.at(baseLat+19*kmLat, baseLng, 50))
	if got := practiceIDs(res.Items); len(got) != 2 || got[0] != branchPr || got[1] != mainPr {
		t.Fatalf("wide: %v", got)
	}
	// The public profile carries the branch location too.
	p, err := f.m.GetProfile(ctx, branchPr)
	if err != nil || p.Clinic.BranchID != b2 || p.Clinic.Lat != c.Lat {
		t.Fatalf("profile: %+v %v", p, err)
	}
}

func TestS130BranchGeocodedWithMinimumAccuracy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Lead Doctor")
	_, admin := f.clinic(t, "Geo Branch Clinic", baseLat, baseLng, lead)
	f.geo.Add("8 Branch Lane, Testville", "XX", geocoding.Result{Lat: 21, Lng: 78, Accuracy: geocoding.Rooftop})
	b, err := f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "Geo Branch", Address: "8 Branch Lane, Testville"})
	if err != nil || b.LocationSource != "geocoded" || b.Lat != 21 || b.Lng != 78 || b.TimeZone != "UTC" || b.Main {
		t.Fatalf("branch: %+v %v", b, err)
	}
	// Too vague, unknown and a down geocoder ask for a pin and create nothing.
	f.geo.Add("9 Vague Road", "XX", geocoding.Result{Lat: 1, Lng: 1, Accuracy: geocoding.Region})
	n := f.count(t, `SELECT count(*) FROM directory.branch`)
	_, err = f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "Vague", Address: "9 Vague Road"})
	wantProblem(t, err, "directory.address_not_located", 422)
	_, err = f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "Unknown", Address: "Unknown Lane 77"})
	wantProblem(t, err, "directory.address_not_located", 422)
	f.geo.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	_, err = f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "Down", Address: "Unknown Lane 77"})
	wantProblem(t, err, "directory.geocoding_unavailable", 503)
	f.geo.FailWith = nil
	if f.count(t, `SELECT count(*) FROM directory.branch`) != n {
		t.Fatal("a failed branch left a row")
	}
	// Validation: zone, pin, duplicate name, ID numbers.
	for name, in := range map[string]directory.BranchInput{
		"bad zone":  {Name: "Zone", Address: "9 Branch Road", Pin: &directory.Point{Lat: 1, Lng: 1}, TimeZone: "Mars/Olympus"},
		"bad pin":   {Name: "Pin", Address: "9 Branch Road", Pin: &directory.Point{Lat: 100, Lng: 1}},
		"id number": {Name: "Id", Address: aadhaarLike(t), Pin: &directory.Point{Lat: 1, Lng: 1}},
		"short":     {Name: "x", Address: "9 Branch Road", Pin: &directory.Point{Lat: 1, Lng: 1}},
	} {
		if _, err := f.m.CreateBranch(ctx, admin, admin.ClinicRef, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	_, err = f.m.CreateBranch(ctx, admin, admin.ClinicRef, directory.BranchInput{Name: "geo branch", Address: "8 Branch Lane, Testville", Pin: &directory.Point{Lat: 1, Lng: 1}})
	wantProblem(t, err, "directory.branch_exists", 409)
}

func TestS130BranchListedOnlyWhileClinicVerified(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead, other := f.doctor(t, "Lead Doctor"), f.doctor(t, "Branch Doctor")
	clinic, admin := f.clinic(t, "Suspend Clinic", baseLat, baseLng, lead)
	b2 := f.branch(t, admin, "Second", baseLat+kmLat, baseLng)
	pr := f.branchPractice(t, admin, other, b2, 100)
	f.verify(t, lead, other)
	// The clinic is not Verified: nothing is listed, and the admin sees the branch as not listed.
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 0 {
		t.Fatalf("listed before the clinic is verified: %d", n)
	}
	list, err := f.m.ListBranches(ctx, admin, clinic)
	if err != nil || len(list) != 2 || !list[0].Main || list[0].ID != clinic || list[0].Listed || list[1].Listed {
		t.Fatalf("list: %+v %v", list, err)
	}
	f.verify(t, clinic)
	if got := practiceIDs(f.search(t, f.at(baseLat, baseLng, 10)).Items); len(got) != 1 || got[0] != pr {
		t.Fatalf("not listed after verification: %v", got)
	}
	list, _ = f.m.ListBranches(ctx, admin, clinic)
	if !list[0].Listed || !list[1].Listed {
		t.Fatalf("branches should be listed: %+v", list)
	}
	// A suspension of the clinic hides every branch at once (K-CS-01).
	f.status(t, clinic, "suspended", f.tick())
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 0 {
		t.Fatalf("suspended clinic still listed: %d", n)
	}
	if _, err := f.m.GetProfile(ctx, pr); err == nil {
		t.Fatal("profile of a suspended clinic's branch answered")
	}
	list, _ = f.m.ListBranches(ctx, admin, clinic)
	if list[0].Listed || list[1].Listed {
		t.Fatalf("branches listed while suspended: %+v", list)
	}
}

func TestS130SwitchingABranchOffHidesItsPracticesAndMainStaysOn(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead, other := f.doctor(t, "Lead Doctor"), f.doctor(t, "Branch Doctor")
	clinic, admin := f.clinic(t, "Toggle Clinic", baseLat, baseLng, lead)
	mainPr := f.practice(t, admin, lead, 100)
	b2 := f.branch(t, admin, "Second", baseLat+kmLat, baseLng)
	pr := f.branchPractice(t, admin, other, b2, 100)
	f.verify(t, lead, other, clinic)
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 2 {
		t.Fatalf("want 2, got %d", n)
	}
	before := f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated'`)
	b, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Active: ptr(false)})
	if err != nil || b.Active || b.Listed {
		t.Fatalf("switch off: %+v %v", b, err)
	}
	if got := practiceIDs(f.search(t, f.at(baseLat, baseLng, 10)).Items); len(got) != 1 || got[0] != mainPr {
		t.Fatalf("after off: %v", got)
	}
	if _, err := f.m.GetPractice(ctx, pr); !errors.Is(err, dp.ErrNotFound) {
		t.Fatalf("reader still answers: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM directory.event_outbox WHERE type='practice.updated'`) != before+1 {
		t.Fatal("booking was not told the branch's practice changed")
	}
	_, err = f.m.UpdateBranch(ctx, admin, clinic, clinic, directory.BranchChange{Active: ptr(false)})
	wantProblem(t, err, "directory.main_branch_required", 409)
	// Back on.
	if _, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Active: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.search(t, f.at(baseLat, baseLng, 10)).Items); n != 2 {
		t.Fatalf("want 2 after switching on, got %d", n)
	}
	// A practice cannot be added to an inactive branch.
	if _, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Active: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	third := f.doctor(t, "Third Doctor")
	_, err = f.m.CreatePractice(ctx, admin, clinic, directory.PracticeInput{DoctorRef: third, BranchRef: b2, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "resource.not_found", 404)
}

func TestS130UpdateBranchMovesLocationAndZone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead, other := f.doctor(t, "Lead Doctor"), f.doctor(t, "Branch Doctor")
	clinic, admin := f.clinic(t, "Move Clinic", baseLat, baseLng, lead)
	b2 := f.branch(t, admin, "Second", baseLat+kmLat, baseLng)
	pr := f.branchPractice(t, admin, other, b2, 100)
	f.verify(t, lead, other, clinic)

	b, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Pin: &directory.Point{Lat: baseLat + 30*kmLat, Lng: baseLng}, TimeZone: ptr("Asia/Dubai"), Name: ptr("Far Branch")})
	if err != nil || b.Name != "Far Branch" || b.TimeZone != "Asia/Dubai" || b.LocationSource != "manual_pin" || b.Address != "1 Branch Road, Testville" {
		t.Fatalf("update: %+v %v", b, err)
	}
	// The reader (booking) sees the branch's zone and ref, not the clinic's (K-ARC-04).
	p, err := f.m.GetPractice(ctx, pr)
	if err != nil || p.TimeZone != "Asia/Dubai" || p.BranchRef != b2 || p.ClinicRef != clinic {
		t.Fatalf("reader: %+v %v", p, err)
	}
	if n := len(f.search(t, f.at(baseLat, baseLng, 5)).Items); n != 0 {
		t.Fatalf("the practice still shows at the old place: %d", n)
	}
	if n := len(f.search(t, f.at(baseLat+30*kmLat, baseLng, 5)).Items); n != 1 {
		t.Fatalf("the practice is not at the new place: %d", n)
	}
	// A new address is located again; a bad one changes nothing.
	f.geo.Add("20 New Road, Testville", "XX", geocoding.Result{Lat: 22, Lng: 79, Accuracy: geocoding.Rooftop})
	b, err = f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Address: ptr("20 New Road, Testville")})
	if err != nil || b.Lat != 22 || b.LocationSource != "geocoded" {
		t.Fatalf("re-geocode: %+v %v", b, err)
	}
	_, err = f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Address: ptr("Unknown Lane 77")})
	wantProblem(t, err, "directory.address_not_located", 422)
	if _, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{}); err == nil {
		t.Fatal("empty update accepted")
	}
	// The main branch keeps the clinic row in step.
	if _, err := f.m.UpdateBranch(ctx, admin, clinic, clinic, directory.BranchChange{Pin: &directory.Point{Lat: 5, Lng: 5}}); err != nil {
		t.Fatal(err)
	}
	var lat float64
	if err := f.env.Owner.QueryRow(ctx, `SELECT ST_Y(geo::geometry) FROM directory.clinic WHERE id=$1`, clinic).Scan(&lat); err != nil || lat != 5 {
		t.Fatalf("clinic row: %v %v", lat, err)
	}
}

func TestS130BranchesAreOwnClinicOnly404(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead1, lead2, doc2 := f.doctor(t, "Lead One"), f.doctor(t, "Lead Two"), f.doctor(t, "Doc Two")
	c1, admin1 := f.clinic(t, "Clinic One", baseLat, baseLng, lead1)
	c2, admin2 := f.clinic(t, "Clinic Two", baseLat+1, baseLng, lead2)
	b1 := f.branch(t, admin1, "One B", baseLat, baseLng+0.1)
	in := directory.BranchInput{Name: "Intruder", Address: "5 Branch Road", Pin: &directory.Point{Lat: 1, Lng: 1}}

	for name, a := range map[string]directory.Actor{
		"other clinic admin": admin2,
		"doctor":             docActor(lead1),
		"front desk":         {UserID: ids.New(), Role: "front_desk", ClinicRef: c1},
		"authenticated":      {UserID: ids.New(), Role: "authenticated"},
		"admin wrong header": adminActor(ids.New(), ids.New()),
	} {
		if _, err := f.m.CreateBranch(ctx, a, c1, in); err == nil {
			t.Errorf("%s created a branch", name)
		} else {
			wantProblem(t, err, "resource.not_found", 404)
		}
		if _, err := f.m.ListBranches(ctx, a, c1); err == nil {
			t.Errorf("%s listed branches", name)
		}
		_, err := f.m.UpdateBranch(ctx, a, c1, b1, directory.BranchChange{Name: ptr("Hijack")})
		wantProblem(t, err, "resource.not_found", 404)
	}
	// Own clinic, other clinic's branch ID: the same answer as an unknown one.
	_, err := f.m.UpdateBranch(ctx, admin2, c2, b1, directory.BranchChange{Name: ptr("Hijack")})
	wantProblem(t, err, "resource.not_found", 404)
	_, err = f.m.UpdateBranch(ctx, admin2, c2, ids.New(), directory.BranchChange{Name: ptr("Hijack")})
	wantProblem(t, err, "resource.not_found", 404)
	_, err = f.m.CreatePractice(ctx, admin2, c2, directory.PracticeInput{DoctorRef: doc2, BranchRef: b1, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "resource.not_found", 404)
	var name string
	if err := f.env.Owner.QueryRow(ctx, `SELECT name FROM directory.branch WHERE id=$1`, b1).Scan(&name); err != nil || name != "One B" {
		t.Fatalf("branch changed: %q %v", name, err)
	}
}

func TestS130BranchChangesAreAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Lead Doctor")
	clinic, admin := f.clinic(t, "Audit Clinic", baseLat, baseLng, lead)
	n := f.count(t, `SELECT count(*) FROM audit.audit_outbox`)
	b2 := f.branch(t, admin, "Second", baseLat, baseLng+0.1)
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox`) != n+1 {
		t.Fatal("create not audited")
	}
	if _, err := f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Name: ptr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox`) != n+2 {
		t.Fatal("update not audited")
	}
	// A refused change leaves no audit row.
	_, _ = f.m.UpdateBranch(ctx, admin, clinic, b2, directory.BranchChange{Name: ptr("x")})
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox`) != n+2 {
		t.Fatal("refused change audited")
	}
}

// ---- S-130: a doctor at several clinics and several branches ----

func TestS130DoctorPractisesAtSeveralClinicsAndBranches(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doc, lead1, lead2 := f.doctor(t, "Shared Doctor"), f.doctor(t, "Lead One"), f.doctor(t, "Lead Two")
	c1, admin1 := f.clinic(t, "Clinic One", baseLat, baseLng, lead1)
	c2, admin2 := f.clinic(t, "Clinic Two", baseLat+10*kmLat, baseLng, lead2)
	b2 := f.branch(t, admin1, "One North", baseLat+20*kmLat, baseLng)
	if _, err := f.m.UpdateBranch(ctx, admin1, c1, b2, directory.BranchChange{TimeZone: ptr("Asia/Dubai")}); err != nil {
		t.Fatal(err)
	}
	p1 := f.practice(t, admin1, doc, 100)           // clinic one, main branch
	p2 := f.branchPractice(t, admin1, doc, b2, 150) // clinic one, second branch
	p3 := f.practice(t, admin2, doc, 200)           // clinic two
	f.verify(t, doc, lead1, lead2, c1, c2)

	res := f.search(t, f.at(baseLat+10*kmLat, baseLng, 50))
	if len(res.Items) != 3 {
		t.Fatalf("want the doctor three times, got %v", practiceIDs(res.Items))
	}
	seen := map[string]bool{}
	for _, it := range res.Items {
		if it.Doctor.ProfessionalRef != doc {
			t.Fatalf("someone else: %+v", it.Doctor)
		}
		seen[it.PracticeID] = true
	}
	if !seen[p1] || !seen[p2] || !seen[p3] {
		t.Fatalf("missing a practice: %v", seen)
	}
	// Each practice reads with its own clinic, branch and zone (booking depends on this).
	for pr, want := range map[string][3]string{p1: {c1, c1, "UTC"}, p2: {c1, b2, "Asia/Dubai"}, p3: {c2, c2, "UTC"}} {
		got, err := f.m.GetPractice(ctx, pr)
		if err != nil || got.ClinicRef != want[0] || got.BranchRef != want[1] || got.TimeZone != want[2] || got.ProfessionalRef != doc {
			t.Fatalf("practice %s: %+v %v", pr, got, err)
		}
	}
	// A second practice at the same branch is still refused; a different fee is per practice.
	_, err := f.m.CreatePractice(ctx, admin1, c1, directory.PracticeInput{DoctorRef: doc, BranchRef: b2, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "directory.practice_exists", 409)
	_, err = f.m.CreatePractice(ctx, admin1, c1, directory.PracticeInput{DoctorRef: doc, Modes: []string{"video"}, FeeAmount: 1})
	wantProblem(t, err, "directory.practice_exists", 409)
	if _, err := f.m.UpdatePractice(ctx, docActor(doc), p2, directory.PracticeChange{FeeAmount: ptr(int64(175))}); err != nil {
		t.Fatal(err)
	}
	g1, _ := f.m.GetProfile(ctx, p1)
	g2, _ := f.m.GetProfile(ctx, p2)
	if g1.FeeAmount != 100 || g2.FeeAmount != 175 {
		t.Fatalf("fees: %d %d", g1.FeeAmount, g2.FeeAmount)
	}
	// Suspending the doctor hides all three; suspending one clinic hides only its own.
	f.status(t, c2, "suspended", f.tick())
	if n := len(f.search(t, f.at(baseLat+10*kmLat, baseLng, 50)).Items); n != 2 {
		t.Fatalf("want 2 after clinic two is suspended, got %d", n)
	}
	f.status(t, doc, "suspended", f.tick())
	if n := len(f.search(t, f.at(baseLat+10*kmLat, baseLng, 50)).Items); n != 0 {
		t.Fatalf("want 0 after the doctor is suspended, got %d", n)
	}
}

func TestS130MainBranchExistsForEveryClinicWithTheClinicID(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lead := f.doctor(t, "Lead Doctor")
	clinic, admin := f.clinic(t, "Main Clinic", baseLat, baseLng, lead)
	list, err := f.m.ListBranches(ctx, admin, clinic)
	if err != nil || len(list) != 1 || list[0].ID != clinic || !list[0].Main || !list[0].Active || list[0].Lat != baseLat {
		t.Fatalf("main branch: %+v %v", list, err)
	}
	b, err := f.m.GetBranch(ctx, clinic)
	if err != nil || b.ClinicRef != clinic || !b.Active {
		t.Fatalf("GetBranch: %+v %v", b, err)
	}
	if _, err := f.m.GetBranch(ctx, ids.New()); !errors.Is(err, dp.ErrNotFound) {
		t.Fatalf("unknown branch: %v", err)
	}
}
