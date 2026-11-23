// Package public is the audit module's interface (S-096, audit-logging-design §2 and §4).
//
// audit.Record is NOT fire-and-forget: it inserts into audit.audit_outbox inside the
// caller's own database transaction (ADR-28). If the insert fails, the caller's
// transaction fails, so a read or change can never happen without its audit event.
// Every module role has INSERT (and nothing else) on that one table.
//
// Events hold opaque references only, never content (K-DP-04): no names, phone numbers,
// clinical text or free text from users.
package public

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Result of the audited action.
type Result string

const (
	Allowed Result = "allowed"
	Denied  Result = "denied"
	Failed  Result = "failed"
)

// Event is one audit record. Fields map to audit-logging-design §2.
type Event struct {
	EventID      string    // set by Record if empty
	OccurredAt   time.Time // set by Record if zero (UTC, microsecond precision)
	ActorRef     string    // pseudonymous user or service ID
	ActorRole    string    // role used for this action, e.g. "doctor"
	Action       string    // dotted verb, e.g. "records.read", "consent.revoke"
	SubjectRef   string    // the patient or doctor the data is about (may be empty)
	ResourceType string    // e.g. "prescription"
	ResourceRef  string    // opaque ID of the resource (may be empty)
	Purpose      string    // purpose or tier, e.g. "T1_current_care" (may be empty)
	GrantID      string    // permission or JIT grant used (may be empty)
	ReasonCode   string    // staff reason code (may be empty)
	TicketRef    string    // staff ticket (may be empty)
	Result       Result
	Client       string // app and version, e.g. "patient-android/1.0.3"
	IPHash       string // keyed hash of the client IP, never the IP itself
	RequestID    string
}

var (
	reCode = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	reRef  = regexp.MustCompile(`^[A-Za-z0-9_:\-]{1,80}$`)
	reTok  = regexp.MustCompile(`^[A-Za-z0-9_.:/+\-]{0,80}$`)
)

// ErrInvalid is returned for events that break the reference-only rules.
var ErrInvalid = errors.New("audit: invalid event")

// Validate checks that the event carries codes and references only.
func (e *Event) Validate() error {
	bad := func(f string) error { return fmt.Errorf("%w: field %s", ErrInvalid, f) }
	if !reCode.MatchString(e.Action) || len(e.Action) > 80 {
		return bad("action")
	}
	if e.ActorRef == "" || !reRef.MatchString(e.ActorRef) {
		return bad("actor_ref")
	}
	if e.ActorRole != "" && !reCode.MatchString(e.ActorRole) {
		return bad("actor_role")
	}
	for name, v := range map[string]string{"subject_ref": e.SubjectRef, "resource_ref": e.ResourceRef, "grant_id": e.GrantID, "ticket_ref": e.TicketRef, "request_id": e.RequestID} {
		if v != "" && !reRef.MatchString(v) {
			return bad(name)
		}
	}
	for name, v := range map[string]string{"resource_type": e.ResourceType, "purpose": e.Purpose, "reason_code": e.ReasonCode} {
		if v != "" && !(reCode.MatchString(v) || reRef.MatchString(v)) {
			return bad(name)
		}
	}
	if !reTok.MatchString(e.Client) || !reTok.MatchString(e.IPHash) {
		return bad("client")
	}
	switch e.Result {
	case Allowed, Denied, Failed:
	default:
		return bad("result")
	}
	return nil
}

// Record writes the event to the audit outbox inside tx. It takes a transaction, never a
// pool, so the audit insert is always part of the caller's unit of work.
func Record(ctx context.Context, tx pgx.Tx, e Event) error {
	if e.EventID == "" {
		e.EventID = ids.New()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
	if err := e.Validate(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit.audit_outbox
		(event_id, occurred_at, actor_ref, actor_role, action, subject_ref, resource_type, resource_ref,
		 purpose, grant_id, reason_code, ticket_ref, result, client, ip_hash, request_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		e.EventID, e.OccurredAt, e.ActorRef, e.ActorRole, e.Action, e.SubjectRef, e.ResourceType, e.ResourceRef,
		e.Purpose, e.GrantID, e.ReasonCode, e.TicketRef, string(e.Result), e.Client, e.IPHash, e.RequestID)
	if err != nil {
		return fmt.Errorf("audit: record: %w", err)
	}
	return nil
}
