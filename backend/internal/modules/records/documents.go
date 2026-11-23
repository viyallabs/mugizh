package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/imagesafe"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

const maxTitleRunes = 120

// File is a decrypted download.
type File struct {
	ContentType string
	Extension   string
	Data        []byte
}

type fileType struct{ mime, ext string }

// sniff identifies PDF, JPEG and PNG by their first bytes, never by the declared type or
// the file name (TC-14). Anything else returns false.
func sniff(b []byte) (fileType, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("%PDF-")):
		return fileType{"application/pdf", "pdf"}, true
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return fileType{"image/jpeg", "jpg"}, true
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return fileType{"image/png", "png"}, true
	}
	return fileType{}, false
}

func extFor(contentType string) string {
	switch contentType {
	case "application/pdf":
		return "pdf"
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	}
	return "bin"
}

func cleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxTitleRunes {
		return "", false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return "", false
		}
	}
	return s, true
}

func (r *Rights) callerIsPatient(c rp.Caller) error {
	if !reUUID.MatchString(c.UserRef) || c.Role != "patient" {
		return rp.ErrDenied
	}
	if c.Restricted {
		return rp.ErrDenied
	}
	return nil
}

// Upload stores a patient's document (S-159, TC-14): content-sniff, size limit, malware
// scan (a scanner failure rejects), encrypt with the patient key, then store the
// ciphertext. Nothing unscanned or unencrypted is ever stored.
func (r *Rights) Upload(ctx context.Context, c rp.Caller, title, declaredType string, content io.Reader) (rp.DocumentInfo, error) {
	if err := r.callerIsPatient(c); err != nil {
		return rp.DocumentInfo{}, err
	}
	if restricted, err := r.isRestricted(ctx, c.UserRef); err != nil {
		return rp.DocumentInfo{}, err
	} else if restricted {
		return rp.DocumentInfo{}, rp.ErrRestricted
	}
	title, ok := cleanTitle(title)
	if !ok {
		return rp.DocumentInfo{}, fmt.Errorf("%w: title", rp.ErrInvalid)
	}
	if scrub.ContainsProhibitedID(title, r.cfg.ProhibitedValidators) {
		return rp.DocumentInfo{}, rp.ErrProhibitedID
	}
	data, err := io.ReadAll(io.LimitReader(content, r.cfg.UploadMaxBytes+1))
	if err != nil {
		return rp.DocumentInfo{}, err
	}
	if int64(len(data)) > r.cfg.UploadMaxBytes {
		return rp.DocumentInfo{}, rp.ErrTooLarge
	}
	ft, ok := sniff(data)
	if !ok {
		return rp.DocumentInfo{}, rp.ErrFileType
	}
	declared, _, perr := mime.ParseMediaType(declaredType)
	if perr != nil || declared != ft.mime {
		return rp.DocumentInfo{}, rp.ErrFileType // declared type must match the content
	}
	sctx, cancel := context.WithTimeout(ctx, r.cfg.ScanTimeout)
	defer cancel()
	v, err := r.d.Scanner.Scan(sctx, data)
	if err != nil {
		r.m.log.Warn("records.scan_failed", logging.Err(err))
		return rp.DocumentInfo{}, rp.ErrScanUnavailable
	}
	if !v.Clean {
		r.m.log.Warn("records.upload_infected", "reason", "malware")
		return rp.DocumentInfo{}, rp.ErrMalware
	}
	// TM-42: an image is stored re-encoded, pixels only (no location or other metadata, nothing appended).
	if ft.mime == "image/jpeg" || ft.mime == "image/png" {
		clean, err := imagesafe.Reencode(data, ft.mime)
		if err != nil {
			return rp.DocumentInfo{}, rp.ErrFileType
		}
		data = clean
	}
	id := ids.New()
	file, err := crypto.SealPatient(ctx, r.d.Crypto, c.UserRef, crypto.AAD{Table: "document", Column: "file", RowID: id}, data)
	if err != nil {
		return rp.DocumentInfo{}, err
	}
	sealedTitle, err := crypto.SealPatient(ctx, r.d.Crypto, c.UserRef, crypto.AAD{Table: "document", Column: "title", RowID: id}, []byte(title))
	if err != nil {
		return rp.DocumentInfo{}, err
	}
	key := "documents/" + c.UserRef + "/" + id + ".bin"
	now := r.now()
	// Review M6: the row and the blob are written in one transaction, blob last. A failed blob
	// write rolls the row back; a failed commit removes the blob again, so no unreferenced
	// ciphertext is left behind.
	blobWritten := false
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		blobWritten = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, c.UserRef); err != nil {
			return err
		}
		if err := r.checkQuota(ctx, tx, c.UserRef, int64(len(data))); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document (id, patient_ref, title_sealed, content_type, size_bytes, scan_result, blob_key, created_at)
			VALUES ($1,$2,$3,$4,$5,'clean',$6,$7)`, id, c.UserRef, sealedTitle, ft.mime, len(data), key, now); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "records.upload", SubjectRef: c.UserRef,
			ResourceType: "document", ResourceRef: id, Purpose: "P-RECORD", Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
			return err
		}
		if err := r.d.Blobs.PutWriteOnce(ctx, key, file.Bytes()); err != nil {
			return err
		}
		blobWritten = true
		return nil
	})
	if err != nil {
		if blobWritten {
			r.deleteBlobs(context.WithoutCancel(ctx), []string{key})
		}
		return rp.DocumentInfo{}, err
	}
	return rp.DocumentInfo{ID: id, Title: title, ContentType: ft.mime, SizeBytes: len(data), CreatedAt: now}, nil
}

// checkQuota refuses an upload beyond the patient's document count or total size (review M6).
func (r *Rights) checkQuota(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, patient string, add int64) error {
	var n, total int64
	if err := q.QueryRow(ctx, `SELECT count(*), COALESCE(sum(size_bytes),0) FROM document WHERE patient_ref=$1 AND hidden_at IS NULL`, patient).Scan(&n, &total); err != nil {
		return err
	}
	if n+1 > int64(r.cfg.MaxDocuments) || total+add > r.cfg.MaxTotalBytes {
		return rp.ErrQuota
	}
	return nil
}

// ListDocuments lists the caller's own uploads (metadata only).
func (r *Rights) ListDocuments(ctx context.Context, c rp.Caller) ([]rp.DocumentInfo, error) {
	if err := r.callerIsPatient(c); err != nil {
		return nil, err
	}
	if restricted, err := r.isRestricted(ctx, c.UserRef); err != nil {
		return nil, err
	} else if restricted {
		return nil, rp.ErrNotFound
	}
	return r.documentsOf(ctx, r.m.pool, c.UserRef)
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *Rights) documentsOf(ctx context.Context, q queryer, patient string) ([]rp.DocumentInfo, error) {
	rows, err := q.Query(ctx, `SELECT id::text, title_sealed, content_type, size_bytes, created_at FROM document WHERE patient_ref=$1 AND hidden_at IS NULL ORDER BY created_at, id`, patient)
	if err != nil {
		return nil, err
	}
	type raw struct {
		d rp.DocumentInfo
		t crypto.Sealed[crypto.PatientKey]
	}
	var list []raw
	for rows.Next() {
		var x raw
		if err := rows.Scan(&x.d.ID, &x.t, &x.d.ContentType, &x.d.SizeBytes, &x.d.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]rp.DocumentInfo, 0, len(list))
	for _, x := range list {
		t, err := crypto.Open(ctx, r.d.Crypto, crypto.AAD{Table: "document", Column: "title", RowID: x.d.ID}, x.t)
		if err != nil {
			return nil, err
		}
		x.d.Title = string(t)
		out = append(out, x.d)
	}
	return out, nil
}

// ---- secure links (S-171, TC-15, E-DP-21) ----

func (r *Rights) linkHash(token string) []byte { return r.d.Crypto.Keyed("records.file_link", token) }

// authorizeDocument applies the gate: the owner reads their own file (unless the account
// is under erasure); everyone else goes through Authorize (audited). A hidden refusal is
// "not found".
func (r *Rights) authorizeDocument(ctx context.Context, c rp.Caller, patient, docID string) error {
	if c.UserRef == patient && c.Role == "patient" {
		if restricted, err := r.isRestricted(ctx, patient); err != nil {
			return err
		} else if restricted {
			return rp.ErrNotFound
		}
	}
	d, err := r.m.Authorize(ctx, rp.Request{Actor: rp.Actor{Ref: c.UserRef, Role: c.Role, ClinicRef: c.ClinicRef, Restricted: c.Restricted},
		PatientRef: patient, Kind: rp.KindDocuments, ResourceType: "document", ResourceRef: docID, RequestID: c.RequestID, Client: c.Client, IPHash: c.IPHash})
	if err != nil {
		return err
	}
	if !d.Allowed {
		if d.Hide {
			return rp.ErrNotFound
		}
		return rp.ErrDenied
	}
	return nil
}

func (r *Rights) newLink(ctx context.Context, c rp.Caller, kind, patient, target string) (rp.FileLink, error) {
	token, err := ids.Token(32)
	if err != nil {
		return rp.FileLink{}, err
	}
	now := r.now()
	exp := now.Add(r.cfg.LinkTTL)
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO file_link (token_hash, user_ref, patient_ref, kind, target_ref, expires_at, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			r.linkHash(token), c.UserRef, patient, kind, target, exp, now); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: safeRole(c.Role), Action: "records.link_issued", SubjectRef: patient,
			ResourceType: kind, ResourceRef: target, Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID})
	})
	if err != nil {
		return rp.FileLink{}, err
	}
	return rp.FileLink{Token: token, ExpiresAt: exp}, nil
}

// IssueDocumentLink issues a single-use link to one document, bound to the caller.
func (r *Rights) IssueDocumentLink(ctx context.Context, c rp.Caller, docID string) (rp.FileLink, error) {
	if !allUUID(c.UserRef, docID) {
		return rp.FileLink{}, rp.ErrNotFound
	}
	var patient string
	err := r.m.pool.QueryRow(ctx, `SELECT patient_ref::text FROM document WHERE id=$1 AND hidden_at IS NULL`, docID).Scan(&patient)
	if errors.Is(err, pgx.ErrNoRows) {
		return rp.FileLink{}, rp.ErrNotFound
	}
	if err != nil {
		return rp.FileLink{}, err
	}
	if err := r.authorizeDocument(ctx, c, patient, docID); err != nil {
		if errors.Is(err, rp.ErrDenied) && c.UserRef != patient {
			return rp.FileLink{}, rp.ErrNotFound // review M6: another patient's document is never confirmed to exist
		}
		return rp.FileLink{}, err
	}
	return r.newLink(ctx, c, "document", patient, docID)
}

// IssueExportLink issues a link to a finished export of the caller's own data.
func (r *Rights) IssueExportLink(ctx context.Context, c rp.Caller, requestID string) (rp.FileLink, error) {
	if err := r.callerIsPatient(c); err != nil {
		return rp.FileLink{}, err
	}
	if !reUUID.MatchString(requestID) {
		return rp.FileLink{}, rp.ErrNotFound
	}
	var exp *time.Time
	var key *string
	err := r.m.pool.QueryRow(ctx, `SELECT export_expires_at, export_blob_key FROM rights_request WHERE id=$1 AND subject_ref=$2 AND type IN ('export','access')`,
		requestID, c.UserRef).Scan(&exp, &key)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (key == nil || exp == nil)) {
		return rp.FileLink{}, rp.ErrNotFound
	}
	if err != nil {
		return rp.FileLink{}, err
	}
	if !r.now().Before(*exp) {
		return rp.FileLink{}, rp.ErrLinkExpired
	}
	if restricted, err := r.isRestricted(ctx, c.UserRef); err != nil {
		return rp.FileLink{}, err
	} else if restricted {
		return rp.FileLink{}, rp.ErrNotFound
	}
	return r.newLink(ctx, c, "export", c.UserRef, requestID)
}

// Download redeems a link: another user, an expired or a used link is refused. Access is
// re-checked now (grants may have ended since the link was issued), the file is
// decrypted and returned. The link is consumed atomically before any bytes are released.
func (r *Rights) Download(ctx context.Context, c rp.Caller, token string) (File, error) {
	if !reUUID.MatchString(c.UserRef) || len(token) < 32 || len(token) > 128 {
		return File{}, rp.ErrLinkInvalid
	}
	hash := r.linkHash(token)
	var user, patient, kind, target string
	var exp time.Time
	var used *time.Time
	err := r.m.pool.QueryRow(ctx, `SELECT user_ref::text, patient_ref::text, kind, target_ref::text, expires_at, used_at FROM file_link WHERE token_hash=$1`, hash).
		Scan(&user, &patient, &kind, &target, &exp, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, rp.ErrLinkInvalid
	}
	if err != nil {
		return File{}, err
	}
	if user != c.UserRef {
		return File{}, rp.ErrLinkInvalid // bound to another user: same answer as an unknown link
	}
	now := r.now()
	if used != nil || !now.Before(exp) {
		return File{}, rp.ErrLinkExpired
	}
	if c.Restricted {
		return File{}, rp.ErrDenied
	}
	var blobKey, contentType string
	var aad crypto.AAD
	switch kind {
	case "document":
		if err := r.authorizeDocument(ctx, c, patient, target); err != nil {
			return File{}, err
		}
		if err := r.m.pool.QueryRow(ctx, `SELECT blob_key, content_type FROM document WHERE id=$1 AND patient_ref=$2 AND hidden_at IS NULL`, target, patient).Scan(&blobKey, &contentType); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return File{}, rp.ErrNotFound
			}
			return File{}, err
		}
		aad = crypto.AAD{Table: "document", Column: "file", RowID: target}
	case "export":
		if c.UserRef != patient || c.Role != "patient" {
			return File{}, rp.ErrLinkInvalid
		}
		if restricted, err := r.isRestricted(ctx, patient); err != nil {
			return File{}, err
		} else if restricted {
			return File{}, rp.ErrNotFound
		}
		var xexp *time.Time
		var key *string
		if err := r.m.pool.QueryRow(ctx, `SELECT export_blob_key, export_expires_at FROM rights_request WHERE id=$1 AND subject_ref=$2`, target, patient).Scan(&key, &xexp); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return File{}, rp.ErrNotFound
			}
			return File{}, err
		}
		if key == nil || xexp == nil || !now.Before(*xexp) {
			return File{}, rp.ErrLinkExpired
		}
		blobKey, contentType = *key, "application/json"
		aad = crypto.AAD{Table: "rights_request", Column: "export", RowID: target}
	default:
		return File{}, rp.ErrLinkInvalid
	}
	// Single use: the first redemption wins; a second one (or a race) is refused.
	tag, err := r.m.pool.Exec(ctx, `UPDATE file_link SET used_at=$2 WHERE token_hash=$1 AND used_at IS NULL AND expires_at > $2`, hash, now)
	if err != nil {
		return File{}, err
	}
	if tag.RowsAffected() == 0 {
		return File{}, rp.ErrLinkExpired
	}
	blob, err := r.d.Blobs.Get(ctx, blobKey)
	if errors.Is(err, blobstore.ErrNotFound) {
		return File{}, rp.ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	var sealed crypto.Sealed[crypto.PatientKey]
	if err := sealed.Scan(blob); err != nil {
		return File{}, err
	}
	plain, err := crypto.Open(ctx, r.d.Crypto, aad, sealed)
	if errors.Is(err, crypto.ErrKeyDestroyed) {
		return File{}, rp.ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	return File{ContentType: contentType, Extension: extFor(contentType), Data: plain}, nil
}

// ---- patient access log (S-170) ----

// AccessLog is "who viewed my record and when": allowed reads by people, newest first.
func (r *Rights) AccessLog(ctx context.Context, c rp.Caller, cursor string, limit int) ([]ap.AccessEntry, string, error) {
	if err := r.callerIsPatient(c); err != nil {
		return nil, "", err
	}
	if r.d.AccessLog == nil {
		return nil, "", errors.New("records: access log reader not configured")
	}
	return r.d.AccessLog.AccessLogFor(ctx, c.UserRef, cursor, limit)
}

// ---- export (S-143) ----

type exportDoc struct {
	GeneratedAt    time.Time        `json:"generatedAt"`
	Kind           string           `json:"kind"`
	Consents       []exportConsent  `json:"consents"`
	ConsentHistory []exportHistory  `json:"consentHistory,omitempty"`
	Grants         []exportGrant    `json:"grants"`
	Documents      []exportDocument `json:"documents,omitempty"`
	AccessLog      []exportAccess   `json:"accessLog,omitempty"`
	Requests       []exportRequest  `json:"rightsRequests,omitempty"`
}

type exportConsent struct {
	PurposeID   string    `json:"purposeId"`
	Granted     bool      `json:"granted"`
	TextVersion int       `json:"textVersion"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type exportHistory struct {
	PurposeID     string    `json:"purposeId"`
	Action        string    `json:"action"`
	TextVersion   int       `json:"textVersion"`
	NoticeVersion string    `json:"noticeVersion"`
	Channel       string    `json:"channel"`
	At            time.Time `json:"at"`
}

type exportGrant struct {
	Tier      string     `json:"tier"`
	GranteeID string     `json:"granteeId"`
	StartsAt  time.Time  `json:"startsAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type exportDocument struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	ContentType string    `json:"contentType"`
	SizeBytes   int       `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

type exportAccess struct {
	At        time.Time `json:"at"`
	ActorRole string    `json:"actorRole"`
	Kind      string    `json:"kind"`
	Purpose   string    `json:"purpose"`
}

type exportRequest struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// ProcessExports builds pending access and export files (worker). Long jobs: the API
// answers 202 and the patient polls the request (API standards §9).
func (r *Rights) ProcessExports(ctx context.Context, limit int) error {
	return r.processExports(ctx, limit)
}

func (r *Rights) processExports(ctx context.Context, limit int) error {
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, subject_ref::text, type FROM rights_request WHERE type IN ('access','export') AND status='received' ORDER BY received_at LIMIT $1`, limit)
	if err != nil {
		return err
	}
	type job struct{ id, subject, typ string }
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.subject, &j.typ); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	var firstErr error
	for _, j := range jobs {
		if err := r.buildExport(ctx, j.id, j.subject, j.typ); err != nil {
			r.m.log.Warn("records.export_failed", "resource_ref", j.id, logging.Err(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *Rights) buildExport(ctx context.Context, id, subject, typ string) error {
	now := r.now()
	if restricted, err := r.isRestricted(ctx, subject); err != nil {
		return err
	} else if restricted {
		_, err := r.m.pool.Exec(ctx, `UPDATE rights_request SET status='rejected', outcome='account_restricted', completed_at=$2 WHERE id=$1 AND status='received'`, id, now)
		return err
	}
	doc := exportDoc{GeneratedAt: now, Kind: typ, Consents: []exportConsent{}, Grants: []exportGrant{}}
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		doc.Consents, doc.ConsentHistory, doc.Grants, doc.Documents, doc.Requests = []exportConsent{}, nil, []exportGrant{}, nil, nil
		rows, err := tx.Query(ctx, `SELECT purpose_id, granted, text_version, updated_at FROM consent_state WHERE subject_ref=$1 ORDER BY purpose_id`, subject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x exportConsent
			if err := rows.Scan(&x.PurposeID, &x.Granted, &x.TextVersion, &x.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			doc.Consents = append(doc.Consents, x)
		}
		rows.Close()
		if typ != string(rp.RightsExport) {
			return nil
		}
		rows, err = tx.Query(ctx, `SELECT purpose_id, action, text_version, notice_version, channel, occurred_at FROM consent_record WHERE subject_ref=$1 ORDER BY occurred_at, id`, subject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x exportHistory
			if err := rows.Scan(&x.PurposeID, &x.Action, &x.TextVersion, &x.NoticeVersion, &x.Channel, &x.At); err != nil {
				rows.Close()
				return err
			}
			doc.ConsentHistory = append(doc.ConsentHistory, x)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT tier, grantee_ref::text, starts_at, expires_at, revoked_at FROM permission_grant
			WHERE patient_ref=$1 AND tier IN ('T1_current_care','T2_own_records','T3_history_share','T4_extension') ORDER BY created_at, id`, subject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x exportGrant
			if err := rows.Scan(&x.Tier, &x.GranteeID, &x.StartsAt, &x.ExpiresAt, &x.RevokedAt); err != nil {
				rows.Close()
				return err
			}
			doc.Grants = append(doc.Grants, x)
		}
		rows.Close()
		docs, err := r.documentsOf(ctx, tx, subject)
		if err != nil {
			return err
		}
		for _, d := range docs {
			doc.Documents = append(doc.Documents, exportDocument{ID: d.ID, Title: d.Title, ContentType: d.ContentType, SizeBytes: d.SizeBytes, CreatedAt: d.CreatedAt})
		}
		rows, err = tx.Query(ctx, `SELECT id::text, type, status, received_at FROM rights_request WHERE subject_ref=$1 ORDER BY received_at, id`, subject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x exportRequest
			if err := rows.Scan(&x.ID, &x.Type, &x.Status, &x.ReceivedAt); err != nil {
				rows.Close()
				return err
			}
			doc.Requests = append(doc.Requests, x)
		}
		rows.Close()
		return nil
	})
	if err != nil {
		return err
	}
	if typ == string(rp.RightsExport) && r.d.AccessLog != nil {
		cursor := ""
		for i := 0; i < 20; i++ {
			es, next, err := r.d.AccessLog.AccessLogFor(ctx, subject, cursor, 100)
			if err != nil {
				return err
			}
			for _, e := range es {
				doc.AccessLog = append(doc.AccessLog, exportAccess{At: e.OccurredAt, ActorRole: e.ActorRole, Kind: e.Kind, Purpose: e.Purpose})
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	sealed, err := crypto.SealPatient(ctx, r.d.Crypto, subject, crypto.AAD{Table: "rights_request", Column: "export", RowID: id}, plain)
	if err != nil {
		return err
	}
	key := "exports/" + subject + "/" + id + ".bin"
	if err := r.d.Blobs.PutWriteOnce(ctx, key, sealed.Bytes()); err != nil && !errors.Is(err, blobstore.ErrExists) {
		return err
	}
	exp := now.Add(r.cfg.ExportAvailability)
	return db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE rights_request SET status='completed', outcome='ready', completed_at=$2, export_blob_key=$3, export_ready_at=$2, export_expires_at=$4
			WHERE id=$1 AND status='received'`, id, now, key, exp)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: subject, ActorRole: "system", Action: "rights.export_ready", SubjectRef: subject,
			ResourceType: "rights_request", ResourceRef: id, Purpose: typ, Result: ap.Allowed})
	})
}

// MaxUploadBytes is the configured upload size limit (for the HTTP layer's read cap).
func (r *Rights) MaxUploadBytes() int64 { return r.cfg.UploadMaxBytes }
