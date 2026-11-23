package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// frontDesk creates invited front-desk staff at a clinic: a pro-app account with no doctor intent, the role from
// the clinic, and MFA enrolled.
func (j *journey) frontDesk(phone, clinic, by string) person {
	j.t.Helper()
	ctx := context.Background()
	p := j.signIn(phone, ip.AppPro, "")
	if err := j.c.Identity.AssignRole(ctx, p.id, ip.RoleFrontDesk, clinic, by); err != nil {
		j.t.Fatal(err)
	}
	pr, err := j.c.Identity.Authenticate(ctx, p.token)
	if err != nil {
		j.t.Fatal(err)
	}
	en, err := j.c.Identity.EnrolTOTP(ctx, pr)
	if err != nil {
		j.t.Fatal(err)
	}
	code, _ := totp.GenerateCode(en.Secret, j.clock())
	if err := j.c.Identity.ConfirmTOTP(ctx, pr, code); err != nil {
		j.t.Fatal(err)
	}
	p.totp = en.Secret
	return p
}

// refused asserts a refusal with the given status (and code, when given).
func (j *journey) refused(r reply, status int, code, what string) {
	j.t.Helper()
	if r.status != status || (code != "" && r.str("code") != code) {
		j.t.Fatalf("%s: want %d %s, got %d %s", what, status, code, r.status, r.raw)
	}
}

// TestS181AttackAuthorisationAndConsent (K-SEC-04, K-DP-02, K-SEC-08; E-DP-01, E-DP-16) attacks the assembled API
// with real data: role escalation through the X-Acting-Role and X-Clinic-Id headers, a verified doctor of another
// clinic, front-desk staff and clinical data, the wrong kind of consent (D-66), patient-only actions by
// professionals, and another patient. No attempt succeeds, and each refused read of the patient's data by a
// professional leaves a denied audit event.
func TestS181AttackAuthorisationAndConsent(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	j := newJourney(t, time.Date(2026, 11, 23, 9, 0, 0, 0, zone))
	legal := j.c.Config.Pack().Legal()

	docA := j.pro("+919876500111")
	j.verifyDoctor(docA, "Asha Rao", "SMC/111")
	clinicA, practiceA, vendor := j.clinicWithPractice(docA)
	docB := j.pro("+919876500112")
	j.verifyDoctor(docB, "Bala Iyer", "SMC/112")
	clinicB, _, _ := j.clinicWithPractice(docB)
	pat := j.patient("+919876500211", "push-token-synthetic-attack-0001")
	other := j.patient("+919876500212", "push-token-synthetic-attack-0002")
	appt, _ := j.bookAndPay(pat, practiceA, vendor, j.clock())
	desk := j.frontDesk("+919876500311", clinicA, docA.id)
	// The patient shares their history with doctor A for this appointment.
	sh := j.must(j.call(http.MethodPost, "/v1/me/history-shares", pat.token, map[string]any{"doctorId": docA.id, "appointmentId": appt,
		"textVersion": legal.ConsentTextVersions["P-SHARE"]}), http.StatusCreated, "share history with doctor A")
	j.must(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", docA.token, nil), http.StatusOK, "doctor A reads the shared history")

	// ---- 1. Headers never add a role ----
	j.refused(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", pat.token, nil, "X-Acting-Role", "doctor"),
		http.StatusForbidden, "auth.forbidden", "patient claiming the doctor role")
	j.refused(j.call(http.MethodPost, "/v1/clinics/"+clinicA+"/practices", pat.token, map[string]any{"doctorId": docA.id, "feeAmount": 100, "modes": []string{"video"}},
		"X-Acting-Role", "clinic_admin", "X-Clinic-Id", clinicA), http.StatusForbidden, "auth.forbidden", "patient claiming clinic admin")
	j.refused(j.call(http.MethodPost, "/v1/clinics/"+clinicA+"/practices", docB.token, map[string]any{"doctorId": docB.id, "feeAmount": 100, "modes": []string{"video"}},
		"X-Acting-Role", "clinic_admin", "X-Clinic-Id", clinicA), http.StatusForbidden, "", "doctor B claiming clinic A's admin role")
	j.refused(j.call(http.MethodGet, "/v1/clinics/"+clinicB+"/branches", docA.token, nil, "X-Clinic-Id", clinicB),
		http.StatusForbidden, "", "clinic A's admin naming clinic B")
	j.refused(j.call(http.MethodPost, "/v1/clinics/"+clinicA+"/closures", desk.token, map[string]any{"date": "2026-11-30", "mode": "cancel"},
		"X-Acting-Role", "clinic_admin"), http.StatusForbidden, "auth.forbidden", "front desk claiming clinic admin")
	j.refused(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", desk.token, nil, "X-Acting-Role", "doctor"),
		http.StatusForbidden, "auth.forbidden", "front desk claiming the doctor role")
	j.refused(j.call(http.MethodGet, "/v1/me/appointments/"+appt, docA.token, nil, "X-Acting-Role", "patient"),
		http.StatusForbidden, "", "doctor claiming the patient role from the pro app")

	// ---- 2. A verified doctor of another clinic ----
	// No relationship with the patient: 404, which does not even confirm the patient exists (K-SEC-04).
	j.refused(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", docB.token, nil), http.StatusNotFound, "resource.not_found",
		"doctor B reading a history shared with doctor A")
	j.refused(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/documents", docB.token, nil), http.StatusNotFound, "resource.not_found",
		"doctor B listing the patient's documents")
	for _, path := range []string{"/v1/appointments/" + appt + "/note", "/v1/appointments/" + appt + "/prescription"} {
		j.refused(j.call(http.MethodGet, path, docB.token, nil), http.StatusNotFound, "", "doctor B reading "+path)
	}
	j.refused(j.call(http.MethodPut, "/v1/appointments/"+appt+"/note", docB.token, map[string]any{"complaint": "x", "findings": "y"}),
		http.StatusNotFound, "", "doctor B writing a note on doctor A's appointment")
	j.refused(j.call(http.MethodPost, "/v1/appointments/"+appt+"/staff-cancel", docB.token, nil),
		http.StatusNotFound, "", "doctor B cancelling doctor A's appointment")
	j.refused(j.call(http.MethodPost, "/v1/patients/"+pat.id+"/access-extension-requests", docB.token, nil),
		http.StatusNotFound, "", "doctor B asking to extend doctor A's share")

	// ---- 3. Front desk: appointments yes, clinical data never ----
	j.refused(j.call(http.MethodGet, "/v1/appointments/"+appt+"/note", desk.token, nil), http.StatusForbidden, "auth.forbidden", "front desk reading the note")
	j.refused(j.call(http.MethodGet, "/v1/appointments/"+appt+"/prescription", desk.token, nil), http.StatusForbidden, "auth.forbidden", "front desk reading the prescription")
	j.refused(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/documents", desk.token, nil), http.StatusForbidden, "auth.forbidden", "front desk listing documents")
	j.refused(j.call(http.MethodPost, "/v1/clinics/"+clinicA+"/closures", desk.token, map[string]any{"date": "2026-11-30", "mode": "cancel"}),
		http.StatusForbidden, "auth.forbidden", "front desk closing the clinic (D-64)")
	j.refused(j.call(http.MethodGet, "/v1/clinics/"+clinicB+"/closures", desk.token, nil, "X-Clinic-Id", clinicB), http.StatusForbidden, "",
		"clinic A's front desk at clinic B")
	j.refused(j.call(http.MethodGet, "/v1/clinics/"+clinicB+"/closures", desk.token, nil), http.StatusNotFound, "",
		"clinic A's front desk reading clinic B's closures without a header")

	// ---- 4. The wrong kind of consent, and patient-only actions by professionals (D-66) ----
	providerNotice, patientNotice := legal.NoticeVersions["provider"], legal.NoticeVersions["patient"]
	for _, purpose := range []string{"P-SHARE", "P-CARE", "P-REMIND"} {
		j.refused(j.call(http.MethodPost, "/v1/me/consents", docA.token, map[string]any{"purposeId": purpose, "textVersion": legal.ConsentTextVersions[purpose],
			"noticeVersion": providerNotice}), http.StatusForbidden, "", "doctor recording patient consent "+purpose)
	}
	j.refused(j.call(http.MethodPost, "/v1/me/consents", desk.token, map[string]any{"purposeId": "P-SHARE", "textVersion": legal.ConsentTextVersions["P-SHARE"],
		"noticeVersion": providerNotice}), http.StatusForbidden, "", "front desk recording P-SHARE")
	j.refused(j.call(http.MethodPost, "/v1/me/consents", pat.token, map[string]any{"purposeId": "D-VERIFY", "textVersion": legal.ConsentTextVersions["D-VERIFY"],
		"noticeVersion": patientNotice}), http.StatusForbidden, "", "patient recording a professional consent")
	j.refused(j.call(http.MethodPost, "/v1/me/history-shares", docA.token, map[string]any{"doctorId": docA.id, "appointmentId": appt,
		"textVersion": legal.ConsentTextVersions["P-SHARE"]}), http.StatusForbidden, "", "doctor sharing a patient's history with themselves")
	j.refused(j.call(http.MethodPost, "/v1/me/access-grants/"+sh.str("grantId")+"/revoke", docA.token, nil), http.StatusForbidden, "",
		"doctor revoking the patient's grant")

	// ---- 5. Another patient ----
	j.refused(j.call(http.MethodPost, "/v1/me/access-grants/"+sh.str("grantId")+"/revoke", other.token, nil), http.StatusNotFound, "",
		"another patient revoking the share")
	j.refused(j.call(http.MethodPost, "/v1/me/history-shares", other.token, map[string]any{"doctorId": docA.id, "appointmentId": appt,
		"textVersion": legal.ConsentTextVersions["P-SHARE"]}), http.StatusNotFound, "", "another patient sharing on someone else's appointment")
	j.refused(j.call(http.MethodGet, "/v1/me/appointments/"+appt+"/payment-document", other.token, nil), http.StatusNotFound, "",
		"another patient reading the receipt")

	// The share still works for doctor A: none of the attempts changed anything.
	j.must(j.call(http.MethodGet, "/v1/patients/"+pat.id+"/history", docA.token, nil), http.StatusOK, "doctor A still reads the shared history")

	// K-DP-02: doctor B's refused reads of the patient's data are on the record.
	if n := j.sql("audit", `SELECT count(*)::text FROM audit_outbox WHERE actor_ref=$1 AND subject_ref=$2 AND result='denied'`, docB.id, pat.id); n == "0" {
		t.Fatal("doctor B's refused reads must be audited")
	}
}
