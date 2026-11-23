package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Caregiver endpoints (v1.1 phase 1, P-CAREGIVER). They answer 404 when the country pack's "caregivers" flag is off.
var (
	errCaregiverLimit  = problem.New(http.StatusConflict, "caregiving.limit_reached", "The limit of caregivers has been reached")
	errAlreadyLinked   = problem.New(http.StatusConflict, "caregiving.already_linked", "You already care for this person")
	errCountryNotConf  = problem.New(http.StatusBadRequest, "caregiving.country_not_confirmed", "Confirm that you are in the country first")
	errCaregiverInvald = problem.New(http.StatusBadRequest, "caregiving.invalid", "The request is not valid")
)

func mapCaregiverErr(err error) error {
	switch {
	case errors.Is(err, rp.ErrCaregiverLimit):
		return errCaregiverLimit
	case errors.Is(err, rp.ErrAlreadyLinked):
		return errAlreadyLinked
	case errors.Is(err, rp.ErrRedeemLimited):
		return problem.RateLimited
	case errors.Is(err, rp.ErrCountryNotConfirmed):
		return errCountryNotConf
	case errors.Is(err, rp.ErrInvalid):
		return errCaregiverInvald
	}
	return mapRightsErr(err)
}

// caregiving returns the service and the caller, or 404 when the feature is off in this country.
func (h Handlers) caregiving(ctx context.Context) (*records.Rights, rp.Caller, error) {
	if h.M == nil || !h.M.CaregiversEnabled() {
		return nil, rp.Caller{}, problem.NotFound
	}
	r, err := h.rights()
	if err != nil {
		return nil, rp.Caller{}, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, rp.Caller{}, err
	}
	return r, c, nil
}

func scopesOut(in []string) []CaregiverScope {
	out := make([]CaregiverScope, 0, len(in))
	for _, s := range in {
		out = append(out, CaregiverScope(s))
	}
	return out
}

func linkOut(l rp.CaregiverLink) CaregiverLink {
	id, _ := uuid.Parse(l.ID)
	cg, _ := uuid.Parse(l.CaregiverRef)
	out := CaregiverLink{LinkId: id, CaregiverId: cg, Scopes: scopesOut(l.Scopes), Status: CaregiverLinkStatus(l.Status), CreatedAt: l.CreatedAt, RevokedAt: l.RevokedAt}
	if l.Label != "" {
		out.Label = &l.Label
	}
	return out
}

func carePatientOut(l rp.CaregiverLink) CaregivingPatient {
	id, _ := uuid.Parse(l.ID)
	p, _ := uuid.Parse(l.PatientRef)
	return CaregivingPatient{PatientId: p, LinkId: id, Scopes: scopesOut(l.Scopes), Since: l.CreatedAt}
}

// CreateCaregiverInvitation makes a one-time code (P-CAREGIVER consent and a fresh step-up).
func (h Handlers) CreateCaregiverInvitation(ctx context.Context, req CreateCaregiverInvitationRequestObject) (CreateCaregiverInvitationResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	var scopes []string
	if req.Body != nil && req.Body.Scopes != nil {
		scopes = []string{}
		for _, s := range *req.Body.Scopes {
			scopes = append(scopes, string(s))
		}
	}
	label := ""
	if req.Body != nil && req.Body.Label != nil {
		label = *req.Body.Label
	}
	inv, err := r.CreateCaregiverInvitation(ctx, c, scopes, label)
	if err != nil {
		return nil, mapCaregiverErr(err)
	}
	id, _ := uuid.Parse(inv.ID)
	out := CaregiverInvitation{InvitationId: id, Code: inv.Code, Scopes: scopesOut(inv.Scopes), ExpiresAt: inv.ExpiresAt}
	if inv.Label != "" {
		out.Label = &inv.Label
	}
	return CreateCaregiverInvitation201JSONResponse(out), nil
}

// CancelCaregiverInvitation withdraws an unused invitation.
func (h Handlers) CancelCaregiverInvitation(ctx context.Context, req CancelCaregiverInvitationRequestObject) (CancelCaregiverInvitationResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.CancelCaregiverInvitation(ctx, c, req.InvitationId.String()); err != nil {
		return nil, mapCaregiverErr(err)
	}
	return CancelCaregiverInvitation204Response{}, nil
}

// ListMyCaregivers lists the patient's links.
func (h Handlers) ListMyCaregivers(ctx context.Context, req ListMyCaregiversRequestObject) (ListMyCaregiversResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	ls, next, err := r.ListMyCaregivers(ctx, c, cursorOf(req.Params.Cursor), limitOf(req.Params.Limit))
	if err != nil {
		return nil, mapCaregiverErr(err)
	}
	out := ListMyCaregivers200JSONResponse{Items: make([]CaregiverLink, 0, len(ls))}
	for _, l := range ls {
		out.Items = append(out.Items, linkOut(l))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// SetCaregiverScopes changes what one caregiver can see (step-up only when a sensitive scope is added).
func (h Handlers) SetCaregiverScopes(ctx context.Context, req SetCaregiverScopesRequestObject) (SetCaregiverScopesResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errCaregiverInvald
	}
	scopes := []string{}
	for _, s := range req.Body.Scopes {
		scopes = append(scopes, string(s))
	}
	l, err := r.SetCaregiverScopes(ctx, c, req.LinkId.String(), scopes)
	if err != nil {
		return nil, mapCaregiverErr(err)
	}
	return SetCaregiverScopes200JSONResponse(linkOut(l)), nil
}

// RevokeCaregiver removes a caregiver at once.
func (h Handlers) RevokeCaregiver(ctx context.Context, req RevokeCaregiverRequestObject) (RevokeCaregiverResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.RevokeCaregiver(ctx, c, req.LinkId.String()); err != nil {
		return nil, mapCaregiverErr(err)
	}
	return RevokeCaregiver204Response{}, nil
}

// RedeemCaregiverCode accepts an invitation.
func (h Handlers) RedeemCaregiverCode(ctx context.Context, req RedeemCaregiverCodeRequestObject) (RedeemCaregiverCodeResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errCaregiverInvald
	}
	l, err := r.RedeemCaregiverCode(ctx, c, req.Body.Code, req.Body.InCountryConfirmed)
	if err != nil {
		return nil, mapCaregiverErr(err)
	}
	return RedeemCaregiverCode201JSONResponse(carePatientOut(l)), nil
}

// ListCaregivingPatients lists the people the caller cares for.
func (h Handlers) ListCaregivingPatients(ctx context.Context, req ListCaregivingPatientsRequestObject) (ListCaregivingPatientsResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	ls, next, err := r.ListCaregivingPatients(ctx, c, cursorOf(req.Params.Cursor), limitOf(req.Params.Limit))
	if err != nil {
		return nil, mapCaregiverErr(err)
	}
	out := ListCaregivingPatients200JSONResponse{Items: make([]CaregivingPatient, 0, len(ls))}
	for _, l := range ls {
		out.Items = append(out.Items, carePatientOut(l))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// LeaveCaregiving ends the caller's own link.
func (h Handlers) LeaveCaregiving(ctx context.Context, req LeaveCaregivingRequestObject) (LeaveCaregivingResponseObject, error) {
	r, c, err := h.caregiving(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.LeaveCaregiving(ctx, c, req.PatientId.String()); err != nil {
		return nil, mapCaregiverErr(err)
	}
	return LeaveCaregiving204Response{}, nil
}
