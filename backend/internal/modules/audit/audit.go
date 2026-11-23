// Package audit is the audit module (S-096): it moves events from the durable outbox
// into the append-only, hash-chained store, anchors each day's final hash in write-once
// storage, and verifies the chain (audit-logging-design §4, E-SEC-12, TC-65).
//
// Other modules never import this package; they call audit/public.Record.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "audit", Schema: "audit", FS: sub}
}

// BacklogAlert is the outbox age that raises an alert (audit-logging-design §7).
const BacklogAlert = 5 * time.Minute

const chainLock = 7_311_002_027 // advisory lock: one chain writer at a time

// Module is the audit worker.
type Module struct {
	pool *pgxpool.Pool
	blob blobstore.Store
	log  *slog.Logger
	now  func() time.Time
	// Alert is called on a chain break or a stale backlog. Wired to paging in main.
	Alert func(ctx context.Context, code string)
}

// New builds the module. pool must use the audit module's role.
func New(pool *pgxpool.Pool, blob blobstore.Store, log *slog.Logger) *Module {
	return &Module{pool: pool, blob: blob, log: log, now: time.Now, Alert: func(context.Context, string) {}}
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

type row struct {
	day                                           time.Time
	seq                                           int64
	eventID                                       string
	occurredAt, chainedAt                         time.Time
	actorRef, actorRole, action, subjectRef       string
	resourceType, resourceRef, purpose, grantID   string
	reasonCode, ticketRef, result, client, ipHash string
	requestID                                     string
	prevHash, hash                                []byte
}

// canonical encodes the event fields in a fixed order with length prefixes, so no two
// different events can produce the same byte string.
func canonical(r *row) []byte {
	var b bytes.Buffer
	put := func(s string) {
		var n [4]byte
		if len(s) > math.MaxUint32 {
			panic("audit: field too long")
		}
		binary.BigEndian.PutUint32(n[:], uint32(len(s))) // #nosec G115 -- range checked above
		b.Write(n[:])
		b.WriteString(s)
	}
	put("v1")
	put(r.day.Format("2006-01-02"))
	put(fmt.Sprint(r.seq))
	put(r.eventID)
	put(r.occurredAt.UTC().Format(time.RFC3339Nano))
	put(r.chainedAt.UTC().Format(time.RFC3339Nano))
	for _, s := range []string{r.actorRef, r.actorRole, r.action, r.subjectRef, r.resourceType, r.resourceRef,
		r.purpose, r.grantID, r.reasonCode, r.ticketRef, r.result, r.client, r.ipHash, r.requestID} {
		put(s)
	}
	return b.Bytes()
}

func chainHash(prev []byte, r *row) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(canonical(r))
	return h.Sum(nil)
}

var genesis = make([]byte, 32)

// ChainBatch moves up to limit events from the outbox into the chain. It returns how
// many were moved. Safe to run from several workers: an advisory lock serialises them.
func (m *Module) ChainBatch(ctx context.Context, limit int) (int, error) {
	moved := 0
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		moved = 0
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, chainLock); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT event_id, occurred_at, actor_ref, actor_role, action, subject_ref,
			resource_type, resource_ref, purpose, grant_id, reason_code, ticket_ref, result, client, ip_hash, request_id
			FROM audit_outbox ORDER BY inserted_at, event_id LIMIT $1`, limit) // serialised by the advisory lock above; FOR UPDATE would need UPDATE rights
		if err != nil {
			return err
		}
		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.eventID, &r.occurredAt, &r.actorRef, &r.actorRole, &r.action, &r.subjectRef,
				&r.resourceType, &r.resourceRef, &r.purpose, &r.grantID, &r.reasonCode, &r.ticketRef, &r.result,
				&r.client, &r.ipHash, &r.requestID); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		now := m.now().UTC().Truncate(time.Microsecond)
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		seq, prev, err := lastLink(ctx, tx, day)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(batch))
		for i := range batch {
			r := &batch[i]
			seq++
			r.day, r.seq, r.chainedAt, r.prevHash = day, seq, now, prev
			r.hash = chainHash(prev, r)
			if _, err := tx.Exec(ctx, `INSERT INTO audit_event (partition_day, seq, event_id, occurred_at, chained_at,
				actor_ref, actor_role, action, subject_ref, resource_type, resource_ref, purpose, grant_id, reason_code,
				ticket_ref, result, client, ip_hash, request_id, prev_hash, hash)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
				r.day, r.seq, r.eventID, r.occurredAt, r.chainedAt, r.actorRef, r.actorRole, r.action, r.subjectRef,
				r.resourceType, r.resourceRef, r.purpose, r.grantID, r.reasonCode, r.ticketRef, r.result, r.client,
				r.ipHash, r.requestID, r.prevHash, r.hash); err != nil {
				return err
			}
			prev = r.hash
			ids = append(ids, r.eventID)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM audit_outbox WHERE event_id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
		moved = len(batch)
		return nil
	})
	return moved, err
}

// lastLink returns the last seq of day and the hash the next event must link to.
func lastLink(ctx context.Context, tx pgx.Tx, day time.Time) (int64, []byte, error) {
	var seq int64
	var h []byte
	err := tx.QueryRow(ctx, `SELECT seq, hash FROM audit_event WHERE partition_day=$1 ORDER BY seq DESC LIMIT 1`, day).Scan(&seq, &h)
	if err == nil {
		return seq, h, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, err
	}
	err = tx.QueryRow(ctx, `SELECT hash FROM audit_event WHERE partition_day < $1 ORDER BY partition_day DESC, seq DESC LIMIT 1`, day).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, genesis, nil
	}
	return 0, h, err
}

// Backlog returns the age of the oldest outbox row (0 when empty) and alerts when it is
// older than BacklogAlert.
func (m *Module) Backlog(ctx context.Context) (time.Duration, error) {
	var oldest *time.Time
	if err := m.pool.QueryRow(ctx, `SELECT min(inserted_at) FROM audit_outbox`).Scan(&oldest); err != nil {
		return 0, err
	}
	if oldest == nil {
		return 0, nil
	}
	age := m.now().Sub(*oldest)
	if age > BacklogAlert {
		m.log.Warn("audit.outbox_backlog", "backlog_seconds", int64(age.Seconds()))
		m.Alert(ctx, "audit.outbox_backlog")
	}
	return age, nil
}

// Run chains events until ctx ends.
func (m *Module) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for {
			n, err := m.ChainBatch(ctx, 500)
			if err != nil {
				m.log.Error("audit.chain_batch_failed", logging.Err(err))
				break
			}
			if n < 500 {
				break
			}
		}
		if _, err := m.Backlog(ctx); err != nil {
			m.log.Error("audit.backlog_check_failed", logging.Err(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type anchorDoc struct {
	Day      string `json:"day"`
	LastSeq  int64  `json:"lastSeq"`
	LastHash string `json:"lastHash"`
}

// Anchor writes the final hash of a finished day to write-once storage and records it.
// Anchoring today is refused: the day is still being appended to.
func (m *Module) Anchor(ctx context.Context, day time.Time) error {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	now := m.now().UTC()
	if !day.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)) {
		return errors.New("audit: cannot anchor a day that has not ended")
	}
	var seq int64
	var h []byte
	err := m.pool.QueryRow(ctx, `SELECT seq, hash FROM audit_event WHERE partition_day=$1 ORDER BY seq DESC LIMIT 1`, day).Scan(&seq, &h)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // nothing appended that day
	}
	if err != nil {
		return err
	}
	key := "audit-anchors/" + day.Format("2006-01-02") + ".json"
	doc, _ := json.Marshal(anchorDoc{Day: day.Format("2006-01-02"), LastSeq: seq, LastHash: hex.EncodeToString(h)})
	if err := m.blob.PutWriteOnce(ctx, key, doc); err != nil && !errors.Is(err, blobstore.ErrExists) {
		return fmt.Errorf("audit: anchor blob: %w", err)
	}
	_, err = m.pool.Exec(ctx, `INSERT INTO chain_anchor (partition_day, last_seq, last_hash, blob_key)
		VALUES ($1,$2,$3,$4) ON CONFLICT (partition_day) DO NOTHING`, day, seq, h, key)
	return err
}

// Break describes one place where the chain does not verify.
type Break struct {
	Day    string
	Seq    int64
	Reason string
}

// Verify recomputes the chain for one day and compares it with the stored hashes, the
// previous day's link, the database anchor and the write-once anchor. Any break raises
// a security alert (audit-logging-design §4 step 5).
func (m *Module) Verify(ctx context.Context, day time.Time) ([]Break, error) {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	ds := day.Format("2006-01-02")
	var breaks []Break
	addBreak := func(seq int64, reason string) { breaks = append(breaks, Break{Day: ds, Seq: seq, Reason: reason}) }

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// Expected link into this day: the previous day's final hash, or genesis.
	expectPrev := genesis
	var ph []byte
	err = tx.QueryRow(ctx, `SELECT hash FROM audit_event WHERE partition_day < $1 ORDER BY partition_day DESC, seq DESC LIMIT 1`, day).Scan(&ph)
	if err == nil {
		expectPrev = ph
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT partition_day, seq, event_id, occurred_at, chained_at, actor_ref, actor_role,
		action, subject_ref, resource_type, resource_ref, purpose, grant_id, reason_code, ticket_ref, result, client,
		ip_hash, request_id, prev_hash, hash FROM audit_event WHERE partition_day=$1 ORDER BY seq`, day)
	if err != nil {
		return nil, err
	}
	var last []byte
	var lastSeq int64
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.day, &r.seq, &r.eventID, &r.occurredAt, &r.chainedAt, &r.actorRef, &r.actorRole,
			&r.action, &r.subjectRef, &r.resourceType, &r.resourceRef, &r.purpose, &r.grantID, &r.reasonCode,
			&r.ticketRef, &r.result, &r.client, &r.ipHash, &r.requestID, &r.prevHash, &r.hash); err != nil {
			rows.Close()
			return nil, err
		}
		if r.seq != lastSeq+1 {
			addBreak(r.seq, "sequence gap")
		}
		if !bytes.Equal(r.prevHash, expectPrev) {
			addBreak(r.seq, "previous hash does not link")
		}
		if !bytes.Equal(chainHash(r.prevHash, &r), r.hash) {
			addBreak(r.seq, "event content does not match its hash")
		}
		expectPrev, last, lastSeq = r.hash, r.hash, r.seq
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var aSeq int64
	var aHash []byte
	var key string
	err = tx.QueryRow(ctx, `SELECT last_seq, last_hash, blob_key FROM chain_anchor WHERE partition_day=$1`, day).Scan(&aSeq, &aHash, &key)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// A finished day with events must have an anchor; a missing one may hide a
		// truncated tail (review L11).
		now := m.now().UTC()
		if lastSeq > 0 && day.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)) {
			addBreak(lastSeq, "anchor missing for a finished day")
		}
	case err != nil:
		return nil, err
	default:
		if aSeq != lastSeq || !bytes.Equal(aHash, last) {
			addBreak(lastSeq, "database anchor does not match the chain")
		}
		b, err := m.blob.Get(ctx, key)
		if err != nil {
			addBreak(lastSeq, "write-once anchor missing")
		} else {
			var doc anchorDoc
			if json.Unmarshal(b, &doc) != nil || doc.LastSeq != lastSeq || doc.LastHash != hex.EncodeToString(last) {
				addBreak(lastSeq, "write-once anchor does not match the chain")
			}
		}
	}
	if len(breaks) > 0 {
		m.log.Error("audit.chain_broken", "partition", ds, "count", len(breaks))
		m.Alert(ctx, "audit.chain_broken")
	}
	return breaks, nil
}
