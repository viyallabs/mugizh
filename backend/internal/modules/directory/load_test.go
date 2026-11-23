package directory_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
)

// S-067 prototype: radius search at 50,000 clinics and practices on real PostgreSQL +
// PostGIS (target p95 < 300 ms, performance-targets.md). The always-on version uses 5,000;
// set DIRECTORY_LOAD_50K=1 for the full run:
//
//	tools/dev.sh backend env DIRECTORY_LOAD_50K=1 go test -run TestS067 -v ./internal/modules/directory/
//
// All data is synthetic: 30 invented city centres in a plain lat/lng box.
func TestS067RadiusSearchLoad(t *testing.T) {
	n, queries := 5000, 300
	if os.Getenv("DIRECTORY_LOAD_50K") != "" {
		n, queries = 50000, 3000
	}
	f := setup(t) // skips under -short
	ctx := context.Background()

	tx, err := f.env.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	stmts := []string{
		`CREATE TEMP TABLE gen ON COMMIT DROP AS
		   SELECT g, gen_random_uuid() AS pid, gen_random_uuid() AS cid, (g % 30) AS city,
		          8 + ((g % 30) * 0.65) + (random() - 0.5) * 0.35 AS lat,
		          70 + ((g % 30) * 0.55) + (random() - 0.5) * 0.35 AS lng
		   FROM generate_series(1, ` + fmt.Sprint(n) + `) g`,
		`INSERT INTO directory.professional (id, display_name, specialty, languages)
		   SELECT pid, 'Synthetic Doctor ' || g, (ARRAY['cardiology','dermatology','paediatrics','orthopaedics','ent','neurology','gynaecology','psychiatry'])[1 + g % 8], ARRAY['English'] FROM gen`,
		`INSERT INTO directory.clinic (id, name, address_text, geo, geo_source, time_zone, lead_professional_id, created_by)
		   SELECT cid, 'Synthetic Clinic ' || g, 'Synthetic address ' || g, ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography, 'manual_pin', 'UTC', pid, pid FROM gen`,
		`INSERT INTO directory.branch (id, clinic_id, name, address_text, geo, geo_source, time_zone, is_main, created_by)
		   SELECT cid, cid, 'Synthetic Branch ' || g, 'Synthetic address ' || g, ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography, 'manual_pin', 'UTC', true, pid FROM gen`,
		`INSERT INTO directory.practice (id, professional_id, clinic_id, branch_id, modes, fee_amount, currency)
		   SELECT gen_random_uuid(), pid, cid, cid, CASE WHEN g % 3 = 0 THEN ARRAY['in_person','video'] ELSE ARRAY['in_person'] END, 20000 + (g % 50) * 1000, 'TST' FROM gen`,
		// 90% verified, the rest in other states, as in production.
		`INSERT INTO directory.verification_status_view (professional_ref, status, updated_at)
		   SELECT pid, CASE WHEN g % 10 = 0 THEN 'suspended' ELSE 'verified' END, now() FROM gen`,
		`INSERT INTO directory.verification_status_view (professional_ref, status, updated_at)
		   SELECT cid, 'verified', now() FROM gen`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Query centres: the city centres, so densities are realistic (about n/30 per city).
	rows, err := tx.Query(ctx, `SELECT 8 + c * 0.65, 70 + c * 0.55 FROM generate_series(0, 29) c`)
	if err != nil {
		t.Fatal(err)
	}
	var centres []directory.Point
	for rows.Next() {
		var p directory.Point
		if err := rows.Scan(&p.Lat, &p.Lng); err != nil {
			t.Fatal(err)
		}
		centres = append(centres, p)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Owner.Exec(ctx, `ANALYZE directory.clinic; ANALYZE directory.branch; ANALYZE directory.practice; ANALYZE directory.professional; ANALYZE directory.verification_status_view`); err != nil {
		t.Fatal(err)
	}

	// The GiST index must be the access path for the distance filter.
	var plan strings.Builder
	prs, err := f.env.Owner.Query(ctx, `EXPLAIN SELECT id FROM directory.branch WHERE ST_DWithin(geo, ST_SetSRID(ST_MakePoint(70.2, 8.2), 4326)::geography, 5000, false)`)
	if err != nil {
		t.Fatal(err)
	}
	for prs.Next() {
		var l string
		_ = prs.Scan(&l)
		plan.WriteString(l + "\n")
	}
	prs.Close()
	if n >= 5000 && !strings.Contains(plan.String(), "branch_geo_idx") {
		t.Fatalf("distance filter does not use the GiST index:\n%s", plan.String())
	}

	radii := []float64{2, 5, 10, 25}
	mk := func(i int) directory.SearchQuery {
		c := centres[i%len(centres)]
		q := directory.SearchQuery{Location: &directory.Point{Lat: c.Lat + float64(i%7-3)*0.01, Lng: c.Lng + float64(i%5-2)*0.01}, RadiusKm: radii[i%len(radii)], Limit: 20}
		switch i % 4 {
		case 1:
			q.Mode = directory.ModeVideo
		case 2:
			q.Specialty = "cardiology"
		case 3:
			q.MinFee, q.MaxFee = ptr(int64(25000)), ptr(int64(60000))
		}
		return q
	}
	for i := 0; i < 20; i++ { // warm-up
		if _, err := f.m.Search(ctx, mk(i)); err != nil {
			t.Fatal(err)
		}
	}
	measure := func(workers int) []time.Duration {
		lat := make([]time.Duration, queries)
		var wg sync.WaitGroup
		next := make(chan int)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					s := time.Now()
					if _, err := f.m.Search(ctx, mk(i)); err != nil {
						t.Error(err)
					}
					lat[i] = time.Since(s)
				}
			}()
		}
		for i := 0; i < queries; i++ {
			next <- i
		}
		close(next)
		wg.Wait()
		sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
		return lat
	}
	pct := func(l []time.Duration, p float64) time.Duration { return l[int(float64(len(l)-1)*p)] }
	var p95 time.Duration
	for _, workers := range []int{1, 4} {
		l := measure(workers)
		t.Logf("S-067 %d practices, %d queries, %d worker(s): p50=%v p95=%v p99=%v max=%v (NumCPU=%d, containerised PostgreSQL+PostGIS on a developer machine)",
			n, queries, workers, pct(l, .50), pct(l, .95), pct(l, .99), l[len(l)-1], runtime.NumCPU())
		if workers == 1 {
			p95 = pct(l, .95)
		}
	}
	if p95 > 300*time.Millisecond {
		t.Fatalf("p95 %v exceeds the 300 ms target", p95)
	}
}
