package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Problems of the rights, upload and link flows. Details never carry personal data.
var (
	errNotConfigured = problem.Unavailable
	errNotFound      = problem.NotFound
	errUndoClosed    = problem.New(http.StatusConflict, "records.undo_closed", "The undo window has ended")
	errRestricted    = problem.New(http.StatusConflict, "records.account_restricted", "This account is being closed")
	errConflict      = problem.New(http.StatusConflict, "records.request_conflict", "A request of this kind is already open")
	errLinkGone      = problem.New(http.StatusGone, "records.link_expired", "This link has expired or was already used")
	errFileType      = problem.New(http.StatusUnsupportedMediaType, "records.file_type", "Only PDF, JPEG and PNG files are accepted")
	errQuota         = problem.New(http.StatusConflict, "records.document_quota_reached", "You have reached the limit for stored documents; delete some to upload more")
	errTooLarge      = problem.New(http.StatusRequestEntityTooLarge, "records.file_too_large", "The file is too large")
	errMalware       = problem.New(http.StatusUnprocessableEntity, "records.file_rejected", "The file was rejected by the security scan")
	errScan          = problem.New(http.StatusServiceUnavailable, "records.scan_unavailable", "The file could not be checked; try again later")
	errProhibited    = problem.New(http.StatusBadRequest, "records.prohibited_identifier", "The text contains a number that must not be entered")
)

func mapRightsErr(err error) error {
	switch {
	case errors.Is(err, rp.ErrStepUpRequired):
		return problem.StepUpRequired
	case errors.Is(err, rp.ErrNotFound), errors.Is(err, rp.ErrLinkInvalid):
		return errNotFound
	case errors.Is(err, rp.ErrRestricted), errors.Is(err, rp.ErrConflict):
		if errors.Is(err, rp.ErrRestricted) {
			return errRestricted
		}
		return errConflict
	case errors.Is(err, rp.ErrUndoClosed):
		return errUndoClosed
	case errors.Is(err, rp.ErrLinkExpired):
		return errLinkGone
	case errors.Is(err, rp.ErrDenied):
		return problem.ConsentRequired
	case errors.Is(err, rp.ErrFileType):
		return errFileType
	case errors.Is(err, rp.ErrQuota):
		return errQuota
	case errors.Is(err, rp.ErrTooLarge):
		return errTooLarge
	case errors.Is(err, rp.ErrMalware):
		return errMalware
	case errors.Is(err, rp.ErrScanUnavailable):
		return errScan
	case errors.Is(err, rp.ErrProhibitedID):
		return errProhibited
	case errors.Is(err, rp.ErrInvalid):
		return errInvalid
	}
	return err
}

// caller builds the rights caller from the authenticated session. Restricted sessions
// (new, unverified device) are carried through so the service refuses them.
func caller(ctx context.Context) (rp.Caller, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return rp.Caller{}, problem.Unauthenticated
	}
	return rp.Caller{UserRef: p.UserID, StepUpAt: p.StepUpAt, Restricted: p.Restricted, Role: p.ActingRole, ClinicRef: p.ActingClinic,
		RequestID: httpx.RequestID(ctx), IPHash: httpx.IPHash(ctx)}, nil
}

func (h Handlers) rights() (*records.Rights, error) {
	if h.R == nil {
		return nil, errNotConfigured
	}
	return h.R, nil
}

func toRequest(q rp.RightsRequest) RightsRequest {
	id, _ := uuid.Parse(q.ID)
	out := RightsRequest{RequestId: id, Type: RightsRequestType(q.Type), Status: RightsRequestStatus(q.Status), ReceivedAt: q.ReceivedAt, AckDueAt: q.AckDueAt,
		DueAt: q.DueAt, AcknowledgedAt: q.AcknowledgedAt, CompletedAt: q.CompletedAt, UndoUntil: q.UndoUntil, HoldEndsAt: q.HoldEndsAt, ExportExpiresAt: q.ExportExpiresAt}
	if q.Outcome != "" {
		o := q.Outcome
		out.Outcome = &o
	}
	if len(q.Report) > 0 {
		items := make([]ReportItem, 0, len(q.Report))
		for _, it := range q.Report {
			items = append(items, ReportItem{Module: it.Module, Action: it.Action, RetentionRule: it.RetentionRule, Until: it.Until})
		}
		out.Report = &items
	}
	return out
}

func limitOf(l *Limit) int {
	if l == nil {
		return 20
	}
	return int(*l)
}

func cursorOf(c *Cursor) string {
	if c == nil {
		return ""
	}
	return string(*c)
}

// CreateRightsRequest records an access, correction or nomination request.
func (h Handlers) CreateRightsRequest(ctx context.Context, req CreateRightsRequestRequestObject) (CreateRightsRequestResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	details := ""
	if req.Body.Details != nil {
		details = *req.Body.Details
	}
	q, err := r.RequestRights(ctx, c, rp.RightsType(req.Body.Type), details)
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return CreateRightsRequest202JSONResponse(toRequest(q)), nil
}

// RequestDataExport asks for a full copy; the service insists on a fresh step-up.
func (h Handlers) RequestDataExport(ctx context.Context, _ RequestDataExportRequestObject) (RequestDataExportResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	q, err := r.RequestRights(ctx, c, rp.RightsExport, "")
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return RequestDataExport202JSONResponse(toRequest(q)), nil
}

// RequestAccountErasure starts erasure step 1.
func (h Handlers) RequestAccountErasure(ctx context.Context, _ RequestAccountErasureRequestObject) (RequestAccountErasureResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	q, err := r.RequestRights(ctx, c, rp.RightsErasure, "")
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return RequestAccountErasure202JSONResponse(toRequest(q)), nil
}

// UndoAccountErasure cancels an erasure inside the window.
func (h Handlers) UndoAccountErasure(ctx context.Context, req UndoAccountErasureRequestObject) (UndoAccountErasureResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.CancelErasure(ctx, c, req.RequestId.String()); err != nil {
		return nil, mapRightsErr(err)
	}
	return UndoAccountErasure204Response{}, nil
}

// GetRightsRequest returns one request.
func (h Handlers) GetRightsRequest(ctx context.Context, req GetRightsRequestRequestObject) (GetRightsRequestResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	q, err := r.GetRequest(ctx, c, req.RequestId.String())
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return GetRightsRequest200JSONResponse(toRequest(q)), nil
}

// ListRightsRequests lists the caller's requests.
func (h Handlers) ListRightsRequests(ctx context.Context, req ListRightsRequestsRequestObject) (ListRightsRequestsResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	qs, next, err := r.ListRequests(ctx, c, cursorOf(req.Params.Cursor), limitOf(req.Params.Limit))
	if err != nil {
		return nil, mapRightsErr(err)
	}
	out := ListRightsRequests200JSONResponse{Items: []RightsRequest{}}
	for _, q := range qs {
		out.Items = append(out.Items, toRequest(q))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

func linkURL(token string) string { return "/v1/files/" + token }

// IssueExportLink issues a single-use link to the caller's finished export.
func (h Handlers) IssueExportLink(ctx context.Context, req IssueExportLinkRequestObject) (IssueExportLinkResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.IssueExportLink(ctx, c, req.RequestId.String())
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return IssueExportLink201JSONResponse(FileLink{Url: linkURL(l.Token), ExpiresAt: l.ExpiresAt}), nil
}

// IssueDocumentLink issues a single-use link to one document.
func (h Handlers) IssueDocumentLink(ctx context.Context, req IssueDocumentLinkRequestObject) (IssueDocumentLinkResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	l, err := r.IssueDocumentLink(ctx, c, req.DocumentId.String())
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return IssueDocumentLink201JSONResponse(FileLink{Url: linkURL(l.Token), ExpiresAt: l.ExpiresAt}), nil
}

// DownloadFile streams the decrypted file once. The response is always an attachment
// that browsers must not sniff or cache.
func (h Handlers) DownloadFile(ctx context.Context, req DownloadFileRequestObject) (DownloadFileResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	f, err := r.Download(ctx, c, string(req.Token))
	if err != nil {
		return nil, mapRightsErr(err)
	}
	disp, nocache, nosniff := "attachment; filename=\"file."+f.Extension+"\"", "no-store", "nosniff"
	return DownloadFile200ApplicationoctetStreamResponse{Body: bytes.NewReader(f.Data), ContentLength: int64(len(f.Data)),
		Headers: DownloadFile200ResponseHeaders{ContentDisposition: &disp, CacheControl: &nocache, XContentTypeOptions: &nosniff}}, nil
}

func toDoc(d rp.DocumentInfo) Document {
	id, _ := uuid.Parse(d.ID)
	return Document{DocumentId: id, Title: d.Title, ContentType: DocumentContentType(d.ContentType), SizeBytes: d.SizeBytes, CreatedAt: d.CreatedAt}
}

// UploadDocument reads the multipart body (title and file) and hands the bytes to the
// upload pipeline, which sniffs, scans, encrypts and stores them.
func (h Handlers) UploadDocument(ctx context.Context, req UploadDocumentRequestObject) (UploadDocumentResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	var title, declared string
	var data []byte
	gotFile := false
	for {
		part, err := req.Body.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errInvalid
		}
		switch part.FormName() {
		case "title":
			b, err := io.ReadAll(io.LimitReader(part, 1024))
			if err != nil {
				return nil, errInvalid
			}
			title = string(b)
		case "file":
			declared = part.Header.Get("Content-Type")
			// The service enforces the real limit; read at most one byte more than it allows.
			b, err := io.ReadAll(io.LimitReader(part, r.MaxUploadBytes()+1))
			if err != nil {
				return nil, errInvalid
			}
			data, gotFile = b, true
		}
		_ = part.Close()
	}
	if !gotFile || strings.TrimSpace(title) == "" {
		return nil, errInvalid
	}
	d, err := r.Upload(ctx, c, title, declared, bytes.NewReader(data))
	if err != nil {
		return nil, mapRightsErr(err)
	}
	return UploadDocument201JSONResponse(toDoc(d)), nil
}

// ListDocuments lists the caller's uploads.
func (h Handlers) ListDocuments(ctx context.Context, _ ListDocumentsRequestObject) (ListDocumentsResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := r.ListDocuments(ctx, c)
	if err != nil {
		return nil, mapRightsErr(err)
	}
	out := ListDocuments200JSONResponse{Items: []Document{}}
	for _, d := range ds {
		out.Items = append(out.Items, toDoc(d))
	}
	return out, nil
}

// ListAccessLog shows who viewed the caller's record.
func (h Handlers) ListAccessLog(ctx context.Context, req ListAccessLogRequestObject) (ListAccessLogResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	es, next, err := r.AccessLog(ctx, c, cursorOf(req.Params.Cursor), limitOf(req.Params.Limit))
	if err != nil {
		return nil, mapRightsErr(err)
	}
	out := ListAccessLog200JSONResponse{Items: []AccessLogEntry{}}
	for _, e := range es {
		out.Items = append(out.Items, toEntry(e))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

func toEntry(e ap.AccessEntry) AccessLogEntry {
	id, _ := uuid.Parse(e.EventID)
	return AccessLogEntry{EntryId: id, At: e.OccurredAt, ActorId: e.ActorRef, ActorRole: e.ActorRole, Kind: e.Kind, Purpose: e.Purpose}
}

// ReportDocumentProhibitedId quarantines a document reported to show a prohibited identifier (AAD-05). A doctor must
// be verified now (review F3, K-CS-01).
func (h Handlers) ReportDocumentProhibitedId(ctx context.Context, req ReportDocumentProhibitedIdRequestObject) (ReportDocumentProhibitedIdResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if c.Role == ip.RoleDoctor {
		if h.S == nil {
			return nil, problem.Unavailable
		}
		if err := h.S.RequireVerified(ctx, c.UserRef); err != nil {
			return nil, mapShareErr(err)
		}
	}
	if err := r.ReportProhibitedID(ctx, c, req.DocumentId.String()); err != nil {
		return nil, mapRightsErr(err)
	}
	return ReportDocumentProhibitedId204Response{}, nil
}

// DeleteMyDocument hides one of the caller's uploads for hard deletion (RET-03).
func (h Handlers) DeleteMyDocument(ctx context.Context, req DeleteMyDocumentRequestObject) (DeleteMyDocumentResponseObject, error) {
	r, err := h.rights()
	if err != nil {
		return nil, err
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.DeleteDocument(ctx, c, req.DocumentId.String()); err != nil {
		return nil, mapRightsErr(err)
	}
	return DeleteMyDocument204Response{}, nil
}
