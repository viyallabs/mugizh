package app

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// knownRoles are the only values allowed in x-roles.
var knownRoles = map[string]bool{
	ip.RolePublic: true, ip.RoleAuthenticated: true, ip.RolePatient: true, ip.RoleDoctor: true, ip.RoleClinicAdmin: true,
	ip.RoleFrontDesk: true, ip.RoleNurse: true, ip.RoleReviewer: true, ip.RoleSupport: true, ip.RoleGrievanceOfficer: true,
	ip.RoleSecurityOwner: true, ip.RoleOps: true,
}

// Policies maps the generated Go operation name (operationId with an upper-case first
// letter, as oapi-codegen passes it to strict middleware) to its declared policy.
type Policies map[string]ip.Policy

// BuildPolicies reads x-roles, x-mfa-exempt, x-restricted-ok and x-step-up from every
// operation of every spec. A missing or unknown x-roles entry is a start-up error: an
// undeclared endpoint must never be reachable (deny by default, access-control-design §1).
func BuildPolicies(specs ...*openapi3.T) (Policies, error) {
	out := Policies{}
	for _, s := range specs {
		for path, item := range s.Paths.Map() {
			for method, op := range item.Operations() {
				if op.OperationID == "" {
					return nil, fmt.Errorf("policy: %s %s has no operationId", method, path)
				}
				roles, err := stringList(op.Extensions["x-roles"])
				if err != nil || len(roles) == 0 {
					return nil, fmt.Errorf("policy: %s has no valid x-roles", op.OperationID)
				}
				for _, r := range roles {
					if !knownRoles[r] {
						return nil, fmt.Errorf("policy: %s has unknown role %q", op.OperationID, r)
					}
				}
				name := strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:]
				if _, dup := out[name]; dup {
					return nil, fmt.Errorf("policy: duplicate operationId %s", op.OperationID)
				}
				out[name] = ip.Policy{OperationID: op.OperationID, Roles: roles,
					MFAExempt: boolExt(op.Extensions["x-mfa-exempt"]), RestrictedOK: boolExt(op.Extensions["x-restricted-ok"]),
					StepUp: boolExt(op.Extensions["x-step-up"])}
			}
		}
	}
	return out, nil
}

func stringList(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("not a list")
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("not a string")
		}
		out = append(out, s)
	}
	return out, nil
}

func boolExt(v any) bool { b, ok := v.(bool); return ok && b }
