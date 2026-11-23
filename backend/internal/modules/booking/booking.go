// Package booking is the booking module: availability, slots, holds, appointments and
// reconfirmation (S-113, S-121, S-122, S-123, S-131, S-144; module-boundaries section 2).
//
// Rules built in:
//   - K-CS-01: only listed practices are booked; checked at hold, at checkout and again
//     when the payment is confirmed.
//   - K-PAY-02: an appointment is confirmed only from a verified payment.captured event
//     (or at once for a pay-at-clinic in-person booking, where no money is handled).
//     The slot take is one UPDATE ... WHERE state = 'open' (TC-22).
//   - K-PAY-05: the refund policy version and the fee (with its version) are copied onto the
//     appointment and never change afterwards.
//   - K-ARC-04: times are stored in UTC with the practice and patient IANA zones.
//   - K-DP-02: staff reads of booking fields go through records.Authorize (KindBooking).
//   - P10: video and audio need patientInIndiaConfirmed; in-person bookings do not.
package booking

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"
	_ "time/tzdata" // IANA zone data, independent of the host image

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "booking", Schema: "booking", FS: sub}
}

// Consult modes and states.
const (
	ModeInPerson = "in_person"
	ModeVideo    = "video"
	ModeAudio    = "audio"

	StateHeld           = "held"
	StatePendingPayment = "pending_payment"
	StateConfirmed      = "confirmed"
	StateInProgress     = "in_progress"
	StateCompleted      = "completed"
	StateCancelled      = "cancelled"
	StateNoShow         = "no_show"
	StateExpired        = "expired"

	// ConsentCare is the purpose a patient must have granted to hold or book.
	ConsentCare = "P-CARE"

	// SystemActor is the actor reference for system-driven changes.
	SystemActor = "system:booking"
)

var validModes = map[string]bool{ModeInPerson: true, ModeVideo: true, ModeAudio: true}

// Config holds values from the country pack and the refund policy. Nothing here is a code
// constant: the composition root reads them from config/country-packs.
type Config struct {
	// RefundPolicyVersion is stored with every appointment (K-PAY-05).
	RefundPolicyVersion string
	// HoldTTL is how long a hold lives (policy windows.pending_payment_timeout_minutes).
	HoldTTL time.Duration
	// RescheduleMinMinutes: a patient reschedule needs at least this much notice
	// (policy windows.free_cancel_before_minutes). Not applied when the clinic caused it.
	RescheduleMinMinutes int
	// FreeReschedules is the number of patient reschedules per appointment (policy windows.free_reschedules).
	FreeReschedules int
	// PatientGraceMinutes: a patient no-show can be marked this long after the start (E-BK-05).
	PatientGraceMinutes int
	// DoctorJoinTimeoutMinutes: a doctor no-show can be marked this long after the start (E-BK-04).
	DoctorJoinTimeoutMinutes int
	// MaxActiveHolds per patient (E-SEC-04). Default 2.
	MaxActiveHolds int
	// MaxPayAtClinicUpcoming caps a patient's upcoming pay-at-clinic bookings (E-SEC-04). Default 3.
	MaxPayAtClinicUpcoming int
	// HorizonDays slots are materialised ahead. Default 30.
	HorizonDays int
	// ReconfirmLeadHours before the start the clinic must reconfirm (E-BK-07). Default 24.
	ReconfirmLeadHours int
	// OfferNextSlots is how many open slots are offered when a slot is taken. Default 3.
	OfferNextSlots int
	// Appointment messages (S-157): the day-before reminder (default 24 h), the short reminder
	// (default 2 h) and, for online consults, the "starting" notice (default 10 min, when join opens).
	ReminderDayBefore, ReminderSoon, ConsultStartNotice time.Duration
}

// RefundPreviewer tells the patient the refund outcome before they confirm a cancellation
// (S-144). Payments implements it; the composition root wires it. byWhom is patient, doctor,
// clinic or system; mode is the booking mode (video, audio or in_person).
type RefundPreviewer interface {
	PreviewRefund(ctx context.Context, appointmentRef, policyVersion, byWhom, mode string, minutesBeforeSlot int) (amountMinor int64, rule string, err error)
}

// Verifier is satisfied by *verification.Module.
type Verifier interface {
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// Deps are the other modules booking depends on, all through their public interfaces.
type Deps struct {
	Directory dp.Reader
	Payments  pp.Service
	Gate      rp.Gate
	Refunds   RefundPreviewer
	// Verifier answers "is this professional verified right now" from the source of truth
	// (review H3): the directory read model is event-fed and can lag a lapsed licence.
	Verifier Verifier
	// Branches is optional. It lets a closure check that a branch belongs to the clinic
	// (S-145). When nil, Directory is used if it also implements dp.BranchReader (the
	// directory module does).
	Branches dp.BranchReader
}

// Module implements public.Service and the booking operations.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	d    Deps
	log  *slog.Logger
	now  func() time.Time
}

var _ bp.Service = (*Module)(nil)

// New builds the module. pool must use the booking role.
func New(pool *pgxpool.Pool, cfg Config, d Deps, log *slog.Logger) (*Module, error) {
	switch {
	case pool == nil || log == nil:
		return nil, errors.New("booking: pool and logger are required")
	case d.Directory == nil || d.Payments == nil || d.Gate == nil || d.Refunds == nil || d.Verifier == nil:
		return nil, errors.New("booking: directory, payments, records gate, refund previewer and verifier are required")
	case cfg.RefundPolicyVersion == "":
		return nil, errors.New("booking: refund policy version is required (K-PAY-05)")
	case cfg.HoldTTL <= 0 || cfg.RescheduleMinMinutes <= 0 || cfg.PatientGraceMinutes <= 0 || cfg.DoctorJoinTimeoutMinutes <= 0 || cfg.FreeReschedules < 0:
		return nil, errors.New("booking: policy windows are required")
	}
	if cfg.MaxActiveHolds <= 0 {
		cfg.MaxActiveHolds = 2
	}
	if cfg.MaxPayAtClinicUpcoming <= 0 {
		cfg.MaxPayAtClinicUpcoming = 3
	}
	if cfg.HorizonDays <= 0 {
		cfg.HorizonDays = 30
	}
	if cfg.ReconfirmLeadHours <= 0 {
		cfg.ReconfirmLeadHours = 24
	}
	if cfg.ReminderDayBefore <= 0 {
		cfg.ReminderDayBefore = 24 * time.Hour
	}
	if cfg.ReminderSoon <= 0 {
		cfg.ReminderSoon = 2 * time.Hour
	}
	if cfg.ConsultStartNotice <= 0 {
		cfg.ConsultStartNotice = 10 * time.Minute
	}
	if cfg.ReminderSoon >= cfg.ReminderDayBefore || cfg.ConsultStartNotice >= cfg.ReminderSoon {
		return nil, errors.New("booking: reminder offsets must be day-before > soon > consult start")
	}
	if cfg.OfferNextSlots <= 0 {
		cfg.OfferNextSlots = 3
	}
	return &Module{pool: pool, cfg: cfg, d: d, log: log, now: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

// Actor is who is acting, built by the HTTP layer from the principal.
type Actor struct {
	UserID    string
	Role      string // acting role: patient, doctor, clinic_admin, front_desk
	ClinicRef string
	RequestID string
	// Restricted is principal.Restricted (a session on a new, unverified device).
	Restricted bool
}

func (m *Module) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, fn)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (m *Module) audit(ctx context.Context, tx pgx.Tx, actorRef, role, action, subject, apptRef, requestID string) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: actorRef, ActorRole: role, Action: action, SubjectRef: subject,
		ResourceType: "appointment", ResourceRef: apptRef, Result: ap.Allowed, RequestID: requestID})
}

func (m *Module) emit(ctx context.Context, tx pgx.Tx, typ, subject string, payload map[string]string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: typ, SubjectRef: subject, Payload: payload, OccurredAt: m.now()})
}

// refundMode maps a consult mode to the refund policy's mode.
func refundMode(consultMode string) string {
	if consultMode == ModeInPerson {
		return bp.RefundModeInPerson
	}
	return bp.RefundModeOnline
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func minutesBefore(now, start time.Time) int {
	d := start.Sub(now)
	if d < 0 {
		return 0
	}
	return int(d / time.Minute)
}

func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("booking: invalid time zone")
	}
	return time.LoadLocation(name)
}

// stillVerified checks the professional against the verification module directly (K-CS-01,
// review H3). Any error is an error: callers refuse or retry, never proceed.
func (m *Module) stillVerified(ctx context.Context, professionalRef string) (bool, error) {
	return m.d.Verifier.IsVerified(ctx, professionalRef)
}
