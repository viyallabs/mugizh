package app_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	pfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments/fake"
	pushfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/push/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	videofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// sql reads one value from a module's own schema (test inspection only).
func (j *journey) sql(module, q string, args ...any) string {
	j.t.Helper()
	var s string
	if err := j.c.Pools.For(module).QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		j.t.Fatalf("%s: %v", q, err)
	}
	return s
}

// pushBodies returns the bodies of the push messages sent to a device token so far.
func (j *journey) pushBodies(token string) []string {
	var out []string
	for _, m := range j.c.PushSender.(*pushfake.Sender).Sent() {
		if m.Target.Token == token && !m.Silent {
			out = append(out, m.Body)
		}
	}
	return out
}

// silentPushes counts the content-free refresh pushes (S-165) sent to a device token so far.
func (j *journey) silentPushes(token string) int {
	n := 0
	for _, m := range j.c.PushSender.(*pushfake.Sender).Sent() {
		if m.Target.Token == token && m.Silent && m.Body == "" && m.Data["sync"] == "reminders" {
			n++
		}
	}
	return n
}

func (j *journey) deliverNotifications() {
	j.t.Helper()
	if _, err := j.c.Notify.ProcessDue(context.Background(), j.clock(), 200); err != nil {
		j.t.Fatal(err)
	}
}

// clinicWithPractice has a verified doctor open a clinic, has it verified, and lists a video
// practice that takes payments, issues receipts and has daily 14:00-15:00 video slots.
func (j *journey) clinicWithPractice(doc person) (clinic, practice string, vendor *pfake.Fake) {
	j.t.Helper()
	ctx := context.Background()
	j.must(j.call(http.MethodPut, "/v1/me/professional", doc.token, map[string]any{"displayName": "Dr Asha Rao", "languages": []string{"en"}}),
		http.StatusOK, "doctor profile")
	cr := j.must(j.call(http.MethodPost, "/v1/clinics", doc.token, map[string]any{"name": "Synthetic Care Clinic", "address": "12 Test Street, Sample Town",
		"leadDoctorId": doc.id, "pin": map[string]any{"lat": 12.9716, "lng": 77.5946}}), http.StatusCreated, "submit clinic")
	clinic = cr.str("clinicId")
	j.must(j.call(http.MethodPut, "/v1/clinics/"+clinic+"/verification/draft", doc.token, map[string]any{"authorityName": "Synthetic Registration Authority",
		"legalName": "Synthetic Care Clinic LLP", "registrationNumber": "CE/2026/101", "registrationYear": 2020}), http.StatusOK, "clinic verification draft")
	j.must(j.call(http.MethodPost, "/v1/clinics/"+clinic+"/verification/evidence", doc.token, map[string]any{"kind": verification.EvidenceRegistrationCertificate,
		"contentBase64": jpegBytes(), "contentType": "image/jpeg"}), http.StatusCreated, "clinic evidence")
	j.must(j.call(http.MethodPost, "/v1/clinics/"+clinic+"/verification/submit", doc.token, nil), http.StatusOK, "submit clinic verification")
	if _, err := j.c.Verification.ProcessPending(ctx, 50); err != nil {
		j.t.Fatal(err)
	}
	cv := j.must(j.call(http.MethodGet, "/v1/clinics/"+clinic+"/verification", doc.token, nil), http.StatusOK, "clinic verification status")
	for i, reviewer := range []string{ids.New(), ids.New()} {
		r, err := j.c.Verification.RecordDecision(ctx, verification.DecisionInput{CaseID: cv.str("id"), ReviewerRef: reviewer,
			Outcome: verification.OutcomeVerified, ReasonCode: "register_confirmed_manually"})
		if err != nil {
			j.t.Fatalf("clinic decision %d: %v (case %s)", i+1, err, cv.raw)
		}
		if r.Final {
			break // one reviewer suffices when no override is needed; four eyes otherwise
		}
	}
	j.pump()
	pr := j.must(j.call(http.MethodPost, "/v1/clinics/"+clinic+"/practices", doc.token, map[string]any{"doctorId": doc.id, "feeAmount": 50000,
		"modes": []string{"video", "in_person"}}), http.StatusCreated, "create practice")
	practice = pr.str("practiceId")
	inv := j.must(j.call(http.MethodGet, "/v1/me/practice-invitations", doc.token, nil), http.StatusOK, "invitations")
	items, _ := inv.body["items"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["status"] == "pending" {
			id, _ := m["invitationId"].(string)
			j.must(j.call(http.MethodPost, "/v1/me/practice-invitations/"+id+"/accept", doc.token, nil), http.StatusOK, "accept invitation")
		}
	}
	j.pump()
	link, err := j.c.Payments.LinkClinic(ctx, payments.LinkParams{ClinicRef: clinic, Aggregator: "fake", SettlementMode: "clinic_collected",
		CredentialRef: "secretref:clinics/" + clinic + "/cred", WebhookSecretRef: "secretref:clinics/" + clinic + "/webhook"})
	if err != nil {
		j.t.Fatal(err)
	}
	vendor = j.c.PayVendor.(*pfake.Fake)
	vendor.RegisterClinic(link, "whsec-synthetic-journey", 10_000_000)
	j.must(j.call(http.MethodPut, "/v1/clinics/"+clinic+"/document-profile", doc.token, map[string]any{"legalName": "Synthetic Care Clinic LLP",
		"address": "12 Test Street, Sample Town", "taxStatus": "unregistered"}), http.StatusNoContent, "document profile")
	var rules []map[string]any
	for d := 0; d < 7; d++ {
		rules = append(rules, map[string]any{"weekday": d, "startLocal": "14:00", "endLocal": "15:00", "slotMinutes": 30, "mode": "video"})
	}
	j.must(j.call(http.MethodPut, "/v1/practices/"+practice+"/availability", doc.token, map[string]any{"rules": rules}), http.StatusOK, "availability")
	if _, err := j.c.Booking.MaterialiseSlots(ctx, j.clock()); err != nil {
		j.t.Fatal(err)
	}
	j.pump()
	return clinic, practice, vendor
}

// TestJourneyVideoConsultation is the v1.0 core journey: a verified doctor's clinic is verified
// and lists a video practice; a patient finds it, books, pays, gets a receipt, is told and
// reminded, consults by video with identity confirmed; the doctor sees the shared history until
// the patient revokes it, writes a note and a prescription (blocked while the draft drug table is
// empty), and completes the consultation. Another patient can see none of it.
func TestJourneyVideoConsultation(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	j := newJourney(t, time.Date(2026, 11, 23, 9, 0, 0, 0, zone))
	ctx := context.Background()
	legal := j.c.Config.Pack().Legal()

	doc := j.pro("+919876500101")
	j.verifyDoctor(doc, "Asha Rao", "SMC/101")
	_, practice, vendor := j.clinicWithPractice(doc)

	// ---- the patient finds the practice and books ----
	pat := j.signIn("+919876500201", ip.AppPatient, "")
	j.consent(pat, "P-ACCOUNT", legal.ConsentTextVersions["P-ACCOUNT"])
	j.consent(pat, "P-CARE", legal.ConsentTextVersions["P-CARE"])
	j.must(j.call(http.MethodPut, "/v1/me/profile", pat.token, map[string]any{"fullName": "Ravi Kumar", "dateOfBirth": "1990-04-12", "sex": "male"}),
		http.StatusNoContent, "patient profile")
	j.must(j.call(http.MethodPost, "/v1/me/devices/current/push-token", pat.token, map[string]any{"token": "push-token-synthetic-patient-0001"}), http.StatusNoContent, "push token")
	sr := j.must(j.call(http.MethodPost, "/v1/practices/search", "", map[string]any{"location": map[string]any{"lat": 12.97, "lng": 77.59}, "mode": "video"}),
		http.StatusOK, "search")
	if !bytes.Contains(sr.raw, []byte(practice)) {
		t.Fatalf("the listed practice must be found: %s", sr.raw)
	}
	sl := j.must(j.call(http.MethodGet, "/v1/practices/"+practice+"/slots", pat.token, nil), http.StatusOK, "slots")
	slots, _ := sl.body["items"].([]any)
	if len(slots) == 0 {
		t.Fatalf("no open slots: %s", sl.raw)
	}
	slot, _ := slots[0].(map[string]any)
	startText, _ := slot["startsAt"].(string)
	start, err := time.Parse(time.RFC3339, startText)
	if err != nil {
		t.Fatal(err)
	}
	hold := j.must(j.call(http.MethodPost, "/v1/practices/"+practice+"/holds", pat.token, map[string]any{"slotId": slot["slotId"], "mode": "video",
		"patientTimeZone": "Asia/Kolkata", "patientInIndiaConfirmed": true}), http.StatusCreated, "hold")
	appt := hold.str("appointmentId")
	j.must(j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/checkout", pat.token, nil), http.StatusOK, "checkout")
	providerOrder := j.sql("payments", `SELECT provider_order_id FROM payment_order WHERE appointment_ref=$1`, appt)
	if err := vendor.PayOrder(providerOrder, "pay_journey_1", j.clock()); err != nil {
		t.Fatal(err)
	}
	for _, d := range vendor.DrainDeliveries() {
		if _, err := j.c.Payments.HandleWebhook(ctx, d.ClinicLinkRef, d.Headers, d.Body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.c.Payments.ProcessWebhooks(ctx, 100); err != nil {
		t.Fatal(err)
	}
	j.pump()
	if a := j.must(j.call(http.MethodGet, "/v1/me/appointments/"+appt, pat.token, nil), http.StatusOK, "appointment"); a.str("state") != "confirmed" {
		t.Fatalf("a paid appointment must be confirmed: %s", a.raw)
	}
	if d := j.must(j.call(http.MethodGet, "/v1/me/appointments/"+appt+"/payment-document", pat.token, nil), http.StatusOK, "receipt"); d.str("documentType") != "receipt" {
		t.Fatalf("receipt: %s", d.raw)
	}
	j.deliverNotifications()
	if got := j.pushBodies("push-token-synthetic-patient-0001"); len(got) != 1 || !strings.Contains(got[0], "Dr Asha Rao") || !strings.Contains(got[0], "2:00 PM") {
		t.Fatalf("booking confirmation push: %v", got)
	}

	// ---- S-165: the patient turns reminders on; the phone is told to refresh its on-device backup schedule ----
	j.consent(pat, "P-REMIND", legal.ConsentTextVersions["P-REMIND"])
	j.pump()
	j.deliverNotifications()
	if n := j.silentPushes("push-token-synthetic-patient-0001"); n != 1 {
		t.Fatalf("one silent refresh push after P-REMIND: %d", n)
	}
	ds := j.must(j.call(http.MethodGet, "/v1/me/reminders/device-schedule", pat.token, nil), http.StatusOK, "device schedule")
	if on, _ := ds.body["remindersOn"].(bool); !on {
		t.Fatalf("reminders are on: %s", ds.raw)
	}
	if entries, ok := ds.body["entries"].([]any); !ok || len(entries) != 0 {
		t.Fatalf("no prescription yet, so no medicine reminders: %s", ds.raw)
	}

	// ---- reminders: the short reminder and the consult-starting notice (booked the same day) ----
	for _, at := range []time.Time{start.Add(-2 * time.Hour), start.Add(-10 * time.Minute)} {
		j.at(at)
		if _, err := j.c.Booking.EmitDueReminders(ctx, at); err != nil {
			t.Fatal(err)
		}
		j.pump()
		j.deliverNotifications()
	}
	if got := j.pushBodies("push-token-synthetic-patient-0001"); len(got) != 3 {
		t.Fatalf("confirmation, 2-hour reminder and consult-starting notice: %v", got)
	}

	// Hours have passed: the apps refresh their sessions with device-signed proofs.
	j.refresh(&pat)
	j.proBack(&doc) // the pro session ended after 30 idle minutes

	// ---- the consultation ----
	cs := j.must(j.call(http.MethodGet, "/v1/appointments/"+appt+"/consult", pat.token, nil), http.StatusOK, "consult")
	consultID := cs.str("consultId")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/pre-join", pat.token, map[string]any{
		"consent":     map[string]any{"textId": "TELE-CONSENT", "textVersion": legal.ConsentTextVersions["TELE-CONSENT"], "inIndiaNow": true},
		"deviceCheck": map[string]any{"cameraOk": true, "microphoneOk": true, "networkOk": true}}), http.StatusOK, "pre-join")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/join", pat.token, nil), http.StatusOK, "patient join")
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/join", doc.token, nil), http.StatusOK, "doctor join")
	room := j.sql("consult", `SELECT provider_room_ref FROM session WHERE id=$1`, consultID)
	vid := j.c.Video.(*videofake.Client)
	for _, role := range []string{"patient", "doctor"} {
		ref := j.sql("consult", `SELECT participant_ref::text FROM participant WHERE session_id=$1 AND role=$2`, consultID, role)
		vid.Simulate(room, ref, video.Joined, j.clock())
	}
	if _, err := j.c.Consult.SyncOpenSessions(ctx, j.clock()); err != nil {
		t.Fatal(err)
	}
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/identity-confirmation", doc.token, map[string]any{"nameConfirmed": true, "ageConfirmed": true}),
		http.StatusOK, "identity confirmation")

	// History sharing during the consultation (S-168/S-169): readable while shared, refused after revoke (TC-01).
	sh := j.must(j.call(http.MethodPost, "/v1/me/history-shares", pat.token, map[string]any{"doctorId": doc.id, "appointmentId": appt,
		"textVersion": legal.ConsentTextVersions["P-SHARE"]}), http.StatusCreated, "share history")
	j.must(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", doc.token, nil), http.StatusOK, "doctor reads the shared history")
	j.must(j.call(http.MethodPost, "/v1/me/access-grants/"+sh.str("grantId")+"/revoke", pat.token, nil), http.StatusNoContent, "revoke share")
	if r := j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", doc.token, nil); r.status != http.StatusForbidden || r.str("code") != "consent.required" {
		t.Fatalf("after revoke the next read is refused (TC-01): %d %s", r.status, r.raw)
	}

	// Note and prescription: the empty draft drug table blocks every item (fail closed, K-CS-02).
	j.must(j.call(http.MethodPut, "/v1/appointments/"+appt+"/note", doc.token, map[string]any{"complaint": "synthetic complaint", "findings": "synthetic findings",
		"diagnosis": "synthetic diagnosis", "advice": "rest"}), http.StatusOK, "note")
	j.must(j.call(http.MethodPut, "/v1/appointments/"+appt+"/prescription", doc.token, map[string]any{"lines": []map[string]any{{"ref": "l1",
		"drugRef": "synthetic-drug", "context": "new_drug", "dose": map[string]any{"quantity": 1, "unit": "tablet"}, "frequency": map[string]any{"times": 2, "period": "day"},
		"duration": map[string]any{"value": 3, "unit": "day"}}}}), http.StatusOK, "draft prescription")
	ck := j.must(j.call(http.MethodPost, "/v1/appointments/"+appt+"/prescription/check", doc.token, nil), http.StatusOK, "check prescription")
	if allowed, _ := ck.body["allowed"].(bool); allowed {
		t.Fatalf("an unclassified drug must be blocked: %s", ck.raw)
	}

	// Completion: both sides connected and identity confirmed; booking follows (D-46).
	j.at(start.Add(15 * time.Minute))
	j.refresh(&pat)
	j.refresh(&doc)
	j.must(j.call(http.MethodPost, "/v1/consults/"+consultID+"/outcome", doc.token, map[string]any{"outcome": "completed"}), http.StatusOK, "complete")
	j.pump()
	if a := j.must(j.call(http.MethodGet, "/v1/me/appointments/"+appt, pat.token, nil), http.StatusOK, "appointment"); a.str("state") != "completed" {
		t.Fatalf("a completed consultation completes the appointment: %s", a.raw)
	}

	// ---- another patient sees none of it (K-SEC-04, S-181) ----
	other := j.signIn("+919876500202", ip.AppPatient, "")
	for _, path := range []string{"/v1/me/appointments/" + appt, "/v1/me/appointments/" + appt + "/payment-document", "/v1/appointments/" + appt + "/consult",
		"/v1/me/appointments/" + appt + "/note"} {
		if r := j.call(http.MethodGet, path, other.token, nil); r.status != http.StatusNotFound {
			t.Fatalf("%s as another patient: want 404, got %d %s", path, r.status, r.raw)
		}
	}
	if r := j.call(http.MethodPost, "/v1/consults/"+consultID+"/join", other.token, nil); r.status != http.StatusNotFound {
		t.Fatalf("joining someone else's consultation: %d %s", r.status, r.raw)
	}
}
