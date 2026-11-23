package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	schedfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	videofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
)

// deviceSchedule fetches the patient's on-device backup schedule and returns its entries.
func (j *journey) deviceSchedule(p person) (on bool, entries []map[string]any) {
	j.t.Helper()
	r := j.must(j.call(http.MethodGet, "/v1/me/reminders/device-schedule", p.token, nil), http.StatusOK, "device schedule")
	on, _ = r.body["remindersOn"].(bool)
	list, _ := r.body["entries"].([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		entries = append(entries, m)
	}
	return on, entries
}

// TestJourneyPrescriptionRemindersXX runs the prescribing half of the v1.0 journey (S-174) on the synthetic test
// country XX, whose pack has an approved synthetic drug table and a fake e-signature: the whole system boots on a
// pack that shares no value with India (TC-53, K-GLB-01). A verified doctor prescribes after a video consult; the
// rules allow the item; the doctor steps up and signs; reminders are scheduled (P-REMIND); the patient's phone gets
// its 72-hour backup schedule (S-165) with neutral text until P-MEDNAMES; a dose marked taken leaves the schedule;
// switching the item off empties it; the prescription is in the patient's records and verifies publicly.
func TestJourneyPrescriptionRemindersXX(t *testing.T) {
	pack := "XX"
	j := newJourneyIn(t, time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC), pack)
	zone, err := time.LoadLocation(j.c.Config.Pack().Locale().DefaultTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	j.at(time.Date(2026, 11, 23, 9, 0, 0, 0, zone))
	ctx := context.Background()
	legal := j.c.Config.Pack().Legal()
	if legal.ConsentTextVersions["P-SHARE"] == 1 || j.c.Config.Pack().Money().Currency == "INR" {
		t.Fatal("the XX pack must differ from the India pack")
	}

	doc := j.pro("+351912340101")
	j.verifyDoctor(doc, "Ana Sousa", "XX/101")
	_, practice, vendor := j.clinicWithPractice(doc)
	pat := j.patient("+351912340201", "push-token-synthetic-xx-patient-01")
	j.consent(pat, "P-REMIND", legal.ConsentTextVersions["P-REMIND"])
	appt, start := j.bookAndPay(pat, practice, vendor, j.clock())

	// ---- the video consultation ----
	j.at(start.Add(-2 * time.Minute))
	j.refresh(&pat)
	j.proBack(&doc)
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

	// ---- note, prescription, rules, step-up, signature ----
	j.must(j.call(http.MethodPut, "/v1/appointments/"+appt+"/note", doc.token, map[string]any{"complaint": "synthetic complaint", "findings": "synthetic findings",
		"diagnosis": "synthetic diagnosis", "advice": "rest"}), http.StatusOK, "note")
	j.must(j.call(http.MethodPut, "/v1/appointments/"+appt+"/prescription", doc.token, map[string]any{"lines": []map[string]any{{"ref": "l1",
		"drugRef": "xx-alpha-10", "context": "new_drug", "route": "oral", "dose": map[string]any{"quantity": 1, "unit": "tablet"}, "frequency": map[string]any{"times": 2, "period": "day"},
		"duration": map[string]any{"value": 5, "unit": "day"}}}}), http.StatusOK, "draft prescription")
	ck := j.must(j.call(http.MethodPost, "/v1/appointments/"+appt+"/prescription/check", doc.token, nil), http.StatusOK, "check prescription")
	if allowed, _ := ck.body["allowed"].(bool); !allowed {
		t.Fatalf("a list A drug in a first video consult is allowed by the synthetic rules: %s", ck.raw)
	}
	if r := j.call(http.MethodPost, "/v1/appointments/"+appt+"/prescription/sign", doc.token, nil); r.status != http.StatusForbidden || r.str("code") != "auth.step_up_required" {
		t.Fatalf("signing needs a step-up first (P08): %d %s", r.status, r.raw)
	}
	j.at(j.clock().Add(31 * time.Second)) // a TOTP code is accepted once: use the next window, not the MFA one
	code, _ := totp.GenerateCode(doc.totp, j.clock())
	j.must(j.call(http.MethodPost, "/v1/auth/step-up", doc.token, map[string]any{"code": code}), http.StatusNoContent, "step-up")
	sg := j.call(http.MethodPost, "/v1/appointments/"+appt+"/prescription/sign", doc.token, nil)
	if sg.status != http.StatusOK && sg.status != http.StatusCreated {
		t.Fatalf("sign: %d %s", sg.status, sg.raw)
	}
	rxID, verifyRef := sg.str("prescriptionId"), sg.str("verificationReference")
	j.pump()
	j.deliverNotifications()
	afterSign := j.silentPushes("push-token-synthetic-xx-patient-01")
	if afterSign < 1 {
		t.Fatal("reminders were scheduled: the phone is told to refresh")
	}

	// ---- the on-device backup schedule (S-165) ----
	j.c.Scheduler.(*schedfake.Client).Materialize(j.clock(), j.clock().Add(72*time.Hour))
	on, entries := j.deviceSchedule(pat)
	if !on || len(entries) < 4 {
		t.Fatalf("twice a day for 72 hours: %v %v", on, entries)
	}
	for _, e := range entries {
		body, _ := e["body"].(string)
		key, _ := e["replaceKey"].(string)
		if strings.Contains(body, "xxdrugalpha") || !strings.HasPrefix(key, "rem-") || !strings.HasSuffix(key, "-c") {
			t.Fatalf("without P-MEDNAMES the backup never names the medicine (K-DP-09): %v", e)
		}
	}
	j.at(j.clock().Add(time.Minute))
	j.consent(pat, "P-MEDNAMES", legal.ConsentTextVersions["P-MEDNAMES"])
	j.pump()
	j.deliverNotifications()
	if j.silentPushes("push-token-synthetic-xx-patient-01") <= afterSign {
		t.Fatal("P-MEDNAMES reaches the phone's backups")
	}
	_, entries = j.deviceSchedule(pat)
	if body, _ := entries[0]["body"].(string); !strings.Contains(body, "xxdrugalpha") {
		t.Fatalf("with P-MEDNAMES the backup may name the medicine: %v", entries[0])
	}

	// A dose marked taken leaves the schedule.
	first, _ := entries[0]["occurrenceIds"].([]any)
	occ, _ := first[0].(string)
	fire, err := time.Parse(time.RFC3339, entries[0]["fireAt"].(string))
	if err != nil {
		t.Fatal(err)
	}
	j.at(fire)
	j.refresh(&pat)
	j.must(j.call(http.MethodPost, "/v1/me/reminders/"+occ+"/actions", pat.token, map[string]any{"action": "taken", "actedAt": fire.UTC().Format(time.RFC3339)}),
		http.StatusOK, "dose taken")
	_, entries = j.deviceSchedule(pat)
	for _, e := range entries {
		for _, id := range e["occurrenceIds"].([]any) {
			if id == occ {
				t.Fatal("a dose taken is not in the backup schedule")
			}
		}
	}

	// Switching the item off cancels its reminders; the schedule empties.
	j.must(j.call(http.MethodPut, "/v1/me/prescriptions/"+rxID+"/reminders/l1", pat.token, map[string]any{"enabled": false}), http.StatusNoContent, "item off")
	j.pump()
	j.c.Scheduler.(*schedfake.Client).Materialize(j.clock(), j.clock().Add(72*time.Hour))
	if on, entries = j.deviceSchedule(pat); !on || len(entries) != 0 {
		t.Fatalf("no reminders left for a switched-off item: %v", entries)
	}

	// ---- records ----
	ls := j.must(j.call(http.MethodGet, "/v1/me/prescriptions", pat.token, nil), http.StatusOK, "my prescriptions")
	if !strings.Contains(string(ls.raw), rxID) {
		t.Fatalf("the signed prescription is in the patient's records: %s", ls.raw)
	}
	if v := j.must(j.call(http.MethodGet, "/v1/prescription-verifications/"+verifyRef, "", nil), http.StatusOK, "verify"); v.str("status") != "valid" {
		t.Fatalf("public verification: %s", v.raw)
	}
}
