// Package public is the identity module's interface (S-104, S-105, S-106): who the
// caller is (Principal), the roles they hold, and the per-operation access policy that
// every HTTP operation declares in its OpenAPI spec (x-roles, x-mfa-exempt,
// x-restricted-ok, x-step-up). Deny by default (K-SEC-04).
package public

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// App is the client application a session belongs to. Professional and staff roles can
// only be used from a "pro" session (with MFA); patient roles only from "patient".
type App string

const (
	AppPatient App = "patient"
	AppPro     App = "pro"
)

// Roles (access-control-design §2). "nurse" is reserved (S-031): no accounts in v1.0.
const (
	RolePatient          = "patient"
	RoleDoctor           = "doctor"
	RoleClinicAdmin      = "clinic_admin"
	RoleFrontDesk        = "front_desk"
	RoleNurse            = "nurse"
	RoleReviewer         = "reviewer"
	RoleSupport          = "support"
	RoleGrievanceOfficer = "grievance_officer"
	RoleSecurityOwner    = "security_owner"
	RoleOps              = "ops"
	// RolePublic in x-roles marks an operation that needs no sign-in (e.g. request OTP).
	RolePublic = "public"
	// RoleAuthenticated in x-roles means any signed-in caller, whatever their roles
	// (sign out, "who am I", own devices). MFA, restriction and step-up rules still apply.
	RoleAuthenticated = "authenticated"
)

// EventDeviceRevoked is emitted when the user signs a device out remotely (S-115). Payload: deviceRef. The
// notifications module tells that device to wipe itself (S-165: it may hold an on-device reminder schedule).
const EventDeviceRevoked = "device.revoked"

// ProRoles need a pro-app session with completed MFA (K-SEC-03).
var ProRoles = []string{RoleDoctor, RoleClinicAdmin, RoleFrontDesk, RoleNurse}

// StaffRoles are internal roles. They sign in only with SSO and a phishing-resistant
// factor (TC-64), which is not built yet, so no session can carry them in v1.0 code.
var StaffRoles = []string{RoleReviewer, RoleSupport, RoleGrievanceOfficer, RoleSecurityOwner, RoleOps}

// RoleGrant is one role a user holds, optionally scoped to a clinic.
type RoleGrant struct {
	Role      string
	ClinicRef string
}

// Principal is the authenticated caller.
type Principal struct {
	UserID    string
	SessionID string
	DeviceID  string
	App       App
	Roles     []RoleGrant
	MFAAt     *time.Time // set when the session completed MFA
	StepUpAt  *time.Time // last step-up re-authentication
	// Restricted is true for a session on a new, not yet re-verified device: it may call
	// only operations marked x-restricted-ok and never reads health data (E-DP-22).
	Restricted bool

	// Set by the policy check for the current operation.
	ActingRole   string
	ActingClinic string
}

// HasRole reports whether the principal holds role (any clinic scope).
func (p *Principal) HasRole(role string) bool {
	return slices.ContainsFunc(p.Roles, func(g RoleGrant) bool { return g.Role == role })
}

// ClinicsFor lists clinic scopes the principal holds for role.
func (p *Principal) ClinicsFor(role string) []string {
	var out []string
	for _, g := range p.Roles {
		if g.Role == role && g.ClinicRef != "" {
			out = append(out, g.ClinicRef)
		}
	}
	return out
}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal, if any.
func FromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(*Principal)
	return p, ok && p != nil
}

// Policy is what an operation declares in its OpenAPI spec.
type Policy struct {
	OperationID  string
	Roles        []string // x-roles; "public" means no sign-in needed
	MFAExempt    bool     // x-mfa-exempt: MFA enrol/verify endpoints only
	RestrictedOK bool     // x-restricted-ok: callable from a restricted session
	StepUp       bool     // x-step-up: needs a step-up within StepUpWindow
}

// StepUpWindow is how long a step-up stays valid (signing, export, erasure).
const StepUpWindow = 5 * time.Minute

// Problems specific to the policy check.
var (
	ProblemRoleAmbiguous = problem.New(http.StatusBadRequest, "auth.role_ambiguous", "Say which role or clinic to act as")
	ProblemWrongApp      = problem.New(http.StatusForbidden, "auth.wrong_app", "This action needs the other app")
)

// Check applies a policy to a principal and picks the acting role. actingRole and
// clinic come from the X-Acting-Role and X-Clinic-Id headers (optional unless ambiguous).
// It returns a *problem.Problem on denial.
func Check(p *Principal, pol Policy, actingRole, clinic string, now time.Time) error {
	if p == nil {
		return problem.Unauthenticated
	}
	var candidates []RoleGrant
	for _, g := range p.Roles {
		if !slices.Contains(pol.Roles, g.Role) {
			continue
		}
		if actingRole != "" && g.Role != actingRole {
			continue
		}
		if clinic != "" && g.ClinicRef != "" && g.ClinicRef != clinic {
			continue
		}
		candidates = append(candidates, g)
	}
	if len(candidates) == 0 {
		if !slices.Contains(pol.Roles, RoleAuthenticated) {
			return problem.Forbidden
		}
		// Any signed-in caller. A pro-app session must still finish MFA first.
		if p.App == AppPro && p.MFAAt == nil && !pol.MFAExempt {
			return problem.MFARequired
		}
		if p.Restricted && !pol.RestrictedOK {
			return problem.Restricted
		}
		if pol.StepUp && (p.StepUpAt == nil || now.Sub(*p.StepUpAt) > StepUpWindow) {
			return problem.StepUpRequired
		}
		p.ActingRole = RoleAuthenticated
		return nil
	}
	// Without X-Acting-Role, the first role in the operation's x-roles order wins. The
	// same role at two clinics needs X-Clinic-Id.
	slices.SortStableFunc(candidates, func(a, b RoleGrant) int {
		return slices.Index(pol.Roles, a.Role) - slices.Index(pol.Roles, b.Role)
	})
	role := candidates[0].Role
	for _, c := range candidates[1:] {
		if c.Role == role && c.ClinicRef != candidates[0].ClinicRef {
			return ProblemRoleAmbiguous
		}
	}
	isPro := slices.Contains(ProRoles, role)
	isStaff := slices.Contains(StaffRoles, role)
	switch {
	case isStaff:
		return problem.Forbidden // staff sign-in (SSO + phishing-resistant factor) not built: fail closed
	case isPro && p.App != AppPro:
		return ProblemWrongApp
	case role == RolePatient && p.App != AppPatient:
		return ProblemWrongApp
	}
	if role == RoleNurse {
		return problem.Forbidden // reserved for v4.0 (S-031)
	}
	if isPro && p.MFAAt == nil && !pol.MFAExempt {
		return problem.MFARequired
	}
	if p.Restricted && !pol.RestrictedOK {
		return problem.Restricted
	}
	if pol.StepUp && (p.StepUpAt == nil || now.Sub(*p.StepUpAt) > StepUpWindow) {
		return problem.StepUpRequired
	}
	p.ActingRole = role
	p.ActingClinic = candidates[0].ClinicRef
	return nil
}

// Authenticator resolves a bearer token to a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, bearer string) (*Principal, error)
}

// RoleAssigner lets other modules grant scoped roles (e.g. directory makes the clinic's
// creator its clinic admin). Assignments are audited by identity.
type RoleAssigner interface {
	AssignRole(ctx context.Context, userID, role, clinicRef, byRef string) error
	RevokeRole(ctx context.Context, userID, role, clinicRef, byRef string) error
}

// AccountSummary is what support may see about an account (S-172): never a name, date of
// birth, contact detail or health data.
type AccountSummary struct {
	UserID        string
	CreatedOn     string // date only
	Roles         []string
	ActiveDevices int
}

// ErrAccountNotFound: no active account for that number (also for an invalid number).
var ErrAccountNotFound = errors.New("identity: no active account")

// AccountLookup finds an active account by phone number for support triage.
type AccountLookup interface {
	LookupByPhone(ctx context.Context, phone string) (AccountSummary, error)
}
