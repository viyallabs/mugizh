// Package httpx holds HTTP middleware shared by every module (API standards §3, §8, §9):
// request and trace IDs, panic recovery, security headers, request size limits, the
// scrubbed access log and rate limiting.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyTraceID
	keyClientIP
)

// MaxBody is the default request size limit (API standards §8: 1 MB).
const MaxBody = 1 << 20

var reTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

func init() { problem.TraceIDFunc = func(r *http.Request) string { return TraceID(r.Context()) } }

// RequestID returns the request ID (a UUID) set by the IDs middleware.
func RequestID(ctx context.Context) string { s, _ := ctx.Value(keyRequestID).(string); return s }

// TraceID returns the W3C trace ID.
func TraceID(ctx context.Context) string { s, _ := ctx.Value(keyTraceID).(string); return s }

// ClientIP returns the client IP chosen by the IDs middleware (never logged raw).
func ClientIP(ctx context.Context) string { s, _ := ctx.Value(keyClientIP).(string); return s }

// IDs assigns a request ID, accepts a valid W3C traceparent (otherwise starts a trace),
// and records the client IP. trustedProxies is the number of reverse proxies in front of
// the API (load balancer, CDN) whose X-Forwarded-For entries are trusted; 0 trusts none.
func IDs(trustedProxies int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			trace := ""
			if m := reTraceparent.FindStringSubmatch(r.Header.Get("traceparent")); m != nil && m[1] != strings.Repeat("0", 32) {
				trace = m[1]
			} else {
				b := make([]byte, 16)
				_, _ = rand.Read(b)
				trace = hex.EncodeToString(b)
			}
			ctx := context.WithValue(r.Context(), keyRequestID, ids.New())
			ctx = context.WithValue(ctx, keyTraceID, trace)
			ctx = context.WithValue(ctx, keyClientIP, clientIP(r, trustedProxies))
			w.Header().Set("X-Request-Id", RequestID(ctx))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func clientIP(r *http.Request, trusted int) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trusted <= 0 {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	// The right-most entries were added by our own proxies; the one before them is the client.
	idx := len(parts) - trusted
	if idx < 0 || idx >= len(parts) {
		return host
	}
	ip := strings.TrimSpace(parts[idx])
	if net.ParseIP(ip) == nil {
		return host
	}
	return ip
}

// Recover turns a panic into a generic 500. The stack is logged without request data.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					if p == http.ErrAbortHandler {
						panic(p)
					}
					log.Error("http.panic", "request_id", RequestID(r.Context()), "route", routePattern(r))
					problem.Write(w, r, problem.Internal)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets headers for a JSON API: no caching of personal data (§9), no
// sniffing, no framing, no referrer, strict transport security.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// LimitBody caps request bodies at n bytes and refuses declared larger bodies early.
func LimitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				problem.Write(w, r, problem.TooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLog writes one line per request with the route pattern (never the raw path or
// query, which could hold identifiers), status and duration (K-DP-04).
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			log.Info("http.request", "method", r.Method, "route", routePattern(r), "status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(), "bytes", sw.bytes,
				"request_id", RequestID(r.Context()), "trace_id", TraceID(r.Context()))
		})
	}
}

func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

// Limiter is an in-process token bucket per key. It is the second line behind the WAF
// and the database-backed OTP limits (which are shared across instances).
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows `perMinute` requests per minute per key with the given burst.
func NewLimiter(perMinute, burst int) *Limiter {
	return &Limiter{rate: float64(perMinute) / 60, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow reports whether one more request for key is allowed, and how long to wait if not.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) > 200_000 { // bound memory under attack
		l.buckets = map[string]*bucket{}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// RateLimitByIP applies l per client IP.
func RateLimitByIP(l *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, wait := l.Allow("ip:" + IPBucket(ClientIP(r.Context())))
			if !ok {
				p := *problem.RateLimited
				p.RetryAfter = int(wait.Seconds()) + 1
				problem.Write(w, r, &p)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type ipHashKey struct{}

// WithIPHash stores the keyed hash of the client IP (computed by the composition root).
func WithIPHash(ctx context.Context, h string) context.Context {
	return context.WithValue(ctx, ipHashKey{}, h)
}

// IPHash returns the keyed client-IP hash, safe to store in audit events.
func IPHash(ctx context.Context) string { s, _ := ctx.Value(ipHashKey{}).(string); return s }

// IPBucket groups an address for rate limiting: IPv4 as is, IPv6 by its /64 network,
// because one subscriber usually controls a whole /64 and could otherwise rotate
// addresses to escape per-IP limits (review M2).
func IPBucket(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// LimitBodyFor is LimitBody with larger limits for specific POST paths (patient uploads);
// every other request keeps def.
func LimitBodyFor(def int64, postPaths map[string]int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := def
			if r.Method == http.MethodPost {
				if v, ok := postPaths[r.URL.Path]; ok {
					n = v
				}
			}
			LimitBody(n)(next).ServeHTTP(w, r)
		})
	}
}
