package clinical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// The doctor's view of a patient's shared history (S-169, C01, C09, E-DP-01, E-DP-02).
//
// Every read goes through records.Authorize (K-DP-02), which audits it durably before any data
// is read. The view holds only SIGNED notes and prescriptions written by OTHER doctors
// (KindClinicalHistory): drafts are never shown to anyone but their author, and signed
// versions are immutable (K-CS-05). The software adds, infers and fills in nothing (K-CS-03).

// DoctorVerifier is satisfied by verification's public Reader (K-CS-01).
type DoctorVerifier interface {
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// HistoryConfig holds the view's settings, from configuration.
type HistoryConfig struct {
	// WarnBefore: the doctor is warned this long before the share ends (E-DP-02: 10 minutes).
	WarnBefore time.Duration
	// MaxItems caps one response (default 100).
	MaxItems int
}

// HistoryDeps are the collaborators; each is an interface another module implements.
type HistoryDeps struct {
	Crypto   *crypto.Service
	Gate     rp.Gate
	Windows  rp.ShareWindowReader
	Verifier DoctorVerifier
	Log      *slog.Logger
	Now      func() time.Time
}

// History serves the doctor's history view.
type History struct {
	m   *Module
	cfg HistoryConfig
	d   HistoryDeps
}

// NewHistory builds the service. It fails closed on missing collaborators or settings.
func NewHistory(m *Module, cfg HistoryConfig, d HistoryDeps) (*History, error) {
	if m == nil || d.Crypto == nil || d.Gate == nil || d.Windows == nil || d.Verifier == nil {
		return nil, errors.New("clinical: crypto, gate, share window reader and doctor verifier are required for the history view")
	}
	if cfg.WarnBefore <= 0 {
		return nil, errors.New("clinical: the history expiry warning time must be set")
	}
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 100
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &History{m: m, cfg: cfg, d: d}, nil
}

// HistoryAccess says how long the doctor may keep reading, so the app can warn before it ends.
type HistoryAccess struct {
	GrantID   string
	Tier      rp.Tier
	ExpiresAt time.Time
	// WarnAt is ExpiresAt minus the warning time; Warn is true once WarnAt has passed.
	WarnAt time.Time
	Warn   bool
}

// HistoryItem is one signed record of another doctor.
type HistoryItem struct {
	Kind      string // note | prescription
	AuthorRef string
	SignedAt  time.Time
	Note      *NoteView
	Rx        *RxView
}

// HistoryView is the response.
type HistoryView struct {
	Access HistoryAccess
	Items  []HistoryItem
	// Next is the cursor for the next (older) page; empty on the last page.
	Next string
}

func (h *History) deniedAudit(ctx context.Context, c Caller, patientRef, reason string) {
	err := db.InTx(ctx, h.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserID, ActorRole: "doctor", Action: "records.read.clinical_history", SubjectRef: patientRef,
			ResourceType: "clinical_history", Purpose: reason, Result: ap.Denied, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID})
	})
	if err != nil {
		h.d.Log.Error("clinical.audit_failed", logging.Err(err))
	}
}

// PatientHistory lists the signed records of other doctors for a patient who shared with the
// calling doctor. A doctor with no relationship with the patient gets ErrNotFound (404); an
// ended, revoked or missing share gets a DeniedError (403 consent.required).
func (h *History) PatientHistory(ctx context.Context, c Caller, patientRef string, limit int, after string) (HistoryView, error) {
	if c.Role != "doctor" || !ids.Valid(c.UserID) || !ids.Valid(patientRef) || c.UserID == patientRef {
		return HistoryView{}, ErrNotFound
	}
	limit = min(max(limit, 1), h.cfg.MaxItems)
	cur, err := parseCursor(after)
	if err != nil {
		return HistoryView{}, &InvalidError{Field: "cursor", Code: "invalid"}
	}
	// Only a Verified doctor reads other doctors' records; any error counts as not verified.
	if ok, err := h.d.Verifier.IsVerified(ctx, c.UserID); err != nil || !ok {
		h.deniedAudit(ctx, c, patientRef, "doctor_not_verified")
		return HistoryView{}, ErrDoctorNotVerified
	}
	d, err := h.d.Gate.Authorize(ctx, rp.Request{
		Actor:      rp.Actor{Ref: c.UserID, Role: c.Role, ClinicRef: c.ClinicRef, Restricted: c.Restricted},
		PatientRef: patientRef, Kind: rp.KindClinicalHistory, ResourceType: "clinical_history",
		RequestID: c.RequestID, Client: c.Client, IPHash: c.IPHash})
	if err != nil {
		return HistoryView{}, fmt.Errorf("clinical: authorize: %w", err) // fail closed: no audit, no data
	}
	if !d.Allowed {
		return HistoryView{}, &DeniedError{Hide: d.Hide, Reason: d.Reason}
	}
	until, ok, err := h.d.Windows.HistoryAccessUntil(ctx, patientRef, c.UserID)
	if err != nil {
		return HistoryView{}, fmt.Errorf("clinical: share window: %w", err)
	}
	if !ok {
		return HistoryView{}, &DeniedError{Reason: "no_active_grant"} // never read without a known end
	}
	now := h.d.Now().UTC()
	view := HistoryView{Access: HistoryAccess{GrantID: d.GrantID, Tier: d.Tier, ExpiresAt: until.UTC(), WarnAt: until.UTC().Add(-h.cfg.WarnBefore)}}
	view.Access.Warn = !now.Before(view.Access.WarnAt)

	notes, err := h.notes(ctx, patientRef, c.UserID, limit+1, cur)
	if err != nil {
		return HistoryView{}, err
	}
	rxs, err := h.prescriptions(ctx, patientRef, c.UserID, limit+1, cur)
	if err != nil {
		return HistoryView{}, err
	}
	view.Items = append(append([]HistoryItem{}, notes...), rxs...)
	sort.SliceStable(view.Items, func(i, j int) bool {
		a, b := view.Items[i], view.Items[j]
		if !a.SignedAt.Equal(b.SignedAt) {
			return a.SignedAt.After(b.SignedAt)
		}
		return a.key() > b.key()
	})
	if len(view.Items) > limit {
		view.Items = view.Items[:limit]
		last := view.Items[limit-1]
		view.Next = last.SignedAt.UTC().Format(time.RFC3339Nano) + "|" + last.key()
	}
	return view, nil
}

// notes returns the latest SIGNED version of each note of other doctors, newest first.
func (h *History) notes(ctx context.Context, patientRef, doctorRef string, limit int, cur cursor) ([]HistoryItem, error) {
	rows, err := h.m.pool.Query(ctx, `SELECT n.id::text, n.appointment_ref::text, n.doctor_ref::text, v.id::text, v.version, v.content, v.signed_at, v.updated_at
		FROM note n JOIN LATERAL (SELECT id, version, content, signed_at, updated_at FROM note_version WHERE note_id = n.id AND state = 'signed' ORDER BY version DESC LIMIT 1) v ON true
		WHERE n.patient_ref = $1 AND n.doctor_ref <> $2 AND ($4::timestamptz IS NULL OR (v.signed_at, n.id) < ($4::timestamptz, $5::uuid))
		ORDER BY v.signed_at DESC, n.id DESC LIMIT $3`, patientRef, doctorRef, limit, cur.at(), cur.idArg())
	if err != nil {
		return nil, err
	}
	type raw struct {
		item       HistoryItem
		versionRef string
		ct         careKey
	}
	var list []raw
	for rows.Next() {
		var x raw
		nv := &NoteView{State: "signed"}
		var signed time.Time
		if err := rows.Scan(&nv.NoteRef, &nv.AppointmentRef, &x.item.AuthorRef, &x.versionRef, &nv.Version, &x.ct, &signed, &nv.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		nv.SignedAt = &signed
		x.item.Kind, x.item.SignedAt, x.item.Note = "note", signed, nv
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]HistoryItem, 0, len(list))
	for _, x := range list {
		b, err := crypto.Open(ctx, h.d.Crypto, crypto.AAD{Table: "note_version", Column: "content", RowID: x.versionRef}, x.ct)
		if err != nil {
			return nil, fmt.Errorf("clinical: open note: %w", err) // fail closed
		}
		if err := json.Unmarshal(b, &x.item.Note.Content); err != nil {
			return nil, err
		}
		out = append(out, x.item)
	}
	return out, nil
}

// prescriptions returns the latest SIGNED version of each prescription of other doctors.
// The signed document and signature are not part of this view.
func (h *History) prescriptions(ctx context.Context, patientRef, doctorRef string, limit int, cur cursor) ([]HistoryItem, error) {
	rows, err := h.m.pool.Query(ctx, `SELECT p.id::text, p.appointment_ref::text, p.doctor_ref::text, v.id::text, v.version, v.content, v.signed_at, v.updated_at,
		v.consult_mode, v.ruleset_version, v.classification_version
		FROM prescription p JOIN LATERAL (SELECT id, version, content, signed_at, updated_at, consult_mode, ruleset_version, classification_version
			FROM prescription_version WHERE prescription_id = p.id AND state = 'signed' ORDER BY version DESC LIMIT 1) v ON true
		WHERE p.patient_ref = $1 AND p.doctor_ref <> $2 AND ($4::timestamptz IS NULL OR (v.signed_at, p.id) < ($4::timestamptz, $5::uuid))
		ORDER BY v.signed_at DESC, p.id DESC LIMIT $3`, patientRef, doctorRef, limit, cur.at(), cur.idArg())
	if err != nil {
		return nil, err
	}
	type raw struct {
		item       HistoryItem
		versionRef string
		ct         careKey
	}
	var list []raw
	for rows.Next() {
		var x raw
		rv := &RxView{State: "signed"}
		var signed time.Time
		if err := rows.Scan(&rv.PrescriptionRef, &rv.AppointmentRef, &x.item.AuthorRef, &x.versionRef, &rv.Version, &x.ct, &signed, &rv.UpdatedAt,
			&rv.ConsultMode, &rv.RuleSetVersion, &rv.ClassificationVersion); err != nil {
			rows.Close()
			return nil, err
		}
		rv.SignedAt = &signed
		x.item.Kind, x.item.SignedAt, x.item.Rx = "prescription", signed, rv
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]HistoryItem, 0, len(list))
	for _, x := range list {
		b, err := crypto.Open(ctx, h.d.Crypto, crypto.AAD{Table: "prescription_version", Column: "content", RowID: x.versionRef}, x.ct)
		if err != nil {
			return nil, fmt.Errorf("clinical: open prescription: %w", err)
		}
		if err := json.Unmarshal(b, &x.item.Rx.Content); err != nil {
			return nil, err
		}
		out = append(out, x.item)
	}
	return out, nil
}

// key is the record's ID, the tie-breaker of the (signed time, ID) order.
func (i HistoryItem) key() string {
	if i.Note != nil {
		return i.Note.NoteRef
	}
	if i.Rx != nil {
		return i.Rx.PrescriptionRef
	}
	return ""
}

// cursor is a keyset position: records strictly older than (signedAt, id).
type cursor struct {
	t  time.Time
	id string
}

func (c cursor) at() any {
	if c.id == "" {
		return nil
	}
	return c.t
}

func parseCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	ts, id, ok := strings.Cut(s, "|")
	if !ok || !ids.Valid(id) {
		return cursor{}, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return cursor{}, err
	}
	return cursor{t: t, id: id}, nil
}

func (c cursor) idArg() any {
	if c.id == "" {
		return nil
	}
	return c.id
}
