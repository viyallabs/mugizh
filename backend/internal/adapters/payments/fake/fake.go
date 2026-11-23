// Package fake is a realistic in-memory payments aggregator for tests and CI (A10,
// adapter-interfaces §15). It behaves like a hosted-checkout aggregator that works on
// each clinic's own merchant account:
//
//   - webhooks are signed with HMAC-SHA-256 over the raw body using a per-clinic secret;
//   - idempotency keys are honoured (and can be switched off, to prove that callers never
//     depend on it after a Timeout);
//   - fault injection: timeouts that still apply the effect, timeouts that do not,
//     5xx, rate limits, insufficient clinic balance on refund, duplicate and
//     out-of-order webhook deliveries (the test re-orders or repeats Deliveries);
//   - it never sees or returns card, UPI or bank data (K-PAY-01).
//
// It is concurrency safe. Time is injected so tests control settle delays.
package fake

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
)

// SignatureHeader carries the hex HMAC-SHA-256 of the raw body.
const SignatureHeader = "X-Fake-Signature"

// Op names an adapter operation for fault injection.
type Op string

// Operations.
const (
	OpCreateOrder      Op = "create_order"
	OpFindOrder        Op = "find_order"
	OpGetPaymentStatus Op = "get_payment_status"
	OpRefund           Op = "refund"
	OpGetRefundStatus  Op = "get_refund_status"
	OpFindRefund       Op = "find_refund"
	OpFetchSettlement  Op = "fetch_settlement"
)

// Fault is a one-shot injected failure.
type Fault int

// Faults.
const (
	// TimeoutApplied applies the effect, then answers Timeout (the dangerous one).
	TimeoutApplied Fault = iota + 1
	// TimeoutNotApplied answers Timeout without applying anything.
	TimeoutNotApplied
	// Unavailable is a 5xx: nothing applied.
	Unavailable
	// RateLimited is a 429: nothing applied.
	RateLimited
	// InsufficientBalance (refund only) rejects as the clinic balance is too low.
	InsufficientBalance
)

// Delivery is one webhook the aggregator would POST to us.
type Delivery struct {
	ClinicLinkRef string
	Headers       http.Header
	Body          []byte
}

type clinic struct {
	secret  string
	balance int64
}

type order struct {
	payments.Order
	link       string
	idemKey    string
	appt       string
	paymentID  string
	capturedAt time.Time
	createdAt  time.Time
}

type pay struct {
	id         string
	orderID    string
	link       string
	amount     payments.Money
	capturedAt time.Time
	refunded   int64
}

type refund struct {
	payments.Refund
	link      string
	idemKey   string
	createdAt time.Time
}

// Fake is the fake aggregator.
type Fake struct {
	mu sync.Mutex

	now func() time.Time
	// IgnoreIdempotency makes the vendor forget idempotency keys, like a vendor that does
	// not support them: a blind retry would then apply twice.
	IgnoreIdempotency bool
	findLag           time.Duration

	clinics    map[string]*clinic
	orders     map[string]*order // by provider order ID
	orderIdem  map[string]string
	pays       map[string]*pay
	refunds    map[string]*refund // by provider refund ID
	refundIdem map[string]string
	faults     map[Op][]Fault
	deliveries []Delivery
	adjust     func(link string, lines []payments.SettlementLine) []payments.SettlementLine
	seq        int
	calls      map[Op]int
}

var _ payments.Port = (*Fake)(nil)

// New returns an empty fake using the real clock.
func New() *Fake {
	return &Fake{now: time.Now, clinics: map[string]*clinic{}, orders: map[string]*order{}, orderIdem: map[string]string{},
		pays: map[string]*pay{}, refunds: map[string]*refund{}, refundIdem: map[string]string{}, faults: map[Op][]Fault{}, calls: map[Op]int{}}
}

// SetClock replaces the clock.
func (f *Fake) SetClock(now func() time.Time) { f.mu.Lock(); f.now = now; f.mu.Unlock() }

// SetFindLag makes FindOrder and FindRefund miss anything younger than d (the vendor's
// settle time), so tests can exercise "NotFound before the settle time".
func (f *Fake) SetFindLag(d time.Duration) { f.mu.Lock(); f.findLag = d; f.mu.Unlock() }

// RegisterClinic adds a clinic merchant account with a webhook secret and a balance.
func (f *Fake) RegisterClinic(linkRef, webhookSecret string, balanceMinor int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clinics[linkRef] = &clinic{secret: webhookSecret, balance: balanceMinor}
}

// SetBalance sets a clinic's available balance.
func (f *Fake) SetBalance(linkRef string, minor int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clinics[linkRef].balance = minor
}

// Balance returns a clinic's balance.
func (f *Fake) Balance(linkRef string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clinics[linkRef].balance
}

// Inject queues one-shot faults for op; each call to op consumes one.
func (f *Fake) Inject(op Op, faults ...Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults[op] = append(f.faults[op], faults...)
}

// Calls returns how many times op was called.
func (f *Fake) Calls(op Op) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[op] }

func (f *Fake) take(op Op) (Fault, bool) {
	f.calls[op]++
	q := f.faults[op]
	if len(q) == 0 {
		return 0, false
	}
	f.faults[op] = q[1:]
	return q[0], true
}

func faultErr(ft Fault) error {
	switch ft {
	case TimeoutApplied, TimeoutNotApplied:
		return adapters.E(adapters.Timeout, "VENDOR_TIMEOUT")
	case Unavailable:
		return adapters.E(adapters.Unavailable, "VENDOR_5XX")
	case RateLimited:
		return adapters.E(adapters.RateLimited, "VENDOR_429")
	case InsufficientBalance:
		return adapters.E(adapters.Rejected, payments.ReasonInsufficientBalance)
	}
	return nil
}

// ---- inspection (tests) ----

// OrderCount is the number of orders the vendor holds for a merchant reference.
func (f *Fake) OrderCount(merchantRef string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.orders {
		if o.MerchantOrderRef == merchantRef {
			n++
		}
	}
	return n
}

// RefundCount is the number of refunds the vendor holds for a merchant refund reference.
func (f *Fake) RefundCount(merchantRefundRef string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.refunds {
		if r.MerchantRefundRef == merchantRefundRef {
			n++
		}
	}
	return n
}

// TotalRefunded is the sum of non-failed refunds against a payment, in minor units.
func (f *Fake) TotalRefunded(paymentID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.pays[paymentID]; ok {
		return p.refunded
	}
	return 0
}

// ProviderOrderID returns the vendor order ID for a merchant reference.
func (f *Fake) ProviderOrderID(merchantRef string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, o := range f.orders {
		if o.MerchantOrderRef == merchantRef {
			return id, true
		}
	}
	return "", false
}

// ProviderRefundID returns the vendor refund ID for a merchant refund reference.
func (f *Fake) ProviderRefundID(merchantRefundRef string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.refunds {
		if r.MerchantRefundRef == merchantRefundRef {
			return id, true
		}
	}
	return "", false
}

// ---- simulating the patient and the vendor's back office ----

// PayOrder simulates the patient paying at the hosted checkout page. paymentID is the
// vendor's payment ID. It queues a signed payment.captured webhook. Paying an order that
// already has a payment with a different ID simulates a duplicate payment.
func (f *Fake) PayOrder(providerOrderID, paymentID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[providerOrderID]
	if !ok {
		return fmt.Errorf("fake: unknown order")
	}
	if _, exists := f.pays[paymentID]; !exists { // re-announcing a payment moves no money
		f.pays[paymentID] = &pay{id: paymentID, orderID: providerOrderID, link: o.link, amount: o.Amount, capturedAt: at}
		f.clinics[o.link].balance += o.Amount.Minor
	}
	if o.paymentID == "" {
		o.paymentID, o.capturedAt, o.Status = paymentID, at, payments.OrderCaptured
	}
	f.queueEvent(o.link, payments.Event{Type: payments.EventPaymentCaptured, ProviderOrderID: providerOrderID,
		MerchantOrderRef: o.MerchantOrderRef, PaymentID: paymentID, Amount: o.Amount, OccurredAt: at})
	return nil
}

// FailPayment simulates a failed payment attempt and queues payment.failed.
func (f *Fake) FailPayment(providerOrderID, paymentID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[providerOrderID]
	if !ok {
		return fmt.Errorf("fake: unknown order")
	}
	f.queueEvent(o.link, payments.Event{Type: payments.EventPaymentFailed, ProviderOrderID: providerOrderID,
		MerchantOrderRef: o.MerchantOrderRef, PaymentID: paymentID, Amount: o.Amount, OccurredAt: at})
	return nil
}

// SettleRefunds marks every pending refund processed and queues refund.succeeded webhooks.
func (f *Fake) SettleRefunds(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.refundIDs() {
		r := f.refunds[id]
		if r.Status != payments.RefundPending {
			continue
		}
		r.Status, r.ProcessedAt = payments.RefundProcessed, at
		f.queueEvent(r.link, payments.Event{Type: payments.EventRefundSucceeded, ProviderRefundID: id,
			MerchantRefundRef: r.MerchantRefundRef, PaymentID: r.PaymentID, Amount: r.Amount, OccurredAt: at})
	}
}

// FailRefund marks a pending refund failed (money returns to the clinic) and queues refund.failed.
func (f *Fake) FailRefund(providerRefundID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refunds[providerRefundID]
	if !ok || r.Status != payments.RefundPending {
		return fmt.Errorf("fake: no pending refund")
	}
	r.Status = payments.RefundFailed
	f.pays[r.PaymentID].refunded -= r.Amount.Minor
	f.clinics[r.link].balance += r.Amount.Minor
	f.queueEvent(r.link, payments.Event{Type: payments.EventRefundFailed, ProviderRefundID: providerRefundID,
		MerchantRefundRef: r.MerchantRefundRef, PaymentID: r.PaymentID, Amount: r.Amount, OccurredAt: at})
	return nil
}

func (f *Fake) refundIDs() []string {
	ids := make([]string, 0, len(f.refunds))
	for id := range f.refunds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DrainDeliveries returns and clears the queued webhooks, in the order they were made.
func (f *Fake) DrainDeliveries() []Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.deliveries
	f.deliveries = nil
	return d
}

// AdjustReport lets a test alter the settlement report to create mismatches.
func (f *Fake) AdjustReport(fn func(link string, lines []payments.SettlementLine) []payments.SettlementLine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adjust = fn
}

type wireEvent struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	ProviderOrderID   string `json:"providerOrderId,omitempty"`
	MerchantOrderRef  string `json:"merchantOrderRef,omitempty"`
	PaymentID         string `json:"paymentId,omitempty"`
	ProviderRefundID  string `json:"providerRefundId,omitempty"`
	MerchantRefundRef string `json:"merchantRefundRef,omitempty"`
	AmountMinor       int64  `json:"amountMinor"`
	Currency          string `json:"currency"`
	OccurredAt        string `json:"occurredAt"`
}

// Sign returns the signature header value for body under the clinic's secret.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// SignedDelivery builds a correctly signed delivery for an arbitrary event (tests use it
// to forge events with wrong amounts, or to repeat an event ID).
func (f *Fake) SignedDelivery(linkRef string, e payments.Event) Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.build(linkRef, e)
}

func (f *Fake) queueEvent(link string, e payments.Event) {
	f.seq++
	e.ID = fmt.Sprintf("evt_fake_%06d", f.seq)
	f.deliveries = append(f.deliveries, f.build(link, e))
}

func (f *Fake) build(link string, e payments.Event) Delivery {
	w := wireEvent{ID: e.ID, Type: string(e.Type), ProviderOrderID: e.ProviderOrderID, MerchantOrderRef: e.MerchantOrderRef,
		PaymentID: e.PaymentID, ProviderRefundID: e.ProviderRefundID, MerchantRefundRef: e.MerchantRefundRef,
		AmountMinor: e.Amount.Minor, Currency: e.Amount.Currency, OccurredAt: e.OccurredAt.UTC().Format(time.RFC3339Nano)}
	body, _ := json.Marshal(w)
	secret := ""
	if c, ok := f.clinics[link]; ok {
		secret = c.secret
	}
	h := http.Header{}
	h.Set(SignatureHeader, Sign(secret, body))
	h.Set("Content-Type", "application/json")
	return Delivery{ClinicLinkRef: link, Headers: h, Body: body}
}

// ---- payments.Port ----

func (f *Fake) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s_fake_%06d", prefix, f.seq)
}

// CreateOrder implements payments.Port.
func (f *Fake) CreateOrder(_ context.Context, r payments.CreateOrderRequest) (payments.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ft, faulted := f.take(OpCreateOrder)
	if faulted && ft != TimeoutApplied {
		return payments.Order{}, faultErr(ft)
	}
	if _, ok := f.clinics[r.ClinicLinkRef]; !ok {
		return payments.Order{}, adapters.E(adapters.AuthFailed, "UNKNOWN_ACCOUNT")
	}
	if r.Amount.Minor <= 0 || r.Amount.Currency == "" || r.MerchantOrderRef == "" || r.IdempotencyKey == "" {
		return payments.Order{}, adapters.E(adapters.Invalid, "BAD_ORDER")
	}
	var o *order
	if id, ok := f.orderIdem[r.IdempotencyKey]; ok && !f.IgnoreIdempotency {
		o = f.orders[id]
	} else {
		id := f.nextID("order")
		o = &order{Order: payments.Order{ProviderOrderID: id, MerchantOrderRef: r.MerchantOrderRef, Status: payments.OrderCreated,
			Amount: r.Amount, CheckoutURL: "https://checkout.fake.invalid/pay/" + id}, link: r.ClinicLinkRef, idemKey: r.IdempotencyKey,
			appt: r.AppointmentRef, createdAt: f.now()}
		f.orders[id] = o
		f.orderIdem[r.IdempotencyKey] = id
	}
	if faulted && ft == TimeoutApplied {
		return payments.Order{}, faultErr(ft)
	}
	return o.Order, nil
}

func (f *Fake) visible(created time.Time) bool {
	return f.findLag <= 0 || !f.now().Before(created.Add(f.findLag))
}

// FindOrder implements payments.Port.
func (f *Fake) FindOrder(_ context.Context, link, merchantRef string) (payments.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.take(OpFindOrder); ok {
		return payments.Order{}, faultErr(ft)
	}
	for _, o := range f.orders {
		if o.link == link && o.MerchantOrderRef == merchantRef && f.visible(o.createdAt) {
			return o.Order, nil
		}
	}
	return payments.Order{}, adapters.E(adapters.NotFound, "ORDER")
}

// GetPaymentStatus implements payments.Port.
func (f *Fake) GetPaymentStatus(_ context.Context, link, providerOrderID string) (payments.PaymentStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.take(OpGetPaymentStatus); ok {
		return payments.PaymentStatus{}, faultErr(ft)
	}
	o, ok := f.orders[providerOrderID]
	if !ok || o.link != link {
		return payments.PaymentStatus{}, adapters.E(adapters.NotFound, "ORDER")
	}
	return payments.PaymentStatus{Status: o.Status, PaymentID: o.paymentID, Amount: o.Amount, CapturedAt: o.capturedAt}, nil
}

// Refund implements payments.Port.
func (f *Fake) Refund(_ context.Context, r payments.RefundRequest) (payments.Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ft, faulted := f.take(OpRefund)
	if faulted && ft != TimeoutApplied {
		return payments.Refund{}, faultErr(ft)
	}
	c, ok := f.clinics[r.ClinicLinkRef]
	if !ok {
		return payments.Refund{}, adapters.E(adapters.AuthFailed, "UNKNOWN_ACCOUNT")
	}
	p, ok := f.pays[r.PaymentID]
	if !ok || p.link != r.ClinicLinkRef || r.IdempotencyKey == "" || r.MerchantRefundRef == "" || r.Amount.Minor <= 0 || r.Amount.Currency != p.amount.Currency {
		return payments.Refund{}, adapters.E(adapters.Invalid, "BAD_REFUND")
	}
	var rf *refund
	if id, ok := f.refundIdem[r.IdempotencyKey]; ok && !f.IgnoreIdempotency {
		rf = f.refunds[id]
	} else {
		if p.refunded+r.Amount.Minor > p.amount.Minor {
			return payments.Refund{}, adapters.E(adapters.Rejected, "EXCEEDS_PAYMENT")
		}
		if c.balance < r.Amount.Minor {
			return payments.Refund{}, adapters.E(adapters.Rejected, payments.ReasonInsufficientBalance)
		}
		id := f.nextID("rfnd")
		rf = &refund{Refund: payments.Refund{ProviderRefundID: id, MerchantRefundRef: r.MerchantRefundRef, PaymentID: r.PaymentID,
			Status: payments.RefundPending, Amount: r.Amount}, link: r.ClinicLinkRef, idemKey: r.IdempotencyKey, createdAt: f.now()}
		f.refunds[id] = rf
		f.refundIdem[r.IdempotencyKey] = id
		p.refunded += r.Amount.Minor
		c.balance -= r.Amount.Minor
	}
	if faulted && ft == TimeoutApplied {
		return payments.Refund{}, faultErr(ft)
	}
	return rf.Refund, nil
}

// GetRefundStatus implements payments.Port.
func (f *Fake) GetRefundStatus(_ context.Context, link, providerRefundID string) (payments.Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.take(OpGetRefundStatus); ok {
		return payments.Refund{}, faultErr(ft)
	}
	r, ok := f.refunds[providerRefundID]
	if !ok || r.link != link {
		return payments.Refund{}, adapters.E(adapters.NotFound, "REFUND")
	}
	return r.Refund, nil
}

// FindRefund implements payments.Port.
func (f *Fake) FindRefund(_ context.Context, link, paymentID, merchantRef string) (payments.Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.take(OpFindRefund); ok {
		return payments.Refund{}, faultErr(ft)
	}
	for _, id := range f.refundIDs() {
		r := f.refunds[id]
		if r.link == link && r.PaymentID == paymentID && r.MerchantRefundRef == merchantRef && f.visible(r.createdAt) {
			return r.Refund, nil
		}
	}
	return payments.Refund{}, adapters.E(adapters.NotFound, "REFUND")
}

// VerifyWebhook implements payments.Port: the signature is checked first, in constant
// time, before the body is parsed.
func (f *Fake) VerifyWebhook(_ context.Context, link string, headers http.Header, body []byte) (payments.Event, error) {
	f.mu.Lock()
	c, ok := f.clinics[link]
	f.mu.Unlock()
	if !ok {
		return payments.Event{}, adapters.E(adapters.Invalid, "UNKNOWN_ACCOUNT")
	}
	got, err := hex.DecodeString(headers.Get(SignatureHeader))
	want, _ := hex.DecodeString(Sign(c.secret, body))
	if err != nil || !hmac.Equal(got, want) {
		return payments.Event{}, adapters.E(adapters.Invalid, "BAD_SIGNATURE")
	}
	var w wireEvent
	if err := json.Unmarshal(body, &w); err != nil || w.ID == "" || w.Type == "" {
		return payments.Event{}, adapters.E(adapters.Invalid, "BAD_BODY")
	}
	at, err := time.Parse(time.RFC3339Nano, w.OccurredAt)
	if err != nil {
		return payments.Event{}, adapters.E(adapters.Invalid, "BAD_BODY")
	}
	return payments.Event{ID: w.ID, Type: payments.EventType(w.Type), ProviderOrderID: w.ProviderOrderID, MerchantOrderRef: w.MerchantOrderRef,
		PaymentID: w.PaymentID, ProviderRefundID: w.ProviderRefundID, MerchantRefundRef: w.MerchantRefundRef,
		Amount: payments.Money{Minor: w.AmountMinor, Currency: w.Currency}, OccurredAt: at}, nil
}

// FetchSettlementReport implements payments.Port.
func (f *Fake) FetchSettlementReport(_ context.Context, link string, day time.Time) ([]payments.SettlementLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.take(OpFetchSettlement); ok {
		return nil, faultErr(ft)
	}
	y, m, d := day.UTC().Date()
	same := func(t time.Time) bool { ty, tm, td := t.UTC().Date(); return ty == y && tm == m && td == d }
	var out []payments.SettlementLine
	for _, id := range sortedKeys(f.pays) {
		p := f.pays[id]
		if p.link == link && same(p.capturedAt) {
			out = append(out, payments.SettlementLine{Kind: payments.SettlementCapture, PaymentID: p.id,
				MerchantRef: f.orders[p.orderID].MerchantOrderRef, Amount: p.amount, OccurredAt: p.capturedAt})
		}
	}
	for _, id := range f.refundIDs() {
		r := f.refunds[id]
		if r.link == link && r.Status == payments.RefundProcessed && same(r.ProcessedAt) {
			out = append(out, payments.SettlementLine{Kind: payments.SettlementRefund, PaymentID: r.PaymentID,
				MerchantRef: r.MerchantRefundRef, Amount: r.Amount, OccurredAt: r.ProcessedAt})
		}
	}
	if f.adjust != nil {
		out = f.adjust(link, out)
	}
	return out, nil
}

func sortedKeys(m map[string]*pay) []string {
	k := make([]string, 0, len(m))
	for id := range m {
		k = append(k, id)
	}
	sort.Strings(k)
	return k
}
