package httpapi_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

type owners map[string]string

func (o owners) PatientOf(_ context.Context, appt string) (string, bool, error) {
	p, ok := o[appt]
	return p, ok, nil
}

func asPatient(id string) context.Context {
	return ip.WithPrincipal(context.Background(), &ip.Principal{UserID: id, ActingRole: ip.RolePatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}})
}

func asClinicAdmin(id, clinic string) context.Context {
	return ip.WithPrincipal(context.Background(), &ip.Principal{UserID: id, ActingRole: ip.RoleClinicAdmin, ActingClinic: clinic,
		Roles: []ip.RoleGrant{{Role: ip.RoleClinicAdmin, ClinicRef: clinic}}})
}

func isNotFound(err error) bool {
	var p *problem.Problem
	return errors.As(err, &p) && p.Status == 404
}

func TestPaymentsHTTP_OwnershipAndStatus(t *testing.T) {
	ctx := context.Background()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), payments.Source()}})
	b, err := os.ReadFile("../../../../../config/country-packs/IN/payments/refund-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := refundpolicy.Parse(b)
	fk := fake.New()
	m, err := payments.New(env.Pools.For("payments"), fk, payments.Config{Currency: pol.Currency, Policy: pol, AllowedModes: []string{"clinic_collected"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clinic, patient, stranger, admin := ids.New(), ids.New(), ids.New(), ids.New()
	link, err := m.LinkClinic(ctx, payments.LinkParams{ClinicRef: clinic, Aggregator: "fake", SettlementMode: "clinic_collected",
		CredentialRef: "secretref:clinics/" + clinic + "/a", WebhookSecretRef: "secretref:clinics/" + clinic + "/b"})
	if err != nil {
		t.Fatal(err)
	}
	fk.RegisterClinic(link, "s", 1_000_000)
	appt := ids.New()
	co, err := m.CreateOrder(ctx, appt, clinic, 40000, pol.Currency, time.Now().Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.Handlers{M: m, Owners: owners{appt: patient}}
	aid := uuid.MustParse(appt)

	got, err := h.GetMyAppointmentPayment(asPatient(patient), httpapi.GetMyAppointmentPaymentRequestObject{AppointmentId: aid})
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(httpapi.GetMyAppointmentPayment200JSONResponse); r.Status != httpapi.PaymentStatePending || r.Amount != 40000 {
		t.Fatalf("%+v", r)
	}
	// Someone else's appointment, and one that does not exist, are both 404.
	if _, err := h.GetMyAppointmentPayment(asPatient(stranger), httpapi.GetMyAppointmentPaymentRequestObject{AppointmentId: aid}); !isNotFound(err) {
		t.Fatalf("other patient: %v", err)
	}
	if _, err := h.GetMyAppointmentRefund(asPatient(patient), httpapi.GetMyAppointmentRefundRequestObject{AppointmentId: uuid.New()}); !isNotFound(err) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := h.GetMyAppointmentRefund(asPatient(patient), httpapi.GetMyAppointmentRefundRequestObject{AppointmentId: aid}); !isNotFound(err) {
		t.Fatalf("no refund yet: %v", err)
	}
	if _, err := h.GetMyAppointmentPayment(context.Background(), httpapi.GetMyAppointmentPaymentRequestObject{AppointmentId: aid}); err == nil {
		t.Fatal("unauthenticated")
	}

	// Pay, then cancel by the clinic: the patient sees the refund and its reference number.
	fk.PayOrder(co.ProviderOrderID, "pay_h1", time.Now())
	for _, d := range fk.DrainDeliveries() {
		m.HandleWebhook(ctx, d.ClinicLinkRef, d.Headers, d.Body)
	}
	m.ProcessWebhooks(ctx, 10)
	if err := m.OnAppointmentCancelled(ctx, outbox.Event{ID: ids.New(), Type: "appointment.cancelled",
		Payload: map[string]string{"appointmentRef": appt, "byWhom": "clinic", "policyVersion": pol.Version, "mode": "online"}}); err != nil {
		t.Fatal(err)
	}
	rf, err := h.GetMyAppointmentRefund(asPatient(patient), httpapi.GetMyAppointmentRefundRequestObject{AppointmentId: aid})
	if err != nil {
		t.Fatal(err)
	}
	r := rf.(httpapi.GetMyAppointmentRefund200JSONResponse)
	if r.Status != httpapi.RefundPatientStateProcessing || r.RuleId != "RF04" || r.Amount != 40000 {
		t.Fatalf("%+v", r)
	}

	// Clinic admin: own clinic only.
	cid := uuid.MustParse(clinic)
	list, err := h.ListClinicPayments(asClinicAdmin(admin, clinic), httpapi.ListClinicPaymentsRequestObject{ClinicId: cid})
	if err != nil {
		t.Fatal(err)
	}
	items := list.(httpapi.ListClinicPayments200JSONResponse).Items
	if len(items) != 1 || items[0].RefundStatus == nil {
		t.Fatalf("%+v", items)
	}
	var audits int
	env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE action='payments.clinic_list'`).Scan(&audits)
	if audits != 1 {
		t.Fatal("clinic reads are audited")
	}
	if _, err := h.ListClinicPayments(asClinicAdmin(admin, ids.New()), httpapi.ListClinicPaymentsRequestObject{ClinicId: cid}); !isNotFound(err) {
		t.Fatalf("another clinic's admin: %v", err)
	}
	if _, err := h.GetClinicPaymentsStatus(asPatient(patient), httpapi.GetClinicPaymentsStatusRequestObject{ClinicId: cid}); err == nil {
		t.Fatal("a patient is not a clinic admin")
	}
	if _, err := h.RetryClinicRefund(asClinicAdmin(admin, clinic), httpapi.RetryClinicRefundRequestObject{ClinicId: cid, RefundId: uuid.New()}); !isNotFound(err) {
		t.Fatalf("unknown refund: %v", err)
	}
	st, err := h.GetClinicPaymentsStatus(asClinicAdmin(admin, clinic), httpapi.GetClinicPaymentsStatusRequestObject{ClinicId: cid})
	if err != nil || st.(httpapi.GetClinicPaymentsStatus200JSONResponse).Paused {
		t.Fatalf("%v %v", st, err)
	}
}
