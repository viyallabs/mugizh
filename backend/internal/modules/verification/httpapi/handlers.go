package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface for doctor submission (S-110) and the
// reviewer queue (S-129). The policy middleware enforces x-roles before these run; each
// handler also checks the acting role itself, so a wiring mistake fails closed.
type Handlers struct{ M *verification.Module }

var _ StrictServerInterface = Handlers{}

var (
	errInvalid       = problem.New(http.StatusBadRequest, "verification.invalid", "The verification request is not valid")
	errCouncil       = problem.New(http.StatusBadRequest, "verification.council_required", "The medical council is required")
	errProhibitedID  = problem.New(http.StatusBadRequest, "verification.prohibited_identifier", "The input contains an identifier that cannot be accepted")
	errIDType        = problem.New(http.StatusBadRequest, "verification.id_type_not_accepted", "This type of ID is not accepted")
	errIncomplete    = problem.New(http.StatusBadRequest, "verification.incomplete", "The submission is incomplete")
	errReason        = problem.New(http.StatusBadRequest, "verification.reason_required", "A valid reason code is required")
	errState         = problem.New(http.StatusConflict, "verification.state", "Not allowed in the current state")
	errFourEyes      = problem.New(http.StatusConflict, "verification.four_eyes", "A second, different reviewer must confirm")
	errSelfReview    = problem.New(http.StatusForbidden, "verification.own_case", "A reviewer cannot act on their own case")
	errTooLarge      = problem.New(http.StatusRequestEntityTooLarge, "verification.evidence_too_large", "The file is too large")
	errEvidenceType  = problem.New(http.StatusUnsupportedMediaType, "verification.evidence_type", "This file type is not accepted")
	errEvidenceLimit = problem.New(http.StatusConflict, "verification.evidence_limit", "Too many files")
	errGone          = problem.New(http.StatusGone, "verification.evidence_deleted", "The file has been deleted")
	errMalware       = problem.New(http.StatusUnprocessableEntity, "verification.file_rejected", "The file was rejected by the security scan")
	errScan          = problem.New(http.StatusServiceUnavailable, "verification.scan_unavailable", "The file could not be checked; try again later")
	errConsent       = problem.ConsentRequired
)

func mapErr(err error) error {
	var inv *verification.InvalidError
	switch {
	case errors.Is(err, verification.ErrNotFound):
		return problem.NotFound // other people's cases look the same as missing ones (K-SEC-04)
	case errors.As(err, &inv):
		return errInvalid.WithFields(problem.FieldError{Field: inv.Field, Code: inv.Code})
	case errors.Is(err, verification.ErrCouncilRequired):
		return errCouncil
	case errors.Is(err, verification.ErrProhibitedID):
		return errProhibitedID
	case errors.Is(err, verification.ErrIDTypeNotAllowed):
		return errIDType
	case errors.Is(err, verification.ErrConsentRequired):
		return errConsent
	case errors.Is(err, verification.ErrIncomplete):
		return errIncomplete
	case errors.Is(err, verification.ErrReasonRequired):
		return errReason
	case errors.Is(err, verification.ErrState):
		return errState
	case errors.Is(err, verification.ErrFourEyes):
		return errFourEyes
	case errors.Is(err, verification.ErrSelfReview):
		return errSelfReview
	case errors.Is(err, verification.ErrEvidenceTooLarge):
		return errTooLarge
	case errors.Is(err, verification.ErrEvidenceType):
		return errEvidenceType
	case errors.Is(err, verification.ErrEvidenceLimit):
		return errEvidenceLimit
	case errors.Is(err, verification.ErrEvidenceGone):
		return errGone
	case errors.Is(err, verification.ErrEvidenceMalware):
		return errMalware
	case errors.Is(err, verification.ErrEvidenceScanUnavailable):
		return errScan
	}
	return err
}

func acting(ctx context.Context, role string) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	if p.ActingRole != role {
		return nil, problem.Forbidden
	}
	return p, nil
}

func uuidOf(s string) openapi_types.UUID { return uuid.MustParse(s) }

func evidenceMeta(e verification.EvidenceMeta) EvidenceMeta {
	return EvidenceMeta{Id: uuidOf(e.ID), Kind: e.Kind, ContentType: e.ContentType, SizeBytes: e.SizeBytes, State: EvidenceMetaState(e.State), CreatedAt: e.CreatedAt}
}

func evidenceList(in []verification.EvidenceMeta) []EvidenceMeta {
	out := make([]EvidenceMeta, 0, len(in))
	for _, e := range in {
		out = append(out, evidenceMeta(e))
	}
	return out
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func doctorCase(c *verification.Case) VerificationCase {
	v := VerificationCase{
		Id: uuidOf(c.ID), Status: VerificationStatus(c.Status), RoundState: VerificationCaseRoundState(c.RoundState), Round: c.Round,
		CouncilCode: optStr(c.CouncilCode), RegistrationNumber: optStr(c.RegistrationNumber), RegisteredName: optStr(c.RegisteredName),
		Qualifications: append([]string{}, c.Qualifications...), SpecialtyClaim: optStr(c.SpecialtyClaim), IdType: optStr(c.IDType),
		Evidence: evidenceList(c.Evidence), InfoRequested: optStr(c.InfoRequested), SubmittedAt: c.SubmittedAt, VerifiedAt: c.VerifiedAt,
		ReVerifyDueAt: c.ReVerifyDueAt,
	}
	if c.RegistrationYear != 0 {
		y := c.RegistrationYear
		v.RegistrationYear = &y
	}
	return v
}

// GetMyVerification returns the doctor's own case.
func (h Handlers) GetMyVerification(ctx context.Context, _ GetMyVerificationRequestObject) (GetMyVerificationResponseObject, error) {
	p, err := acting(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	c, err := h.M.GetMine(ctx, p.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	return GetMyVerification200JSONResponse(doctorCase(c)), nil
}

// SaveVerificationDraft creates or replaces the draft.
func (h Handlers) SaveVerificationDraft(ctx context.Context, req SaveVerificationDraftRequestObject) (SaveVerificationDraftResponseObject, error) {
	p, err := acting(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	b := req.Body
	in := verification.DraftInput{}
	if b.CouncilCode != nil {
		in.CouncilCode = *b.CouncilCode
	}
	if b.RegistrationNumber != nil {
		in.RegistrationNumber = *b.RegistrationNumber
	}
	if b.RegistrationYear != nil {
		in.RegistrationYear = *b.RegistrationYear
	}
	if b.RegisteredName != nil {
		in.RegisteredName = *b.RegisteredName
	}
	if b.Qualifications != nil {
		in.Qualifications = *b.Qualifications
	}
	if b.SpecialtyClaim != nil {
		in.SpecialtyClaim = *b.SpecialtyClaim
	}
	if b.IdType != nil {
		in.IDType = *b.IdType
	}
	c, err := h.M.SaveDraft(ctx, p.UserID, in)
	if err != nil {
		return nil, mapErr(err)
	}
	return SaveVerificationDraft200JSONResponse(doctorCase(c)), nil
}

// UploadVerificationEvidence stores one file (base64 in JSON, within the 1 MB body limit).
func (h Handlers) UploadVerificationEvidence(ctx context.Context, req UploadVerificationEvidenceRequestObject) (UploadVerificationEvidenceResponseObject, error) {
	p, err := acting(ctx, ip.RoleDoctor)
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
	m, err := h.M.UploadEvidence(ctx, p.UserID, in)
	if err != nil {
		return nil, mapErr(err)
	}
	return UploadVerificationEvidence201JSONResponse(evidenceMeta(*m)), nil
}

// DeleteVerificationEvidence withdraws one of the doctor's own uploads.
func (h Handlers) DeleteVerificationEvidence(ctx context.Context, req DeleteVerificationEvidenceRequestObject) (DeleteVerificationEvidenceResponseObject, error) {
	p, err := acting(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	if err := h.M.DeleteEvidence(ctx, p.UserID, req.EvidenceId.String()); err != nil {
		return nil, mapErr(err)
	}
	return DeleteVerificationEvidence204Response{}, nil
}

// SubmitVerification sends the draft for review.
func (h Handlers) SubmitVerification(ctx context.Context, _ SubmitVerificationRequestObject) (SubmitVerificationResponseObject, error) {
	p, err := acting(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	c, err := h.M.Submit(ctx, p.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	return SubmitVerification200JSONResponse(doctorCase(c)), nil
}

// ListVerificationQueue lists cases waiting for a reviewer.
func (h Handlers) ListVerificationQueue(ctx context.Context, req ListVerificationQueueRequestObject) (ListVerificationQueueResponseObject, error) {
	p, err := acting(ctx, ip.RoleReviewer)
	if err != nil {
		return nil, err
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	items, next, err := h.M.ListQueue(ctx, p.UserID, limit, cursor)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListVerificationQueue200JSONResponse{Items: []QueueItem{}}
	for _, q := range items {
		item := QueueItem{CaseId: uuidOf(q.CaseID), SubjectRef: uuidOf(q.SubjectRef), Status: VerificationStatus(q.Status), SubmittedAt: q.SubmittedAt,
			DueBy: q.DueBy, Overdue: q.Overdue, AwaitingSecond: q.AwaitingSecond, Reverification: q.Reverification}
		k := QueueItemSubjectKind(q.SubjectKind)
		item.SubjectKind = &k
		a := QueueItemAutoCheck(q.AutoCheck)
		item.AutoCheck = &a
		out.Items = append(out.Items, item)
	}
	out.NextCursor = optStr(next)
	return out, nil
}

// GetVerificationReviewCase opens a case (audited).
func (h Handlers) GetVerificationReviewCase(ctx context.Context, req GetVerificationReviewCaseRequestObject) (GetVerificationReviewCaseResponseObject, error) {
	p, err := acting(ctx, ip.RoleReviewer)
	if err != nil {
		return nil, err
	}
	c, err := h.M.OpenCase(ctx, p.UserID, req.CaseId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	d := doctorCase(c)
	k, a := ReviewCaseSubjectKind(c.SubjectKind), ReviewCaseAutoCheck(c.AutoCheck)
	out := ReviewCase{
		Id: d.Id, Status: d.Status, RoundState: ReviewCaseRoundState(d.RoundState), Round: d.Round, CouncilCode: d.CouncilCode,
		RegistrationNumber: d.RegistrationNumber, RegistrationYear: d.RegistrationYear, RegisteredName: d.RegisteredName,
		Qualifications: d.Qualifications, SpecialtyClaim: d.SpecialtyClaim, IdType: d.IdType, Evidence: d.Evidence, InfoRequested: d.InfoRequested,
		SubmittedAt: d.SubmittedAt, VerifiedAt: d.VerifiedAt, ReVerifyDueAt: d.ReVerifyDueAt,
		SubjectRef: uuidOf(c.SubjectRef), SubjectKind: &k, AutoCheck: &a, ManualFlags: append([]string{}, c.ManualFlags...),
		Checks: []CheckResult{}, Decisions: []DecisionEntry{}, PendingOutcome: optStr(c.PendingOutcome),
		SpecialtyConfirmed: &c.SpecialtyConfirmed, ReviewDueBy: c.ReviewDueBy, Overdue: c.Overdue,
	}
	if c.PendingBy != "" {
		u := uuidOf(c.PendingBy)
		out.PendingBy = &u
	}
	for _, k := range c.Checks {
		out.Checks = append(out.Checks, CheckResult{Source: k.Source, Outcome: k.Outcome, RegisteredName: optStr(k.RegisteredName), CheckedAt: k.CheckedAt})
	}
	for _, d := range c.Decisions {
		out.Decisions = append(out.Decisions, DecisionEntry{Stage: DecisionEntryStage(d.Stage), Outcome: d.Outcome, ReasonCode: d.ReasonCode,
			ReviewerRef: uuidOf(d.ReviewerRef), Override: d.Override, DecidedAt: d.At})
	}
	return GetVerificationReviewCase200JSONResponse(out), nil
}

// GetVerificationEvidence reads one file for a reviewer (audited).
func (h Handlers) GetVerificationEvidence(ctx context.Context, req GetVerificationEvidenceRequestObject) (GetVerificationEvidenceResponseObject, error) {
	p, err := acting(ctx, ip.RoleReviewer)
	if err != nil {
		return nil, err
	}
	b, ct, err := h.M.ReadEvidence(ctx, p.UserID, req.CaseId.String(), req.EvidenceId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetVerificationEvidence200JSONResponse{ContentType: ct, ContentBase64: base64.StdEncoding.EncodeToString(b)}, nil
}

// RecordVerificationDecision records a decision; four-eyes rules are enforced by the module.
func (h Handlers) RecordVerificationDecision(ctx context.Context, req RecordVerificationDecisionRequestObject) (RecordVerificationDecisionResponseObject, error) {
	p, err := acting(ctx, ip.RoleReviewer)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	in := verification.DecisionInput{CaseID: req.CaseId.String(), ReviewerRef: p.UserID, Outcome: string(req.Body.Outcome), ReasonCode: req.Body.ReasonCode}
	if req.Body.LicenceExpiresOn != nil {
		t := req.Body.LicenceExpiresOn.Time
		in.LicenceExpiresOn = &t
	}
	if req.Body.SpecialtyConfirmed != nil {
		in.SpecialtyConfirmed = *req.Body.SpecialtyConfirmed
	}
	r, err := h.M.RecordDecision(ctx, in)
	if err != nil {
		return nil, mapErr(err)
	}
	return RecordVerificationDecision200JSONResponse{Status: VerificationStatus(r.Status), Final: r.Final, AwaitingSecond: r.AwaitingSecond}, nil
}

// ReportProhibitedIdDocument is the checklist action: hard-delete and ask for another ID.
func (h Handlers) ReportProhibitedIdDocument(ctx context.Context, req ReportProhibitedIdDocumentRequestObject) (ReportProhibitedIdDocumentResponseObject, error) {
	p, err := acting(ctx, ip.RoleReviewer)
	if err != nil {
		return nil, err
	}
	if err := h.M.ReportProhibitedID(ctx, p.UserID, req.CaseId.String(), req.EvidenceId.String()); err != nil {
		return nil, mapErr(err)
	}
	return ReportProhibitedIdDocument204Response{}, nil
}
