package httpapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/httpapi"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers refuse a caller with the wrong acting role before touching the service.
func TestHandlers_RoleChecksFailClosed(t *testing.T) {
	h := httpapi.Handlers{Rx: (*clinical.Rx)(nil)}
	doctor := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ids.New(), ActingRole: ip.RoleDoctor})
	patient := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ids.New(), ActingRole: ip.RolePatient})
	appt := uuid.New()

	if _, err := h.SignPrescription(patient, httpapi.SignPrescriptionRequestObject{AppointmentId: appt}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("patient signing: %v", err)
	}
	if _, err := h.ListMyPrescriptions(doctor, httpapi.ListMyPrescriptionsRequestObject{}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("doctor on patient route: %v", err)
	}
	if _, err := h.SignPrescription(context.Background(), httpapi.SignPrescriptionRequestObject{AppointmentId: appt}); !errors.Is(err, problem.Unauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
}

// The spec declares roles on every operation, a step-up and a required key on signing, and
// the verification page as public (K-SEC-03, K-SEC-04).
func TestSpec_PolicyDeclarations(t *testing.T) {
	sw, err := httpapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for path, item := range sw.Paths.Map() {
		for method, op := range item.Operations() {
			seen++
			if op.Extensions["x-roles"] == nil {
				t.Errorf("%s %s has no x-roles", method, path)
			}
			if op.OperationID == "signPrescription" {
				if v, _ := op.Extensions["x-step-up"].(bool); !v {
					t.Error("sign must declare x-step-up: true")
				}
				required := false
				for _, p := range op.Parameters {
					if p.Value.Name == "Idempotency-Key" && p.Value.Required {
						required = true
					}
				}
				if !required {
					t.Error("sign must require Idempotency-Key")
				}
			}
		}
	}
	if seen != 14 {
		t.Fatalf("operations %d", seen)
	}
}
