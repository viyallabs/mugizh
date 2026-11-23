package directory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

var _ dp.Reader = (*Module)(nil)

// GetPractice implements public.Reader for booking: a listed practice, or ErrNotFound.
// Unknown, inactive, unverified and suspended are the same answer.
func (m *Module) GetPractice(ctx context.Context, id string) (dp.Practice, error) {
	if !reUUID.MatchString(id) {
		return dp.Practice{}, dp.ErrNotFound
	}
	var p dp.Practice
	var ver int
	err := m.pool.QueryRow(ctx, `SELECT pr.id, pr.professional_id, pr.clinic_id, pr.modes, pr.fee_amount, pr.currency, pr.fee_version, b.time_zone, pr.branch_id`+
		listedFrom+` WHERE pr.id = $1 AND pr.active`, id).
		Scan(&p.ID, &p.ProfessionalRef, &p.ClinicRef, &p.Modes, &p.FeeAmount, &p.Currency, &ver, &p.TimeZone, &p.BranchRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return dp.Practice{}, dp.ErrNotFound
	}
	if err != nil {
		return dp.Practice{}, err
	}
	p.FeeVersion = fmt.Sprint(ver)
	p.Listed = true
	return p, nil
}

// Profile is the public view of a listed practice (S-120).
type Profile struct {
	PracticeID     string
	Doctor         Card
	Clinic         ClinicCard
	Modes          []string
	FeeAmount      int64
	Currency       string
	FeeVersion     string
	FeeUpdatedAt   time.Time
	Timings        string
	TimeZone       string
	Qualifications []string
	VerifiedAt     time.Time
	Checks         []string
}

// GetProfile returns the public profile of a listed practice. Unverified, suspended,
// inactive and unknown all answer not found (K-CS-01, S-120).
func (m *Module) GetProfile(ctx context.Context, id string) (Profile, error) {
	if !reUUID.MatchString(id) {
		return Profile{}, errNotFound
	}
	var p Profile
	var ver int
	err := m.pool.QueryRow(ctx, `SELECT pr.id, pr.professional_id, pf.display_name, COALESCE(pf.specialty,''), pf.languages, COALESCE(pf.photo_ref,''),
		c.id, c.name, b.id, b.name, b.address_text, ST_Y(b.geo::geometry), ST_X(b.geo::geometry),
		pr.modes, pr.fee_amount, pr.currency, pr.fee_version, pr.fee_updated_at, pr.timings, b.time_zone, vp.updated_at`+
		listedFrom+` WHERE pr.id = $1 AND pr.active`, id).
		Scan(&p.PracticeID, &p.Doctor.ProfessionalRef, &p.Doctor.DisplayName, &p.Doctor.Specialty, &p.Doctor.Languages, &p.Doctor.PhotoRef,
			&p.Clinic.ID, &p.Clinic.Name, &p.Clinic.BranchID, &p.Clinic.BranchName, &p.Clinic.Address, &p.Clinic.Lat, &p.Clinic.Lng,
			&p.Modes, &p.FeeAmount, &p.Currency, &ver, &p.FeeUpdatedAt, &p.Timings, &p.TimeZone, &p.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, errNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	p.FeeVersion = fmt.Sprint(ver)
	rs, err := m.pool.Query(ctx, `SELECT degree FROM qualification WHERE professional_id = $1 ORDER BY recorded_at, id`, p.Doctor.ProfessionalRef)
	if err != nil {
		return Profile{}, err
	}
	defer rs.Close()
	p.Qualifications = []string{}
	for rs.Next() {
		var d string
		if err := rs.Scan(&d); err != nil {
			return Profile{}, err
		}
		p.Qualifications = append(p.Qualifications, d)
	}
	p.Checks = append([]string{}, m.cfg.VerificationChecks...)
	return p, rs.Err()
}

// OnVerificationStatusChanged keeps the verification read model current from
// verification.status_changed (B5). Payload: professionalRef (a doctor's or a clinic's
// ID), status and subjectKind (doctor or clinic). It is idempotent (outbox.Once) and ignores an older event than the one
// already applied. A status other than "verified" hides the doctor or clinic at once,
// and practice.updated is emitted for every practice affected so booking can react.
func (m *Module) OnVerificationStatusChanged(ctx context.Context, e outbox.Event) error {
	ref, status := e.Payload["professionalRef"], e.Payload["status"]
	if !reUUID.MatchString(ref) || status == "" {
		return fmt.Errorf("directory: malformed verification.status_changed event %s", e.ID)
	}
	// subjectKind says whether the ref is a doctor's or a clinic's (D-38). Both are rows of
	// the same read model, keyed by ID. An absent kind is an older doctor event; any other
	// value is not understood and fails closed (the event is retried, then dead-lettered).
	switch e.Payload["subjectKind"] {
	case "", "doctor", "clinic":
	default:
		return fmt.Errorf("directory: unknown subject kind in verification.status_changed event %s", e.ID)
	}
	at := e.OccurredAt
	if at.IsZero() {
		at = m.now()
	}
	return outbox.Once(ctx, m.pool, m.handler, e, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO verification_status_view (professional_ref, status, updated_at) VALUES ($1,$2,$3)
			ON CONFLICT (professional_ref) DO UPDATE SET status = EXCLUDED.status, updated_at = EXCLUDED.updated_at
			WHERE verification_status_view.updated_at <= EXCLUDED.updated_at`, ref, status, at.UTC())
		if err != nil {
			return err // a bad status value fails the CHECK and retries to the dead letter
		}
		if tag.RowsAffected() == 0 {
			return nil // stale event: a newer status is already stored
		}
		rs, err := tx.Query(ctx, `SELECT pr.id FROM practice pr JOIN clinic c ON c.id = pr.clinic_id
			WHERE pr.professional_id = $1 OR pr.clinic_id = $1 OR c.lead_professional_id = $1`, ref)
		if err != nil {
			return err
		}
		var affected []string
		for rs.Next() {
			var id string
			if err := rs.Scan(&id); err != nil {
				rs.Close()
				return err
			}
			affected = append(affected, id)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return err
		}
		for _, id := range affected {
			if err := m.emitPracticeUpdated(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DisplayName implements public.NameReader. Unknown professionals are ErrNotFound.
func (m *Module) DisplayName(ctx context.Context, professionalRef string) (string, error) {
	if !ids.Valid(professionalRef) {
		return "", dp.ErrNotFound
	}
	var name string
	err := m.pool.QueryRow(ctx, `SELECT display_name FROM professional WHERE id=$1`, professionalRef).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", dp.ErrNotFound
	}
	return name, err
}

var _ dp.NameReader = (*Module)(nil)
