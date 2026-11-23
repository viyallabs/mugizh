package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
)

// TM-50: each signed-in user has their own budget, whatever addresses they use; anonymous requests are left to the
// per-IP limit; one user's flood does not touch another user.
func TestTM50RateLimitByUser(t *testing.T) {
	h := RateLimitByUser(httpx.NewLimiter(60, 2))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(user, addr string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.RemoteAddr = addr
		if user != "" {
			req = req.WithContext(ip.WithPrincipal(req.Context(), &ip.Principal{UserID: user}))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	a := "0193a000-0000-7000-8000-00000000000a"
	if call(a, "10.0.0.1:1") != 204 || call(a, "10.0.0.2:1") != 204 {
		t.Fatal("within the burst")
	}
	if got := call(a, "10.0.0.3:1"); got != http.StatusTooManyRequests {
		t.Fatalf("a third address does not reset the user's budget: %d", got)
	}
	if call("0193a000-0000-7000-8000-00000000000b", "10.0.0.3:1") != 204 {
		t.Fatal("another user is not affected")
	}
	for i := 0; i < 5; i++ {
		if call("", "10.0.0.9:1") != 204 {
			t.Fatal("anonymous requests are left to the per-IP limit")
		}
	}
}
