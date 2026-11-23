package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Notifier sends a notice to the phone on file (consumer-side interface; the
// notifications module implements it, tests use a fake). Kinds are codes:
// erasure_requested, erasure_cancelled, erasure_completed. The notice never carries
// health data (K-DP-09).
type Notifier interface {
	NoticeToPhoneOnFile(ctx context.Context, subjectRef, kind, requestRef string) error
}

// RightsConfig holds the timers and limits. Every value comes from the country pack
// (legal.grievance, retention rule RET-18) or the decisions log, passed in by the
// composition root; nothing here is a literal.
type RightsConfig struct {
	// Modules get one RightsTask per erasure step. Must include "records".
	Modules []string
	// RespondWithin: data-rights target (pack legal.grievance data_rights_response_days).
	RespondWithin time.Duration
	// RecordCopyWithin: copy of medical records (pack medical_record_copy_hours).
	RecordCopyWithin time.Duration
	// AckWithin: acknowledgement (pack acknowledge_hours).
	AckWithin time.Duration
	// UndoWindow: time to undo an erasure before hard deletion (default 7 days [VALIDATE]).
	UndoWindow time.Duration
	// HoldRegistration: RET-18 hold for registration data after closure.
	HoldRegistration time.Duration
	// HoldProcessing: RET-18 hold for processing data and logs from HoldProcessingFrom.
	HoldProcessing     time.Duration
	HoldProcessingFrom time.Time
	// ExportAvailability: how long a finished export stays offered (data-rights-flows §3).
	ExportAvailability time.Duration
	// LinkTTL: life of a download link (minutes, S-171).
	LinkTTL time.Duration
	// UploadMaxBytes, ScanTimeout, ProhibitedValidators: uploads (S-159).
	UploadMaxBytes int64
	ScanTimeout    time.Duration
	// MaxDocuments and MaxTotalBytes cap one patient's uploads (review M6). Zero means the
	// defaults: 200 files and 500 MB.
	MaxDocuments         int
	MaxTotalBytes        int64
	ProhibitedValidators []string
}

// RightsDeps are the collaborators.
type RightsDeps struct {
	Crypto   *crypto.Service
	Blobs    blobstore.Store
	Scanner  malwarescan.Scanner
	Notifier Notifier
	// AccessLog answers the patient's "who viewed my record" (audit module).
	AccessLog ap.AccessLogReader
}

// Rights implements data rights, uploads, secure links and the access log on top of
// the records module.
type Rights struct {
	m   *Module
	cfg RightsConfig
	d   RightsDeps
}

// NewRights builds the rights service. Missing timers or collaborators are start-up
// errors (fail closed).
func NewRights(m *Module, cfg RightsConfig, d RightsDeps) (*Rights, error) {
	positive := []time.Duration{cfg.RespondWithin, cfg.RecordCopyWithin, cfg.AckWithin, cfg.UndoWindow, cfg.HoldRegistration,
		cfg.HoldProcessing, cfg.ExportAvailability, cfg.LinkTTL, cfg.ScanTimeout}
	for _, p := range positive {
		if p <= 0 {
			return nil, errors.New("records: every rights timer and limit must be set (country pack and config)")
		}
	}
	if cfg.UploadMaxBytes <= 0 || cfg.HoldProcessingFrom.IsZero() || len(cfg.ProhibitedValidators) == 0 {
		return nil, errors.New("records: upload limit, RET-18 start date and prohibited-identifier validators are required")
	}
	if cfg.MaxDocuments <= 0 {
		cfg.MaxDocuments = 200
	}
	if cfg.MaxTotalBytes <= 0 {
		cfg.MaxTotalBytes = 500 << 20
	}
	if !contains(cfg.Modules, "records") {
		return nil, errors.New("records: the module list must include records")
	}
	if d.Crypto == nil || d.Blobs == nil || d.Scanner == nil || d.Notifier == nil {
		return nil, errors.New("records: crypto, blob store, scanner and notifier are required")
	}
	return &Rights{m: m, cfg: cfg, d: d}, nil
}

// heldAccessOK lists the only access that survives an erasure request: a verified legal
// order (staff grant with reason legal_order) and the treating doctor's own records (T2,
// RET-02). Everything else is hidden.
func heldAccessOK(g rp.Grant, r rp.Request) bool {
	switch g.Tier {
	case rp.JITStaff:
		return g.ReasonCode == "legal_order"
	case rp.T2OwnRecords:
		return r.Kind == rp.KindClinicalOwn
	}
	return false
}

func (r *Rights) now() time.Time { return r.m.now().UTC() }

func (r *Rights) stepFresh(c rp.Caller) bool {
	if c.StepUpAt == nil {
		return false
	}
	d := r.now().Sub(c.StepUpAt.UTC())
	return d >= 0 && d <= ip.StepUpWindow
}

func (r *Rights) deadline(t rp.RightsType) time.Duration {
	if t == rp.RightsExport {
		return r.cfg.RecordCopyWithin
	}
	return r.cfg.RespondWithin
}

const requestCols = `id::text, subject_ref::text, type, status, received_at, ack_due_at, due_at, acknowledged_at, completed_at, outcome,
	undo_until, hold_ends_at, export_expires_at, report`

type rowScanner interface{ Scan(dest ...any) error }

type reportJSON struct {
	Module string     `json:"module"`
	Action string     `json:"action"`
	Rule   string     `json:"rule"`
	Until  *time.Time `json:"until,omitempty"`
}

func scanRequest(s rowScanner) (rp.RightsRequest, error) {
	var q rp.RightsRequest
	var typ, status string
	var rep []byte
	if err := s.Scan(&q.ID, &q.SubjectRef, &typ, &status, &q.ReceivedAt, &q.AckDueAt, &q.DueAt, &q.AcknowledgedAt, &q.CompletedAt, &q.Outcome,
		&q.UndoUntil, &q.HoldEndsAt, &q.ExportExpiresAt, &rep); err != nil {
		return q, err
	}
	q.Type, q.Status = rp.RightsType(typ), rp.RightsStatus(status)
	var items []reportJSON
	if err := json.Unmarshal(rep, &items); err != nil {
		return q, err
	}
	for _, it := range items {
		q.Report = append(q.Report, rp.ReportItem{Module: it.Module, Action: it.Action, RetentionRule: it.Rule, Until: it.Until})
	}
	return q, nil
}

// RequestRights records a data-rights request. Export and erasure need a fresh step-up
// (TC-63, E-SEC-10). details is free text for a correction or the nominee for a nomination
// (sealed with the patient key). Erasure starts step 1 at once.
func (r *Rights) RequestRights(ctx context.Context, c rp.Caller, t rp.RightsType, details string) (rp.RightsRequest, error) {
	if !reUUID.MatchString(c.UserRef) {
		return rp.RightsRequest{}, rp.ErrInvalid
	}
	switch t {
	case rp.RightsAccess, rp.RightsExport, rp.RightsCorrection, rp.RightsErasure, rp.RightsNomination:
	default:
		return rp.RightsRequest{}, fmt.Errorf("%w: type", rp.ErrInvalid)
	}
	if c.Restricted {
		return rp.RightsRequest{}, rp.ErrDenied
	}
	if (t == rp.RightsExport || t == rp.RightsErasure) && !r.stepFresh(c) {
		return rp.RightsRequest{}, rp.ErrStepUpRequired
	}
	needsText := t == rp.RightsCorrection || t == rp.RightsNomination
	details = strings.TrimSpace(details)
	if needsText && (details == "" || utf8.RuneCountInString(details) > 2000) {
		return rp.RightsRequest{}, fmt.Errorf("%w: details", rp.ErrInvalid)
	}
	if !needsText && details != "" {
		return rp.RightsRequest{}, fmt.Errorf("%w: details not accepted", rp.ErrInvalid)
	}
	if needsText && scrub.ContainsProhibitedID(details, r.cfg.ProhibitedValidators) {
		return rp.RightsRequest{}, rp.ErrProhibitedID
	}
	if restricted, err := r.isRestricted(ctx, c.UserRef); err != nil {
		return rp.RightsRequest{}, err
	} else if restricted {
		return rp.RightsRequest{}, rp.ErrRestricted
	}

	now := r.now()
	id := ids.New()
	var sealed any
	if needsText {
		s, err := crypto.SealPatient(ctx, r.d.Crypto, c.UserRef, crypto.AAD{Table: "rights_request", Column: "details", RowID: id}, []byte(details))
		if err != nil {
			return rp.RightsRequest{}, err
		}
		sealed = s
	}
	erasure := t == rp.RightsErasure
	status := rp.StatusReceived
	var undo, restrictedAt any
	if erasure {
		status = rp.StatusInProgress
		undo = now.Add(r.cfg.UndoWindow)
		restrictedAt = now
	}
	var out rp.RightsRequest
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO rights_request (id, subject_ref, type, status, received_at, ack_due_at, due_at, acknowledged_at, details_sealed, undo_until, restricted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$5,$8,$9,$10)`, id, c.UserRef, string(t), string(status), now, now.Add(r.cfg.AckWithin), now.Add(r.deadline(t)), sealed, undo, restrictedAt); err != nil {
			if db.IsUniqueViolation(err, "rights_request_one_erasure") {
				return rp.ErrConflict
			}
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "rights.request", SubjectRef: c.UserRef,
			ResourceType: "rights_request", ResourceRef: id, Purpose: string(t), Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: "rights.request_received", SubjectRef: c.UserRef, Payload: map[string]string{"requestRef": id, "rightsType": string(t)}}); err != nil {
			return err
		}
		if erasure {
			if err := r.startRestrict(ctx, tx, c.UserRef, id, now); err != nil {
				return err
			}
		}
		var err error
		out, err = scanRequest(tx.QueryRow(ctx, `SELECT `+requestCols+` FROM rights_request WHERE id=$1`, id))
		return err
	})
	if err != nil {
		return rp.RightsRequest{}, err
	}
	if erasure {
		r.m.Invalidate(c.UserRef)
		r.journal(ctx, id, journalRequested) // survives a database restore (S-185)
		r.notice(ctx, c.UserRef, "erasure_requested", id)
	}
	return out, nil
}

// startRestrict is step 1 of an erasure: the account is hidden at once from the app and
// every staff role, each module gets a task, and this module's own task is done.
func (r *Rights) startRestrict(ctx context.Context, tx pgx.Tx, subject, id string, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO account_restriction (subject_ref, request_id, restricted_at) VALUES ($1,$2,$3)
		ON CONFLICT (subject_ref) DO UPDATE SET request_id=EXCLUDED.request_id, restricted_at=EXCLUDED.restricted_at, released_at=NULL`, subject, id, now); err != nil {
		return err
	}
	for _, mod := range r.cfg.Modules {
		if _, err := tx.Exec(ctx, `INSERT INTO rights_task (request_id, step, module) VALUES ($1,1,$2)`, id, mod); err != nil {
			return err
		}
	}
	if err := outbox.Emit(ctx, tx, outbox.Event{Type: "rights.erasure_requested", SubjectRef: subject, Payload: map[string]string{"requestRef": id, "subjectRef": subject}}); err != nil {
		return err
	}
	if err := notify(ctx, tx, subject); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE rights_task SET outcome='restricted', retention_rule='RET-18', done_at=$2 WHERE request_id=$1 AND step=1 AND module='records'`, id, now); err != nil {
		return err
	}
	_, err := r.advance(ctx, tx, id)
	return err
}

// notice sends a notice to the phone on file. A failed notice is logged and never undoes
// the patient's request; the event outbox carries the same fact to the notifications
// module for retry.
func (r *Rights) notice(ctx context.Context, subject, kind, ref string) {
	if err := r.d.Notifier.NoticeToPhoneOnFile(ctx, subject, kind, ref); err != nil {
		r.m.log.Warn("records.notice_failed", "kind", kind, "resource_ref", ref, logging.Err(err))
	}
}

func (r *Rights) isRestricted(ctx context.Context, subject string) (bool, error) {
	var n int
	err := r.m.pool.QueryRow(ctx, `SELECT count(*) FROM account_restriction WHERE subject_ref=$1 AND released_at IS NULL`, subject).Scan(&n)
	return n > 0, err
}

// holdEnd is when the RET-18 legal-minimum hold ends for an account closed at closure:
// registration data 180 days after closure; processing data and logs one year after,
// from the date the DPDP Rules apply. The longest applicable period wins. Closure is the
// end of the undo window (the point of no return), the conservative reading [VALIDATE].
func (r *Rights) holdEnd(closure time.Time) time.Time {
	end := closure.Add(r.cfg.HoldRegistration)
	if !closure.Before(r.cfg.HoldProcessingFrom) {
		if e := closure.Add(r.cfg.HoldProcessing); e.After(end) {
			end = e
		}
	}
	return end
}

// advance moves an erasure forward when all tasks of its current step are done. It
// returns ready=true when step 2 is complete and only key destruction is left.
func (r *Rights) advance(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var status string
	var step int
	var undo *time.Time
	if err := tx.QueryRow(ctx, `SELECT status, step, undo_until FROM rights_request WHERE id=$1 FOR UPDATE`, id).Scan(&status, &step, &undo); err != nil {
		return false, err
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM rights_task WHERE request_id=$1 AND step=$2 AND done_at IS NULL`, id, step).Scan(&pending); err != nil {
		return false, err
	}
	if pending > 0 {
		return false, nil
	}
	switch rp.RightsStatus(status) {
	case rp.StatusInProgress:
		if undo == nil {
			return false, errors.New("records: erasure without undo time")
		}
		hold := r.holdEnd(*undo)
		rep, err := r.buildReport(ctx, tx, id, hold, false)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE rights_request SET status='restricted', hold_ends_at=$2, report=$3 WHERE id=$1`, id, hold, rep)
		return false, err
	case rp.StatusFinalising:
		return true, nil
	}
	return false, nil
}

// buildReport lists what was kept, why and until when (E-DP-03). Codes only.
func (r *Rights) buildReport(ctx context.Context, tx pgx.Tx, id string, hold time.Time, final bool) ([]byte, error) {
	erased := "erased_after_hold"
	if final {
		erased = "erased"
	}
	items := []reportJSON{
		{Module: "records", Action: erased, Rule: "RET-18", Until: &hold},
		{Module: "records", Action: "restricted", Rule: "RET-05"},
		{Module: "records", Action: "restricted", Rule: "RET-06"},
		{Module: "records", Action: "restricted", Rule: "RET-09"},
	}
	rows, err := tx.Query(ctx, `SELECT module, outcome, retention_rule FROM rights_task WHERE request_id=$1 AND step=1 AND module<>'records' ORDER BY module`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mod, outcome, rule string
		if err := rows.Scan(&mod, &outcome, &rule); err != nil {
			return nil, err
		}
		switch {
		case outcome == "none":
		case outcome == "restricted" && (rule == "" || rule == "RET-18"):
			items = append(items, reportJSON{Module: mod, Action: erased, Rule: "RET-18", Until: &hold})
		default:
			items = append(items, reportJSON{Module: mod, Action: outcome, Rule: rule})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

// OnRightsTaskDone consumes rights.task_done {requestRef, module, outcome[, retentionRule]}
// from every module. Subscribe it on the bus. It is idempotent; late reports for an
// undone or finished request are ignored.
func (r *Rights) OnRightsTaskDone(ctx context.Context, e outbox.Event) error {
	ref, mod, outcome, rule := e.Payload["requestRef"], e.Payload["module"], e.Payload["outcome"], e.Payload["retentionRule"]
	if !reUUID.MatchString(ref) || mod == "records" || !contains(r.cfg.Modules, mod) {
		return fmt.Errorf("%w: task report", rp.ErrInvalid)
	}
	var finishRef string
	err := outbox.Once(ctx, r.m.pool, "records.rights_task_done", e, func(tx pgx.Tx) error {
		finishRef = ""
		var status string
		var step int
		err := tx.QueryRow(ctx, `SELECT status, step FROM rights_request WHERE id=$1 AND type='erasure' FOR UPDATE`, ref).Scan(&status, &step)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		switch rp.RightsStatus(status) {
		case rp.StatusInProgress:
			step = 1
		case rp.StatusFinalising:
			step = 2
		default:
			return nil // undone, finished or waiting out the hold: nothing to collect
		}
		valid := map[int][]string{1: {"restricted", "deleted", "none"}, 2: {"erased", "none"}}
		if !contains(valid[step], outcome) {
			return fmt.Errorf("%w: outcome", rp.ErrInvalid)
		}
		tag, err := tx.Exec(ctx, `UPDATE rights_task SET outcome=$4, retention_rule=$5, done_at=$6 WHERE request_id=$1 AND step=$2 AND module=$3 AND done_at IS NULL`,
			ref, step, mod, outcome, rule, r.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		ready, err := r.advance(ctx, tx, ref)
		if ready {
			finishRef = ref
		}
		return err
	})
	if err != nil {
		return err
	}
	if finishRef != "" {
		if err := r.finishErasure(ctx, finishRef); err != nil {
			r.m.log.Warn("records.erasure_finish_deferred", "resource_ref", finishRef, logging.Err(err))
		}
	}
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// CancelErasure undoes an erasure inside the window (TC-63). It needs a fresh step-up
// because it makes the account visible again. After the window it is refused.
func (r *Rights) CancelErasure(ctx context.Context, c rp.Caller, requestID string) error {
	if !allUUID(c.UserRef, requestID) {
		return rp.ErrNotFound
	}
	if c.Restricted {
		return rp.ErrDenied
	}
	if !r.stepFresh(c) {
		return rp.ErrStepUpRequired
	}
	now := r.now()
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		var undo *time.Time
		err := tx.QueryRow(ctx, `SELECT status, undo_until FROM rights_request WHERE id=$1 AND subject_ref=$2 AND type='erasure' FOR UPDATE`, requestID, c.UserRef).Scan(&status, &undo)
		if errors.Is(err, pgx.ErrNoRows) {
			return rp.ErrNotFound
		}
		if err != nil {
			return err
		}
		if s := rp.RightsStatus(status); s != rp.StatusInProgress && s != rp.StatusRestricted {
			if s == rp.StatusCancelled {
				return nil // already undone
			}
			return rp.ErrUndoClosed
		}
		if undo == nil || !now.Before(*undo) {
			return rp.ErrUndoClosed
		}
		if _, err := tx.Exec(ctx, `UPDATE rights_request SET status='cancelled', cancelled_at=$2, outcome='undone' WHERE id=$1`, requestID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_restriction SET released_at=$2 WHERE subject_ref=$1 AND released_at IS NULL`, c.UserRef, now); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "rights.erasure_undone", SubjectRef: c.UserRef,
			ResourceType: "rights_request", ResourceRef: requestID, Purpose: "erasure", Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: "rights.erasure_cancelled", SubjectRef: c.UserRef, Payload: map[string]string{"requestRef": requestID, "subjectRef": c.UserRef}}); err != nil {
			return err
		}
		return notify(ctx, tx, c.UserRef)
	})
	if err != nil {
		return err
	}
	r.m.Invalidate(c.UserRef)
	r.journal(ctx, requestID, journalCancelled)
	r.notice(ctx, c.UserRef, "erasure_cancelled", requestID)
	return nil
}

// GetRequest returns one of the caller's own requests (others' are "not found").
func (r *Rights) GetRequest(ctx context.Context, c rp.Caller, id string) (rp.RightsRequest, error) {
	if !allUUID(c.UserRef, id) {
		return rp.RightsRequest{}, rp.ErrNotFound
	}
	q, err := scanRequest(r.m.pool.QueryRow(ctx, `SELECT `+requestCols+` FROM rights_request WHERE id=$1 AND subject_ref=$2`, id, c.UserRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return q, rp.ErrNotFound
	}
	return q, err
}

// ListRequests lists the caller's requests, newest first, with an opaque cursor.
func (r *Rights) ListRequests(ctx context.Context, c rp.Caller, cursor string, limit int) ([]rp.RightsRequest, string, error) {
	if !reUUID.MatchString(c.UserRef) {
		return nil, "", rp.ErrNotFound
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var at *time.Time
	curID := "00000000-0000-0000-0000-000000000000"
	if cursor != "" {
		us, id, ok := strings.Cut(cursor, ".")
		n, err := strconv.ParseInt(us, 10, 64)
		if !ok || err != nil || !reUUID.MatchString(id) {
			return nil, "", rp.ErrInvalid
		}
		t := time.UnixMicro(n).UTC()
		at, curID = &t, id
	}
	rows, err := r.m.pool.Query(ctx, `SELECT `+requestCols+` FROM rights_request WHERE subject_ref=$1
		AND ($2::timestamptz IS NULL OR (received_at, id) < ($2::timestamptz, $3::uuid)) ORDER BY received_at DESC, id DESC LIMIT $4`, c.UserRef, at, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []rp.RightsRequest
	for rows.Next() {
		q, err := scanRequest(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		l := out[len(out)-1]
		next = strconv.FormatInt(l.ReceivedAt.UnixMicro(), 10) + "." + l.ID
	}
	return out, next, nil
}

// RunDue is the worker entry point (run it every minute): builds pending exports, starts
// step 2 of erasures whose RET-18 hold has ended, retries unfinished ones, and removes
// old link rows.
func (r *Rights) RunDue(ctx context.Context) error {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	keep(r.processExports(ctx, 50))
	keep(r.journalCatchUp(ctx))
	now := r.now()
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, status FROM rights_request WHERE type='erasure'
		AND ((status='restricted' AND hold_ends_at <= $1) OR status='finalising') ORDER BY hold_ends_at LIMIT 50`, now)
	if err != nil {
		return err
	}
	type due struct{ id, status string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.status); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		if d.status == string(rp.StatusRestricted) {
			keep(r.startFinal(ctx, d.id))
		} else {
			keep(r.finishErasure(ctx, d.id))
		}
	}
	keep(r.purgeExpiredExports(ctx, now))
	_, err = r.m.pool.Exec(ctx, `DELETE FROM file_link WHERE expires_at < $1`, now.Add(-24*time.Hour))
	keep(err)
	return firstErr
}

// startFinal begins step 2 once the hold has ended: this module deletes its own erasable
// data, the other modules are told to delete theirs.
func (r *Rights) startFinal(ctx context.Context, id string) error {
	now := r.now()
	var blobKeys []string
	ready := false
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		blobKeys, ready = nil, false
		var status, subject string
		var hold *time.Time
		if err := tx.QueryRow(ctx, `SELECT status, subject_ref::text, hold_ends_at FROM rights_request WHERE id=$1 AND type='erasure' FOR UPDATE`, id).Scan(&status, &subject, &hold); err != nil {
			return err
		}
		if rp.RightsStatus(status) != rp.StatusRestricted || hold == nil || now.Before(*hold) {
			return nil // not due: the hold is never cut short
		}
		if _, err := tx.Exec(ctx, `UPDATE rights_request SET status='finalising', step=2 WHERE id=$1`, id); err != nil {
			return err
		}
		for _, mod := range r.cfg.Modules {
			if _, err := tx.Exec(ctx, `INSERT INTO rights_task (request_id, step, module) VALUES ($1,2,$2)`, id, mod); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT blob_key FROM document WHERE patient_ref=$1 UNION SELECT export_blob_key FROM rights_request WHERE subject_ref=$1 AND export_blob_key IS NOT NULL`, subject)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			blobKeys = append(blobKeys, k)
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM document WHERE patient_ref=$1`, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE rights_request SET details_sealed=NULL, export_blob_key=NULL WHERE subject_ref=$1`, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM file_link WHERE patient_ref=$1`, subject); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE rights_task SET outcome='erased', retention_rule='RET-18', done_at=$2 WHERE request_id=$1 AND step=2 AND module='records'`, id, now); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: "rights.erasure_due", SubjectRef: subject, Payload: map[string]string{"requestRef": id, "subjectRef": subject}}); err != nil {
			return err
		}
		ready, err = r.advance(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	r.deleteBlobs(ctx, blobKeys)
	if ready {
		return r.finishErasure(ctx, id)
	}
	return nil
}

// deleteBlobs removes ciphertext objects where the store supports deletion. Without it the
// objects stay unreadable once the patient key is destroyed (crypto-shredding).
func (r *Rights) deleteBlobs(ctx context.Context, keys []string) {
	d, ok := r.d.Blobs.(interface {
		Delete(ctx context.Context, key string) error
	})
	if !ok {
		return
	}
	for _, k := range keys {
		if err := d.Delete(ctx, k); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			r.m.log.Warn("records.blob_delete_failed", logging.Err(err))
		}
	}
}

// finishErasure destroys the patient key, only when every step-2 task is done and the
// RET-18 hold has ended, then closes the request and sends the report notice.
func (r *Rights) finishErasure(ctx context.Context, id string) error {
	now := r.now()
	var status, subject string
	var hold *time.Time
	var pending int
	if err := r.m.pool.QueryRow(ctx, `SELECT status, subject_ref::text, hold_ends_at,
		(SELECT count(*) FROM rights_task t WHERE t.request_id=r.id AND t.step=2 AND t.done_at IS NULL)
		FROM rights_request r WHERE id=$1 AND type='erasure'`, id).Scan(&status, &subject, &hold, &pending); err != nil {
		return err
	}
	if rp.RightsStatus(status) != rp.StatusFinalising || pending > 0 || hold == nil || now.Before(*hold) {
		return nil
	}
	if err := r.d.Crypto.DestroyPatientKey(ctx, subject, "erasure_hold_ended"); err != nil {
		return err
	}
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rep, err := r.buildReport(ctx, tx, id, *hold, true)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE rights_request SET status='completed', completed_at=$2, deleted_at=$2, outcome='erased', report=$3 WHERE id=$1 AND status='finalising'`, id, now, rep)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: subject, ActorRole: "system", Action: "rights.erasure_completed", SubjectRef: subject,
			ResourceType: "rights_request", ResourceRef: id, Purpose: "erasure", Result: ap.Allowed}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.erasure_completed", SubjectRef: subject, Payload: map[string]string{"requestRef": id, "subjectRef": subject}})
	})
	if err != nil {
		return err
	}
	r.journal(ctx, id, journalCompleted)
	r.notice(ctx, subject, "erasure_completed", id)
	return nil
}

// purgeExpiredExports deletes the blobs of exports that have passed ExportAvailability and
// marks them gone (review M6): the ciphertext does not outlive the offer.
func (r *Rights) purgeExpiredExports(ctx context.Context, now time.Time) error {
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, export_blob_key FROM rights_request
		WHERE export_blob_key IS NOT NULL AND export_expires_at <= $1 ORDER BY export_expires_at LIMIT 100`, now)
	if err != nil {
		return err
	}
	type exp struct{ id, key string }
	var list []exp
	for rows.Next() {
		var e exp
		if err := rows.Scan(&e.id, &e.key); err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range list {
		r.deleteBlobs(ctx, []string{e.key})
		if _, err := r.m.pool.Exec(ctx, `UPDATE rights_request SET export_blob_key=NULL WHERE id=$1 AND export_blob_key=$2`, e.id, e.key); err != nil {
			return err
		}
	}
	return nil
}
