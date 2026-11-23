package reviews_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

// Test pack value: the name of the pack's prohibited-identifier validator (synthetic setup).
const aadhaarNames = "verhoeff_12_digit_first_2_to_9"

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// bookFake is booking's public interface for the appointments the test registers.
type bookFake struct {
	mu     sync.Mutex
	byAppt map[string]bp.Info
}

func (b *bookFake) PatientOf(context.Context, string) (string, bool, error) { return "", false, nil }
func (b *bookFake) Get(_ context.Context, ref string) (bp.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i, ok := b.byAppt[ref]; ok {
		return i, nil
	}
	return bp.Info{}, bp.ErrNotFound
}
func (b *bookFake) MarkDoctorNoShow(context.Context, string) error { return nil }
func (b *bookFake) EarliestSlots(context.Context, []string, time.Time) (map[string]time.Time, error) {
	return nil, nil
}

type verFake struct {
	mu  sync.Mutex
	bad map[string]bool
	err error
}

func (v *verFake) IsVerified(_ context.Context, ref string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.bad[ref], v.err
}

var _ bp.Service = (*bookFake)(nil)

type fx struct {
	env  *testkit.Env
	m    *reviews.Module
	book *bookFake
	ver  *verFake
	cr   *crypto.Service
	now  time.Time
	mu   sync.Mutex
}

func (f *fx) clock() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fx) at(t time.Time)   { f.mu.Lock(); f.now = t; f.mu.Unlock() }

func testCfg() reviews.Config {
	c := reviews.DefaultConfig()
	c.ProhibitedIDValidators = []string{aadhaarNames}
	return c
}

func setupWith(t testing.TB, mod func(*reviews.Config)) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), reviews.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{env: env, book: &bookFake{byAppt: map[string]bp.Info{}}, ver: &verFake{bad: map[string]bool{}}, cr: cr, now: t0}
	cfg := testCfg()
	if mod != nil {
		mod(&cfg)
	}
	m, err := reviews.New(env.Pools.For("reviews"), cfg, reviews.Deps{Booking: f.book, Verifier: f.ver, Crypto: cr}, slogDiscard())
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(f.clock)
	f.m = m
	return f
}

func setup(t testing.TB) *fx { return setupWith(t, nil) }

// visit is one appointment with its people.
type visit struct{ appt, patient, doctor string }

func (v visit) actor() reviews.Actor { return reviews.Actor{UserID: v.patient, Role: "patient"} }

func completedEvent(v visit, at time.Time) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: "appointment.completed", OccurredAt: at, SubjectRef: v.appt, Payload: map[string]string{
		"appointmentRef": v.appt, "patientRef": v.patient, "doctorRef": v.doctor, "clinicRef": ids.New(), "practiceRef": ids.New()}}
}

func (f *fx) register(v visit, state string) {
	f.book.mu.Lock()
	defer f.book.mu.Unlock()
	f.book.byAppt[v.appt] = bp.Info{ID: v.appt, PatientRef: v.patient, ProfessionalRef: v.doctor, State: state}
}

// completed makes a completed visit of a new patient with the given doctor (a new one when empty).
func (f *fx) completed(t testing.TB, doctor string, at time.Time) visit {
	t.Helper()
	if doctor == "" {
		doctor = ids.New()
	}
	v := visit{appt: ids.New(), patient: ids.New(), doctor: doctor}
	f.register(v, "completed")
	if err := f.m.OnAppointmentCompleted(context.Background(), completedEvent(v, at)); err != nil {
		t.Fatal(err)
	}
	return v
}

// prob returns the problem inside err, or fails.
func prob(t testing.TB, err error) *problem.Problem {
	t.Helper()
	var p *problem.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a problem, got %v", err)
	}
	return p
}

func (f *fx) count(t testing.TB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// aggregate returns n and sum of a doctor's rating row (0, 0 when there is none).
func (f *fx) aggregate(t testing.TB, doctor string) (n, sum int) {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT n, sum FROM reviews.doctor_rating WHERE doctor_ref=$1`, doctor)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&n, &sum); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func (f *fx) events(t testing.TB, typ string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload FROM reviews.event_outbox WHERE type=$1 ORDER BY occurred_at, event_id`, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var p map[string]string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}
