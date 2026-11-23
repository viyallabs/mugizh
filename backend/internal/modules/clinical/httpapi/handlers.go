package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface for notes and prescriptions. The policy
// middleware enforces x-roles and the step-up window before these run; each handler also
// checks the acting role itself, so a wiring mistake fails closed.
type Handlers struct {
	Rx *clinical.Rx
	// H serves the doctor's view of a shared patient history (S-169). Nil refuses it (fail closed).
	H *clinical.History
}

var _ StrictServerInterface = Handlers{}

var (
	errInvalid         = problem.New(http.StatusBadRequest, "clinical.invalid", "The request is not valid")
	errNotVerified     = problem.New(http.StatusForbidden, "clinical.doctor_not_verified", "Only a verified doctor can prescribe").WithDetail("RX_DOCTOR_NOT_VERIFIED")
	errBlocked         = problem.New(http.StatusUnprocessableEntity, "clinical.rules_blocked", "The prescribing rules do not allow this prescription")
	errESignUnavail    = problem.New(http.StatusServiceUnavailable, "clinical.esign_unavailable", "Electronic signing is not available; nothing was issued")
	errESignFailed     = problem.New(http.StatusServiceUnavailable, "clinical.esign_failed", "Electronic signing did not complete; nothing was issued")
	errNoDraft         = problem.New(http.StatusConflict, "clinical.no_draft", "There is no draft to sign")
	errPatientDetails  = problem.New(http.StatusConflict, "clinical.patient_details_missing", "The patient's name and age are needed before signing")
	errAlreadySigned   = problem.New(http.StatusConflict, "clinical.already_signed", "This version is already signed; amend it to change it")
	errDraftChanged    = problem.New(http.StatusConflict, "clinical.draft_changed", "The draft changed while signing; review it and sign again")
	errOpenDraft       = problem.New(http.StatusConflict, "clinical.draft_open", "An unsigned draft already exists")
	errNothingToAmend  = problem.New(http.StatusConflict, "clinical.nothing_to_amend", "There is no signed version to amend")
	errVerifyRateLimit = problem.RateLimited
)

func mapErr(err error) error {
	var inv *clinical.InvalidError
	var de *clinical.DeniedError
	var be *clinical.BlockedError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, clinical.ErrNotFound):
		return problem.NotFound // other people's resources look the same as missing ones (K-SEC-04)
	case errors.As(err, &de):
		if de.Hide {
			return problem.NotFound
		}
		return problem.ConsentRequired
	case errors.As(err, &inv):
		return errInvalid.WithFields(problem.FieldError{Field: inv.Field, Code: inv.Code})
	case errors.Is(err, clinical.ErrDoctorNotVerified):
		return errNotVerified
	case errors.Is(err, clinical.ErrStepUp):
		return problem.StepUpRequired
	case errors.As(err, &be):
		var fs []problem.FieldError
		for _, r := range be.Result.Reasons {
			fs = append(fs, problem.FieldError{Field: "prescription", Code: r.Code})
		}
		for _, it := range be.Result.Items {
			for _, r := range it.Reasons {
				fs = append(fs, problem.FieldError{Field: "lines." + it.Ref, Code: r.Code})
			}
		}
		return errBlocked.WithFields(fs...)
	case errors.Is(err, clinical.ErrESignUnavailable):
		return errESignUnavail
	case errors.Is(err, clinical.ErrESignFailed):
		return errESignFailed
	case errors.Is(err, clinical.ErrNoDraft):
		return errNoDraft
	case errors.Is(err, clinical.ErrPatientDetails):
		return errPatientDetails
	case errors.Is(err, clinical.ErrReminderConsent):
		return problem.ConsentRequired // S-155: the app offers P-REMIND
	case errors.Is(err, clinical.ErrReminderConflict):
		return problem.New(http.StatusConflict, "clinical.reminder_conflict", "The reminder setting changed; reload it")
	case errors.Is(err, clinical.ErrAlreadySigned):
		return errAlreadySigned
	case errors.Is(err, clinical.ErrDraftChanged):
		return errDraftChanged
	case errors.Is(err, clinical.ErrOpenDraft):
		return errOpenDraft
	case errors.Is(err, clinical.ErrNothingToAmend):
		return errNothingToAmend
	}
	return err
}

func caller(ctx context.Context, role string) (clinical.Caller, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return clinical.Caller{}, problem.Unauthenticated
	}
	if p.ActingRole != role {
		return clinical.Caller{}, problem.Forbidden
	}
	return clinical.Caller{UserID: p.UserID, Role: role, ClinicRef: p.ActingClinic, StepUpAt: p.StepUpAt, Restricted: p.Restricted,
		RequestID: httpx.RequestID(ctx), IPHash: httpx.IPHash(ctx)}, nil
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func day(d *openapi_types.Date) string {
	if d == nil {
		return ""
	}
	return d.Format("2006-01-02")
}

func dose(d *Dose) clinical.DoseInput {
	if d == nil {
		return clinical.DoseInput{}
	}
	out := clinical.DoseInput{Unit: s(d.Unit)}
	if d.Quantity != nil {
		out.Amount = *d.Quantity
	}
	return out
}

func freq(f *Frequency) clinical.FrequencyInput {
	if f == nil {
		return clinical.FrequencyInput{}
	}
	return clinical.FrequencyInput{Times: i(f.Times), Period: s(f.Period)}
}

func dur(d *Duration) clinical.DurationInput {
	if d == nil {
		return clinical.DurationInput{}
	}
	return clinical.DurationInput{Value: i(d.Value), Unit: s(d.Unit)}
}

func draftIn(b *PrescriptionInput) clinical.DraftContent {
	d := clinical.DraftContent{Advice: s(b.Advice), FollowUpOn: day(b.FollowUpOn)}
	for _, it := range b.Lines {
		x := clinical.ItemInput{Ref: it.Ref, DrugRef: it.DrugRef, Context: string(it.Context), Dose: dose(it.Dose), Frequency: freq(it.Frequency),
			Duration: dur(it.Duration), Route: s(it.Route), Instructions: s(it.Instructions)}
		if it.ForNewCondition != nil {
			x.ForNewCondition = *it.ForNewCondition
		}
		if it.AsNeeded != nil {
			x.AsNeeded = *it.AsNeeded
		}
		if it.Taper != nil {
			for _, t := range *it.Taper {
				x.Taper = append(x.Taper, clinical.TaperStepInput{Dose: dose(t.Dose), Frequency: freq(t.Frequency), Duration: dur(t.Duration)})
			}
		}
		d.Items = append(d.Items, x)
	}
	if b.Tests != nil {
		for _, t := range *b.Tests {
			d.Tests = append(d.Tests, clinical.TestInput{Ref: t.Ref, Name: t.Name, DueOn: day(t.DueOn), IntervalDays: i(t.IntervalDays), Repeats: i(t.Repeats)})
		}
	}
	return d
}

func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func itemOut(it clinical.ItemInput) Item {
	out := Item{Ref: it.Ref, DrugRef: it.DrugRef, Context: ItemContext(it.Context), Dose: &Dose{Quantity: &it.Dose.Amount, Unit: ptr(it.Dose.Unit)},
		Frequency: &Frequency{Times: &it.Frequency.Times, Period: ptr(it.Frequency.Period)}, Duration: &Duration{Value: &it.Duration.Value, Unit: ptr(it.Duration.Unit)},
		Route: ptr(it.Route), ForNewCondition: ptr(it.ForNewCondition), AsNeeded: ptr(it.AsNeeded), Instructions: ptr(it.Instructions)}
	if len(it.Taper) > 0 {
		ts := make([]TaperStep, 0, len(it.Taper))
		for _, t := range it.Taper {
			ts = append(ts, TaperStep{Dose: &Dose{Quantity: &t.Dose.Amount, Unit: ptr(t.Dose.Unit)}, Frequency: &Frequency{Times: &t.Frequency.Times, Period: ptr(t.Frequency.Period)},
				Duration: &Duration{Value: &t.Duration.Value, Unit: ptr(t.Duration.Unit)}})
		}
		out.Taper = &ts
	}
	return out
}

func rxOut(v clinical.RxView) Prescription {
	out := Prescription{PrescriptionId: uuid.MustParse(v.PrescriptionRef), AppointmentId: uuid.MustParse(v.AppointmentRef), Version: v.Version,
		State: PrescriptionState(v.State), Lines: []Item{}, Tests: []TestItem{}, Advice: ptr(v.Content.Advice), FollowUpOn: ptr(v.Content.FollowUpOn),
		SignedAt: v.SignedAt, ConsultMode: ptr(v.ConsultMode), RuleSetVersion: ptr(v.RuleSetVersion), ClassificationVersion: ptr(v.ClassificationVersion)}
	if !v.UpdatedAt.IsZero() {
		u := v.UpdatedAt
		out.UpdatedAt = &u
	}
	for _, it := range v.Content.Items {
		out.Lines = append(out.Lines, itemOut(it))
	}
	for _, t := range v.Content.Tests {
		ti := TestItem{Ref: t.Ref, Name: t.Name, IntervalDays: ptr(t.IntervalDays), Repeats: ptr(t.Repeats)}
		if t.DueOn != "" {
			if d, err := time.Parse("2006-01-02", t.DueOn); err == nil {
				ti.DueOn = &openapi_types.Date{Time: d}
			}
		}
		out.Tests = append(out.Tests, ti)
	}
	if len(v.Document) > 0 {
		var m map[string]interface{}
		if json.Unmarshal(v.Document, &m) == nil {
			out.Document = &m
		}
	}
	return out
}

func noteOut(n clinical.NoteView) ConsultNote {
	out := ConsultNote{NoteId: uuid.MustParse(n.NoteRef), AppointmentId: uuid.MustParse(n.AppointmentRef), Version: n.Version, State: ConsultNoteState(n.State),
		Complaint: n.Content.Complaint, Findings: n.Content.Findings, Diagnosis: n.Content.Diagnosis, Advice: n.Content.Advice, SignedAt: n.SignedAt}
	if !n.UpdatedAt.IsZero() {
		u := n.UpdatedAt
		out.UpdatedAt = &u
	}
	return out
}

// GetConsultNote returns the doctor's newest note version.
func (h Handlers) GetConsultNote(ctx context.Context, req GetConsultNoteRequestObject) (GetConsultNoteResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	n, err := h.Rx.GetNote(ctx, c, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetConsultNote200JSONResponse(noteOut(n)), nil
}

// SaveConsultNote saves the draft note.
func (h Handlers) SaveConsultNote(ctx context.Context, req SaveConsultNoteRequestObject) (SaveConsultNoteResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	n, err := h.Rx.SaveNote(ctx, c, req.AppointmentId.String(), clinical.NoteContent{Complaint: s(req.Body.Complaint), Findings: s(req.Body.Findings),
		Diagnosis: s(req.Body.Diagnosis), Advice: s(req.Body.Advice)})
	if err != nil {
		return nil, mapErr(err)
	}
	return SaveConsultNote200JSONResponse(noteOut(n)), nil
}

// GetAppointmentPrescription returns the doctor's newest prescription version.
func (h Handlers) GetAppointmentPrescription(ctx context.Context, req GetAppointmentPrescriptionRequestObject) (GetAppointmentPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	v, err := h.Rx.GetPrescription(ctx, c, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetAppointmentPrescription200JSONResponse(rxOut(v)), nil
}

// SaveDraftPrescription replaces the draft exactly as entered.
func (h Handlers) SaveDraftPrescription(ctx context.Context, req SaveDraftPrescriptionRequestObject) (SaveDraftPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errInvalid
	}
	v, err := h.Rx.SaveDraft(ctx, c, req.AppointmentId.String(), draftIn(req.Body))
	if err != nil {
		return nil, mapErr(err)
	}
	return SaveDraftPrescription200JSONResponse(rxOut(v)), nil
}

// CheckDraftPrescription runs the rules at the draft stage.
func (h Handlers) CheckDraftPrescription(ctx context.Context, req CheckDraftPrescriptionRequestObject) (CheckDraftPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	cv, err := h.Rx.Check(ctx, c, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	r := cv.Result
	out := RuleCheck{Allowed: r.Allowed, Stage: RuleCheckStage(r.Stage), Checks: []CheckStep{}, Reasons: []Reason{}, Items: []ItemResult{},
		RuleSetVersion: ptr(r.RuleSetVersion), RuleSetChecksum: ptr(r.RuleSetChecksum), ClassificationVersion: ptr(r.DrugClassificationVersion),
		Mode: ptr(string(r.Mode)), ConsultType: ptr(string(r.ConsultType))}
	for _, ck := range r.Checks {
		st := CheckStep{Id: ck.ID, Passed: ck.Passed}
		if !ck.Passed {
			st.Reason = &Reason{Code: ck.Reason.Code, Key: ck.Reason.Key}
		}
		out.Checks = append(out.Checks, st)
	}
	for _, x := range r.Reasons {
		out.Reasons = append(out.Reasons, Reason{Code: x.Code, Key: x.Key})
	}
	for _, it := range r.Items {
		ir := ItemResult{Ref: it.Ref, DrugRef: ptr(it.DrugRef), Allowed: it.Allowed, Reasons: []Reason{}}
		for _, x := range it.Reasons {
			ir.Reasons = append(ir.Reasons, Reason{Code: x.Code, Key: x.Key})
		}
		out.Items = append(out.Items, ir)
	}
	return CheckDraftPrescription200JSONResponse(out), nil
}

// SignPrescription signs and issues. x-step-up is enforced by the policy and again by the service.
func (h Handlers) SignPrescription(ctx context.Context, req SignPrescriptionRequestObject) (SignPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	res, err := h.Rx.Sign(ctx, c, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return SignPrescription200JSONResponse{PrescriptionId: uuid.MustParse(res.PrescriptionRef), Version: res.Version, SignedAt: res.SignedAt,
		VerificationReference: res.VerificationReference}, nil
}

// AmendPrescription opens a new draft version.
func (h Handlers) AmendPrescription(ctx context.Context, req AmendPrescriptionRequestObject) (AmendPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RoleDoctor)
	if err != nil {
		return nil, err
	}
	v, err := h.Rx.Amend(ctx, c, req.PrescriptionId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return AmendPrescription200JSONResponse(rxOut(v)), nil
}

// ListMyPrescriptions lists the patient's signed prescriptions.
func (h Handlers) ListMyPrescriptions(ctx context.Context, req ListMyPrescriptionsRequestObject) (ListMyPrescriptionsResponseObject, error) {
	c, err := caller(ctx, ip.RolePatient)
	if err != nil {
		return nil, err
	}
	limit, cursor := 20, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	list, next, err := h.Rx.PatientPrescriptions(ctx, c, limit, cursor)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListMyPrescriptions200JSONResponse{Items: []PrescriptionSummary{}, NextCursor: ptr(next)}
	for _, x := range list {
		out.Items = append(out.Items, PrescriptionSummary{PrescriptionId: uuid.MustParse(x.PrescriptionRef), AppointmentId: uuid.MustParse(x.AppointmentRef),
			Version: x.Version, SignedAt: x.SignedAt})
	}
	return out, nil
}

// GetMyPrescription returns one of the patient's signed prescriptions.
func (h Handlers) GetMyPrescription(ctx context.Context, req GetMyPrescriptionRequestObject) (GetMyPrescriptionResponseObject, error) {
	c, err := caller(ctx, ip.RolePatient)
	if err != nil {
		return nil, err
	}
	v, err := h.Rx.PatientPrescription(ctx, c, req.PrescriptionId.String(), i(req.Params.Version))
	if err != nil {
		return nil, mapErr(err)
	}
	return GetMyPrescription200JSONResponse(rxOut(v)), nil
}

// GetMyConsultNote returns the patient's signed note.
func (h Handlers) GetMyConsultNote(ctx context.Context, req GetMyConsultNoteRequestObject) (GetMyConsultNoteResponseObject, error) {
	c, err := caller(ctx, ip.RolePatient)
	if err != nil {
		return nil, err
	}
	n, err := h.Rx.PatientNote(ctx, c, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetMyConsultNote200JSONResponse(noteOut(n)), nil
}

// VerifyPrescription is the public verification page. It is rate-limited per IP and shows
// no patient data; an unknown reference answers like an invalid one.
func (h Handlers) VerifyPrescription(ctx context.Context, req VerifyPrescriptionRequestObject) (VerifyPrescriptionResponseObject, error) {
	if ok, wait := h.Rx.AllowVerify(httpx.ClientIP(ctx)); !ok {
		p := *errVerifyRateLimit
		p.RetryAfter = int(wait.Seconds()) + 1
		return nil, &p
	}
	v := h.Rx.VerifyReference(ctx, req.Reference)
	if !v.Valid {
		return VerifyPrescription200JSONResponse{Status: NotValid}, nil
	}
	t := v.SignedAt
	return VerifyPrescription200JSONResponse{Status: Valid, SignedAt: &t, RegistrationNumber: ptr(v.RegistrationNumber)}, nil
}

// ListMyPrescriptionReminders returns each item's reminder switch (S-155).
func (h Handlers) ListMyPrescriptionReminders(ctx context.Context, req ListMyPrescriptionRemindersRequestObject) (ListMyPrescriptionRemindersResponseObject, error) {
	c, err := caller(ctx, ip.RolePatient)
	if err != nil {
		return nil, err
	}
	items, err := h.Rx.ItemReminders(ctx, c, req.PrescriptionId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListMyPrescriptionReminders200JSONResponse{Reminders: []ItemReminder{}}
	for _, it := range items {
		out.Reminders = append(out.Reminders, ItemReminder{ItemRef: it.ItemRef, Kind: ItemReminderKind(it.Kind), Enabled: it.Enabled, Active: it.Active})
	}
	return out, nil
}

// SetMyPrescriptionReminder turns one item's reminders off or on again (S-155).
func (h Handlers) SetMyPrescriptionReminder(ctx context.Context, req SetMyPrescriptionReminderRequestObject) (SetMyPrescriptionReminderResponseObject, error) {
	c, err := caller(ctx, ip.RolePatient)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.Rx.SetItemReminders(ctx, c, req.PrescriptionId.String(), req.ItemRef, req.Body.Enabled); err != nil {
		return nil, mapErr(err)
	}
	return SetMyPrescriptionReminder204Response{}, nil
}
