package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// TC-10 (E-DP-13): plant a phone number, a name, a diagnosis, an OTP and an
// Aadhaar-like number in log calls; none may reach the output.
func TestTC10PlantedPersonalDataNeverReachesLogs(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug)

	aadhaar := ""
	for d := '0'; d <= '9'; d++ {
		if s := "29876543210" + string(d); scrub.Verhoeff(s) {
			aadhaar = s
		}
	}
	const (
		phone     = "+919812345678"
		name      = "Lakshmi Narayanan"
		diagnosis = "type 2 diabetes mellitus"
		otp       = "583920"
	)

	// 1. Interpolated message: replaced because it is not an event name.
	log.Info("sign-in for " + name + " " + phone)
	// 2. Not allow-listed keys.
	log.Info("clinical.note_saved", "diagnosis", diagnosis, "patient_name", name, "phone", phone, "otp", otp)
	// 3. Allow-listed free-text key carrying data.
	log.Error("identity.otp_failed", Err(errors.New("otp "+otp+" wrong for "+phone+" id "+aadhaar)))
	// 4. Reference keys must be UUIDs.
	log.Info("records.read", "user_key", phone, "subject_ref", name)
	// 5. Number under a reference key.
	log.Info("records.read", "actor_ref", int64(9812345678))
	// 6. Groups are flattened and checked.
	log.Info("http.request", slog.Group("patient", "name", name))
	log.With("diagnosis", diagnosis).Info("http.request")

	out := buf.String()
	for _, leak := range []string{phone, "9812345678", "98123", "Lakshmi", "Narayanan", "diabetes", otp, aadhaar} {
		if leak != "" && strings.Contains(out, leak) {
			t.Errorf("planted value %q reached the log output:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "log.non_event_message") {
		t.Error("free-text message was not replaced")
	}
}

func TestAllowedFieldsPass(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo)
	log.Info("http.request", "method", "GET", "route", "/v1/appointments/{id}", "status", 200,
		"duration_ms", 12, "request_id", "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")
	out := buf.String()
	for _, want := range []string{`"route":"/v1/appointments/{id}"`, `"status":200`, `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`, `"request_id":"0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}
