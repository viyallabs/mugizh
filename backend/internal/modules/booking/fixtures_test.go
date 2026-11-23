package booking_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

const (
	testPolicy   = "1.0.0-test"
	practiceZone = "Asia/Kolkata" // tests only: shipped code never names a zone
	abroadZone   = "Asia/Dubai"
	fee          = int64(50000)
)

// ---- fakes for the other modules (synthetic data only) ----

type dirFake struct {
	mu sync.Mutex
	p  map[string]dp.Practice
}

func (d *dirFake) GetPractice(_ context.Context, id string) (dp.Practice, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.p[id]
	if !ok || !p.Listed {
		return dp.Practice{}, dp.ErrNotFound
	}
	return p, nil
}

func (d *dirFake) set(p dp.Practice) { d.mu.Lock(); d.p[p.ID] = p; d.mu.Unlock() }

type orderCall struct {
	Appt, Clinic, Currency string
	Amount                 int64
	Expires                time.Time
}

type payFake struct {
	mu     sync.Mutex
	mode   map[string]pp.SettlementMode
	paused map[string]bool
	orders map[string]string // appointment -> order ref
	calls  []orderCall
	err    error
}

func (p *payFake) CreateOrder(_ context.Context, appt, clinic string, amount int64, cur string, exp time.Time) (pp.Checkout, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return pp.Checkout{}, p.err
	}
	p.calls = append(p.calls, orderCall{appt, clinic, cur, amount, exp})
	ref, ok := p.orders[appt]
	if !ok {
		ref = ids.New()
		p.orders[appt] = ref
	}
	return pp.Checkout{OrderRef: ref, CheckoutURL: "https://pay.test/" + ref, State: pp.OrderCreated, AmountMinor: amount, Currency: cur}, nil
}
func (p *payFake) OrderStatus(context.Context, string) (pp.OrderInfo, error) {
	return pp.OrderInfo{}, pp.ErrNotFound
}
func (p *payFake) RefundStatus(context.Context, string) (pp.RefundInfo, error) {
	return pp.RefundInfo{}, pp.ErrNotFound
}
func (p *payFake) ClinicPaymentsPaused(_ context.Context, clinic string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused[clinic], nil
}
func (p *payFake) SettlementMode(_ context.Context, clinic string) (pp.SettlementMode, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := p.mode[clinic]; ok {
		return m, nil
	}
	return pp.ClinicCollected, nil
}

type gateFake struct {
	mu       sync.Mutex
	noCare   map[string]bool
	requests []rp.Request
	// allow decides Authorize; default: clinic admin and front desk of any clinic.
	allow func(r rp.Request) bool
}

func (g *gateFake) HasConsent(_ context.Context, subject, purpose string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return purpose == "P-CARE" && !g.noCare[subject], nil
}
func (g *gateFake) Authorize(_ context.Context, r rp.Request) (rp.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r)
	if g.allow != nil {
		return rp.Decision{Allowed: g.allow(r)}, nil
	}
	ok := r.Kind == rp.KindBooking && (r.Actor.Role == ip.RoleFrontDesk || r.Actor.Role == ip.RoleClinicAdmin)
	return rp.Decision{Allowed: ok}, nil
}

type verFake struct {
	mu  sync.Mutex
	bad map[string]bool
	err error
}

func (v *verFake) IsVerified(_ context.Context, ref string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return false, v.err
	}
	return !v.bad[ref], nil
}

type refundFake struct {
	mu    sync.Mutex
	fail  bool
	calls []string
}

func (r *refundFake) PreviewRefund(_ context.Context, appt, policy, byWhom, mode string, minutes int) (int64, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return 0, "", context.DeadlineExceeded
	}
	r.calls = append(r.calls, byWhom+"/"+mode)
	switch {
	case byWhom != "patient":
		return fee, "RF04", nil
	case minutes >= 120:
		return fee, "RF07", nil
	}
	return 0, "RF08", nil
}

// ---- fixture ----

type fx struct {
	env    *testkit.Env
	m      *booking.Module
	dir    *dirFake
	pay    *payFake
	gate   *gateFake
	refund *refundFake
	ver    *verFake
	mu     sync.Mutex
	clock  time.Time
}

func (f *fx) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
func (f *fx) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}
func (f *fx) setClock(t time.Time) { f.mu.Lock(); f.clock = t; f.mu.Unlock() }

// 2026-11-17 04:00 UTC is a Tuesday, 09:30 in the practice zone.
var t0 = time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC)

func testCfg() booking.Config {
	return booking.Config{RefundPolicyVersion: testPolicy, HoldTTL: 15 * time.Minute, RescheduleMinMinutes: 120, FreeReschedules: 1,
		PatientGraceMinutes: 10, DoctorJoinTimeoutMinutes: 10}
}

func setup(t testing.TB) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), booking.Source()}})
	f := &fx{env: env, dir: &dirFake{p: map[string]dp.Practice{}}, pay: &payFake{mode: map[string]pp.SettlementMode{}, paused: map[string]bool{}, orders: map[string]string{}},
		gate: &gateFake{noCare: map[string]bool{}}, refund: &refundFake{}, ver: &verFake{bad: map[string]bool{}}, clock: t0}
	m, err := booking.New(env.Pools.For("booking"), testCfg(), booking.Deps{Directory: f.dir, Payments: f.pay, Gate: f.gate, Refunds: f.refund, Verifier: f.ver},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(f.now)
	f.m = m
	return f
}

func docActor(p dp.Practice) booking.Actor {
	return booking.Actor{UserID: p.ProfessionalRef, Role: ip.RoleDoctor}
}
func adminActor(clinic string) booking.Actor {
	return booking.Actor{UserID: ids.New(), Role: ip.RoleClinicAdmin, ClinicRef: clinic}
}
func deskActor(clinic string) booking.Actor {
	return booking.Actor{UserID: ids.New(), Role: ip.RoleFrontDesk, ClinicRef: clinic}
}

// weekly: every day, in-person 09:00-12:00 in 30-minute slots and video 14:00-15:00.
func weekly() []booking.RuleInput {
	var r []booking.RuleInput
	for d := 0; d < 7; d++ {
		r = append(r, booking.RuleInput{Weekday: d, StartLocal: "09:00", EndLocal: "12:00", SlotMinutes: 30, Mode: booking.ModeInPerson},
			booking.RuleInput{Weekday: d, StartLocal: "14:00", EndLocal: "15:00", SlotMinutes: 30, Mode: booking.ModeVideo})
	}
	return r
}

// practice registers a listed practice with the weekly template and returns it.
func (f *fx) practice(t testing.TB) dp.Practice {
	t.Helper()
	return f.practiceAt(t, ids.New())
}

func (f *fx) practiceAt(t testing.TB, clinic string) dp.Practice {
	t.Helper()
	p := dp.Practice{ID: ids.New(), ProfessionalRef: ids.New(), ClinicRef: clinic, Modes: []string{booking.ModeInPerson, booking.ModeVideo},
		FeeAmount: fee, Currency: "TST", FeeVersion: "1", TimeZone: practiceZone, Listed: true}
	f.dir.set(p)
	if _, err := f.m.SetWeeklyTemplate(context.Background(), docActor(p), p.ID, weekly()); err != nil {
		t.Fatal(err)
	}
	return p
}

// openSlots returns every open slot of a mode, earliest first.
func (f *fx) openSlots(t testing.TB, p dp.Practice, mode string) []booking.SlotInfo {
	t.Helper()
	var out []booking.SlotInfo
	cursor := ""
	for {
		page, next, err := f.m.ListOpenSlots(context.Background(), p.ID, time.Time{}, time.Time{}, 100, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page {
			if s.Mode == mode {
				out = append(out, s)
			}
		}
		if next == "" {
			return out
		}
		cursor = next
	}
}

func (f *fx) holdIn(p dp.Practice, patient string, s booking.SlotInfo) (booking.Appointment, error) {
	return f.m.Hold(context.Background(), booking.HoldInput{PatientRef: patient, PracticeID: p.ID, SlotID: s.ID, Mode: s.Mode,
		PatientTimeZone: practiceZone, PatientInIndiaConfirmed: s.Mode != booking.ModeInPerson})
}

// paid books, pays and confirms an appointment (hold, checkout, payment.captured).
func (f *fx) paid(t testing.TB, p dp.Practice, patient string, s booking.SlotInfo) booking.Appointment {
	t.Helper()
	a, err := f.holdIn(p, patient, s)
	if err != nil {
		t.Fatal(err)
	}
	co, err := f.m.Checkout(context.Background(), patient, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.capture(t, a.ID, co.OrderRef)
	got, err := f.m.Mine(context.Background(), patient, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != booking.StateConfirmed {
		t.Fatalf("expected confirmed, got %s", got.State)
	}
	return got
}

func (f *fx) capture(t testing.TB, appt, order string) {
	t.Helper()
	e := outbox.Event{ID: ids.New(), Type: "payment.captured", Payload: map[string]string{"orderRef": order, "appointmentRef": appt}}
	if err := f.m.OnPaymentCaptured(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

// events returns the payloads of booking events of a type, oldest first.
func (f *fx) events(t testing.TB, typ string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload FROM booking.event_outbox WHERE type=$1 ORDER BY occurred_at, event_id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (f *fx) count(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
