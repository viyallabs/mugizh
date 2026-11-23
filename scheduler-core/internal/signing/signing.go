// Package signing signs and verifies the occurrence.due callback: HMAC-SHA-256 over timestamp + "." + body,
// sent as "v1=<hex>" in X-Scheduler-Signature with the Unix-seconds timestamp in X-Scheduler-Timestamp.
// The Core API verifies with Verify (constant-time compare, bounded clock skew to limit replay).
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header names.
const (
	HeaderTimestamp = "X-Scheduler-Timestamp"
	HeaderSignature = "X-Scheduler-Signature"
	HeaderEventID   = "X-Scheduler-Event-Id"
)

// Sign returns "v1=<hex>" for the timestamp and body.
func Sign(secret []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

// Errors from Verify.
var (
	ErrSignature = errors.New("signing: signature does not match")
	ErrTimestamp = errors.New("signing: timestamp missing or outside the allowed window")
)

// Verify checks signature against the body and that the timestamp is within tolerance of now.
func Verify(secret []byte, timestamp string, body []byte, signature string, now time.Time, tolerance time.Duration) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrTimestamp
	}
	d := now.Sub(time.Unix(sec, 0))
	if d < 0 {
		d = -d
	}
	if d > tolerance {
		return ErrTimestamp
	}
	want := Sign(secret, timestamp, body)
	if !strings.HasPrefix(signature, "v1=") || !hmac.Equal([]byte(want), []byte(signature)) {
		return ErrSignature
	}
	return nil
}
