// Package app is the composition root: it wires modules, adapters and the HTTP server.
// It is the only package (with cmd/) allowed to import vendor adapters (rule A1).
package app

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/idempotency"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/telemetry"
)

// StrictHandler is the signature every generated strict handler shares.
type StrictHandler interface {
	~func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error)
}

// PolicyMiddleware enforces the operation's declared policy (deny by default). Operations
// without a policy are refused, so a route added without x-roles is unreachable.
func PolicyMiddleware[H StrictHandler](pol Policies, now func() time.Time) func(f H, operationID string) H {
	return func(f H, operationID string) H {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
			p, ok := pol[operationID]
			if !ok {
				return nil, problem.NotFound
			}
			principal, signedIn := ip.FromContext(ctx)
			for _, role := range p.Roles {
				if role == ip.RolePublic {
					return f(ctx, w, r, req)
				}
			}
			if !signedIn {
				return nil, problem.Unauthenticated
			}
			if err := ip.Check(principal, p, r.Header.Get("X-Acting-Role"), r.Header.Get("X-Clinic-Id"), now()); err != nil {
				return nil, err
			}
			return f(ctx, w, r, req)
		}
	}
}

// RequestError answers request decoding errors without echoing values.
func RequestError(w http.ResponseWriter, r *http.Request, _ error) {
	problem.Write(w, r, problem.BadRequest.WithDetail("The request body or parameters do not match the API"))
}

// ResponseError writes problems as they are and hides everything else behind a 500.
func ResponseError(log *slog.Logger) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var p *problem.Problem
		if !asProblem(err, &p) {
			// TM-39: the key service is down. Fail closed with a clear 503 (the app retries)
			// and raise an alert; never a plaintext fallback.
			if errors.Is(err, crypto.ErrKeyUnavailable) {
				raiseAlert(r.Context(), log, "crypto.key_unavailable")
				problem.Write(w, r, keyUnavailable)
				return
			}
			log.Error("http.handler_error", "request_id", httpx.RequestID(r.Context()), logging.Err(err))
		}
		problem.Write(w, r, err)
	}
}

func asProblem(err error, p **problem.Problem) bool {
	for e := err; e != nil; {
		if pp, ok := e.(*problem.Problem); ok {
			*p = pp
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// Authn resolves the bearer token, if any. A present but invalid token is refused at
// once (never treated as anonymous).
func Authn(a ip.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if h == "" {
				next.ServeHTTP(w, r)
				return
			}
			tok, ok := strings.CutPrefix(h, "Bearer ")
			if !ok || tok == "" {
				problem.Write(w, r, problem.Unauthenticated)
				return
			}
			p, err := a.Authenticate(r.Context(), tok)
			if err != nil {
				problem.Write(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(ip.WithPrincipal(r.Context(), p)))
		})
	}
}

// IPHasher stores a keyed hash of the client IP for audit events (never the raw IP).
func IPHasher(cr *crypto.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := hex.EncodeToString(cr.Keyed("client_ip", httpx.IPBucket(httpx.ClientIP(r.Context()))))[:32]
			next.ServeHTTP(w, r.WithContext(httpx.WithIPHash(r.Context(), h)))
		})
	}
}

// IdempotencyScope is the replay-cache scope: the user, or the IP hash when anonymous.
func IdempotencyScope(r *http.Request) string {
	if p, ok := ip.FromContext(r.Context()); ok {
		return "user:" + p.UserID
	}
	return "anon:" + httpx.IPHash(r.Context())
}

// RouterConfig configures the HTTP stack.
type RouterConfig struct {
	Log            *slog.Logger
	Crypto         *crypto.Service
	Auth           ip.Authenticator
	Idempotency    *idempotency.Store
	TrustedProxies int
	IPPerMinute    int
	// UserPerMinute limits each signed-in user on this instance (TM-50), whatever addresses they use. Default 300.
	UserPerMinute int
	Ready         func(ctx context.Context) error
	// Mount registers module routes on the API router (generated chi handlers).
	Mount []func(r chi.Router)
	// Raw registers routes outside the user-facing chain (no sign-in, no replay cache):
	// signed callbacks from vendors and scheduler-core, which authenticate themselves.
	Raw []func(r chi.Router)
	// Telemetry records a span and a duration per request (S-095); nil means none.
	Telemetry *telemetry.Telemetry
	// CaregiversOn reports the pack's "caregivers" flag. Nil means off: the caregiver routes answer 404 before sign-in.
	CaregiversOn func() bool
	// Now is the clock for policy checks (step-up age); nil means time.Now.
	Now func() time.Time
}

// NewRouter builds the HTTP handler.
func NewRouter(c RouterConfig) http.Handler {
	// Deny by default before anything parses the request (S-116). The specs were already
	// validated at start-up, so a failure here is a programming error.
	docs, err := SpecDocs()
	if err != nil {
		panic(err)
	}
	pol, err := BuildPolicies(docs...)
	if err != nil {
		panic(err)
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	careGate, err := CaregiverGate(docs, c.CaregiversOn)
	if err != nil {
		panic(err)
	}
	gate, err := PolicyGate(docs, pol, now)
	if err != nil {
		panic(err)
	}
	r := chi.NewRouter()
	r.Use(httpx.IDs(c.TrustedProxies))
	if c.Telemetry != nil {
		r.Use(c.Telemetry.HTTP)
	}
	r.Use(httpx.Recover(c.Log), httpx.SecurityHeaders, httpx.AccessLog(c.Log),
		httpx.LimitBodyFor(httpx.MaxBody, map[string]int64{
			"/v1/me/documents": UploadMaxBytes + 64<<10,
			// Doctor identity evidence arrives as base64 JSON (about 4/3 of the file size, review M7).
			"/v1/me/verification/evidence": EvidenceMaxBytes*4/3 + 64<<10,
		}))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { problem.Write(w, r, problem.NotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, problem.New(http.StatusMethodNotAllowed, "request.method_not_allowed", "Method not allowed"))
	})
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/health/ready", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if c.Ready != nil && c.Ready(ctx) != nil {
			problem.Write(w, req, problem.Unavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	perMin := c.IPPerMinute
	if perMin <= 0 {
		perMin = 600
	}
	// Self-authenticating callbacks still get a per-network limit (review M1), so a flood of
	// forged webhooks cannot exhaust the database before signatures are checked.
	r.Group(func(raw chi.Router) {
		raw.Use(httpx.RateLimitByIP(httpx.NewLimiter(1200, 300)))
		for _, m := range c.Raw {
			m(raw)
		}
	})
	r.Group(func(api chi.Router) {
		userMin := c.UserPerMinute
		if userMin <= 0 {
			userMin = 300
		}
		api.Use(careGate, httpx.RateLimitByIP(httpx.NewLimiter(perMin, perMin/4+1)), IPHasher(c.Crypto), Authn(c.Auth), RateLimitByUser(httpx.NewLimiter(userMin, userMin/4+1)),
			gate, c.Idempotency.Middleware(IdempotencyScope))
		for _, m := range c.Mount {
			m(api)
		}
	})
	return r
}
