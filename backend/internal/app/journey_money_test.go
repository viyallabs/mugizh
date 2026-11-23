package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	pfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments/fake"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// patient signs a patient up with the consents booking needs, a profile and a push device.
func (j *journey) patient(phone, pushToken string) person {
	j.t.Helper()
	legal := j.c.Config.Pack().Legal()
	p := j.signIn(phone, ip.AppPatient, "")
	j.consent(p, "P-ACCOUNT", legal.ConsentTextVersions["P-ACCOUNT"])
	j.consent(p, "P-CARE", legal.ConsentTextVersions["P-CARE"])
	j.must(j.call(http.MethodPut, "/v1/me/profile", p.token, map[string]any{"fullName": "Meena Iyer", "dateOfBirth": "1985-08-20", "sex": "female"}),
		http.StatusNoContent, "patient profile")
	j.must(j.call(http.MethodPost, "/v1/me/devices/current/push-token", p.token, map[string]any{"token": pushToken}), http.StatusNoContent, "push token")
	return p
}

// settle delivers the aggregator's pending webhooks (payments and refunds) and the events
// they cause, as the webhook route and the worker would.
func (j *journey) settle(vendor *pfake.Fake) {
	j.t.Helper()
	ctx := context.Background()
	for _, d := range vendor.DrainDeliveries() {
		if _, err := j.c.Payments.HandleWebhook(ctx, d.ClinicLinkRef, d.Headers, d.Body); err != nil {
			j.t.Fatal(err)
		}
	}
	if _, err := j.c.Payments.ProcessWebhooks(ctx, 100); err != nil {
		j.t.Fatal(err)
	}
	j.pump()
}

// bookAndPay holds the first open video slot starting after `after`, checks out and pays.
func (j *journey) bookAndPay(p person, practice string, vendor *pfake.Fake, after time.Time) (string, time.Time) {
	j.t.Helper()
	sl := j.must(j.call(http.MethodGet, "/v1/practices/"+practice+"/slots", p.token, nil), http.StatusOK, "slots")
	items, _ := sl.body["items"].([]any)
	for _, it := range items {
		s, _ := it.(map[string]any)
		text, _ := s["startsAt"].(string)
		start, err := time.Parse(time.RFC3339, text)
		if err != nil || !start.After(after) {
			continue
		}
		hold := j.must(j.call(http.MethodPost, "/v1/practices/"+practice+"/holds", p.token, map[string]any{"slotId": s["slotId"], "mode": "video",
			"patientTimeZone": j.c.Config.Pack().Locale().DefaultTimeZone, "patientInIndiaConfirmed": true}), http.StatusCreated, "hold")
		appt := hold.str("appointmentId")
		j.must(j.call(http.MethodPost, "/v1/me/appointments/"+appt+"/checkout", p.token, nil), http.StatusOK, "checkout")
		order := j.sql("payments", `SELECT provider_order_id FROM payment_order WHERE appointment_ref=$1`, appt)
		if err := vendor.PayOrder(order, "pay_"+strings.ReplaceAll(appt, "-", "")[:20], j.clock()); err != nil {
			j.t.Fatal(err)
		}
		j.settle(vendor)
		return appt, start
	}
	j.t.Fatalf("no open slot after %s: %s", after, sl.raw)
	return "", time.Time{}
}

func (j *journey) num(r reply, key string) float64 {
	v, _ := r.body[key].(float64)
	return v
}

func (j *journey) refundState(p person, appt string) reply {
	return j.call(http.MethodGet, "/v1/me/appointments/"+appt+"/refund", p.token, nil)
}

// TestJourneyRefunds follows the money: a patient's own early cancellation (RF07), a clinic
// closing a day (RF04, cancelled by the clinic) and a doctor who never joins (RF03) each refund
// the right amount from the stored policy, and the patient is told what happened and what is
// coming back — booking messages never mention money, refund messages state the exact amount.
func TestJourneyRefunds(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	monday9 := time.Date(2026, 11, 23, 9, 0, 0, 0, zone)
	j := newJourney(t, monday9)
	ctx := context.Background()

	doc := j.pro("+919876500111")
	j.verifyDoctor(doc, "Asha Rao", "SMC/111")
	clinic, practice, vendor := j.clinicWithPractice(doc)
	pat := j.patient("+919876500211", "push-token-synthetic-patient-0002")
	const push = "push-token-synthetic-patient-0002"

	// Three paid appointments: today 14:00, tomorrow, the day after.
	today, todayStart := j.bookAndPay(pat, practice, vendor, monday9)
	tomorrow, tomorrowStart := j.bookAndPay(pat, practice, vendor, todayStart.Add(12*time.Hour))
	later, _ := j.bookAndPay(pat, practice, vendor, tomorrowStart.Add(12*time.Hour))

	// 1. The patient cancels the day-after appointment well ahead: RF07, full refund.
	pv := j.must(j.call(http.MethodGet, "/v1/me/appointments/"+later+"/cancel-preview", pat.token, nil), http.StatusOK, "cancel preview")
	amount := int64(j.num(pv, "refundAmount"))
	if amount != 50000 {
		t.Fatalf("an early cancellation previews a full refund: %s", pv.raw)
	}
	j.must(j.call(http.MethodPost, "/v1/me/appointments/"+later+"/cancel", pat.token, map[string]any{"acknowledgedRefundAmount": amount}), http.StatusOK, "cancel")
	j.pump()
	if r := j.refundState(pat, later); r.status != http.StatusOK || r.str("ruleId") != "RF07" {
		t.Fatalf("RF07 refund: %d %s", r.status, r.raw)
	}
	vendor.SettleRefunds(j.clock())
	j.settle(vendor)
	if r := j.refundState(pat, later); r.str("status") != "succeeded" {
		t.Fatalf("settled refund: %s", r.raw)
	}

	// 2. The clinic closes tomorrow: cancelled by the clinic, RF04 full refund.
	date := tomorrowStart.In(zone).Format(time.DateOnly)
	j.must(j.call(http.MethodPost, "/v1/clinics/"+clinic+"/closures", doc.token, map[string]any{"date": date, "mode": "cancel"},
		"X-Acting-Role", "clinic_admin"), http.StatusOK, "close tomorrow")
	j.pump()
	if a := j.must(j.call(http.MethodGet, "/v1/me/appointments/"+tomorrow, pat.token, nil), http.StatusOK, "appointment"); a.str("state") != "cancelled" {
		t.Fatalf("the closure cancels tomorrow's appointment: %s", a.raw)
	}
	if r := j.refundState(pat, tomorrow); r.str("ruleId") != "RF04" {
		t.Fatalf("RF04 refund: %s", r.raw)
	}

	// 3. Today the doctor never joins: after the join timeout the consult ends as a doctor
	// no-show, booking records it and payments refunds in full (RF03).
	j.at(todayStart.Add(12 * time.Minute))
	j.refresh(&pat)
	if _, err := j.c.Consult.DetectDoctorNoShow(ctx, j.clock()); err != nil {
		t.Fatal(err)
	}
	j.pump()
	if r := j.refundState(pat, today); r.str("ruleId") != "RF03" {
		t.Fatalf("RF03 refund: %s", r.raw)
	}

	// What the patient was told.
	j.deliverNotifications()
	all := strings.Join(j.pushBodies(push), "\n")
	for _, want := range []string{
		"Your refund of Rs 500",                         // RF07 started, then sent to the bank
		"has been sent to your bank",                    // refund.succeeded
		"was cancelled by the clinic",                   // closure, booking message (no money)
		"A full refund of Rs 500 for your cancelled",    // RF04 refund message
		"Your doctor was not available",                 // doctor no-show, booking message (no money)
		"A full refund of Rs 500 for your consultation", // RF03 refund message
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing message %q in:\n%s", want, all)
		}
	}
	for _, body := range j.pushBodies(push) {
		if (strings.Contains(body, "cancelled by the clinic") || strings.Contains(body, "doctor was not available")) && strings.Contains(body, "Rs") {
			t.Fatalf("a booking message must not mention money: %q", body)
		}
	}
}
