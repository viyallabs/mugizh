// Package idempotency is the HTTP replay cache (API standards §6): the same
// Idempotency-Key with the same body returns the stored response; with a different body
// it is refused (409 idempotency.key_reused); while the first request still runs it is
// refused (409 idempotency.in_progress). Keys are scoped per user and kept 24 hours.
package idempotency

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the migrations (platform schema).
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "platform", Schema: "platform", FS: sub}
}

// TTL is how long a key is remembered.
const TTL = 24 * time.Hour

// Lease is how long an "in progress" key blocks retries if its request never finished.
const Lease = 2 * time.Minute

var (
	errKeyReused  = problem.New(http.StatusConflict, "idempotency.key_reused", "This Idempotency-Key was used with a different request")
	errInProgress = problem.New(http.StatusConflict, "idempotency.in_progress", "The first request with this key is still running")
	errBadKey     = problem.New(http.StatusBadRequest, "idempotency.key_invalid", "Idempotency-Key must be a UUID")
)

// Store keeps keys in the platform schema.
type Store struct {
	pool *pgxpool.Pool // platform role
	cr   *crypto.Service
	now  func() time.Time
}

// New builds the store.
func New(pool *pgxpool.Pool, cr *crypto.Service) *Store {
	return &Store{pool: pool, cr: cr, now: time.Now}
}

func aad(scope, key string) crypto.AAD {
	return crypto.AAD{Table: "platform.http_idempotency", Column: "body_ct", RowID: scope + "|" + key}
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int) {
	if r.status == 0 {
		r.status = s
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.body.Write(b)
}

// NoCache lists path prefixes whose responses carry credentials (session tokens, the TOTP
// secret). They are never stored in the replay cache; their own single-use checks (OTP
// consumed once, refresh-token rotation) already stop duplicates.
var NoCache = []string{"/v1/auth/sessions", "/v1/auth/mfa/", "/v1/auth/step-up"}

// Middleware applies to POST requests that carry an Idempotency-Key. scopeOf returns the
// caller's scope (user ID, or a keyed IP hash for anonymous calls).
func (s *Store) Middleware(scopeOf func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if r.Method != http.MethodPost || key == "" || noCache(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if !ids.Valid(key) {
				problem.Write(w, r, errBadKey)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				problem.Write(w, r, problem.TooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			h := sha256.New()
			// The query and the acting role or clinic change the meaning of a request (review L2).
			h.Write([]byte(r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery + "\n" +
				r.Header.Get("X-Acting-Role") + "\n" + r.Header.Get("X-Clinic-Id") + "\n"))
			h.Write(body)
			reqHash := h.Sum(nil)
			scope := scopeOf(r)
			ctx := r.Context()

			replay, err := s.begin(ctx, scope, key, reqHash)
			if err != nil {
				problem.Write(w, r, err)
				return
			}
			if replay != nil {
				w.Header().Set("Content-Type", replay.contentType)
				w.Header().Set("Idempotent-Replayed", "true")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(replay.status)
				_, _ = w.Write(replay.body)
				return
			}
			rec := &recorder{header: http.Header{}}
			// A panic must not leave the key stuck "in progress" (review L1).
			defer func() {
				if p := recover(); p != nil {
					_, _ = s.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM http_idempotency WHERE scope=$1 AND key=$2`, scope, key)
					panic(p)
				}
			}()
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			// Only successful results are remembered. Errors (403 step-up required, 409, 429)
			// must not be replayed for 24 hours after the client fixes the cause (review M4).
			if rec.status < 200 || rec.status >= 300 {
				_, _ = s.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM http_idempotency WHERE scope=$1 AND key=$2`, scope, key)
			} else if err := s.finish(context.WithoutCancel(ctx), scope, key, rec); err != nil {
				_, _ = s.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM http_idempotency WHERE scope=$1 AND key=$2`, scope, key)
			}
			for k, v := range rec.header {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body.Bytes())
		})
	}
}

type stored struct {
	status      int
	contentType string
	body        []byte
}

func (s *Store) begin(ctx context.Context, scope, key string, reqHash []byte) (*stored, error) {
	now := s.now().UTC()
	for attempt := 0; attempt < 2; attempt++ {
		tag, err := s.pool.Exec(ctx, `INSERT INTO http_idempotency (scope, key, request_hash, state, expires_at, created_at) VALUES ($1,$2,$3,'in_progress',$4,$5)
			ON CONFLICT (scope, key) DO NOTHING`, scope, key, reqHash, now.Add(TTL), now)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 1 {
			return nil, nil
		}
		var h []byte
		var state string
		var status *int
		var ct *string
		var body crypto.Sealed[crypto.ServiceKey]
		var exp, created time.Time
		err = s.pool.QueryRow(ctx, `SELECT request_hash, state, status, content_type, body_ct, expires_at, created_at FROM http_idempotency WHERE scope=$1 AND key=$2`,
			scope, key).Scan(&h, &state, &status, &ct, &body, &exp, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !now.Before(exp) {
			_, _ = s.pool.Exec(ctx, `DELETE FROM http_idempotency WHERE scope=$1 AND key=$2 AND expires_at <= $3`, scope, key, now)
			continue
		}
		if !hmac.Equal(h, reqHash) {
			return nil, errKeyReused
		}
		if state != "done" || status == nil {
			// A request that died without cleaning up releases its key after the lease.
			if created.Before(now.Add(-Lease)) {
				_, _ = s.pool.Exec(ctx, `DELETE FROM http_idempotency WHERE scope=$1 AND key=$2 AND state='in_progress' AND created_at < $3`,
					scope, key, now.Add(-Lease))
				continue
			}
			return nil, errInProgress
		}
		out := &stored{status: *status}
		if ct != nil {
			out.contentType = *ct
		}
		if !body.IsZero() {
			b, err := crypto.Open(ctx, s.cr, aad(scope, key), body)
			if err != nil {
				return nil, err
			}
			out.body = b
		}
		return out, nil
	}
	return nil, errInProgress
}

func (s *Store) finish(ctx context.Context, scope, key string, rec *recorder) error {
	var sealed []byte
	if rec.body.Len() > 0 {
		b, err := crypto.SealService(ctx, s.cr, "platform.idempotency", aad(scope, key), rec.body.Bytes())
		if err != nil {
			return err
		}
		sealed = b.Bytes()
	}
	_, err := s.pool.Exec(ctx, `UPDATE http_idempotency SET state='done', status=$3, content_type=$4, body_ct=$5 WHERE scope=$1 AND key=$2`,
		scope, key, rec.status, rec.header.Get("Content-Type"), sealed)
	return err
}

// Purge deletes expired keys (run by the worker).
func (s *Store) Purge(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM http_idempotency WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}

// NoCacheSuffixes are routes whose responses carry a single-use credential: download links
// (S-171), and consult join tokens (S-136; tokens are never stored, review F1). A replay must
// not hand out the same credential again, and the cache must never hold it.
var NoCacheSuffixes = []string{"/export-link", "/links", "/join"}

func noCache(path string) bool {
	for _, p := range NoCache {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, s := range NoCacheSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}
