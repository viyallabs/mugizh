// Package records is the records module: purpose consent, access permissions and the
// records.authorize gate (S-097; consent-design §2-§5, access-control-design §3-§5).
package records

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "records", Schema: "records", FS: sub}
}

const notifyChannel = "records_consent"

// Config holds values from the country pack and decisions log.
type Config struct {
	// ConsentVersions: purpose → text version in force (pack legal.consent_text_versions).
	ConsentVersions map[string]int
	// T1Window: how long T1 lasts after the consult closes (consent-design §2, 24 h [CONFIRM]).
	T1Window time.Duration
	// JITMax: longest staff grant (access-control-design §5: 4 h [CONFIRM]).
	JITMax time.Duration
	// CacheTTL: safety-net TTL of the in-process cache (decision D-14: 5 s).
	CacheTTL time.Duration
	// NoticeVersion is the patient privacy notice in force (pack legal.notice_versions),
	// recorded with withdrawals.
	NoticeVersion string
	// ProviderNoticeVersion is the provider privacy notice in force (pack legal.notice_versions
	// "provider"), shown with the D-* purposes of doctors, clinics and staff (S-275).
	ProviderNoticeVersion string
	// CaregiversEnabled turns the caregiver endpoints on (pack feature "caregivers", v1.1). Off: the
	// handlers answer 404. The gate itself never trusts the flag: with no link there is no access.
	CaregiversEnabled bool
}

// neededPurposes can only be withdrawn by closing the account (consent-design §5).
var neededPurposes = map[string]bool{"P-ACCOUNT": true, "P-CARE": true, "P-RECORD": true}

// jitReasons are the only reason codes for staff access (no free-form "other").
var jitReasons = map[string]bool{"complaint": true, "rights_request": true, "payment_dispute": true, "security_incident": true, "legal_order": true}

// clinicalJITReasons may unlock clinical data (access-control-design §5).
var clinicalJITReasons = map[string]bool{"complaint": true, "rights_request": true, "legal_order": true}

var (
	reUUID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	rePurpose = regexp.MustCompile(`^[PD]-[A-Z]+$`)
)

// role → data kinds the role may ever read (deny by default, access-control-design §3).
var roleKinds = map[string][]rp.DataKind{
	"doctor":            {rp.KindBooking, rp.KindIdentityBasic, rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments},
	"clinic_admin":      {rp.KindBooking},
	"front_desk":        {rp.KindBooking},
	"support":           {rp.KindBooking, rp.KindPayment},
	"ops":               {rp.KindBooking, rp.KindPayment},
	"grievance_officer": {rp.KindBooking, rp.KindPayment, rp.KindIdentityBasic, rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments},
	"security_owner":    {rp.KindBooking, rp.KindPayment, rp.KindIdentityBasic, rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments},
	// A caregiver reads only what the patient's link scopes name (kindScope). Never clinical notes, never JIT-only kinds.
	ip.RoleCaregiver: {rp.KindBooking, rp.KindReminders, rp.KindPrescriptions, rp.KindDocuments},
}

// kindScope maps each data kind a caregiver may ever read to the scope that allows it. A kind that is not here is
// refused for caregivers (deny by default): identity, clinical notes of any kind, payments.
var kindScope = map[rp.DataKind]string{
	rp.KindReminders:     rp.ScopeReminders,
	rp.KindBooking:       rp.ScopeAppointments,
	rp.KindPrescriptions: rp.ScopePrescriptions,
	rp.KindDocuments:     rp.ScopeDocuments,
}

var staffRoles = map[string]bool{"support": true, "ops": true, "grievance_officer": true, "security_owner": true}

// tierKinds: data kinds each patient tier covers (consent-texts A2).
var tierKinds = map[rp.Tier][]rp.DataKind{
	rp.T1CurrentCare:  {rp.KindBooking, rp.KindIdentityBasic, rp.KindClinicalCurrent},
	rp.T2OwnRecords:   {rp.KindClinicalOwn},
	rp.T3HistoryShare: {rp.KindIdentityBasic, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments},
	rp.T4Extension:    {rp.KindIdentityBasic, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments},
}

// tierPurpose: purpose consent each tier relies on (T2 rests on the legal record duty).
var tierPurpose = map[rp.Tier]string{rp.T1CurrentCare: "P-CARE", rp.T3HistoryShare: "P-SHARE", rp.T4Extension: "P-SHARE"}

// Module implements rp.Gate and the consent and grant operations.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	log  *slog.Logger
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]snapshot
	gen   map[string]uint64 // per-patient change counter (review M1)
	epoch uint64
	// roles verifies staff and approver roles for just-in-time grants (review M5).
	roles RoleChecker
	// Alert is called when break-glass access is granted (access-control-design §5).
	Alert func(ctx context.Context, code string)
	// caregivers is the pack's "caregivers" flag (Config.CaregiversEnabled).
	caregivers atomic.Bool
}

// CaregiversEnabled reports whether caregiver access is on in this country.
func (m *Module) CaregiversEnabled() bool { return m.caregivers.Load() }

// SetCaregiversEnabled sets the flag after construction (the composition root and tests).
func (m *Module) SetCaregiversEnabled(on bool) { m.caregivers.Store(on) }

type snapshot struct {
	states map[string]rp.ConsentState
	grants []rp.Grant
	exp    time.Time
	// erasing is true while the account is restricted by an erasure request (RET-18 hold).
	erasing bool
	// carers are the patient's active caregiver links (id, caregiver, scopes).
	carers []carerLink
}

type carerLink struct {
	id, caregiver string
	scopes        []string
}

// New builds the module. pool must use the records role.
func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Module, error) {
	if len(cfg.ConsentVersions) == 0 {
		return nil, errors.New("records: consent text versions are required (country pack)")
	}
	if cfg.T1Window <= 0 || cfg.JITMax <= 0 || cfg.JITMax > 4*time.Hour {
		return nil, errors.New("records: T1 window and JIT maximum (at most 4 h) are required")
	}
	if cfg.CacheTTL <= 0 || cfg.CacheTTL > 60*time.Second {
		cfg.CacheTTL = 5 * time.Second
	}
	m := &Module{pool: pool, cfg: cfg, log: log, now: time.Now, cache: map[string]snapshot{}, gen: map[string]uint64{}, Alert: func(context.Context, string) {}}
	m.caregivers.Store(cfg.CaregiversEnabled)
	return m, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

var _ rp.Gate = (*Module)(nil)

// ---- consent ----

// GrantConsent records a grant of a purpose for the version in force.
func (m *Module) GrantConsent(ctx context.Context, c rp.ConsentChange) error {
	return m.changeConsent(ctx, c, "grant")
}

// WithdrawConsent records a withdrawal. Needed purposes are refused (close the account).
func (m *Module) WithdrawConsent(ctx context.Context, c rp.ConsentChange) error {
	if neededPurposes[c.PurposeID] {
		return rp.ErrNeededConsent
	}
	return m.changeConsent(ctx, c, "withdraw")
}

func (m *Module) changeConsent(ctx context.Context, c rp.ConsentChange, action string) error {
	c, err := m.prepareConsent(c, action)
	if err != nil {
		return err
	}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { return m.writeConsent(ctx, tx, c, action) })
	if err == nil {
		m.Invalidate(c.SubjectRef)
	}
	return err
}

// GrantConsentTx records a grant inside the caller's transaction (review F11: an extension
// approval records the consent and the grant together, or neither). The caller invalidates the
// patient's cache after commit (Invalidate).
func (m *Module) GrantConsentTx(ctx context.Context, tx pgx.Tx, c rp.ConsentChange) error {
	c, err := m.prepareConsent(c, "grant")
	if err != nil {
		return err
	}
	return m.writeConsent(ctx, tx, c, "grant")
}

// prepareConsent validates a change and fills its defaults.
func (m *Module) prepareConsent(c rp.ConsentChange, action string) (rp.ConsentChange, error) {
	if c.GivenByRef == "" {
		c.GivenByRef = c.SubjectRef
	}
	if !reUUID.MatchString(c.SubjectRef) || !reUUID.MatchString(c.GivenByRef) || !rePurpose.MatchString(c.PurposeID) {
		return c, rp.ErrInvalid
	}
	// v1.0 is adults only (K-DP-10): nobody consents on behalf of another person.
	if c.GivenByRef != c.SubjectRef {
		return c, fmt.Errorf("%w: consent on behalf of another person is not supported in v1.0", rp.ErrInvalid)
	}
	cur, ok := m.cfg.ConsentVersions[c.PurposeID]
	if !ok {
		return c, fmt.Errorf("%w: unknown purpose", rp.ErrInvalid)
	}
	if action == "grant" && c.TextVersion != cur {
		return c, fmt.Errorf("%w: consent text version %d is not the version in force", rp.ErrInvalid, c.TextVersion)
	}
	// A consent is recorded with the privacy notice in force for its kind of purpose: the patient
	// notice for P-*, the provider notice for D-* (DPDP s.5: the notice accompanies the request).
	notice := m.cfg.NoticeVersion
	if strings.HasPrefix(c.PurposeID, "D-") {
		notice = m.cfg.ProviderNoticeVersion
	}
	if action == "grant" && notice != "" {
		if c.NoticeVersion == "" {
			c.NoticeVersion = notice
		}
		if c.NoticeVersion != notice {
			return c, fmt.Errorf("%w: the privacy notice shown is not the version in force", rp.ErrInvalid)
		}
	}
	if action == "withdraw" {
		c.TextVersion = cur
		if c.NoticeVersion == "" {
			c.NoticeVersion = notice
		}
	}
	switch c.Channel {
	case "app", "web", "paper":
	default:
		return c, fmt.Errorf("%w: channel", rp.ErrInvalid)
	}
	return c, nil
}

// writeConsent writes a prepared change, its audit event and its event in the transaction.
func (m *Module) writeConsent(ctx context.Context, tx pgx.Tx, c rp.ConsentChange, action string) error {
	// Withdrawing P-CAREGIVER takes the patient lock first, so a redeem or an invitation that is running either
	// finishes before the withdrawal (and is revoked by it) or sees the withdrawal and refuses (same lock order as
	// redeem: caregiver, network, patient).
	if action == "withdraw" && c.PurposeID == purposeCaregiver {
		if err := lock(ctx, tx, lockPatient, c.SubjectRef); err != nil {
			return err
		}
	}
	now := m.now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO consent_record (id, subject_ref, given_by_ref, purpose_id, text_version, notice_version, action, channel, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ids.New(), c.SubjectRef, c.GivenByRef, c.PurposeID, c.TextVersion, c.NoticeVersion, action, c.Channel, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO consent_state (subject_ref, purpose_id, granted, text_version, updated_at) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (subject_ref, purpose_id) DO UPDATE SET granted=EXCLUDED.granted, text_version=EXCLUDED.text_version, updated_at=EXCLUDED.updated_at`,
		c.SubjectRef, c.PurposeID, action == "grant", c.TextVersion, now); err != nil {
		return err
	}
	evType := "consent.granted"
	if action == "withdraw" {
		evType = "consent.revoked"
	}
	role := c.ActorRole
	if role == "" {
		role = "patient"
	}
	if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.GivenByRef, ActorRole: role, Action: "consent." + action,
		SubjectRef: c.SubjectRef, ResourceType: "consent", Purpose: c.PurposeID, Result: ap.Allowed, RequestID: c.RequestID}); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, outbox.Event{Type: evType, SubjectRef: c.SubjectRef, Payload: map[string]string{"purposeId": c.PurposeID}}); err != nil {
		return err
	}
	// Withdrawing P-CAREGIVER ends every caregiver link and open invitation at once, in this transaction.
	if action == "withdraw" && c.PurposeID == purposeCaregiver {
		if err := endAllCaregiving(ctx, tx, c.SubjectRef, c.SubjectRef, "patient", now, c.RequestID, "consent_withdrawn"); err != nil {
			return err
		}
	}
	return notify(ctx, tx, c.SubjectRef)
}

// Consents returns the subject's current consent states.
func (m *Module) Consents(ctx context.Context, subjectRef string) ([]rp.ConsentState, error) {
	s, err := m.snapshot(ctx, subjectRef)
	if err != nil {
		return nil, err
	}
	out := make([]rp.ConsentState, 0, len(s.states))
	for _, v := range s.states {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b rp.ConsentState) int { return strings.Compare(a.PurposeID, b.PurposeID) })
	return out, nil
}

// HasConsent reports whether the purpose is granted now. Workers call it at send time
// (e.g. P-REMIND, P-MEDNAMES), never relying on a value from scheduling time.
func (m *Module) HasConsent(ctx context.Context, subjectRef, purposeID string) (bool, error) {
	if !reUUID.MatchString(subjectRef) {
		return false, rp.ErrInvalid
	}
	s, err := m.snapshot(ctx, subjectRef)
	if err != nil {
		return false, err
	}
	st, ok := s.states[purposeID]
	return ok && st.Granted, nil
}

// ---- grants ----

// GrantT1 creates current-care access when an appointment is confirmed. It lasts until
// the slot end plus the completion window; ExtendT1 moves it when the consult ends.
func (m *Module) GrantT1(ctx context.Context, patientRef, doctorRef, clinicRef, bookingRef string, slotEnd time.Time) (string, error) {
	if !allUUID(patientRef, doctorRef, clinicRef, bookingRef) {
		return "", rp.ErrInvalid
	}
	now := m.now().UTC()
	exp := slotEnd.UTC().Add(m.cfg.T1Window)
	if !exp.After(now) {
		return "", fmt.Errorf("%w: T1 would already be expired", rp.ErrInvalid)
	}
	id := ids.New()
	err := m.insertGrant(ctx, rp.Grant{ID: id, PatientRef: patientRef, GranteeRef: doctorRef, ClinicRef: clinicRef, Tier: rp.T1CurrentCare,
		ScopeRef: bookingRef, Basis: "P-CARE", StartsAt: now, ExpiresAt: &exp}, "appointment.confirmed", "system", "")
	if db.IsUniqueViolation(err, "permission_grant_t1_once") {
		return m.grantID(ctx, `SELECT id FROM permission_grant WHERE tier='T1_current_care' AND patient_ref=$1 AND grantee_ref=$2 AND scope_ref=$3`, patientRef, doctorRef, bookingRef)
	}
	return id, err
}

// ExtendT1 sets T1 to end the completion window after the consult closed.
func (m *Module) ExtendT1(ctx context.Context, bookingRef string, consultEndedAt time.Time) error {
	exp := consultEndedAt.UTC().Add(m.cfg.T1Window)
	var patient string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE permission_grant SET expires_at=$2 WHERE tier='T1_current_care' AND scope_ref=$1 AND revoked_at IS NULL
			RETURNING patient_ref`, bookingRef, exp).Scan(&patient)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return notify(ctx, tx, patient)
	})
	if err == nil && patient != "" {
		m.Invalidate(patient)
	}
	return err
}

// GrantT2 records the treating doctor's own-records access when they sign their first
// record for a patient. Not revocable by the patient while RET-02 runs.
func (m *Module) GrantT2(ctx context.Context, patientRef, doctorRef, clinicRef string) (string, error) {
	if !allUUID(patientRef, doctorRef) || (clinicRef != "" && !reUUID.MatchString(clinicRef)) {
		return "", rp.ErrInvalid
	}
	id := ids.New()
	err := m.insertGrant(ctx, rp.Grant{ID: id, PatientRef: patientRef, GranteeRef: doctorRef, ClinicRef: clinicRef, Tier: rp.T2OwnRecords,
		Basis: "legal_record_duty", StartsAt: m.now().UTC()}, "record.signed", "system", "")
	if db.IsUniqueViolation(err, "permission_grant_t2_once") {
		return m.grantID(ctx, `SELECT id FROM permission_grant WHERE tier='T2_own_records' AND patient_ref=$1 AND grantee_ref=$2 AND revoked_at IS NULL LIMIT 1`, patientRef, doctorRef)
	}
	return id, err
}

// ShareT3 records the patient's P-SHARE tap: consent and the grant in one step.
func (m *Module) ShareT3(ctx context.Context, patientRef, doctorRef, clinicRef, bookingRef string, until time.Time, shareTextVersion int, noticeVersion, requestID string) (string, error) {
	if !allUUID(patientRef, doctorRef) {
		return "", rp.ErrInvalid
	}
	now := m.now().UTC()
	if !until.After(now) || until.Sub(now) > 90*24*time.Hour {
		return "", fmt.Errorf("%w: share expiry", rp.ErrInvalid)
	}
	id, _, err := m.shareT3Once(ctx, patientRef, doctorRef, clinicRef, bookingRef, until, shareTextVersion, noticeVersion, requestID, now)
	return id, err
}

// shareT3Once creates the share, or returns the active one for the same doctor and appointment
// (review F10: a double tap creates one share). The check, the P-SHARE consent and the grant
// run in one transaction under a transaction-scoped lock, so nothing holds a connection while
// waiting for another (no pool exhaustion under bursts).
func (m *Module) shareT3Once(ctx context.Context, patientRef, doctorRef, clinicRef, bookingRef string, until time.Time, shareTextVersion int,
	noticeVersion, requestID string, now time.Time) (string, time.Time, error) {
	var id string
	var exp time.Time
	created := false
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		created = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, patientRef+"|"+bookingRef); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id::text, expires_at FROM permission_grant WHERE patient_ref=$1 AND grantee_ref=$2 AND scope_ref=$3
			AND tier='T3_history_share' AND revoked_at IS NULL AND expires_at > $4 ORDER BY expires_at DESC LIMIT 1`, patientRef, doctorRef, bookingRef, now).Scan(&id, &exp)
		if err == nil {
			return nil // the share already exists
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := m.GrantConsentTx(ctx, tx, rp.ConsentChange{SubjectRef: patientRef, PurposeID: "P-SHARE", TextVersion: shareTextVersion,
			NoticeVersion: noticeVersion, Channel: "app", RequestID: requestID}); err != nil {
			return err
		}
		id, exp, created = ids.New(), until.UTC(), true
		return insertGrantTx(ctx, tx, rp.Grant{ID: id, PatientRef: patientRef, GranteeRef: doctorRef, ClinicRef: clinicRef, Tier: rp.T3HistoryShare,
			ScopeRef: bookingRef, Basis: "P-SHARE", StartsAt: now, ExpiresAt: &exp}, "share.patient", patientRef, requestID)
	})
	if err == nil && created {
		m.Invalidate(patientRef)
	}
	return id, exp, err
}

// GrantJIT creates a staff just-in-time grant (K-SEC-08). Break-glass needs no approver
// but raises an alert; any other grant needs an approver who is not the requester.
func (m *Module) GrantJIT(ctx context.Context, r rp.JITRequest) (string, error) {
	if !allUUID(r.StaffRef, r.PatientRef) || r.TicketRef == "" || !jitReasons[r.ReasonCode] || len(r.Kinds) == 0 {
		return "", rp.ErrInvalid
	}
	if !staffRoles[r.StaffRole] {
		return "", fmt.Errorf("%w: role %s cannot hold staff grants", rp.ErrInvalid, r.StaffRole)
	}
	if r.Duration <= 0 || r.Duration > m.cfg.JITMax {
		return "", fmt.Errorf("%w: duration must be between 0 and %s", rp.ErrInvalid, m.cfg.JITMax)
	}
	allowed := roleKinds[r.StaffRole]
	for _, k := range r.Kinds {
		if !slices.Contains(allowed, k) {
			return "", fmt.Errorf("%w: role %s may not be granted %s", rp.ErrInvalid, r.StaffRole, k)
		}
		if isClinical(k) && !clinicalJITReasons[r.ReasonCode] {
			return "", fmt.Errorf("%w: clinical data only for complaints, rights requests or legal orders", rp.ErrInvalid)
		}
	}
	tier := rp.JITStaff
	if r.BreakGlass {
		if r.StaffRole != "security_owner" || r.ReasonCode != "security_incident" {
			return "", fmt.Errorf("%w: break-glass is for the security owner during a security incident", rp.ErrInvalid)
		}
		tier = rp.BreakGlass
	} else {
		if r.ApproverRef == "" || !reUUID.MatchString(r.ApproverRef) {
			return "", rp.ErrInvalid
		}
		if r.ApproverRef == r.StaffRef {
			return "", rp.ErrSelfApproval
		}
	}
	if err := m.checkStaffRoles(ctx, r); err != nil {
		return "", err
	}
	now := m.now().UTC()
	exp := now.Add(r.Duration)
	id := ids.New()
	g := rp.Grant{ID: id, PatientRef: r.PatientRef, GranteeRef: r.StaffRef, Tier: tier, ScopeRef: r.TicketRef, Kinds: r.Kinds,
		Basis: "staff_" + r.ReasonCode, StartsAt: now, ExpiresAt: &exp, ApprovedBy: r.ApproverRef, ReasonCode: r.ReasonCode}
	if err := m.insertGrant(ctx, g, "admin.jit", r.StaffRef, ""); err != nil {
		return "", err
	}
	if r.BreakGlass {
		m.log.Warn("records.break_glass_granted", "grant_id", id)
		m.Alert(ctx, "access.break_glass")
	}
	return id, nil
}

func isClinical(k rp.DataKind) bool {
	switch k {
	case rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments, rp.KindIdentityBasic:
		return true
	}
	return false
}

func (m *Module) insertGrant(ctx context.Context, g rp.Grant, createdFrom, actorRef, requestID string) error {
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return insertGrantTx(ctx, tx, g, createdFrom, actorRef, requestID)
	})
	if err == nil {
		m.Invalidate(g.PatientRef)
	}
	return err
}

// insertGrantTx writes a grant with its audit event, outbox event and NOTIFY in the caller's
// transaction. The caller invalidates the patient's cache after the commit.
func insertGrantTx(ctx context.Context, tx pgx.Tx, g rp.Grant, createdFrom, actorRef, requestID string) error {
	kinds := make([]string, 0, len(g.Kinds))
	for _, k := range g.Kinds {
		kinds = append(kinds, string(k))
	}
	var clinic, approver any
	if g.ClinicRef != "" {
		clinic = g.ClinicRef
	}
	if g.ApprovedBy != "" {
		approver = g.ApprovedBy
	}
	if _, err := tx.Exec(ctx, `INSERT INTO permission_grant (id, patient_ref, grantee_ref, clinic_ref, tier, scope_ref, kinds, basis, starts_at, expires_at, approved_by, reason_code, created_from)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, g.ID, g.PatientRef, g.GranteeRef, clinic, string(g.Tier), g.ScopeRef, kinds, g.Basis,
		g.StartsAt, g.ExpiresAt, approver, g.ReasonCode, createdFrom); err != nil {
		return err
	}
	actorRole := "system"
	if actorRef == g.PatientRef {
		actorRole = "patient"
	} else if actorRef != "system" {
		actorRole = "staff"
	}
	if actorRef == "system" {
		actorRef = g.GranteeRef // the system acts on the grantee's behalf; reference stays pseudonymous
	}
	if err := ap.Record(ctx, tx, ap.Event{ActorRef: actorRef, ActorRole: actorRole, Action: "permission.created", SubjectRef: g.PatientRef,
		ResourceType: "permission_grant", ResourceRef: g.ID, Purpose: string(g.Tier), GrantID: g.ID, ReasonCode: g.ReasonCode,
		TicketRef: ticketOrEmpty(g), Result: ap.Allowed, RequestID: requestID}); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, outbox.Event{Type: "consent.granted", SubjectRef: g.PatientRef, Payload: map[string]string{"grantId": g.ID, "tier": string(g.Tier)}}); err != nil {
		return err
	}
	return notify(ctx, tx, g.PatientRef)
}

func ticketOrEmpty(g rp.Grant) string {
	if g.Tier == rp.JITStaff || g.Tier == rp.BreakGlass {
		return g.ScopeRef
	}
	return ""
}

// Revoke ends a grant. The patient may revoke only what they gave (T3, T4); T1 and T2
// are revoked by the system (e.g. cancellation, doctor leaving a clinic).
func (m *Module) Revoke(ctx context.Context, grantID, byRef string, byPatient bool, requestID string) error {
	if !allUUID(grantID, byRef) {
		return rp.ErrInvalid
	}
	var patient, tier string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT patient_ref, tier FROM permission_grant WHERE id=$1 AND revoked_at IS NULL FOR UPDATE`, grantID).Scan(&patient, &tier); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return rp.ErrInvalid
			}
			return err
		}
		if byPatient {
			if patient != byRef {
				return rp.ErrInvalid // not theirs: indistinguishable from "no such grant"
			}
			if tier != string(rp.T3HistoryShare) && tier != string(rp.T4Extension) {
				return rp.ErrNotRevocable
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE permission_grant SET revoked_at=$2, revoked_by=$3 WHERE id=$1`, grantID, m.now().UTC(), byRef); err != nil {
			return err
		}
		role := "system"
		if byPatient {
			role = "patient"
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: byRef, ActorRole: role, Action: "permission.revoked", SubjectRef: patient,
			ResourceType: "permission_grant", ResourceRef: grantID, Purpose: tier, GrantID: grantID, Result: ap.Allowed, RequestID: requestID}); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: "consent.revoked", SubjectRef: patient, Payload: map[string]string{"grantId": grantID, "tier": tier}}); err != nil {
			return err
		}
		return notify(ctx, tx, patient)
	})
	// Revocation acts synchronously on this instance before returning (consent-design §4);
	// other instances drop their entry on the NOTIFY.
	if patient != "" {
		m.Invalidate(patient)
	}
	return err
}

// Grants lists the patient's grants (for Settings → Privacy and the access log).
func (m *Module) Grants(ctx context.Context, patientRef string) ([]rp.Grant, error) {
	s, err := m.snapshot(ctx, patientRef)
	if err != nil {
		return nil, err
	}
	return append([]rp.Grant(nil), s.grants...), nil
}

// ---- the gate ----

// Authorize is records.authorize. Allowed reads and every denial are audited durably
// before the decision is returned; if the audit write fails, the read fails.
func (m *Module) Authorize(ctx context.Context, r rp.Request) (rp.Decision, error) {
	if !reUUID.MatchString(r.PatientRef) || r.Actor.Ref == "" || r.Kind == "" {
		return rp.Decision{}, rp.ErrInvalid
	}
	var d rp.Decision
	if r.Actor.Restricted {
		// Not yet re-verified device: nothing is readable, not even the patient's own data
		// (E-DP-22). The denial is audited below.
		d = rp.Decision{Reason: "session_restricted"}
	} else if r.Actor.Ref == r.PatientRef && r.Actor.Role == "patient" {
		// The patient reading their own data needs no grant (K-DP-02 covers others' reads).
		return rp.Decision{Allowed: true, Tier: rp.Self}, nil
	} else {
		var err error
		if d, err = m.decide(ctx, r); err != nil {
			return rp.Decision{}, err
		}
	}
	res := ap.Denied
	if d.Allowed {
		res = ap.Allowed
	}
	purpose := string(d.Tier)
	if !d.Allowed {
		purpose = d.Reason
	}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return ap.Record(ctx, tx, ap.Event{ActorRef: r.Actor.Ref, ActorRole: safeRole(r.Actor.Role), Action: "records.read." + string(r.Kind),
			SubjectRef: r.PatientRef, ResourceType: r.ResourceType, ResourceRef: r.ResourceRef, Purpose: purpose, GrantID: d.GrantID,
			Result: res, Client: r.Client, IPHash: r.IPHash, RequestID: r.RequestID})
	})
	if err != nil {
		m.log.Error("records.audit_failed", logging.Err(err))
		return rp.Decision{}, fmt.Errorf("records: audit failed, read refused: %w", err)
	}
	if !d.Allowed {
		m.log.Info("records.read_denied", "reason", d.Reason, "actor_ref", r.Actor.Ref)
	}
	return d, nil
}

func safeRole(role string) string {
	if _, ok := roleKinds[role]; ok || role == "patient" {
		return role
	}
	return "unknown"
}

func (m *Module) decide(ctx context.Context, r rp.Request) (rp.Decision, error) {
	deny := func(reason string, hide bool) rp.Decision { return rp.Decision{Reason: reason, Hide: hide} }
	kinds, ok := roleKinds[r.Actor.Role]
	if !ok || !slices.Contains(kinds, r.Kind) {
		return deny("role_not_allowed", true), nil
	}
	s, err := m.snapshot(ctx, r.PatientRef)
	if err != nil {
		return rp.Decision{}, err
	}
	if r.Actor.Role == ip.RoleCaregiver {
		return m.decideCaregiver(s, r), nil
	}
	now := m.now()
	related, expired, outOfScope, expiredCovering := false, false, false, false
	for _, g := range s.grants {
		if !m.relates(r.Actor, g) {
			continue
		}
		if s.erasing && !heldAccessOK(g, r) {
			continue // account under erasure: hidden from every role (RET-18)
		}
		related = true
		if g.RevokedAt != nil || now.Before(g.StartsAt) || (g.ExpiresAt != nil && !now.Before(*g.ExpiresAt)) {
			expired = true
			if m.covers(r, g) {
				expiredCovering = true // the access that would have served this read has ended
			}
			continue
		}
		if !m.covers(r, g) {
			outOfScope = true
			continue
		}
		if p, ok := tierPurpose[g.Tier]; ok {
			if st, ok := s.states[p]; !ok || !st.Granted {
				continue
			}
		}
		if r.Purpose != "" {
			if st, ok := s.states[r.Purpose]; !ok || !st.Granted {
				return deny("purpose_not_granted", false), nil
			}
		}
		return rp.Decision{Allowed: true, Tier: g.Tier, GrantID: g.ID}, nil
	}
	switch {
	case !related:
		return deny("no_relationship", true), nil
	case expiredCovering:
		return deny("grant_expired", false), nil
	case outOfScope:
		return deny("out_of_scope", false), nil
	case expired:
		return deny("grant_expired", false), nil
	}
	return deny("no_active_grant", false), nil
}

// decideCaregiver is the caregiver tier (v1.1, E-FAM-02). Allowed only for an active link of this actor with this
// patient, a kind that maps to a scope on that link, and while the patient's P-CAREGIVER consent is granted. A
// patient under an erasure restriction is hidden from caregivers too. Anything unmapped is refused.
func (m *Module) decideCaregiver(s snapshot, r rp.Request) rp.Decision {
	deny := func(reason string, hide bool) rp.Decision { return rp.Decision{Reason: reason, Hide: hide} }
	scope, ok := kindScope[r.Kind]
	if !ok {
		return deny("role_not_allowed", true)
	}
	if s.erasing {
		return deny("no_relationship", true) // hidden from every role (RET-18)
	}
	var link *carerLink
	for i := range s.carers {
		if s.carers[i].caregiver == r.Actor.Ref {
			link = &s.carers[i]
			break
		}
	}
	if link == nil {
		return deny("no_relationship", true)
	}
	if !slices.Contains(link.scopes, scope) {
		return deny("out_of_scope", false)
	}
	if st, ok := s.states["P-CAREGIVER"]; !ok || !st.Granted {
		return deny("purpose_not_granted", false)
	}
	if r.Purpose != "" {
		if st, ok := s.states[r.Purpose]; !ok || !st.Granted {
			return deny("purpose_not_granted", false)
		}
	}
	return rp.Decision{Allowed: true, Tier: rp.Caregiver, GrantID: link.id}
}

// relates reports whether the grant is held by this actor (or the actor's clinic, for
// clinic roles, which only ever see booking fields).
func (m *Module) relates(a rp.Actor, g rp.Grant) bool {
	switch a.Role {
	case "doctor":
		return g.GranteeRef == a.Ref && (g.Tier == rp.T1CurrentCare || g.Tier == rp.T2OwnRecords || g.Tier == rp.T3HistoryShare || g.Tier == rp.T4Extension)
	case "clinic_admin", "front_desk":
		return a.ClinicRef != "" && g.ClinicRef == a.ClinicRef && g.Tier == rp.T1CurrentCare
	case "security_owner":
		return g.GranteeRef == a.Ref && g.Tier == rp.BreakGlass
	default:
		return staffRoles[a.Role] && g.GranteeRef == a.Ref && g.Tier == rp.JITStaff
	}
}

func (m *Module) covers(r rp.Request, g rp.Grant) bool {
	switch g.Tier {
	case rp.JITStaff, rp.BreakGlass:
		return slices.Contains(g.Kinds, r.Kind)
	case rp.T1CurrentCare:
		return r.BookingRef != "" && r.BookingRef == g.ScopeRef && slices.Contains(tierKinds[g.Tier], r.Kind)
	case rp.T2OwnRecords:
		return slices.Contains(tierKinds[g.Tier], r.Kind) && r.OwnerRef != "" && (r.OwnerRef == g.GranteeRef || (g.ClinicRef != "" && r.OwnerRef == g.ClinicRef))
	default:
		return slices.Contains(tierKinds[g.Tier], r.Kind)
	}
}

// ---- cache ----

func (m *Module) snapshot(ctx context.Context, patient string) (snapshot, error) {
	now := m.now()
	m.mu.Lock()
	if s, ok := m.cache[patient]; ok && now.Before(s.exp) {
		m.mu.Unlock()
		return s, nil
	}
	// Remember the generation before reading. If a change for this patient (or a full
	// flush) happens while we read, the result is used for this request only and never
	// cached, so a revoke can never be undone by a slow reader (review M1).
	gen0, epoch0 := m.gen[patient], m.epoch
	m.mu.Unlock()

	s := snapshot{states: map[string]rp.ConsentState{}}
	// One consistent view of consents and grants.
	err := db.InTx(ctx, m.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		s.states, s.grants, s.carers = map[string]rp.ConsentState{}, nil, nil
		rows, err := tx.Query(ctx, `SELECT purpose_id, granted, text_version, updated_at FROM consent_state WHERE subject_ref=$1`, patient)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c rp.ConsentState
			if err := rows.Scan(&c.PurposeID, &c.Granted, &c.TextVersion, &c.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			s.states[c.PurposeID] = c
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id, patient_ref, grantee_ref, coalesce(clinic_ref::text,''), tier, scope_ref, kinds, basis, starts_at,
			expires_at, revoked_at, coalesce(approved_by::text,''), reason_code FROM permission_grant WHERE patient_ref=$1 ORDER BY created_at`, patient)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g rp.Grant
			var tier string
			var kinds []string
			if err := rows.Scan(&g.ID, &g.PatientRef, &g.GranteeRef, &g.ClinicRef, &tier, &g.ScopeRef, &kinds, &g.Basis, &g.StartsAt,
				&g.ExpiresAt, &g.RevokedAt, &g.ApprovedBy, &g.ReasonCode); err != nil {
				return err
			}
			g.Tier = rp.Tier(tier)
			for _, k := range kinds {
				g.Kinds = append(g.Kinds, rp.DataKind(k))
			}
			s.grants = append(s.grants, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_restriction WHERE subject_ref=$1 AND released_at IS NULL`, patient).Scan(&n); err != nil {
			return err
		}
		s.erasing = n > 0
		crows, err := tx.Query(ctx, `SELECT id::text, caregiver_ref::text, scopes FROM caregiver_link WHERE patient_ref=$1 AND status='active'`, patient)
		if err != nil {
			return err
		}
		defer crows.Close()
		for crows.Next() {
			var c carerLink
			if err := crows.Scan(&c.id, &c.caregiver, &c.scopes); err != nil {
				return err
			}
			s.carers = append(s.carers, c)
		}
		return crows.Err()
	})
	if err != nil {
		return s, err
	}
	s.exp = now.Add(m.cfg.CacheTTL)
	m.mu.Lock()
	if m.gen[patient] == gen0 && m.epoch == epoch0 {
		if len(m.cache) > 100_000 {
			m.cache = map[string]snapshot{}
		}
		m.cache[patient] = s
	}
	m.mu.Unlock()
	return s, nil
}

// Invalidate drops one patient's cached consent and grants and bumps their generation,
// so an in-flight read for that patient will not be cached.
func (m *Module) Invalidate(patient string) {
	m.mu.Lock()
	delete(m.cache, patient)
	if len(m.gen) > 200_000 {
		m.gen = map[string]uint64{}
		m.epoch++
	}
	m.gen[patient]++
	m.mu.Unlock()
}

// FlushCache drops every cached entry (on listener reconnect) and starts a new epoch.
func (m *Module) FlushCache() {
	m.mu.Lock()
	m.cache = map[string]snapshot{}
	m.epoch++
	m.mu.Unlock()
}

func notify(ctx context.Context, tx pgx.Tx, patient string) error {
	_, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, patient)
	return err
}

// Listen keeps the cache fresh across instances: every consent or grant change
// NOTIFYs the patient reference; the cache entry is dropped. On (re)connect the whole
// cache is flushed, so a missed notification can never keep stale access.
func (m *Module) Listen(ctx context.Context) {
	for ctx.Err() == nil {
		m.listenOnce(ctx)
		m.FlushCache()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (m *Module) listenOnce(ctx context.Context) {
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return
	}
	m.FlushCache()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("records.listener_reconnect", logging.Err(err))
			}
			return
		}
		m.Invalidate(n.Payload)
	}
}

func allUUID(v ...string) bool {
	for _, s := range v {
		if !reUUID.MatchString(s) {
			return false
		}
	}
	return true
}

func (m *Module) grantID(ctx context.Context, sql string, args ...any) (string, error) {
	var id string
	err := m.pool.QueryRow(ctx, sql, args...).Scan(&id)
	return id, err
}

// RoleChecker answers whether a user holds a role (implemented by the identity module).
type RoleChecker interface {
	HasRole(ctx context.Context, userID, role string) (bool, error)
}

// SetRoleChecker installs the role checker. Without it every staff grant is refused.
func (m *Module) SetRoleChecker(r RoleChecker) { m.roles = r }

// checkStaffRoles verifies that the requester holds the staff role they claim and that
// the approver is a security owner (K-SEC-08: a real, different second person).
func (m *Module) checkStaffRoles(ctx context.Context, r rp.JITRequest) error {
	if m.roles == nil {
		return errors.New("records: role checker not configured; staff grants are refused")
	}
	ok, err := m.roles.HasRole(ctx, r.StaffRef, r.StaffRole)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: the requester does not hold role %s", rp.ErrInvalid, r.StaffRole)
	}
	if r.BreakGlass {
		return nil
	}
	ok, err = m.roles.HasRole(ctx, r.ApproverRef, "security_owner")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: the approver is not a security owner", rp.ErrInvalid)
	}
	return nil
}

// RevokeForBooking ends current-care access (T1) when its appointment is cancelled, expired
// or never confirmed (consent-design §2). T2 (records the doctor already signed) is not
// affected. Idempotent: an already revoked or missing grant is not an error.
func (m *Module) RevokeForBooking(ctx context.Context, bookingRef string) error {
	if !reUUID.MatchString(bookingRef) {
		return rp.ErrInvalid
	}
	var patients []string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		patients = nil
		rows, err := tx.Query(ctx, `UPDATE permission_grant SET revoked_at=$2 WHERE tier='T1_current_care' AND scope_ref=$1 AND revoked_at IS NULL
			RETURNING id, patient_ref, grantee_ref`, bookingRef, m.now().UTC())
		if err != nil {
			return err
		}
		type rv struct{ id, patient, grantee string }
		var revoked []rv
		for rows.Next() {
			var r rv
			if err := rows.Scan(&r.id, &r.patient, &r.grantee); err != nil {
				rows.Close()
				return err
			}
			revoked = append(revoked, r)
		}
		rows.Close()
		for _, r := range revoked {
			patients = append(patients, r.patient)
			if err := ap.Record(ctx, tx, ap.Event{ActorRef: r.grantee, ActorRole: "system", Action: "permission.revoked", SubjectRef: r.patient,
				ResourceType: "permission_grant", ResourceRef: r.id, Purpose: string(rp.T1CurrentCare), GrantID: r.id, Result: ap.Allowed}); err != nil {
				return err
			}
			if err := outbox.Emit(ctx, tx, outbox.Event{Type: "consent.revoked", SubjectRef: r.patient, Payload: map[string]string{"grantId": r.id, "tier": string(rp.T1CurrentCare)}}); err != nil {
				return err
			}
			if err := notify(ctx, tx, r.patient); err != nil {
				return err
			}
		}
		return nil
	})
	for _, p := range patients {
		m.Invalidate(p)
	}
	return err
}
