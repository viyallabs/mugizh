// Package httpapi implements the booking OpenAPI operations (api/core/booking.v1.yaml).
package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface. Errors are *problem.Problem values returned by
// the module; nothing is written by hand.
type Handlers struct{ M *booking.Module }

var _ StrictServerInterface = Handlers{}

func principal(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	return p, nil
}

func patient(ctx context.Context) (string, error) {
	p, err := principal(ctx)
	if err != nil {
		return "", err
	}
	if p.ActingRole != ip.RolePatient {
		return "", problem.Forbidden
	}
	return p.UserID, nil
}

func actor(ctx context.Context) (booking.Actor, error) {
	p, err := principal(ctx)
	if err != nil {
		return booking.Actor{}, err
	}
	return booking.Actor{UserID: p.UserID, Role: p.ActingRole, ClinicRef: p.ActingClinic, RequestID: httpx.RequestID(ctx), Restricted: p.Restricted}, nil
}

func uid(s string) openapi_types.UUID { return uuid.MustParse(s) }

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func enumPtr[T ~string](s string) *T {
	if s == "" {
		return nil
	}
	v := T(s)
	return &v
}

func slot(s booking.SlotInfo) Slot {
	return Slot{SlotId: uid(s.ID), PracticeId: uid(s.PracticeID), StartsAt: s.StartsAt, EndsAt: s.EndsAt, Mode: ConsultMode(s.Mode)}
}

func slots(in []booking.SlotInfo) []Slot {
	out := make([]Slot, 0, len(in))
	for _, s := range in {
		out = append(out, slot(s))
	}
	return out
}

func appt(a booking.Appointment) Appointment {
	pl, _ := parseTime(a.PracticeLocalStart())
	dl, _ := parseTime(a.PatientLocalStart())
	return Appointment{AppointmentId: uid(a.ID), PracticeId: uid(a.PracticeID), DoctorId: uid(a.ProfessionalRef), ClinicId: uid(a.ClinicRef),
		State: AppointmentState(a.State), Mode: ConsultMode(a.Mode), StartsAt: a.StartsAt, EndsAt: a.EndsAt,
		PracticeTimeZone: a.PracticeTimeZone, PatientTimeZone: a.PatientTimeZone, PracticeLocalStart: pl, PatientLocalStart: dl,
		PatientInIndiaConfirmed: a.PatientInIndiaConfirmed, FeeAmount: a.FeeMinor, Currency: a.Currency, FeeVersion: a.FeeVersion,
		RefundPolicyVersion: a.PolicyVersion, SettlementMode: AppointmentSettlementMode(a.SettlementMode), HoldExpiresAt: a.HoldExpiresAt,
		CancelledBy: enumPtr[AppointmentCancelledBy](a.CancelledBy), NoShowBy: enumPtr[AppointmentNoShowBy](a.NoShowBy),
		RescheduleCount: a.RescheduleCount, NeedsReschedule: a.NeedsReschedule}
}

func slotTaken(err error) (*booking.SlotTakenError, bool) {
	var st *booking.SlotTakenError
	return st, errors.As(err, &st)
}

func takenBody(st *booking.SlotTakenError) SlotTakenProblem {
	p := booking.ProblemSlotTaken
	next := slots(st.Next)
	return SlotTakenProblem{Type: p.Type, Title: p.Title, Status: p.Status, Code: p.Code, NextSlots: &next}
}

func (h Handlers) ListOpenSlots(ctx context.Context, req ListOpenSlotsRequestObject) (ListOpenSlotsResponseObject, error) {
	if _, err := patient(ctx); err != nil {
		return nil, err
	}
	var from, to = zeroTime, zeroTime
	if req.Params.From != nil {
		from = *req.Params.From
	}
	if req.Params.To != nil {
		to = *req.Params.To
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	items, next, err := h.M.ListOpenSlots(ctx, req.PracticeId.String(), from, to, limit, cursor)
	if err != nil {
		return nil, err
	}
	return ListOpenSlots200JSONResponse(SlotList{Items: slots(items), NextCursor: optStr(next)}), nil
}

func (h Handlers) HoldSlot(ctx context.Context, req HoldSlotRequestObject) (HoldSlotResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	in := booking.HoldInput{PatientRef: pat, PracticeID: req.PracticeId.String(), SlotID: req.Body.SlotId.String(), Mode: string(req.Body.Mode),
		PatientTimeZone: req.Body.PatientTimeZone, RequestID: httpx.RequestID(ctx)}
	if req.Body.PatientInIndiaConfirmed != nil {
		in.PatientInIndiaConfirmed = *req.Body.PatientInIndiaConfirmed
	}
	a, err := h.M.Hold(ctx, in)
	if st, ok := slotTaken(err); ok {
		return HoldSlot409ApplicationProblemPlusJSONResponse(takenBody(st)), nil
	}
	if err != nil {
		return nil, err
	}
	return HoldSlot201JSONResponse(appt(a)), nil
}

func (h Handlers) ListMyAppointments(ctx context.Context, req ListMyAppointmentsRequestObject) (ListMyAppointmentsResponseObject, error) {
	pat, err := patient(ctx)
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
	items, next, err := h.M.ListMine(ctx, pat, limit, cursor)
	if err != nil {
		return nil, err
	}
	out := ListMyAppointments200JSONResponse{Items: make([]Appointment, 0, len(items)), NextCursor: optStr(next)}
	for _, a := range items {
		out.Items = append(out.Items, appt(a))
	}
	return out, nil
}

func (h Handlers) GetMyAppointment(ctx context.Context, req GetMyAppointmentRequestObject) (GetMyAppointmentResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	a, err := h.M.Mine(ctx, pat, req.AppointmentId.String())
	if err != nil {
		return nil, err
	}
	return GetMyAppointment200JSONResponse(appt(a)), nil
}

func (h Handlers) CheckoutAppointment(ctx context.Context, req CheckoutAppointmentRequestObject) (CheckoutAppointmentResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	co, err := h.M.Checkout(ctx, pat, req.AppointmentId.String())
	if err != nil {
		return nil, err
	}
	return CheckoutAppointment200JSONResponse(Checkout{Appointment: appt(co.Appointment), OrderId: optStr(co.OrderRef), CheckoutUrl: optStr(co.CheckoutURL),
		OrderState: optStr(co.OrderState), AmountMinor: co.AmountMinor, Currency: co.Currency}), nil
}

func (h Handlers) PreviewCancelAppointment(ctx context.Context, req PreviewCancelAppointmentRequestObject) (PreviewCancelAppointmentResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.M.PreviewCancel(ctx, pat, req.AppointmentId.String())
	if err != nil {
		return nil, err
	}
	return PreviewCancelAppointment200JSONResponse(CancelPreview{Paid: p.Paid, RefundAmount: p.RefundMinor, Currency: p.Currency, RuleId: optStr(p.Rule),
		MinutesBeforeSlot: p.MinutesBeforeSlot, ByWhom: CancelPreviewByWhom(p.ByWhom)}), nil
}

func (h Handlers) CancelAppointment(ctx context.Context, req CancelAppointmentRequestObject) (CancelAppointmentResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	_, a, err := h.M.Cancel(ctx, pat, req.AppointmentId.String(), req.Body.AcknowledgedRefundAmount, httpx.RequestID(ctx))
	if err != nil {
		return nil, err
	}
	return CancelAppointment200JSONResponse(appt(a)), nil
}

func (h Handlers) RescheduleAppointment(ctx context.Context, req RescheduleAppointmentRequestObject) (RescheduleAppointmentResponseObject, error) {
	pat, err := patient(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	a, err := h.M.Reschedule(ctx, pat, req.AppointmentId.String(), req.Body.SlotId.String(), httpx.RequestID(ctx))
	if st, ok := slotTaken(err); ok {
		return RescheduleAppointment409ApplicationProblemPlusJSONResponse(takenBody(st)), nil
	}
	if err != nil {
		return nil, err
	}
	return RescheduleAppointment200JSONResponse(appt(a)), nil
}

func rulesOut(in []booking.Rule) []AvailabilityRule {
	out := make([]AvailabilityRule, 0, len(in))
	for _, r := range in {
		out = append(out, AvailabilityRule{Weekday: r.Weekday, StartLocal: r.StartLocal, EndLocal: r.EndLocal, SlotMinutes: r.SlotMinutes, Mode: ConsultMode(r.Mode)})
	}
	return out
}

func exc(e booking.Exception) AvailabilityException {
	return AvailabilityException{ExceptionId: uid(e.ID), Kind: AvailabilityExceptionKind(e.Kind), StartsAt: e.StartsAt, EndsAt: e.EndsAt}
}

func conflicts(in []booking.Conflict) []Conflict {
	out := make([]Conflict, 0, len(in))
	for _, c := range in {
		out = append(out, Conflict{AppointmentId: uid(c.AppointmentID), PatientId: uid(c.PatientRef), PracticeId: uid(c.PracticeID), StartsAt: c.StartsAt,
			Mode: ConsultMode(c.Mode), State: AppointmentState(c.State)})
	}
	return out
}

func (h Handlers) GetAvailability(ctx context.Context, req GetAvailabilityRequestObject) (GetAvailabilityResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	av, err := h.M.GetAvailability(ctx, a, req.PracticeId.String())
	if err != nil {
		return nil, err
	}
	out := GetAvailability200JSONResponse{PracticeId: uid(av.PracticeID), TimeZone: av.TimeZone, Rules: rulesOut(av.Rules), Exceptions: make([]AvailabilityException, 0, len(av.Exceptions))}
	for _, e := range av.Exceptions {
		out.Exceptions = append(out.Exceptions, exc(e))
	}
	return out, nil
}

func (h Handlers) SetAvailability(ctx context.Context, req SetAvailabilityRequestObject) (SetAvailabilityResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	in := make([]booking.RuleInput, 0, len(req.Body.Rules))
	for _, r := range req.Body.Rules {
		in = append(in, booking.RuleInput{Weekday: r.Weekday, StartLocal: r.StartLocal, EndLocal: r.EndLocal, SlotMinutes: r.SlotMinutes, Mode: string(r.Mode)})
	}
	c, err := h.M.SetWeeklyTemplate(ctx, a, req.PracticeId.String(), in)
	if err != nil {
		return nil, err
	}
	return SetAvailability200JSONResponse(ConflictResult{Conflicts: conflicts(c)}), nil
}

func (h Handlers) AddAvailabilityException(ctx context.Context, req AddAvailabilityExceptionRequestObject) (AddAvailabilityExceptionResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	e, c, err := h.M.AddException(ctx, a, req.PracticeId.String(), string(req.Body.Kind), req.Body.FromDate.Format("2006-01-02"), req.Body.ToDate.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	return AddAvailabilityException201JSONResponse(ExceptionResult{Exception: exc(e), Conflicts: conflicts(c)}), nil
}

func (h Handlers) RemoveAvailabilityException(ctx context.Context, req RemoveAvailabilityExceptionRequestObject) (RemoveAvailabilityExceptionResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.RemoveException(ctx, a, req.PracticeId.String(), req.ExceptionId.String()); err != nil {
		return nil, err
	}
	return RemoveAvailabilityException204Response{}, nil
}

func (h Handlers) ListAvailabilityConflicts(ctx context.Context, req ListAvailabilityConflictsRequestObject) (ListAvailabilityConflictsResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	c, err := h.M.ListConflicts(ctx, a, req.PracticeId.String())
	if err != nil {
		return nil, err
	}
	return ListAvailabilityConflicts200JSONResponse(ConflictList{Items: conflicts(c)}), nil
}

func (h Handlers) GetPracticeDay(ctx context.Context, req GetPracticeDayRequestObject) (GetPracticeDayResponseObject, error) {
	a, err := actor(ctx)
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
	items, next, err := h.M.DayView(ctx, a, req.PracticeId.String(), req.Params.Date.Format("2006-01-02"), limit, cursor)
	if err != nil {
		return nil, err
	}
	out := GetPracticeDay200JSONResponse{Items: make([]DayItem, 0, len(items)), NextCursor: optStr(next)}
	for _, it := range items {
		out.Items = append(out.Items, DayItem{AppointmentId: uid(it.AppointmentID), PatientId: uid(it.PatientRef), StartsAt: it.StartsAt, EndsAt: it.EndsAt,
			Mode: ConsultMode(it.Mode), State: AppointmentState(it.State), NoShowBy: enumPtr[DayItemNoShowBy](it.NoShowBy),
			CancelledBy: enumPtr[DayItemCancelledBy](it.CancelledBy), NeedsReschedule: it.NeedsReschedule, Reconfirmation: enumPtr[DayItemReconfirmation](it.ReconfirmState)})
	}
	return out, nil
}

func (h Handlers) MarkAppointment(ctx context.Context, req MarkAppointmentRequestObject) (MarkAppointmentResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	st, err := h.M.Mark(ctx, a, req.AppointmentId.String(), string(req.Body.Mark))
	if err != nil {
		return nil, err
	}
	return MarkAppointment200JSONResponse(MarkResult{AppointmentId: req.AppointmentId, State: AppointmentState(st)}), nil
}

func (h Handlers) StaffCancelAppointment(ctx context.Context, req StaffCancelAppointmentRequestObject) (StaffCancelAppointmentResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.CancelByStaff(ctx, a, req.AppointmentId.String()); err != nil {
		return nil, err
	}
	return StaffCancelAppointment204Response{}, nil
}

func (h Handlers) ReconfirmAppointment(ctx context.Context, req ReconfirmAppointmentRequestObject) (ReconfirmAppointmentResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.Reconfirm(ctx, a, req.AppointmentId.String()); err != nil {
		return nil, err
	}
	return ReconfirmAppointment204Response{}, nil
}

func (h Handlers) ListReconfirmations(ctx context.Context, req ListReconfirmationsRequestObject) (ListReconfirmationsResponseObject, error) {
	a, err := actor(ctx)
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
	items, next, err := h.M.ListReconfirmations(ctx, a, req.ClinicId.String(), limit, cursor)
	if err != nil {
		return nil, err
	}
	out := ListReconfirmations200JSONResponse{Items: make([]Reconfirmation, 0, len(items)), NextCursor: optStr(next)}
	for _, r := range items {
		out.Items = append(out.Items, Reconfirmation{AppointmentId: uid(r.AppointmentID), PracticeId: uid(r.PracticeID), DoctorId: uid(r.DoctorRef),
			StartsAt: r.StartsAt, Mode: ConsultMode(r.Mode), DueAt: r.DueAt, State: ReconfirmationState(r.State)})
	}
	return out, nil
}

func closureOut(c booking.Closure) (Closure, error) {
	day, err := time.Parse("2006-01-02", c.Date)
	if err != nil {
		return Closure{}, err
	}
	out := Closure{ClosureId: uid(c.ID), ClinicId: uid(c.ClinicRef), Date: openapi_types.Date{Time: day}, Mode: ClosureMode(c.Mode), State: ClosureState(c.State),
		CancelledCount: c.CancelledCount, FlaggedCount: c.FlaggedCount, CreatedAt: c.CreatedAt, CompletedAt: c.CompletedAt}
	if c.BranchRef != "" {
		b := uid(c.BranchRef)
		out.BranchId = &b
	}
	return out, nil
}

// CloseClinic closes the caller's clinic (or one branch) for a local date (S-145).
func (h Handlers) CloseClinic(ctx context.Context, req CloseClinicRequestObject) (CloseClinicResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	in := booking.ClosureInput{Date: req.Body.Date.Format("2006-01-02"), Mode: string(req.Body.Mode)}
	if req.Body.BranchId != nil {
		in.BranchRef = req.Body.BranchId.String()
	}
	c, err := h.M.CloseClinic(ctx, a, req.ClinicId.String(), in)
	if err != nil {
		return nil, err
	}
	out, err := closureOut(c)
	if err != nil {
		return nil, err
	}
	return CloseClinic200JSONResponse(out), nil
}

// ListClinicClosures lists the caller's clinic's closures (counts only).
func (h Handlers) ListClinicClosures(ctx context.Context, req ListClinicClosuresRequestObject) (ListClinicClosuresResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	list, err := h.M.ListClosures(ctx, a, req.ClinicId.String(), limit)
	if err != nil {
		return nil, err
	}
	out := ListClinicClosures200JSONResponse{Items: make([]Closure, 0, len(list))}
	for _, c := range list {
		o, err := closureOut(c)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, o)
	}
	return out, nil
}
