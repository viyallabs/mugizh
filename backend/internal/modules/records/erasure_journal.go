package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// The erasure journal (S-185, E-DP-04; backup-and-restore.md sections 2 and 3).
//
// A Core database restore can roll back an erasure request that is still in its undo
// window or RET-18 hold: the patient key is not destroyed yet, so crypto-shredding does
// not protect the data, and the restored account would be visible again. Every erasure
// request, undo and completion is therefore also written to write-once storage (the
// same store as the key tombstones), and ReplayJournal re-applies it before the service
// opens. Entries hold references and times only: no contact details, no health data.

const journalPrefix = "erasure-journal/"

type journalEvent string

const (
	journalRequested journalEvent = "requested"
	journalCancelled journalEvent = "cancelled"
	journalCompleted journalEvent = "completed"
)

// journalEntry is self-contained: any one entry is enough to rebuild the request.
type journalEntry struct {
	V          int          `json:"v"`
	RequestRef string       `json:"requestRef"`
	SubjectRef string       `json:"subjectRef"`
	Event      journalEvent `json:"event"`
	At         time.Time    `json:"at"`
	ReceivedAt time.Time    `json:"receivedAt"`
	UndoUntil  time.Time    `json:"undoUntil"`
}

func journalKey(requestRef string, ev journalEvent) string {
	return journalPrefix + requestRef + "/" + string(ev) + ".json"
}

// journal writes one entry for a committed change and marks it written. A failure is
// logged and never undoes the patient's request; journalCatchUp retries every minute.
func (r *Rights) journal(ctx context.Context, requestRef string, ev journalEvent) {
	if err := r.writeJournal(ctx, requestRef, ev); err != nil {
		r.m.log.Warn("records.erasure_journal_write_failed", "resource_ref", requestRef, "state", string(ev), logging.Err(err))
	}
}

func (r *Rights) writeJournal(ctx context.Context, requestRef string, ev journalEvent) error {
	e := journalEntry{V: 1, RequestRef: requestRef, Event: ev}
	var undo *time.Time
	var at *time.Time
	if err := r.m.pool.QueryRow(ctx, `SELECT subject_ref::text, received_at, undo_until,
		CASE $2 WHEN 'cancelled' THEN cancelled_at WHEN 'completed' THEN completed_at ELSE received_at END
		FROM rights_request WHERE id=$1 AND type='erasure'`, requestRef, string(ev)).Scan(&e.SubjectRef, &e.ReceivedAt, &undo, &at); err != nil {
		return err
	}
	if undo == nil || at == nil {
		return errors.New("records: erasure without undo or event time")
	}
	e.UndoUntil, e.At = undo.UTC(), at.UTC()
	e.ReceivedAt = e.ReceivedAt.UTC()
	doc, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := r.d.Blobs.PutWriteOnce(ctx, journalKey(requestRef, ev), doc); err != nil && !errors.Is(err, blobstore.ErrExists) {
		return err
	}
	_, err = r.m.pool.Exec(ctx, `UPDATE rights_request SET journaled = array_append(journaled, $2)
		WHERE id=$1 AND NOT ($2 = ANY (journaled))`, requestRef, string(ev))
	return err
}

// journalCatchUp writes entries that an object-store outage left unwritten.
func (r *Rights) journalCatchUp(ctx context.Context) error {
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, ev FROM (
		SELECT id, received_at, 'requested' AS ev FROM rights_request WHERE type='erasure' AND NOT ('requested' = ANY (journaled))
		UNION ALL
		SELECT id, received_at, status FROM rights_request WHERE type='erasure' AND status IN ('cancelled','completed') AND NOT (status = ANY (journaled))
	) d ORDER BY received_at, ev DESC LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct{ id, ev string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ev); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var firstErr error
	for _, d := range list {
		if err := r.writeJournal(ctx, d.id, journalEvent(d.ev)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ReplayJournal re-applies the erasure journal after a restore. Call it at start-up,
// after the key tombstones are replayed and before the service opens; it is safe on
// every start and on several instances at once. It returns how many requests it
// changed. Any unreadable entry is an error: the service must not open (fail closed).
func (r *Rights) ReplayJournal(ctx context.Context) (int, error) {
	keys, err := r.d.Blobs.List(ctx, journalPrefix)
	if err != nil {
		return 0, fmt.Errorf("records: list erasure journal: %w", err)
	}
	byRequest := map[string]*journalState{}
	for _, k := range keys {
		b, err := r.d.Blobs.Get(ctx, k)
		if err != nil {
			return 0, fmt.Errorf("records: read erasure journal: %w", err)
		}
		var e journalEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return 0, fmt.Errorf("records: bad erasure journal entry %s: %w", k, err)
		}
		if e.V != 1 || !allUUID(e.RequestRef, e.SubjectRef) || k != journalKey(e.RequestRef, e.Event) ||
			e.ReceivedAt.IsZero() || e.UndoUntil.IsZero() || e.At.IsZero() ||
			(e.Event != journalRequested && e.Event != journalCancelled && e.Event != journalCompleted) {
			return 0, fmt.Errorf("records: bad erasure journal entry %s", k)
		}
		s := byRequest[e.RequestRef]
		if s == nil {
			s = &journalState{first: e}
			byRequest[e.RequestRef] = s
		} else if s.first.SubjectRef != e.SubjectRef {
			return 0, fmt.Errorf("records: erasure journal entries disagree for %s", e.RequestRef)
		}
		switch e.Event {
		case journalCancelled:
			s.cancelled = &e
		case journalCompleted:
			s.completed = true
		}
	}
	refs := make([]string, 0, len(byRequest))
	for k := range byRequest {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	n := 0
	for _, ref := range refs {
		changed, err := r.replayOne(ctx, ref, byRequest[ref])
		if err != nil {
			return n, err
		}
		if changed {
			n++
			r.m.Invalidate(byRequest[ref].first.SubjectRef)
		}
	}
	if n > 0 {
		r.m.log.Info("records.erasure_journal_replayed", "count", n)
	}
	return n, nil
}

type journalState struct {
	first     journalEntry
	cancelled *journalEntry
	completed bool
}

func (r *Rights) replayOne(ctx context.Context, ref string, s *journalState) (bool, error) {
	e := s.first
	changed := false
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		changed = false
		// One instance at a time per request.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "erasure-journal:"+ref); err != nil {
			return err
		}
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM rights_request WHERE id=$1 AND type='erasure' FOR UPDATE`, ref).Scan(&status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return r.replayMissing(ctx, tx, ref, s, &changed)
		case err != nil:
			return err
		}
		// The request survived the restore; only an undo made after the restore point can
		// be missing. Restricted states move on by themselves (the worker re-runs step 2
		// when the hold has ended, and the destroyed key stays destroyed).
		if s.cancelled != nil && (status == string(rp.StatusInProgress) || status == string(rp.StatusRestricted)) {
			changed = true
			return r.applyCancel(ctx, tx, ref, e.SubjectRef, s.cancelled.At, "rights.erasure_undo_replayed")
		}
		return nil
	})
	return changed, err
}

// replayMissing rebuilds a request that the restore removed.
func (r *Rights) replayMissing(ctx context.Context, tx pgx.Tx, ref string, s *journalState, changed *bool) error {
	e := s.first
	journaled := []string{string(journalRequested)}
	status, outcome := rp.StatusInProgress, ""
	var cancelledAt, restrictedAt any = nil, e.ReceivedAt
	if s.cancelled != nil {
		status, outcome, cancelledAt = rp.StatusCancelled, "undone", s.cancelled.At
		journaled = append(journaled, string(journalCancelled))
	} else {
		// A newer live erasure for the same person is already in the database: the account
		// is restricted by it, so nothing is lost by leaving this entry as history only.
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM rights_request WHERE subject_ref=$1 AND type='erasure'
			AND status IN ('received','in_progress','restricted','finalising')`, e.SubjectRef).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			r.m.log.Warn("records.erasure_journal_superseded", "resource_ref", ref)
			return nil
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO rights_request (id, subject_ref, type, status, received_at, ack_due_at, due_at, acknowledged_at,
		cancelled_at, outcome, undo_until, restricted_at, journaled)
		VALUES ($1,$2,'erasure',$3,$4,$5,$6,$4,$7,$8,$9,$10,$11)`,
		ref, e.SubjectRef, string(status), e.ReceivedAt, e.ReceivedAt.Add(r.cfg.AckWithin), e.ReceivedAt.Add(r.deadline(rp.RightsErasure)),
		cancelledAt, outcome, e.UndoUntil, restrictedAt, journaled); err != nil {
		return err
	}
	*changed = true
	if err := ap.Record(ctx, tx, ap.Event{ActorRef: e.SubjectRef, ActorRole: "system", Action: "rights.erasure_replayed", SubjectRef: e.SubjectRef,
		ResourceType: "rights_request", ResourceRef: ref, Purpose: "erasure", Result: ap.Allowed}); err != nil {
		return err
	}
	if s.cancelled != nil {
		return nil // history only: the account was released by the undo
	}
	// Step 1 again: hide the account and tell every module (their consumers are
	// idempotent). The request keeps its original undo window, so its RET-18 hold is the
	// same; if the hold has already ended, the worker starts step 2 at once.
	_, err := r.startRestrict(ctx, tx, e.SubjectRef, ref, e.ReceivedAt)
	return err
}

// applyCancel releases an erasure (the patient's undo, or its replay after a restore).
func (r *Rights) applyCancel(ctx context.Context, tx pgx.Tx, ref, subject string, at time.Time, action string) error {
	if _, err := tx.Exec(ctx, `UPDATE rights_request SET status='cancelled', cancelled_at=$2, outcome='undone' WHERE id=$1`, ref, at); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_restriction SET released_at=$2 WHERE subject_ref=$1 AND released_at IS NULL`, subject, at); err != nil {
		return err
	}
	if err := ap.Record(ctx, tx, ap.Event{ActorRef: subject, ActorRole: "system", Action: action, SubjectRef: subject,
		ResourceType: "rights_request", ResourceRef: ref, Purpose: "erasure", Result: ap.Allowed}); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, outbox.Event{Type: "rights.erasure_cancelled", SubjectRef: subject, Payload: map[string]string{"requestRef": ref, "subjectRef": subject}}); err != nil {
		return err
	}
	return notify(ctx, tx, subject)
}
