package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// ProfileConfig holds the country pack values the profile needs (K-GLB-01). Until
// SetProfileConfig is called with both values, every profile write fails closed.
type ProfileConfig struct {
	MinAccountAge        int      // pack people.min_account_age (K-DP-10)
	ProhibitedValidators []string // pack prohibited_identifiers validator names (K-DP-05)
}

// SetProfileConfig sets the profile configuration (composition root).
func (m *Module) SetProfileConfig(c ProfileConfig) { m.profileCfg = c }

// Profile problems.
var (
	ErrProfileNotConfigured = problem.New(http.StatusServiceUnavailable, "identity.profile_unavailable", "The profile cannot be saved right now")
	ErrProfileInvalid       = problem.New(http.StatusUnprocessableEntity, "identity.profile_invalid", "Check the profile fields")
	ErrProfileUnderage      = problem.New(http.StatusUnprocessableEntity, "identity.profile_underage", "This service is for adults only")
	ErrProfileProhibitedID  = problem.New(http.StatusUnprocessableEntity, "identity.profile_prohibited_identifier", "Do not enter an identity number here")
	ErrProfileMissing       = problem.New(http.StatusNotFound, "identity.profile_missing", "No profile yet")
)

// Allowed values. Blood groups and sex codes are medical vocabulary, not country values.
var (
	bloodGroups = map[string]bool{"A+": true, "A-": true, "B+": true, "B-": true, "AB+": true, "AB-": true, "O+": true, "O-": true, "unknown": true}
	sexValues   = map[string]bool{"female": true, "male": true, "other": true}
	reLang      = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
)

const (
	maxName      = 120
	maxAllergies = 20
	maxAllergy   = 100
)

// EmergencyContact is a third party's name and number, kept only with the patient's consent
// (data inventory 4.1).
type EmergencyContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// Profile is the patient's profile. DateOfBirth is YYYY-MM-DD.
type Profile struct {
	FullName          string
	DateOfBirth       string
	Sex               string
	PreferredLanguage string
	BloodGroup        string // empty when not given
	Allergies         []string
	EmergencyContact  *EmergencyContact
}

func profileAAD(userID, col string) crypto.AAD {
	return crypto.AAD{Table: "identity.profile", Column: col, RowID: userID}
}

func ageAt(dob, now time.Time) int {
	y := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		y--
	}
	return y
}

// validateProfile checks and normalises p. Nothing is stored or logged on failure.
func (m *Module) validateProfile(p Profile) (Profile, error) {
	c := m.profileCfg
	if c.MinAccountAge <= 0 || len(c.ProhibitedValidators) == 0 {
		return p, ErrProfileNotConfigured
	}
	bad := func(field string) error {
		return ErrProfileInvalid.WithFields(problem.FieldError{Field: field, Code: "invalid"})
	}
	p.FullName = strings.TrimSpace(p.FullName)
	if n := utf8.RuneCountInString(p.FullName); n < 1 || n > maxName {
		return p, bad("fullName")
	}
	dob, err := time.Parse("2006-01-02", p.DateOfBirth)
	now := m.now().UTC()
	if err != nil || dob.After(now) || dob.Year() < 1900 {
		return p, bad("dateOfBirth")
	}
	if ageAt(dob, now) < c.MinAccountAge { // adults only (K-DP-10)
		return p, ErrProfileUnderage
	}
	if !sexValues[p.Sex] {
		return p, bad("sex")
	}
	if p.PreferredLanguage != "" && !reLang.MatchString(p.PreferredLanguage) {
		return p, bad("preferredLanguage")
	}
	if p.BloodGroup != "" && !bloodGroups[p.BloodGroup] {
		return p, bad("bloodGroup")
	}
	if len(p.Allergies) > maxAllergies {
		return p, bad("allergies")
	}
	text := []string{p.FullName}
	allergies := make([]string, 0, len(p.Allergies))
	for _, a := range p.Allergies {
		a = strings.TrimSpace(a)
		if a == "" || utf8.RuneCountInString(a) > maxAllergy {
			return p, bad("allergies")
		}
		allergies = append(allergies, a)
	}
	p.Allergies = allergies
	text = append(text, allergies...)
	if ec := p.EmergencyContact; ec != nil {
		cp := *ec
		cp.Name = strings.TrimSpace(cp.Name)
		if n := utf8.RuneCountInString(cp.Name); n < 1 || n > maxName {
			return p, bad("emergencyContact")
		}
		ph, err := m.normalisePhone(cp.Phone)
		if err != nil {
			return p, bad("emergencyContact")
		}
		cp.Phone = ph
		p.EmergencyContact = &cp
		text = append(text, cp.Name)
	}
	for _, s := range text { // K-DP-05: reject identity numbers in free text
		if scrub.ContainsProhibitedID(s, c.ProhibitedValidators) {
			return p, ErrProfileProhibitedID
		}
	}
	return p, nil
}

// PutProfile creates or replaces the caller's own profile. Every personal field is sealed
// with the patient key (K-DP-03); the change is audited in the same transaction.
func (m *Module) PutProfile(ctx context.Context, userID string, in Profile) error {
	p, err := m.validateProfile(in)
	if err != nil {
		return err
	}
	seal := func(col string, plain []byte) (crypto.Sealed[crypto.PatientKey], error) {
		return crypto.SealPatient(ctx, m.cr, userID, profileAAD(userID, col), plain)
	}
	name, err := seal("full_name_ct", []byte(p.FullName))
	if err != nil {
		return err
	}
	dob, err := seal("dob_ct", []byte(p.DateOfBirth))
	if err != nil {
		return err
	}
	sex, err := seal("sex_ct", []byte(p.Sex))
	if err != nil {
		return err
	}
	var bg, al, ec crypto.Sealed[crypto.PatientKey]
	if p.BloodGroup != "" {
		if bg, err = seal("blood_group_ct", []byte(p.BloodGroup)); err != nil {
			return err
		}
	}
	if len(p.Allergies) > 0 {
		j, _ := json.Marshal(p.Allergies)
		if al, err = seal("allergies_ct", j); err != nil {
			return err
		}
	}
	if p.EmergencyContact != nil {
		j, _ := json.Marshal(p.EmergencyContact)
		if ec, err = seal("emergency_contact_ct", j); err != nil {
			return err
		}
	}
	nullable := func(s crypto.Sealed[crypto.PatientKey]) any {
		if s.IsZero() {
			return nil
		}
		return s
	}
	now := m.now()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE user_account SET preferred_lang=$2 WHERE id=$1 AND status='active'`, userID, p.PreferredLanguage)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		// Review F3: move identity_changed_at only when the name, date of birth or sex really
		// changed (an unreadable old value counts as a change: fail closed).
		identChanged := now
		var oldName, oldDOB, oldSex crypto.Sealed[crypto.PatientKey]
		var oldAt time.Time
		err = tx.QueryRow(ctx, `SELECT full_name_ct, dob_ct, sex_ct, identity_changed_at FROM profile WHERE user_id=$1 FOR UPDATE`, userID).
			Scan(&oldName, &oldDOB, &oldSex, &oldAt)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			n, e1 := m.openCol(ctx, userID, "full_name_ct", oldName)
			d, e2 := m.openCol(ctx, userID, "dob_ct", oldDOB)
			s, e3 := m.openCol(ctx, userID, "sex_ct", oldSex)
			if e1 == nil && e2 == nil && e3 == nil && n == p.FullName && d == p.DateOfBirth && s == p.Sex {
				identChanged = oldAt
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO profile (user_id, full_name_ct, dob_ct, sex_ct, blood_group_ct, allergies_ct, emergency_contact_ct, created_at, updated_at, identity_changed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9)
			ON CONFLICT (user_id) DO UPDATE SET full_name_ct=EXCLUDED.full_name_ct, dob_ct=EXCLUDED.dob_ct, sex_ct=EXCLUDED.sex_ct,
			  blood_group_ct=EXCLUDED.blood_group_ct, allergies_ct=EXCLUDED.allergies_ct, emergency_contact_ct=EXCLUDED.emergency_contact_ct, updated_at=EXCLUDED.updated_at,
			  identity_changed_at=EXCLUDED.identity_changed_at`,
			userID, name, dob, sex, nullable(bg), nullable(al), nullable(ec), now, identChanged); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: userID, ActorRole: ip.RolePatient, Action: "identity.profile_updated", SubjectRef: userID,
			ResourceType: "profile", ResourceRef: userID, Result: ap.Allowed})
	})
}

type profileRow struct {
	name, dob, sex, bg, al, ec crypto.Sealed[crypto.PatientKey]
}

func (m *Module) loadProfileRow(ctx context.Context, userID string) (profileRow, error) {
	var r profileRow
	err := m.pool.QueryRow(ctx, `SELECT p.full_name_ct, p.dob_ct, p.sex_ct, p.blood_group_ct, p.allergies_ct, p.emergency_contact_ct
		FROM profile p JOIN user_account u ON u.id=p.user_id WHERE p.user_id=$1 AND u.status='active'`, userID).
		Scan(&r.name, &r.dob, &r.sex, &r.bg, &r.al, &r.ec)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrProfileMissing
	}
	return r, err
}

func (m *Module) openCol(ctx context.Context, userID, col string, s crypto.Sealed[crypto.PatientKey]) (string, error) {
	b, err := crypto.Open(ctx, m.cr, profileAAD(userID, col), s)
	return string(b), err
}

// GetProfile returns the caller's own profile (the patient reading their own data).
func (m *Module) GetProfile(ctx context.Context, userID string) (Profile, error) {
	r, err := m.loadProfileRow(ctx, userID)
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if p.FullName, err = m.openCol(ctx, userID, "full_name_ct", r.name); err != nil {
		return Profile{}, err
	}
	if p.DateOfBirth, err = m.openCol(ctx, userID, "dob_ct", r.dob); err != nil {
		return Profile{}, err
	}
	if p.Sex, err = m.openCol(ctx, userID, "sex_ct", r.sex); err != nil {
		return Profile{}, err
	}
	if !r.bg.IsZero() {
		if p.BloodGroup, err = m.openCol(ctx, userID, "blood_group_ct", r.bg); err != nil {
			return Profile{}, err
		}
	}
	if !r.al.IsZero() {
		s, err := m.openCol(ctx, userID, "allergies_ct", r.al)
		if err != nil || json.Unmarshal([]byte(s), &p.Allergies) != nil {
			return Profile{}, errors.New("identity: profile allergies unreadable")
		}
	}
	if !r.ec.IsZero() {
		s, err := m.openCol(ctx, userID, "emergency_contact_ct", r.ec)
		var ec EmergencyContact
		if err != nil || json.Unmarshal([]byte(s), &ec) != nil {
			return Profile{}, errors.New("identity: profile emergency contact unreadable")
		}
		p.EmergencyContact = &ec
	}
	if err := m.pool.QueryRow(ctx, `SELECT preferred_lang FROM user_account WHERE id=$1`, userID).Scan(&p.PreferredLanguage); err != nil {
		return Profile{}, err
	}
	return p, nil
}

var _ ip.ProfileReader = (*Module)(nil)

// ProfileFacts implements ip.ProfileReader: name, completed age in years and sex, for the
// prescription (P02). Fails closed: a missing profile or a decryption failure is an error.
func (m *Module) ProfileFacts(ctx context.Context, userID string) (string, int, string, error) {
	r, err := m.loadProfileRow(ctx, userID)
	if err != nil {
		return "", 0, "", err
	}
	name, err := m.openCol(ctx, userID, "full_name_ct", r.name)
	if err != nil {
		return "", 0, "", err
	}
	dobS, err := m.openCol(ctx, userID, "dob_ct", r.dob)
	if err != nil {
		return "", 0, "", err
	}
	sex, err := m.openCol(ctx, userID, "sex_ct", r.sex)
	if err != nil {
		return "", 0, "", err
	}
	dob, err := time.Parse("2006-01-02", dobS)
	if err != nil {
		return "", 0, "", errors.New("identity: profile date of birth unreadable")
	}
	return name, ageAt(dob, m.now().UTC()), sex, nil
}

// ProfileIdentityChangedAt implements ip.ProfileChanges. A missing profile or an inactive
// account is an error, so callers fail closed.
func (m *Module) ProfileIdentityChangedAt(ctx context.Context, userID string) (time.Time, error) {
	var at time.Time
	err := m.pool.QueryRow(ctx, `SELECT p.identity_changed_at FROM profile p JOIN user_account u ON u.id=p.user_id WHERE p.user_id=$1 AND u.status='active'`, userID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrProfileMissing
	}
	return at.UTC(), err
}

var _ ip.ProfileChanges = (*Module)(nil)
