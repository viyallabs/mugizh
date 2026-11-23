package admin

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Reverifier is satisfied by *verification.Module (consumer-side interface, K-ARC-01).
type Reverifier interface {
	TriggerReverification(ctx context.Context, actorRef, subjectRef, reasonCode string) error
}

// reverifyReasonComplaint is the verification module's reason code for a complaint-triggered check.
const reverifyReasonComplaint = "complaint"

// ReferForReverification lets the grievance officer refer the professional named in a complaint
// for re-verification (TM-31; verification RG-07): their case becomes due at once, and if they
// are not re-verified within the grace period they are suspended (no listing, booking or
// prescribing, K-CS-01). The verification module is called first, outside this module's
// transaction (no transaction spans modules, B6); the referral is then recorded on the
// grievance trail and audited. Only complaints can be referred.
func (m *Module) ReferForReverification(ctx context.Context, s pub.Staff, grievanceID, professionalRef string) error {
	if err := m.holds(ctx, s, "grievance_officer"); err != nil {
		return err
	}
	if !reUUID.MatchString(grievanceID) || !reUUID.MatchString(professionalRef) {
		return pub.ErrNotFound
	}
	if m.d.Reverify == nil {
		return errors.New("admin: re-verification is not wired") // fail closed
	}
	g, err := m.getGrievance(ctx, grievanceID)
	if err != nil {
		return err
	}
	if g.Kind != pub.KindComplaint {
		return pub.ErrInvalid
	}
	if err := m.d.Reverify.TriggerReverification(ctx, s.Ref, professionalRef, reverifyReasonComplaint); err != nil {
		return err
	}
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO grievance_event (id, grievance_id, action, actor_ref, occurred_at) VALUES ($1,$2,'referred_reverification',$3,$4)`,
			ids.New(), grievanceID, s.Ref, now); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.reverification_referred", "", "professional", professionalRef, reverifyReasonComplaint, "", "", ap.Allowed)
	})
}
