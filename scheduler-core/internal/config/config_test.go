package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
		"SCHEDULER_CALLBACK_HMAC_SECRET": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))),
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
		"no database":   func(m map[string]string) { delete(m, "SCHEDULER_DATABASE_URL") },
		"http callback": func(m map[string]string) { m["SCHEDULER_CALLBACK_URL"] = "http://core.example.test/x" },
		"short secret": func(m map[string]string) {
			m["SCHEDULER_CALLBACK_HMAC_SECRET"] = base64.StdEncoding.EncodeToString([]byte("short"))
		},
		"raw secret":       func(m map[string]string) { m["SCHEDULER_CALLBACK_HMAC_SECRET"] = strings.Repeat("s", 40) },
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

// The callback key is the same bytes the Core API uses: both read the same secret, base64 of at
// least 32 bytes (Core: MUGIZH_SCHEDULER_CALLBACK_SECRET; review of S-093 found the two sides
// interpreted one value differently, so every signature would have failed).
func TestCallbackSecretMatchesCore(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	m := valid()
	m["SCHEDULER_CALLBACK_HMAC_SECRET"] = base64.StdEncoding.EncodeToString(key)
	c, err := Load(env(m), true)
	if err != nil || !bytes.Equal(c.CallbackSecret, key) {
		t.Fatalf("decoded key: %v %x", err, c.CallbackSecret)
	}
}
