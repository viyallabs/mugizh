package records

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// ShareConfig holds the sharing timers. Every value is required and comes from configuration
// (consent-design §2, consent-texts B); nothing is hard-coded here.
type ShareConfig struct {
	// DefaultAfterAppointment: a T3 share lasts this long after the appointment ends (7 days).
	DefaultAfterAppointment time.Duration
	// ExtensionMax: the longest extra time one approval can give (consent-texts B shows 24 hours [CONFIRM]).
	ExtensionMax time.Duration
	// WarnBefore: a share counts as "ending soon", and may be extended, this long before it ends
	// (E-DP-02: 10 minutes).
	WarnBefore time.Duration
	// DeclineCooldown: after the patient declines, the same doctor may not ask again for this long
	// (review F4: no stream of requests and pushes; default 24 h [CONFIRM]).
	DeclineCooldown time.Duration
	// RequestTTL: how long an unanswered extension request stays open.
	RequestTTL time.Duration
}

// Sharing implements S-168: the patient's history share (T3) and the extension flow (T4).
// All access to the shared history is still decided by Module.Authorize.
type Sharing struct {
	m     *Module
	cfg   ShareConfig
	appts rp.AppointmentReader
	ver   rp.DoctorVerifier
}

// NewSharing builds the sharing service. Missing timers or collaborators are start-up errors.
func NewSharing(m *Module, cfg ShareConfig, appts rp.AppointmentReader, ver rp.DoctorVerifier) (*Sharing, error) {
	if m == nil || appts == nil || ver == nil {
		return nil, errors.New("records: the module, an appointment reader and a doctor verifier are required for sharing")
	}
	if cfg.DefaultAfterAppointment <= 0 || cfg.ExtensionMax <= 0 || cfg.WarnBefore <= 0 || cfg.RequestTTL <= 0 {
		return nil, errors.New("records: every sharing timer must be set")
	}
	if cfg.DeclineCooldown <= 0 {
		cfg.DeclineCooldown = 24 * time.Hour
	}
	return &Sharing{m: m, cfg: cfg, appts: appts, ver: ver}, nil
}

// verified is the K-CS-01 gate on the server. Any error means not verified (fail closed).
func (s *Sharing) verified(ctx context.Context, doctorRef string) error {
	ok, err := s.ver.IsVerified(ctx, doctorRef)
	if err != nil || !ok {
		return rp.ErrDoctorNotVerified
	}
	return nil
}

// Share records the patient's tap on the P-SHARE screen: the consent for the text version in
// force and a T3 grant to one verified doctor for one confirmed appointment. It ends at the
// default (appointment end plus the configured time) or sooner, as the patient chooses.
// Repeating the tap while that share is active returns the same share.
func (s *Sharing) Share(ctx context.Context, patientRef string, in rp.ShareInput) (rp.ShareResult, error) {
	if !allUUID(patientRef, in.DoctorRef, in.AppointmentRef) || patientRef == in.DoctorRef {
		return rp.ShareResult{}, rp.ErrInvalid
	}
	if err := s.verified(ctx, in.DoctorRef); err != nil {
		return rp.ShareResult{}, err
	}
	facts, err := s.appts.ConfirmedAppointment(ctx, patientRef, in.DoctorRef, in.AppointmentRef)
	if errors.Is(err, rp.ErrNoAppointment) {
		return rp.ShareResult{}, rp.ErrNotFound // somebody else's appointment looks missing (K-SEC-04)
	}
	if err != nil {
		return rp.ShareResult{}, fmt.Errorf("records: appointment lookup: %w", err)
	}
	if facts.ClinicRef != "" && !reUUID.MatchString(facts.ClinicRef) {
		return rp.ShareResult{}, rp.ErrInvalid
	}
	now := s.m.now().UTC()
	latest := facts.EndsAt.UTC().Add(s.cfg.DefaultAfterAppointment)
	until := latest
	if in.Until != nil {
		until = in.Until.UTC()
		if until.After(latest) {
			return rp.ShareResult{}, fmt.Errorf("%w: a share cannot end later than the default", rp.ErrInvalid)
		}
	}
	if !until.After(now) {
		return rp.ShareResult{}, fmt.Errorf("%w: the share would already be over", rp.ErrInvalid)
	}
	notice := in.NoticeVersion
	if notice == "" {
		notice = s.m.cfg.NoticeVersion
	}
	if notice == "" {
		return rp.ShareResult{}, fmt.Errorf("%w: notice version", rp.ErrInvalid)
	}
	// Review F10: one share per doctor and appointment even on a double tap; a repeat tap returns
	// the active share (checked and created atomically in shareT3Once, under a lock scoped to its
	// own transaction, so no connection is held while waiting for another).
	id, exp, err := s.m.shareT3Once(ctx, patientRef, in.DoctorRef, facts.ClinicRef, in.AppointmentRef, until, in.TextVersion, notice, in.RequestID, now)
	if err != nil {
		return rp.ShareResult{}, err
	}
	return rp.ShareResult{GrantID: id, DoctorRef: in.DoctorRef, ExpiresAt: exp}, nil
}

// HistoryAccessUntil returns the latest end of the doctor's active history grants (T3, T4)
// for the patient. ok is false when there is none. It reads the same cached, synchronously
// invalidated view the gate uses.
func (m *Module) HistoryAccessUntil(ctx context.Context, patientRef, doctorRef string) (time.Time, bool, error) {
	if !allUUID(patientRef, doctorRef) {
		return time.Time{}, false, rp.ErrInvalid
	}
	snap, err := m.snapshot(ctx, patientRef)
	if err != nil {
		return time.Time{}, false, err
	}
	now := m.now()
	var best time.Time
	ok := false
	for _, g := range snap.grants {
		if g.GranteeRef != doctorRef || (g.Tier != rp.T3HistoryShare && g.Tier != rp.T4Extension) || g.RevokedAt != nil ||
			now.Before(g.StartsAt) || g.ExpiresAt == nil || !now.Before(*g.ExpiresAt) {
			continue
		}
		if !ok || g.ExpiresAt.After(best) {
			best, ok = *g.ExpiresAt, true
		}
	}
	return best, ok, nil
}

var _ rp.ShareWindowReader = (*Module)(nil)

// ---- extension requests (E-DP-02, T4) ----

// RequestExtension records a doctor's request for more time on a history share. It gives no
// access: only the patient's approval does. It is allowed only for a doctor who holds a share
// the patient has not stopped, and only when that share is ending within the warning time or
// has ended. A doctor with no relationship with the patient gets "not found" (K-SEC-04).
func (s *Sharing) RequestExtension(ctx context.Context, doctorRef, patientRef, requestID string) (rp.ExtensionRequest, error) {
	if !allUUID(doctorRef, patientRef) || doctorRef == patientRef {
		return rp.ExtensionRequest{}, rp.ErrInvalid
	}
	if err := s.verified(ctx, doctorRef); err != nil {
		return rp.ExtensionRequest{}, err
	}
	snap, err := s.m.snapshot(ctx, patientRef)
	if err != nil {
		return rp.ExtensionRequest{}, err
	}
	related := false
	var newest *rp.Grant // the doctor's most recent share (T3 or T4)
	var lastEnd time.Time
	clinic := ""
	for i := range snap.grants {
		g := snap.grants[i]
		if g.GranteeRef != doctorRef {
			continue
		}
		switch g.Tier {
		case rp.T1CurrentCare, rp.T2OwnRecords:
			related = true
		case rp.T3HistoryShare, rp.T4Extension:
			related = true
			if newest == nil || g.StartsAt.After(newest.StartsAt) {
				newest = &snap.grants[i]
			}
			if g.RevokedAt == nil && g.ExpiresAt != nil && g.ExpiresAt.After(lastEnd) {
				lastEnd = *g.ExpiresAt
			}
			if g.ClinicRef != "" {
				clinic = g.ClinicRef
			}
		}
	}
	if !related || snap.erasing {
		return rp.ExtensionRequest{}, rp.ErrNotFound
	}
	now := s.m.now().UTC()
	// Nothing to extend, the patient stopped the latest share, or it is not close to ending.
	if newest == nil || newest.RevokedAt != nil || lastEnd.IsZero() || lastEnd.After(now.Add(s.cfg.WarnBefore)) {
		return rp.ExtensionRequest{}, rp.ErrExtensionNotAllowed
	}

	var out rp.ExtensionRequest
	err = db.InTx(ctx, s.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		out = rp.ExtensionRequest{}
		if _, err := tx.Exec(ctx, `UPDATE share_request SET status='expired', decided_at=$3 WHERE patient_ref=$1 AND doctor_ref=$2 AND status='pending' AND expires_at <= $3`,
			patientRef, doctorRef, now); err != nil {
			return err
		}
		var recentDecline bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM share_request WHERE patient_ref=$1 AND doctor_ref=$2 AND status='declined' AND decided_at > $3)`,
			patientRef, doctorRef, now.Add(-s.cfg.DeclineCooldown)).Scan(&recentDecline); err != nil {
			return err
		}
		if recentDecline {
			return rp.ErrExtensionNotAllowed // review F4: the patient said no recently
		}
		id := ids.New()
		exp := now.Add(s.cfg.RequestTTL)
		var cl any
		if clinic != "" {
			cl = clinic
		}
		tag, err := tx.Exec(ctx, `INSERT INTO share_request (id, patient_ref, doctor_ref, clinic_ref, status, requested_at, expires_at)
			VALUES ($1,$2,$3,$4,'pending',$5,$6) ON CONFLICT (patient_ref, doctor_ref) WHERE status = 'pending' DO NOTHING`, id, patientRef, doctorRef, cl, now, exp)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 { // an open request exists: a repeat tap returns it
			r, err := scanExtension(tx.QueryRow(ctx, requestSQL+` WHERE patient_ref=$1 AND doctor_ref=$2 AND status='pending'`, patientRef, doctorRef))
			out = s.withMax(r)
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: doctorRef, ActorRole: "doctor", Action: "share.extension_requested", SubjectRef: patientRef,
			ResourceType: "share_request", ResourceRef: id, Purpose: "P-SHARE", Result: ap.Allowed, RequestID: requestID}); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: "share.extension_requested", SubjectRef: patientRef,
			Payload: map[string]string{"requestId": id, "doctorId": doctorRef}}); err != nil {
			return err
		}
		out = s.withMax(rp.ExtensionRequest{ID: id, PatientRef: patientRef, DoctorRef: doctorRef, ClinicRef: clinic, Status: "pending", RequestedAt: now, ExpiresAt: exp})
		return nil
	})
	return out, err
}

const requestSQL = `SELECT id::text, patient_ref::text, doctor_ref::text, coalesce(clinic_ref::text,''), status, requested_at, expires_at FROM share_request`

func scanExtension(row pgx.Row) (rp.ExtensionRequest, error) {
	var r rp.ExtensionRequest
	err := row.Scan(&r.ID, &r.PatientRef, &r.DoctorRef, &r.ClinicRef, &r.Status, &r.RequestedAt, &r.ExpiresAt)
	return r, err
}

func (s *Sharing) withMax(r rp.ExtensionRequest) rp.ExtensionRequest {
	r.ExtensionMax = s.cfg.ExtensionMax
	return r
}

// PendingRequests lists the patient's open extension requests, oldest first. Requests that
// lapsed are not shown.
func (s *Sharing) PendingRequests(ctx context.Context, patientRef string) ([]rp.ExtensionRequest, error) {
	if !reUUID.MatchString(patientRef) {
		return nil, rp.ErrInvalid
	}
	rows, err := s.m.pool.Query(ctx, requestSQL+` WHERE patient_ref=$1 AND status='pending' AND expires_at > $2 ORDER BY requested_at, id`, patientRef, s.m.now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rp.ExtensionRequest{}
	for rows.Next() {
		r, err := scanExtension(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s.withMax(r))
	}
	return out, rows.Err()
}

// Approve is the patient's "Allow". It records the P-SHARE consent for the text version in
// force and creates the T4 grant, in one decision. Someone else's request looks missing.
func (s *Sharing) Approve(ctx context.Context, patientRef, requestRef string, in rp.ApproveInput) (rp.ShareResult, error) {
	if !allUUID(patientRef, requestRef) {
		return rp.ShareResult{}, rp.ErrNotFound
	}
	dur := in.For
	if dur == 0 {
		dur = s.cfg.ExtensionMax
	}
	if dur < 0 || dur > s.cfg.ExtensionMax {
		return rp.ShareResult{}, fmt.Errorf("%w: extension length", rp.ErrInvalid)
	}
	notice := in.NoticeVersion
	if notice == "" {
		notice = s.m.cfg.NoticeVersion
	}
	if notice == "" {
		return rp.ShareResult{}, fmt.Errorf("%w: notice version", rp.ErrInvalid)
	}
	req, err := scanExtension(s.m.pool.QueryRow(ctx, requestSQL+` WHERE id=$1 AND patient_ref=$2`, requestRef, patientRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return rp.ShareResult{}, rp.ErrNotFound
	}
	if err != nil {
		return rp.ShareResult{}, err
	}
	now := s.m.now().UTC()
	if req.Status != "pending" || !req.ExpiresAt.After(now) {
		return rp.ShareResult{}, rp.ErrRequestClosed
	}
	if err := s.verified(ctx, req.DoctorRef); err != nil {
		return rp.ShareResult{}, err
	}
	grantID := ids.New()
	until := now.Add(dur)
	err = db.InTx(ctx, s.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		var exp time.Time
		if err := tx.QueryRow(ctx, `SELECT status, expires_at FROM share_request WHERE id=$1 AND patient_ref=$2 FOR UPDATE`, requestRef, patientRef).Scan(&status, &exp); err != nil {
			return err
		}
		if status != "pending" || !exp.After(now) {
			return rp.ErrRequestClosed
		}
		// Review F11: the consent for the text shown is recorded in the same transaction as the
		// grant, so a request that lapsed or was declined meanwhile records neither.
		if err := s.m.GrantConsentTx(ctx, tx, rp.ConsentChange{SubjectRef: patientRef, PurposeID: "P-SHARE", TextVersion: in.TextVersion,
			NoticeVersion: notice, Channel: "app", RequestID: in.RequestID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE share_request SET status='approved', decided_at=$2, grant_id=$3 WHERE id=$1`, requestRef, now, grantID); err != nil {
			return err
		}
		g := rp.Grant{ID: grantID, PatientRef: patientRef, GranteeRef: req.DoctorRef, ClinicRef: req.ClinicRef, Tier: rp.T4Extension,
			ScopeRef: requestRef, Basis: "P-SHARE", StartsAt: now, ExpiresAt: &until}
		if err := insertGrantTx(ctx, tx, g, "share_request", patientRef, in.RequestID); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: patientRef, ActorRole: "patient", Action: "share.extension_approved", SubjectRef: patientRef,
			ResourceType: "share_request", ResourceRef: requestRef, Purpose: string(rp.T4Extension), GrantID: grantID, Result: ap.Allowed, RequestID: in.RequestID}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "share.extension_decided", SubjectRef: patientRef,
			Payload: map[string]string{"requestId": requestRef, "doctorId": req.DoctorRef, "decision": "approved"}})
	})
	s.m.Invalidate(patientRef)
	if err != nil {
		return rp.ShareResult{}, err
	}
	return rp.ShareResult{GrantID: grantID, DoctorRef: req.DoctorRef, ExpiresAt: until}, nil
}

// Decline is the patient's "No". No access is created. The share is unchanged and ends when it ends;
// the doctor can ask again only while the share is ending or over, and no request is open.
func (s *Sharing) Decline(ctx context.Context, patientRef, requestRef, requestID string) error {
	if !allUUID(patientRef, requestRef) {
		return rp.ErrNotFound
	}
	now := s.m.now().UTC()
	return db.InTx(ctx, s.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status, doctor string
		var exp time.Time
		err := tx.QueryRow(ctx, `SELECT status, doctor_ref::text, expires_at FROM share_request WHERE id=$1 AND patient_ref=$2 FOR UPDATE`, requestRef, patientRef).Scan(&status, &doctor, &exp)
		if errors.Is(err, pgx.ErrNoRows) {
			return rp.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "pending" || !exp.After(now) {
			return rp.ErrRequestClosed
		}
		if _, err := tx.Exec(ctx, `UPDATE share_request SET status='declined', decided_at=$2 WHERE id=$1`, requestRef, now); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: patientRef, ActorRole: "patient", Action: "share.extension_declined", SubjectRef: patientRef,
			ResourceType: "share_request", ResourceRef: requestRef, Purpose: string(rp.T4Extension), Result: ap.Allowed, RequestID: requestID}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "share.extension_decided", SubjectRef: patientRef,
			Payload: map[string]string{"requestId": requestRef, "doctorId": doctor, "decision": "declined"}})
	})
}

// PendingRequestsPage is PendingRequests with keyset paging: after is the ID of the last request
// of the previous page (empty for the first). next is empty on the last page.
func (s *Sharing) PendingRequestsPage(ctx context.Context, patientRef string, limit int, after string) ([]rp.ExtensionRequest, string, error) {
	all, err := s.PendingRequests(ctx, patientRef)
	if err != nil {
		return nil, "", err
	}
	if after != "" && !reUUID.MatchString(after) {
		return nil, "", rp.ErrInvalid
	}
	limit = min(max(limit, 1), 100)
	start := 0
	if after != "" {
		start = len(all)
		for i, r := range all {
			if r.ID == after {
				start = i + 1
				break
			}
		}
	}
	all = all[start:]
	if len(all) > limit {
		return all[:limit], all[limit-1].ID, nil
	}
	return all, "", nil
}

// RequireVerified is the K-CS-01 check for doctor reads that do not go through Share (review
// F3: the doctor's document list re-checks on every read, as the history view does).
func (s *Sharing) RequireVerified(ctx context.Context, doctorRef string) error {
	return s.verified(ctx, doctorRef)
}
