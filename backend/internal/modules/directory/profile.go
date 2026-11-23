package directory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SpecialtyLabelKey is the translation key shown for a doctor (K-GLB-02). Without a
// recognised postgraduate qualification the profile says "General practice (MBBS)" (RG-04).
func SpecialtyLabelKey(specialty string) string {
	if specialty == "" {
		return "directory.general_practice_mbbs"
	}
	return "directory.specialty." + specialty
}

// ---- Professional (S-112) ----

// ProfessionalInput is what a doctor may set on their own profile.
type ProfessionalInput struct {
	DisplayName string
	Specialty   string // empty = none; needs a recognised PG qualification (RG-04)
	Languages   []string
	PhotoRef    string // optional plain photo reference
}

// Professional is a doctor's profile as the doctor sees it.
type Professional struct {
	ID          string
	DisplayName string
	Specialty   string
	Languages   []string
	PhotoRef    string
	Listed      bool
}

// Qualification is a recognised qualification (recorded by verification or admin).
type Qualification struct {
	Degree    string
	Level     string // ug, pg, other
	Specialty string // only for level pg
}

// UpsertProfessional creates or changes the acting doctor's own profile. The profile id is
// the doctor's user ID; nobody else's profile can be reached (S-112).
func (m *Module) UpsertProfessional(ctx context.Context, a Actor, in ProfessionalInput) (Professional, error) {
	if a.Role != ip.RoleDoctor || a.UserID == "" {
		return Professional{}, problem.Forbidden
	}
	v := &validation{}
	name := m.text(v, "displayName", in.DisplayName, 2, 80, true)
	spec := strings.TrimSpace(in.Specialty)
	if spec != "" && !reSpecialty.MatchString(spec) {
		v.add("specialty", codeFormat)
	}
	langs := m.languages(v, in.Languages)
	photo := strings.TrimSpace(in.PhotoRef)
	if photo != "" && !reRef.MatchString(photo) {
		v.add("photoRef", codeFormat)
	}
	if err := v.err(); err != nil {
		return Professional{}, err
	}
	out := Professional{ID: a.UserID}
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		if spec != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM qualification WHERE professional_id = $1 AND level = 'pg' AND specialty = $2)`,
				a.UserID, spec).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid.WithFields(problem.FieldError{Field: "specialty", Code: codeSpecialty})
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO professional (id, display_name, specialty, languages, photo_ref)
			VALUES ($1, $2, NULLIF($3,''), $4, NULLIF($5,''))
			ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name, specialty = EXCLUDED.specialty,
			  languages = EXCLUDED.languages, photo_ref = EXCLUDED.photo_ref, updated_at = now()`,
			a.UserID, name, spec, langs, photo); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM verification_status_view WHERE professional_ref = $1 AND status = 'verified')`,
			a.UserID).Scan(&out.Listed); err != nil {
			return err
		}
		return m.audit(ctx, tx, a, "directory.professional.upsert", "professional", a.UserID, a.UserID)
	})
	if err != nil {
		return Professional{}, err
	}
	out.DisplayName, out.Specialty, out.Languages, out.PhotoRef = name, spec, langs, photo
	return out, nil
}

func (m *Module) languages(v *validation, in []string) []string {
	if len(in) > 10 {
		v.add("languages", codeRange)
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(l)
		k := strings.ToLower(l)
		if !reLanguage.MatchString(l) {
			v.add("languages", codeFormat)
			return nil
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, l)
		}
	}
	return out
}

// RecordQualification records a recognised qualification for a doctor who has a profile.
// It is called by the verification wiring or the admin console, never by the doctor, so a
// specialty cannot be self-declared (RG-04). byRef is the acting staff member or service.
func (m *Module) RecordQualification(ctx context.Context, professionalRef string, q Qualification, byRef string) error {
	v := &validation{}
	if !reUUID.MatchString(professionalRef) {
		return errNotFound
	}
	q.Degree = m.text(v, "degree", q.Degree, 2, 80, false)
	switch q.Level {
	case "ug", "other":
		if q.Specialty != "" {
			v.add("specialty", codeFormat)
		}
	case "pg":
		if q.Specialty != "" && !reSpecialty.MatchString(q.Specialty) {
			v.add("specialty", codeFormat)
		}
	default:
		v.add("level", codeFormat)
	}
	if byRef == "" {
		v.add("recordedBy", codeRequired)
	}
	if err := v.err(); err != nil {
		return err
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO qualification (id, professional_id, degree, level, specialty)
			SELECT $1, id, $3, $4, NULLIF($5,'') FROM professional WHERE id = $2`,
			ids.New(), professionalRef, q.Degree, q.Level, q.Specialty)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return m.audit(ctx, tx, Actor{UserID: byRef}, "directory.qualification.record", "professional", professionalRef, professionalRef)
	})
}

// ---- Clinic (S-117) ----

// ClinicInput is a clinic submission.
type ClinicInput struct {
	Name               string
	Address            string
	Pin                *Point // manual pin; wins over geocoding
	TimeZone           string // IANA; empty = the pack default
	LeadDoctorRef      string
	RegistrationDocRef string
	HFRID              string
}

// Point is a latitude and longitude in degrees.
type Point struct{ Lat, Lng float64 }

// Clinic is a clinic as its admin sees it.
type Clinic struct {
	ID             string
	Name           string
	Address        string
	Lat, Lng       float64
	LocationSource string // geocoded or manual_pin
	LeadDoctorRef  string
	Listed         bool
}

// SubmitClinic creates a clinic for the signed-in user, who becomes its clinic admin.
// The clinic is not listed until the clinic and its lead doctor are Verified. The caller
// (HTTP layer) has already required a professional-app session.
func (m *Module) SubmitClinic(ctx context.Context, a Actor, in ClinicInput) (Clinic, error) {
	if a.UserID == "" {
		return Clinic{}, problem.Unauthenticated
	}
	v := &validation{}
	name := m.text(v, "name", in.Name, 2, 120, true)
	addr := m.text(v, "address", in.Address, 5, 300, false)
	if !reUUID.MatchString(in.LeadDoctorRef) {
		v.add("leadDoctorId", codeFormat)
	}
	tz := strings.TrimSpace(in.TimeZone)
	if tz == "" {
		tz = m.cfg.DefaultTimeZone
	} else if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		v.add("timeZone", codeFormat)
	}
	doc := strings.TrimSpace(in.RegistrationDocRef)
	if doc == "" && m.cfg.RequireRegistrationDoc {
		v.add("registrationDocRef", codeRequired)
	} else if doc != "" && !reRef.MatchString(doc) {
		v.add("registrationDocRef", codeFormat)
	}
	hfr := strings.TrimSpace(in.HFRID)
	if hfr != "" && !reHFR.MatchString(hfr) {
		v.add("hfrId", codeFormat)
	}
	if in.Pin != nil && !validPoint(in.Pin.Lat, in.Pin.Lng) {
		v.add("pin", codeRange)
	}
	if err := v.err(); err != nil {
		return Clinic{}, err
	}
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM professional WHERE id = $1)`, in.LeadDoctorRef).Scan(&exists); err != nil {
		return Clinic{}, err
	}
	if !exists {
		return Clinic{}, errInvalid.WithFields(problem.FieldError{Field: "leadDoctorId", Code: codeNotFound})
	}

	pt, src, acc, err := m.locate(ctx, in.Pin, addr)
	if err != nil {
		return Clinic{}, err
	}

	clinicID := ids.New()
	selfLead := in.LeadDoctorRef == a.UserID
	// The creator becomes the clinic admin through identity (B6: no shared transaction).
	// The role is granted first: a role for a clinic that never got created is harmless,
	// whereas a clinic nobody administers would not be.
	if err := m.roles.AssignRole(ctx, a.UserID, ip.RoleClinicAdmin, clinicID, a.UserID); err != nil {
		m.log.Warn("directory.role_assign_failed", logging.Err(err))
		return Clinic{}, errRoleAssign
	}
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO clinic (id, name, address_text, geo, geo_source, geo_accuracy, time_zone, lead_professional_id,
			registration_doc_ref, hfr_id, created_by, lead_accepted)
			VALUES ($1,$2,$3, ST_SetSRID(ST_MakePoint($4::float8,$5::float8),4326)::public.geography, $6,$7,$8,$9, NULLIF($10,''), NULLIF($11,''), $12, $13)`,
			clinicID, name, addr, pt.Lng, pt.Lat, src, string(acc), tz, in.LeadDoctorRef, doc, hfr, a.UserID, selfLead); err != nil {
			return err
		}
		// S-130: every clinic starts with a main branch (same ID as the clinic) at the submitted address.
		if _, err := tx.Exec(ctx, `INSERT INTO branch (id, clinic_id, name, address_text, geo, geo_source, geo_accuracy, time_zone, is_main, created_by)
			SELECT id, id, left(name, 100) || ' (main)', address_text, geo, geo_source, geo_accuracy, time_zone, true, created_by FROM clinic WHERE id = $1`, clinicID); err != nil {
			return err
		}
		if !selfLead {
			// Review M5: naming another doctor as lead needs that doctor's acceptance.
			if err := m.invite(ctx, tx, a, "clinic_lead", in.LeadDoctorRef, clinicID, ""); err != nil {
				return err
			}
		}
		return m.audit(ctx, tx, a, "directory.clinic.create", "clinic", clinicID, in.LeadDoctorRef)
	})
	if err != nil {
		if rerr := m.roles.RevokeRole(context.WithoutCancel(ctx), a.UserID, ip.RoleClinicAdmin, clinicID, a.UserID); rerr != nil {
			m.log.Error("directory.role_revoke_failed", logging.Err(rerr))
		}
		return Clinic{}, err
	}
	return Clinic{ID: clinicID, Name: name, Address: addr, Lat: pt.Lat, Lng: pt.Lng, LocationSource: src,
		LeadDoctorRef: in.LeadDoctorRef, Listed: false}, nil
}

// locate returns the clinic's point: the manual pin if given, otherwise the geocoder's
// answer if precise enough. Anything else asks for a manual pin (adapter-interfaces §10).
func (m *Module) locate(ctx context.Context, pin *Point, addr string) (Point, string, geocoding.Accuracy, error) {
	if pin != nil {
		return *pin, "manual_pin", "", nil
	}
	gctx, cancel := context.WithTimeout(ctx, m.cfg.GeocodeTimeout)
	defer cancel()
	res, err := m.geo.Geocode(gctx, addr, m.cfg.CountryCode)
	if err != nil {
		switch adapters.KindOf(err) {
		case adapters.NotFound:
			return Point{}, "", "", errAddressVague
		default:
			m.log.Warn("directory.geocode_failed", "kind", string(adapters.KindOf(err)))
			return Point{}, "", "", errGeocodeDown
		}
	}
	if !validPoint(res.Lat, res.Lng) || !res.Accuracy.AtLeast(m.cfg.MinGeocodeAccuracy) {
		return Point{}, "", "", errAddressVague
	}
	return Point{Lat: res.Lat, Lng: res.Lng}, "geocoded", res.Accuracy, nil
}

// ---- Practice (S-112) ----

// PracticeInput creates a practice at a clinic.
type PracticeInput struct {
	DoctorRef string
	BranchRef string // empty = the clinic's main branch
	Modes     []string
	FeeAmount int64
	Timings   string
}

// PracticeChange changes a practice; nil fields are left alone.
type PracticeChange struct {
	Modes     *[]string
	FeeAmount *int64
	Timings   *string
	Active    *bool
}

// ManagedPractice is a practice as its owners see it, listed or not.
type ManagedPractice struct {
	ID           string
	DoctorRef    string
	ClinicRef    string
	BranchRef    string
	Modes        []string
	FeeAmount    int64
	Currency     string
	FeeVersion   string
	FeeUpdatedAt time.Time
	Timings      string
	Active       bool
	Listed       bool
}

const practiceCols = `pr.id, pr.professional_id, pr.clinic_id, pr.branch_id, pr.modes, pr.fee_amount, pr.currency, pr.fee_version, pr.fee_updated_at, pr.timings, pr.active, ` + listedExpr

func scanManaged(row pgx.Row) (ManagedPractice, error) {
	var p ManagedPractice
	var ver int
	err := row.Scan(&p.ID, &p.DoctorRef, &p.ClinicRef, &p.BranchRef, &p.Modes, &p.FeeAmount, &p.Currency, &ver, &p.FeeUpdatedAt, &p.Timings, &p.Active, &p.Listed)
	p.FeeVersion = fmt.Sprint(ver)
	return p, err
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreatePractice adds a doctor's practice at a clinic. Only that clinic's admin may.
func (m *Module) CreatePractice(ctx context.Context, a Actor, clinicRef string, in PracticeInput) (ManagedPractice, error) {
	if a.Role != ip.RoleClinicAdmin || a.UserID == "" || !reUUID.MatchString(clinicRef) || a.ClinicRef != clinicRef {
		return ManagedPractice{}, errNotFound // others' clinics are 404 (K-SEC-04)
	}
	v := &validation{}
	if !reUUID.MatchString(in.DoctorRef) {
		v.add("doctorId", codeFormat)
	}
	branchRef := in.BranchRef
	if branchRef == "" {
		branchRef = clinicRef // the main branch has the clinic's ID
	} else if !reUUID.MatchString(branchRef) {
		return ManagedPractice{}, errNotFound
	}
	md := modes(v, "modes", in.Modes)
	m.fee(v, "feeAmount", in.FeeAmount)
	timings := m.text(v, "timings", in.Timings, 0, 200, true)
	if err := v.err(); err != nil {
		return ManagedPractice{}, err
	}
	var out ManagedPractice
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		var clinicOK, docOK bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM clinic WHERE id = $1), EXISTS (SELECT 1 FROM professional WHERE id = $2)`,
			clinicRef, in.DoctorRef).Scan(&clinicOK, &docOK); err != nil {
			return err
		}
		if !clinicOK {
			return errNotFound
		}
		if !docOK {
			return errInvalid.WithFields(problem.FieldError{Field: "doctorId", Code: codeNotFound})
		}
		var branchOK bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM branch WHERE id = $1 AND clinic_id = $2 AND active)`, branchRef, clinicRef).Scan(&branchOK); err != nil {
			return err
		}
		if !branchOK {
			return errNotFound // another clinic's branch looks like an unknown one (K-SEC-04)
		}
		// Review M5: a practice for another doctor needs that doctor's acceptance; one the doctor
		// creates for themself is accepted at once.
		self := in.DoctorRef == a.UserID
		id := ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO practice (id, professional_id, clinic_id, branch_id, modes, fee_amount, currency, timings, doctor_accepted)
			VALUES ($1,$2,$3,$9,$4,$5,$6,$7,$8)`, id, in.DoctorRef, clinicRef, md, in.FeeAmount, m.cfg.Currency, timings, self, branchRef); err != nil {
			if isUnique(err) {
				return errPracticeDup
			}
			return err
		}
		if !self {
			if err := m.invite(ctx, tx, a, "practice", in.DoctorRef, clinicRef, id); err != nil {
				return err
			}
		}
		var err error
		out, err = scanManaged(tx.QueryRow(ctx, `SELECT `+practiceCols+` FROM practice pr JOIN clinic c ON c.id = pr.clinic_id WHERE pr.id = $1`, id))
		if err != nil {
			return err
		}
		if err := m.audit(ctx, tx, a, "directory.practice.create", "practice", id, in.DoctorRef); err != nil {
			return err
		}
		return m.emitPracticeUpdated(ctx, tx, id)
	})
	return out, err
}

// UpdatePractice changes a practice. The doctor of the practice, or the admin of its
// clinic, may; anyone else gets not found. A fee change bumps the fee version and never
// touches existing bookings, which copied the fee when they were held.
func (m *Module) UpdatePractice(ctx context.Context, a Actor, practiceRef string, ch PracticeChange) (ManagedPractice, error) {
	if a.UserID == "" || !reUUID.MatchString(practiceRef) {
		return ManagedPractice{}, errNotFound
	}
	v := &validation{}
	if ch.Modes == nil && ch.FeeAmount == nil && ch.Timings == nil && ch.Active == nil {
		v.add("body", codeRequired)
	}
	var md []string
	if ch.Modes != nil {
		md = modes(v, "modes", *ch.Modes)
	}
	if ch.FeeAmount != nil {
		m.fee(v, "feeAmount", *ch.FeeAmount)
	}
	var timings string
	if ch.Timings != nil {
		timings = m.text(v, "timings", *ch.Timings, 0, 200, true)
	}
	if err := v.err(); err != nil {
		return ManagedPractice{}, err
	}
	var out ManagedPractice
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		var prof, clinic string
		var amount int64
		if err := tx.QueryRow(ctx, `SELECT professional_id, clinic_id, fee_amount FROM practice WHERE id = $1 FOR UPDATE`, practiceRef).
			Scan(&prof, &clinic, &amount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		owner := (a.Role == ip.RoleDoctor && prof == a.UserID) || (a.Role == ip.RoleClinicAdmin && clinic == a.ClinicRef)
		if !owner {
			return errNotFound // not theirs: same answer as unknown (K-SEC-04)
		}
		feeChanged := ch.FeeAmount != nil && *ch.FeeAmount != amount
		if _, err := tx.Exec(ctx, `UPDATE practice SET
			modes = CASE WHEN $2 THEN $3::text[] ELSE modes END,
			timings = CASE WHEN $4 THEN $5 ELSE timings END,
			active = COALESCE($6, active),
			fee_amount = CASE WHEN $7 THEN $8 ELSE fee_amount END,
			fee_version = fee_version + CASE WHEN $7 THEN 1 ELSE 0 END,
			fee_updated_at = CASE WHEN $7 THEN now() ELSE fee_updated_at END,
			updated_at = now() WHERE id = $1`,
			practiceRef, ch.Modes != nil, md, ch.Timings != nil, timings, ch.Active, feeChanged, derefInt(ch.FeeAmount)); err != nil {
			return err
		}
		var err error
		out, err = scanManaged(tx.QueryRow(ctx, `SELECT `+practiceCols+` FROM practice pr JOIN clinic c ON c.id = pr.clinic_id WHERE pr.id = $1`, practiceRef))
		if err != nil {
			return err
		}
		if err := m.audit(ctx, tx, a, "directory.practice.update", "practice", practiceRef, prof); err != nil {
			return err
		}
		return m.emitPracticeUpdated(ctx, tx, practiceRef)
	})
	return out, err
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// roundKm rounds a distance in metres to 0.1 km for display.
func roundKm(metres float64) float64 { return math.Round(metres/100) / 10 }
