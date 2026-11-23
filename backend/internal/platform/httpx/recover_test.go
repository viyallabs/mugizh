package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// TM-40 (S-178): a panic answers a generic 500 and logs only the request ID and route:
// never the body, query, headers, token or the panic value (which may hold personal data).
func TestTM40RecoverLogsNoRequestData(t *testing.T) {
	var buf bytes.Buffer
	h := Recover(logging.New(&buf, 0))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("patient +919876500111 diagnosis diabetes")
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/me/profile?phone=9876500111", strings.NewReader(`{"name":"Asha Synthetic"}`))
	req.Header.Set("Authorization", "Bearer secret-token-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "server.error") {
		t.Fatalf("generic 500 expected: %d %s", rec.Code, rec.Body.String())
	}
	out := buf.String() + rec.Body.String()
	for _, leak := range []string{"9876500111", "diabetes", "Asha", "secret-token-value", "phone="} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked:\n%s", leak, out)
		}
	}
	if !strings.Contains(buf.String(), "http.panic") {
		t.Fatalf("the panic is logged by name: %s", buf.String())
	}
}
