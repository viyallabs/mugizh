package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// PolicyGate enforces each operation's declared policy at the HTTP layer, before the
// replay cache and before the generated wrappers parse parameters or bodies (S-116). A
// caller without a declared role is refused 401/403 without the server reading the body.
// The strict-handler PolicyMiddleware still runs afterwards as a second layer.
//
// Requests that match no operation pass through to the router, which answers 404.
func PolicyGate(docs []*openapi3.T, pol Policies, now func() time.Time) (func(http.Handler) http.Handler, error) {
	match := chi.NewRouter()
	byRoute := map[string]ip.Policy{}
	noop := func(http.ResponseWriter, *http.Request) {}
	for _, d := range docs {
		for path, item := range d.Paths.Map() {
			for method, op := range item.Operations() {
				name := strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:]
				p, ok := pol[name]
				if !ok {
					return nil, fmt.Errorf("gate: %s %s has no policy", method, path)
				}
				match.MethodFunc(method, path, noop)
				byRoute[method+" "+path] = p
			}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Match exactly as chi's router does: on the raw (still percent-encoded) path when
			// there is one, so an encoded slash cannot make the gate and the router disagree.
			path := r.URL.Path
			if r.URL.RawPath != "" {
				path = r.URL.RawPath
			}
			rctx := chi.NewRouteContext()
			if !match.Match(rctx, r.Method, path) {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := byRoute[r.Method+" "+rctx.RoutePattern()]
			if !ok {
				problem.Write(w, r, problem.NotFound)
				return
			}
			for _, role := range p.Roles {
				if role == ip.RolePublic {
					next.ServeHTTP(w, r)
					return
				}
			}
			principal, signedIn := ip.FromContext(r.Context())
			if !signedIn {
				problem.Write(w, r, problem.Unauthenticated)
				return
			}
			if err := ip.Check(principal, p, r.Header.Get("X-Acting-Role"), r.Header.Get("X-Clinic-Id"), now()); err != nil {
				problem.Write(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// RateLimitByUser applies l per signed-in user (TM-50): one account cannot escape the per-IP limit by spreading its
// requests over many addresses. Anonymous requests pass (the per-IP limit covers them). The key is the user ID,
// which never leaves the process.
func RateLimitByUser(l *httpx.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := ip.FromContext(r.Context()); ok && p.UserID != "" {
				if allowed, wait := l.Allow("user:" + p.UserID); !allowed {
					pr := *problem.RateLimited
					pr.RetryAfter = int(wait.Seconds()) + 1
					problem.Write(w, r, &pr)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
