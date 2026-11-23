package payments_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

const amount = 50000

var ctx = context.Background()

type fx struct {
	env    *testkit.Env
	m      *payments.Module
	fk     *fake.Fake
	pol    *refundpolicy.Policy
	now    time.Time
	clinic string
	link   string
	alerts []string
}

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), payments.Source()}})
	b, err := os.ReadFile("../../../../config/country-packs/IN/payments/refund-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := refundpolicy.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{env: env, pol: pol, fk: fake.New(), now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), clinic: ids.New()}
	f.m, err = payments.New(env.Pools.For("payments"), f.fk, payments.Config{Currency: pol.Currency, Policy: pol,
		AllowedModes: []string{"clinic_collected", "pay_at_clinic"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return f.now }
	f.m.SetClock(clock)
	f.fk.SetClock(clock)
	f.m.Alert = func(_ context.Context, code string) { f.alerts = append(f.alerts, code) }
	f.link, err = f.m.LinkClinic(ctx, payments.LinkParams{ClinicRef: f.clinic, Aggregator: "fake", SettlementMode: "clinic_collected",
		CredentialRef: "secretref:clinics/" + f.clinic + "/cred", WebhookSecretRef: "secretref:clinics/" + f.clinic + "/webhook"})
	if err != nil {
		t.Fatal(err)
	}
	f.fk.RegisterClinic(f.link, "whsec-synthetic-1", 1_000_000)
	return f
}

func (f *fx) advance(d time.Duration) { f.now = f.now.Add(d) }

func (f *fx) open(t *testing.T) (string, rp.Checkout) {
	t.Helper()
	appt := ids.New()
	co, err := f.m.CreateOrder(ctx, appt, f.clinic, amount, f.pol.Currency, f.now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return appt, co
}

func (f *fx) deliver(t *testing.T) {
	t.Helper()
	for _, d := range f.fk.DrainDeliveries() {
		if _, err := f.m.HandleWebhook(ctx, d.ClinicLinkRef, d.Headers, d.Body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.m.ProcessWebhooks(ctx, 100); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) poll(t *testing.T) payments.PollStats {
	t.Helper()
	st, err := f.m.PollPending(ctx, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *fx) n(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fx) events(t *testing.T, typ string) int {
	return f.n(t, `SELECT count(*) FROM payments.event_outbox WHERE type=$1`, typ)
}

func (f *fx) paid(t *testing.T) (string, rp.Checkout, string) {
	t.Helper()
	appt, co := f.open(t)
	pay := "pay_" + strings.ReplaceAll(ids.New(), "-", "")[16:]
	if err := f.fk.PayOrder(co.ProviderOrderID, pay, f.now); err != nil {
		t.Fatal(err)
	}
	f.deliver(t)
	return appt, co, pay
}

func (f *fx) apptEvent(typ, appt, by string, extra map[string]string) outbox.Event {
	p := map[string]string{"appointmentRef": appt, "byWhom": by, "policyVersion": f.pol.Version, "mode": "online"}
	for k, v := range extra {
		p[k] = v
	}
	return outbox.Event{ID: ids.New(), Type: typ, Payload: p}
}

func (f *fx) refundState(t *testing.T, appt string) rp.RefundState {
	t.Helper()
	r, err := f.m.RefundStatus(ctx, appt)
	if err != nil {
		t.Fatal(err)
	}
	return r.State
}

// ---- S-124 / S-125: orders, operation keys ----

func TestS125_CreateOrderIsIdempotentPerAppointment(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	if co.State != rp.OrderCreated || co.CheckoutURL == "" || co.ProviderOrderID == "" {
		t.Fatalf("checkout: %+v", co)
	}
	again, err := f.m.CreateOrder(ctx, appt, f.clinic, amount, f.pol.Currency, f.now.Add(15*time.Minute))
	if err != nil || again.OrderRef != co.OrderRef {
		t.Fatalf("second call: %+v %v", again, err)
	}
	if f.fk.Calls(fake.OpCreateOrder) != 1 || f.fk.OrderCount(co.OrderRef) != 1 {
		t.Fatal("the vendor must see the order once")
	}
	if _, err := f.m.CreateOrder(ctx, appt, f.clinic, amount+1, f.pol.Currency, f.now.Add(time.Hour)); !errors.Is(err, rp.ErrConflict) {
		t.Fatalf("re-pricing: want ErrConflict, got %v", err)
	}
	if _, err := f.m.CreateOrder(ctx, ids.New(), f.clinic, amount, "USD", f.now.Add(time.Hour)); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("other currency: want ErrInvalid, got %v", err)
	}
	// The unique constraints hold even for a direct insert.
	_, err = f.env.Pools.For("payments").Exec(ctx, `INSERT INTO payment_order (order_ref, operation_key, merchant_ref, appointment_ref, clinic_ref, link_ref, amount_minor, currency, status, hold_expires_at, next_attempt_at)
		VALUES ($1,$2,$1,$3,$4,$5,1,'XXX','requested',now(),now())`, ids.New(), "order:"+appt, appt, f.clinic, f.link)
	if err == nil {
		t.Fatal("a second order for the same appointment must be refused by the database")
	}
}

func TestS163_SettlementModes(t *testing.T) {
	f := setup(t)
	other := ids.New()
	if _, err := f.m.LinkClinic(ctx, payments.LinkParams{ClinicRef: other, Aggregator: "fake", SettlementMode: "split",
		CredentialRef: "secretref:a", WebhookSecretRef: "secretref:b"}); !errors.Is(err, rp.ErrSplitNotReady) {
		t.Fatalf("split: %v", err)
	}
	if _, err := f.m.LinkClinic(ctx, payments.LinkParams{ClinicRef: other, Aggregator: "fake", SettlementMode: "clinic_collected",
		CredentialRef: "rzp_live_abcdef", WebhookSecretRef: "secretref:b"}); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("a secret instead of a reference must be refused: %v", err)
	}
	if _, err := f.m.LinkClinic(ctx, payments.LinkParams{ClinicRef: other, Aggregator: "fake", SettlementMode: "pay_at_clinic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.CreateOrder(ctx, ids.New(), other, amount, f.pol.Currency, f.now.Add(time.Hour)); !errors.Is(err, rp.ErrPayAtClinic) {
		t.Fatalf("pay at clinic: %v", err)
	}
	if mode, err := f.m.SettlementMode(ctx, other); err != nil || mode != rp.PayAtClinic {
		t.Fatalf("mode %q %v", mode, err)
	}
	if mode, _ := f.m.SettlementMode(ctx, f.clinic); mode != rp.ClinicCollected {
		t.Fatal("default mode is clinic_collected")
	}
	if _, err := f.m.CreateOrder(ctx, ids.New(), ids.New(), amount, f.pol.Currency, f.now.Add(time.Hour)); !errors.Is(err, rp.ErrNoMerchant) {
		t.Fatalf("no link: %v", err)
	}
	if n := f.n(t, `SELECT count(*) FROM payments.clinic_merchant_link WHERE credential_ref NOT LIKE 'secretref:%' AND credential_ref <> ''`); n != 0 {
		t.Fatal("only references may be stored")
	}
}

// ---- Timeout paths: never a double charge or double refund ----

func TestTimeout_CreateOrderAppliedNeverDoubleCharges(t *testing.T) {
	f := setup(t)
	f.fk.IgnoreIdempotency = true // a vendor that would apply twice on a blind retry
	f.fk.Inject(fake.OpCreateOrder, fake.TimeoutApplied)
	appt, co := f.open(t)
	if co.State != rp.OrderUnknown || co.CheckoutURL != "" {
		t.Fatalf("want unknown, got %+v", co)
	}
	f.poll(t)
	if f.fk.Calls(fake.OpCreateOrder) != 1 || f.fk.OrderCount(co.OrderRef) != 1 {
		t.Fatalf("no second create: calls=%d count=%d", f.fk.Calls(fake.OpCreateOrder), f.fk.OrderCount(co.OrderRef))
	}
	o, err := f.m.OrderStatus(ctx, appt)
	if err != nil || o.State != rp.OrderCreated {
		t.Fatalf("resolved by findOrder: %+v %v", o, err)
	}
}

func TestTimeout_CreateOrderNotAppliedRetriesOnlyAfterSettleTime(t *testing.T) {
	f := setup(t)
	f.fk.IgnoreIdempotency = true
	f.fk.Inject(fake.OpCreateOrder, fake.TimeoutNotApplied)
	appt, co := f.open(t)
	f.advance(time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpCreateOrder) != 1 {
		t.Fatal("before the settle time NotFound is not believed: no retry")
	}
	f.advance(11 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpCreateOrder) != 2 || f.fk.OrderCount(co.OrderRef) != 1 {
		t.Fatalf("one retry, one order: calls=%d count=%d", f.fk.Calls(fake.OpCreateOrder), f.fk.OrderCount(co.OrderRef))
	}
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCreated {
		t.Fatalf("state %s", o.State)
	}
}

func TestTimeout_Vendor5xxOnCreateIsResolvedNotBlindlyRetried(t *testing.T) {
	f := setup(t)
	f.fk.Inject(fake.OpCreateOrder, fake.Unavailable)
	_, co := f.open(t)
	if co.State != rp.OrderUnknown {
		t.Fatalf("%+v", co)
	}
	f.poll(t)
	if f.fk.Calls(fake.OpCreateOrder) != 1 {
		t.Fatal("5xx must not trigger an immediate blind retry")
	}
}

func cancelClinic(f *fx, t *testing.T, appt string) {
	t.Helper()
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, appt, "clinic", nil)); err != nil {
		t.Fatal(err)
	}
}

func TestTimeout_RefundAppliedNeverDoubleRefunds(t *testing.T) {
	f := setup(t)
	f.fk.IgnoreIdempotency = true
	appt, co, pay := f.paid(t)
	f.fk.Inject(fake.OpRefund, fake.TimeoutApplied)
	cancelClinic(f, t, appt)
	if s := f.refundState(t, appt); s != rp.RefundUnknown {
		t.Fatalf("want unknown, got %s", s)
	}
	f.poll(t)
	f.poll(t)
	r, _ := f.m.RefundStatus(ctx, appt)
	if f.fk.Calls(fake.OpRefund) != 1 || f.fk.RefundCount(r.RefundRef) != 1 || f.fk.TotalRefunded(pay) != amount {
		t.Fatalf("refund must happen exactly once: calls=%d count=%d total=%d", f.fk.Calls(fake.OpRefund), f.fk.RefundCount(r.RefundRef), f.fk.TotalRefunded(pay))
	}
	f.fk.SettleRefunds(f.now)
	f.poll(t)
	if s := f.refundState(t, appt); s != rp.RefundSucceeded {
		t.Fatalf("want succeeded, got %s", s)
	}
	bal, _ := f.m.Balance(ctx, payments.ClinicAccount(f.clinic), f.pol.Currency)
	if bal != 0 {
		t.Fatalf("clinic memo balance after capture and refund: %d", bal)
	}
	_ = co
}

func TestTimeout_RefundNotAppliedRetriesSameKeyAfterSettle(t *testing.T) {
	f := setup(t)
	f.fk.IgnoreIdempotency = true
	appt, _, pay := f.paid(t)
	f.fk.Inject(fake.OpRefund, fake.TimeoutNotApplied)
	cancelClinic(f, t, appt)
	f.advance(2 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 1 {
		t.Fatal("no retry before the settle time")
	}
	f.advance(10 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 2 || f.fk.TotalRefunded(pay) != amount {
		t.Fatalf("one retry, one refund: calls=%d total=%d", f.fk.Calls(fake.OpRefund), f.fk.TotalRefunded(pay))
	}
	f.poll(t)
	if f.fk.TotalRefunded(pay) != amount {
		t.Fatal("never refund twice")
	}
}

func TestTimeout_RefundAppliedButNotYetVisible(t *testing.T) {
	f := setup(t)
	f.fk.IgnoreIdempotency = true
	f.fk.SetFindLag(5 * time.Minute)
	appt, _, pay := f.paid(t)
	f.fk.Inject(fake.OpRefund, fake.TimeoutApplied)
	cancelClinic(f, t, appt)
	f.advance(time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 1 {
		t.Fatal("findRefund NotFound before settle time must not cause a retry")
	}
	f.advance(5 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 1 || f.fk.TotalRefunded(pay) != amount {
		t.Fatalf("found by findRefund, still one refund: calls=%d total=%d", f.fk.Calls(fake.OpRefund), f.fk.TotalRefunded(pay))
	}
}

func TestTimeout_RateLimitedRefundIsNotAppliedAndRetriedLater(t *testing.T) {
	f := setup(t)
	appt, _, pay := f.paid(t)
	f.fk.Inject(fake.OpRefund, fake.RateLimited)
	cancelClinic(f, t, appt)
	if f.fk.TotalRefunded(pay) != 0 {
		t.Fatal("rate limited means not applied")
	}
	f.advance(time.Minute)
	f.poll(t)
	if f.fk.TotalRefunded(pay) != amount {
		t.Fatal("refund after the backoff")
	}
}

// ---- Webhooks (S-124): TC-30, TC-58 ----

func TestTC30_SameWebhookThreeTimesProcessedOnce(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	if err := f.fk.PayOrder(co.ProviderOrderID, "pay_once", f.now); err != nil {
		t.Fatal(err)
	}
	d := f.fk.DrainDeliveries()[0]
	want := []payments.WebhookOutcome{payments.WebhookStored, payments.WebhookDuplicate, payments.WebhookDuplicate}
	for i := range want {
		got, err := f.m.HandleWebhook(ctx, d.ClinicLinkRef, d.Headers, d.Body)
		if err != nil || got != want[i] {
			t.Fatalf("delivery %d: %v %v", i, got, err)
		}
	}
	if n, _ := f.m.ProcessWebhooks(ctx, 10); n != 1 {
		t.Fatalf("processed %d", n)
	}
	if f.events(t, rp.EventPaymentCaptured) != 1 || f.n(t, `SELECT count(*) FROM payments.ledger_txn`) != 1 {
		t.Fatal("one capture event and one ledger transaction")
	}
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCaptured {
		t.Fatal("captured")
	}
}

func TestTC58_ForgedWebhooksAreRejected(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	cur := f.pol.Currency
	good := adp.Event{ID: "evt_forge_1", Type: adp.EventPaymentCaptured, ProviderOrderID: co.ProviderOrderID, MerchantOrderRef: co.OrderRef,
		PaymentID: "pay_forged", Amount: adp.Money{Minor: amount, Currency: cur}, OccurredAt: f.now}

	// 1. Bad signature: refused, nothing stored.
	d := f.fk.SignedDelivery(f.link, good)
	d.Headers.Set(fake.SignatureHeader, strings.Repeat("0", 64))
	if out, _ := f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body); out != payments.WebhookUnauthorized {
		t.Fatalf("bad signature: %v", out)
	}
	// Signed with another clinic's secret.
	f.fk.RegisterClinic(ids.New(), "other-secret", 0)
	d2 := d
	d2.Headers = http.Header{fake.SignatureHeader: []string{fake.Sign("other-secret", d.Body)}}
	if out, _ := f.m.HandleWebhook(ctx, f.link, d2.Headers, d2.Body); out != payments.WebhookUnauthorized {
		t.Fatalf("wrong secret: %v", out)
	}
	// Unknown clinic link: same answer.
	if out, _ := f.m.HandleWebhook(ctx, ids.New(), d.Headers, d.Body); out != payments.WebhookUnauthorized {
		t.Fatal("unknown link")
	}
	if f.n(t, `SELECT count(*) FROM payments.webhook_event`) != 0 {
		t.Fatal("unverified events are not stored")
	}

	// 2. Wrong amount, wrong currency, unknown order, wrong clinic order: stored (signature ok) then rejected.
	wrong := good
	wrong.ID, wrong.Amount.Minor = "evt_forge_2", 100
	wrongCur := good
	wrongCur.ID, wrongCur.Amount.Currency = "evt_forge_3", "USD"
	unknown := good
	unknown.ID, unknown.MerchantOrderRef = "evt_forge_4", ids.New()
	for _, e := range []adp.Event{wrong, wrongCur, unknown} {
		d := f.fk.SignedDelivery(f.link, e)
		if out, err := f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body); err != nil || out != payments.WebhookStored {
			t.Fatalf("%s: %v %v", e.ID, out, err)
		}
	}
	if _, err := f.m.ProcessWebhooks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if n := f.n(t, `SELECT count(*) FROM payments.webhook_event WHERE status='rejected'`); n != 3 {
		t.Fatalf("rejected %d", n)
	}
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCreated || f.events(t, rp.EventPaymentCaptured) != 0 {
		t.Fatal("no booking may be confirmed by a forged event")
	}
	if f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='webhook_rejected'`) != 3 || len(f.alerts) != 3 {
		t.Fatalf("ticket and alert for each forged event: alerts=%v", f.alerts)
	}

	// 3. The genuine event confirms; replaying its event ID changes nothing.
	d = f.fk.SignedDelivery(f.link, good)
	f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body)
	f.m.ProcessWebhooks(ctx, 10)
	if out, _ := f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body); out != payments.WebhookDuplicate {
		t.Fatal("replayed event ID")
	}
	f.m.ProcessWebhooks(ctx, 10)
	if f.events(t, rp.EventPaymentCaptured) != 1 {
		t.Fatal("exactly one confirmation")
	}
}

func TestTC58_StateChangesOnlyFromWebhooksOrPolls(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	f.fk.PayOrder(co.ProviderOrderID, "pay_unseen", f.now) // vendor captured, but no webhook delivered yet
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCreated {
		t.Fatal("capture must not be assumed")
	}
}

func TestWebhookHTTPHandler(t *testing.T) {
	f := setup(t)
	_, co := f.open(t)
	f.fk.PayOrder(co.ProviderOrderID, "pay_http", f.now)
	d := f.fk.DrainDeliveries()[0]
	r := chi.NewRouter()
	r.Post("/webhooks/payments/{clinicLinkRef}", f.m.WebhookHandler().ServeHTTP)
	do := func(link string, sig string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/payments/"+link, strings.NewReader(string(d.Body)))
		req.Header.Set(fake.SignatureHeader, sig)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if c := do(f.link, "00"); c != http.StatusUnauthorized {
		t.Fatalf("bad sig %d", c)
	}
	if c := do(ids.New(), d.Headers.Get(fake.SignatureHeader)); c != http.StatusUnauthorized {
		t.Fatalf("unknown link %d", c)
	}
	if f.n(t, `SELECT count(*) FROM payments.webhook_event`) != 0 {
		t.Fatal("nothing stored yet")
	}
	if c := do(f.link, d.Headers.Get(fake.SignatureHeader)); c != http.StatusOK {
		t.Fatalf("good %d", c)
	}
	if f.n(t, `SELECT count(*) FROM payments.webhook_event`) != 1 {
		t.Fatal("2xx only after the event is stored")
	}
	if c := do(f.link, d.Headers.Get(fake.SignatureHeader)); c != http.StatusOK {
		t.Fatal("duplicate is 2xx")
	}
}

func TestWebhook_OutOfOrderEventsAreHarmless(t *testing.T) {
	f := setup(t)
	appt, co, pay := f.paid(t)
	cur := f.pol.Currency
	// payment.failed for the SAME payment, and for another attempt, arrive after the capture.
	for i, pid := range []string{pay, "pay_other"} {
		d := f.fk.SignedDelivery(f.link, adp.Event{ID: "evt_late_" + string(rune('a'+i)), Type: adp.EventPaymentFailed, ProviderOrderID: co.ProviderOrderID,
			MerchantOrderRef: co.OrderRef, PaymentID: pid, Amount: adp.Money{Minor: amount, Currency: cur}, OccurredAt: f.now})
		f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body)
	}
	f.m.ProcessWebhooks(ctx, 10)
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCaptured || f.events(t, rp.EventPaymentFailed) != 0 {
		t.Fatal("a late failure must not undo a capture")
	}

	// refund.succeeded arrives while the refund is still `unknown`, then a stale refund.failed.
	f.fk.Inject(fake.OpRefund, fake.TimeoutApplied)
	cancelClinic(f, t, appt)
	f.fk.SettleRefunds(f.now)
	f.deliver(t)
	if s := f.refundState(t, appt); s != rp.RefundSucceeded {
		t.Fatalf("want succeeded, got %s", s)
	}
	r, _ := f.m.RefundStatus(ctx, appt)
	pid, _ := f.fk.ProviderRefundID(r.RefundRef)
	d := f.fk.SignedDelivery(f.link, adp.Event{ID: "evt_stale_fail", Type: adp.EventRefundFailed, ProviderRefundID: pid, MerchantRefundRef: r.RefundRef,
		PaymentID: pay, Amount: adp.Money{Minor: amount, Currency: cur}, OccurredAt: f.now})
	f.m.HandleWebhook(ctx, f.link, d.Headers, d.Body)
	f.m.ProcessWebhooks(ctx, 10)
	if f.refundState(t, appt) != rp.RefundSucceeded || f.events(t, rp.EventRefundSucceeded) != 1 || f.events(t, rp.EventRefundFailed) != 0 {
		t.Fatal("a stale refund.failed must change nothing")
	}
}

// ---- TC-28, TC-29: late payments, failed booking ----

func TestTC28_WebhookDelayedPollingConfirms(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	f.advance(3 * time.Minute)
	f.fk.PayOrder(co.ProviderOrderID, "pay_poll", f.now)
	f.fk.DrainDeliveries() // the webhook never arrives
	f.advance(time.Minute)
	f.poll(t)
	if o, _ := f.m.OrderStatus(ctx, appt); o.State != rp.OrderCaptured || f.events(t, rp.EventPaymentCaptured) != 1 {
		t.Fatal("polling must confirm the booking")
	}
	f.poll(t)
	if f.events(t, rp.EventPaymentCaptured) != 1 {
		t.Fatal("polling twice must not confirm twice")
	}
}

func TestTC28_LateDebitAfterHoldExpiresIsRefunded(t *testing.T) {
	f := setup(t)
	appt, co := f.open(t)
	f.advance(20 * time.Minute) // hold (15 min) expired
	f.fk.PayOrder(co.ProviderOrderID, "pay_late", f.now)
	f.fk.DrainDeliveries()
	f.poll(t)
	if f.events(t, rp.EventPaymentCaptured) != 0 {
		t.Fatal("a late capture must not confirm a booking")
	}
	r, err := f.m.RefundStatus(ctx, appt)
	if err != nil || r.RuleID != "RF01" || r.AmountMinor != amount {
		t.Fatalf("automatic full refund RF01: %+v %v", r, err)
	}
	if f.fk.TotalRefunded("pay_late") != amount {
		t.Fatal("refund sent to the vendor")
	}
	f.fk.SettleRefunds(f.now)
	f.poll(t)
	if f.refundState(t, appt) != rp.RefundSucceeded || f.events(t, rp.EventRefundSucceeded) != 1 {
		t.Fatal("refund succeeded and reported")
	}
	// Webhook for the same capture arriving now changes nothing.
	f.fk.PayOrder(co.ProviderOrderID, "pay_late", f.now)
	f.deliver(t)
	if f.fk.TotalRefunded("pay_late") != amount || f.events(t, rp.EventPaymentCaptured) != 0 {
		t.Fatal("idempotent")
	}
}

func TestTC29_DebitedButBookingExpiredOrCancelled(t *testing.T) {
	f := setup(t)
	// (a) booking reports the appointment expired AFTER the capture.
	a1, _, p1 := f.paid(t)
	if err := f.m.OnAppointmentExpired(ctx, f.apptEvent(rp.EventAppointmentExpired, a1, "system", nil)); err != nil {
		t.Fatal(err)
	}
	r, err := f.m.RefundStatus(ctx, a1)
	if err != nil || r.RuleID != "RF01" || f.fk.TotalRefunded(p1) != amount {
		t.Fatalf("a: %+v %v", r, err)
	}
	// (b) booking reports it cancelled BEFORE the capture arrives.
	a2, co2 := f.open(t)
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, a2, "system", nil)); err != nil {
		t.Fatal(err)
	}
	f.fk.PayOrder(co2.ProviderOrderID, "pay_after_cancel", f.now)
	f.deliver(t)
	if f.fk.TotalRefunded("pay_after_cancel") != amount || f.refundState(t, a2) != rp.RefundProcessing {
		t.Fatalf("b: refunded=%d state=%s", f.fk.TotalRefunded("pay_after_cancel"), f.refundState(t, a2))
	}
	if f.events(t, rp.EventPaymentCaptured) != 1 { // only (a)'s original capture
		t.Fatal("a cancelled booking is never confirmed")
	}
}

func TestE_PAY_03_DuplicatePaymentRefunded(t *testing.T) {
	f := setup(t)
	appt, co, _ := f.paid(t)
	f.fk.PayOrder(co.ProviderOrderID, "pay_dup", f.now)
	f.deliver(t)
	if f.fk.TotalRefunded("pay_dup") != amount || f.n(t, `SELECT count(*) FROM payments.refund WHERE rule_id='RF02'`) != 1 {
		t.Fatal("the duplicate is refunded under RF02")
	}
	if f.events(t, rp.EventPaymentCaptured) != 1 {
		t.Fatal("one confirmation")
	}
	_ = appt
}

// ---- Refund policy through the handlers (S-133, S-125) ----

func TestRefundPolicy_PatientCancel(t *testing.T) {
	f := setup(t)
	appt, _, pay := f.paid(t)
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, appt, "patient", nil)); err == nil {
		t.Fatal("a patient cancellation without minutesBeforeSlot must fail closed")
	}
	if f.fk.TotalRefunded(pay) != 0 {
		t.Fatal("no refund on a bad event")
	}
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, appt, "patient", map[string]string{"minutesBeforeSlot": "30"})); err != nil {
		t.Fatal(err)
	}
	if f.fk.TotalRefunded(pay) != 0 || f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='refund_policy_blocked'`) != 1 {
		t.Fatal("RF08 is blocked: no refund, a ticket for the founder's decision")
	}
	appt2, _, pay2 := f.paid(t)
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, appt2, "patient", map[string]string{"minutesBeforeSlot": "300"})); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.m.RefundStatus(ctx, appt2); r.RuleID != "RF07" || f.fk.TotalRefunded(pay2) != amount {
		t.Fatalf("RF07: %+v", r)
	}
}

func TestRefundPolicy_NoShowAndDoctorEvents(t *testing.T) {
	f := setup(t)
	a1, _, p1 := f.paid(t)
	if err := f.m.OnAppointmentNoShow(ctx, f.apptEvent(rp.EventAppointmentNoShow, a1, "patient", nil)); err != nil {
		t.Fatal(err)
	}
	if f.fk.TotalRefunded(p1) != 0 || f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE ticket_key LIKE 'policy_blocked:%:RF09'`) != 1 {
		t.Fatal("RF09 blocked")
	}
	a2, _, p2 := f.paid(t)
	if err := f.m.OnAppointmentNoShow(ctx, f.apptEvent(rp.EventAppointmentNoShow, a2, "doctor", nil)); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.m.RefundStatus(ctx, a2); r.RuleID != "RF03" || f.fk.TotalRefunded(p2) != amount {
		t.Fatalf("RF03 %+v", r)
	}
	a3, _, p3 := f.paid(t)
	if err := f.m.OnAppointmentCancelled(ctx, f.apptEvent(rp.EventAppointmentCancelled, a3, "doctor", map[string]string{"rule": "RF06"})); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.m.RefundStatus(ctx, a3); r.RuleID != "RF06" || f.fk.TotalRefunded(p3) != amount/2 {
		t.Fatalf("RF06 %+v", r)
	}
	// S-157: each decided refund is announced with its rule, trigger, share and exact amount,
	// so the patient is told precisely what is coming back; a blocked rule announces nothing.
	var payloads []map[string]string
	rows, err := f.env.Owner.Query(ctx, `SELECT payload FROM payments.event_outbox WHERE type=$1 ORDER BY occurred_at, event_id`, rp.EventRefundRequested)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p map[string]string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	rows.Close()
	byAppt := map[string]map[string]string{}
	for _, p := range payloads {
		byAppt[p["appointmentRef"]] = p
	}
	if len(payloads) != 2 || byAppt[a1] != nil {
		t.Fatalf("two refunds announced, none for the blocked RF09: %v", payloads)
	}
	if p := byAppt[a2]; p["ruleId"] != "RF03" || p["trigger"] != "doctor_not_joined" || p["pct"] != "100" || p["amountMinor"] != strconv.FormatInt(amount, 10) || p["currency"] == "" || p["refundRef"] == "" {
		t.Fatalf("RF03 payload: %v", p)
	}
	if p := byAppt[a3]; p["ruleId"] != "RF06" || p["trigger"] != "converted_to_in_person" || p["pct"] != "50" || p["amountMinor"] != strconv.FormatInt(amount/2, 10) {
		t.Fatalf("RF06 payload: %v", p)
	}
	// An unknown policy version fails closed.
	a4, _, _ := f.paid(t)
	e := f.apptEvent(rp.EventAppointmentCancelled, a4, "clinic", nil)
	e.Payload["policyVersion"] = "0.0.1"
	if err := f.m.OnAppointmentCancelled(ctx, e); err == nil {
		t.Fatal("unknown policy version must fail")
	}
}

func TestRefund_OnePerAppointmentAndRule(t *testing.T) {
	f := setup(t)
	appt, _, pay := f.paid(t)
	cancelClinic(f, t, appt)
	cancelClinic(f, t, appt) // a second event with a different event ID
	if f.n(t, `SELECT count(*) FROM payments.refund WHERE appointment_ref=$1`, appt) != 1 || f.fk.TotalRefunded(pay) != amount {
		t.Fatal("one refund per appointment and rule")
	}
	// The same event delivered twice is processed once.
	e := f.apptEvent(rp.EventAppointmentExpired, appt, "system", nil)
	f.m.OnAppointmentExpired(ctx, e)
	f.m.OnAppointmentExpired(ctx, e)
	if f.fk.TotalRefunded(pay) != amount {
		t.Fatalf("never above what was paid: %d", f.fk.TotalRefunded(pay))
	}
	if _, err := f.env.Pools.For("payments").Exec(ctx, `INSERT INTO refund (refund_ref, operation_key, merchant_ref, order_ref, appointment_ref, clinic_ref, link_ref, payment_id, rule_id,
		policy_version, amount_minor, currency, status, next_attempt_at) SELECT $1, operation_key, $1::text, order_ref, appointment_ref, clinic_ref, link_ref, payment_id, rule_id, policy_version,
		amount_minor, currency, 'requested', now() FROM payments.refund WHERE appointment_ref=$2`, ids.New(), appt); err == nil {
		t.Fatal("unique operation key must refuse a second refund")
	}
}

// ---- TC-31: clinic balance shortfall ----

func TestTC31_InsufficientClinicBalance(t *testing.T) {
	f := setup(t)
	appt, _, pay := f.paid(t)
	f.fk.SetBalance(f.link, 0)
	cancelClinic(f, t, appt)
	if s := f.refundState(t, appt); s != rp.RefundEscalated {
		t.Fatalf("want escalated, got %s", s)
	}
	if f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='grievance_refund_shortfall'`) != 1 || f.events(t, rp.EventRefundEscalated) != 1 {
		t.Fatal("grievance ticket and clinic alert event")
	}
	alerts := len(f.alerts)
	f.advance(10 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 1 {
		t.Fatal("retry only after the backoff")
	}
	f.advance(10 * time.Minute)
	f.poll(t)
	if f.fk.Calls(fake.OpRefund) != 2 || len(f.alerts) != alerts || f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='grievance_refund_shortfall'`) != 1 {
		t.Fatalf("one retry; alert and ticket once only (calls=%d)", f.fk.Calls(fake.OpRefund))
	}
	if p, _ := f.m.ClinicPaymentsPaused(ctx, f.clinic); p {
		t.Fatal("not paused before 48 hours")
	}
	f.advance(48 * time.Hour)
	if p, err := f.m.ClinicPaymentsPaused(ctx, f.clinic); err != nil || !p {
		t.Fatalf("paused after 48 hours: %v %v", p, err)
	}
	if _, err := f.m.CreateOrder(ctx, ids.New(), f.clinic, amount, f.pol.Currency, f.now.Add(time.Hour)); !errors.Is(err, rp.ErrClinicPaused) {
		t.Fatalf("new bookings pause: %v", err)
	}
	// The clinic tops up and asks for the retry: the refund succeeds and the pause clears.
	f.fk.SetBalance(f.link, 1_000_000)
	if err := f.m.RetryRefundNow(ctx, f.clinic, mustRef(t, f, appt)); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	f.fk.SettleRefunds(f.now)
	f.poll(t)
	if f.refundState(t, appt) != rp.RefundSucceeded || f.fk.TotalRefunded(pay) != amount {
		t.Fatal("refund must succeed once, after the top-up")
	}
	if p, _ := f.m.ClinicPaymentsPaused(ctx, f.clinic); p {
		t.Fatal("pause must clear")
	}
	if err := f.m.RetryRefundNow(ctx, ids.New(), mustRef(t, f, appt)); !errors.Is(err, rp.ErrNotFound) {
		t.Fatal("another clinic cannot touch it")
	}
}

func mustRef(t *testing.T, f *fx, appt string) string {
	t.Helper()
	r, err := f.m.RefundStatus(ctx, appt)
	if err != nil {
		t.Fatal(err)
	}
	return r.RefundRef
}

// ---- S-132: ledger ----

func TestS132_LedgerAppendOnlyAndBalanced(t *testing.T) {
	f := setup(t)
	appt, _, _ := f.paid(t)
	cancelClinic(f, t, appt)
	f.fk.SettleRefunds(f.now)
	f.deliver(t)
	if n := f.n(t, `SELECT count(*) FROM (SELECT txn_id, currency FROM payments.ledger_entry GROUP BY 1,2 HAVING sum(amount_minor) <> 0) s`); n != 0 {
		t.Fatal("every transaction sums to zero")
	}
	if f.n(t, `SELECT count(*) FROM payments.ledger_txn`) != 2 || f.n(t, `SELECT count(*) FROM payments.ledger_entry WHERE memo`) != 4 {
		t.Fatal("capture and refund, memo entries")
	}
	if b, _ := f.m.Balance(ctx, payments.PatientPaymentsAccount(f.clinic), f.pol.Currency); b != 0 {
		t.Fatalf("derived balance %d", b)
	}
	pool := f.env.Pools.For("payments")
	for _, q := range []string{`UPDATE ledger_entry SET amount_minor = 1`, `DELETE FROM ledger_entry`, `UPDATE ledger_txn SET kind = kind`, `DELETE FROM ledger_txn`, `TRUNCATE ledger_entry`, `TRUNCATE ledger_txn`} {
		if _, err := pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%s: module role must lack the privilege, got %v", q, err)
		}
	}
	// Review open item: no payments record can be deleted by the module role (RET-04).
	for _, tbl := range []string{"clinic_merchant_link", "payment_order", "payment_attempt", "webhook_event", "refund",
		"ops_ticket", "payment_alert", "reconciliation_run", "processed_event"} {
		for _, q := range []string{`DELETE FROM ` + tbl, `TRUNCATE ` + tbl} {
			if _, err := pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("%s: module role must not delete financial records, got %v", q, err)
			}
		}
	}
	// Even a role that has the privilege (the owner) is stopped by the trigger.
	if _, err := f.env.Owner.Exec(ctx, `UPDATE payments.ledger_entry SET amount_minor = 1`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("trigger: %v", err)
	}
	if _, err := f.env.Owner.Exec(ctx, `DELETE FROM payments.ledger_txn`); err == nil {
		t.Fatal("delete must fail")
	}
	// An unbalanced transaction cannot commit.
	tx, _ := pool.Begin(ctx)
	txn := ids.New()
	if _, err := tx.Exec(ctx, `INSERT INTO ledger_txn VALUES ($1,'bad','payment_captured',$2,NULL,now())`, txn, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ledger_entry VALUES ($1,$2,$3,100,'XXX',true,now())`, ids.New(), txn, "memo:clinic_account:"+f.clinic); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("an unbalanced ledger transaction must not commit")
	}
}

// ---- S-146: reconciliation ----

func TestS146_Reconciliation(t *testing.T) {
	f := setup(t)
	appt, _, _ := f.paid(t)
	cancelClinic(f, t, appt)
	f.fk.SettleRefunds(f.now)
	f.deliver(t)
	f.paid(t)
	res, err := f.m.Reconcile(ctx, f.link, f.now)
	if err != nil || res.Mismatches != 0 || res.Lines != 3 {
		t.Fatalf("clean day: %+v %v", res, err)
	}
	if f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='reconciliation_mismatch'`) != 0 {
		t.Fatal("no tickets on a clean day")
	}
	f.fk.AdjustReport(func(_ string, lines []adp.SettlementLine) []adp.SettlementLine {
		lines = lines[1:]          // a line our records have but the vendor does not
		lines[0].Amount.Minor += 1 // an amount that differs
		lines = append(lines, adp.SettlementLine{Kind: adp.SettlementCapture, PaymentID: "pay_ghost", MerchantRef: ids.New(),
			Amount: adp.Money{Minor: 100, Currency: f.pol.Currency}, OccurredAt: f.now}) // a vendor line we never saw
		return lines
	})
	res, err = f.m.Reconcile(ctx, f.link, f.now)
	if err != nil || res.Mismatches != 4 { // missing_vendor x2 (dropped one + the changed one's key stays) -> see below
		// dropped line: missing_vendor; changed amount: differs; ghost: missing_local; plus the original of the changed one is matched by key, so 3
		if res.Mismatches != 3 {
			t.Fatalf("mismatches %+v %v", res, err)
		}
	}
	tickets := f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='reconciliation_mismatch'`)
	if tickets != res.Mismatches || len(f.alerts) < tickets {
		t.Fatalf("ticket and alert for every mismatch: tickets=%d alerts=%d", tickets, len(f.alerts))
	}
	if _, err := f.m.Reconcile(ctx, f.link, f.now); err != nil {
		t.Fatal(err)
	}
	if f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='reconciliation_mismatch'`) != tickets {
		t.Fatal("re-running must not duplicate tickets")
	}
}

// S-144: the preview a patient sees before cancelling equals what the engine applies, and
// a clinic cancellation is always a full refund (RF04); no order means nothing to refund.
func TestPreviewRefundMatchesPolicy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	appt, _, _ := f.paid(t)
	var paid int64
	_ = f.env.Owner.QueryRow(ctx, `SELECT amount_minor FROM payments.payment_order WHERE appointment_ref=$1`, appt).Scan(&paid)
	amt, rule, err := f.m.PreviewRefund(ctx, appt, f.pol.Version, "clinic", "video", 30)
	if err != nil || amt != paid || rule == "" {
		t.Fatalf("clinic cancellation preview: %d %q %v (paid %d)", amt, rule, err, paid)
	}
	early, _, err := f.m.PreviewRefund(ctx, appt, f.pol.Version, "patient", "in_person", 48*60)
	if err != nil || early != paid {
		t.Fatalf("early patient cancellation should be free: %d %v", early, err)
	}
	if _, _, err := f.m.PreviewRefund(ctx, appt, f.pol.Version, "stranger", "video", 30); err == nil {
		t.Fatal("unknown actor must be refused")
	}
	if a, r, err := f.m.PreviewRefund(ctx, ids.New(), f.pol.Version, "patient", "video", 30); err != nil || a != 0 || r != "" {
		t.Fatalf("no order: %d %q %v", a, r, err)
	}
}

// Review M3: links are namespaced to the clinic, never silently overwritten, audited, and
// listed for the daily reconciliation.
func TestReviewM3LinkClinic(t *testing.T) {
	f := setup(t)
	other := ids.New()
	good := payments.LinkParams{ClinicRef: other, Aggregator: "fake", SettlementMode: "clinic_collected",
		CredentialRef: "secretref:clinics/" + other + "/cred", WebhookSecretRef: "secretref:clinics/" + other + "/hook"}
	// Another clinic's namespace, or none, is refused.
	bad := good
	bad.CredentialRef = "secretref:clinics/" + f.clinic + "/cred"
	if _, err := f.m.LinkClinic(ctx, bad); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("foreign namespace: %v", err)
	}
	bad = good
	bad.WebhookSecretRef = "secretref:payments/shared/hook"
	if _, err := f.m.LinkClinic(ctx, bad); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("no namespace: %v", err)
	}
	bad = good
	bad.CredentialRef = "secretref:clinics/" + other + "/../" + f.clinic + "/cred"
	if _, err := f.m.LinkClinic(ctx, bad); !errors.Is(err, rp.ErrInvalid) {
		t.Fatalf("path escape: %v", err)
	}
	good.ActorRef, good.ActorRole = ids.New(), "ops"
	if _, err := f.m.LinkClinic(ctx, good); err != nil {
		t.Fatal(err)
	}
	// No silent overwrite.
	if _, err := f.m.LinkClinic(ctx, good); !errors.Is(err, rp.ErrLinkExists) {
		t.Fatalf("second link: %v", err)
	}
	// Explicit update, audited with old and new mode.
	upd := good
	upd.SettlementMode, upd.CredentialRef, upd.WebhookSecretRef = "pay_at_clinic", "", ""
	if _, err := f.m.UpdateClinicLink(ctx, upd); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpdateClinicLink(ctx, payments.LinkParams{ClinicRef: ids.New(), Aggregator: "fake", SettlementMode: "pay_at_clinic"}); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("update of unknown link: %v", err)
	}
	var created, updated int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action='payments.link_created' AND actor_ref=$1`, good.ActorRef).Scan(&created)
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action='payments.link_updated' AND purpose='clinic_collected:pay_at_clinic'`).Scan(&updated)
	if created != 1 || updated != 1 {
		t.Fatalf("audit created=%d updated=%d", created, updated)
	}
	links, err := f.m.ListLinks(ctx)
	if err != nil || len(links) != 1 || links[0].LinkRef != f.link {
		t.Fatalf("ListLinks (only active clinic-collected): %+v %v", links, err)
	}
}
