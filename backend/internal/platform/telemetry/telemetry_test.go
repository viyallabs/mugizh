package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
)

// TM-40, K-DP-04 (S-095): a span and a metric carry only the method, the route pattern and the
// status: no ID from the path, no query, no header, no client address. The span's trace ID is the
// request's trace ID from httpx.IDs, so logs and traces join up.
func TestS095SpansCarryNoPersonalData(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans), sdktrace.WithIDGenerator(idGen{}), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tel, err := newWith(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(httpx.IDs(0), tel.HTTP)
	var traceID string
	r.Get("/v1/patients/{patientId}/documents", func(w http.ResponseWriter, req *http.Request) {
		traceID = httpx.TraceID(req.Context())
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/patients/0190d7c4-aaaa-7000-8000-000000000001/documents?phone=9876500111", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("User-Agent", "Asha's phone")
	r.ServeHTTP(httptest.NewRecorder(), req)

	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("want one span, got %d", len(got))
	}
	s := got[0]
	if s.Name != "GET /v1/patients/{patientId}/documents" {
		t.Fatalf("span name must use the route pattern: %q", s.Name)
	}
	if s.SpanContext.TraceID().String() != traceID {
		t.Fatalf("span trace %s must be the request trace %s", s.SpanContext.TraceID(), traceID)
	}
	var all strings.Builder
	all.WriteString(s.Name)
	for _, a := range s.Attributes {
		all.WriteString(" " + string(a.Key) + "=" + a.Value.Emit())
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && m.Name == "http.server.request.duration" {
				for _, dp := range h.DataPoints {
					found = true
					for _, a := range dp.Attributes.ToSlice() {
						all.WriteString(" " + string(a.Key) + "=" + a.Value.Emit())
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("request duration not recorded")
	}
	out := all.String()
	for _, leak := range []string{"0190d7c4", "9876500111", "phone", "secret-token", "Asha", "192.0.2"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked into telemetry: %s", leak, out)
		}
	}
	if !strings.Contains(out, "http.response.status_code=418") {
		t.Fatalf("status missing: %s", out)
	}
}

func TestS095EndpointMustBeHTTPS(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"http://collector:4318", "https://user:pw@collector:4318", "https://collector:4318?x=1", "collector:4318", "ftp://x"} {
		if _, err := New(ctx, Config{Service: "api", Env: "production", Endpoint: bad}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, err := New(ctx, Config{Service: "api", Env: "local", Endpoint: "http://collector:4318", AllowInsecure: true}); err != nil {
		t.Fatalf("plain http in local: %v", err)
	}
	tel, err := New(ctx, Config{Service: "api", Env: "local"}) // no endpoint: no-op
	if err != nil {
		t.Fatal(err)
	}
	tel.Alert(ctx, "x.y")
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

// Review of S-095: the sampling decision is the server's, whatever trace ID a client sends.
func TestS095SamplingIgnoresTheTraceID(t *testing.T) {
	s := randomSampler{ratio: 0.5}
	kept := 0
	for i := 0; i < 2000; i++ {
		if s.ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background()}).Decision == sdktrace.RecordAndSample {
			kept++
		}
	}
	if kept < 800 || kept > 1200 {
		t.Fatalf("about half must be kept, got %d of 2000", kept)
	}
	if (randomSampler{ratio: 0}).ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background()}).Decision != sdktrace.Drop {
		t.Fatal("ratio 0 keeps nothing")
	}
}
