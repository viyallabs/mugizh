package consult_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

func code(err error) string {
	var p *problem.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if code(err) != want {
		t.Fatalf("got %v, want problem %s", err, want)
	}
}

func TestS135SessionPerConfirmedOnlineAppointment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	// A duplicate event and a rescheduled event do not create a second session.
	if err := f.m.OnAppointmentConfirmed(ctx, confirmedEvent(c, "video", t0)); err != nil {
		t.Fatal(err)
	}
	moved := confirmedEvent(c, "video", t0.Add(2*time.Hour))
	if err := f.m.OnAppointmentRescheduled(ctx, moved); err != nil {
		t.Fatal(err)
	}
	v, err := f.m.Get(ctx, c.patientActor(), c.appt, true)
	if err != nil || v.ID != c.session || !v.SlotStart.Equal(t0.Add(2*time.Hour)) || v.State != consult.StateScheduled {
		t.Fatalf("session: %+v %v", v, err)
	}
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.session`).Scan(&n)
	if n != 1 {
		t.Fatalf("sessions = %d", n)
	}
	// In-person appointments get no session.
	ip := f.confirm(t, "in_person")
	if _, err := f.m.SessionFacts(ctx, ip.appt); !errors.Is(err, cp.ErrNotFound) {
		t.Fatalf("in-person facts: %v", err)
	}
	// Malformed events are errors (retried), never silently dropped.
	bad := confirmedEvent(c, "video", t0)
	bad.ID = ids.New()
	bad.Payload["patientRef"] = "not-an-id"
	if err := f.m.OnAppointmentConfirmed(ctx, bad); err == nil {
		t.Fatal("bad event accepted")
	}
}

// TC-24 (P10): a remote consult cannot start without "I am in India now".
func TestTC24RemoteConsultNeedsInIndiaDeclaration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	_, err := f.m.Join(ctx, c.patientActor(), c.session)
	wantCode(t, err, "consult.consent_required")

	in := consult.PreJoinInput{CameraOK: true, MicrophoneOK: true, NetworkOK: true, ConsentTextID: cp.ConsentTextTeleconsult, ConsentTextVersion: consentVersion}
	_, err = f.m.PreJoin(ctx, c.patientActor(), c.session, in) // InIndiaNow false
	wantCode(t, err, "consult.patient_location_not_allowed")
	_, err = f.m.Join(ctx, c.patientActor(), c.session)
	wantCode(t, err, "consult.patient_location_not_allowed")
	facts, err := f.m.SessionFacts(ctx, c.appt)
	if err != nil || facts.PatientInIndia || facts.PatientCountry != "" {
		t.Fatalf("facts after refusal: %+v %v", facts, err)
	}
	if len(f.vid.Rooms) != 0 {
		t.Fatal("a room was created for a patient outside India")
	}

	in.InIndiaNow = true
	if _, err := f.m.PreJoin(ctx, c.patientActor(), c.session, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Join(ctx, c.patientActor(), c.session); err != nil {
		t.Fatal(err)
	}
	facts, _ = f.m.SessionFacts(ctx, c.appt)
	if !facts.PatientInIndia || facts.PatientCountry != testCountry || !facts.ExplicitConsent || facts.Initiator != "patient" {
		t.Fatalf("facts: %+v", facts)
	}
}

func TestS135PreJoinConsentTextAndDeviceCheck(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	ok := consult.PreJoinInput{CameraOK: true, MicrophoneOK: true, NetworkOK: true, ConsentTextID: cp.ConsentTextTeleconsult, ConsentTextVersion: consentVersion, InIndiaNow: true}

	old := ok
	old.ConsentTextVersion = 1
	_, err := f.m.PreJoin(ctx, c.patientActor(), c.session, old)
	wantCode(t, err, "consult.consent_stale")

	noMic := ok
	noMic.MicrophoneOK = false
	_, err = f.m.PreJoin(ctx, c.patientActor(), c.session, noMic)
	wantCode(t, err, "consult.device_check_failed")

	noCam := ok
	noCam.CameraOK = false
	_, err = f.m.PreJoin(ctx, c.patientActor(), c.session, noCam)
	wantCode(t, err, "consult.device_check_failed")
	noCam.AcceptAudioFallback = true // the patient accepts an audio call
	if _, err := f.m.PreJoin(ctx, c.patientActor(), c.session, noCam); err != nil {
		t.Fatal(err)
	}
	facts, _ := f.m.SessionFacts(ctx, c.appt)
	if facts.ModeAtSigning != "audio" || facts.BookedMode != "video" {
		t.Fatalf("mode: %+v", facts)
	}
	// Only the patient records consent.
	_, err = f.m.PreJoin(ctx, c.doctorActor(), c.session, ok)
	wantCode(t, err, "auth.forbidden")
	// Another patient cannot see or touch it (K-SEC-04).
	other := consult.Actor{UserID: ids.New(), Role: ip.RolePatient}
	_, err = f.m.PreJoin(ctx, other, c.session, ok)
	wantCode(t, err, "resource.not_found")
}

func TestS136JoinWindowAndStranger(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	f.at(t0.Add(-11 * time.Minute))
	_, err := f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.outside_window")
	f.at(t0.Add(30*time.Minute + 16*time.Minute))
	_, err = f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.outside_window")
	f.at(t0.Add(-9 * time.Minute))
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	// A doctor who is not this appointment's doctor gets 404.
	other := consult.Actor{UserID: ids.New(), Role: ip.RoleDoctor}
	_, err = f.m.Join(ctx, other, c.session)
	wantCode(t, err, "resource.not_found")
	_, err = f.m.Get(ctx, other, c.appt, true)
	wantCode(t, err, "resource.not_found")
	// An unverified doctor cannot join (K-CS-01).
	f.ver.mu.Lock()
	f.ver.Bad = true
	f.ver.mu.Unlock()
	_, err = f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.doctor_not_verified")
}

// ADR-13: tokens only for the two participants, bound to the room, short-lived; no recording.
func TestS136TokensBoundAndNoRecording(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	pt, err := f.m.Join(ctx, c.patientActor(), c.session)
	if err != nil {
		t.Fatal(err)
	}
	dt, err := f.m.Join(ctx, c.doctorActor(), c.session)
	if err != nil {
		t.Fatal(err)
	}
	room := f.roomOf(t, c)
	pRoom, pRef, ok1 := f.vid.Validate(pt.Token, f.clock())
	dRoom, dRef, ok2 := f.vid.Validate(dt.Token, f.clock())
	if !ok1 || !ok2 || pRoom != room || dRoom != room || pRef == dRef {
		t.Fatalf("tokens not bound: %v %v %s %s", ok1, ok2, pRef, dRef)
	}
	if pRef != f.participantRef(t, c, "patient") || pRef == c.patient || dRef == c.doctor {
		t.Fatal("the vendor must see an opaque participant reference, not the user ID")
	}
	if !pt.ExpiresAt.Before(f.clock().Add(11 * time.Minute)) {
		t.Fatalf("token not short-lived: %v", pt.ExpiresAt)
	}
	if _, _, ok := f.vid.Validate(pt.Token, pt.ExpiresAt.Add(time.Second)); ok {
		t.Fatal("expired token accepted")
	}
	req := f.vid.Rooms[room]
	if req.SessionRef != c.session || req.AudioOnly {
		t.Fatalf("room request: %+v", req)
	}
	// Tokens are never stored.
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='consult' AND column_name ILIKE '%token%' AND column_name NOT IN ('tokens_issued','last_token_at')`).Scan(&n)
	if n != 0 {
		t.Fatal("a token column exists")
	}
}

func TestS136RoomReportingRecordingIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.vid.BrokenRecording = true
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	_, err := f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.room_unavailable")
	if len(f.vid.Ended) != 1 || len(f.vid.Tokens) != 0 {
		t.Fatalf("a recording room must be closed and get no token: ended=%v tokens=%d", f.vid.Ended, len(f.vid.Tokens))
	}
	if st, _ := f.state(t, c); st != consult.StateScheduled {
		t.Fatalf("state = %s", st)
	}
}

// Fallback (adapter section 9): video room fails -> audio room, recorded; both fail -> 503.
func TestS136AudioFallbackWhenVideoRoomFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.vid.FailCreateVideo = adapters.E(adapters.Unavailable, "VIDEO")
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	r, err := f.m.Join(ctx, c.doctorActor(), c.session)
	if err != nil || r.Mode != "audio" {
		t.Fatalf("join: %+v %v", r, err)
	}
	facts, _ := f.m.SessionFacts(ctx, c.appt)
	if facts.ModeAtSigning != "audio" {
		t.Fatalf("mode at signing = %s", facts.ModeAtSigning)
	}
	var reason string
	_ = f.env.Owner.QueryRow(ctx, `SELECT reason FROM consult.mode_change WHERE session_id=$1`, c.session).Scan(&reason)
	if reason != "room_fallback" {
		t.Fatalf("reason = %q", reason)
	}

	f2 := setup(t)
	f2.vid.FailCreateVideo = adapters.E(adapters.Unavailable, "VIDEO")
	f2.vid.FailCreateAudio = adapters.E(adapters.Timeout, "AUDIO")
	c2 := f2.confirm(t, "video")
	f2.preJoinOK(t, c2)
	_, err = f2.m.Join(ctx, c2.doctorActor(), c2.session)
	wantCode(t, err, "consult.room_unavailable")
	if st, _ := f2.state(t, c2); st != consult.StateScheduled {
		t.Fatalf("state = %s", st)
	}
}

// S-137 / TC-32: both sides connect; a drop never completes the consult; rejoin works.
func TestTC32DropIsNeverAutoCompletedAndRejoinWorks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.bothConnected(t, c)
	if st, _ := f.state(t, c); st != consult.StateBothConnected {
		t.Fatalf("state = %s", st)
	}
	if ev := f.events(t, cp.EventBothConnected); len(ev) != 1 || ev[0]["appointmentRef"] != c.appt || ev[0]["sessionRef"] != c.session {
		t.Fatalf("both_connected events: %v", ev)
	}
	// Syncing again is harmless and emits nothing more.
	_ = f.m.SyncParticipants(ctx, c.session)
	if len(f.events(t, cp.EventBothConnected)) != 1 {
		t.Fatal("both_connected emitted twice")
	}

	// The network drops: patient leaves, mode falls back to audio.
	f.at(t0.Add(5 * time.Minute))
	f.vendor(t, c, "patient", video.Left, f.clock())
	if n, err := f.m.SyncOpenSessions(ctx, f.clock()); err != nil || n != 1 {
		t.Fatalf("sync: %d %v", n, err)
	}
	if _, err := f.m.ChangeMode(ctx, c.patientActor(), c.session, "audio", "network"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.ChangeMode(ctx, c.doctorActor(), c.session, "audio", "network"); err != nil {
		t.Fatal(err) // repeating is harmless
	}
	// A long silence later, still nothing ended it.
	f.at(t0.Add(25 * time.Minute))
	_, _ = f.m.SyncOpenSessions(ctx, f.clock())
	if n, err := f.m.DetectDoctorNoShow(ctx, f.clock()); err != nil || n != 0 {
		t.Fatalf("doctor no-show on a live consult: %d %v", n, err)
	}
	if st, out := f.state(t, c); st != consult.StateBothConnected || out != "" {
		t.Fatalf("auto-ended: %s %q", st, out)
	}
	if len(f.events(t, cp.EventEnded)) != 0 {
		t.Fatal("consult.ended emitted on a drop")
	}

	// Rejoin: a new token, then the vendor reports the patient back.
	t1, err := f.m.Join(ctx, c.patientActor(), c.session)
	if err != nil || t1.Token == "" || t1.Mode != "audio" {
		t.Fatalf("rejoin: %+v %v", t1, err)
	}
	f.vendor(t, c, "patient", video.Joined, f.clock())
	_ = f.m.SyncParticipants(ctx, c.session)

	facts, _ := f.m.SessionFacts(ctx, c.appt)
	if facts.ModeAtSigning != "audio" || facts.BookedMode != "video" || !facts.BothConnected {
		t.Fatalf("facts: %+v", facts)
	}
	// Audio cannot go back up to video.
	_, err = f.m.ChangeMode(ctx, c.patientActor(), c.session, "video", "network")
	wantCode(t, err, "request.invalid")
}

// Webhook path: events passed by room reference are applied the same way.
func TestS137WebhookEventsApplied(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "audio")
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.patientActor(), c.session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	room := f.roomOf(t, c)
	evs := []video.Event{
		{ID: "a", ParticipantRef: f.participantRef(t, c, "doctor"), Kind: video.Joined, At: t0},
		{ID: "b", ParticipantRef: f.participantRef(t, c, "patient"), Kind: video.Joined, At: t0.Add(time.Second)},
		{ID: "x", ParticipantRef: ids.New(), Kind: video.Joined, At: t0}, // a stranger is ignored
	}
	if err := f.m.RecordParticipantEvents(ctx, room, evs); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RecordParticipantEvents(ctx, room, evs); err != nil { // replay
		t.Fatal(err)
	}
	if err := f.m.RecordParticipantEvents(ctx, "unknown-room", evs); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.state(t, c); st != consult.StateBothConnected {
		t.Fatalf("state = %s", st)
	}
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.participant_event`).Scan(&n)
	if n != 2 {
		t.Fatalf("events stored = %d", n)
	}
}

// S-147 / TC-34: the doctor must confirm name and age before the composer is usable.
func TestTC34DoctorConfirmsIdentityBeforeComposer(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	// Not connected yet.
	_, err := f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true)
	wantCode(t, err, "consult.not_connected")
	if facts, _ := f.m.SessionFacts(ctx, c.appt); facts.IdentityConfirmed {
		t.Fatal("identity confirmed too early")
	}

	f.bothConnected2(t, c)
	// The doctor sees the name and age through the records gate, never the patient's other data.
	v, err := f.m.Get(ctx, c.doctorActor(), c.session, false)
	if err != nil || v.Patient == nil || v.Patient.FullName != "Test Patient" || v.Patient.AgeYears != 41 {
		t.Fatalf("doctor view: %+v %v", v.Patient, err)
	}
	last := f.gate.Calls[len(f.gate.Calls)-1]
	if last.Kind != rp.KindIdentityBasic || last.BookingRef != c.appt || last.PatientRef != c.patient || last.Actor.Ref != c.doctor {
		t.Fatalf("gate request: %+v", last)
	}
	// The patient's own view has no identity block.
	pv, _ := f.m.Get(ctx, c.patientActor(), c.session, false)
	if pv.Patient != nil {
		t.Fatal("patient view must not carry the identity block")
	}
	// Both must be confirmed; the patient cannot confirm.
	_, err = f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, false)
	wantCode(t, err, "consult.identity_not_confirmed")
	_, err = f.m.ConfirmIdentity(ctx, c.patientActor(), c.session, true, true)
	wantCode(t, err, "auth.forbidden")

	// If the gate says no, or the profile is missing, the doctor cannot confirm blind.
	f.gate.Deny = true
	v, _ = f.m.Get(ctx, c.doctorActor(), c.session, false)
	if v.Patient != nil {
		t.Fatal("identity shown although the gate denied")
	}
	_, err = f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true)
	wantCode(t, err, "consult.identity_unavailable")
	f.gate.Deny = false
	f.prof.Missing = true
	_, err = f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true)
	wantCode(t, err, "consult.identity_unavailable")
	f.prof.Missing = false

	v, err = f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true)
	if err != nil || !v.IdentityConfirmed {
		t.Fatalf("confirm: %+v %v", v, err)
	}
	if facts, _ := f.m.SessionFacts(ctx, c.appt); !facts.IdentityConfirmed {
		t.Fatal("facts do not show the confirmation")
	}
}

// bothConnected2 connects the patient to a session whose doctor already joined.
func (f *fx) bothConnected2(t testing.TB, c call) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.m.Join(ctx, c.patientActor(), c.session); err != nil {
		t.Fatal(err)
	}
	f.vendor(t, c, "doctor", video.Joined, f.clock())
	f.vendor(t, c, "patient", video.Joined, f.clock().Add(time.Second))
	if err := f.m.SyncParticipants(ctx, c.session); err != nil {
		t.Fatal(err)
	}
}

// E-BK-08 / TC-27: completed needs both sides; under two minutes is flagged.
func TestTC27CompletedNeedsBothSidesAndShortCallIsFlagged(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	f.vendor(t, c, "doctor", video.Joined, f.clock())
	_ = f.m.SyncParticipants(ctx, c.session)
	// Only the doctor joined: "completed" cannot be set.
	_, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "completed")
	wantCode(t, err, "consult.not_completable")

	f.bothConnected2(t, c)
	// Both connected but identity not confirmed (P02).
	_, err = f.m.SetOutcome(ctx, c.doctorActor(), c.session, "completed")
	wantCode(t, err, "consult.identity_required")
	// The patient cannot complete it.
	_, err = f.m.SetOutcome(ctx, c.patientActor(), c.session, "completed")
	wantCode(t, err, "consult.outcome_not_allowed")
	if _, err := f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true); err != nil {
		t.Fatal(err)
	}
	f.at(t0.Add(90 * time.Second)) // under 2 minutes
	v, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "completed")
	if err != nil || v.State != consult.StateEnded || v.Outcome != "completed" || !v.ShortCall {
		t.Fatalf("complete: %+v %v", v, err)
	}
	ev := f.events(t, cp.EventEnded)
	if len(ev) != 1 || ev[0]["outcome"] != "completed" || ev[0]["appointmentRef"] != c.appt || ev[0]["sessionRef"] != c.session ||
		ev[0]["flag"] != "short_call" || ev[0]["endedAt"] != f.clock().Format(time.RFC3339) {
		t.Fatalf("consult.ended: %v", ev)
	}
	// Same outcome again: harmless, one event. A different one: refused.
	if _, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "completed"); err != nil {
		t.Fatal(err)
	}
	_, err = f.m.SetOutcome(ctx, c.doctorActor(), c.session, "technical_failure")
	wantCode(t, err, "consult.closed")
	if len(f.events(t, cp.EventEnded)) != 1 {
		t.Fatal("ended emitted twice")
	}
	if len(f.vid.Ended) != 1 {
		t.Fatalf("room not ended: %v", f.vid.Ended)
	}

	// A long call is not flagged.
	g := f.confirm(t, "video")
	f.at(t0)
	f.bothConnected(t, g)
	if _, err := f.m.ConfirmIdentity(ctx, g.doctorActor(), g.session, true, true); err != nil {
		t.Fatal(err)
	}
	f.at(t0.Add(20 * time.Minute))
	v, err = f.m.SetOutcome(ctx, g.doctorActor(), g.session, "completed")
	if err != nil || v.ShortCall {
		t.Fatalf("long call: %+v %v", v, err)
	}
}

// E-TC-02 / TC-33: converting to in-person records the outcome and the mode.
func TestTC33ConvertToInPerson(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.bothConnected(t, c)
	if _, err := f.m.SetOutcome(ctx, c.patientActor(), c.session, "converted_in_person"); code(err) != "consult.outcome_not_allowed" {
		t.Fatalf("patient converted: %v", err)
	}
	v, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "converted_in_person")
	if err != nil || v.State != consult.StateEnded || v.CurrentMode != "in_person" {
		t.Fatalf("convert: %+v %v", v, err)
	}
	facts, _ := f.m.SessionFacts(ctx, c.appt)
	if facts.ModeAtSigning != "in_person" || facts.Outcome != "converted_in_person" {
		t.Fatalf("facts: %+v", facts)
	}
	if ev := f.events(t, cp.EventEnded); len(ev) != 1 || ev[0]["outcome"] != "converted_in_person" {
		t.Fatalf("events: %v", ev)
	}
	// Not possible if the patient never connected.
	g := f.confirm(t, "video")
	_, err = f.m.SetOutcome(ctx, g.doctorActor(), g.session, "converted_in_person")
	wantCode(t, err, "consult.not_completable")
}

func TestS148PatientNoShow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	f.vendor(t, c, "doctor", video.Joined, t0)
	f.at(t0.Add(5 * time.Minute)) // before the grace period
	_, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "patient_no_show")
	wantCode(t, err, "consult.not_no_show")
	f.at(t0.Add(11 * time.Minute))
	v, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "patient_no_show")
	if err != nil || v.State != consult.StateNoShow {
		t.Fatalf("no-show: %+v %v", v, err)
	}
	if ev := f.events(t, cp.EventEnded); len(ev) != 1 || ev[0]["outcome"] != "patient_no_show" {
		t.Fatalf("events: %v", ev)
	}

	// The patient did connect: no no-show.
	g := f.confirm(t, "video")
	f.at(t0)
	f.bothConnected(t, g)
	f.at(t0.Add(12 * time.Minute))
	_, err = f.m.SetOutcome(ctx, g.doctorActor(), g.session, "patient_no_show")
	wantCode(t, err, "consult.not_no_show")

	// Unknown connection state fails closed.
	h := f.confirm(t, "video")
	f.at(t0)
	f.preJoinOK(t, h)
	_, _ = f.m.Join(ctx, h.doctorActor(), h.session)
	f.vid.FailEvents = adapters.E(adapters.Timeout, "EVENTS")
	f.at(t0.Add(12 * time.Minute))
	_, err = f.m.SetOutcome(ctx, h.doctorActor(), h.session, "patient_no_show")
	wantCode(t, err, "consult.unavailable")
}

func TestS148TechnicalFailureByPatientOrDoctor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	_, _ = f.m.Join(ctx, c.patientActor(), c.session)
	v, err := f.m.SetOutcome(ctx, c.patientActor(), c.session, "technical_failure")
	if err != nil || v.State != consult.StateFailedTech {
		t.Fatalf("tech failure: %+v %v", v, err)
	}
	if ev := f.events(t, cp.EventEnded); len(ev) != 1 || ev[0]["outcome"] != "technical_failure" {
		t.Fatalf("events: %v", ev)
	}
	// Nothing can be done with an ended consult.
	_, err = f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.closed")
	_, err = f.m.SetOutcome(ctx, c.doctorActor(), c.session, "bogus")
	wantCode(t, err, "request.invalid")
}

// Review F2: a technical failure needs the join window and a side that tried to join, so
// nobody can end a consult days early or dodge a no-show by declaring a failure; a vendor
// outage that stops every room still lets the side that tried record it.
func TestReviewF2TechnicalFailureNeedsAnAttempt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	f.at(t0.Add(-2 * time.Hour))
	_, err := f.m.SetOutcome(ctx, c.patientActor(), c.session, "technical_failure")
	wantCode(t, err, "consult.outside_window")
	f.at(t0)
	_, err = f.m.SetOutcome(ctx, c.patientActor(), c.session, "technical_failure")
	wantCode(t, err, "consult.not_technical_failure") // never tried to join
	_, err = f.m.SetOutcome(ctx, c.doctorActor(), c.session, "technical_failure")
	wantCode(t, err, "consult.not_technical_failure")

	// Every room fails: the join returns 503, but the attempt is recorded.
	f.vid.FailCreateVideo = errors.New("vendor down")
	f.vid.FailCreateAudio = errors.New("vendor down")
	_, err = f.m.Join(ctx, c.patientActor(), c.session)
	wantCode(t, err, "consult.room_unavailable")
	v, err := f.m.SetOutcome(ctx, c.patientActor(), c.session, "technical_failure")
	if err != nil || v.State != consult.StateFailedTech {
		t.Fatalf("after a failed attempt: %+v %v", v, err)
	}
}

// Review F4: every pre-join answer stays in the audit trail with the consent text version,
// even though the pre-join record keeps only the latest one.
func TestReviewF4PreJoinAnswersAreAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.at(t0)
	in := consult.PreJoinInput{ConsentTextID: cp.ConsentTextTeleconsult, ConsentTextVersion: consentVersion, CameraOK: true, MicrophoneOK: true, NetworkOK: true}
	_, err := f.m.PreJoin(ctx, c.patientActor(), c.session, in) // not in the country
	wantCode(t, err, "consult.patient_location_not_allowed")
	in.InIndiaNow = true
	if _, err := f.m.PreJoin(ctx, c.patientActor(), c.session, in); err != nil {
		t.Fatal(err)
	}
	rows, err := f.env.Owner.Query(ctx, `SELECT purpose, reason_code FROM audit.audit_outbox WHERE action='consult.pre_join' AND resource_ref=$1 ORDER BY occurred_at, event_id`, c.session)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p, r string
		_ = rows.Scan(&p, &r)
		got = append(got, p+"|"+r)
	}
	want := fmt.Sprintf("%s:%d|", cp.ConsentTextTeleconsult, consentVersion)
	if len(got) != 2 || !slices.Contains(got, want+"in_country_not_declared") || !slices.Contains(got, want+"in_country_declared") {
		t.Fatalf("audit trail: %v", got)
	}
}

// E-BK-04 / TC-25: the doctor is absent 10 minutes after the start.
func TestTC25DoctorNoShow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	absent := f.confirm(t, "video")
	present := f.confirm(t, "video")
	f.preJoinOK(t, present)
	if _, err := f.m.Join(ctx, present.doctorActor(), present.session); err != nil {
		t.Fatal(err)
	}
	f.vendor(t, present, "doctor", video.Joined, t0.Add(time.Minute))

	f.at(t0.Add(9 * time.Minute))
	if n, err := f.m.DetectDoctorNoShow(ctx, f.clock()); err != nil || n != 0 || len(f.book.NoShows) != 0 {
		t.Fatalf("too early: %d %v %v", n, err, f.book.NoShows)
	}
	// Review F6: a doctor who was just issued a join token may still be connecting, so the job
	// waits until that token has expired.
	late := f.confirm(t, "video")
	f.at(t0.Add(9*time.Minute + 30*time.Second))
	if _, err := f.m.Join(ctx, late.doctorActor(), late.session); err != nil {
		t.Fatal(err)
	}
	f.at(t0.Add(10 * time.Minute))
	n, err := f.m.DetectDoctorNoShow(ctx, f.clock())
	if err != nil || n != 1 {
		t.Fatalf("detect: %d %v", n, err)
	}
	if st, _ := f.state(t, late); st == consult.StateNoShow {
		t.Fatal("a doctor still connecting was marked no-show")
	}
	// Consult decides; booking learns it from consult.ended (never a direct call, review F6).
	if len(f.book.NoShows) != 0 {
		t.Fatalf("booking must not be called directly: %v", f.book.NoShows)
	}
	if st, out := f.state(t, absent); st != consult.StateNoShow || out != "doctor_no_show" {
		t.Fatalf("absent: %s %s", st, out)
	}
	if st, _ := f.state(t, present); st == consult.StateNoShow {
		t.Fatal("a doctor who joined was marked no-show")
	}
	if ev := f.events(t, cp.EventEnded); len(ev) != 1 || ev[0]["outcome"] != "doctor_no_show" || ev[0]["appointmentRef"] != absent.appt {
		t.Fatalf("events: %v", ev)
	}
	// Running again changes nothing.
	if n, err := f.m.DetectDoctorNoShow(ctx, f.clock()); err != nil || n != 0 {
		t.Fatalf("rerun: %d %v", n, err)
	}
	// Once the late doctor's token has expired without a connection, the no-show stands.
	f.at(t0.Add(20 * time.Minute))
	if n, err := f.m.DetectDoctorNoShow(ctx, f.clock()); err != nil || n != 1 {
		t.Fatalf("after the token expired: %d %v", n, err)
	}
	if st, out := f.state(t, late); st != consult.StateNoShow || out != "doctor_no_show" {
		t.Fatalf("late: %s %s", st, out)
	}
}

// E-FAM-04 / TC-48: a minor stops the consult and flags the account; identity is never confirmed.
func TestTC48MinorDiscoveredStopsConsult(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.bothConnected(t, c)
	_, err := f.m.ReportMinor(ctx, c.patientActor(), c.session)
	wantCode(t, err, "auth.forbidden")
	v, err := f.m.ReportMinor(ctx, c.doctorActor(), c.session)
	if err != nil || v.State != consult.StateEnded || v.Outcome != "minor_stopped" || v.IdentityConfirmed {
		t.Fatalf("minor: %+v %v", v, err)
	}
	if ev := f.events(t, cp.EventEnded); len(ev) != 1 || ev[0]["outcome"] != "minor_stopped" {
		t.Fatalf("ended: %v", ev)
	}
	if ev := f.events(t, cp.EventMinorFlagged); len(ev) != 1 || ev[0]["patientRef"] != c.patient {
		t.Fatalf("minor_flagged: %v", ev)
	}
	facts, _ := f.m.SessionFacts(ctx, c.appt)
	if !facts.MinorFlagged || facts.IdentityConfirmed {
		t.Fatalf("facts: %+v", facts)
	}
	_, err = f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true)
	wantCode(t, err, "consult.closed")
	if _, err := f.m.ReportMinor(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(f.events(t, cp.EventMinorFlagged)) != 1 {
		t.Fatal("flag emitted twice")
	}
}

func TestS135CancelledAppointmentClosesSession(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.preJoinOK(t, c)
	if _, err := f.m.Join(ctx, c.doctorActor(), c.session); err != nil {
		t.Fatal(err)
	}
	e := outbox.Event{ID: ids.New(), Type: "appointment.cancelled", Payload: map[string]string{"appointmentRef": c.appt, "byWhom": "doctor"}}
	if err := f.m.OnAppointmentCancelled(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnAppointmentCancelled(ctx, e); err != nil { // replay
		t.Fatal(err)
	}
	if st, _ := f.state(t, c); st != consult.StateCancelled {
		t.Fatalf("state = %s", st)
	}
	if len(f.vid.Ended) != 1 || len(f.events(t, cp.EventEnded)) != 0 {
		t.Fatalf("room ended %v, ended events %d", f.vid.Ended, len(f.events(t, cp.EventEnded)))
	}
	_, err := f.m.Join(ctx, c.doctorActor(), c.session)
	wantCode(t, err, "consult.closed")
	// Unknown appointment: nothing to do.
	if err := f.m.OnAppointmentCancelled(ctx, outbox.Event{ID: ids.New(), Type: "appointment.cancelled", Payload: map[string]string{"appointmentRef": ids.New()}}); err != nil {
		t.Fatal(err)
	}
}

// Everything is audited, and nothing personal is in the consult schema's events or tables.
func TestS148AuditAndNoPersonalDataInEvents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	c := f.confirm(t, "video")
	f.bothConnected(t, c)
	if _, err := f.m.ConfirmIdentity(ctx, c.doctorActor(), c.session, true, true); err != nil {
		t.Fatal(err)
	}
	f.at(t0.Add(10 * time.Minute))
	if _, err := f.m.SetOutcome(ctx, c.doctorActor(), c.session, "completed"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action LIKE 'consult.%'`).Scan(&n)
	if n < 5 {
		t.Fatalf("audit events = %d", n)
	}
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.event_outbox WHERE payload::text ILIKE '%Test Patient%'`).Scan(&n)
	if n != 0 {
		t.Fatal("a name is in an event")
	}
}
