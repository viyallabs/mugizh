// Package httpapi implements the payments OpenAPI operations (api/core/payments.v1.yaml).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// AppointmentOwners is the consumer-side port that tells payments whose appointment it is.
// Payments holds no patient reference (data minimisation), and booking depends on payments,
// so the composition root implements this with booking's public interface.
type AppointmentOwners interface {
	// PatientOf returns the patient of an appointment. found is false when it does not exist.
	PatientOf(ctx context.Context, appointmentRef string) (patientRef string, found bool, err error)
}

// Handlers implements StrictServerInterface.
type Handlers struct {
	M      *payments.Module
	Owners AppointmentOwners
}

var _ StrictServerInterface = Handlers{}

func principal(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	return p, nil
}

// ownAppointment returns nil only when the caller is the appointment's patient; anything
// else, including a missing appointment, is a 404 (K-SEC-04).
func (h Handlers) ownAppointment(ctx context.Context, appt uuid.UUID) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	if p.ActingRole != ip.RolePatient {
		return problem.Forbidden
	}
	patient, found, err := h.Owners.PatientOf(ctx, appt.String())
	if err != nil {
		return err
	}
	if !found || patient != p.UserID {
		return problem.NotFound
	}
	return nil
}

// ownClinic returns the principal only when it holds clinic_admin for exactly this clinic.
func ownClinic(ctx context.Context, clinic uuid.UUID) (*ip.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.ActingRole != ip.RoleClinicAdmin {
		return nil, problem.Forbidden
	}
	// Review F6: the clinic in the path must be the clinic the caller is acting for (as booking and
	// directory require), and the caller must hold the admin role there.
	if p.ActingClinic != clinic.String() {
		return nil, problem.NotFound
	}
	for _, g := range p.Roles {
		if g.Role == ip.RoleClinicAdmin && g.ClinicRef == clinic.String() {
			return p, nil
		}
	}
	return nil, problem.NotFound
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, rp.ErrNotFound):
		return problem.NotFound
	case errors.Is(err, payments.ErrDocumentsOff):
		return errDocumentsOff
	case errors.Is(err, rp.ErrInvalid):
		return problem.BadRequest
	}
	return err
}

var errDocumentsOff = problem.New(http.StatusServiceUnavailable, "payments.documents_unavailable", "Payment documents are not available yet")

func payState(s rp.OrderState) PaymentState {
	switch s {
	case rp.OrderCaptured:
		return PaymentStatePaid
	case rp.OrderExpired:
		return PaymentStateExpired
	case rp.OrderFailed:
		return PaymentStateFailed
	}
	return PaymentStatePending
}

func refundState(s rp.RefundState) RefundPatientState {
	switch s {
	case rp.RefundSucceeded:
		return RefundPatientStateSucceeded
	case rp.RefundEscalated:
		return RefundPatientStateDelayed
	case rp.RefundFailed:
		return RefundPatientStateFailed
	}
	return RefundPatientStateProcessing
}

// GetMyAppointmentPayment returns the payment status of the caller's appointment.
func (h Handlers) GetMyAppointmentPayment(ctx context.Context, req GetMyAppointmentPaymentRequestObject) (GetMyAppointmentPaymentResponseObject, error) {
	if err := h.ownAppointment(ctx, req.AppointmentId); err != nil {
		return nil, err
	}
	o, err := h.M.OrderStatus(ctx, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetMyAppointmentPayment200JSONResponse{OrderId: uuid.MustParse(o.OrderRef), Status: payState(o.State), Amount: o.AmountMinor, Currency: o.Currency, PaidAt: o.CapturedAt}, nil
}

// GetMyAppointmentRefund returns the refund status of the caller's appointment.
func (h Handlers) GetMyAppointmentRefund(ctx context.Context, req GetMyAppointmentRefundRequestObject) (GetMyAppointmentRefundResponseObject, error) {
	if err := h.ownAppointment(ctx, req.AppointmentId); err != nil {
		return nil, err
	}
	r, err := h.M.RefundStatus(ctx, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetMyAppointmentRefund200JSONResponse{RefundId: uuid.MustParse(r.RefundRef), Status: refundState(r.State), Amount: r.AmountMinor,
		Currency: r.Currency, RuleId: r.RuleID, UpdatedAt: r.UpdatedAt}, nil
}

// ListClinicPayments lists the caller's clinic payments (no patient identity).
func (h Handlers) ListClinicPayments(ctx context.Context, req ListClinicPaymentsRequestObject) (ListClinicPaymentsResponseObject, error) {
	p, err := ownClinic(ctx, req.ClinicId)
	if err != nil {
		return nil, err
	}
	limit, cursor := 20, ""
	if req.Params.Limit != nil {
		limit = int(*req.Params.Limit)
	}
	if req.Params.Cursor != nil {
		cursor = string(*req.Params.Cursor)
	}
	who := payments.ClinicReader{UserRef: p.UserID, Role: p.ActingRole, RequestID: httpx.RequestID(ctx)}
	rows, next, err := h.M.ClinicPayments(ctx, who, req.ClinicId.String(), limit, cursor)
	if err != nil {
		return nil, mapErr(err)
	}
	out := ListClinicPayments200JSONResponse{Items: []ClinicPayment{}}
	for _, r := range rows {
		c := ClinicPayment{OrderId: uuid.MustParse(r.OrderRef), Status: payState(r.State), Amount: r.AmountMinor, Currency: r.Currency, CreatedAt: r.CreatedAt}
		if r.RefundRef != "" {
			id, st, amt := uuid.MustParse(r.RefundRef), refundState(r.RefundState), r.RefundAmount
			c.RefundId, c.RefundStatus, c.RefundAmount = &id, &st, &amt
		}
		out.Items = append(out.Items, c)
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// GetClinicPaymentsStatus returns the pause flag for the caller's clinic.
func (h Handlers) GetClinicPaymentsStatus(ctx context.Context, req GetClinicPaymentsStatusRequestObject) (GetClinicPaymentsStatusResponseObject, error) {
	if _, err := ownClinic(ctx, req.ClinicId); err != nil {
		return nil, err
	}
	paused, since, delayed, err := h.M.ClinicStatus(ctx, req.ClinicId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return GetClinicPaymentsStatus200JSONResponse{Paused: paused, PausedSince: since, DelayedRefunds: delayed}, nil
}

// RetryClinicRefund brings a shortfall refund's next retry forward.
func (h Handlers) RetryClinicRefund(ctx context.Context, req RetryClinicRefundRequestObject) (RetryClinicRefundResponseObject, error) {
	if _, err := ownClinic(ctx, req.ClinicId); err != nil {
		return nil, err
	}
	if err := h.M.RetryRefundNow(ctx, req.ClinicId.String(), req.RefundId.String()); err != nil {
		return nil, mapErr(err)
	}
	return RetryClinicRefund202Response{}, nil
}

// GetMyPaymentDocument returns the receipt or bill of supply of the caller's appointment (S-134).
func (h Handlers) GetMyPaymentDocument(ctx context.Context, req GetMyPaymentDocumentRequestObject) (GetMyPaymentDocumentResponseObject, error) {
	if err := h.ownAppointment(ctx, req.AppointmentId); err != nil {
		return nil, err
	}
	d, err := h.M.DocumentFor(ctx, req.AppointmentId.String())
	if err != nil {
		return nil, mapErr(err)
	}
	day, err := time.Parse(time.DateOnly, d.IssuedOn)
	if err != nil {
		return nil, err
	}
	out := GetMyPaymentDocument200JSONResponse{DocumentId: uuid.MustParse(d.DocRef), AppointmentId: uuid.MustParse(d.AppointmentRef), DocumentType: d.DocType,
		Number: d.Number, IssuedOn: openapi_types.Date{Time: day}, ClinicLegalName: d.ClinicLegalName, ClinicAddress: d.ClinicAddress,
		Amount: d.AmountMinor, Currency: d.Currency, PaymentReference: d.PaymentRef}
	if d.ClinicTaxID != "" {
		tax := d.ClinicTaxID
		out.ClinicTaxId = &tax
	}
	return out, nil
}

// PutClinicDocumentProfile records the clinic's document profile (S-134).
func (h Handlers) PutClinicDocumentProfile(ctx context.Context, req PutClinicDocumentProfileRequestObject) (PutClinicDocumentProfileResponseObject, error) {
	p, err := ownClinic(ctx, req.ClinicId)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	tax := ""
	if req.Body.TaxId != nil {
		tax = *req.Body.TaxId
	}
	who := payments.ClinicReader{UserRef: p.UserID, Role: p.ActingRole, RequestID: httpx.RequestID(ctx)}
	if err := h.M.SetDocumentProfile(ctx, who, req.ClinicId.String(), payments.DocumentProfile{LegalName: req.Body.LegalName, Address: req.Body.Address,
		TaxStatus: string(req.Body.TaxStatus), TaxID: tax}); err != nil {
		return nil, mapErr(err)
	}
	return PutClinicDocumentProfile204Response{}, nil
}
