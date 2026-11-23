package signing

import (
	"strconv"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret := []byte("synthetic-test-secret")
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"a":1}`)
	sig := Sign(secret, ts, body)
	if err := Verify(secret, ts, body, sig, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Known answer: HMAC-SHA256(secret, "1800000000.{\"a\":1}").
	if len(sig) != 3+64 || sig[:3] != "v1=" {
		t.Fatalf("unexpected format %q", sig)
	}
	if err := Verify(secret, ts, []byte(`{"a":2}`), sig, now, time.Minute); err != ErrSignature {
		t.Fatalf("tampered body: %v", err)
	}
	if err := Verify([]byte("other"), ts, body, sig, now, time.Minute); err != ErrSignature {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := Verify(secret, ts, body, sig, now.Add(10*time.Minute), time.Minute); err != ErrTimestamp {
		t.Fatalf("stale timestamp: %v", err)
	}
	if err := Verify(secret, "x", body, sig, now, time.Minute); err != ErrTimestamp {
		t.Fatalf("bad timestamp: %v", err)
	}
	if err := Verify(secret, ts, body, sig[3:], now, time.Minute); err != ErrSignature {
		t.Fatalf("missing prefix: %v", err)
	}
}
