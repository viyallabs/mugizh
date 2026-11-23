// Package telemetry exports traces and metrics with OpenTelemetry (S-095, ADR-03).
//
// Privacy first (K-DP-04, TM-40): a span or metric carries only the HTTP method, the route
// pattern (never the path with its IDs), the status and the duration; never a query string,
// header, client address, user, body or error text. Telemetry goes to our own collector over
// OTLP/HTTP; the collector and its store must be in India (infrastructure R3, C07). With no
// endpoint configured (local, test) everything is a no-op.
//
// Spans reuse the request's W3C trace ID from httpx.IDs, so a log line's trace_id finds its
// trace.
package telemetry

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
)

// Config says where telemetry goes. Endpoint is the collector's base URL (for example
// https://otel.internal:4318); empty switches telemetry off. Plain http is accepted only when
// AllowInsecure is set (local, test and dev).
type Config struct {
	Service       string
	Env           string
	Endpoint      string
	SampleRatio   float64 // share of new traces kept; 0 means the default 0.1
	AllowInsecure bool
}

// Telemetry holds the providers and the instruments the platform records.
type Telemetry struct {
	tp       *sdktrace.TracerProvider
	mp       *sdkmetric.MeterProvider
	tracer   trace.Tracer
	duration metric.Float64Histogram
	alerts   metric.Int64Counter
}

// New starts telemetry. It never dials at start-up; exports are batched in the background.
func New(ctx context.Context, cfg Config) (*Telemetry, error) {
	t := &Telemetry{}
	var mp metric.MeterProvider = metricnoop.NewMeterProvider()
	var tp trace.TracerProvider = tracenoop.NewTracerProvider()
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && !(u.Scheme == "http" && cfg.AllowInsecure)) {
			return nil, errors.New("telemetry: the endpoint must be an https URL with no credentials or query (http only in local, test and dev)")
		}
		base := strings.TrimRight(cfg.Endpoint, "/")
		res := resource.NewSchemaless(attribute.String("service.name", cfg.Service), attribute.String("deployment.environment.name", cfg.Env))
		te, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(base+"/v1/traces"))
		if err != nil {
			return nil, err
		}
		me, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"))
		if err != nil {
			return nil, err
		}
		ratio := cfg.SampleRatio
		if ratio <= 0 || ratio > 1 {
			ratio = 0.1
		}
		t.tp = sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res), sdktrace.WithIDGenerator(idGen{}),
			sdktrace.WithSampler(randomSampler{ratio: ratio}))
		t.mp = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(time.Minute))))
		tp, mp = t.tp, t.mp
	}
	return t.instruments(tp, mp)
}

// newWith builds telemetry on given providers (tests).
func newWith(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider) (*Telemetry, error) {
	t := &Telemetry{tp: tp, mp: mp}
	return t.instruments(tp, mp)
}

func (t *Telemetry) instruments(tp trace.TracerProvider, mp metric.MeterProvider) (*Telemetry, error) {
	t.tracer = tp.Tracer("mugizh/http")
	meter := mp.Meter("mugizh")
	var err error
	if t.duration, err = meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP requests by route pattern")); err != nil {
		return nil, err
	}
	if t.alerts, err = meter.Int64Counter("ops.alerts", metric.WithDescription("Operational alerts raised, by code")); err != nil {
		return nil, err
	}
	return t, nil
}

// Shutdown flushes and stops the exporters.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.tp != nil {
		errs = append(errs, t.tp.Shutdown(ctx))
	}
	if t.mp != nil {
		errs = append(errs, t.mp.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

// Alert counts one operational alert by its code (codes are plain, checked by the caller).
func (t *Telemetry) Alert(ctx context.Context, code string) {
	t.alerts.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", code)))
}

// HTTP records one span and one duration per request. It must run after httpx.IDs. The span
// name and attributes use the route pattern, so no identifier from the path is exported.
func (t *Telemetry) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, span := t.tracer.Start(r.Context(), "http.request", trace.WithSpanKind(trace.SpanKindServer))
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		// Deferred, so an aborted request (http.ErrAbortHandler re-panics through Recover) is
		// still ended and measured.
		defer t.finish(ctx, r, span, sw, start)
		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}

func (t *Telemetry) finish(ctx context.Context, r *http.Request, span trace.Span, sw *statusWriter, start time.Time) {
	{
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		method := r.Method
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		default:
			method = "OTHER" // never echo an arbitrary method string
		}
		attrs := []attribute.KeyValue{attribute.String("http.request.method", method), attribute.String("http.route", route),
			attribute.Int("http.response.status_code", sw.status)}
		span.SetName(method + " " + route)
		span.SetAttributes(attrs...)
		span.End()
		t.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	}
}

// randomSampler keeps a share of new traces chosen by the server. The trace ID may come from a
// client's traceparent, so a sampler that reads the trace ID would let a client choose whether
// it is traced (review of S-095).
type randomSampler struct{ ratio float64 }

func (s randomSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	var b [8]byte
	_, _ = crand.Read(b[:])
	keep := float64(binary.BigEndian.Uint64(b[:])>>11)/(1<<53) < s.ratio
	d := sdktrace.Drop
	if keep {
		d = sdktrace.RecordAndSample
	}
	return sdktrace.SamplingResult{Decision: d, Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState()}
}

func (s randomSampler) Description() string { return "randomSampler" }

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// idGen reuses the request's trace ID (httpx.IDs) so logs and traces share it; span IDs and
// trace IDs outside a request are random.
type idGen struct{}

func (idGen) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	var tid trace.TraceID
	if b, err := hex.DecodeString(httpx.TraceID(ctx)); err == nil && len(b) == len(tid) {
		copy(tid[:], b)
	}
	if !tid.IsValid() {
		_, _ = crand.Read(tid[:])
	}
	return tid, newSpanID()
}

func (idGen) NewSpanID(context.Context, trace.TraceID) trace.SpanID { return newSpanID() }

func newSpanID() trace.SpanID {
	var sid trace.SpanID
	for !sid.IsValid() {
		_, _ = crand.Read(sid[:])
	}
	return sid
}
