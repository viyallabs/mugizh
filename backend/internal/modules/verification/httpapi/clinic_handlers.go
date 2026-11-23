package httpapi

import (
	"context"
	"encoding/base64"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Clinic (facility) verification endpoints (D-38). The clinic admin acts for the clinic in
// the principal's scope only; another clinic's ID answers 404 (K-SEC-04), decided in the
// module (ClinicActor.scope).

func clinicActing(ctx context.Context) (verification.ClinicActor, error) {
	p, err := acting(ctx, ip.RoleClinicAdmin)
	if err != nil {
		return verification.ClinicActor{}, err
	}
	return verification.ClinicActor{UserID: p.UserID, ClinicRef: p.ActingClinic}, nil
}

func clinicCase(c *verification.Case) ClinicVerificationCase {
	v := ClinicVerificationCase{
		Id: uuidOf(c.ID), ClinicId: uuidOf(c.SubjectRef), Status: VerificationStatus(c.Status),
		RoundState: ClinicVerificationCaseRoundState(c.RoundState), Round: c.Round,
		AuthorityName: optStr(c.CouncilCode), RegistrationNumber: optStr(c.RegistrationNumber), LegalName: optStr(c.RegisteredName),
		Evidence: evidenceList(c.Evidence), InfoRequested: optStr(c.InfoRequested), SubmittedAt: c.SubmittedAt, VerifiedAt: c.VerifiedAt,
		ReVerifyDueAt: c.ReVerifyDueAt,
	}
	if c.RegistrationYear != 0 {
		y := c.RegistrationYear
		v.RegistrationYear = &y
	}
	return v
}

// GetClinicVerification returns the clinic's own case.
func (h Handlers) GetClinicVerification(ctx context.Context, req GetClinicVerificationRequestObject) (GetClinicVerificationResponseObject, error) {
	a, err := clinicActing(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.M.GetClinicCase(ctx, a, req.ClinicId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetClinicVerification200JSONResponse(clinicCase(c)), nil
}

// SaveClinicVerificationDraft creates or replaces the clinic's draft.
func (h Handlers) SaveClinicVerificationDraft(ctx context.Context, req SaveClinicVerificationDraftRequestObject) (SaveClinicVerificationDraftResponseObject, error) {
	a, err := clinicActing(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	b := req.Body
	in := verification.ClinicDraftInput{}
	if b.AuthorityName != nil {
		in.AuthorityName = *b.AuthorityName
	}
	if b.RegistrationNumber != nil {
		in.RegistrationNumber = *b.RegistrationNumber
	}
	if b.RegistrationYear != nil {
		in.RegistrationYear = *b.RegistrationYear
	}
	if b.LegalName != nil {
		in.LegalName = *b.LegalName
	}
	c, err := h.M.SaveClinicDraft(ctx, a, req.ClinicId.String(), in)
	if err != nil {
		return nil, mapErr(err)
	}
	return SaveClinicVerificationDraft200JSONResponse(clinicCase(c)), nil
}

// UploadClinicVerificationEvidence stores one document (base64 in JSON).
func (h Handlers) UploadClinicVerificationEvidence(ctx context.Context, req UploadClinicVerificationEvidenceRequestObject) (UploadClinicVerificationEvidenceResponseObject, error) {
	a, err := clinicActing(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	data, err := base64.StdEncoding.DecodeString(req.Body.ContentBase64)
	if err != nil {
		return nil, errInvalid.WithFields(problem.FieldError{Field: "contentBase64", Code: "format"})
	}
	in := verification.EvidenceInput{Kind: string(req.Body.Kind), Data: data}
	if req.Body.ContentType != nil {
		in.ContentType = string(*req.Body.ContentType)
	}
	m, err := h.M.UploadClinicEvidence(ctx, a, req.ClinicId.String(), in)
	if err != nil {
		return nil, mapErr(err)
	}
	return UploadClinicVerificationEvidence201JSONResponse(evidenceMeta(*m)), nil
}

// DeleteClinicVerificationEvidence withdraws one of the clinic's uploads.
func (h Handlers) DeleteClinicVerificationEvidence(ctx context.Context, req DeleteClinicVerificationEvidenceRequestObject) (DeleteClinicVerificationEvidenceResponseObject, error) {
	a, err := clinicActing(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.DeleteClinicEvidence(ctx, a, req.ClinicId.String(), req.EvidenceId.String()); err != nil {
		return nil, mapErr(err)
	}
	return DeleteClinicVerificationEvidence204Response{}, nil
}

// SubmitClinicVerification sends the clinic's draft for review.
func (h Handlers) SubmitClinicVerification(ctx context.Context, req SubmitClinicVerificationRequestObject) (SubmitClinicVerificationResponseObject, error) {
	a, err := clinicActing(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.M.SubmitClinic(ctx, a, req.ClinicId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return SubmitClinicVerification200JSONResponse(clinicCase(c)), nil
}
