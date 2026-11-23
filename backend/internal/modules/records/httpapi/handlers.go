package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface for consent and sharing (S-107, S-168).
type Handlers struct {
	M *records.Module
	// R serves data rights, uploads, secure links and the access log (S-143, S-159, S-170, S-171).
	R *records.Rights
	// S serves history sharing and extension requests (S-168). Nil refuses them (fail closed).
	S *records.Sharing
}

var _ StrictServerInterface = Handlers{}

var (
	errNeeded       = problem.New(http.StatusConflict, "records.needed_consent", "Needed consents are withdrawn by closing the account")
	errNotRevocable = problem.New(http.StatusConflict, "records.not_revocable", "This access is required by law")
	errInvalid      = problem.New(http.StatusBadRequest, "records.invalid", "The consent request is not valid")
)

func mapErr(err error) error {
	switch {
	case errors.Is(err, rp.ErrNeededConsent):
		return errNeeded
	case errors.Is(err, rp.ErrNotRevocable):
		return errNotRevocable
	case errors.Is(err, rp.ErrInvalid):
		return errInvalid
	}
	return err
}

func patient(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok || p.ActingRole != ip.RolePatient {
		return nil, problem.Forbidden
	}
	return p, nil
}

// ListConsents returns the caller's consent states.
func (h Handlers) ListConsents(ctx context.Context, _ ListConsentsRequestObject) (ListConsentsResponseObject, error) {
	p, prefix, err := consentOwner(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := h.M.Consents(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := ListConsents200JSONResponse{Items: []Consent{}}
	for _, c := range cs {
		if !strings.HasPrefix(c.PurposeID, prefix) {
			continue
		}
		out.Items = append(out.Items, Consent{PurposeId: c.PurposeID, Granted: c.Granted, TextVersion: c.TextVersion, UpdatedAt: c.UpdatedAt})
	}
	return out, nil
}

// GrantConsent records consent for the shown text version.
func (h Handlers) GrantConsent(ctx context.Context, req GrantConsentRequestObject) (GrantConsentResponseObject, error) {
	p, prefix, err := consentOwner(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	if !strings.HasPrefix(req.Body.PurposeId, prefix) {
		return nil, problem.Forbidden // patients consent to P-* purposes, professionals to D-*
	}
	err = h.M.GrantConsent(ctx, rp.ConsentChange{SubjectRef: p.UserID, PurposeID: req.Body.PurposeId, TextVersion: req.Body.TextVersion,
		NoticeVersion: req.Body.NoticeVersion, Channel: "app", RequestID: httpx.RequestID(ctx), ActorRole: p.ActingRole})
	if err != nil {
		return nil, mapErr(err)
	}
	return GrantConsent204Response{}, nil
}

// WithdrawConsent withdraws an optional purpose immediately.
func (h Handlers) WithdrawConsent(ctx context.Context, req WithdrawConsentRequestObject) (WithdrawConsentResponseObject, error) {
	p, prefix, err := consentOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(req.PurposeId, prefix) {
		return nil, problem.Forbidden
	}
	err = h.M.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p.UserID, PurposeID: req.PurposeId, NoticeVersion: "",
		Channel: "app", RequestID: httpx.RequestID(ctx), ActorRole: p.ActingRole})
	if err != nil {
		return nil, mapErr(err)
	}
	return WithdrawConsent204Response{}, nil
}

// ListAccessGrants shows who can see the caller's record.
func (h Handlers) ListAccessGrants(ctx context.Context, _ ListAccessGrantsRequestObject) (ListAccessGrantsResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	gs, err := h.M.Grants(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := ListAccessGrants200JSONResponse{Items: []Grant{}}
	for _, g := range gs {
		switch g.Tier {
		case rp.T1CurrentCare, rp.T2OwnRecords, rp.T3HistoryShare, rp.T4Extension:
		default:
			continue // staff grants appear in the access log, not here
		}
		revocable := g.Tier == rp.T3HistoryShare || g.Tier == rp.T4Extension
		item := Grant{GrantId: uuid.MustParse(g.ID), Tier: GrantTier(g.Tier), GranteeId: uuid.MustParse(g.GranteeRef),
			StartsAt: g.StartsAt, ExpiresAt: g.ExpiresAt, RevokedAt: g.RevokedAt, Revocable: &revocable}
		if g.ClinicRef != "" {
			c := uuid.MustParse(g.ClinicRef)
			item.ClinicId = &c
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// RevokeAccessGrant stops sharing; the doctor's next request is denied (E-DP-01).
func (h Handlers) RevokeAccessGrant(ctx context.Context, req RevokeAccessGrantRequestObject) (RevokeAccessGrantResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	err = h.M.Revoke(ctx, req.GrantId.String(), p.UserID, true, httpx.RequestID(ctx))
	if errors.Is(err, rp.ErrInvalid) {
		return nil, problem.NotFound // not theirs or no such grant: same answer (K-SEC-04)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return RevokeAccessGrant204Response{}, nil
}

// consentOwner is the caller managing their own consents and the purposes they may manage:
// a patient the P-* purposes; a doctor, clinic admin or front desk member the D-* purposes of
// the provider privacy notice (S-275: D-VERIFY, D-PROFILE, D-STAFF). Nobody manages another
// person's consents (K-DP-10, adults only).
func consentOwner(ctx context.Context) (*ip.Principal, string, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, "", problem.Unauthenticated
	}
	switch p.ActingRole {
	case ip.RolePatient:
		return p, "P-", nil
	case ip.RoleDoctor, ip.RoleClinicAdmin, ip.RoleFrontDesk:
		return p, "D-", nil
	}
	return nil, "", problem.Forbidden
}
