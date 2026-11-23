// Package consult is the consult module: one session per confirmed online appointment, the
// pre-join consent, join tokens, connection proof, outcomes (S-135, S-136, S-137, S-147,
// S-148; module-boundaries section 2).
//
// Rules built in:
//   - Telemedicine Practice Guidelines 2020: consent per consultation (TELE-CONSENT, recorded
//     in pre_join), patient identification by the doctor before the composer opens (P02),
//     the patient declares being in India for every online consult (P10, TC-24), the mode at
//     signing is known (E-RX-06).
//   - ADR-13: one room per appointment, tokens for the two participants only, never recorded
//     (a room that reports recording on is refused), India media region (vendor config).
//   - K-CS-01: a doctor must still be Verified to join.
//   - K-DP-02: the doctor sees the patient's name and age only through records.Authorize.
//   - K-DP-04: references, codes and times only in events, logs and the vendor.
//   - K-ARC-04: times are UTC.
//   - TC-32: a dropped call is never completed automatically.
package consult

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "consult", Schema: "consult", FS: sub}
}

// Session states.
const (
	StateScheduled      = "scheduled"
	StateOpen           = "open"
	StateBothConnected  = "both_connected"
	StateEnded          = "ended"
	StateFailedTech     = "failed_technical"
	StateNoShow         = "no_show"
	StateCancelled      = "cancelled"
	SystemActor         = "system:consult"
	rolePatient         = "patient"
	roleDoctor          = "doctor"
	initiatorPatient    = "patient"
	reasonRoomFallback  = "room_fallback"
	reasonConversion    = "doctor_conversion"
	reasonDeviceCheck   = "device"
	reasonNetworkChoice = "network"
)

func terminal(state string) bool {
	switch state {
	case StateEnded, StateFailedTech, StateNoShow, StateCancelled:
		return true
	}
	return false
}

// Config holds the windows and the consent text in force. Country values come from the pack
// through the caller (K-GLB-01).
type Config struct {
	JoinEarly          time.Duration // join opens this long before the slot start
	JoinLate           time.Duration // join stays open this long after the slot end
	DoctorJoinTimeout  time.Duration // doctor no-show after this (policy windows.doctor_join_timeout, 10 minutes)
	PatientNoShowGrace time.Duration // a patient no-show can be set this long after the start (E-BK-05)
	MinDuration        time.Duration // a completed consult under this is flagged (TC-27, 2 minutes)
	TokenTTL           time.Duration // join token lifetime (short)
	MaxDuration        time.Duration // room maximum duration

	ConsentTextID      string // TELE-CONSENT
	ConsentTextVersion int    // version in force (pack consent versions), 2 at 2026-11-16
	// DeclaredCountry is the pack's country code that "I am in India now" stands for (pack
	// online_consult_patient_countries). It is stored with the facts, never hard-coded.
	DeclaredCountry string
}

// DefaultConfig returns the documented windows. ConsentTextVersion and DeclaredCountry must
// still be set from the pack.
func DefaultConfig() Config {
	return Config{JoinEarly: 10 * time.Minute, JoinLate: 15 * time.Minute, DoctorJoinTimeout: 10 * time.Minute,
		PatientNoShowGrace: 10 * time.Minute, MinDuration: 2 * time.Minute, TokenTTL: 10 * time.Minute, MaxDuration: 2 * time.Hour,
		ConsentTextID: cp.ConsentTextTeleconsult}
}

func (c Config) validate() error {
	if c.JoinEarly <= 0 || c.JoinLate <= 0 || c.DoctorJoinTimeout <= 0 || c.PatientNoShowGrace <= 0 || c.MinDuration <= 0 || c.TokenTTL <= 0 || c.MaxDuration <= 0 {
		return errors.New("consult: windows are required")
	}
	if c.ConsentTextID == "" || c.ConsentTextVersion <= 0 || c.DeclaredCountry == "" {
		return errors.New("consult: consent text, its version and the declared country are required (country pack)")
	}
	return nil
}

// Verifier is satisfied by *verification.Module (K-CS-01).
type Verifier interface {
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// Deps are the other modules and adapters consult uses, all through public interfaces.
type Deps struct {
	Booking  bp.Service
	Video    video.Client
	Gate     rp.Gate
	Profiles ip.ProfileReader
	Verifier Verifier
}

// Module is the consult module.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	d    Deps
	log  *slog.Logger
	now  func() time.Time
}

var _ cp.FactsReader = (*Module)(nil)

// New builds the module. pool must use the consult role.
func New(pool *pgxpool.Pool, cfg Config, d Deps, log *slog.Logger) (*Module, error) {
	if pool == nil || log == nil {
		return nil, errors.New("consult: pool and logger are required")
	}
	if d.Booking == nil || d.Video == nil || d.Gate == nil || d.Profiles == nil || d.Verifier == nil {
		return nil, errors.New("consult: booking, video, records gate, profiles and verifier are required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Module{pool: pool, cfg: cfg, d: d, log: log, now: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

// Problems.
var (
	ErrClosed          = problem.New(http.StatusConflict, "consult.closed", "This consultation has ended")
	ErrOutsideWindow   = problem.New(http.StatusConflict, "consult.outside_window", "It is not time to join this consultation")
	ErrConsentRequired = problem.New(http.StatusForbidden, "consult.consent_required", "Confirm the consultation consent first")
	ErrConsentStale    = problem.New(http.StatusConflict, "consult.consent_stale", "The consent text has changed; read it again")
	// ErrOutsideCountry: the patient did not declare being in the pack country now (P10, TC-24).
	// The wording names no country (K-GLB-01); the app shows the pack's text.
	ErrOutsideCountry    = problem.New(http.StatusForbidden, "consult.patient_location_not_allowed", "Online consultations are only for patients who are in the service country now")
	ErrDeviceCheck       = problem.New(http.StatusUnprocessableEntity, "consult.device_check_failed", "The device check failed")
	ErrDoctorNotVerified = problem.New(http.StatusForbidden, "consult.doctor_not_verified", "This doctor cannot take consultations right now")
	ErrRoomUnavailable   = problem.New(http.StatusServiceUnavailable, "consult.room_unavailable", "The call room could not be set up; try again")
	ErrNotConnected      = problem.New(http.StatusConflict, "consult.not_connected", "Both sides must be connected first")
	ErrNotCompletable    = problem.New(http.StatusConflict, "consult.not_completable", "This consultation cannot be completed")
	ErrIdentityRequired  = problem.New(http.StatusConflict, "consult.identity_required", "Confirm the patient's name and age first")
	ErrIdentityNotSure   = problem.New(http.StatusUnprocessableEntity, "consult.identity_not_confirmed", "Name and age must both be confirmed")
	ErrIdentityMissing   = problem.New(http.StatusConflict, "consult.identity_unavailable", "The patient's name and age cannot be shown, so they cannot be confirmed")
	ErrModeChange        = problem.New(http.StatusConflict, "consult.mode_change_invalid", "That change of call mode is not allowed")
	ErrNotNoShow         = problem.New(http.StatusConflict, "consult.not_no_show", "A patient no-show needs the doctor present and the patient absent after the grace period")
	ErrUnavailable       = problem.New(http.StatusServiceUnavailable, "consult.unavailable", "Connection status cannot be checked right now")
	ErrWrongOutcome      = problem.New(http.StatusForbidden, "consult.outcome_not_allowed", "You cannot set that outcome")
	ErrNotTechnical      = problem.New(http.StatusConflict, "consult.not_technical_failure", "A technical failure can be recorded only by a side that tried to join, during the consultation window")
)

// Actor is who is acting, built by the HTTP layer from the principal.
type Actor struct {
	UserID    string
	Role      string // acting role: patient or doctor
	ClinicRef string
	RequestID string
}

// Session is a consult session row.
type Session struct {
	ID, AppointmentRef, PatientRef, DoctorRef, ClinicRef string
	BookedMode, CurrentMode, State, Outcome, RoomRef     string
	SlotStart, SlotEnd                                   time.Time
	OpenedAt, BothConnectedAt, EndedAt                   *time.Time
	IdentityConfirmed, ShortCall, MinorFlagged           bool
}

const sessionCols = `id, appointment_ref, patient_ref, doctor_ref, clinic_ref, booked_mode, current_mode, state, outcome, provider_room_ref,
	slot_start, slot_end, opened_at, both_connected_at, ended_at, identity_confirmed, short_call, minor_flagged`

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.AppointmentRef, &s.PatientRef, &s.DoctorRef, &s.ClinicRef, &s.BookedMode, &s.CurrentMode, &s.State, &s.Outcome, &s.RoomRef,
		&s.SlotStart, &s.SlotEnd, &s.OpenedAt, &s.BothConnectedAt, &s.EndedAt, &s.IdentityConfirmed, &s.ShortCall, &s.MinorFlagged)
	return s, err
}

// loadSession reads one session by ID (byAppointment false) or appointment ref (true).
func loadSession(ctx context.Context, q querier, ref string, byAppointment, lock bool) (Session, error) {
	col := "id"
	if byAppointment {
		col = "appointment_ref"
	}
	sql := `SELECT ` + sessionCols + ` FROM session WHERE ` + col + `=$1`
	if lock {
		sql += ` FOR UPDATE`
	}
	return scanSession(q.QueryRow(ctx, sql, ref))
}

func (m *Module) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, fn)
}

func (m *Module) emit(ctx context.Context, tx pgx.Tx, typ, subject string, payload map[string]string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: typ, SubjectRef: subject, Payload: payload, OccurredAt: m.now()})
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// access returns the session when the actor is its patient or its doctor; otherwise it is
// "not found" (K-SEC-04).
func (m *Module) access(ctx context.Context, a Actor, ref string, byAppointment bool) (Session, error) {
	s, err := loadSession(ctx, m.pool, ref, byAppointment, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, problem.NotFound
	}
	if err != nil {
		return Session{}, err
	}
	if !ownsSession(a, s) {
		return Session{}, problem.NotFound
	}
	return s, nil
}

func ownsSession(a Actor, s Session) bool {
	switch a.Role {
	case rolePatient:
		return a.UserID == s.PatientRef
	case roleDoctor:
		return a.UserID == s.DoctorRef
	}
	return false
}

func (m *Module) joinWindow(s Session) (opens, closes time.Time) {
	return s.SlotStart.Add(-m.cfg.JoinEarly), s.SlotEnd.Add(m.cfg.JoinLate)
}

func (m *Module) inWindow(s Session, now time.Time) bool {
	o, c := m.joinWindow(s)
	return !now.Before(o) && !now.After(c)
}

func (m *Module) auditEvent(ctx context.Context, tx pgx.Tx, a Actor, action string, s Session) error {
	return auditRecord(ctx, tx, a, action, s)
}
