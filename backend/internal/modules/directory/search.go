package directory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// ---- Ranking v1 (S-162, CD-05, K-CS-08) ----

// Factor is one published ranking factor.
type Factor struct {
	Name   string
	Kind   string  // "score" adds to the ranking; "gate" decides who is shown at all
	Weight float64 // for score factors; the weights of the factors in use are normalised
	// Plain is the wording published on the search screen and in the Terms.
	Plain string
}

// RankingFactors returns the exact ranking rules. They must match the published list
// (CD-05, E-Commerce Rules). Payment, subscription tier, partner revenue, personal or
// health data, doctor reliability (Provider Agreement A11 not approved) and ratings
// (v1.1) are NOT inputs. Ties rotate daily by a hash of practice and date.
func RankingFactors() []Factor {
	return []Factor{
		{Name: "distance", Kind: "score", Weight: 0.45,
			Plain: "How close the clinic is to the place you searched: nearer ranks higher."},
		{Name: "availability", Kind: "score", Weight: 0.35,
			Plain: "How soon the doctor has an open appointment (within the next 7 days): sooner ranks higher. Used only once online booking data is available."},
		{Name: "verification", Kind: "gate", Weight: 0,
			Plain: "Only doctors and clinics whose registration we have verified are shown at all; it does not change the order."},
	}
}

func factorWeights(availabilityActive bool) (dist, avail float64) {
	for _, f := range RankingFactors() {
		switch f.Name {
		case "distance":
			dist = f.Weight
		case "availability":
			if availabilityActive {
				avail = f.Weight
			}
		}
	}
	return
}

const availabilityHorizonHours = 168

// score combines distance (1 - d/radius) and, when active, availability
// (1 - min(hours to earliest slot, 168)/168), normalised over the factors in use.
func score(dMetres, radiusMetres float64, hoursToSlot float64, hasSlot, availabilityActive bool) float64 {
	wd, wa := factorWeights(availabilityActive)
	sd := 1 - dMetres/radiusMetres
	if sd < 0 {
		sd = 0
	}
	sa := 0.0
	if availabilityActive && hasSlot {
		sa = 1 - minf(hoursToSlot, availabilityHorizonHours)/availabilityHorizonHours
	}
	return (wd*sd + wa*sa) / (wd + wa)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ---- availability hook ----

type availabilityHolder struct {
	mu  sync.RWMutex
	src dp.AvailabilitySource
}

// SetAvailability injects the booking module's availability source. Until it is set,
// the next-available-slot filter is refused and availability has no weight in ranking.
func (m *Module) SetAvailability(src dp.AvailabilitySource) {
	m.avail.mu.Lock()
	m.avail.src = src
	m.avail.mu.Unlock()
}

func (m *Module) availability() dp.AvailabilitySource {
	m.avail.mu.RLock()
	defer m.avail.mu.RUnlock()
	return m.avail.src
}

// ---- Search (S-119) ----

// SearchQuery is a radius search. The location is used for this query only: it is never
// stored or logged (RET-12).
type SearchQuery struct {
	Location            *Point
	PostalCode          string
	RadiusKm            float64 // 0 = default
	Specialty           string
	Mode                string
	MinFee, MaxFee      *int64
	AvailableWithinDays int
	Limit               int
	Cursor              string
}

// Card is the public view of a doctor.
type Card struct {
	ProfessionalRef string
	DisplayName     string
	Specialty       string
	Languages       []string
	PhotoRef        string
}

// ClinicCard is the public view of a clinic at one branch (business locations are public
// data). Address and position are the branch's, so a clinic with several branches shows
// each one as its own location (S-130, E-GEO-02).
type ClinicCard struct {
	ID         string
	Name       string
	BranchID   string
	BranchName string
	Address    string
	Lat, Lng   float64
}

// Item is one search result.
type Item struct {
	PracticeID   string
	Doctor       Card
	Clinic       ClinicCard
	Modes        []string
	FeeAmount    int64
	Currency     string
	FeeVersion   string
	FeeUpdatedAt time.Time
	DistanceKm   float64 // rounded to 0.1 km
}

// SearchResult is one page.
type SearchResult struct {
	Items      []Item
	NextCursor string
}

type cursor struct {
	Day  string  `json:"d"`
	Dist float64 `json:"m,omitempty"`
	Tie  string  `json:"t,omitempty"`
	ID   string  `json:"i,omitempty"`
	Off  int     `json:"o,omitempty"`
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || len(c.Day) != 10 || c.Off < 0 || (c.ID != "" && !reUUID.MatchString(c.ID)) {
		return cursor{}, errors.New("bad cursor")
	}
	if _, err := time.Parse("2006-01-02", c.Day); err != nil {
		return cursor{}, errors.New("bad cursor")
	}
	return c, nil
}

type row struct {
	item Item
	dist float64 // metres
	tie  string
}

const pointExpr = `ST_SetSRID(ST_MakePoint($1::float8, $2::float8), 4326)::public.geography`

// Search finds listed practices within the radius, nearest first. Only Verified doctors
// at Verified clinics can appear (K-CS-01). Nothing about the query is stored or logged.
func (m *Module) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	v := &validation{}
	// Location: exactly one of a point or a postal code.
	var pt Point
	switch {
	case q.Location != nil && q.PostalCode != "":
		v.add("location", codeFormat)
	case q.Location == nil && q.PostalCode == "":
		v.add("location", codeRequired)
	case q.Location != nil:
		if !validPoint(q.Location.Lat, q.Location.Lng) {
			v.add("location", codeRange)
		}
		pt = *q.Location
	}
	radius := q.RadiusKm
	if radius == 0 {
		radius = m.cfg.DefaultRadiusKm
	}
	if !finite(radius) || radius < 0.1 || radius > m.cfg.MaxRadiusKm {
		v.add("radiusKm", codeRange)
	}
	spec := strings.TrimSpace(q.Specialty)
	if spec != "" && !reSpecialty.MatchString(spec) {
		v.add("specialty", codeFormat)
	}
	if q.Mode != "" && !validModes[q.Mode] {
		v.add("mode", codeFormat)
	}
	if (q.MinFee != nil && *q.MinFee < 0) || (q.MaxFee != nil && *q.MaxFee < 0) || (q.MinFee != nil && q.MaxFee != nil && *q.MinFee > *q.MaxFee) {
		v.add("minFee", codeRange)
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		v.add("limit", codeRange)
	}
	if q.AvailableWithinDays < 0 || q.AvailableWithinDays > 60 {
		v.add("availableWithinDays", codeRange)
	}
	var cur cursor
	hasCur := q.Cursor != ""
	if hasCur {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			v.add("cursor", codeFormat)
		}
		cur = c
	}
	if err := v.err(); err != nil {
		return SearchResult{}, err
	}
	avail := m.availability()
	if q.AvailableWithinDays > 0 && avail == nil {
		return SearchResult{}, errNoAvailabilty // never faked: booking data does not exist yet
	}
	if q.PostalCode != "" {
		p, err := m.resolvePostal(ctx, q.PostalCode)
		if err != nil {
			return SearchResult{}, err
		}
		pt = p
	}
	day := m.now().UTC().Format("2006-01-02")
	if hasCur {
		day = cur.Day // ties stay in the same order for the whole paging session
	}
	radiusM := radius * 1000

	if avail != nil {
		return m.searchRanked(ctx, avail, q, spec, pt, radiusM, day, cur, hasCur, limit)
	}
	rows, err := m.queryRows(ctx, q, spec, pt, radiusM, day, hasCur, cur, limit+1)
	if err != nil {
		return SearchResult{}, err
	}
	res := SearchResult{Items: []Item{}}
	if len(rows) > limit {
		last := rows[limit-1]
		res.NextCursor = encodeCursor(cursor{Day: day, Dist: last.dist, Tie: last.tie, ID: last.item.PracticeID})
		rows = rows[:limit]
	}
	for _, r := range rows {
		res.Items = append(res.Items, r.item)
	}
	return res, nil
}

// queryRows runs the PostGIS query: ST_DWithin on the branch's geography column uses the
// GiST index; ordering is distance, then the daily tie hash, then id (keyset paging).
func (m *Module) queryRows(ctx context.Context, q SearchQuery, spec string, pt Point, radiusM float64, day string, hasCur bool, cur cursor, n int) ([]row, error) {
	var sb strings.Builder
	args := []any{pt.Lng, pt.Lat, radiusM, day}
	sb.WriteString(`SELECT pr.id, pr.professional_id, pf.display_name, COALESCE(pf.specialty,''), pf.languages, COALESCE(pf.photo_ref,''),
		c.id, c.name, b.id, b.name, b.address_text, ST_Y(b.geo::geometry), ST_X(b.geo::geometry),
		pr.modes, pr.fee_amount, pr.currency, pr.fee_version, pr.fee_updated_at,
		ST_Distance(b.geo, ` + pointExpr + `, false) AS dist, md5(pr.id::text || $4::text) AS tie`)
	sb.WriteString(listedFrom)
	sb.WriteString(` WHERE pr.active AND ST_DWithin(b.geo, ` + pointExpr + `, $3::float8, false)`)
	add := func(cond string, v any) {
		args = append(args, v)
		sb.WriteString(" AND " + strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if spec != "" {
		add("pf.specialty = ?", spec)
	}
	if q.Mode != "" {
		add("? = ANY(pr.modes)", q.Mode)
	}
	if q.MinFee != nil {
		add("pr.fee_amount >= ?", *q.MinFee)
	}
	if q.MaxFee != nil {
		add("pr.fee_amount <= ?", *q.MaxFee)
	}
	if hasCur && cur.ID != "" {
		args = append(args, cur.Dist, cur.Tie, cur.ID)
		k := len(args)
		fmt.Fprintf(&sb, ` AND (ST_Distance(b.geo, `+pointExpr+`, false), md5(pr.id::text || $4::text), pr.id::text) > ($%d::float8, $%d::text, $%d::text)`, k-2, k-1, k)
	}
	args = append(args, n)
	fmt.Fprintf(&sb, ` ORDER BY dist, tie, pr.id::text LIMIT $%d`, len(args))

	rs, err := m.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		var ver int
		it := &r.item
		if err := rs.Scan(&it.PracticeID, &it.Doctor.ProfessionalRef, &it.Doctor.DisplayName, &it.Doctor.Specialty, &it.Doctor.Languages, &it.Doctor.PhotoRef,
			&it.Clinic.ID, &it.Clinic.Name, &it.Clinic.BranchID, &it.Clinic.BranchName, &it.Clinic.Address, &it.Clinic.Lat, &it.Clinic.Lng,
			&it.Modes, &it.FeeAmount, &it.Currency, &ver, &it.FeeUpdatedAt, &r.dist, &r.tie); err != nil {
			return nil, err
		}
		it.FeeVersion = fmt.Sprint(ver)
		it.DistanceKm = roundKm(r.dist)
		out = append(out, r)
	}
	return out, rs.Err()
}

// searchRanked is used when the availability hook is present: the nearest candidates are
// scored in memory with both factors. Paging is by offset over that scored list (the
// availability data may change between pages); candidates beyond MaxRankCandidates are
// not reachable, which is why radius search should be narrow when booking is live.
func (m *Module) searchRanked(ctx context.Context, avail dp.AvailabilitySource, q SearchQuery, spec string, pt Point, radiusM float64, day string, cur cursor, hasCur bool, limit int) (SearchResult, error) {
	rows, err := m.queryRows(ctx, q, spec, pt, radiusM, day, false, cursor{}, m.cfg.MaxRankCandidates)
	if err != nil {
		return SearchResult{}, err
	}
	idsList := make([]string, len(rows))
	for i, r := range rows {
		idsList[i] = r.item.PracticeID
	}
	now := m.now().UTC()
	slots, err := avail.EarliestSlots(ctx, idsList, now)
	if err != nil {
		return SearchResult{}, problem.Unavailable // fail closed: never rank on guessed availability
	}
	type scored struct {
		r row
		s float64
	}
	var list []scored
	horizon := time.Duration(q.AvailableWithinDays) * 24 * time.Hour
	for _, r := range rows {
		at, ok := slots[r.item.PracticeID]
		if q.AvailableWithinDays > 0 && (!ok || at.Sub(now) > horizon) {
			continue
		}
		list = append(list, scored{r, score(r.dist, radiusM, at.Sub(now).Hours(), ok, true)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].s != list[j].s {
			return list[i].s > list[j].s
		}
		if list[i].r.tie != list[j].r.tie {
			return list[i].r.tie < list[j].r.tie
		}
		return list[i].r.item.PracticeID < list[j].r.item.PracticeID
	})
	off := 0
	if hasCur {
		off = cur.Off
	}
	res := SearchResult{Items: []Item{}}
	for i := off; i < len(list) && i < off+limit; i++ {
		res.Items = append(res.Items, list[i].r.item)
	}
	if off+limit < len(list) {
		res.NextCursor = encodeCursor(cursor{Day: day, Off: off + limit})
	}
	return res, nil
}

// ---- Locality table (PIN-code fallback) ----

// Locality is one postal code with its centroid.
type Locality struct {
	PostalCode string
	Lat, Lng   float64
}

func (m *Module) normalisePostal(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), ""))
}

// LoadLocalities loads or replaces localities in the module's own table. The dataset
// comes from an operator task; the repository holds synthetic data only. byRef is the
// operator or service doing the load (audited).
func (m *Module) LoadLocalities(ctx context.Context, byRef string, locs []Locality) error {
	v := &validation{}
	for _, l := range locs {
		if !m.postal.MatchString(m.normalisePostal(l.PostalCode)) || !validPoint(l.Lat, l.Lng) {
			v.add("locality", codeFormat)
			break
		}
	}
	if byRef == "" {
		v.add("loadedBy", codeRequired)
	}
	if err := v.err(); err != nil {
		return err
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		for _, l := range locs {
			if _, err := tx.Exec(ctx, `INSERT INTO locality (postal_code, centroid)
				VALUES ($1, ST_SetSRID(ST_MakePoint($2::float8, $3::float8), 4326)::public.geography)
				ON CONFLICT (postal_code) DO UPDATE SET centroid = EXCLUDED.centroid`,
				m.normalisePostal(l.PostalCode), l.Lng, l.Lat); err != nil {
				return err
			}
		}
		return m.audit(ctx, tx, Actor{UserID: byRef}, "directory.locality.load", "locality", "", "")
	})
}

func (m *Module) resolvePostal(ctx context.Context, code string) (Point, error) {
	code = m.normalisePostal(code)
	if !m.postal.MatchString(code) {
		return Point{}, errInvalid.WithFields(problem.FieldError{Field: "postalCode", Code: codeFormat})
	}
	var p Point
	err := m.pool.QueryRow(ctx, `SELECT ST_Y(centroid::geometry), ST_X(centroid::geometry) FROM locality WHERE postal_code = $1`, code).Scan(&p.Lat, &p.Lng)
	if errors.Is(err, pgx.ErrNoRows) {
		return Point{}, errPinUnknown
	}
	return p, err
}
