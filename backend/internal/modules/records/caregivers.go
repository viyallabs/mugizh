package records

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Caregiver access, phase 1 (v1.1; purpose P-CAREGIVER; E-FAM-02, TC-47, CH-13). The patient invites a person with a
// one-time code; the person accepts it from their own patient-app session. The link is view-only and holds the
// scopes the patient chose. Reads by the caregiver go through Authorize (decideCaregiver), which checks the active
// link, the scope and the patient's P-CAREGIVER consent on every call.

const (
	purposeCaregiver = "P-CAREGIVER"
	// Limits [CONFIRM]: how many caregivers one patient has, how many patients one caregiver cares for, how many wrong
	// codes one caregiver may try in an hour, and how long an invitation lives.
	maxCaregiversPerPatient = 5
	maxPatientsPerCaregiver = 5
	maxWrongCodes           = 5
	wrongCodeWindow         = time.Hour
	invitationTTL           = 48 * time.Hour // [CONFIRM]
	invitationKeep          = 30 * 24 * time.Hour
	maxWrongCodesPerNetwork = 20
	labelMax                = 40
	// codeAlphabet is Crockford base32 (no I, L, O, U): 32 symbols, so 8 characters carry 40 bits.
	codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	codeLength   = 8
	// Advisory-lock classes (the second argument of hashtextextended).
	lockPatient   = 12
	lockCaregiver = 13
	lockNetwork   = 14
)

var scopeOrder = []string{rp.ScopeReminders, rp.ScopeAppointments, rp.ScopePrescriptions, rp.ScopeDocuments}

// normalizeScopes returns the scopes de-duplicated, in a fixed order. Unknown scopes are refused.
func normalizeScopes(in []string) ([]string, error) {
	for _, s := range in {
		if !slices.Contains(scopeOrder, s) {
			return nil, fmt.Errorf("%w: scope", rp.ErrInvalid)
		}
	}
	var out []string
	for _, s := range scopeOrder {
		if slices.Contains(in, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *Rights) caregiversOn() error {
	if !r.m.CaregiversEnabled() {
		return rp.ErrNotFound
	}
	return nil
}

func (r *Rights) codeHash(code string) []byte {
	return r.d.Crypto.Keyed("records.caregiver_code", code)
}

// newCode returns 8 random characters from the Crockford alphabet (40 bits). 256 is a multiple of 32, so the
// reduction is unbiased.
func newCode() (string, error) {
	b := make([]byte, codeLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, codeLength)
	for i, v := range b {
		out[i] = codeAlphabet[int(v)%len(codeAlphabet)]
	}
	return string(out), nil
}

// normalizeCode makes a typed code comparable: upper case, no spaces or hyphens, Crockford look-alikes folded
// (O to 0, I and L to 1). It reports false when the result cannot be a code.
func normalizeCode(s string) (string, bool) {
	var sb strings.Builder
	for _, c := range strings.ToUpper(s) {
		switch c {
		case ' ', '-':
			continue
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		if !strings.ContainsRune(codeAlphabet, c) {
			return "", false
		}
		sb.WriteRune(c)
	}
	return sb.String(), sb.Len() == codeLength
}

func lock(ctx context.Context, tx pgx.Tx, class int, ref string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, ref, class)
	return err
}

func (r *Rights) stepUpFor(c rp.Caller) error {
	if !r.stepFresh(c) {
		return rp.ErrStepUpRequired
	}
	return nil
}

// checkLabel validates the patient's optional label for an invitation: at most 40 characters of free text with no
// identity number, phone number, e-mail, link or markup (the same checks as other free text that staff may read).
func (r *Rights) checkLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", nil
	}
	if utf8.RuneCountInString(label) > labelMax || scrub.ContainsProhibitedIDStrict(label, r.cfg.ProhibitedValidators) ||
		scrub.ContainsPhoneNumberStrict(label) || scrub.ContainsEmail(label) || scrub.HasLinkOrMarkup(label) {
		return "", fmt.Errorf("%w: label", rp.ErrInvalid)
	}
	return label, nil
}

// consentAndNotErasing reads, inside the caller's transaction, whether the patient's P-CAREGIVER consent is granted
// and whether the account is under an erasure restriction. Callers hold the patient lock.
func consentAndNotErasing(ctx context.Context, tx pgx.Tx, patient string) (consented, erasing bool, err error) {
	var n int
	err = tx.QueryRow(ctx, `SELECT coalesce((SELECT granted FROM consent_state WHERE subject_ref=$1 AND purpose_id=$2), false),
		(SELECT count(*) FROM account_restriction WHERE subject_ref=$1 AND released_at IS NULL)`, patient, purposeCaregiver).Scan(&consented, &n)
	return consented, n > 0, err
}

// CreateCaregiverInvitation makes a one-time code for a person the patient trusts. It needs the patient's
// P-CAREGIVER consent and a fresh step-up. nil scopes mean the defaults (reminders, appointments). The optional
// label (max 40 characters) is sealed with the patient key and shown only to the patient.
func (r *Rights) CreateCaregiverInvitation(ctx context.Context, c rp.Caller, scopes []string, label string) (rp.CaregiverInvitation, error) {
	if err := r.caregiversOn(); err != nil {
		return rp.CaregiverInvitation{}, err
	}
	if err := r.callerIsPatient(c); err != nil {
		return rp.CaregiverInvitation{}, err
	}
	if scopes == nil {
		scopes = rp.DefaultCaregiverScopes()
	}
	scopes, err := normalizeScopes(scopes)
	if err != nil || len(scopes) == 0 {
		return rp.CaregiverInvitation{}, fmt.Errorf("%w: scopes", rp.ErrInvalid)
	}
	label, err = r.checkLabel(label)
	if err != nil {
		return rp.CaregiverInvitation{}, err
	}
	if ok, err := r.m.HasConsent(ctx, c.UserRef, purposeCaregiver); err != nil {
		return rp.CaregiverInvitation{}, err
	} else if !ok {
		return rp.CaregiverInvitation{}, rp.ErrDenied // 403 consent.required
	}
	if err := r.stepUpFor(c); err != nil {
		return rp.CaregiverInvitation{}, err
	}
	now := r.now()
	out := rp.CaregiverInvitation{ID: ids.New(), Scopes: scopes, Label: label, ExpiresAt: now.Add(invitationTTL)}
	var sealed any
	if label != "" {
		s, err := crypto.SealPatient(ctx, r.d.Crypto, c.UserRef, crypto.AAD{Table: "caregiver_invitation", Column: "label", RowID: out.ID}, []byte(label))
		if err != nil {
			return rp.CaregiverInvitation{}, err
		}
		sealed = s
	}
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, lockPatient, c.UserRef); err != nil {
			return err
		}
		// Checked again under the lock: a withdrawal or an erasure request that won the race is seen here.
		consented, erasing, err := consentAndNotErasing(ctx, tx, c.UserRef)
		if err != nil {
			return err
		}
		if erasing {
			return rp.ErrRestricted
		}
		if !consented {
			return rp.ErrDenied
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM caregiver_link WHERE patient_ref=$1 AND status='active') +
			(SELECT count(*) FROM caregiver_invitation WHERE patient_ref=$1 AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > $2)`,
			c.UserRef, now).Scan(&n); err != nil {
			return err
		}
		if n >= maxCaregiversPerPatient {
			return rp.ErrCaregiverLimit
		}
		for attempt := 0; ; attempt++ {
			code, err := newCode()
			if err != nil {
				return err
			}
			// A savepoint keeps the transaction usable if the (vanishingly rare) hash collides.
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			_, err = sp.Exec(ctx, `INSERT INTO caregiver_invitation (id, patient_ref, code_hash, scopes, label_sealed, created_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				out.ID, c.UserRef, r.codeHash(code), scopes, sealed, now, out.ExpiresAt)
			if err == nil {
				if err := sp.Commit(ctx); err != nil {
					return err
				}
				out.Code = code
				break
			}
			_ = sp.Rollback(ctx)
			if !db.IsUniqueViolation(err, "") || attempt >= 3 {
				return err
			}
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "caregiver.invited", SubjectRef: c.UserRef,
			ResourceType: "caregiver_invitation", ResourceRef: out.ID, Purpose: purposeCaregiver, Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID})
	})
	if err != nil {
		return rp.CaregiverInvitation{}, err
	}
	return out, nil
}

// CancelCaregiverInvitation withdraws an invitation that was not used. Not theirs, used or unknown: ErrNotFound.
func (r *Rights) CancelCaregiverInvitation(ctx context.Context, c rp.Caller, id string) error {
	if err := r.caregiversOn(); err != nil {
		return err
	}
	if err := r.callerIsPatient(c); err != nil {
		return err
	}
	if !reUUID.MatchString(id) {
		return rp.ErrNotFound
	}
	return db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE caregiver_invitation SET revoked_at=$3 WHERE id=$1 AND patient_ref=$2 AND redeemed_at IS NULL AND revoked_at IS NULL`,
			id, c.UserRef, r.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return rp.ErrNotFound
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "caregiver.invitation_cancelled", SubjectRef: c.UserRef,
			ResourceType: "caregiver_invitation", ResourceRef: id, Purpose: purposeCaregiver, Result: ap.Allowed, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID})
	})
}

// RedeemCaregiverCode accepts an invitation. Any signed-in patient-app user other than the patient may redeem. A
// wrong, expired, used, cancelled, own or withdrawn code, and a caller under erasure, all return ErrNotFound (one
// answer, no oracle). Five wrong codes in an hour for one account, or 20 for one network, return ErrRedeemLimited,
// counted in the database under advisory locks (caregiver, network, then patient: the same order everywhere).
// Every refusal is audited as a denied event by the caller, with a reason code and never the code.
func (r *Rights) RedeemCaregiverCode(ctx context.Context, c rp.Caller, code string, inCountryConfirmed bool) (rp.CaregiverLink, error) {
	if err := r.caregiversOn(); err != nil {
		return rp.CaregiverLink{}, err
	}
	if err := r.callerIsPatient(c); err != nil {
		return rp.CaregiverLink{}, err
	}
	// Open decision 15, built as "restrict": the caregiver declares being in the pack's country.
	if !inCountryConfirmed {
		return rp.CaregiverLink{}, rp.ErrCountryNotConfirmed
	}
	norm, wellFormed := normalizeCode(code)
	now := r.now()
	var link rp.CaregiverLink
	var patientToNotify string
	limited := false
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		link, patientToNotify, limited = rp.CaregiverLink{}, "", false
		if err := lock(ctx, tx, lockCaregiver, c.UserRef); err != nil {
			return err
		}
		if c.IPHash != "" {
			if err := lock(ctx, tx, lockNetwork, c.IPHash); err != nil {
				return err
			}
		}
		denied := func(reason string) error {
			return ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "caregiver.redeem_failed", SubjectRef: c.UserRef,
				Purpose: purposeCaregiver, ReasonCode: reason, Result: ap.Denied, Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID})
		}
		// Only this caller's own expired attempts are purged here; the periodic job purges the rest.
		if _, err := tx.Exec(ctx, `DELETE FROM caregiver_redeem_attempt WHERE caregiver_ref=$1 AND attempted_at <= $2`, c.UserRef, now.Add(-wrongCodeWindow)); err != nil {
			return err
		}
		var wrong, wrongNet int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM caregiver_redeem_attempt WHERE caregiver_ref=$1 AND attempted_at > $3),
			(SELECT count(*) FROM caregiver_redeem_attempt WHERE $2 <> '' AND ip_hash=$2 AND attempted_at > $3)`,
			c.UserRef, c.IPHash, now.Add(-wrongCodeWindow)).Scan(&wrong, &wrongNet); err != nil {
			return err
		}
		if wrong >= maxWrongCodes || wrongNet >= maxWrongCodesPerNetwork {
			limited = true
			reason := "rate_limited"
			if wrong < maxWrongCodes {
				reason = "network_limited"
			}
			return denied(reason)
		}
		miss := func(reason string) error { // count the failure and answer 404 (the transaction commits so the count stays)
			if _, err := tx.Exec(ctx, `INSERT INTO caregiver_redeem_attempt (caregiver_ref, attempted_at, ip_hash) VALUES ($1,$2,$3)`, c.UserRef, now, c.IPHash); err != nil {
				return err
			}
			link = rp.CaregiverLink{}
			return denied(reason)
		}
		// A caller whose own account is being closed cannot take on a new link.
		var callerErasing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_restriction WHERE subject_ref=$1 AND released_at IS NULL`, c.UserRef).Scan(&callerErasing); err != nil {
			return err
		}
		if callerErasing > 0 {
			return miss("caller_restricted")
		}
		if !wellFormed {
			return miss("invalid_code")
		}
		var invID, patient string
		var scopes []string
		var sealed crypto.Sealed[crypto.PatientKey]
		err := tx.QueryRow(ctx, `SELECT id::text, patient_ref::text, scopes, label_sealed FROM caregiver_invitation
			WHERE code_hash=$1 AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > $2 FOR UPDATE`, r.codeHash(norm), now).Scan(&invID, &patient, &scopes, &sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return miss("invalid_code")
		}
		if err != nil {
			return err
		}
		if patient == c.UserRef {
			return miss("own_code") // the patient cannot be their own caregiver
		}
		if err := lock(ctx, tx, lockPatient, patient); err != nil {
			return err
		}
		consented, erasing, err := consentAndNotErasing(ctx, tx, patient)
		if err != nil {
			return err
		}
		if !consented || erasing {
			return miss("patient_unavailable")
		}
		var onPatient, onCaregiver, already int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM caregiver_link WHERE patient_ref=$1 AND status='active'),
			(SELECT count(*) FROM caregiver_link WHERE caregiver_ref=$2 AND status='active'),
			(SELECT count(*) FROM caregiver_link WHERE patient_ref=$1 AND caregiver_ref=$2 AND status='active')`, patient, c.UserRef).Scan(&onPatient, &onCaregiver, &already); err != nil {
			return err
		}
		switch {
		case already > 0:
			return rp.ErrAlreadyLinked
		case onPatient >= maxCaregiversPerPatient || onCaregiver >= maxPatientsPerCaregiver:
			return rp.ErrCaregiverLimit
		}
		link = rp.CaregiverLink{ID: ids.New(), PatientRef: patient, CaregiverRef: c.UserRef, Scopes: scopes, Status: "active", CreatedAt: now}
		// The sealed label is copied as it is; label_ref names the invitation whose id the seal is bound to.
		var labelRef any
		if !sealed.IsZero() {
			labelRef = invID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO caregiver_link (id, patient_ref, caregiver_ref, scopes, status, created_at, label_sealed, label_ref) VALUES ($1,$2,$3,$4,'active',$5,$6,$7)`,
			link.ID, patient, c.UserRef, scopes, now, sealed, labelRef); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE caregiver_invitation SET redeemed_at=$2 WHERE id=$1`, invID, now); err != nil {
			return err
		}
		// An audit event for each side: the patient sees who was linked; the caregiver sees their own link.
		for _, subject := range []string{patient, c.UserRef} {
			if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "caregiver", Action: "caregiver.link_created", SubjectRef: subject,
				ResourceType: "caregiver_link", ResourceRef: link.ID, Purpose: purposeCaregiver, GrantID: link.ID, Result: ap.Allowed,
				Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
				return err
			}
		}
		// The patient is told, neutrally, that someone accepted (notifications; no name, no scopes).
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: rp.EventCaregiverLinked, SubjectRef: patient, Payload: map[string]string{"linkRef": link.ID}}); err != nil {
			return err
		}
		patientToNotify = patient
		return notify(ctx, tx, patient)
	})
	if err != nil {
		return rp.CaregiverLink{}, err
	}
	if patientToNotify != "" {
		r.m.Invalidate(patientToNotify)
	}
	if limited {
		return rp.CaregiverLink{}, rp.ErrRedeemLimited
	}
	if link.ID == "" {
		return rp.CaregiverLink{}, rp.ErrNotFound
	}
	return link, nil
}

// withLabels opens the patient's own labels for their links (sealed with their key). A label that cannot be opened
// (for example after the key was destroyed) is left empty.
func (r *Rights) withLabels(ctx context.Context, patient string, ls []rp.CaregiverLink) {
	if len(ls) == 0 {
		return
	}
	idsIn := make([]string, 0, len(ls))
	for _, l := range ls {
		idsIn = append(idsIn, l.ID)
	}
	rows, err := r.m.pool.Query(ctx, `SELECT id::text, label_sealed, label_ref::text FROM caregiver_link WHERE patient_ref=$1 AND id = ANY($2::uuid[]) AND label_sealed IS NOT NULL`, patient, idsIn)
	if err != nil {
		return
	}
	type row struct {
		id, ref string
		sealed  crypto.Sealed[crypto.PatientKey]
	}
	var found []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.sealed, &x.ref); err == nil {
			found = append(found, x)
		}
	}
	rows.Close()
	for _, x := range found {
		b, err := crypto.Open(ctx, r.d.Crypto, crypto.AAD{Table: "caregiver_invitation", Column: "label", RowID: x.ref}, x.sealed)
		if err != nil {
			continue
		}
		for i := range ls {
			if ls[i].ID == x.id {
				ls[i].Label = string(b)
			}
		}
	}
}

// PurgeCaregiving is the periodic clean-up (worker): wrong-code counters past their hour, and invitation rows 30 days
// after they expired, were redeemed or were cancelled. Link rows are never purged here (evidence, RET-06).
func (r *Rights) PurgeCaregiving(ctx context.Context) (attempts, invitations int, err error) {
	now := r.now()
	tag, err := r.m.pool.Exec(ctx, `DELETE FROM caregiver_redeem_attempt WHERE attempted_at <= $1`, now.Add(-wrongCodeWindow))
	if err != nil {
		return 0, 0, err
	}
	attempts = int(tag.RowsAffected())
	tag, err = r.m.pool.Exec(ctx, `DELETE FROM caregiver_invitation WHERE LEAST(coalesce(redeemed_at,'infinity'), coalesce(revoked_at,'infinity'), expires_at) <= $1`, now.Add(-invitationKeep))
	if err != nil {
		return attempts, 0, err
	}
	return attempts, int(tag.RowsAffected()), nil
}

func scanLink(row pgx.Row) (rp.CaregiverLink, error) {
	var l rp.CaregiverLink
	err := row.Scan(&l.ID, &l.PatientRef, &l.CaregiverRef, &l.Scopes, &l.Status, &l.CreatedAt, &l.RevokedAt)
	return l, err
}

const linkCols = `id::text, patient_ref::text, caregiver_ref::text, scopes, status, created_at, revoked_at`

// listLinks pages links by (created_at, id), newest first. where names the column that holds the caller.
func (r *Rights) listLinks(ctx context.Context, col, ref string, activeOnly bool, cursor string, limit int) ([]rp.CaregiverLink, string, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	q := `SELECT ` + linkCols + ` FROM caregiver_link WHERE ` + col + `=$1`
	args := []any{ref}
	if activeOnly {
		q += ` AND status='active'`
	}
	if cursor != "" {
		at, id, ok := strings.Cut(cursor, "|")
		ts, err := time.Parse(time.RFC3339Nano, at)
		if !ok || err != nil || !reUUID.MatchString(id) {
			return nil, "", fmt.Errorf("%w: cursor", rp.ErrInvalid)
		}
		q += ` AND (created_at, id) < ($2, $3)`
		args = append(args, ts, id)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1)
	rows, err := r.m.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []rp.CaregiverLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	return out, next, nil
}

// ListMyCaregivers lists the patient's links (revoked ones stay as history), newest first.
func (r *Rights) ListMyCaregivers(ctx context.Context, c rp.Caller, cursor string, limit int) ([]rp.CaregiverLink, string, error) {
	if err := r.caregiversOn(); err != nil {
		return nil, "", err
	}
	if err := r.callerIsPatient(c); err != nil {
		return nil, "", err
	}
	ls, next, err := r.listLinks(ctx, "patient_ref", c.UserRef, false, cursor, limit)
	r.withLabels(ctx, c.UserRef, ls)
	return ls, next, err
}

// ListCaregivingPatients lists the patients the caller cares for (references and scopes only).
func (r *Rights) ListCaregivingPatients(ctx context.Context, c rp.Caller, cursor string, limit int) ([]rp.CaregiverLink, string, error) {
	if err := r.caregiversOn(); err != nil {
		return nil, "", err
	}
	if err := r.callerIsPatient(c); err != nil {
		return nil, "", err
	}
	return r.listLinks(ctx, "caregiver_ref", c.UserRef, true, cursor, limit)
}

// SetCaregiverScopes replaces the scopes of one active link. Adding a sensitive scope needs a fresh step-up.
func (r *Rights) SetCaregiverScopes(ctx context.Context, c rp.Caller, linkID string, scopes []string) (rp.CaregiverLink, error) {
	if err := r.caregiversOn(); err != nil {
		return rp.CaregiverLink{}, err
	}
	if err := r.callerIsPatient(c); err != nil {
		return rp.CaregiverLink{}, err
	}
	scopes, err := normalizeScopes(scopes)
	if err != nil || len(scopes) == 0 {
		return rp.CaregiverLink{}, fmt.Errorf("%w: scopes", rp.ErrInvalid)
	}
	if !reUUID.MatchString(linkID) {
		return rp.CaregiverLink{}, rp.ErrNotFound
	}
	var out rp.CaregiverLink
	err = db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		cur, err := scanLink(tx.QueryRow(ctx, `SELECT `+linkCols+` FROM caregiver_link WHERE id=$1 AND patient_ref=$2 AND status='active' FOR UPDATE`, linkID, c.UserRef))
		if errors.Is(err, pgx.ErrNoRows) {
			return rp.ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, s := range scopes {
			if rp.ScopeSensitive(s) && !slices.Contains(cur.Scopes, s) {
				if err := r.stepUpFor(c); err != nil {
					return err
				}
				break
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE caregiver_link SET scopes=$2 WHERE id=$1`, linkID, scopes); err != nil {
			return err
		}
		cur.Scopes = scopes
		out = cur
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: c.UserRef, ActorRole: "patient", Action: "caregiver.scopes_changed", SubjectRef: c.UserRef,
			ResourceType: "caregiver_link", ResourceRef: linkID, Purpose: purposeCaregiver, GrantID: linkID, Result: ap.Allowed,
			Client: c.Client, IPHash: c.IPHash, RequestID: c.RequestID}); err != nil {
			return err
		}
		return notify(ctx, tx, c.UserRef)
	})
	r.m.Invalidate(c.UserRef) // takes effect at once on this instance; NOTIFY reaches the others
	if err == nil {
		one := []rp.CaregiverLink{out}
		r.withLabels(ctx, c.UserRef, one)
		out = one[0]
	}
	return out, err
}

// RevokeCaregiver ends one link. Repeating it is a no-op; another patient's or an unknown link is ErrNotFound.
func (r *Rights) RevokeCaregiver(ctx context.Context, c rp.Caller, linkID string) error {
	if err := r.caregiversOn(); err != nil {
		return err
	}
	if err := r.callerIsPatient(c); err != nil {
		return err
	}
	if !reUUID.MatchString(linkID) {
		return rp.ErrNotFound
	}
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM caregiver_link WHERE id=$1 AND patient_ref=$2 FOR UPDATE`, linkID, c.UserRef).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return rp.ErrNotFound
			}
			return err
		}
		if status != "active" {
			return nil
		}
		if err := endLink(ctx, tx, linkID, c.UserRef, c.UserRef, "patient", r.now(), c.RequestID, "patient_revoked"); err != nil {
			return err
		}
		return notify(ctx, tx, c.UserRef)
	})
	r.m.Invalidate(c.UserRef)
	return err
}

// LeaveCaregiving ends the caller's own link with a patient.
func (r *Rights) LeaveCaregiving(ctx context.Context, c rp.Caller, patientRef string) error {
	if err := r.caregiversOn(); err != nil {
		return err
	}
	if err := r.callerIsPatient(c); err != nil {
		return err
	}
	if !reUUID.MatchString(patientRef) {
		return rp.ErrNotFound
	}
	err := db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM caregiver_link WHERE patient_ref=$1 AND caregiver_ref=$2 AND status='active' FOR UPDATE`, patientRef, c.UserRef).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return rp.ErrNotFound
			}
			return err
		}
		if err := endLink(ctx, tx, id, patientRef, c.UserRef, "caregiver", r.now(), c.RequestID, "caregiver_left"); err != nil {
			return err
		}
		return notify(ctx, tx, patientRef)
	})
	r.m.Invalidate(patientRef)
	return err
}

// endLink revokes one active link and writes its audit event, for the patient's record.
func endLink(ctx context.Context, tx pgx.Tx, linkID, patient, byRef, byRole string, now time.Time, requestID, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE caregiver_link SET status='revoked', revoked_at=$2 WHERE id=$1 AND status='active'`, linkID, now); err != nil {
		return err
	}
	return ap.Record(ctx, tx, ap.Event{ActorRef: byRef, ActorRole: byRole, Action: "caregiver.link_revoked", SubjectRef: patient,
		ResourceType: "caregiver_link", ResourceRef: linkID, Purpose: purposeCaregiver, GrantID: linkID, ReasonCode: reason, Result: ap.Allowed, RequestID: requestID})
}

// endAllCaregiving revokes every active link of a patient and every open invitation, in the caller's transaction.
// Used when P-CAREGIVER is withdrawn and at the patient's erasure (step 1). The caller notifies and invalidates.
func endAllCaregiving(ctx context.Context, tx pgx.Tx, patient, byRef, byRole string, now time.Time, requestID, reason string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM caregiver_link WHERE patient_ref=$1 AND status='active' FOR UPDATE`, patient)
	if err != nil {
		return err
	}
	var linkIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		linkIDs = append(linkIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range linkIDs {
		if err := endLink(ctx, tx, id, patient, byRef, byRole, now, requestID, reason); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE caregiver_invitation SET revoked_at=$2 WHERE patient_ref=$1 AND redeemed_at IS NULL AND revoked_at IS NULL`, patient, now)
	return err
}

// caregiverErasureStep1 is the caregiver side of an erasure (K-DP-07): the links where the subject is the caregiver
// are revoked (kept as restricted evidence), their wrong-code counters are dropped, and the patients they cared for
// are returned so the caller can notify and invalidate their caches. The subject's own patient side is
// endAllCaregiving plus the deletion of their invitations.
func caregiverErasureStep1(ctx context.Context, tx pgx.Tx, subject string, now time.Time) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, patient_ref::text FROM caregiver_link WHERE caregiver_ref=$1 AND status='active' FOR UPDATE`, subject)
	if err != nil {
		return nil, err
	}
	type pair struct{ id, patient string }
	var ps []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.patient); err != nil {
			rows.Close()
			return nil, err
		}
		ps = append(ps, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var patients []string
	for _, p := range ps {
		if err := endLink(ctx, tx, p.id, p.patient, subject, "caregiver", now, "", "caregiver_erased"); err != nil {
			return nil, err
		}
		patients = append(patients, p.patient)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM caregiver_redeem_attempt WHERE caregiver_ref=$1`, subject); err != nil {
		return nil, err
	}
	return patients, nil
}

// patientCaregiverErasureStep1 revokes every link of the erasing patient and deletes their invitations (the hashes
// have no value once the account is closed). Link rows stay as restricted evidence (RET-06 [CONFIRM]).
func patientCaregiverErasureStep1(ctx context.Context, tx pgx.Tx, subject string, now time.Time) error {
	if err := endAllCaregiving(ctx, tx, subject, subject, "patient", now, "", "erasure_requested"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM caregiver_invitation WHERE patient_ref=$1`, subject)
	return err
}
