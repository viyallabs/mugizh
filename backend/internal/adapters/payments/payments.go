// Package payments is the payments adapter port (adapter-interfaces §4, K-ARC-03, K-PAY-01,
// K-PAY-02). Orders and refunds are made on the CLINIC's own merchant account (mode B,
// K-PAY-03). Hosted checkout only: no card, UPI or bank data ever passes through this
// port, so the types below have no field that could carry it.
//
// Error behaviour (common model in package adapters):
//   - Timeout means the outcome is UNKNOWN. After a Timeout on CreateOrder or Refund the
//     caller resolves with FindOrder / FindRefund and never retries blindly.
//   - Rejected(INSUFFICIENT_BALANCE) on Refund means the clinic's balance is too low.
//
// Vendor implementations live in payments/<vendor>/ and are imported only by the
// composition root. The vendor's secrets are resolved by the adapter from the secrets
// manager using the clinic link reference; they never pass through the module.
package payments

import (
	"context"
	"net/http"
	"time"
)

// Rejection reasons the adapter reports in adapters.Error.Reason.
const (
	ReasonInsufficientBalance = "INSUFFICIENT_BALANCE"
)

// Money is an amount in minor units plus an ISO 4217 code (taken from the country pack).
type Money struct {
	Minor    int64
	Currency string
}

// OrderStatus is the vendor-side state of an order.
type OrderStatus string

// Order states.
const (
	OrderCreated    OrderStatus = "created"
	OrderAuthorised OrderStatus = "authorised"
	OrderCaptured   OrderStatus = "captured"
	OrderFailed     OrderStatus = "failed"
)

// CreateOrderRequest creates a hosted-checkout order on the clinic's account.
type CreateOrderRequest struct {
	ClinicLinkRef    string
	Amount           Money
	IdempotencyKey   string // our operation key, stable across retries
	MerchantOrderRef string // our order ID, sent as the vendor's receipt/reference field
	AppointmentRef   string
}

// Order is the vendor's view of an order.
type Order struct {
	ProviderOrderID  string
	MerchantOrderRef string
	Status           OrderStatus
	Amount           Money
	// CheckoutURL is the hosted checkout page the app opens. It carries no card data.
	CheckoutURL string
}

// PaymentStatus is the polling answer for an order (webhook-late fallback, K-PAY-02).
type PaymentStatus struct {
	Status     OrderStatus
	PaymentID  string // set when a payment was captured
	Amount     Money
	CapturedAt time.Time
}

// RefundStatus is the vendor-side state of a refund.
type RefundStatus string

// Refund states.
const (
	RefundPending   RefundStatus = "pending"
	RefundProcessed RefundStatus = "processed"
	RefundFailed    RefundStatus = "failed"
)

// RefundRequest asks the vendor to refund a captured payment from the clinic's account.
type RefundRequest struct {
	ClinicLinkRef     string
	PaymentID         string
	Amount            Money
	IdempotencyKey    string // our operation key; the SAME key is reused on every retry
	MerchantRefundRef string // our refund ID, sent in the vendor's reference/notes field
	ReasonCode        string // policy rule ID, for example RF03
}

// Refund is the vendor's view of a refund.
type Refund struct {
	ProviderRefundID  string
	MerchantRefundRef string
	PaymentID         string
	Status            RefundStatus
	Amount            Money
	ProcessedAt       time.Time
}

// EventType is a verified webhook event type.
type EventType string

// Webhook event types.
const (
	EventPaymentCaptured EventType = "payment.captured"
	EventPaymentFailed   EventType = "payment.failed"
	EventRefundSucceeded EventType = "refund.succeeded"
	EventRefundFailed    EventType = "refund.failed"
)

// Event is a signature-verified webhook event, parsed. Only references and amounts.
type Event struct {
	ID                string // the vendor's event ID, the de-duplication key
	Type              EventType
	ProviderOrderID   string
	MerchantOrderRef  string
	PaymentID         string
	ProviderRefundID  string
	MerchantRefundRef string
	Amount            Money
	OccurredAt        time.Time
}

// SettlementKind says what a settlement line is.
type SettlementKind string

// Settlement line kinds.
const (
	SettlementCapture SettlementKind = "capture"
	SettlementRefund  SettlementKind = "refund"
)

// SettlementLine is one line of a vendor settlement report.
type SettlementLine struct {
	Kind        SettlementKind
	PaymentID   string
	MerchantRef string // order ref for captures, refund ref for refunds
	Amount      Money
	OccurredAt  time.Time
}

// Port is the payments adapter. Every method has a timeout set by the implementation
// (CreateOrder 5 s, FindOrder 3 s, GetPaymentStatus 3 s, Refund 5 s, GetRefundStatus 3 s,
// FindRefund 3 s, FetchSettlementReport 30 s; adapter-interfaces §4).
type Port interface {
	// CreateOrder is idempotent on IdempotencyKey. On Timeout call FindOrder.
	CreateOrder(ctx context.Context, r CreateOrderRequest) (Order, error)
	// FindOrder returns the order for our merchant reference, or NotFound.
	FindOrder(ctx context.Context, clinicLinkRef, merchantOrderRef string) (Order, error)
	// GetPaymentStatus is the polling fallback.
	GetPaymentStatus(ctx context.Context, clinicLinkRef, providerOrderID string) (PaymentStatus, error)
	// Refund is idempotent on IdempotencyKey. On Timeout call FindRefund, never Refund.
	Refund(ctx context.Context, r RefundRequest) (Refund, error)
	GetRefundStatus(ctx context.Context, clinicLinkRef, providerRefundID string) (Refund, error)
	// FindRefund lists the payment's refunds and matches our merchant reference, or NotFound.
	FindRefund(ctx context.Context, clinicLinkRef, paymentID, merchantRefundRef string) (Refund, error)
	// VerifyWebhook checks the signature (constant time, per-clinic secret) BEFORE anything
	// else and returns the parsed event, or an Invalid error.
	VerifyWebhook(ctx context.Context, clinicLinkRef string, headers http.Header, rawBody []byte) (Event, error)
	// FetchSettlementReport returns the report lines for one UTC day.
	FetchSettlementReport(ctx context.Context, clinicLinkRef string, day time.Time) ([]SettlementLine, error)
}
