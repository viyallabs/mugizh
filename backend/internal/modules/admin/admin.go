// Package admin is the admin module: just-in-time staff access (S-128, K-SEC-08) and the
// grievance queue with its legal timers (S-142). It never reads another module's schema:
// it creates staff grants through the records module's public interface, and the records
// module enforces the grant rules itself.
package admin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "admin", Schema: "admin", FS: sub}
}

// JITGranter creates staff grants (implemented by the records module).
type JITGranter interface {
	GrantJIT(ctx context.Context, r rp.JITRequest) (string, error)
}

// RoleChecker answers whether a user holds a role (implemented by the identity module).
type RoleChecker interface {
	HasRole(ctx context.Context, userID, role string) (bool, error)
}

// Config holds the timers and limits. Grievance timers come from the country pack
// (legal.grievance); nothing here is a literal.
type Config struct {
	JITMax time.Duration // longest staff grant (4 h)
	// LookupsPerHour caps one staff member's account lookups (S-172, E-DP-16). Default 30.
	LookupsPerHour      int
	ApprovalWindow      time.Duration // how long a request waits for approval
	JustificationWithin time.Duration // break-glass justification (24 h)

	AckWithin          time.Duration // acknowledge (24 h)
	ResolveWithin      time.Duration // complaints (7 days)
	ContentTakedown    time.Duration // unlawful content (36 h)
	OrderCompliance    time.Duration // government or court order (3 h)
	IntimateImage      time.Duration // intimate or morphed images (2 h)
	LawfulRequest      time.Duration // lawful requests (72 h)
	MedicalRecordCopy  time.Duration // copy of medical records (72 h)
	DataRightsResponse time.Duration // data rights (15 days)
	DataRightsAlertAt  time.Duration // alert for data rights (10 days)
	AlertFraction      float64       // alert for other items at this share of the window (0.8)
	// FeedbackPerDay caps one person's feedback items in any 24 hours (S-189). Default 10.
	FeedbackPerDay int
	// FeedbackRetention is how long feedback is kept from receipt. Default 1 year [CONFIRM].
	FeedbackRetention    time.Duration
	ProhibitedValidators []string
}

// Deps are the collaborators.
type Deps struct {
	Records JITGranter
	Roles   RoleChecker
	Crypto  *crypto.Service
	// Accounts finds an account by phone for the support lookup (S-172); nil means lookups are
	// unavailable (fail closed).
	Accounts ip.AccountLookup
	// Reverify starts a complaint-triggered re-verification (TM-31); nil refuses referrals.
	Reverify Reverifier
}

// Module is the admin module.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	d    Deps
	log  *slog.Logger
	now  func() time.Time
}

var _ pub.Service = (*Module)(nil)

// New builds the module. pool must use the admin role. Missing timers are start-up errors.
func New(pool *pgxpool.Pool, cfg Config, d Deps, log *slog.Logger) (*Module, error) {
	for _, v := range []time.Duration{cfg.JITMax, cfg.ApprovalWindow, cfg.JustificationWithin, cfg.AckWithin, cfg.ResolveWithin, cfg.ContentTakedown,
		cfg.OrderCompliance, cfg.IntimateImage, cfg.LawfulRequest, cfg.MedicalRecordCopy, cfg.DataRightsResponse, cfg.DataRightsAlertAt} {
		if v <= 0 {
			return nil, errors.New("admin: every access and grievance timer must be set (country pack)")
		}
	}
	if cfg.JITMax > 4*time.Hour {
		return nil, errors.New("admin: staff grants last at most 4 hours (access-control-design §5)")
	}
	if cfg.AlertFraction <= 0 || cfg.AlertFraction >= 1 || len(cfg.ProhibitedValidators) == 0 {
		return nil, errors.New("admin: alert fraction (between 0 and 1) and prohibited-identifier validators are required")
	}
	if d.Records == nil || d.Roles == nil || d.Crypto == nil {
		return nil, errors.New("admin: records, role checker and crypto are required")
	}
	if cfg.LookupsPerHour <= 0 {
		cfg.LookupsPerHour = 30
	}
	return &Module{pool: pool, cfg: cfg, d: d, log: log, now: time.Now}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

var (
	reUUID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reTicket = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,40}$`)
	reReason = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

var reasons = []string{pub.ReasonComplaint, pub.ReasonRightsRequest, pub.ReasonPaymentDispute, pub.ReasonSecurityIncident, pub.ReasonLegalOrder}

var staffRoles = []string{"support", "ops", "grievance_officer", "security_owner"}

var dataKinds = []rp.DataKind{rp.KindBooking, rp.KindIdentityBasic, rp.KindClinicalCurrent, rp.KindClinicalOwn, rp.KindClinicalHistory, rp.KindDocuments, rp.KindPayment}

// holds verifies that the signed-in staff member really holds the role they act in.
func (m *Module) holds(ctx context.Context, s pub.Staff, roles ...string) error {
	if !reUUID.MatchString(s.Ref) || !slices.Contains(roles, s.Role) {
		return pub.ErrForbidden
	}
	ok, err := m.d.Roles.HasRole(ctx, s.Ref, s.Role)
	if err != nil {
		return err
	}
	if !ok {
		return pub.ErrForbidden
	}
	return nil
}

func (m *Module) auditStaff(ctx context.Context, tx pgx.Tx, s pub.Staff, action, patient, resType, resRef, reason, ticket, grant string, res ap.Result) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: s.Ref, ActorRole: s.Role, Action: action, SubjectRef: patient, ResourceType: resType, ResourceRef: resRef,
		ReasonCode: reason, TicketRef: ticket, GrantID: grant, Result: res, RequestID: s.RequestID})
}

// RequestAccess records a staff member's request for access to one patient's data
// (access-control-design §5): a reason from the closed list, a ticket, the smallest
// data kinds, at most four hours. Nothing is granted until a security owner approves.
func (m *Module) RequestAccess(ctx context.Context, s pub.Staff, in pub.AccessRequestInput) (pub.AccessRequest, error) {
	if err := m.holds(ctx, s, staffRoles...); err != nil {
		return pub.AccessRequest{}, err
	}
	if err := m.validateAccess(in); err != nil {
		return pub.AccessRequest{}, err
	}
	now := m.now().UTC()
	id := ids.New()
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO access_request (id, staff_ref, staff_role, patient_ref, kinds, reason_code, ticket_ref, duration_seconds, status, requested_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9)`, id, s.Ref, s.Role, in.PatientRef, kindStrings(in.Kinds), in.ReasonCode, in.TicketRef, int(in.Duration.Seconds()), now); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.access_requested", in.PatientRef, "access_request", id, in.ReasonCode, in.TicketRef, "", ap.Allowed)
	})
	if err != nil {
		return pub.AccessRequest{}, err
	}
	return m.GetAccessRequest(ctx, id)
}

func (m *Module) validateAccess(in pub.AccessRequestInput) error {
	if !reUUID.MatchString(in.PatientRef) || !slices.Contains(reasons, in.ReasonCode) || !reTicket.MatchString(in.TicketRef) || len(in.Kinds) == 0 {
		return pub.ErrInvalid
	}
	if in.Duration <= 0 || in.Duration > m.cfg.JITMax {
		return fmt.Errorf("%w: duration", pub.ErrInvalid)
	}
	for _, k := range in.Kinds {
		if !slices.Contains(dataKinds, k) {
			return fmt.Errorf("%w: data kind", pub.ErrInvalid)
		}
	}
	return nil
}

func kindStrings(k []rp.DataKind) []string {
	out := make([]string, 0, len(k))
	for _, x := range k {
		out = append(out, string(x))
	}
	return out
}

const accessCols = `id::text, staff_ref::text, staff_role, patient_ref::text, kinds, reason_code, ticket_ref, duration_seconds, break_glass, status,
	requested_at, decided_at, coalesce(approver_ref::text,''), coalesce(grant_id::text,''), grant_expires_at, justification_due_at, justified_at`

func scanAccess(s interface{ Scan(...any) error }) (pub.AccessRequest, error) {
	var r pub.AccessRequest
	var kinds []string
	var secs int
	if err := s.Scan(&r.ID, &r.StaffRef, &r.StaffRole, &r.PatientRef, &kinds, &r.ReasonCode, &r.TicketRef, &secs, &r.BreakGlass, &r.Status,
		&r.RequestedAt, &r.DecidedAt, &r.ApproverRef, &r.GrantID, &r.GrantExpiresAt, &r.JustificationDueAt, &r.JustifiedAt); err != nil {
		return r, err
	}
	for _, k := range kinds {
		r.Kinds = append(r.Kinds, rp.DataKind(k))
	}
	r.Duration = time.Duration(secs) * time.Second
	return r, nil
}

// GetAccessRequest returns one request.
func (m *Module) GetAccessRequest(ctx context.Context, id string) (pub.AccessRequest, error) {
	if !reUUID.MatchString(id) {
		return pub.AccessRequest{}, pub.ErrNotFound
	}
	r, err := scanAccess(m.pool.QueryRow(ctx, `SELECT `+accessCols+` FROM access_request WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, pub.ErrNotFound
	}
	return r, err
}

// ApproveAccess is the second person's decision: a security owner who is not the
// requester. It creates the grant through the records module, which enforces the rules
// again (roles, reason, scope, duration). Break-glass requests are not approved here.
func (m *Module) ApproveAccess(ctx context.Context, approver pub.Staff, id string) (pub.AccessRequest, error) {
	if err := m.holds(ctx, approver, "security_owner"); err != nil {
		return pub.AccessRequest{}, err
	}
	r, err := m.GetAccessRequest(ctx, id)
	if err != nil {
		return r, err
	}
	if r.StaffRef == approver.Ref {
		return r, pub.ErrSelfApproval
	}
	if r.Status != "pending" || r.BreakGlass {
		return r, pub.ErrNotPending
	}
	now := m.now().UTC()
	if !now.Before(r.RequestedAt.Add(m.cfg.ApprovalWindow)) {
		_, _ = m.pool.Exec(ctx, `UPDATE access_request SET status='expired', decided_at=$2, decision_reason='approval_window_passed' WHERE id=$1 AND status='pending'`, id, now)
		return r, pub.ErrExpired
	}
	// Claim the request first (review H2): only one caller moves it to 'approving', so only one
	// grant can ever be created for it.
	tag, err := m.pool.Exec(ctx, `UPDATE access_request SET status='approving' WHERE id=$1 AND status='pending' AND NOT break_glass`, id)
	if err != nil {
		return r, err
	}
	if tag.RowsAffected() == 0 {
		return r, pub.ErrNotPending
	}
	gid, err := m.d.Records.GrantJIT(ctx, rp.JITRequest{StaffRef: r.StaffRef, StaffRole: r.StaffRole, PatientRef: r.PatientRef, Kinds: r.Kinds,
		ReasonCode: r.ReasonCode, TicketRef: r.TicketRef, ApproverRef: approver.Ref, Duration: r.Duration})
	if err != nil {
		// No grant exists: put the request back so it can be decided again.
		if _, rerr := m.pool.Exec(context.WithoutCancel(ctx), `UPDATE access_request SET status='pending' WHERE id=$1 AND status='approving'`, id); rerr != nil {
			m.log.Error("admin.approval_release_failed")
		}
		return r, err
	}
	exp := now.Add(r.Duration)
	err = db.InTx(context.WithoutCancel(ctx), m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE access_request SET status='approved', decided_at=$2, approver_ref=$3, grant_id=$4, grant_expires_at=$5 WHERE id=$1 AND status='approving'`,
			id, now, approver.Ref, gid, exp)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pub.ErrNotPending
		}
		if err := m.auditStaff(ctx, tx, approver, "admin.access_approved", r.PatientRef, "access_request", id, r.ReasonCode, r.TicketRef, gid, ap.Allowed); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "access.granted", SubjectRef: r.PatientRef, Payload: map[string]string{"requestRef": id, "grantRef": gid}})
	})
	if err != nil {
		// The grant exists but its approval record failed. Staff access without an approval
		// record must not stay live (K-SEC-08): revoke the grant at once. The request stays
		// 'approving' (not decidable again) for the security owner to resolve.
		m.log.Error("admin.approval_finalize_failed")
		if rv, ok := m.d.Records.(grantRevoker); ok {
			if rerr := rv.Revoke(context.WithoutCancel(ctx), gid, approver.Ref, false, ""); rerr != nil {
				m.log.Error("admin.orphan_grant_revoke_failed")
			}
		} else {
			m.log.Error("admin.orphan_grant_not_revocable")
		}
		return r, err
	}
	return m.GetAccessRequest(ctx, id)
}

// RejectAccess declines a pending request. reason is a code, never free text.
func (m *Module) RejectAccess(ctx context.Context, approver pub.Staff, id, reason string) error {
	if err := m.holds(ctx, approver, "security_owner"); err != nil {
		return err
	}
	if !reReason.MatchString(reason) || scrub.ContainsProhibitedID(reason, m.cfg.ProhibitedValidators) {
		return pub.ErrInvalid
	}
	r, err := m.GetAccessRequest(ctx, id)
	if err != nil {
		return err
	}
	if r.StaffRef == approver.Ref {
		return pub.ErrSelfApproval
	}
	if r.Status != "pending" {
		return pub.ErrNotPending
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE access_request SET status='rejected', decided_at=$2, approver_ref=$3, decision_reason=$4 WHERE id=$1 AND status='pending'`,
			id, m.now().UTC(), approver.Ref, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pub.ErrNotPending
		}
		return m.auditStaff(ctx, tx, approver, "admin.access_rejected", r.PatientRef, "access_request", id, r.ReasonCode, r.TicketRef, "", ap.Allowed)
	})
}

// BreakGlass gives the security owner immediate access during a live security incident.
// No approver is needed, the records module raises the alert, an event goes to alerting,
// and a written justification is due within 24 hours (access-control-design §5).
func (m *Module) BreakGlass(ctx context.Context, s pub.Staff, in pub.AccessRequestInput) (pub.AccessRequest, error) {
	if err := m.holds(ctx, s, "security_owner"); err != nil {
		return pub.AccessRequest{}, err
	}
	in.ReasonCode = pub.ReasonSecurityIncident
	if err := m.validateAccess(in); err != nil {
		return pub.AccessRequest{}, err
	}
	gid, err := m.d.Records.GrantJIT(ctx, rp.JITRequest{StaffRef: s.Ref, StaffRole: s.Role, PatientRef: in.PatientRef, Kinds: in.Kinds,
		ReasonCode: in.ReasonCode, TicketRef: in.TicketRef, Duration: in.Duration, BreakGlass: true})
	if err != nil {
		return pub.AccessRequest{}, err
	}
	now := m.now().UTC()
	id := ids.New()
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO access_request (id, staff_ref, staff_role, patient_ref, kinds, reason_code, ticket_ref, duration_seconds, break_glass, status, requested_at,
			decided_at, grant_id, grant_expires_at, justification_due_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,'approved',$9,$9,$10,$11,$12)`,
			id, s.Ref, s.Role, in.PatientRef, kindStrings(in.Kinds), in.ReasonCode, in.TicketRef, int(in.Duration.Seconds()), now, gid, now.Add(in.Duration), now.Add(m.cfg.JustificationWithin)); err != nil {
			return err
		}
		if err := m.auditStaff(ctx, tx, s, "admin.break_glass", in.PatientRef, "access_request", id, in.ReasonCode, in.TicketRef, gid, ap.Allowed); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "access.break_glass", SubjectRef: in.PatientRef, Payload: map[string]string{"requestRef": id, "grantRef": gid}})
	})
	if err != nil {
		return pub.AccessRequest{}, err
	}
	return m.GetAccessRequest(ctx, id)
}

// Justify records the written justification for a break-glass access (sealed).
func (m *Module) Justify(ctx context.Context, s pub.Staff, id, text string) error {
	if err := m.holds(ctx, s, "security_owner"); err != nil {
		return err
	}
	if !reUUID.MatchString(id) {
		return pub.ErrNotFound
	}
	text, err := m.checkText(text)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealService(ctx, m.d.Crypto, "access_justification", crypto.AAD{Table: "access_request", Column: "justification", RowID: id}, []byte(text))
	if err != nil {
		return err
	}
	tag, err := m.pool.Exec(ctx, `UPDATE access_request SET justification_sealed=$3, justified_at=$4 WHERE id=$1 AND staff_ref=$2 AND break_glass AND justified_at IS NULL`,
		id, s.Ref, sealed, m.now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pub.ErrNotFound
	}
	return nil
}

// MonthlyReview lists every staff access request of a month with its reason, for the
// security owner's monthly review, and records that the review was done (evidence for
// the audit, S-051). month is any instant inside the month (UTC).
func (m *Module) MonthlyReview(ctx context.Context, s pub.Staff, month time.Time) ([]pub.ReviewRow, error) {
	if err := m.holds(ctx, s, "security_owner"); err != nil {
		return nil, err
	}
	from := time.Date(month.UTC().Year(), month.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	rows, err := m.pool.Query(ctx, `SELECT id::text, requested_at, staff_ref::text, staff_role, patient_ref::text, kinds, reason_code, ticket_ref,
		coalesce(approver_ref::text,''), break_glass, status, coalesce(grant_id::text,''), justified_at IS NOT NULL
		FROM access_request WHERE requested_at >= $1 AND requested_at < $2 ORDER BY requested_at, id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pub.ReviewRow{}
	for rows.Next() {
		var r pub.ReviewRow
		if err := rows.Scan(&r.RequestID, &r.RequestedAt, &r.StaffRef, &r.StaffRole, &r.PatientRef, &r.Kinds, &r.ReasonCode, &r.TicketRef, &r.ApproverRef,
			&r.BreakGlass, &r.Status, &r.GrantID, &r.Justified); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO access_review (month, reviewer_ref, reviewed_at, row_count) VALUES ($1,$2,$3,$4)
			ON CONFLICT (month) DO NOTHING`, from, s.Ref, m.now().UTC(), len(out)); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.access_review", "", "access_review", from.Format("2006-01"), "", "", "", ap.Allowed)
	})
	return out, err
}

// grantRevoker is implemented by the records module (Revoke). It is optional on JITGranter
// so tests can use a minimal fake; the composition root passes the real records module.
type grantRevoker interface {
	Revoke(ctx context.Context, grantID, byRef string, byPatient bool, requestID string) error
}
