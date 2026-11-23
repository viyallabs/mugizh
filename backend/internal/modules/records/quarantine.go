package records

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Hiding and hard-deleting patient uploads.
//
// AAD-05 (TM-43): a document reported to show a prohibited identifier (an Aadhaar number in the India pack; the pack
// names it) is quarantined at once on the report, without anyone opening the file again: hidden from the patient's
// list, doctors, links already issued, exports and the upload quota. The patient is told and can upload a masked
// copy. The audit trail holds the file reference, the reporter's role and the times, never the number.
//
// RET-03: the patient may delete their own upload at any time; it is hidden the same way.
//
// Only the purge job touches a hidden document afterwards: it hard-deletes the blob, the row and its links.

// EventDocumentQuarantined tells the patient a document was hidden for a prohibited identifier (payload: documentRef).
const EventDocumentQuarantined = "document.quarantined"

// Reasons a document is hidden.
const (
	hiddenProhibitedID  = "prohibited_identifier"
	hiddenPatientDelete = "patient_deleted"
)

// ReportProhibitedID quarantines a document. The patient reports their own; a doctor reports a document they may read
// now (an active share, through the records gate, audited). The patient reporting a document already hidden gets the
// same answer; anything else is not found.
func (r *Rights) ReportProhibitedID(ctx context.Context, c rp.Caller, docID string) error {
	patientRef, hidden, err := r.documentOwner(ctx, docID)
	if err != nil {
		return err
	}
	role := ""
	switch c.Role {
	case "patient":
		if err := r.callerIsPatient(c); err != nil {
			return err
		}
		if c.UserRef != patientRef {
			return rp.ErrNotFound
		}
		role = "patient"
	case "doctor":
		role = "doctor"
	default:
		return rp.ErrDenied
	}
	if hidden {
		if role == "patient" {
			return nil
		}
		return rp.ErrNotFound // a doctor learns nothing about a document they can no longer see
	}
	if role == "doctor" {
		if err := r.authorizeDocument(ctx, c, patientRef, docID); err != nil {
			return err
		}
	}
	return r.hide(ctx, c, role, patientRef, docID, hiddenProhibitedID, "records.document_quarantined")
}

// DeleteDocument hides one of the caller's own uploads for hard deletion by the purge job (RET-03). Deleting again
// succeeds; someone else's document is not found.
func (r *Rights) DeleteDocument(ctx context.Context, c rp.Caller, docID string) error {
	if err := r.callerIsPatient(c); err != nil {
		return err
	}
	patientRef, hidden, err := r.documentOwner(ctx, docID)
	if err != nil {
		return err
	}
	if patientRef != c.UserRef {
		return rp.ErrNotFound
	}
	if restricted, err := r.isRestricted(ctx, c.UserRef); err != nil {
		return err
	} else if restricted {
		return rp.ErrNotFound // under erasure, everything goes anyway
	}
	if hidden {
		return nil
	}
	return r.hide(ctx, c, "patient", patientRef, docID, hiddenPatientDelete, "records.document_delete")
}

func (r *Rights) documentOwner(ctx context.Context, docID string) (patientRef string, hidden bool, err error) {
	if !reUUID.MatchString(docID) {
		return "", false, rp.ErrNotFound
	}
	var hiddenAt *time.Time
	err = r.m.pool.QueryRow(ctx, `SELECT patient_ref::text, hidden_at FROM document WHERE id=$1`, docID).Scan(&patientRef, &hiddenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, rp.ErrNotFound
	}
	return patientRef, hiddenAt != nil, err
}

func (r *Rights) hide(ctx context.Context, c rp.Caller, role, patientRef, docID, reason, action string) error {
	now := r.now()
	return db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE document SET hidden_at=$3, hidden_reason=$4, hidden_by_role=$5 WHERE id=$1 AND patient_ref=$2 AND hidden_at IS NULL`,
			docID, patientRef, now, reason, role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // two requests at once: the first one did it
		}
		// Links already issued stop working at once.
		if _, err := tx.Exec(ctx, `UPDATE file_link SET used_at=$2 WHERE target_ref=$1 AND kind='document' AND used_at IS NULL`, docID, now); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: role, Action: action, SubjectRef: patientRef,
			ResourceType: "document", ResourceRef: docID, ReasonCode: reason, Result: ap.Allowed,
			Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
			return err
		}
		if reason != hiddenProhibitedID {
			return nil
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: EventDocumentQuarantined, SubjectRef: patientRef, Payload: map[string]string{"documentRef": docID}})
	})
}

// PurgeHidden hard-deletes hidden documents: the blob first, then the row and its links, with an audit event. A blob
// that cannot be deleted keeps its row (still hidden) and is tried again next time. Returns how many were deleted.
func (r *Rights) PurgeHidden(ctx context.Context, limit int) (int, error) {
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, patient_ref::text, blob_key, hidden_reason FROM document WHERE hidden_at IS NOT NULL ORDER BY hidden_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type doc struct{ id, patient, key, reason string }
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.id, &d.patient, &d.key, &d.reason); err != nil {
			rows.Close()
			return 0, err
		}
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	del, ok := r.d.Blobs.(interface {
		Delete(ctx context.Context, key string) error
	})
	n := 0
	for _, d := range docs {
		if !ok {
			r.m.log.Error("records.document_delete_unsupported", "resource_ref", d.id) // the store cannot delete: an operator must
			continue
		}
		if err := del.Delete(ctx, d.key); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			r.m.log.Warn("records.document_blob_delete_failed", logging.Err(err), "resource_ref", d.id)
			continue
		}
		err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM file_link WHERE target_ref=$1 AND kind='document'`, d.id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM document WHERE id=$1 AND hidden_at IS NOT NULL`, d.id)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return ap.Record(ctx, tx, ap.Event{ActorRef: d.patient, ActorRole: "system", Action: "records.document_deleted", SubjectRef: d.patient,
				ResourceType: "document", ResourceRef: d.id, ReasonCode: d.reason, Result: ap.Allowed})
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
