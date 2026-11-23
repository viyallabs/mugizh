package public_test

import (
	"errors"
	"testing"
	"time"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// S-181 (K-SEC-04, K-SEC-08): the acting-role and clinic headers can only choose among roles the caller holds;
// they never add one. Staff roles and the reserved nurse role fail closed; each app only acts in its own roles.

const (
	clinicA = "0193a000-0000-7000-8000-00000000000a"
	clinicB = "0193a000-0000-7000-8000-00000000000b"
)

func at() time.Time { return time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC) }

func pro(roles ...ip.RoleGrant) *ip.Principal {
	mfa := at().Add(-time.Minute)
	return &ip.Principal{UserID: "0193a000-0000-7000-8000-000000000001", App: ip.AppPro, MFAAt: &mfa, Roles: roles}
}

func status(err error) int {
	var p *problem.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	if err == nil {
		return 200
	}
	return -1
}

func TestS181HeadersNeverAddARole(t *testing.T) {
	patient := &ip.Principal{UserID: "0193a000-0000-7000-8000-000000000002", App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}
	doctorOnly := ip.Policy{Roles: []string{ip.RoleDoctor}}
	adminOnly := ip.Policy{Roles: []string{ip.RoleClinicAdmin}}
	for name, c := range map[string]struct {
		p                  *ip.Principal
		pol                ip.Policy
		role, clinic       string
		want               int
		wantRole, wantClin string
	}{
		"patient claims doctor":                {patient, doctorOnly, ip.RoleDoctor, "", 403, "", ""},
		"patient claims clinic admin":          {patient, adminOnly, ip.RoleClinicAdmin, clinicA, 403, "", ""},
		"doctor claims clinic admin":           {pro(ip.RoleGrant{Role: ip.RoleDoctor}), adminOnly, ip.RoleClinicAdmin, clinicA, 403, "", ""},
		"front desk claims clinic admin":       {pro(ip.RoleGrant{Role: ip.RoleFrontDesk, ClinicRef: clinicA}), adminOnly, ip.RoleClinicAdmin, clinicA, 403, "", ""},
		"admin of A names clinic B":            {pro(ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: clinicA}), adminOnly, "", clinicB, 403, "", ""},
		"admin of A acts at A":                 {pro(ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: clinicA}), adminOnly, "", clinicA, 200, ip.RoleClinicAdmin, clinicA},
		"admin at two clinics must choose":     {pro(ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: clinicA}, ip.RoleGrant{Role: ip.RoleClinicAdmin, ClinicRef: clinicB}), adminOnly, "", "", 400, "", ""},
		"unscoped doctor cannot inject clinic": {pro(ip.RoleGrant{Role: ip.RoleDoctor}), doctorOnly, "", clinicB, 200, ip.RoleDoctor, ""},
		"pro role from the patient app":        {&ip.Principal{UserID: "u", App: ip.AppPatient, Roles: []ip.RoleGrant{{Role: ip.RoleDoctor}}}, doctorOnly, "", "", 403, "", ""},
		"patient role from the pro app":        {pro(ip.RoleGrant{Role: ip.RolePatient}), ip.Policy{Roles: []string{ip.RolePatient}}, "", "", 403, "", ""},
		"pro without MFA":                      {&ip.Principal{UserID: "u", App: ip.AppPro, Roles: []ip.RoleGrant{{Role: ip.RoleDoctor}}}, doctorOnly, "", "", 403, "", ""},
		"nurse is reserved":                    {pro(ip.RoleGrant{Role: ip.RoleNurse, ClinicRef: clinicA}), ip.Policy{Roles: []string{ip.RoleNurse}}, "", "", 403, "", ""},
		"support fails closed (no staff SSO)":  {pro(ip.RoleGrant{Role: ip.RoleSupport}), ip.Policy{Roles: []string{ip.RoleSupport}}, ip.RoleSupport, "", 403, "", ""},
		"reviewer fails closed (no staff SSO)": {pro(ip.RoleGrant{Role: ip.RoleReviewer}), ip.Policy{Roles: []string{ip.RoleReviewer}}, "", "", 403, "", ""},
		"staff role cannot reach patient ops":  {pro(ip.RoleGrant{Role: ip.RoleSupport}), ip.Policy{Roles: []string{ip.RolePatient}}, ip.RoleSupport, "", 403, "", ""},
		"unknown header role on authenticated": {patient, ip.Policy{Roles: []string{ip.RoleAuthenticated}}, ip.RoleDoctor, "", 200, ip.RoleAuthenticated, ""},
		"restricted device on a normal op":     {&ip.Principal{UserID: "u", App: ip.AppPatient, Restricted: true, Roles: []ip.RoleGrant{{Role: ip.RolePatient}}}, ip.Policy{Roles: []string{ip.RolePatient}}, "", "", 403, "", ""},
		"step-up needed and missing":           {pro(ip.RoleGrant{Role: ip.RoleDoctor}), ip.Policy{Roles: []string{ip.RoleDoctor}, StepUp: true}, "", "", 403, "", ""},
	} {
		got := ip.Check(c.p, c.pol, c.role, c.clinic, at())
		if status(got) != c.want {
			t.Errorf("%s: status %d (%v), want %d", name, status(got), got, c.want)
			continue
		}
		if c.want == 200 && (c.p.ActingRole != c.wantRole || c.p.ActingClinic != c.wantClin) {
			t.Errorf("%s: acting %q at %q, want %q at %q", name, c.p.ActingRole, c.p.ActingClinic, c.wantRole, c.wantClin)
		}
	}
	if status(ip.Check(nil, doctorOnly, "", "", at())) != 401 {
		t.Error("no principal is 401")
	}
}
