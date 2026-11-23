// Package public is the payments module's interface (S-125, module-boundaries section 2).
// Booking depends on it. Payments answers booking only with events (payment.captured,
// payment.failed, refund.succeeded, refund.failed, refund.escalated), never by calling
// booking, so there is no cycle.
//
// Money rules this interface keeps: amounts are integer minor units chosen by the SERVER
// (booking), never by a client; one order per appointment and one refund per appointment
// and rule, enforced by unique operation keys; no card, UPI or bank data exists here.
package public

import (
	"context"
	"errors"
	"time"
)

// Event types emitted by payments (payload: references and codes only).
const (
	EventPaymentCaptured = "payment.captured" // {orderRef, appointmentRef}
	EventPaymentFailed   = "payment.failed"   // {orderRef, appointmentRef}
	EventRefundSucceeded = "refund.succeeded" // {refundRef, appointmentRef, ruleId, amountMinor, currency}
	EventRefundFailed    = "refund.failed"    // {refundRef, appointmentRef, ruleId, amountMinor, currency}
	EventRefundEscalated = "refund.escalated" // {refundRef, appointmentRef, ruleId, amountMinor, currency, clinicRef}: shortfall, clinic alerted
	// EventRefundRequested: a refund was decided from the stored policy (S-157 messages):
	// {refundRef, appointmentRef, ruleId, trigger, pct, amountMinor, currency}.
	EventRefundRequested = "refund.requested"
)

// Event types payments consumes from booking (payload: appointmentRef, byWhom,
// policyVersion, and where relevant rule, mode, minutesBeforeSlot).
const (
	EventAppointmentCancelled = "appointment.cancelled"
	EventAppointmentExpired   = "appointment.expired"
	EventAppointmentNoShow    = "appointment.no_show"
)

// Subscriber names for the outbox bus (one per handler, unique).
const (
	SubscriberCancelled = "payments.on_appointment_cancelled"
	SubscriberExpired   = "payments.on_appointment_expired"
	SubscriberNoShow    = "payments.on_appointment_no_show"
)

// Errors.
var (
	ErrNotFound      = errors.New("payments: not found")
	ErrInvalid       = errors.New("payments: invalid request")
	ErrConflict      = errors.New("payments: an order for this appointment exists with different details")
	ErrNoMerchant    = errors.New("payments: clinic has no active merchant link")
	ErrPayAtClinic   = errors.New("payments: clinic is pay-at-clinic; no online payment")
	ErrClinicPaused  = errors.New("payments: clinic payments are paused (unpaid refunds over 48 hours)")
	ErrOrderFailed   = errors.New("payments: the payment company refused to create the order")
	ErrSplitNotReady = errors.New("payments: split settlement is not available before v1.2")
	// ErrLinkExists: the clinic already has a merchant link; use UpdateClinicLink (review M3).
	ErrLinkExists = errors.New("payments: the clinic already has a merchant link")
)

// OrderState is the order's state.
type OrderState string

// Order states.
const (
	OrderRequested  OrderState = "requested"
	OrderProcessing OrderState = "processing"
	OrderUnknown    OrderState = "unknown" // vendor call outcome unknown; being resolved, never retried blindly
	OrderCreated    OrderState = "created" // checkout open
	OrderCaptured   OrderState = "captured"
	OrderExpired    OrderState = "expired"
	OrderFailed     OrderState = "failed"
)

// RefundState is the refund's state.
type RefundState string

// Refund states (module-boundaries: requested, processing, unknown, succeeded | failed, escalated).
const (
	RefundRequested  RefundState = "requested"
	RefundProcessing RefundState = "processing"
	RefundUnknown    RefundState = "unknown"
	RefundSucceeded  RefundState = "succeeded"
	RefundFailed     RefundState = "failed"
	RefundEscalated  RefundState = "escalated"
)

// Checkout is what the app needs to open the hosted checkout. When the vendor call
// outcome is not known yet, State is requested, processing or unknown and CheckoutURL is
// empty: poll OrderStatus (or wait for the event).
type Checkout struct {
	OrderRef        string
	ProviderOrderID string
	CheckoutURL     string
	State           OrderState
	AmountMinor     int64
	Currency        string
}

// OrderInfo is the status of an appointment's order.
type OrderInfo struct {
	OrderRef    string
	State       OrderState
	AmountMinor int64
	Currency    string
	CapturedAt  *time.Time
}

// RefundInfo is the status of an appointment's refund.
type RefundInfo struct {
	RefundRef   string // the reference number shown to the patient
	RuleID      string
	State       RefundState
	AmountMinor int64
	Currency    string
	UpdatedAt   time.Time
}

// SettlementMode of a clinic.
type SettlementMode string

// Settlement modes. Split is v1.2 and is refused.
const (
	ClinicCollected SettlementMode = "clinic_collected"
	PayAtClinic     SettlementMode = "pay_at_clinic"
)

// Service is the interface other modules use.
type Service interface {
	// CreateOrder opens a hosted-checkout order on the clinic's merchant account. It is
	// idempotent on the appointment: the same appointment returns the same order. A
	// different amount, clinic or currency for the same appointment is ErrConflict.
	CreateOrder(ctx context.Context, appointmentRef, clinicRef string, amountMinor int64, currency string, holdExpiresAt time.Time) (Checkout, error)
	// OrderStatus returns the order of an appointment, or ErrNotFound.
	OrderStatus(ctx context.Context, appointmentRef string) (OrderInfo, error)
	// RefundStatus returns the latest refund of an appointment, or ErrNotFound.
	RefundStatus(ctx context.Context, appointmentRef string) (RefundInfo, error)
	// ClinicPaymentsPaused reports whether new bookings must pause for this clinic: a refund
	// has been unpaid for lack of clinic balance for 48 hours (E-PAY-05). Fails closed.
	ClinicPaymentsPaused(ctx context.Context, clinicRef string) (bool, error)
	// SettlementMode tells booking whether the clinic takes online payment.
	SettlementMode(ctx context.Context, clinicRef string) (SettlementMode, error)
}
