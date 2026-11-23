package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/admin"
	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface. Staff routes are unreachable until staff
// sign-in exists (decision D-21); the patient grievance routes are live.
type Handlers struct{ M *admin.Module }

var _ StrictServerInterface = Handlers{}

var (
	errInvalid     = problem.New(http.StatusBadRequest, "admin.invalid", "The request is not valid")
	errNotPending  = problem.New(http.StatusConflict, "admin.not_pending", "The request is no longer pending")
	errExpired     = problem.New(http.StatusConflict, "admin.request_expired", "The request expired before it was approved")
	errSelfApprove = problem.New(http.StatusForbidden, "admin.self_approval", "The approver must not be the requester")
)

func mapErr(err error) error {
	switch {
	case errors.Is(err, pub.ErrInvalid), errors.Is(err, rp.ErrInvalid):
		return errInvalid
	case errors.Is(err, pub.ErrNotFound):
		return problem.NotFound
	case errors.Is(err, pub.ErrForbidden):
		return problem.Forbidden
	case errors.Is(err, pub.ErrSelfApproval):
		return errSelfApprove
	case errors.Is(err, pub.ErrNotPending):
		return errNotPending
	case errors.Is(err, pub.ErrExpired):
		return errExpired
	case errors.Is(err, pub.ErrRateLimited):
		return errLookupLimit
	case errors.Is(err, pub.ErrUnavailable):
		return problem.Unavailable
	}
	return err
}

func principal(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	return p, nil
}

func staff(ctx context.Context) (pub.Staff, error) {
	p, err := principal(ctx)
	if err != nil {
		return pub.Staff{}, err
	}
	return pub.Staff{Ref: p.UserID, Role: p.ActingRole, RequestID: httpx.RequestID(ctx)}, nil
}

func toGrievance(g pub.Grievance) Grievance {
	id, _ := uuid.Parse(g.ID)
	out := Grievance{GrievanceId: id, Reference: g.Reference, Kind: GrievanceKind(g.Kind), Status: GrievanceStatus(g.Status), ReceivedAt: g.ReceivedAt,
		AckDueAt: g.AckDueAt, DueAt: g.DueAt, AcknowledgedAt: g.AcknowledgedAt, ResolvedAt: g.ResolvedAt}
	if g.Outcome != "" {
		o := g.Outcome
		out.Outcome = &o
	}
	return out
}

func toAccess(r pub.AccessRequest) AccessRequest {
	id, _ := uuid.Parse(r.ID)
	sid, _ := uuid.Parse(r.StaffRef)
	pid, _ := uuid.Parse(r.PatientRef)
	out := AccessRequest{RequestId: id, StaffId: sid, StaffRole: r.StaffRole, PatientId: pid, ReasonCode: r.ReasonCode, TicketRef: r.TicketRef,
		DurationMinutes: int(r.Duration / time.Minute), BreakGlass: r.BreakGlass, Status: AccessRequestStatus(r.Status), RequestedAt: r.RequestedAt,
		DecidedAt: r.DecidedAt, GrantExpiresAt: r.GrantExpiresAt, JustificationDueAt: r.JustificationDueAt, Kinds: []string{}}
	for _, k := range r.Kinds {
		out.Kinds = append(out.Kinds, string(k))
	}
	if u, err := uuid.Parse(r.ApproverRef); err == nil {
		out.ApproverId = &u
	}
	if u, err := uuid.Parse(r.GrantID); err == nil {
		out.GrantId = &u
	}
	return out
}

func toInput(b AccessRequestCreate) pub.AccessRequestInput {
	in := pub.AccessRequestInput{PatientRef: b.PatientId.String(), ReasonCode: string(b.ReasonCode), TicketRef: b.TicketRef, Duration: time.Duration(b.DurationMinutes) * time.Minute}
	for _, k := range b.Kinds {
		in.Kinds = append(in.Kinds, rp.DataKind(k))
	}
	return in
}

// CreateGrievance records the caller's complaint (patient-facing, live).
func (h Handlers) CreateGrievance(ctx context.Context, req CreateGrievanceRequestObject) (CreateGrievanceResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.ActingRole != ip.RolePatient {
		return nil, problem.Forbidden
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	g, err := h.M.OpenGrievance(ctx, p.UserID, req.Body.Text)
	if err != nil {
		return nil, mapErr(err)
	}
	return CreateGrievance201JSONResponse(toGrievance(g)), nil
}

// ListMyGrievances lists the caller's complaints.
func (h Handlers) ListMyGrievances(ctx context.Context, req ListMyGrievancesRequestObject) (ListMyGrievancesResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.ActingRole != ip.RolePatient {
		return nil, problem.Forbidden
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	gs, err := h.M.MyGrievances(ctx, p.UserID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListMyGrievances200JSONResponse{Items: []Grievance{}}
	for _, g := range gs {
		out.Items = append(out.Items, toGrievance(g))
	}
	return out, nil
}

// RequestStaffAccess records a staff access request.
func (h Handlers) RequestStaffAccess(ctx context.Context, req RequestStaffAccessRequestObject) (RequestStaffAccessResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	r, err := h.M.RequestAccess(ctx, s, toInput(*req.Body))
	if err != nil {
		return nil, mapErr(err)
	}
	return RequestStaffAccess201JSONResponse(toAccess(r)), nil
}

// ApproveStaffAccess approves a request (a security owner who is not the requester).
func (h Handlers) ApproveStaffAccess(ctx context.Context, req ApproveStaffAccessRequestObject) (ApproveStaffAccessResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.M.ApproveAccess(ctx, s, req.RequestId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return ApproveStaffAccess200JSONResponse(toAccess(r)), nil
}

// RejectStaffAccess declines a request.
func (h Handlers) RejectStaffAccess(ctx context.Context, req RejectStaffAccessRequestObject) (RejectStaffAccessResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.M.RejectAccess(ctx, s, req.RequestId.String(), req.Body.Reason); err != nil {
		return nil, mapErr(err)
	}
	return RejectStaffAccess204Response{}, nil
}

// BreakGlassAccess grants immediate access in a live security incident.
func (h Handlers) BreakGlassAccess(ctx context.Context, req BreakGlassAccessRequestObject) (BreakGlassAccessResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	r, err := h.M.BreakGlass(ctx, s, toInput(*req.Body))
	if err != nil {
		return nil, mapErr(err)
	}
	return BreakGlassAccess201JSONResponse(toAccess(r)), nil
}

// MonthlyAccessReview lists a month of staff access for the security owner.
func (h Handlers) MonthlyAccessReview(ctx context.Context, req MonthlyAccessReviewRequestObject) (MonthlyAccessReviewResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	month, perr := time.Parse("2006-01", req.Month)
	if perr != nil {
		return nil, errInvalid
	}
	rows, err := h.M.MonthlyReview(ctx, s, month)
	if err != nil {
		return nil, mapErr(err)
	}
	out := MonthlyAccessReview200JSONResponse{Items: []AccessReviewRow{}}
	for _, r := range rows {
		id, _ := uuid.Parse(r.RequestID)
		sid, _ := uuid.Parse(r.StaffRef)
		pid, _ := uuid.Parse(r.PatientRef)
		row := AccessReviewRow{RequestId: id, RequestedAt: r.RequestedAt, StaffId: sid, StaffRole: r.StaffRole, PatientId: pid, Kinds: r.Kinds, ReasonCode: r.ReasonCode,
			TicketRef: r.TicketRef, BreakGlass: r.BreakGlass, Status: r.Status, Justified: r.Justified}
		if u, err := uuid.Parse(r.ApproverRef); err == nil {
			row.ApproverId = &u
		}
		if u, err := uuid.Parse(r.GrantID); err == nil {
			row.GrantId = &u
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}

// ListGrievanceQueue lists open grievances, most urgent first.
func (h Handlers) ListGrievanceQueue(ctx context.Context, req ListGrievanceQueueRequestObject) (ListGrievanceQueueResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	cursor, limit := "", 20
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	gs, next, err := h.M.Queue(ctx, s, cursor, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListGrievanceQueue200JSONResponse{Items: []Grievance{}}
	for _, g := range gs {
		out.Items = append(out.Items, toGrievance(g))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// AcknowledgeGrievance marks a grievance acknowledged.
func (h Handlers) AcknowledgeGrievance(ctx context.Context, req AcknowledgeGrievanceRequestObject) (AcknowledgeGrievanceResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.Acknowledge(ctx, s, req.GrievanceId.String()); err != nil {
		return nil, mapErr(err)
	}
	return AcknowledgeGrievance204Response{}, nil
}

// ResolveGrievance closes a grievance with an outcome code.
func (h Handlers) ResolveGrievance(ctx context.Context, req ResolveGrievanceRequestObject) (ResolveGrievanceResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.M.Resolve(ctx, s, req.GrievanceId.String(), req.Body.Outcome); err != nil {
		return nil, mapErr(err)
	}
	return ResolveGrievance204Response{}, nil
}

// ListGrievanceAlerts lists the timer alerts as of now.
func (h Handlers) ListGrievanceAlerts(ctx context.Context, _ ListGrievanceAlertsRequestObject) (ListGrievanceAlertsResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if s.Role != ip.RoleGrievanceOfficer && s.Role != ip.RoleSecurityOwner {
		return nil, problem.Forbidden
	}
	al, err := h.M.DueSoon(ctx, time.Now())
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListGrievanceAlerts200JSONResponse{Items: []Alert{}}
	for _, a := range al {
		id, _ := uuid.Parse(a.GrievanceID)
		k := string(a.Kind)
		out.Items = append(out.Items, Alert{ItemId: id, Reference: a.Reference, Kind: &k, Reason: AlertReason(a.Reason), DueAt: a.DueAt})
	}
	return out, nil
}

var errLookupLimit = problem.New(http.StatusTooManyRequests, "admin.lookup_limit", "Too many account lookups; try again later")

// LookupAccount finds an account by phone for support triage (S-172).
func (h Handlers) LookupAccount(ctx context.Context, req LookupAccountRequestObject) (LookupAccountResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	sum, err := h.M.LookupAccount(ctx, s, req.Body.Phone, req.Body.TicketRef)
	if err != nil {
		return nil, mapErr(err)
	}
	id, err := uuid.Parse(sum.UserID)
	if err != nil {
		return nil, err
	}
	day, err := time.Parse(time.DateOnly, sum.CreatedOn)
	if err != nil {
		return nil, err
	}
	roles := sum.Roles
	if roles == nil {
		roles = []string{}
	}
	return LookupAccount200JSONResponse{AccountId: id, ActiveDevices: sum.ActiveDevices, CreatedOn: openapi_types.Date{Time: day}, Roles: roles}, nil
}

func feedbackUser(ctx context.Context) (pub.User, error) {
	p, err := principal(ctx)
	if err != nil {
		return pub.User{}, err
	}
	return pub.User{Ref: p.UserID, Role: p.ActingRole, RequestID: httpx.RequestID(ctx)}, nil
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optPlatform(s string) *FeedbackPlatform {
	if s == "" {
		return nil
	}
	p := FeedbackPlatform(s)
	return &p
}

func toDiagnostics(d *pub.FeedbackDiagnostics) *FeedbackDiagnostics {
	if d == nil {
		return nil
	}
	out := &FeedbackDiagnostics{LastErrorCode: optStr(d.LastErrorCode), OsVersion: optStr(d.OSVersion)}
	if d.NetworkType != "" {
		n := FeedbackDiagnosticsNetworkType(d.NetworkType)
		out.NetworkType = &n
	}
	return out
}

func fromDiagnostics(d *FeedbackDiagnostics) *pub.FeedbackDiagnostics {
	if d == nil {
		return nil
	}
	out := &pub.FeedbackDiagnostics{}
	if d.LastErrorCode != nil {
		out.LastErrorCode = *d.LastErrorCode
	}
	if d.OsVersion != nil {
		out.OSVersion = *d.OsVersion
	}
	if d.NetworkType != nil {
		out.NetworkType = string(*d.NetworkType)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CreateFeedback records the caller's feedback or bug report (S-189), any signed-in role.
func (h Handlers) CreateFeedback(ctx context.Context, req CreateFeedbackRequestObject) (CreateFeedbackResponseObject, error) {
	u, err := feedbackUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	in := pub.FeedbackInput{Kind: string(b.Kind), Message: b.Message, Screen: deref(b.Screen), AppVersion: deref(b.AppVersion), Diagnostics: fromDiagnostics(b.Diagnostics)}
	if b.Platform != nil {
		in.Platform = string(*b.Platform)
	}
	r, err := h.M.SubmitFeedback(ctx, u, in)
	if err != nil {
		return nil, mapErr(err)
	}
	return CreateFeedback201JSONResponse(FeedbackReceipt{FeedbackId: uuid.MustParse(r.ID), Reference: r.Reference, ReceivedAt: r.ReceivedAt}), nil
}

// ListMyFeedback lists the caller's own feedback with their own message.
func (h Handlers) ListMyFeedback(ctx context.Context, req ListMyFeedbackRequestObject) (ListMyFeedbackResponseObject, error) {
	u, err := feedbackUser(ctx)
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
	items, next, err := h.M.MyFeedback(ctx, u, limit, cursor)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListMyFeedback200JSONResponse{Items: make([]Feedback, 0, len(items))}
	for _, i := range items {
		out.Items = append(out.Items, Feedback{FeedbackId: uuid.MustParse(i.ID), Reference: i.Reference, Kind: FeedbackKind(i.Kind), Message: i.Message,
			Screen: optStr(i.Screen), AppVersion: optStr(i.AppVersion), Platform: optPlatform(i.Platform), Diagnostics: toDiagnostics(i.Diagnostics),
			Status: FeedbackStatus(i.Status), ReceivedAt: i.ReceivedAt})
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// ListFeedbackForTriage lists feedback for support without the message. Staff sign-in does not
// exist yet (D-21), so this is unreachable over HTTP until it does.
func (h Handlers) ListFeedbackForTriage(ctx context.Context, req ListFeedbackForTriageRequestObject) (ListFeedbackForTriageResponseObject, error) {
	s, err := staff(ctx)
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
	items, next, err := h.M.FeedbackQueue(ctx, s, cursor, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListFeedbackForTriage200JSONResponse{Items: make([]FeedbackTriage, 0, len(items))}
	for _, i := range items {
		out.Items = append(out.Items, FeedbackTriage{FeedbackId: uuid.MustParse(i.ID), Reference: i.Reference, Kind: FeedbackKind(i.Kind),
			Screen: optStr(i.Screen), AppVersion: optStr(i.AppVersion), Platform: optPlatform(i.Platform), Diagnostics: toDiagnostics(i.Diagnostics),
			Status: FeedbackStatus(i.Status), ReceivedAt: i.ReceivedAt})
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// SetFeedbackStatus marks feedback triaged or closed (support).
func (h Handlers) SetFeedbackStatus(ctx context.Context, req SetFeedbackStatusRequestObject) (SetFeedbackStatusResponseObject, error) {
	s, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.M.SetFeedbackStatus(ctx, s, req.FeedbackId.String(), string(req.Body.Status)); err != nil {
		return nil, mapErr(err)
	}
	return SetFeedbackStatus204Response{}, nil
}
