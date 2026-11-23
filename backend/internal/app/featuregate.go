package app

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// caregiverOperations are the operations that exist only where the country pack's "caregivers" flag is on.
var caregiverOperations = map[string]bool{
	"createCaregiverInvitation": true, "cancelCaregiverInvitation": true, "listMyCaregivers": true, "setCaregiverScopes": true,
	"revokeCaregiver": true, "redeemCaregiverCode": true, "listCaregivingPatients": true, "leaveCaregiving": true,
}

// CaregiverGate answers 404 for exactly the caregiver routes while the flag is off, before sign-in, step-up, the
// idempotency cache and body parsing, so the routes are indistinguishable from routes that do not exist (the
// caregiver code is checked once more in the handlers, which never trusts this). It matches routes the way
// PolicyGate does. A nil flag means off.
func CaregiverGate(docs []*openapi3.T, on func() bool) (func(http.Handler) http.Handler, error) {
	match := chi.NewRouter()
	found := 0
	noop := func(http.ResponseWriter, *http.Request) {}
	for _, d := range docs {
		for path, item := range d.Paths.Map() {
			for method, op := range item.Operations() {
				// SpecDocs may upper-case the first letter (the generated Go name); the spec has it lower case.
				if id := op.OperationID; id != "" && caregiverOperations[strings.ToLower(id[:1])+id[1:]] {
					match.MethodFunc(method, path, noop)
					found++
				}
			}
		}
	}
	if found != len(caregiverOperations) {
		return nil, fmt.Errorf("gate: found %d of %d caregiver operations in the specs", found, len(caregiverOperations))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if on == nil || !on() {
				path := r.URL.Path
				if r.URL.RawPath != "" {
					path = r.URL.RawPath
				}
				if match.Match(chi.NewRouteContext(), r.Method, path) {
					problem.Write(w, r, problem.NotFound)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
