package httpapi_test

import (
	"context"
	"errors"
	"testing"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// The handlers refuse a caller whose acting role is wrong before touching the module, and
// a doctor with no case (or someone else's data) gets 404 (K-SEC-04).
func TestHandlers_RoleChecksFailClosed(t *testing.T) {
	h := httpapi.Handlers{M: (*verification.Module)(nil)}
	doctor := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ids.New(), ActingRole: ip.RoleDoctor})
	patient := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ids.New(), ActingRole: ip.RolePatient})

	if _, err := h.GetMyVerification(patient, httpapi.GetMyVerificationRequestObject{}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("patient on doctor route: %v", err)
	}
	if _, err := h.ListVerificationQueue(doctor, httpapi.ListVerificationQueueRequestObject{}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("doctor on reviewer route: %v", err)
	}
	if _, err := h.GetMyVerification(context.Background(), httpapi.GetMyVerificationRequestObject{}); !errors.Is(err, problem.Unauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
}

// Clinic routes are for the clinic admin only; the clinic scope is checked in the module.
func TestHandlers_ClinicRoutesRoleChecks(t *testing.T) {
	h := httpapi.Handlers{M: (*verification.Module)(nil)}
	doctor := ip.WithPrincipal(context.Background(), &ip.Principal{UserID: ids.New(), ActingRole: ip.RoleDoctor})
	if _, err := h.GetClinicVerification(doctor, httpapi.GetClinicVerificationRequestObject{}); !errors.Is(err, problem.Forbidden) {
		t.Fatalf("doctor on clinic route: %v", err)
	}
	if _, err := h.SubmitClinicVerification(context.Background(), httpapi.SubmitClinicVerificationRequestObject{}); !errors.Is(err, problem.Unauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
}
