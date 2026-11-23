package httpapi

import (
	"context"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// GetProfile returns the signed-in patient's own profile (S-108).
func (h Handlers) GetProfile(ctx context.Context, _ GetProfileRequestObject) (GetProfileResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := h.M.GetProfile(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	dob, err := time.Parse("2006-01-02", pr.DateOfBirth)
	if err != nil {
		return nil, problem.Internal
	}
	out := Profile{FullName: pr.FullName, DateOfBirth: openapi_types.Date{Time: dob}, Sex: ProfileSex(pr.Sex)}
	if pr.PreferredLanguage != "" {
		out.PreferredLanguage = &pr.PreferredLanguage
	}
	if pr.BloodGroup != "" {
		bg := BloodGroup(pr.BloodGroup)
		out.BloodGroup = &bg
	}
	if len(pr.Allergies) > 0 {
		out.Allergies = &pr.Allergies
	}
	if pr.EmergencyContact != nil {
		out.EmergencyContact = &EmergencyContact{Name: pr.EmergencyContact.Name, Phone: pr.EmergencyContact.Phone}
	}
	return GetProfile200JSONResponse(out), nil
}

// PutProfile creates or replaces the signed-in patient's own profile (S-108).
func (h Handlers) PutProfile(ctx context.Context, req PutProfileRequestObject) (PutProfileResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	b := req.Body
	in := identity.Profile{FullName: b.FullName, DateOfBirth: b.DateOfBirth.Format("2006-01-02"), Sex: string(b.Sex)}
	if b.PreferredLanguage != nil {
		in.PreferredLanguage = *b.PreferredLanguage
	}
	if b.BloodGroup != nil {
		in.BloodGroup = string(*b.BloodGroup)
	}
	if b.Allergies != nil {
		in.Allergies = *b.Allergies
	}
	if b.EmergencyContact != nil {
		in.EmergencyContact = &identity.EmergencyContact{Name: b.EmergencyContact.Name, Phone: b.EmergencyContact.Phone}
	}
	if err := h.M.PutProfile(ctx, p.UserID, in); err != nil {
		return nil, err
	}
	return PutProfile204Response{}, nil
}
