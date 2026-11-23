package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	vadapter "gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// DraftInput is what a doctor enters (RG-02). The council is mandatory free text,
// normalised here: the country pack has no council list yet, so any council name or
// code is accepted and the reviewer compares it with the register [FILL: pack list].
type DraftInput struct {
	CouncilCode        string
	RegistrationNumber string
	RegistrationYear   int
	RegisteredName     string
	Qualifications     []string
	SpecialtyClaim     string // RG-04: shown only if a reviewer confirms it
	IDType             string // from the allow-list in Config
}

var (
	reNumber = regexp.MustCompile(`^[A-Z0-9][A-Z0-9/\-.]{1,39}$`)
	reName   = regexp.MustCompile(`^[\p{L}\p{M} .'\-]{2,120}$`)
	reQual   = regexp.MustCompile(`^[\p{L}\p{N}\p{M} .,()&/\-]{2,80}$`)
	reSpec   = regexp.MustCompile(`^[\p{L}\p{M} .,()&/\-]{2,80}$`)
	reIDType = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

// normaliseCouncil upper-cases the council, turns every run of other characters into one
// space, and requires some letters. "Delhi Medical Council." and "DELHI  MEDICAL COUNCIL"
// become the same value.
func normaliseCouncil(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToUpper(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func registrationKey(number string) string {
	var b strings.Builder
	for _, r := range number {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameKey makes two spellings of a name comparable: no case, no titles, no punctuation.
func nameKey(s string) string {
	var out []string
	for _, w := range strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsMark(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)) {
		if w == "dr" {
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

type normalised struct {
	council, number, key, name, specialty, idType string
	year                                          int
	quals                                         []string
}

// validateDraft normalises and checks a draft. Prohibited identifiers are refused before
// anything else, so nothing from the input is stored or logged (TC-20, K-DP-05).
func (m *Module) validateDraft(in DraftInput, now time.Time) (normalised, error) {
	var n normalised
	texts := append([]string{in.CouncilCode, in.RegistrationNumber, in.RegisteredName, in.SpecialtyClaim, in.IDType}, in.Qualifications...)
	for _, t := range texts {
		if scrub.ContainsProhibitedID(t, m.cfg.ProhibitedIDValidators) {
			return n, ErrProhibitedID
		}
	}
	n.council = normaliseCouncil(in.CouncilCode)
	if in.RegistrationNumber != "" && n.council == "" {
		return n, ErrCouncilRequired // E-VER-02: a number without its council is never accepted
	}
	if n.council != "" && (len(n.council) < 2 || len(n.council) > 60) {
		return n, invalid("councilCode", "format")
	}
	if num := strings.ToUpper(strings.Join(strings.Fields(in.RegistrationNumber), "")); num != "" {
		if !reNumber.MatchString(num) {
			return n, invalid("registrationNumber", "format")
		}
		n.number, n.key = num, registrationKey(num)
	}
	if in.RegistrationYear != 0 && (in.RegistrationYear < 1900 || in.RegistrationYear > now.Year()) {
		return n, invalid("registrationYear", "range")
	}
	n.year = in.RegistrationYear
	if name := strings.Join(strings.Fields(in.RegisteredName), " "); name != "" {
		if !reName.MatchString(name) {
			return n, invalid("registeredName", "format")
		}
		n.name = name
	}
	if len(in.Qualifications) > 10 {
		return n, invalid("qualifications", "too_many")
	}
	for _, q := range in.Qualifications {
		q = strings.Join(strings.Fields(q), " ")
		if !reQual.MatchString(q) {
			return n, invalid("qualifications", "format")
		}
		n.quals = append(n.quals, q)
	}
	if sp := strings.Join(strings.Fields(in.SpecialtyClaim), " "); sp != "" {
		if !reSpec.MatchString(sp) {
			return n, invalid("specialtyClaim", "format")
		}
		n.specialty = sp
	}
	if in.IDType != "" {
		if !reIDType.MatchString(in.IDType) || !slices.Contains(m.cfg.IDTypes, in.IDType) {
			return n, ErrIDTypeNotAllowed
		}
		n.idType = in.IDType
	}
	return n, nil
}

// requireConsent checks the doctor's D-VERIFY consent through the records gate. A gate
// error is treated as no consent (fail closed).
func (m *Module) requireConsent(ctx context.Context, doctorRef string) error {
	ok, err := m.d.Consent.HasConsent(ctx, doctorRef, m.cfg.ConsentPurpose)
	if err != nil {
		return fmt.Errorf("verification: consent check: %w", err)
	}
	if !ok {
		return ErrConsentRequired
	}
	return nil
}

func validRef(ref string) error {
	if !ids.Valid(ref) {
		return ErrNotFound
	}
	return nil
}

// SaveDraft creates or updates the doctor's draft (S-110). It is allowed while the case
// is a draft or the reviewer asked for more information, and it opens a new round when a
// professional whose standing lapsed (re-verification due, suspended, expired) starts again.
func (m *Module) SaveDraft(ctx context.Context, doctorRef string, in DraftInput) (*Case, error) {
	if err := validRef(doctorRef); err != nil {
		return nil, err
	}
	n, err := m.validateDraft(in, m.now())
	if err != nil {
		return nil, err
	}
	if err := m.requireConsent(ctx, doctorRef); err != nil {
		return nil, err
	}
	out, err := m.saveDraft(ctx, KindDoctor, doctorRef, actor{Ref: doctorRef, Role: "doctor"}, n)
	if err != nil {
		return nil, err
	}
	return m.doctorView(ctx, out)
}

// saveDraft creates or updates the draft of one subject (doctor or clinic) in one
// transaction. a is who edits it; for a clinic that is the clinic admin.
func (m *Module) saveDraft(ctx context.Context, kind, subject string, a actor, n normalised) (*caseRow, error) {
	var out *caseRow
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseBySubject(ctx, tx, kind, subject)
		switch {
		case errors.Is(err, ErrNotFound):
			c = &caseRow{ID: ids.New(), Kind: kind, Subject: subject, Status: public.StatusDraft, Round: 1, RoundState: roundDraft}
			if _, err := tx.Exec(ctx, `INSERT INTO verification_case (id, subject_kind, subject_ref, status, round, round_state) VALUES ($1,$2,$3,$4,$5,$6)`,
				c.ID, c.Kind, c.Subject, string(c.Status), c.Round, c.RoundState); err != nil {
				return err
			}
			if err := m.auditEvent(ctx, tx, a, "verification.case_created", c, ""); err != nil {
				return err
			}
		case err != nil:
			return err
		case c.RoundState == roundDraft || c.RoundState == roundNeedsInfo:
			// editable
		case c.RoundState == roundDecided && !c.isApplication() &&
			(c.Status == public.StatusReVerifyDue || c.Status == public.StatusSuspended || c.Status == public.StatusExpired):
			c.Round++
			c.RoundState = roundDraft
			c.AutoCheck, c.Flags, c.InfoRequested, c.PendingOutcome, c.PendingBy, c.PendingReason = "", nil, "", "", nil, ""
			c.SpecialtyConfirmed = false
			if err := m.auditEvent(ctx, tx, a, "verification.round_started", c, ""); err != nil {
				return err
			}
		default:
			return ErrState
		}
		c.Council, c.Number, c.Key, c.Name, c.Specialty, c.IDType, c.Quals = n.council, n.number, n.key, n.name, n.specialty, n.idType, n.quals
		if n.year != 0 {
			y := n.year
			c.Year = &y
		} else {
			c.Year = nil
		}
		if err := m.auditEvent(ctx, tx, a, "verification.draft_saved", c, ""); err != nil {
			return err
		}
		out = c
		return m.save(ctx, tx, c)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetMine returns the doctor's own case (status visible to the doctor). Doctors only ever
// reach their own case, so there is no way to read another doctor's.
func (m *Module) GetMine(ctx context.Context, doctorRef string) (*Case, error) {
	if err := validRef(doctorRef); err != nil {
		return nil, err
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2`, KindDoctor, doctorRef))
	if err != nil {
		return nil, err
	}
	return m.doctorView(ctx, c)
}

// Submit sends the draft for review (S-110). The doctor must hold D-VERIFY consent. The
// automated checks then run; whatever they say, the case waits for a human reviewer.
func (m *Module) Submit(ctx context.Context, doctorRef string) (*Case, error) {
	if err := validRef(doctorRef); err != nil {
		return nil, err
	}
	if err := m.requireConsent(ctx, doctorRef); err != nil {
		return nil, err
	}
	caseID, err := m.submit(ctx, KindDoctor, doctorRef, actor{Ref: doctorRef, Role: "doctor"})
	if err != nil {
		return nil, err
	}
	// The checks are advice for the reviewer. If they cannot run now, the case stays
	// "submitted" and ProcessPending retries; it is never approved either way.
	if err := m.RunChecks(ctx, caseID); err != nil {
		m.log.Warn("verification.checks_deferred", logging.Err(err))
	}
	return m.GetMine(ctx, doctorRef)
}

// submit moves a complete draft of one subject to "submitted" and returns the case ID.
func (m *Module) submit(ctx context.Context, kind, subject string, a actor) (string, error) {
	var caseID string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseBySubject(ctx, tx, kind, subject)
		if err != nil {
			return err
		}
		if c.RoundState != roundDraft && c.RoundState != roundNeedsInfo {
			return ErrState
		}
		if c.Council == "" {
			return ErrCouncilRequired
		}
		required := m.cfg.RequiredEvidenceKinds
		if kind == KindClinic {
			// A clinic needs its registering authority, registration number, year and legal
			// name; it has no qualifications, specialty or photo ID.
			if c.Number == "" || c.Year == nil || c.Name == "" {
				return ErrIncomplete
			}
			required = m.cfg.RequiredClinicEvidenceKinds
		} else {
			if c.Number == "" || c.Year == nil || c.Name == "" || len(c.Quals) == 0 || c.IDType == "" {
				return ErrIncomplete
			}
			if !slices.Contains(m.cfg.IDTypes, c.IDType) {
				return ErrIDTypeNotAllowed
			}
		}
		have := map[string]bool{}
		rs, err := tx.Query(ctx, `SELECT kind FROM evidence WHERE case_id = $1 AND round = $2 AND state = 'stored'`, c.ID, c.Round)
		if err != nil {
			return err
		}
		for rs.Next() {
			var k string
			if err := rs.Scan(&k); err != nil {
				rs.Close()
				return err
			}
			have[k] = true
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return err
		}
		for _, k := range required {
			if !have[k] {
				return ErrIncomplete
			}
		}
		now := m.now().UTC()
		c.SubmittedAt = &now
		c.AutoCheck, c.Flags, c.InfoRequested, c.PendingOutcome, c.PendingBy, c.PendingReason = "", nil, "", "", nil, ""
		if err := m.setRound(ctx, tx, a, c, roundSubmitted, "verification.submitted", ""); err != nil {
			return err
		}
		caseID = c.ID
		return m.save(ctx, tx, c)
	})
	return caseID, err
}

// Flag codes recorded on a case. Reviewers see them; doctors do not (a duplicate flag
// would tell a doctor that someone else holds the number).
const (
	flagRegistryNoMatch     = "registry_no_match"
	flagRegistryNotFound    = "registry_not_found"
	flagRegistryInconclusve = "registry_inconclusive"
	flagCheckUnavailable    = "check_unavailable"
	flagCheckTimeout        = "check_timeout"
	flagNameMismatch        = "name_mismatch"
	flagOtherCouncil        = "registration_in_other_council"
	flagNumberInUse         = "registration_in_use"
)

type checkRec struct {
	source, outcome, registeredName string
}

// RunChecks runs the automated checks for a submitted case and moves it to "checking".
// Any error, timeout, inconclusive or not_found answer, name mismatch or duplicate
// registration only adds a flag: the case goes to manual review (E-VER-01, E-VER-02,
// E-VER-04; TC-42, TC-43, TC-45). Even a clean "match" is only advice for the reviewer
// in v1.0, so the case is never verified here. Safe to repeat.
func (m *Module) RunChecks(ctx context.Context, caseID string) error {
	if err := validRef(caseID); err != nil {
		return err
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1`, caseID))
	if err != nil {
		return err
	}
	if c.RoundState != roundSubmitted {
		return nil
	}
	var flags []string
	var recs []checkRec

	// Same registration under another council, or in use by another professional.
	rs, err := m.pool.Query(ctx, `SELECT council_code FROM verification_case WHERE registration_key = $1 AND id <> $2 AND subject_kind = $3`, c.Key, c.ID, c.Kind)
	if err != nil {
		return fmt.Errorf("verification: duplicate check: %w", err)
	}
	dupOther, dupSame := false, false
	for rs.Next() {
		var council string
		if err := rs.Scan(&council); err != nil {
			rs.Close()
			return err
		}
		if council != c.Council {
			dupOther = true
		} else {
			dupSame = true
		}
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	if dupOther {
		flags = append(flags, flagOtherCouncil)
		recs = append(recs, checkRec{source: "duplicate", outcome: flagOtherCouncil})
	}
	if dupSame {
		flags = append(flags, flagNumberInUse)
		recs = append(recs, checkRec{source: "duplicate", outcome: flagNumberInUse})
	}

	// Register lookup with a timeout. Every failure mode becomes a flag.
	// A clinic has no register to look up in v1.0 (no adapter exists): only the duplicate
	// check runs and the reviewer confirms the registration from the documents.
	var res vadapter.RegistrationResult
	if c.Kind == KindDoctor {
		cctx, cancel := context.WithTimeout(ctx, m.cfg.CheckTimeout)
		res, err = m.d.Checker.CheckRegistration(cctx, vadapter.RegistrationQuery{Council: c.Council, Number: c.Number, Name: c.Name})
		cancel()
	}
	switch {
	case c.Kind != KindDoctor:
	case err != nil:
		flag := flagCheckUnavailable
		if adapters.KindOf(err) == adapters.Timeout || errors.Is(err, context.DeadlineExceeded) {
			flag = flagCheckTimeout
		}
		flags = append(flags, flag)
		recs = append(recs, checkRec{source: "registry", outcome: flag})
	default:
		rec := checkRec{source: "registry", outcome: string(res.Outcome), registeredName: res.RegisteredName}
		switch res.Outcome {
		case vadapter.Match:
			if res.RegisteredName == "" || nameKey(res.RegisteredName) != nameKey(c.Name) {
				flags = append(flags, flagNameMismatch)
				recs = append(recs, rec, checkRec{source: "name", outcome: flagNameMismatch})
				rec = checkRec{}
			}
		case vadapter.NoMatch:
			flags = append(flags, flagRegistryNoMatch)
		case vadapter.NotFound:
			flags = append(flags, flagRegistryNotFound)
		default: // inconclusive or anything unknown: fail closed
			rec.outcome = string(vadapter.Inconclusive)
			flags = append(flags, flagRegistryInconclusve)
		}
		if rec.source != "" {
			recs = append(recs, rec)
		}
	}

	auto := "passed"
	if len(flags) > 0 {
		auto = "failed"
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		if c.RoundState != roundSubmitted {
			return nil // another worker got there first
		}
		for _, r := range recs {
			id := ids.New()
			var ct any
			if r.registeredName != "" {
				doc, _ := json.Marshal(checkDetail{RegisteredName: r.registeredName})
				sealed, err := crypto.SealService(ctx, m.d.Crypto, "verification_check", checkAAD(id), doc)
				if err != nil {
					return fmt.Errorf("verification: seal check: %w", err)
				}
				ct = sealed.Bytes()
			}
			if _, err := tx.Exec(ctx, `INSERT INTO check_result (id, case_id, round, source, outcome, detail_ct, checked_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				id, c.ID, c.Round, r.source, r.outcome, ct, m.now().UTC()); err != nil {
				return err
			}
		}
		c.AutoCheck, c.Flags = auto, flags
		if err := m.setRound(ctx, tx, system, c, roundChecking, "verification.checks_recorded", ""); err != nil {
			return err
		}
		return m.save(ctx, tx, c)
	})
}

// ProcessPending is a job method: it runs the checks for cases still "submitted" (the
// checks could not run at submit time, for example a crash or a register outage that
// outlasted the request). It returns how many cases moved to "checking".
func (m *Module) ProcessPending(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rs, err := m.pool.Query(ctx, `SELECT id FROM verification_case WHERE round_state = 'submitted' ORDER BY submitted_at, id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var idsList []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			rs.Close()
			return 0, err
		}
		idsList = append(idsList, id)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range idsList {
		if err := m.RunChecks(ctx, id); err != nil {
			m.log.Warn("verification.checks_deferred", logging.Err(err))
			continue
		}
		n++
	}
	return n, nil
}
