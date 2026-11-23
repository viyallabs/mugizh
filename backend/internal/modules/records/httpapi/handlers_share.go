package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Problems of the sharing flows (S-168). Details never carry personal data.
var (
	errDoctorNotVerified = problem.New(http.StatusForbidden, "records.doctor_not_verified", "This doctor cannot be given access")
	errExtensionNotNow   = problem.New(http.StatusConflict, "records.extension_not_allowed", "There is no share to extend right now")
	errRequestClosed     = problem.New(http.StatusConflict, "records.request_closed", "This request is already answered or has lapsed")
)

func mapShareErr(err error) error {
	switch {
	case errors.Is(err, rp.ErrDoctorNotVerified):
		return errDoctorNotVerified
	case errors.Is(err, rp.ErrExtensionNotAllowed):
		return errExtensionNotNow
	case errors.Is(err, rp.ErrRequestClosed):
		return errRequestClosed
	case errors.Is(err, rp.ErrNotFound):
		return problem.NotFound
	}
	return mapErr(err)
}

func (h Handlers) sharing() (*records.Sharing, error) {
	if h.S == nil {
		return nil, errNotConfigured // fail closed when sharing is not wired
	}
	return h.S, nil
}

func shareOut(r rp.ShareResult) HistoryShare {
	g, _ := uuid.Parse(r.GrantID)
	d, _ := uuid.Parse(r.DoctorRef)
	return HistoryShare{GrantId: g, DoctorId: d, ExpiresAt: r.ExpiresAt}
}

// CreateHistoryShare is the patient's tap on the P-SHARE screen (T3).
func (h Handlers) CreateHistoryShare(ctx context.Context, req CreateHistoryShareRequestObject) (CreateHistoryShareResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.sharing()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	in := rp.ShareInput{DoctorRef: req.Body.DoctorId.String(), AppointmentRef: req.Body.AppointmentId.String(), Until: req.Body.Until,
		TextVersion: req.Body.TextVersion, RequestID: httpx.RequestID(ctx)}
	if req.Body.NoticeVersion != nil {
		in.NoticeVersion = *req.Body.NoticeVersion
	}
	r, err := s.Share(ctx, p.UserID, in)
	if err != nil {
		return nil, mapShareErr(err)
	}
	return CreateHistoryShare201JSONResponse(shareOut(r)), nil
}

// ListAccessExtensionRequests lists the open requests for more time.
func (h Handlers) ListAccessExtensionRequests(ctx context.Context, req ListAccessExtensionRequestsRequestObject) (ListAccessExtensionRequestsResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.sharing()
	if err != nil {
		return nil, err
	}
	rs, next, err := s.PendingRequestsPage(ctx, p.UserID, limitOf(req.Params.Limit), cursorOf(req.Params.Cursor))
	if err != nil {
		return nil, mapShareErr(err)
	}
	out := ListAccessExtensionRequests200JSONResponse{Items: []ExtensionRequest{}, NextCursor: ptrIf(next)}
	for _, r := range rs {
		id, _ := uuid.Parse(r.ID)
		d, _ := uuid.Parse(r.DoctorRef)
		out.Items = append(out.Items, ExtensionRequest{RequestId: id, DoctorId: d, RequestedAt: r.RequestedAt, ExpiresAt: r.ExpiresAt,
			MaxExtensionSeconds: int(r.ExtensionMax / time.Second)})
	}
	return out, nil
}

// ApproveAccessExtension is the patient's "Allow": the only way a T4 grant is created.
func (h Handlers) ApproveAccessExtension(ctx context.Context, req ApproveAccessExtensionRequestObject) (ApproveAccessExtensionResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.sharing()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	in := rp.ApproveInput{TextVersion: req.Body.TextVersion, RequestID: httpx.RequestID(ctx)}
	if req.Body.NoticeVersion != nil {
		in.NoticeVersion = *req.Body.NoticeVersion
	}
	if req.Body.ExtensionSeconds != nil {
		in.For = time.Duration(*req.Body.ExtensionSeconds) * time.Second
	}
	r, err := s.Approve(ctx, p.UserID, req.RequestId.String(), in)
	if err != nil {
		return nil, mapShareErr(err)
	}
	return ApproveAccessExtension201JSONResponse(shareOut(r)), nil
}

// DeclineAccessExtension is the patient's "No".
func (h Handlers) DeclineAccessExtension(ctx context.Context, req DeclineAccessExtensionRequestObject) (DeclineAccessExtensionResponseObject, error) {
	p, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.sharing()
	if err != nil {
		return nil, err
	}
	if err := s.Decline(ctx, p.UserID, req.RequestId.String(), httpx.RequestID(ctx)); err != nil {
		return nil, mapShareErr(err)
	}
	return DeclineAccessExtension204Response{}, nil
}

func doctorCaller(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	if p.ActingRole != ip.RoleDoctor {
		return nil, problem.Forbidden
	}
	return p, nil
}

// RequestAccessExtension is the doctor's one-tap "ask for more time" (E-DP-02). It gives no access.
func (h Handlers) RequestAccessExtension(ctx context.Context, req RequestAccessExtensionRequestObject) (RequestAccessExtensionResponseObject, error) {
	p, err := doctorCaller(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.sharing()
	if err != nil {
		return nil, err
	}
	r, err := s.RequestExtension(ctx, p.UserID, req.PatientId.String(), httpx.RequestID(ctx))
	if err != nil {
		return nil, mapShareErr(err)
	}
	id, _ := uuid.Parse(r.ID)
	return RequestAccessExtension202JSONResponse(ExtensionRequestStatus{RequestId: id, Status: ExtensionRequestStatusStatus(r.Status), RequestedAt: r.RequestedAt, ExpiresAt: r.ExpiresAt}), nil
}

// ListPatientDocuments lists a patient's uploads for a doctor holding an active share.
func (h Handlers) ListPatientDocuments(ctx context.Context, req ListPatientDocumentsRequestObject) (ListPatientDocumentsResponseObject, error) {
	doc, err := doctorCaller(ctx)
	if err != nil {
		return nil, err
	}
	// Review F3 (K-CS-01): a doctor suspended during a share loses access at once. No sharing
	// service means no verifier: refuse (fail closed).
	if h.S == nil {
		return nil, problem.Unavailable
	}
	if err := h.S.RequireVerified(ctx, doc.UserID); err != nil {
		return nil, mapShareErr(err)
	}
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	sd, err := r.ListPatientDocuments(ctx, c, req.PatientId.String())
	if err != nil {
		return nil, mapRightsErr(err)
	}
	page, next := sd.Page(limitOf(req.Params.Limit), cursorOf(req.Params.Cursor))
	out := ListPatientDocuments200JSONResponse{Items: []Document{}, AccessExpiresAt: sd.AccessUntil, NextCursor: ptrIf(next)}
	for _, d := range page {
		out.Items = append(out.Items, toDoc(d))
	}
	return out, nil
}

func ptrIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
