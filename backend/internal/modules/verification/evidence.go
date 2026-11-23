package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/imagesafe"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// Content types accepted for evidence.
const (
	typeJPEG = "image/jpeg"
	typePNG  = "image/png"
	typePDF  = "application/pdf"
)

// detectType decides the type from the first bytes, never from the declared header
// alone: a renamed file or a wrong header is refused.
func detectType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return typeJPEG
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return typePNG
	case bytes.HasPrefix(b, []byte("%PDF-")):
		return typePDF
	}
	return ""
}

// EvidenceInput is one uploaded file.
type EvidenceInput struct {
	Kind        string
	ContentType string // declared; must match the bytes
	Data        []byte
}

type evidenceRow struct {
	ID, CaseID, Subject, Kind, ContentType, BlobKey, KeyOwner, State string
	Round                                                            int
}

func evidenceKeyOwner(subject, evidenceID string) string { return subject + "|" + evidenceID }

func evidenceAAD(id string) crypto.AAD {
	return crypto.AAD{Table: "verification.evidence", Column: "blob", RowID: id}
}

// UploadEvidence stores one file for the doctor's current draft (S-110). The bytes are
// checked (size, declared type, magic bytes), encrypted under a key that belongs to this
// one item (crypto.SealVerification), and only then written to object storage.
func (m *Module) UploadEvidence(ctx context.Context, doctorRef string, in EvidenceInput) (*EvidenceMeta, error) {
	if err := validRef(doctorRef); err != nil {
		return nil, err
	}
	return m.uploadEvidence(ctx, KindDoctor, doctorRef, actor{Ref: doctorRef, Role: "doctor"}, in)
}

// uploadEvidence is the shared pipeline for a doctor's or a clinic's file. The caller has
// checked scope. subject is the doctor or clinic reference; a is who uploads and must hold
// D-VERIFY.
func (m *Module) uploadEvidence(ctx context.Context, kind, subject string, a actor, in EvidenceInput) (*EvidenceMeta, error) {
	allowed := m.cfg.EvidenceKinds
	if kind == KindClinic {
		allowed = m.cfg.ClinicEvidenceKinds
	}
	if !slices.Contains(allowed, in.Kind) {
		return nil, invalid("kind", "not_allowed")
	}
	if len(in.Data) == 0 {
		return nil, invalid("content", "empty")
	}
	if len(in.Data) > m.cfg.MaxEvidenceBytes {
		return nil, ErrEvidenceTooLarge
	}
	got := detectType(in.Data)
	if got == "" || (in.ContentType != "" && in.ContentType != got) {
		return nil, ErrEvidenceType
	}
	if err := m.requireConsent(ctx, a.Ref); err != nil {
		return nil, err
	}
	// Early state check so a refused upload never creates a key or a blob.
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2`, kind, subject))
	if err != nil {
		return nil, err
	}
	if c.RoundState != roundDraft && c.RoundState != roundNeedsInfo {
		return nil, ErrState
	}
	// Review M7: scan before anything is encrypted or stored. A scanner error rejects the
	// file (fail closed); the log carries no file name, content or subject.
	sctx, cancel := context.WithTimeout(ctx, m.cfg.ScanTimeout)
	v, err := m.d.Scanner.Scan(sctx, in.Data)
	cancel()
	if err != nil {
		m.log.Warn("verification.scan_failed", logging.Err(err))
		return nil, ErrEvidenceScanUnavailable
	}
	if !v.Clean {
		m.log.Warn("verification.upload_infected", "reason", "malware")
		return nil, ErrEvidenceMalware
	}
	// TM-42: an ID or certificate photo is stored re-encoded, pixels only (no location or other metadata).
	if got == typeJPEG || got == typePNG {
		clean, err := imagesafe.Reencode(in.Data, got)
		if err != nil {
			return nil, ErrEvidenceType
		}
		in.Data = clean
	}

	id := ids.New()
	owner := evidenceKeyOwner(subject, id)
	blobKey := fmt.Sprintf("verification/%s/%s", c.ID, id)
	sealed, err := crypto.SealVerification(ctx, m.d.Crypto, owner, evidenceAAD(id), in.Data)
	if err != nil {
		return nil, fmt.Errorf("verification: seal evidence: %w", err)
	}
	if err := m.d.Blob.PutWriteOnce(ctx, blobKey, sealed.Bytes()); err != nil {
		return nil, fmt.Errorf("verification: store evidence: %w", err)
	}
	meta := &EvidenceMeta{ID: id, Kind: in.Kind, ContentType: got, SizeBytes: len(in.Data), State: "stored", CreatedAt: m.now().UTC()}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseBySubject(ctx, tx, kind, subject)
		if err != nil {
			return err
		}
		if c.RoundState != roundDraft && c.RoundState != roundNeedsInfo {
			return ErrState
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM evidence WHERE case_id = $1 AND round = $2 AND state = 'stored'`, c.ID, c.Round).Scan(&n); err != nil {
			return err
		}
		if n >= m.cfg.MaxEvidencePerCase {
			return ErrEvidenceLimit
		}
		if _, err := tx.Exec(ctx, `INSERT INTO evidence (id, case_id, subject_ref, round, kind, content_type, size_bytes, blob_key, key_owner, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, c.ID, subject, c.Round, in.Kind, got, len(in.Data), blobKey, owner, meta.CreatedAt); err != nil {
			return err
		}
		return m.auditEvent(ctx, tx, a, "verification.evidence_added", c, "")
	})
	if err != nil {
		// The file is encrypted and nothing refers to it; remove it and its key.
		m.discardBlob(ctx, blobKey, subject, id)
		return nil, err
	}
	return meta, nil
}

// discardBlob destroys an evidence item's key and removes its object when the store can.
func (m *Module) discardBlob(ctx context.Context, blobKey, subject, evidenceID string) {
	ctx = context.WithoutCancel(ctx)
	if err := m.d.Crypto.DestroyVerificationKey(ctx, evidenceKeyOwner(subject, evidenceID), "evidence_discarded"); err != nil {
		m.log.Error("verification.evidence_shred_failed", logging.Err(err))
	}
	if d, ok := m.d.Blob.(Deleter); ok {
		if err := d.Delete(ctx, blobKey); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			m.log.Error("verification.evidence_delete_failed", logging.Err(err))
		}
	}
}

func scanEvidence(row pgx.Row) (*evidenceRow, error) {
	var e evidenceRow
	err := row.Scan(&e.ID, &e.CaseID, &e.Subject, &e.Round, &e.Kind, &e.ContentType, &e.BlobKey, &e.KeyOwner, &e.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

const evidenceCols = `id, case_id, subject_ref, round, kind, content_type, blob_key, key_owner, state`

// destroyEvidence makes one evidence item unrecoverable: first the key (so the stored
// bytes can never be decrypted again, whatever the store does), then the object. It is
// the hard delete for prohibited identity documents, withdrawn uploads and the 90-day
// purge (RET-08), and is safe to repeat after a partial failure.
func (m *Module) destroyEvidence(ctx context.Context, e *evidenceRow, reason string) error {
	if err := m.d.Crypto.DestroyVerificationKey(ctx, e.KeyOwner, reason); err != nil {
		return fmt.Errorf("verification: destroy evidence key: %w", err)
	}
	if d, ok := m.d.Blob.(Deleter); ok {
		if err := d.Delete(ctx, e.BlobKey); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("verification: delete evidence object: %w", err)
		}
	}
	return nil
}

// markEvidence records the deletion in the row, inside the caller's transaction.
func markEvidence(ctx context.Context, tx pgx.Tx, id, state, reason string, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE evidence SET state = $2, delete_reason = $3, deleted_at = $4, purge_after = NULL WHERE id = $1`,
		id, state, reason, now.UTC())
	return err
}

// DeleteEvidence lets a doctor withdraw an upload while the case is still editable.
// Someone else's evidence answers not-found.
func (m *Module) DeleteEvidence(ctx context.Context, doctorRef, evidenceID string) error {
	if validRef(doctorRef) != nil || validRef(evidenceID) != nil {
		return ErrNotFound
	}
	return m.deleteEvidence(ctx, KindDoctor, doctorRef, actor{Ref: doctorRef, Role: "doctor"}, evidenceID)
}

func (m *Module) deleteEvidence(ctx context.Context, kind, subject string, a actor, evidenceID string) error {
	if validRef(evidenceID) != nil {
		return ErrNotFound
	}
	e, err := scanEvidence(m.pool.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = $1 AND subject_ref = $2`, evidenceID, subject))
	if err != nil {
		return err
	}
	if e.State != "stored" {
		return ErrNotFound
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1 AND subject_kind = $2`, e.CaseID, kind))
	if err != nil {
		return err
	}
	if c.Round != e.Round || (c.RoundState != roundDraft && c.RoundState != roundNeedsInfo) {
		return ErrState
	}
	if err := m.destroyEvidence(ctx, e, "withdrawn_by_doctor"); err != nil {
		return err
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := markEvidence(ctx, tx, e.ID, "deleted", "withdrawn_by_doctor", m.now()); err != nil {
			return err
		}
		return m.auditEvent(ctx, tx, a, "verification.evidence_deleted", c, "withdrawn_by_doctor")
	})
}

// ReadEvidence decrypts one evidence file for a reviewer. Every read is audited before
// the bytes are returned.
func (m *Module) ReadEvidence(ctx context.Context, reviewerRef, caseID, evidenceID string) ([]byte, string, error) {
	if validRef(reviewerRef) != nil || validRef(caseID) != nil || validRef(evidenceID) != nil {
		return nil, "", ErrNotFound
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1`, caseID))
	if err != nil {
		return nil, "", err
	}
	if c.Subject == reviewerRef {
		return nil, "", ErrSelfReview
	}
	e, err := scanEvidence(m.pool.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = $1 AND case_id = $2`, evidenceID, caseID))
	if err != nil {
		return nil, "", err
	}
	if e.State != "stored" {
		return nil, "", ErrEvidenceGone
	}
	if err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return m.auditEvent(ctx, tx, actor{Ref: reviewerRef, Role: "reviewer"}, "verification.evidence_read", c, "")
	}); err != nil {
		return nil, "", err
	}
	raw, err := m.d.Blob.Get(ctx, e.BlobKey)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, "", ErrEvidenceGone
	}
	if err != nil {
		return nil, "", fmt.Errorf("verification: read evidence: %w", err)
	}
	var sealed crypto.Sealed[crypto.VerificationKey]
	if err := sealed.Scan(raw); err != nil {
		return nil, "", err
	}
	plain, err := crypto.Open(ctx, m.d.Crypto, evidenceAAD(e.ID), sealed)
	if errors.Is(err, crypto.ErrKeyDestroyed) {
		return nil, "", ErrEvidenceGone
	}
	if err != nil {
		return nil, "", fmt.Errorf("verification: open evidence: %w", err)
	}
	return plain, e.ContentType, nil
}

// PurgeEvidence is a job method (RET-08): it destroys the encryption key of every
// evidence item whose retention has ended, 90 days after the final decision, and of
// items left in an abandoned draft for as long. The object is deleted when the store can,
// and is unreadable regardless. It returns how many items were purged and keeps going
// after a failure on one item, returning the first error at the end.
func (m *Module) PurgeEvidence(ctx context.Context, now time.Time) (int, error) {
	rs, err := m.pool.Query(ctx, `SELECT `+evidenceCols+` FROM evidence e WHERE state = 'stored' AND (
			purge_after <= $1
			OR (purge_after IS NULL AND created_at <= $2 AND EXISTS (
				SELECT 1 FROM verification_case c WHERE c.id = e.case_id AND c.round = e.round AND c.round_state IN ('draft','needs_info'))))
		ORDER BY created_at LIMIT 500`, now.UTC(), now.UTC().Add(-m.cfg.EvidenceRetention))
	if err != nil {
		return 0, err
	}
	var rows []*evidenceRow
	for rs.Next() {
		e, err := scanEvidence(rs)
		if err != nil {
			rs.Close()
			return 0, err
		}
		rows = append(rows, e)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return 0, err
	}
	var first error
	n := 0
	for _, e := range rows {
		if err := m.destroyEvidence(ctx, e, "retention_ended"); err != nil {
			if first == nil {
				first = err
			}
			m.log.Error("verification.purge_failed", logging.Err(err))
			continue
		}
		c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1`, e.CaseID))
		if err == nil {
			err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
				if err := markEvidence(ctx, tx, e.ID, "purged", "retention_ended", now); err != nil {
					return err
				}
				return m.auditEvent(ctx, tx, system, "verification.evidence_purged", c, "retention_ended")
			})
		}
		if err != nil && first == nil {
			first = err
		}
		n++
	}
	return n, first
}
