// Package logging is the only way the backend writes logs (K-DP-04, audit-logging-design §3).
//
// Three rules are enforced at runtime, so a mistake in calling code cannot leak data:
//  1. The message must be an event name (lower-case, dots and underscores, for example
//     "http.request"). Anything else is replaced, so request data can never be
//     interpolated into a message.
//  2. Only allow-listed attribute keys are written. Unknown keys are replaced by a marker.
//  3. Every string value is scrubbed for phone numbers, emails, tokens and prohibited
//     identifiers (package scrub). Pseudonymous reference keys must look like UUIDs.
package logging

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

type keyKind int

const (
	kindCode   keyKind = iota // short codes and enums: scrubbed with Text
	kindFree                  // free text such as error strings: scrubbed with FreeText
	kindRef                   // pseudonymous IDs: must be a UUID, else redacted
	kindHex                   // trace and span IDs: must be lower-case hex
	kindNumber                // numbers; strings are scrubbed with FreeText
)

// allowed lists every key that may appear in a log line. Adding a key is a reviewed change.
var allowed = map[string]keyKind{
	// request plumbing
	"request_id": kindRef, "trace_id": kindHex, "span_id": kindHex,
	"method": kindCode, "route": kindCode, "status": kindNumber, "duration_ms": kindNumber,
	"bytes": kindNumber, "client": kindCode, "operation": kindCode,
	// actors and subjects: pseudonymous references only (S-044)
	"user_key": kindRef, "actor_ref": kindRef, "subject_ref": kindRef, "resource_ref": kindRef,
	"grant_id": kindRef, "event_id": kindRef, "job_id": kindNumber,
	// outcomes
	"module": kindCode, "op": kindCode, "outcome": kindCode, "error_code": kindCode,
	"error": kindFree, "reason": kindCode, "panic": kindFree,
	// workers, adapters, config
	"event_type": kindCode, "vendor": kindCode, "adapter": kindCode, "latency_ms": kindNumber,
	"job": kindCode, "queue": kindCode, "attempt": kindNumber, "count": kindNumber,
	"seq": kindNumber, "backlog_seconds": kindNumber, "component": kindCode,
	"pack": kindCode, "version": kindCode, "env": kindCode, "kind": kindCode, "state": kindCode,
	"addr": kindCode, "partition": kindCode, "hits": kindNumber,
}

var (
	reEvent = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	reUUID  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reHexID = regexp.MustCompile(`^[0-9a-f]{16,32}$`)
)

// New returns a JSON logger that applies the rules above.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(&handler{next: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

type handler struct {
	next   slog.Handler
	prefix string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	if !reEvent.MatchString(msg) {
		msg = "log.non_event_message"
	}
	out := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.clean(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		cleaned = append(cleaned, h.clean(a))
	}
	return &handler{next: h.next.WithAttrs(cleaned), prefix: h.prefix}
}

// WithGroup flattens groups into dotted keys so the allow-list sees the full key.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{next: h.next, prefix: h.prefix + name + "."}
}

func (h *handler) clean(a slog.Attr) slog.Attr {
	key := h.prefix + a.Key
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		// Groups are flattened; nested attributes are checked one by one.
		parts := a.Value.Group()
		cleaned := make([]any, 0, len(parts))
		sub := &handler{next: h.next, prefix: key + "."}
		for _, p := range parts {
			c := sub.clean(p)
			cleaned = append(cleaned, slog.Attr{Key: strings.TrimPrefix(c.Key, key+"."), Value: c.Value})
		}
		return slog.Group(a.Key, cleaned...)
	}
	kind, ok := allowed[key]
	if !ok {
		scrubCount()
		return slog.String(a.Key, "[not-allow-listed]")
	}
	switch a.Value.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		if kind == kindNumber || kind == kindCode {
			return slog.Attr{Key: a.Key, Value: a.Value}
		}
		// A number under a reference or free-text key could be a phone number.
		return slog.String(a.Key, "[REDACTED]")
	}
	s := a.Value.String()
	switch kind {
	case kindRef:
		if !reUUID.MatchString(s) {
			s = "[REDACTED]"
		}
	case kindHex:
		if !reHexID.MatchString(s) {
			s = "[REDACTED]"
		}
	case kindFree, kindNumber:
		s = scrub.FreeText(s)
	default:
		s = scrub.Text(s)
	}
	return slog.String(a.Key, s)
}

// scrubCount records a dropped key in the same counter as scrubber hits, so the alert
// "personal data reached a log call" fires for both.
func scrubCount() { scrub.Count() }

// Err returns the standard attribute for an error. The text is scrubbed when written.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	return slog.String("error", err.Error())
}
