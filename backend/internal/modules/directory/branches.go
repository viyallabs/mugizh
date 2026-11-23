package directory

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Branches (S-130, E-GEO-02). A clinic is one legal entity and is verified once; it can have
// several branch locations, each with its own address, geolocation and time zone. A practice
// belongs to one branch. A branch is listed only while its clinic is Verified.
//
// [VALIDATE] with the lawyer: whether each branch needs its own establishment registration
// or verification. Until that is answered, a branch needs nothing beyond its clinic.

var (
	errBranchDup      = problem.New(http.StatusConflict, "directory.branch_exists", "This clinic already has a branch with that name")
	errMainBranch     = problem.New(http.StatusConflict, "directory.main_branch_required", "The main branch cannot be switched off")
	errTooManyBranch  = problem.New(http.StatusConflict, "directory.too_many_branches", "This clinic has reached the branch limit")
	maxBranchesPerOne = 50
)

// BranchInput is a new branch. Give an address to be located, or a manual pin, or both.
type BranchInput struct {
	Name     string
	Address  string
	Pin      *Point
	TimeZone string // IANA; empty = the clinic's zone
}

// BranchChange changes a branch; nil fields are left alone. A new address without a pin is
// located again; a pin alone moves the point and keeps the address text.
type BranchChange struct {
	Name     *string
	Address  *string
	Pin      *Point
	TimeZone *string
	Active   *bool
}

// Branch is a branch as its clinic admin sees it.
type Branch struct {
	ID             string
	ClinicRef      string
	Name           string
	Address        string
	Lat, Lng       float64
	LocationSource string
	TimeZone       string
	Main           bool
	Active         bool
	// Listed is true when the branch is active and its clinic (and lead doctor) are Verified.
	Listed bool
}

const branchCols = `b.id, b.clinic_id, b.name, b.address_text, ST_Y(b.geo::geometry), ST_X(b.geo::geometry), b.geo_source, b.time_zone, b.is_main, b.active,
 (b.active AND c.lead_accepted
  AND EXISTS (SELECT 1 FROM verification_status_view v WHERE v.professional_ref = c.id AND v.status = 'verified')
  AND EXISTS (SELECT 1 FROM verification_status_view v WHERE v.professional_ref = c.lead_professional_id AND v.status = 'verified'))`

func scanBranch(row pgx.Row) (Branch, error) {
	var b Branch
	err := row.Scan(&b.ID, &b.ClinicRef, &b.Name, &b.Address, &b.Lat, &b.Lng, &b.LocationSource, &b.TimeZone, &b.Main, &b.Active, &b.Listed)
	return b, err
}

// branchAdmin reports whether the actor administers exactly this clinic. Anyone else gets
// not found, the same as for an unknown clinic (K-SEC-04).
func branchAdmin(a Actor, clinicRef string) bool {
	return a.Role == ip.RoleClinicAdmin && a.UserID != "" && reUUID.MatchString(clinicRef) && a.ClinicRef == clinicRef
}

func (m *Module) zone(v *validation, tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		v.add("timeZone", codeFormat)
	}
	return tz
}

// CreateBranch adds a branch to the actor's own clinic. The location comes from the pin or
// from the geocoder at the configured minimum accuracy, as for a clinic.
func (m *Module) CreateBranch(ctx context.Context, a Actor, clinicRef string, in BranchInput) (Branch, error) {
	if !branchAdmin(a, clinicRef) {
		return Branch{}, errNotFound
	}
	v := &validation{}
	name := m.text(v, "name", in.Name, 2, 120, false)
	addr := m.text(v, "address", in.Address, 5, 300, false)
	tz := m.zone(v, in.TimeZone)
	if in.Pin != nil && !validPoint(in.Pin.Lat, in.Pin.Lng) {
		v.add("pin", codeRange)
	}
	if err := v.err(); err != nil {
		return Branch{}, err
	}
	pt, src, acc, err := m.locate(ctx, in.Pin, addr)
	if err != nil {
		return Branch{}, err
	}
	id := ids.New()
	var out Branch
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		var clinicTZ string
		if err := tx.QueryRow(ctx, `SELECT time_zone FROM clinic WHERE id = $1 FOR SHARE`, clinicRef).Scan(&clinicTZ); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM branch WHERE clinic_id = $1`, clinicRef).Scan(&n); err != nil {
			return err
		}
		if n >= maxBranchesPerOne {
			return errTooManyBranch
		}
		zone := tz
		if zone == "" {
			zone = clinicTZ
		}
		if _, err := tx.Exec(ctx, `INSERT INTO branch (id, clinic_id, name, address_text, geo, geo_source, geo_accuracy, time_zone, created_by)
			VALUES ($1,$2,$3,$4, ST_SetSRID(ST_MakePoint($5::float8,$6::float8),4326)::public.geography, $7,$8,$9,$10)`,
			id, clinicRef, name, addr, pt.Lng, pt.Lat, src, string(acc), zone, a.UserID); err != nil {
			if isUnique(err) {
				return errBranchDup
			}
			return err
		}
		var err error
		out, err = scanBranch(tx.QueryRow(ctx, `SELECT `+branchCols+` FROM branch b JOIN clinic c ON c.id = b.clinic_id WHERE b.id = $1`, id))
		if err != nil {
			return err
		}
		return m.audit(ctx, tx, a, "directory.branch.create", "branch", id, "")
	})
	return out, err
}

// ListBranches lists the branches of the actor's own clinic, main first.
func (m *Module) ListBranches(ctx context.Context, a Actor, clinicRef string) ([]Branch, error) {
	if !branchAdmin(a, clinicRef) {
		return nil, errNotFound
	}
	rs, err := m.pool.Query(ctx, `SELECT `+branchCols+` FROM branch b JOIN clinic c ON c.id = b.clinic_id
		WHERE b.clinic_id = $1 ORDER BY b.is_main DESC, b.created_at, b.id`, clinicRef)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []Branch{}
	for rs.Next() {
		b, err := scanBranch(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rs.Err()
}

// UpdateBranch changes a branch of the actor's own clinic. A change of position, zone or
// active flag emits practice.updated for every practice of the branch, so booking refreshes
// its copy of the zone (K-ARC-04) and search reflects the change at once. The main branch
// cannot be switched off.
func (m *Module) UpdateBranch(ctx context.Context, a Actor, clinicRef, branchRef string, ch BranchChange) (Branch, error) {
	if !branchAdmin(a, clinicRef) || !reUUID.MatchString(branchRef) {
		return Branch{}, errNotFound
	}
	v := &validation{}
	if ch.Name == nil && ch.Address == nil && ch.Pin == nil && ch.TimeZone == nil && ch.Active == nil {
		v.add("body", codeRequired)
	}
	var name, addr, tz string
	if ch.Name != nil {
		name = m.text(v, "name", *ch.Name, 2, 120, false)
	}
	if ch.Address != nil {
		addr = m.text(v, "address", *ch.Address, 5, 300, false)
	}
	if ch.TimeZone != nil {
		tz = m.zone(v, *ch.TimeZone)
		if tz == "" {
			v.add("timeZone", codeRequired)
		}
	}
	if ch.Pin != nil && !validPoint(ch.Pin.Lat, ch.Pin.Lng) {
		v.add("pin", codeRange)
	}
	if err := v.err(); err != nil {
		return Branch{}, err
	}
	// Locate outside the transaction (a slow geocoder must not hold a lock).
	var pt Point
	var src, acc string
	relocate := ch.Pin != nil || ch.Address != nil
	if relocate {
		cur, err := m.GetBranch(ctx, branchRef)
		if err != nil || cur.ClinicRef != clinicRef {
			return Branch{}, errNotFound
		}
		p, s, ac, err := m.locate(ctx, ch.Pin, addr) // a pin wins over the address
		if err != nil {
			return Branch{}, err
		}
		pt, src, acc = p, s, string(ac)
	}
	var out Branch
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		var isMain, active bool
		if err := tx.QueryRow(ctx, `SELECT is_main, active FROM branch WHERE id = $1 AND clinic_id = $2 FOR UPDATE`, branchRef, clinicRef).Scan(&isMain, &active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		if ch.Active != nil && !*ch.Active && isMain {
			return errMainBranch
		}
		if _, err := tx.Exec(ctx, `UPDATE branch SET
			name = CASE WHEN $3 THEN $4 ELSE name END,
			address_text = CASE WHEN $5 THEN $6 ELSE address_text END,
			geo = CASE WHEN $7 THEN ST_SetSRID(ST_MakePoint($8::float8,$9::float8),4326)::public.geography ELSE geo END,
			geo_source = CASE WHEN $7 THEN $10 ELSE geo_source END,
			geo_accuracy = CASE WHEN $7 THEN $11 ELSE geo_accuracy END,
			time_zone = CASE WHEN $12 THEN $13 ELSE time_zone END,
			active = COALESCE($14, active),
			updated_at = now() WHERE id = $1 AND clinic_id = $2`,
			branchRef, clinicRef, ch.Name != nil, name, ch.Address != nil, addr, relocate, pt.Lng, pt.Lat, src, acc, ch.TimeZone != nil, tz, ch.Active); err != nil {
			if isUnique(err) {
				return errBranchDup
			}
			return err
		}
		if isMain && (relocate || ch.TimeZone != nil) {
			// The clinic row keeps the main branch's location for older readers.
			if _, err := tx.Exec(ctx, `UPDATE clinic c SET address_text = b.address_text, geo = b.geo, geo_source = b.geo_source, geo_accuracy = b.geo_accuracy,
				time_zone = b.time_zone, updated_at = now() FROM branch b WHERE b.id = $1 AND c.id = b.clinic_id`, branchRef); err != nil {
				return err
			}
		}
		var err error
		out, err = scanBranch(tx.QueryRow(ctx, `SELECT `+branchCols+` FROM branch b JOIN clinic c ON c.id = b.clinic_id WHERE b.id = $1`, branchRef))
		if err != nil {
			return err
		}
		if err := m.audit(ctx, tx, a, "directory.branch.update", "branch", branchRef, ""); err != nil {
			return err
		}
		if relocate || ch.TimeZone != nil || ch.Active != nil {
			return m.emitBranchPractices(ctx, tx, branchRef)
		}
		return nil
	})
	return out, err
}

func (m *Module) emitBranchPractices(ctx context.Context, tx pgx.Tx, branchRef string) error {
	rs, err := tx.Query(ctx, `SELECT id FROM practice WHERE branch_id = $1`, branchRef)
	if err != nil {
		return err
	}
	var all []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			rs.Close()
			return err
		}
		all = append(all, id)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	for _, id := range all {
		if err := m.emitPracticeUpdated(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

var _ dp.BranchReader = (*Module)(nil)

// GetBranch implements public.BranchReader (booking uses it before a branch closure). It
// answers for inactive branches too; the caller checks the clinic.
func (m *Module) GetBranch(ctx context.Context, branchRef string) (dp.Branch, error) {
	if !reUUID.MatchString(branchRef) {
		return dp.Branch{}, dp.ErrNotFound
	}
	var b dp.Branch
	err := m.pool.QueryRow(ctx, `SELECT id, clinic_id, time_zone, active FROM branch WHERE id = $1`, branchRef).Scan(&b.ID, &b.ClinicRef, &b.TimeZone, &b.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return dp.Branch{}, dp.ErrNotFound
	}
	return b, err
}
