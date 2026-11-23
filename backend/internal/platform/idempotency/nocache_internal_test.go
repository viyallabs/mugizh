package idempotency

import "testing"

// D-31, S-171, review F1: responses that carry credentials (session tokens, the TOTP
// secret, single-use download links, consult join tokens) are never stored for replay.
func TestCredentialRoutesAreNeverCached(t *testing.T) {
	for _, p := range []string{
		"/v1/auth/sessions", "/v1/auth/sessions/refresh", "/v1/auth/mfa/totp", "/v1/auth/step-up",
		"/v1/me/rights-requests/0b8f2c1e-1111-4a5b-9c3d-2e4f5a6b7c8d/export-link",
		"/v1/me/documents/0b8f2c1e-1111-4a5b-9c3d-2e4f5a6b7c8d/links",
		"/v1/consults/0b8f2c1e-1111-4a5b-9c3d-2e4f5a6b7c8d/join",
	} {
		if !noCache(p) {
			t.Errorf("%s must never be cached", p)
		}
	}
	for _, p := range []string{"/v1/me/documents", "/v1/appointments/holds", "/v1/consults/0b8f2c1e-1111-4a5b-9c3d-2e4f5a6b7c8d/pre-join"} {
		if noCache(p) {
			t.Errorf("%s should keep replay protection", p)
		}
	}
}
