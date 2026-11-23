package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Header names scheduler-core sets (scheduler-core/internal/signing).
const (
	HeaderTimestamp = "X-Scheduler-Timestamp"
	HeaderSignature = "X-Scheduler-Signature"
	HeaderEventID   = "X-Scheduler-Event-Id"
)

const receiverHandler = "scheduler.occurrence_due"

// signOccurrence is scheduler-core's algorithm: "v1=" + hex(HMAC-SHA-256(secret, timestamp + "." + body)).
func signOccurrence(secret []byte, timestamp string, body []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(timestamp))
	h.Write([]byte("."))
	h.Write(body)
	return "v1=" + hex.EncodeToString(h.Sum(nil))
}

// SignOccurrence exposes the algorithm so tests and tools can produce valid callbacks.
func SignOccurrence(secret []byte, timestamp string, body []byte) string {
	return signOccurrence(secret, timestamp, body)
}

func verifyOccurrence(secret []byte, timestamp string, body []byte, signature string, now time.Time, tolerance time.Duration) bool {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	d := now.Sub(time.Unix(sec, 0))
	if d < 0 {
		d = -d
	}
	if d > tolerance {
		return false
	}
	want := signOccurrence(secret, timestamp, body)
	return strings.HasPrefix(signature, "v1=") && hmac.Equal([]byte(want), []byte(signature))
}

type occurrenceDue struct {
	OccurrenceID        string    `json:"occurrenceId"`
	ActivityExternalRef string    `json:"activityExternalRef"`
	TypeKey             string    `json:"typeKey"`
	DueAt               time.Time `json:"dueAt"`
	SubjectRef          string    `json:"subjectRef"`
	EventID             string    `json:"eventId"`
	// NotifyAt is set on a snoozed re-notification: the reminder is wanted again at this time (S-154).
	NotifyAt *time.Time `json:"notifyAt,omitempty"`
}

var (
	errBadSignature = problem.New(http.StatusUnauthorized, "notifications.bad_signature", "Signature or timestamp is not valid")
	errBadEvent     = problem.New(http.StatusBadRequest, "notifications.bad_event", "The event is not valid")
)

// OccurrenceDueHandler receives scheduler-core's signed occurrence.due callback. It is an
// internal endpoint, not in the public OpenAPI; Opus mounts it at
// /internal/v1/scheduler/occurrence-due. A stale timestamp or wrong signature is refused;
// a repeated event ID is acknowledged without a second dispatch.
func (m *Module) OccurrenceDueHandler(secret []byte, tolerance time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		if err != nil {
			problem.Write(w, r, errBadEvent)
			return
		}
		if !verifyOccurrence(secret, r.Header.Get(HeaderTimestamp), body, r.Header.Get(HeaderSignature), m.now(), tolerance) {
			problem.Write(w, r, errBadSignature)
			return
		}
		var ev occurrenceDue
		if json.Unmarshal(body, &ev) != nil || !ids.Valid(ev.EventID) || !ids.Valid(ev.OccurrenceID) || !ids.Valid(ev.SubjectRef) ||
			!reRef.MatchString(ev.ActivityExternalRef) || ev.DueAt.IsZero() || ev.EventID != r.Header.Get(HeaderEventID) {
			problem.Write(w, r, errBadEvent)
			return
		}
		if ev.NotifyAt != nil {
			// A snoozed re-notification: lateness (rule D-28), grouping and the SMS time follow the time it was
			// asked for, not the original due time.
			if ev.NotifyAt.Before(ev.DueAt) {
				problem.Write(w, r, errBadEvent)
				return
			}
			ev.DueAt = ev.NotifyAt.UTC()
		}
		key, ok := m.cfg.ReminderTemplates[ev.TypeKey]
		if !ok {
			problem.Write(w, r, errBadEvent)
			return
		}
		dup, err := m.enqueueReminder(r.Context(), ev, key)
		if err != nil {
			problem.Write(w, r, problem.Internal)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if dup {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"duplicate"}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	})
}

func (m *Module) enqueueReminder(ctx context.Context, ev occurrenceDue, templateKey string) (dup bool, err error) {
	t := m.tmpl[templateKey]
	now := m.now().UTC()
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		dup = false
		tag, err := tx.Exec(ctx, `INSERT INTO processed_event (handler, event_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, receiverHandler, ev.EventID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			dup = true
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO dispatch (id, dedupe_key, kind, template_key, recipient_ref, priority, category, reason,
			occurrence_ref, activity_ref, type_key, due_at, state, next_attempt_at, created_at, updated_at)
			VALUES ($1,$2,'reminder',$3,$4,$5,'service','scheduler.occurrence_due',$6,$7,$8,$9,'queued',$10,$11,$11)
			ON CONFLICT (dedupe_key) DO NOTHING`,
			ids.New(), "occ:"+ev.EventID, t.Key, ev.SubjectRef, string(t.Priority), ev.OccurrenceID, ev.ActivityExternalRef, ev.TypeKey,
			ev.DueAt.UTC(), now.Add(m.cfg.GroupHold), now)
		return err
	})
	if errors.Is(err, context.Canceled) {
		return false, err
	}
	return dup, err
}
