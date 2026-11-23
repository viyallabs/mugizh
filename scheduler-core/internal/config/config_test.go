package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func valid() map[string]string {
	sum := sha256.Sum256([]byte("synthetic-token"))
	return map[string]string{
		"SCHEDULER_DATABASE_URL":         "postgres://app@localhost/sched",
		"SCHEDULER_CALLBACK_URL":         "https://core.example.test/internal/occurrence-due",
		"SCHEDULER_CALLBACK_HMAC_SECRET": strings.Repeat("s", 40),
		"SCHEDULER_SERVICE_TOKEN_SHA256": hex.EncodeToString(sum[:]),
	}
}

func TestLoadAcceptsValid(t *testing.T) {
	c, err := Load(env(valid()), true)
	if err != nil || len(c.TokenHashes) != 1 || !c.RequireDBTLS {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestLoadFailsClosed(t *testing.T) {
	for name, mut := range map[string]func(map[string]string){
		"no database":      func(m map[string]string) { delete(m, "SCHEDULER_DATABASE_URL") },
		"http callback":    func(m map[string]string) { m["SCHEDULER_CALLBACK_URL"] = "http://core.example.test/x" },
		"short secret":     func(m map[string]string) { m["SCHEDULER_CALLBACK_HMAC_SECRET"] = "short" },
		"no token":         func(m map[string]string) { delete(m, "SCHEDULER_SERVICE_TOKEN_SHA256") },
		"token not a hash": func(m map[string]string) { m["SCHEDULER_SERVICE_TOKEN_SHA256"] = "plain-token" },
		"empty callback":   func(m map[string]string) { delete(m, "SCHEDULER_CALLBACK_URL") },
	} {
		m := valid()
		mut(m)
		if _, err := Load(env(m), true); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	m := valid()
	m["SCHEDULER_CALLBACK_URL"] = "http://127.0.0.1:9/x"
	m["SCHEDULER_ALLOW_INSECURE_CALLBACK"] = "1"
	if _, err := Load(env(m), true); err != nil {
		t.Fatalf("insecure callback is allowed only when explicitly enabled: %v", err)
	}
	if _, err := Load(env(map[string]string{}), false); err == nil {
		t.Fatal("migrate needs SCHEDULER_MIGRATE_DATABASE_URL")
	}
}
