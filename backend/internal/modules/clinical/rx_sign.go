package clinical

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Event names and payload keys (module-boundaries §4). References and codes only.
const (
	EventSigned  = "prescription.signed"
	EventAmended = "prescription.amended"
	// EventRemindersChanged says the patient's reminder activities were created or cancelled, so their devices
	// refresh the on-device backup schedule (S-165). Subject only, no payload.
	EventRemindersChanged = "reminders.changed"
	PayloadPrescrip       = "prescriptionRef"
	PayloadVersion        = "version"
	verificationTokenN    = 32 // bytes: 256 bits, at least the 128 required by review R-09
)

// Document is the canonical prescription (spec section 8). It is serialised with
// encoding/json from fixed-order structs, so the bytes are reproducible; the SHA-256 of
// those bytes is the only thing sent to the e-signature provider. A PDF renderer is an
// open item: it must print exactly these fields.
type Document struct {
	Schema                int        `json:"schema"`
	PrescriptionRef       string     `json:"prescriptionRef"`
	Version               int        `json:"version"`
	IssuedAt              string     `json:"issuedAt"`
	ConsultMode           string     `json:"consultMode"`
	Doctor                DocDoctor  `json:"doctor"`
	Patient               DocPatient `json:"patient"`
	Complaint             string     `json:"complaint"`
	Findings              string     `json:"findings"`
	Diagnosis             string     `json:"diagnosis"`
	Investigations        []DocTest  `json:"investigations"`
	Items                 []DocItem  `json:"items"`
	Advice                string     `json:"advice"`
	NoteAdvice            string     `json:"noteAdvice"`
	FollowUpOn            string     `json:"followUpOn"`
	RuleSetVersion        string     `json:"ruleSetVersion"`
	ClassificationVersion string     `json:"classificationVersion"`
	VerificationReference string     `json:"verificationReference"`
}

// DocDoctor is the prescriber block.
type DocDoctor struct {
	Name               string `json:"name"`
	Qualification      string `json:"qualification"`
	RegistrationNumber string `json:"registrationNumber"`
	Council            string `json:"council"`
}

// DocPatient is the patient block.
type DocPatient struct {
	Name     string `json:"name"`
	AgeYears int    `json:"ageYears"`
	Sex      string `json:"sex"`
}

// DocTest is a test or scan.
type DocTest struct {
	Name         string `json:"name"`
	DueOn        string `json:"dueOn,omitempty"`
	IntervalDays int    `json:"intervalDays,omitempty"`
	Repeats      int    `json:"repeats,omitempty"`
}

// DocItem is a prescription line: the drug identity comes from the drug master, everything
// else exactly as the doctor entered it.
type DocItem struct {
	Ref          string           `json:"ref"`
	DrugRef      string           `json:"drugRef"`
	GenericName  string           `json:"genericName"`
	Strength     string           `json:"strength"`
	Form         string           `json:"form"`
	Dose         DoseInput        `json:"dose"`
	Frequency    FrequencyInput   `json:"frequency"`
	Duration     DurationInput    `json:"duration"`
	Route        string           `json:"route"`
	AsNeeded     bool             `json:"asNeeded"`
	Taper        []TaperStepInput `json:"taper"`
	Instructions string           `json:"instructions"`
}

type storedSignature struct {
	Value       []byte    `json:"value"`
	Certificate []byte    `json:"certificate"`
	SignedAt    time.Time `json:"signedAt"`
	Provider    string    `json:"provider"`
}

// SignResult is what the doctor gets back from a successful signing.
type SignResult struct {
	PrescriptionRef       string
	Version               int
	SignedAt              time.Time
	VerificationReference string
	DocumentSHA256        string
}

func (r *Rx) drugDetails(ctx context.Context, version string, refs []string) (map[string][3]string, error) {
	rows, err := r.m.pool.Query(ctx, `SELECT e.drug_ref, e.generic_name, e.strength, e.form FROM drug_master_entry e
		JOIN drug_classification_version v ON v.id = e.classification_id WHERE v.version = $1 AND e.drug_ref = ANY($2)`, version, refs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][3]string{}
	for rows.Next() {
		var ref, g, s, f string
		if err := rows.Scan(&ref, &g, &s, &f); err != nil {
			return nil, err
		}
		out[ref] = [3]string{g, s, f}
	}
	return out, rows.Err()
}

func (r *Rx) buildDocument(ctx context.Context, f ConsultFacts, row rxRow, note NoteContent, signedAt time.Time, token string) ([]byte, error) {
	refs := make([]string, 0, len(row.Content.Items))
	for _, it := range row.Content.Items {
		refs = append(refs, it.DrugRef)
	}
	dd, err := r.drugDetails(ctx, r.cfg.ClassificationVersion, refs)
	if err != nil {
		return nil, err
	}
	doc := Document{Schema: 1, PrescriptionRef: row.PrescriptionRef, Version: row.Version, IssuedAt: signedAt.UTC().Format(time.RFC3339Nano),
		ConsultMode: string(f.Mode), Doctor: DocDoctor(f.Doctor), Patient: DocPatient{Name: f.PatientName, AgeYears: f.PatientAgeYears, Sex: f.PatientSex},
		Complaint: note.Complaint, Findings: note.Findings, Diagnosis: note.Diagnosis, Advice: row.Content.Advice, NoteAdvice: note.Advice,
		FollowUpOn: row.Content.FollowUpOn, RuleSetVersion: r.m.engine.Version(), ClassificationVersion: r.cfg.ClassificationVersion,
		VerificationReference: token, Investigations: []DocTest{}, Items: []DocItem{}}
	for _, t := range row.Content.Tests {
		doc.Investigations = append(doc.Investigations, DocTest{Name: t.Name, DueOn: t.DueOn, IntervalDays: t.IntervalDays, Repeats: t.Repeats})
	}
	for _, it := range row.Content.Items {
		d, ok := dd[it.DrugRef]
		if !ok {
			return nil, fmt.Errorf("clinical: drug missing from classification %s", r.cfg.ClassificationVersion)
		}
		taper := it.Taper
		if taper == nil {
			taper = []TaperStepInput{}
		}
		doc.Items = append(doc.Items, DocItem{Ref: it.Ref, DrugRef: it.DrugRef, GenericName: d[0], Strength: d[1], Form: d[2], Dose: it.Dose,
			Frequency: it.Frequency, Duration: it.Duration, Route: it.Route, AsNeeded: it.AsNeeded, Taper: taper, Instructions: it.Instructions})
	}
	return json.Marshal(doc)
}

func codeOf(res rules.Result) string {
	if len(res.Reasons) > 0 {
		return res.Reasons[0].Code
	}
	for _, it := range res.Items {
		if len(it.Reasons) > 0 {
			return it.Reasons[0].Code
		}
	}
	return "blocked"
}

// Sign issues the prescription. Everything is checked on the server, in this order, and
// every refusal is audited; nothing is issued unless every step passes (fail closed):
//  1. the appointment is the caller's and the records gate allows current care (T1);
//  2. the doctor is Verified (K-CS-01);
//  3. a step-up happened within 5 minutes (P08);
//  4. the rules engine passes at the sign stage with the consult mode at signing and the
//     patient's in-India declaration (K-CS-02, P10);
//  5. the electronic signature adapter signs the document hash (P09); until N5 the module
//     is configured without one and this step refuses.
func (r *Rx) Sign(ctx context.Context, c Caller, appt string) (SignResult, error) {
	f, err := r.facts(ctx, c, appt)
	if err != nil {
		return SignResult{}, err
	}
	refuse := func(code string, err error) (SignResult, error) {
		r.auditAlone(ctx, r.auditEvent(c, "clinical.prescription.sign", f.PatientRef, "prescription", appt, ap.Denied, code))
		return SignResult{}, err
	}
	if err := r.authWrite(ctx, c, f, "prescription", appt); err != nil {
		return SignResult{}, err
	}
	verified, verr := r.d.Verifier.IsVerified(ctx, c.UserID)
	if verr != nil || !verified {
		return refuse("RX_DOCTOR_NOT_VERIFIED", ErrDoctorNotVerified)
	}
	// The prescription format needs the doctor's reviewed registration details and the
	// patient's name and age. A document is never signed with any of them missing.
	if f.Doctor.Name == "" || f.Doctor.RegistrationNumber == "" || f.Doctor.Council == "" {
		return refuse("RX_DOCTOR_NOT_VERIFIED", ErrDoctorNotVerified)
	}
	if f.PatientName == "" || f.PatientAgeYears <= 0 {
		return refuse("patient_details_missing", ErrPatientDetails)
	}
	now := r.now()
	if !stepUpFresh(c, now) {
		return refuse("RX_SIGNATURE_REQUIRED", ErrStepUp)
	}

	row, err := r.loadRx(ctx, appt, f.DoctorRef, false)
	if errors.Is(err, ErrNotFound) {
		return refuse("no_draft", ErrNoDraft)
	}
	if err != nil {
		return SignResult{}, err
	}
	if row.State != "draft" {
		return refuse("already_signed", ErrAlreadySigned)
	}
	var note noteRow
	if note, err = r.latestNote(ctx, f, false); err != nil && !errors.Is(err, ErrNotFound) {
		return SignResult{}, err
	}
	cls, err := r.m.LoadClassification(ctx, r.cfg.ClassificationVersion)
	if err != nil {
		return SignResult{}, err
	}
	hasDx := note.versionID != "" && note.hasDiagnosis

	// Rules at the sign stage. The e-signature does not exist yet, so P09 is evaluated as
	// "will be applied"; it is evaluated again for real once the signature is in hand.
	pre := r.m.engine.Check(r.input(f, row.Content, cls, rules.StageSign, hasDx, true, true, true))
	if !pre.Allowed {
		r.recordCheckAlone(ctx, c, f, row.PrescriptionRef, pre)
		return refuse(codeOf(pre), &BlockedError{Result: pre})
	}
	if !r.cfg.ESignEnabled || r.d.Signer == nil {
		return refuse("esign_unavailable", ErrESignUnavailable)
	}

	token, err := ids.Token(verificationTokenN)
	if err != nil {
		return SignResult{}, err
	}
	docBytes, err := r.buildDocument(ctx, f, row, note.Content, now, token)
	if err != nil {
		return SignResult{}, err
	}
	sum := sha256.Sum256(docBytes)
	sess, err := r.d.Signer.StartSigning(ctx, esign.StartRequest{SignerRef: c.UserID, DocumentHash: sum[:], Summary: "prescription"})
	if err != nil {
		r.d.Log.Warn("clinical.esign_failed", "op", "start", logging.Err(err))
		return refuse("esign_failed", ErrESignFailed)
	}
	sig, err := r.d.Signer.CompleteSigning(ctx, sess.ID)
	if err != nil {
		r.d.Log.Warn("clinical.esign_failed", "op", "complete", logging.Err(err))
		return refuse("esign_failed", ErrESignFailed)
	}
	if v, err := r.d.Signer.Verify(ctx, sum[:], sig); err != nil || v != esign.Valid {
		return refuse("esign_failed", ErrESignFailed)
	}

	// The real, final evaluation: the signature is applied.
	final := r.m.engine.Check(r.input(f, row.Content, cls, rules.StageSign, hasDx, true, true, true))
	if !final.Allowed {
		r.recordCheckAlone(ctx, c, f, row.PrescriptionRef, final)
		return refuse(codeOf(final), &BlockedError{Result: final})
	}

	ruleRaw, _ := json.Marshal(final)
	sigRaw, _ := json.Marshal(storedSignature{Value: sig.Value, Certificate: sig.Certificate, SignedAt: sig.SignedAt.UTC(), Provider: sig.Provider})
	docSealed, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "document", row.versionID, docBytes)
	if err != nil {
		return SignResult{}, err
	}
	ruleSealed, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "rule_result", row.versionID, ruleRaw)
	if err != nil {
		return SignResult{}, err
	}
	sigSealed, err := r.seal(ctx, f.PatientRef, f.DoctorRef, "prescription_version", "signature", row.versionID, sigRaw)
	if err != nil {
		return SignResult{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	event := EventSigned
	if row.Version > 1 {
		event = EventAmended
	}

	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var state string
		var rev int
		if err := tx.QueryRow(ctx, `SELECT state, rev FROM prescription_version WHERE id = $1 FOR UPDATE`, row.versionID).Scan(&state, &rev); err != nil {
			return err
		}
		if state != "draft" {
			return ErrAlreadySigned
		}
		if rev != row.rev {
			return ErrDraftChanged // the draft was edited while signing: the signed document would differ
		}
		noteVersionID := note.versionID
		if note.versionID == "" {
			return rulesNoteMissing
		}
		var nrev int
		var nstate string
		if err := tx.QueryRow(ctx, `SELECT state, rev FROM note_version WHERE id = $1 FOR UPDATE`, noteVersionID).Scan(&nstate, &nrev); err != nil {
			return err
		}
		if nrev != note.rev || nstate != note.State {
			return ErrDraftChanged
		}
		if nstate == "draft" {
			if _, err := tx.Exec(ctx, `UPDATE note_version SET state='signed', signed_at=$2, updated_at=$2 WHERE id=$1 AND state='draft'`, noteVersionID, now); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE prescription_version SET state='signed', signed_at=$2, signed_by=$3, updated_at=$2, note_version_id=$4,
			consult_mode=$5, ruleset_version=$6, ruleset_checksum=$7, classification_version=$8, rule_result=$9, document=$10, document_sha256=$11,
			signature=$12, signing_provider=$13, verification_hash=$14 WHERE id=$1 AND state='draft'`,
			row.versionID, now, c.UserID, noteVersionID, string(f.Mode), final.RuleSetVersion, final.RuleSetChecksum, final.DrugClassificationVersion,
			ruleSealed, docSealed, sum[:], sigSealed, sig.Provider, tokenHash[:])
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrAlreadySigned
		}
		if err := r.m.RecordCheck(ctx, tx, c.actor(), f.PatientRef, row.PrescriptionRef, final); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, r.auditEvent(c, "clinical.prescription.sign", f.PatientRef, "prescription", row.PrescriptionRef, ap.Allowed, "")); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: event, Producer: "clinical", SubjectRef: f.PatientRef,
			Payload: map[string]string{PayloadPrescrip: row.PrescriptionRef, PayloadVersion: fmt.Sprint(row.Version)}})
	})
	if err != nil {
		if errors.Is(err, ErrAlreadySigned) || errors.Is(err, ErrDraftChanged) || errors.Is(err, rulesNoteMissing) {
			return refuse("sign_conflict", err)
		}
		return SignResult{}, err
	}
	// T2: the treating doctor's own-records permission. The event handler repeats this
	// (idempotent), so a failure here is retried.
	if _, gerr := r.d.Granter.GrantT2(ctx, f.PatientRef, f.DoctorRef, f.ClinicRef); gerr != nil {
		r.d.Log.Error("clinical.t2_grant_failed", logging.Err(gerr))
	}
	r.volumeAlert(ctx, c, now)
	return SignResult{PrescriptionRef: row.PrescriptionRef, Version: row.Version, SignedAt: now, VerificationReference: token,
		DocumentSHA256: fmt.Sprintf("%x", sum[:])}, nil
}

var rulesNoteMissing = errors.New("clinical: a note with a diagnosis is required")

func (r *Rx) recordCheckAlone(ctx context.Context, c Caller, f ConsultFacts, rxRef string, res rules.Result) {
	if err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return r.m.RecordCheck(ctx, tx, c.actor(), f.PatientRef, rxRef, res)
	}); err != nil {
		r.d.Log.Error("clinical.audit_failed", logging.Err(err))
	}
}

// volumeAlert raises one alert when the doctor's signed prescriptions in the last 24 hours reach the threshold
// (TM-04): an error log for the alerting pipeline and an audit event, references only. It never blocks signing:
// a busy clinic is not an attack, and security decides.
func (r *Rx) volumeAlert(ctx context.Context, c Caller, now time.Time) {
	limit := r.cfg.VolumeAlertPerDay
	if limit <= 0 {
		return
	}
	var n int
	if err := r.m.pool.QueryRow(ctx, `SELECT count(*) FROM prescription_version WHERE signed_by=$1 AND state='signed' AND signed_at > $2`,
		c.UserID, now.Add(-24*time.Hour)).Scan(&n); err != nil {
		r.d.Log.Error("clinical.volume_check_failed", logging.Err(err))
		return
	}
	if n != limit {
		return // alert once, at the crossing
	}
	r.d.Log.Error("clinical.alert_prescription_volume", "actor_ref", c.UserID, "count", n)
	if err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserID, ActorRole: "system", Action: "clinical.alert_prescription_volume", SubjectRef: c.UserID,
			ResourceType: "doctor", ResourceRef: c.UserID, ReasonCode: "volume_threshold", Result: ap.Allowed})
	}); err != nil {
		r.d.Log.Error("clinical.audit_failed", logging.Err(err))
	}
}
