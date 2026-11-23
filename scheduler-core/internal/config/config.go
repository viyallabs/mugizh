// Package config reads scheduler-core's settings from the environment. Secrets (database passwords, the
// callback HMAC secret, the service token digests) come only from the environment, never from code (K-SEC-05),
// and are never logged. Country values do not belong here (K-GLB-01).
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/dispatch"
)

// Config is the validated configuration.
type Config struct {
	ListenAddr      string
	DatabaseURL     string // application login role
	MigrateURL      string // migration owner (migrate command only)
	RequireDBTLS    bool
	CallbackURL     string
	CallbackSecret  []byte
	TokenHashes     [][]byte
	Dispatch        dispatch.Config
	CallbackTimeout time.Duration
	DBMaxConns      int32
}

// Getenv is os.Getenv; tests replace it.
type Getenv func(string) string

// Load reads and validates the configuration. serve is true for the server (needs the application database,
// callback and tokens); the migrate command needs only the migration URL.
func Load(get Getenv, serve bool) (*Config, error) {
	if get == nil {
		get = os.Getenv
	}
	c := &Config{ListenAddr: or(get("SCHEDULER_LISTEN_ADDR"), ":8081"), DatabaseURL: get("SCHEDULER_DATABASE_URL"), MigrateURL: get("SCHEDULER_MIGRATE_DATABASE_URL"),
		RequireDBTLS: get("SCHEDULER_ALLOW_INSECURE_DB") != "1", CallbackURL: get("SCHEDULER_CALLBACK_URL")}
	if !serve {
		if c.MigrateURL == "" {
			return nil, errors.New("config: SCHEDULER_MIGRATE_DATABASE_URL is required")
		}
		return c, nil
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("config: SCHEDULER_DATABASE_URL is required")
	}
	if err := dispatch.ValidateCallbackURL(c.CallbackURL, get("SCHEDULER_ALLOW_INSECURE_CALLBACK") == "1"); err != nil {
		return nil, fmt.Errorf("config: SCHEDULER_CALLBACK_URL: %w", err)
	}
	secret := get("SCHEDULER_CALLBACK_HMAC_SECRET")
	if len(secret) < 32 {
		return nil, errors.New("config: SCHEDULER_CALLBACK_HMAC_SECRET must be at least 32 characters")
	}
	c.CallbackSecret = []byte(secret)
	for _, h := range strings.Split(get("SCHEDULER_SERVICE_TOKEN_SHA256"), ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 {
			return nil, errors.New("config: SCHEDULER_SERVICE_TOKEN_SHA256 must be comma-separated SHA-256 hex digests")
		}
		c.TokenHashes = append(c.TokenHashes, b)
	}
	if len(c.TokenHashes) == 0 {
		return nil, errors.New("config: SCHEDULER_SERVICE_TOKEN_SHA256 is required (deny by default)")
	}
	c.CallbackTimeout = 10 * time.Second
	c.DBMaxConns = int32Env(get, "SCHEDULER_DB_MAX_CONNS", 40)
	c.Dispatch = dispatch.Config{
		Workers:     intEnv(get, "SCHEDULER_DISPATCH_WORKERS", 200),
		MaxAttempts: intEnv(get, "SCHEDULER_DISPATCH_MAX_ATTEMPTS", 8),
	}
	return c, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intEnv(get Getenv, key string, def int) int {
	if v := get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func int32Env(get Getenv, key string, def int32) int32 {
	if v := get(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			return int32(n)
		}
	}
	return def
}
