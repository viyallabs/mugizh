package clinical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	vp "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// Deps are the collaborators of Rx. Every one is an interface the owning module implements
// (or a fake in tests); clinical imports none of those modules' internals.
type Deps struct {
	Crypto    *crypto.Service
	Gate      rp.Gate
	Granter   T2Granter
	Verifier  vp.Reader
	Consults  ConsultInfo
	Signer    esign.Signer // may be nil only when Config.ESignEnabled is false
	Scheduler schedulercore.Client
	Notifier  np.Sender // optional: nil sends nothing
	Log       *slog.Logger
	Now       func() time.Time
}

// Rx is the notes and prescriptions service (S-139, S-149, S-150, S-151, S-152, S-164).
type Rx struct {
	m       *Module
	cfg     Config
	d       Deps
	limiter *httpx.Limiter
}

// NewRx builds the service on a Module. It fails closed on missing collaborators.
func NewRx(m *Module, cfg Config, d Deps) (*Rx, error) {
	if m == nil || d.Crypto == nil || d.Gate == nil || d.Granter == nil || d.Verifier == nil || d.Consults == nil || d.Scheduler == nil {
		return nil, errors.New("clinical: crypto, gate, T2 granter, verification reader, consult info and scheduler client are required")
	}
	if cfg.ESignEnabled && d.Signer == nil {
		return nil, errors.New("clinical: an e-signature adapter is required when ESignEnabled is true")
	}
	if cfg.ClassificationVersion == "" || cfg.AppRef == "" || cfg.ReminderStartHour < 0 || cfg.ReminderEndHour > 23 || cfg.ReminderStartHour > cfg.ReminderEndHour {
		return nil, errors.New("clinical: invalid configuration")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.VerifyPerMinute <= 0 {
		cfg.VerifyPerMinute, cfg.VerifyBurst = 30, 10
	}
	return &Rx{m: m, cfg: cfg, d: d, limiter: httpx.NewLimiter(cfg.VerifyPerMinute, cfg.VerifyBurst)}, nil
}

// ESignEnabled reports whether signing can issue prescriptions.
func (r *Rx) ESignEnabled() bool { return r.cfg.ESignEnabled }

// ---- helpers ----

type careKey = crypto.Sealed[crypto.CareKey]

func (r *Rx) seal(ctx context.Context, patient, doctor, table, col, row string, b []byte) (careKey, error) {
	return crypto.SealCare(ctx, r.d.Crypto, patient, doctor, crypto.AAD{Table: table, Column: col, RowID: row}, b)
}

func (r *Rx) open(ctx context.Context, table, col, row string, s careKey) ([]byte, error) {
	return crypto.Open(ctx, r.d.Crypto, crypto.AAD{Table: table, Column: col, RowID: row}, s)
}

func (r *Rx) now() time.Time { return r.d.Now().UTC().Truncate(time.Microsecond) }

func (c Caller) actor() Actor { return Actor{Ref: c.UserID, Role: c.Role} }

func (r *Rx) auditEvent(c Caller, action, subject, resType, resRef string, res ap.Result, reason string) ap.Event {
	return ap.Event{ActorRef: c.UserID, ActorRole: c.Role, Action: action, SubjectRef: subject, ResourceType: resType, ResourceRef: resRef,
		Result: res, ReasonCode: reason, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}
}

// auditAlone writes one audit event in its own transaction (refusals).
func (r *Rx) auditAlone(ctx context.Context, e ap.Event) {
	if err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { return ap.Record(ctx, tx, e) }); err != nil {
		r.d.Log.Error("clinical.audit_failed", logging.Err(err))
	}
}

// facts loads the appointment and checks that it belongs to the calling doctor. Anyone
// else's appointment looks missing (K-SEC-04).
func (r *Rx) facts(ctx context.Context, c Caller, appt string) (ConsultFacts, error) {
	if !ids.Valid(appt) || c.Role != "doctor" || !ids.Valid(c.UserID) {
		return ConsultFacts{}, ErrNotFound
	}
	f, err := r.d.Consults.Consult(ctx, appt)
	if errors.Is(err, ErrConsultNotFound) {
		return ConsultFacts{}, ErrNotFound
	}
	if err != nil {
		return ConsultFacts{}, fmt.Errorf("clinical: consult facts: %w", err)
	}
	if f.DoctorRef != c.UserID || f.AppointmentRef != appt || !ids.Valid(f.PatientRef) {
		return ConsultFacts{}, ErrNotFound
	}
	return f, nil
}

func (r *Rx) gate(ctx context.Context, c Caller, f ConsultFacts, kind rp.DataKind, resType, resRef string) error {
	d, err := r.d.Gate.Authorize(ctx, rp.Request{
		Actor:      rp.Actor{Ref: c.UserID, Role: c.Role, ClinicRef: c.ClinicRef, Restricted: c.Restricted},
		PatientRef: f.PatientRef, Kind: kind, BookingRef: f.AppointmentRef, OwnerRef: f.DoctorRef,
		ResourceType: resType, ResourceRef: resRef, RequestID: c.RequestID, Client: c.Client, IPHash: c.IPHash})
	if err != nil {
		return fmt.Errorf("clinical: authorize: %w", err) // fail closed
	}
	if !d.Allowed {
		return &DeniedError{Hide: d.Hide, Reason: d.Reason}
	}
	return nil
}

// authDoctor lets the doctor in with the current-care permission (T1). When allowOwn is
// true (the record already has a signed version, so this is an amendment) the doctor's
// own-records permission (T2) is accepted too.
func (r *Rx) authDoctor(ctx context.Context, c Caller, f ConsultFacts, resType, resRef string, allowOwn bool) error {
	err := r.gate(ctx, c, f, rp.KindClinicalCurrent, resType, resRef)
	var de *DeniedError
	if err != nil && allowOwn && errors.As(err, &de) {
		if err2 := r.gate(ctx, c, f, rp.KindClinicalOwn, resType, resRef); err2 == nil {
			return nil
		}
	}
	return err
}

func (r *Rx) hasSigned(ctx context.Context, f ConsultFacts) (bool, error) {
	var n int
	err := r.m.pool.QueryRow(ctx, `SELECT count(*) FROM prescription_version v JOIN prescription p ON p.id = v.prescription_id
		WHERE p.appointment_ref = $1 AND p.doctor_ref = $2 AND v.state = 'signed'`, f.AppointmentRef, f.DoctorRef).Scan(&n)
	return n > 0, err
}

func (r *Rx) authWrite(ctx context.Context, c Caller, f ConsultFacts, resType, resRef string) error {
	own, err := r.hasSigned(ctx, f)
	if err != nil {
		return err
	}
	return r.authDoctor(ctx, c, f, resType, resRef, own)
}

func stepUpFresh(c Caller, now time.Time) bool {
	return c.StepUpAt != nil && now.Sub(*c.StepUpAt) <= 5*time.Minute && !c.StepUpAt.After(now.Add(time.Minute))
}

// ---- notes (S-139) ----

// NoteView is a consult note version.
type NoteView struct {
	NoteRef        string
	AppointmentRef string
	Version        int
	State          string // draft | signed
	Content        NoteContent
	SignedAt       *time.Time
	UpdatedAt      time.Time
}

// SaveNote creates or replaces the draft note of an appointment. A signed version is never
// changed: saving after signing starts a new draft version (K-CS-05).
func (r *Rx) SaveNote(ctx context.Context, c Caller, appt string, n NoteContent) (NoteView, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return NoteView{}, err
	}
	if err := r.validateNote(n); err != nil {
		return NoteView{}, err
	}
	if err := r.authWrite(ctx, c, f, "note", appt); err != nil {
		return NoteView{}, err
	}
	raw, _ := json.Marshal(n)
	var out NoteView
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		noteID, err := r.noteID(ctx, tx, f, true)
		if err != nil {
			return err
		}
		now := r.now()
		var verID string
		var rev, version int
		err = tx.QueryRow(ctx, `SELECT id::text, rev, version FROM note_version WHERE note_id = $1 AND state = 'draft' FOR UPDATE`, noteID).Scan(&verID, &rev, &version)
		switch {
		case err == nil:
			ct, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "note_version", "content", verID, raw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE note_version SET content=$2, has_diagnosis=$3, rev=rev+1, updated_at=$4 WHERE id=$1 AND state='draft'`,
				verID, ct, n.Diagnosis != "", now); err != nil {
				return err
			}
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM note_version WHERE note_id = $1`, noteID).Scan(&version); err != nil {
				return err
			}
			verID = ids.New()
			ct, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "note_version", "content", verID, raw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO note_version (id, note_id, version, state, content, has_diagnosis, created_at, updated_at)
				VALUES ($1,$2,$3,'draft',$4,$5,$6,$6)`, verID, noteID, version, ct, n.Diagnosis != "", now); err != nil {
				if db.IsUniqueViolation(err, "") {
					return ErrOpenDraft
				}
				return err
			}
		default:
			return err
		}
		out = NoteView{NoteRef: noteID, AppointmentRef: appt, Version: version, State: "draft", Content: n, UpdatedAt: now}
		return ap.Record(ctx, tx, r.auditEvent(c, "clinical.note.save", f.PatientRef, "note", noteID, ap.Allowed, ""))
	})
	return out, err
}

// noteID returns the note header ID, creating it when create is true.
func (r *Rx) noteID(ctx context.Context, tx pgx.Tx, f ConsultFacts, create bool) (string, error) {
	var id string
	q := `SELECT id::text FROM note WHERE appointment_ref = $1 AND doctor_ref = $2`
	err := tx.QueryRow(ctx, q, f.AppointmentRef, f.DoctorRef).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && create {
		if _, err := tx.Exec(ctx, `INSERT INTO note (id, appointment_ref, patient_ref, doctor_ref, clinic_ref) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (appointment_ref, doctor_ref) DO NOTHING`, ids.New(), f.AppointmentRef, f.PatientRef, f.DoctorRef, nullUUID(f.ClinicRef)); err != nil {
			return "", err
		}
		err = tx.QueryRow(ctx, q, f.AppointmentRef, f.DoctorRef).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type noteRow struct {
	NoteView
	versionID    string
	rev          int
	hasDiagnosis bool
}

// latestNote returns the newest note version (draft first), or ErrNotFound.
func (r *Rx) latestNote(ctx context.Context, f ConsultFacts, signedOnly bool) (noteRow, error) {
	var nr noteRow
	var ct careKey
	q := `SELECT n.id::text, v.id::text, v.version, v.state, v.content, v.signed_at, v.updated_at, v.rev, v.has_diagnosis
		FROM note n JOIN note_version v ON v.note_id = n.id WHERE n.appointment_ref = $1 AND n.doctor_ref = $2`
	if signedOnly {
		q += ` AND v.state = 'signed'`
	}
	q += ` ORDER BY v.version DESC LIMIT 1`
	err := r.m.pool.QueryRow(ctx, q, f.AppointmentRef, f.DoctorRef).Scan(&nr.NoteRef, &nr.versionID, &nr.Version, &nr.State, &ct, &nr.SignedAt, &nr.UpdatedAt, &nr.rev, &nr.hasDiagnosis)
	if errors.Is(err, pgx.ErrNoRows) {
		return noteRow{}, ErrNotFound
	}
	if err != nil {
		return noteRow{}, err
	}
	raw, err := r.open(ctx, "note_version", "content", nr.versionID, ct)
	if err != nil {
		return noteRow{}, fmt.Errorf("clinical: open note: %w", err)
	}
	if err := json.Unmarshal(raw, &nr.Content); err != nil {
		return noteRow{}, err
	}
	nr.AppointmentRef = f.AppointmentRef
	return nr, nil
}

// GetNote returns the newest version of the doctor's note for an appointment, through the gate.
func (r *Rx) GetNote(ctx context.Context, c Caller, appt string) (NoteView, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return NoteView{}, err
	}
	if err := r.authWrite(ctx, c, f, "note", appt); err != nil {
		return NoteView{}, err
	}
	nr, err := r.latestNote(ctx, f, false)
	return nr.NoteView, err
}

// ---- prescription drafts (S-149) ----

// RxView is a prescription version.
type RxView struct {
	PrescriptionRef       string
	AppointmentRef        string
	Version               int
	State                 string // draft | signed
	Content               DraftContent
	SignedAt              *time.Time
	ConsultMode           string
	RuleSetVersion        string
	ClassificationVersion string
	// Document is the canonical signed document (JSON), present on signed versions.
	Document  json.RawMessage
	UpdatedAt time.Time
}

type rxRow struct {
	RxView
	versionID string
	rev       int
	patient   string
	doctor    string
}

// loadRx returns the newest version of a doctor's prescription for an appointment.
func (r *Rx) loadRx(ctx context.Context, appt, doctor string, draftOnly bool) (rxRow, error) {
	q := `SELECT p.id::text, p.patient_ref::text, p.doctor_ref::text, v.id::text, v.version, v.state, v.content, v.rev, v.signed_at,
		coalesce(v.consult_mode,''), coalesce(v.ruleset_version,''), coalesce(v.classification_version,''), v.document, v.updated_at
		FROM prescription p JOIN prescription_version v ON v.prescription_id = p.id WHERE p.appointment_ref = $1 AND p.doctor_ref = $2`
	if draftOnly {
		q += ` AND v.state = 'draft'`
	}
	q += ` ORDER BY v.version DESC LIMIT 1`
	var row rxRow
	var ct, doc careKey
	err := r.m.pool.QueryRow(ctx, q, appt, doctor).Scan(&row.PrescriptionRef, &row.patient, &row.doctor, &row.versionID, &row.Version, &row.State, &ct, &row.rev,
		&row.SignedAt, &row.ConsultMode, &row.RuleSetVersion, &row.ClassificationVersion, &doc, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rxRow{}, ErrNotFound
	}
	if err != nil {
		return rxRow{}, err
	}
	if err := r.fill(ctx, &row, ct, doc); err != nil {
		return rxRow{}, err
	}
	row.AppointmentRef = appt
	return row, nil
}

func (r *Rx) fill(ctx context.Context, row *rxRow, ct, doc careKey) error {
	raw, err := r.open(ctx, "prescription_version", "content", row.versionID, ct)
	if err != nil {
		return fmt.Errorf("clinical: open prescription: %w", err)
	}
	if err := json.Unmarshal(raw, &row.Content); err != nil {
		return err
	}
	if !doc.IsZero() {
		b, err := r.open(ctx, "prescription_version", "document", row.versionID, doc)
		if err != nil {
			return fmt.Errorf("clinical: open document: %w", err)
		}
		row.Document = b
	}
	return nil
}

// SaveDraft replaces the draft prescription (items exactly as entered, K-CS-03). Nothing
// is suggested, completed or corrected. If a signed version exists and no draft is open,
// the doctor must amend first.
func (r *Rx) SaveDraft(ctx context.Context, c Caller, appt string, d DraftContent) (RxView, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return RxView{}, err
	}
	if err := r.validateDraft(&d); err != nil {
		return RxView{}, err
	}
	if err := r.authWrite(ctx, c, f, "prescription", appt); err != nil {
		return RxView{}, err
	}
	raw, _ := json.Marshal(d)
	var out RxView
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var pid string
		err := tx.QueryRow(ctx, `SELECT id::text FROM prescription WHERE appointment_ref=$1 AND doctor_ref=$2`, f.AppointmentRef, f.DoctorRef).Scan(&pid)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `INSERT INTO prescription (id, appointment_ref, patient_ref, doctor_ref, clinic_ref) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (appointment_ref, doctor_ref) DO NOTHING`, ids.New(), f.AppointmentRef, f.PatientRef, f.DoctorRef, nullUUID(f.ClinicRef)); err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `SELECT id::text FROM prescription WHERE appointment_ref=$1 AND doctor_ref=$2`, f.AppointmentRef, f.DoctorRef).Scan(&pid)
		}
		if err != nil {
			return err
		}
		now := r.now()
		var verID string
		var version int
		err = tx.QueryRow(ctx, `SELECT id::text, version FROM prescription_version WHERE prescription_id=$1 AND state='draft' FOR UPDATE`, pid).Scan(&verID, &version)
		switch {
		case err == nil:
			ct, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "content", verID, raw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE prescription_version SET content=$2, rev=rev+1, updated_at=$3 WHERE id=$1 AND state='draft'`, verID, ct, now); err != nil {
				return err
			}
		case errors.Is(err, pgx.ErrNoRows):
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM prescription_version WHERE prescription_id=$1`, pid).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrAlreadySigned // amend to start a new version
			}
			version, verID = 1, ids.New()
			ct, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "content", verID, raw)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO prescription_version (id, prescription_id, version, state, content, created_at, updated_at)
				VALUES ($1,$2,1,'draft',$3,$4,$4)`, verID, pid, ct, now); err != nil {
				if db.IsUniqueViolation(err, "") {
					return ErrOpenDraft
				}
				return err
			}
		default:
			return err
		}
		out = RxView{PrescriptionRef: pid, AppointmentRef: appt, Version: version, State: "draft", Content: d, UpdatedAt: now}
		return ap.Record(ctx, tx, r.auditEvent(c, "clinical.prescription.save", f.PatientRef, "prescription", pid, ap.Allowed, ""))
	})
	return out, err
}

// GetPrescription returns the doctor's newest prescription version for an appointment.
func (r *Rx) GetPrescription(ctx context.Context, c Caller, appt string) (RxView, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return RxView{}, err
	}
	if err := r.authWrite(ctx, c, f, "prescription", appt); err != nil {
		return RxView{}, err
	}
	row, err := r.loadRx(ctx, appt, f.DoctorRef, false)
	return row.RxView, err
}

// Amend opens a new draft version copied from the latest signed version (S-164). The old
// version stays immutable (K-CS-05). Signing the new version cancels the old reminders,
// creates new ones and notifies the patient.
func (r *Rx) Amend(ctx context.Context, c Caller, prescriptionRef string) (RxView, error) {
	if !ids.Valid(prescriptionRef) || c.Role != "doctor" {
		return RxView{}, ErrNotFound
	}
	var appt string
	err := r.m.pool.QueryRow(ctx, `SELECT appointment_ref::text FROM prescription WHERE id = $1 AND doctor_ref = $2`, prescriptionRef, c.UserID).Scan(&appt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RxView{}, ErrNotFound
	}
	if err != nil {
		return RxView{}, err
	}
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return RxView{}, err
	}
	if err := r.authDoctor(ctx, c, f, "prescription", prescriptionRef, true); err != nil {
		return RxView{}, err
	}
	var out RxView
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM prescription_version WHERE prescription_id=$1 AND state='draft'`, prescriptionRef).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return ErrOpenDraft
		}
		var last int
		var lastID string
		var ct careKey
		err := tx.QueryRow(ctx, `SELECT id::text, version, content FROM prescription_version WHERE prescription_id=$1 AND state='signed' ORDER BY version DESC LIMIT 1`, prescriptionRef).Scan(&lastID, &last, &ct)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNothingToAmend
		}
		if err != nil {
			return err
		}
		raw, err := r.open(ctx, "prescription_version", "content", lastID, ct)
		if err != nil {
			return err
		}
		verID, now := ids.New(), r.now()
		nct, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "content", verID, raw)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO prescription_version (id, prescription_id, version, state, content, created_at, updated_at) VALUES ($1,$2,$3,'draft',$4,$5,$5)`,
			verID, prescriptionRef, last+1, nct, now); err != nil {
			if db.IsUniqueViolation(err, "") {
				return ErrOpenDraft
			}
			return err
		}
		var d DraftContent
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		out = RxView{PrescriptionRef: prescriptionRef, AppointmentRef: appt, Version: last + 1, State: "draft", Content: d, UpdatedAt: now}
		return ap.Record(ctx, tx, r.auditEvent(c, "clinical.prescription.amend", f.PatientRef, "prescription", prescriptionRef, ap.Allowed, ""))
	})
	return out, err
}

// ---- rules input ----

func (r *Rx) priorSet(f ConsultFacts) map[string]bool {
	m := map[string]bool{}
	for _, d := range f.PriorDrugRefs {
		m[d] = true
	}
	return m
}

func (r *Rx) input(f ConsultFacts, d DraftContent, cls rules.Classifier, stage rules.Stage, hasDiagnosis, verified, stepUp, esigned bool) rules.Input {
	return rules.Input{
		RuleSetVersion: r.m.engine.Version(), Stage: stage, Mode: f.Mode, ConsultAt: f.ConsultAt, PatientTimeZone: f.PatientTimeZone,
		FollowUp: f.FollowUp, PatientCountry: f.PatientCountry, Initiator: f.Initiator, ExplicitConsent: f.ExplicitConsent,
		PrescriberVerified: verified, IdentityConfirmed: f.IdentityConfirmed, PatientAgeYears: f.PatientAgeYears, DiagnosisPresent: hasDiagnosis,
		StepUpDone: stepUp, ESignatureApplied: esigned, Classification: cls, Items: toRules(d.Items, r.priorSet(f)),
	}
}

// CheckView is the per-item result of a draft check. It never carries a suggestion.
type CheckView struct{ Result rules.Result }

// Check runs the rules engine on the draft at the draft stage and audits the evaluation.
func (r *Rx) Check(ctx context.Context, c Caller, appt string) (CheckView, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return CheckView{}, err
	}
	if err := r.authWrite(ctx, c, f, "prescription", appt); err != nil {
		return CheckView{}, err
	}
	row, err := r.loadRx(ctx, appt, f.DoctorRef, true)
	if err != nil {
		return CheckView{}, err
	}
	cls, err := r.m.LoadClassification(ctx, r.cfg.ClassificationVersion)
	if err != nil {
		return CheckView{}, err
	}
	hasDx := false
	if nr, err := r.latestNote(ctx, f, false); err == nil {
		hasDx = nr.hasDiagnosis
	}
	verified, verr := r.d.Verifier.IsVerified(ctx, c.UserID)
	verified = verified && verr == nil
	res := r.m.engine.Check(r.input(f, row.Content, cls, rules.StageDraft, hasDx, verified, stepUpFresh(c, r.now()), false))
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return r.m.RecordCheck(ctx, tx, c.actor(), f.PatientRef, row.PrescriptionRef, res)
	})
	return CheckView{Result: res}, err
}

// ---- patient reads (S-151) ----

// Summary is one line of a patient's list.
type Summary struct {
	PrescriptionRef string
	AppointmentRef  string
	Version         int
	SignedAt        time.Time
}

func (r *Rx) selfGate(ctx context.Context, c Caller, resType, resRef string) error {
	if c.Role != "patient" || !ids.Valid(c.UserID) {
		return ErrNotFound
	}
	d, err := r.d.Gate.Authorize(ctx, rp.Request{Actor: rp.Actor{Ref: c.UserID, Role: c.Role, Restricted: c.Restricted}, PatientRef: c.UserID,
		Kind: rp.KindClinicalOwn, ResourceType: resType, ResourceRef: resRef, RequestID: c.RequestID, Client: c.Client, IPHash: c.IPHash})
	if err != nil {
		return fmt.Errorf("clinical: authorize: %w", err)
	}
	if !d.Allowed {
		return &DeniedError{Hide: d.Hide, Reason: d.Reason}
	}
	return nil
}

// PatientPrescriptions lists the caller's signed prescriptions, newest first (cursor = last ref).
func (r *Rx) PatientPrescriptions(ctx context.Context, c Caller, limit int, cursor string) ([]Summary, string, error) {
	if err := r.selfGate(ctx, c, "prescription", ""); err != nil {
		return nil, "", err
	}
	limit = min(max(limit, 1), 100)
	if cursor != "" && !ids.Valid(cursor) {
		return nil, "", &InvalidError{Field: "cursor", Code: "invalid"}
	}
	rows, err := r.m.pool.Query(ctx, `SELECT p.id::text, p.appointment_ref::text, v.version, v.signed_at
		FROM prescription p JOIN LATERAL (SELECT version, signed_at FROM prescription_version WHERE prescription_id = p.id AND state='signed' ORDER BY version DESC LIMIT 1) v ON true
		WHERE p.patient_ref = $1 AND ($2 = '' OR p.id < $2::uuid) ORDER BY p.id DESC LIMIT $3`, c.UserID, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.PrescriptionRef, &s.AppointmentRef, &s.Version, &s.SignedAt); err != nil {
			return nil, "", err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].PrescriptionRef
	}
	return out, next, nil
}

// PatientPrescription returns one of the caller's signed prescription versions (0 = latest).
// Someone else's prescription looks missing (K-SEC-04).
func (r *Rx) PatientPrescription(ctx context.Context, c Caller, prescriptionRef string, version int) (RxView, error) {
	if err := r.selfGate(ctx, c, "prescription", ""); err != nil {
		return RxView{}, err
	}
	if !ids.Valid(prescriptionRef) {
		return RxView{}, ErrNotFound
	}
	var row rxRow
	var ct, doc careKey
	err := r.m.pool.QueryRow(ctx, `SELECT p.id::text, p.appointment_ref::text, p.patient_ref::text, p.doctor_ref::text, v.id::text, v.version, v.state, v.content, v.rev, v.signed_at,
		v.consult_mode, v.ruleset_version, v.classification_version, v.document, v.updated_at
		FROM prescription p JOIN prescription_version v ON v.prescription_id = p.id
		WHERE p.id = $1 AND p.patient_ref = $2 AND v.state = 'signed' AND ($3 = 0 OR v.version = $3) ORDER BY v.version DESC LIMIT 1`,
		prescriptionRef, c.UserID, version).Scan(&row.PrescriptionRef, &row.AppointmentRef, &row.patient, &row.doctor, &row.versionID, &row.Version, &row.State, &ct, &row.rev,
		&row.SignedAt, &row.ConsultMode, &row.RuleSetVersion, &row.ClassificationVersion, &doc, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RxView{}, ErrNotFound
	}
	if err != nil {
		return RxView{}, err
	}
	if err := r.fill(ctx, &row, ct, doc); err != nil {
		return RxView{}, err
	}
	return row.RxView, nil
}

// PatientNote returns the latest signed note version of an appointment of the caller.
func (r *Rx) PatientNote(ctx context.Context, c Caller, appt string) (NoteView, error) {
	if err := r.selfGate(ctx, c, "note", ""); err != nil {
		return NoteView{}, err
	}
	if !ids.Valid(appt) {
		return NoteView{}, ErrNotFound
	}
	var nr noteRow
	var ct careKey
	err := r.m.pool.QueryRow(ctx, `SELECT n.id::text, v.id::text, v.version, v.state, v.content, v.signed_at, v.updated_at
		FROM note n JOIN note_version v ON v.note_id = n.id WHERE n.appointment_ref = $1 AND n.patient_ref = $2 AND v.state = 'signed'
		ORDER BY v.version DESC LIMIT 1`, appt, c.UserID).Scan(&nr.NoteRef, &nr.versionID, &nr.Version, &nr.State, &ct, &nr.SignedAt, &nr.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NoteView{}, ErrNotFound
	}
	if err != nil {
		return NoteView{}, err
	}
	raw, err := r.open(ctx, "note_version", "content", nr.versionID, ct)
	if err != nil {
		return NoteView{}, fmt.Errorf("clinical: open note: %w", err)
	}
	if err := json.Unmarshal(raw, &nr.Content); err != nil {
		return NoteView{}, err
	}
	nr.AppointmentRef = appt
	return nr.NoteView, nil
}
