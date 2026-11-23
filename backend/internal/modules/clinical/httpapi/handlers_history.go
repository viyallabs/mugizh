package httpapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// mapHistoryErr maps the history view's errors. A refused share is 403 consent.required with the
// gate's reason code (grant_expired, out_of_scope, no_active_grant, ...) so the app can show the
// request-access prompt without any patient data in the problem. No relationship is 404.
func mapHistoryErr(err error) error {
	var de *clinical.DeniedError
	if errors.As(err, &de) && !de.Hide {
		return problem.ConsentRequired.WithFields(problem.FieldError{Field: "access", Code: de.Reason})
	}
	return mapErr(err)
}

// GetPatientHistory lists the signed records of other doctors for a patient who shared with me.
func (h Handlers) GetPatientHistory(ctx context.Context, req GetPatientHistoryRequestObject) (GetPatientHistoryResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	if h.H == nil {
		return nil, problem.Unavailable // fail closed when the view is not wired
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	after := ""
	if req.Params.Cursor != nil {
		after = *req.Params.Cursor
	}
	v, err := h.H.PatientHistory(ctx, c, req.PatientId.String(), limit, after)
	if err != nil {
		return nil, mapHistoryErr(err)
	}
	grant, _ := uuid.Parse(v.Access.GrantID)
	out := GetPatientHistory200JSONResponse{
		Access: HistoryAccess{GrantId: grant, Tier: HistoryAccessTier(v.Access.Tier), ExpiresAt: v.Access.ExpiresAt, WarnAt: v.Access.WarnAt, Warn: v.Access.Warn},
		Items:  []HistoryItem{},
		NextCursor: func() *string {
			if v.Next == "" {
				return nil
			}
			return &v.Next
		}(),
	}
	for _, it := range v.Items {
		author, _ := uuid.Parse(it.AuthorRef)
		x := HistoryItem{AuthorId: author, Kind: HistoryItemKind(it.Kind), SignedAt: it.SignedAt}
		if it.Note != nil {
			n := noteOut(*it.Note)
			x.Note = &n
		}
		if it.Rx != nil {
			r := rxOut(*it.Rx)
			x.Prescription = &r
		}
		out.Items = append(out.Items, x)
	}
	return out, nil
}
