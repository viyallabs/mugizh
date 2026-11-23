package directory

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Invitation states (review M5).
const (
	InvitationPending  = "pending_doctor"
	InvitationAccepted = "accepted"
	InvitationDeclined = "declined"
)

// Invitation is a clinic's request that a doctor be its lead doctor, or be listed there.
// It lists nothing until the doctor accepts.
type Invitation struct {
	ID          string
	Kind        string // practice or clinic_lead
	ClinicRef   string
	ClinicName  string
	PracticeRef string // empty for clinic_lead
	Status      string
	CreatedAt   time.Time
}

// invite records a pending invitation inside the caller's transaction and audits it.
func (m *Module) invite(ctx context.Context, tx pgx.Tx, a Actor, kind, doctorRef, clinicRef, practiceRef string) error {
	id := ids.New()
	var pr any
	if practiceRef != "" {
		pr = practiceRef
	}
	if _, err := tx.Exec(ctx, `INSERT INTO doctor_invitation (id, kind, professional_id, clinic_id, practice_id, status, created_by)
		VALUES ($1,$2,$3,$4,$5,'pending_doctor',$6)`, id, kind, doctorRef, clinicRef, pr, a.UserID); err != nil {
		return err
	}
	return m.audit(ctx, tx, a, "directory.invitation.create", "invitation", id, doctorRef)
}

// MyInvitations lists the acting doctor's pending invitations. Only doctors have any.
func (m *Module) MyInvitations(ctx context.Context, a Actor) ([]Invitation, error) {
	if a.Role != ip.RoleDoctor || !reUUID.MatchString(a.UserID) {
		return nil, errNotFound
	}
	rs, err := m.pool.Query(ctx, `SELECT i.id, i.kind, i.clinic_id, c.name, COALESCE(i.practice_id::text,''), i.status, i.created_at
		FROM doctor_invitation i JOIN clinic c ON c.id = i.clinic_id
		WHERE i.professional_id = $1 AND i.status = 'pending_doctor' ORDER BY i.created_at, i.id LIMIT 100`, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []Invitation{}
	for rs.Next() {
		var i Invitation
		if err := rs.Scan(&i.ID, &i.Kind, &i.ClinicRef, &i.ClinicName, &i.PracticeRef, &i.Status, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rs.Err()
}

// AcceptInvitation lets the invited doctor accept; the link then counts for listing (it
// still needs every verification). Anyone else, an unknown ID or an already decided
// invitation answers not found (K-SEC-04).
func (m *Module) AcceptInvitation(ctx context.Context, a Actor, id string) error {
	return m.decide(ctx, a, id, true)
}

// DeclineInvitation lets the invited doctor refuse; the link never lists.
func (m *Module) DeclineInvitation(ctx context.Context, a Actor, id string) error {
	return m.decide(ctx, a, id, false)
}

func (m *Module) decide(ctx context.Context, a Actor, id string, accept bool) error {
	if a.Role != ip.RoleDoctor || !reUUID.MatchString(a.UserID) || !reUUID.MatchString(id) {
		return errNotFound
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		var kind, clinic, practice string
		err := tx.QueryRow(ctx, `SELECT kind, clinic_id, COALESCE(practice_id::text,'') FROM doctor_invitation
			WHERE id = $1 AND professional_id = $2 AND status = 'pending_doctor' FOR UPDATE`, id, a.UserID).Scan(&kind, &clinic, &practice)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		status, action := InvitationDeclined, "directory.invitation.decline"
		if accept {
			status, action = InvitationAccepted, "directory.invitation.accept"
		}
		if _, err := tx.Exec(ctx, `UPDATE doctor_invitation SET status = $2, decided_at = now() WHERE id = $1`, id, status); err != nil {
			return err
		}
		if accept {
			if kind == "practice" {
				_, err = tx.Exec(ctx, `UPDATE practice SET doctor_accepted = true, updated_at = now() WHERE id = $1 AND professional_id = $2`, practice, a.UserID)
			} else {
				_, err = tx.Exec(ctx, `UPDATE clinic SET lead_accepted = true, updated_at = now() WHERE id = $1 AND lead_professional_id = $2`, clinic, a.UserID)
			}
			if err != nil {
				return err
			}
		}
		if err := m.audit(ctx, tx, a, action, "invitation", id, a.UserID); err != nil {
			return err
		}
		if accept {
			// Booking and search react to practice.updated; a lead acceptance can list every practice of the clinic.
			return m.emitClinicPractices(ctx, tx, clinic, practice)
		}
		return nil
	})
}

func (m *Module) emitClinicPractices(ctx context.Context, tx pgx.Tx, clinicRef, practiceRef string) error {
	if practiceRef != "" {
		return m.emitPracticeUpdated(ctx, tx, practiceRef)
	}
	rs, err := tx.Query(ctx, `SELECT id FROM practice WHERE clinic_id = $1`, clinicRef)
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
