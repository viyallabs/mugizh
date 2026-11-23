package consult_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	videofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

// Test pack values (synthetic; shipped code never names a country, K-GLB-01).
const (
	consentVersion = 2
	testCountry    = "ZZ"
)

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC) // slot start

// ---- fakes for the other modules (synthetic data only) ----

type bookFake struct {
	mu      sync.Mutex
	NoShows []string
	Err     error
	byAppt  map[string]bp.Info
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
func (b *bookFake) MarkDoctorNoShow(_ context.Context, ref string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Err != nil {
		return b.Err
	}
	b.NoShows = append(b.NoShows, ref)
	return nil
}
func (b *bookFake) EarliestSlots(context.Context, []string, time.Time) (map[string]time.Time, error) {
	return nil, nil
}

type gateFake struct {
	mu    sync.Mutex
	Deny  bool
	Calls []rp.Request
}

func (g *gateFake) Authorize(_ context.Context, r rp.Request) (rp.Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls = append(g.Calls, r)
	if g.Deny {
		return rp.Decision{Reason: "no_relationship", Hide: true}, nil
	}
	return rp.Decision{Allowed: true, Tier: rp.T1CurrentCare}, nil
}
func (g *gateFake) HasConsent(context.Context, string, string) (bool, error) { return true, nil }

type profFake struct{ Missing bool }

func (p *profFake) ProfileFacts(context.Context, string) (string, int, string, error) {
	if p.Missing {
		return "", 0, "", cp.ErrNotFound
	}
	return "Test Patient", 41, "female", nil
}

type verFake struct {
	mu  sync.Mutex
	Bad bool
}

func (v *verFake) IsVerified(context.Context, string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.Bad, nil
}

var (
	_ bp.Service       = (*bookFake)(nil)
	_ rp.Gate          = (*gateFake)(nil)
	_ ip.ProfileReader = (*profFake)(nil)
)

type fx struct {
	env  *testkit.Env
	m    *consult.Module
	vid  *videofake.Client
	book *bookFake
	gate *gateFake
	prof *profFake
	ver  *verFake
	now  time.Time
	mu   sync.Mutex
}

func (f *fx) clock() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fx) at(t time.Time)   { f.mu.Lock(); f.now = t; f.mu.Unlock() }

func testCfg() consult.Config {
	c := consult.DefaultConfig()
	c.ConsentTextVersion = consentVersion
	c.DeclaredCountry = testCountry
	return c
}

func setup(t testing.TB) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), consult.Source()}})
	f := &fx{env: env, vid: &videofake.Client{}, book: &bookFake{byAppt: map[string]bp.Info{}}, gate: &gateFake{}, prof: &profFake{}, ver: &verFake{}, now: t0}
	m, err := consult.New(env.Pools.For("consult"), testCfg(), consult.Deps{Booking: f.book, Video: f.vid, Gate: f.gate, Profiles: f.prof, Verifier: f.ver},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(f.clock)
	f.m = m
	return f
}

// call is one online appointment with its people.
type call struct {
	appt, patient, doctor, clinic string
	session                       string // consult ID, set by confirm
}

func (c call) patientActor() consult.Actor {
	return consult.Actor{UserID: c.patient, Role: ip.RolePatient}
}
func (c call) doctorActor() consult.Actor {
	return consult.Actor{UserID: c.doctor, Role: ip.RoleDoctor, ClinicRef: c.clinic}
}

func confirmedEvent(c call, mode string, start time.Time) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: "appointment.confirmed", Payload: map[string]string{
		"appointmentRef": c.appt, "patientRef": c.patient, "doctorRef": c.doctor, "clinicRef": c.clinic, "practiceRef": ids.New(), "mode": mode,
		"slotStart": start.UTC().Format(time.RFC3339), "slotEnd": start.Add(30 * time.Minute).UTC().Format(time.RFC3339)}}
}

// confirm creates an appointment.confirmed event and delivers it.
func (f *fx) confirm(t testing.TB, mode string) call {
	t.Helper()
	c := call{appt: ids.New(), patient: ids.New(), doctor: ids.New(), clinic: ids.New()}
	if err := f.m.OnAppointmentConfirmed(context.Background(), confirmedEvent(c, mode, t0)); err != nil {
		t.Fatal(err)
	}
	if mode == "in_person" {
		return c
	}
	v, err := f.m.Get(context.Background(), c.patientActor(), c.appt, true)
	if err != nil {
		t.Fatal(err)
	}
	c.session = v.ID
	return c
}

func (f *fx) preJoinOK(t testing.TB, c call) {
	t.Helper()
	_, err := f.m.PreJoin(context.Background(), c.patientActor(), c.session, consult.PreJoinInput{
		CameraOK: true, MicrophoneOK: true, NetworkOK: true, ConsentTextID: cp.ConsentTextTeleconsult, ConsentTextVersion: consentVersion, InIndiaNow: true})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fx) participantRef(t testing.TB, c call, role string) string {
	t.Helper()
	var ref string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT participant_ref::text FROM consult.participant WHERE session_id=$1 AND role=$2`, c.session, role).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func (f *fx) roomOf(t testing.TB, c call) string {
	t.Helper()
	var room string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT provider_room_ref FROM consult.session WHERE id=$1`, c.session).Scan(&room); err != nil {
		t.Fatal(err)
	}
	return room
}

// vendorJoin simulates the vendor reporting that a role connected (or left) at time at.
func (f *fx) vendor(t testing.TB, c call, role string, kind video.EventKind, at time.Time) {
	t.Helper()
	f.vid.Simulate(f.roomOf(t, c), f.participantRef(t, c, role), kind, at)
}

// bothConnected runs pre-join, both joins, both vendor join events and a sync.
func (f *fx) bothConnected(t testing.TB, c call) {
	t.Helper()
	ctx := context.Background()
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.patientActor(), c.session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	f.vendor(t, c, "doctor", video.Joined, f.clock())
	f.vendor(t, c, "patient", video.Joined, f.clock().Add(time.Second))
	if err := f.m.SyncParticipants(ctx, c.session); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) state(t testing.TB, c call) (state, outcome string) {
	t.Helper()
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT state, outcome FROM consult.session WHERE id=$1`, c.session).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	return
}

// events returns the payloads of emitted events of one type.
func (f *fx) events(t testing.TB, typ string) []map[string]string {
	t.Helper()
	rows, err := f.env.Owner.Query(context.Background(), `SELECT payload FROM consult.event_outbox WHERE type=$1 ORDER BY occurred_at, event_id`, typ)
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
