package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
)

// EvidenceMeta describes an uploaded file. The bytes are never part of a view.
type EvidenceMeta struct {
	ID          string
	Kind        string
	ContentType string
	SizeBytes   int
	State       string // stored | deleted | purged
	CreatedAt   time.Time
}

// CheckView is one automated check result, for reviewers.
type CheckView struct {
	Source         string
	Outcome        string
	RegisteredName string // the register's name, decrypted for the reviewer
	CheckedAt      time.Time
}

// DecisionView is one entry of the decision log, for reviewers.
type DecisionView struct {
	Stage       string // proposed | final
	Outcome     string
	ReasonCode  string
	ReviewerRef string
	Override    bool
	At          time.Time
}

// Case is a verification case. Doctor views leave the reviewer-only fields empty.
type Case struct {
	ID          string
	SubjectKind string
	SubjectRef  string
	Status      public.Status // effective status (the same answer the gate gives)
	Round       int
	RoundState  string

	CouncilCode        string
	RegistrationNumber string
	RegistrationYear   int
	RegisteredName     string
	Qualifications     []string
	SpecialtyClaim     string
	IDType             string
	Evidence           []EvidenceMeta
	InfoRequested      string // reason code asking the doctor for more information

	SubmittedAt   *time.Time
	VerifiedAt    *time.Time
	ReVerifyDueAt *time.Time

	// Reviewer only.
	AutoCheck          string // passed | failed | ""
	ManualFlags        []string
	Checks             []CheckView
	Decisions          []DecisionView
	PendingOutcome     string // awaiting the second reviewer
	PendingBy          string
	SpecialtyConfirmed bool
	ReviewDueBy        *time.Time
	Overdue            bool
}

func (m *Module) evidenceFor(ctx context.Context, c *caseRow, all bool) ([]EvidenceMeta, error) {
	q := `SELECT id, kind, content_type, size_bytes, state, created_at FROM evidence WHERE case_id = $1 AND round = $2`
	if !all {
		q += ` AND state = 'stored'`
	}
	rs, err := m.pool.Query(ctx, q+` ORDER BY created_at, id`, c.ID, c.Round)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []EvidenceMeta{}
	for rs.Next() {
		var e EvidenceMeta
		if err := rs.Scan(&e.ID, &e.Kind, &e.ContentType, &e.SizeBytes, &e.State, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

func (m *Module) baseView(c *caseRow) *Case {
	v := &Case{
		ID: c.ID, SubjectKind: c.Kind, SubjectRef: c.Subject, Status: m.effective(c, m.now()), Round: c.Round, RoundState: c.RoundState,
		CouncilCode: c.Council, RegistrationNumber: c.Number, RegisteredName: c.Name, Qualifications: nonNil(c.Quals),
		SpecialtyClaim: c.Specialty, IDType: c.IDType, InfoRequested: c.InfoRequested, SubmittedAt: c.SubmittedAt,
		VerifiedAt: c.VerifiedAt, ReVerifyDueAt: c.DueAt,
	}
	if c.Year != nil {
		v.RegistrationYear = *c.Year
	}
	return v
}

// doctorView is what the doctor sees of their own case (status visible to the doctor,
// S-110). Automated check results and flags stay with reviewers.
func (m *Module) doctorView(ctx context.Context, c *caseRow) (*Case, error) {
	v := m.baseView(c)
	ev, err := m.evidenceFor(ctx, c, false)
	if err != nil {
		return nil, fmt.Errorf("verification: view: %w", err)
	}
	v.Evidence = ev
	return v, nil
}

// reviewerView adds checks, flags and the decision log.
func (m *Module) reviewerView(ctx context.Context, c *caseRow) (*Case, error) {
	v := m.baseView(c)
	ev, err := m.evidenceFor(ctx, c, true)
	if err != nil {
		return nil, fmt.Errorf("verification: view: %w", err)
	}
	v.Evidence = ev
	v.AutoCheck, v.ManualFlags, v.PendingOutcome, v.SpecialtyConfirmed = c.AutoCheck, nonNil(c.Flags), c.PendingOutcome, c.SpecialtyConfirmed
	if c.PendingBy != nil {
		v.PendingBy = *c.PendingBy
	}
	if c.SubmittedAt != nil {
		d := c.SubmittedAt.Add(m.cfg.ReviewSLA)
		v.ReviewDueBy = &d
		v.Overdue = (c.RoundState == roundSubmitted || c.RoundState == roundChecking) && m.now().After(d)
	}

	rs, err := m.pool.Query(ctx, `SELECT id, source, outcome, detail_ct, checked_at FROM check_result WHERE case_id = $1 AND round = $2 ORDER BY checked_at, id`, c.ID, c.Round)
	if err != nil {
		return nil, fmt.Errorf("verification: view: %w", err)
	}
	type chk struct {
		id string
		v  CheckView
		ct crypto.Sealed[crypto.ServiceKey]
	}
	var chks []chk
	for rs.Next() {
		var k chk
		if err := rs.Scan(&k.id, &k.v.Source, &k.v.Outcome, &k.ct, &k.v.CheckedAt); err != nil {
			rs.Close()
			return nil, err
		}
		chks = append(chks, k)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for _, k := range chks {
		if !k.ct.IsZero() {
			b, err := crypto.Open(ctx, m.d.Crypto, checkAAD(k.id), k.ct)
			if err != nil {
				return nil, fmt.Errorf("verification: open check: %w", err)
			}
			var d checkDetail
			if json.Unmarshal(b, &d) == nil {
				k.v.RegisteredName = d.RegisteredName
			}
		}
		v.Checks = append(v.Checks, k.v)
	}

	ds, err := m.pool.Query(ctx, `SELECT stage, outcome, reason_code, reviewer_ref, override, created_at FROM decision WHERE case_id = $1 ORDER BY created_at, id`, c.ID)
	if err != nil {
		return nil, fmt.Errorf("verification: view: %w", err)
	}
	defer ds.Close()
	for ds.Next() {
		var d DecisionView
		if err := ds.Scan(&d.Stage, &d.Outcome, &d.ReasonCode, &d.ReviewerRef, &d.Override, &d.At); err != nil {
			return nil, err
		}
		v.Decisions = append(v.Decisions, d)
	}
	return v, ds.Err()
}

type checkDetail struct {
	RegisteredName string `json:"registeredName,omitempty"`
}

func checkAAD(id string) crypto.AAD {
	return crypto.AAD{Table: "verification.check_result", Column: "detail_ct", RowID: id}
}
