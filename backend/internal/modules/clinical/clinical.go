// Package clinical is the clinical module. This part holds the drug master (S-126) and the
// glue for the prescription rules engine (S-138, package rules): importing a drug
// classification version, loading one for the engine, searching drugs by name, and
// auditing rule evaluations.
//
// Drug master data is Internal data (E-DP-19): no patient data, so no encryption. The
// software never suggests a drug or dose (K-CS-03): search is a lookup by name prefix.
package clinical

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "clinical", Schema: "clinical", FS: sub}
}

// Errors.
var (
	ErrVersionConflict = errors.New("clinical: a classification with this version label and different content exists")
	ErrUnknownVersion  = errors.New("clinical: unknown drug classification version")
	ErrBadSearch       = errors.New("clinical: search text must be 2 to 40 letters, digits, spaces or hyphens")
)

// Module is the clinical module.
type Module struct {
	pool   *pgxpool.Pool
	engine *rules.Engine
}

// New builds the module. The engine is the loaded prescribing rule file (it validates imports).
func New(pool *pgxpool.Pool, engine *rules.Engine) (*Module, error) {
	if pool == nil || engine == nil {
		return nil, errors.New("clinical: a pool and a loaded rules engine are required")
	}
	return &Module{pool: pool, engine: engine}, nil
}

// Engine returns the rules engine.
func (m *Module) Engine() *rules.Engine { return m.engine }

// Actor is who performs an audited action (references only).
type Actor struct {
	Ref  string
	Role string
}

// ImportResult describes an import.
type ImportResult struct {
	VersionID  string
	Version    string
	EntryCount int
	// Created is false when the identical classification (same label, same bytes) was already imported.
	Created bool
}

const (
	actionImport = "clinical.classification.import"
	actionCheck  = "clinical.rules.check"
	resClsVer    = "drug_classification_version"
)

// ImportClassification validates the pack's drugs/classification.yaml against the rule
// file and writes it as a new, immutable classification version, with an audit event in
// the same transaction. A rejected file writes no version; the rejection is audited too.
// Re-importing identical bytes returns the existing version.
func (m *Module) ImportClassification(ctx context.Context, actor Actor, data []byte) (ImportResult, error) {
	cls, err := m.engine.ParseClassification(data)
	if err != nil {
		aerr := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			return ap.Record(ctx, tx, ap.Event{ActorRef: actor.Ref, ActorRole: actor.Role, Action: actionImport,
				ResourceType: resClsVer, ReasonCode: "import_rejected", Result: ap.Failed})
		})
		if aerr != nil {
			return ImportResult{}, errors.Join(err, aerr)
		}
		return ImportResult{}, err
	}

	var out ImportResult
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id, sum string
		switch qerr := tx.QueryRow(ctx, `SELECT id::text, checksum FROM drug_classification_version WHERE version = $1`, cls.Version).Scan(&id, &sum); {
		case qerr == nil:
			if sum != cls.Checksum {
				return ErrVersionConflict
			}
			out = ImportResult{VersionID: id, Version: cls.Version, EntryCount: len(cls.Entries)}
			return nil
		case !errors.Is(qerr, pgx.ErrNoRows):
			return fmt.Errorf("clinical: look up version: %w", qerr)
		}

		id = ids.New()
		if _, err := tx.Exec(ctx, `INSERT INTO drug_classification_version (id, version, status, checksum, ruleset_version, entry_count, imported_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, cls.Version, cls.Status, cls.Checksum, m.engine.Version(), len(cls.Entries), actor.Ref); err != nil {
			if db.IsUniqueViolation(err, "") {
				return ErrVersionConflict
			}
			return fmt.Errorf("clinical: insert version: %w", err)
		}
		for _, en := range cls.Entries {
			d := en.Drug()
			brands, tags := en.BrandNames, en.ConditionTags
			if brands == nil {
				brands = []string{}
			}
			if tags == nil {
				tags = []string{}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO drug_master_entry (classification_id, drug_ref, generic_name, brand_names, strength, form, route,
				in_prohibited, in_list_o, in_list_a, in_list_b, amendment_exception, condition_tags, source_ref)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
				id, en.Ref, en.GenericName, brands, en.Strength, en.Form, en.Route,
				d.Prohibited, d.ListO, d.ListA, d.ListB, en.AmendmentException, tags, en.Source); err != nil {
				return fmt.Errorf("clinical: insert drug: %w", err)
			}
			keys := map[string]bool{rules.NormalizeName(en.GenericName): true}
			for _, b := range brands {
				keys[rules.NormalizeName(b)] = true
			}
			for k := range keys {
				if _, err := tx.Exec(ctx, `INSERT INTO drug_search_key (classification_id, search_key, drug_ref) VALUES ($1,$2,$3)`, id, k, en.Ref); err != nil {
					return fmt.Errorf("clinical: insert search key: %w", err)
				}
			}
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: actor.Ref, ActorRole: actor.Role, Action: actionImport,
			ResourceType: resClsVer, ResourceRef: id, Result: ap.Allowed}); err != nil {
			return err
		}
		out = ImportResult{VersionID: id, Version: cls.Version, EntryCount: len(cls.Entries), Created: true}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return out, nil
}

// LoadClassification reads one classification version into memory for the engine.
func (m *Module) LoadClassification(ctx context.Context, version string) (*rules.MapClassifier, error) {
	var id string
	if err := m.pool.QueryRow(ctx, `SELECT id::text FROM drug_classification_version WHERE version = $1`, version).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUnknownVersion
		}
		return nil, fmt.Errorf("clinical: load version: %w", err)
	}
	rows, err := m.pool.Query(ctx, `SELECT drug_ref, generic_name, in_prohibited, in_list_o, in_list_a, in_list_b, amendment_exception
		FROM drug_master_entry WHERE classification_id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("clinical: load entries: %w", err)
	}
	defer rows.Close()
	var drugs []rules.Drug
	for rows.Next() {
		var d rules.Drug
		if err := rows.Scan(&d.Ref, &d.GenericName, &d.Prohibited, &d.ListO, &d.ListA, &d.ListB, &d.AmendmentException); err != nil {
			return nil, fmt.Errorf("clinical: load entries: %w", err)
		}
		drugs = append(drugs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clinical: load entries: %w", err)
	}
	return rules.NewMapClassifier(version, drugs...), nil
}

// DrugSummary is what the prescription composer may show for a search hit: identity only,
// no list membership and nothing that suggests a choice (K-CS-03).
type DrugSummary struct {
	Ref         string
	GenericName string
	BrandNames  []string
	Strength    string
	Form        string
	Route       string
}

// Search finds drugs in one classification version whose generic or brand name starts with
// prefix (case-insensitive). It is a lookup, not a recommendation: results are ordered by
// name. limit is 1 to 50 (default 20).
func (m *Module) Search(ctx context.Context, version, prefix string, limit int) ([]DrugSummary, error) {
	p := strings.ToLower(strings.TrimSpace(prefix))
	if len(p) < 2 || len(p) > 40 {
		return nil, ErrBadSearch
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ' ' || r == '-') {
			return nil, ErrBadSearch
		}
	}
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 50)
	rows, err := m.pool.Query(ctx, `SELECT e.drug_ref, e.generic_name, e.brand_names, e.strength, e.form, e.route
		FROM drug_classification_version v
		JOIN drug_master_entry e ON e.classification_id = v.id
		WHERE v.version = $1
		  AND EXISTS (SELECT 1 FROM drug_search_key k WHERE k.classification_id = v.id AND k.drug_ref = e.drug_ref AND k.search_key LIKE $2)
		ORDER BY lower(e.generic_name), e.strength, e.drug_ref
		LIMIT $3`, version, p+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("clinical: search: %w", err)
	}
	defer rows.Close()
	out := []DrugSummary{}
	for rows.Next() {
		var s DrugSummary
		if err := rows.Scan(&s.Ref, &s.GenericName, &s.BrandNames, &s.Strength, &s.Form, &s.Route); err != nil {
			return nil, fmt.Errorf("clinical: search: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clinical: search: %w", err)
	}
	return out, nil
}

// RecordCheck writes the audit event for one rule evaluation, in the caller's transaction
// (T18). It holds references and the primary reason code only, never drug or dose content.
// The caller stores the full evaluation with the prescription (RET-02).
func (m *Module) RecordCheck(ctx context.Context, tx pgx.Tx, actor Actor, subjectRef, prescriptionRef string, res rules.Result) error {
	e := ap.Event{ActorRef: actor.Ref, ActorRole: actor.Role, Action: actionCheck, SubjectRef: subjectRef,
		ResourceType: "prescription", ResourceRef: prescriptionRef, Result: ap.Allowed}
	if !res.Allowed {
		e.Result = ap.Denied
		switch {
		case len(res.Reasons) > 0:
			e.ReasonCode = res.Reasons[0].Code
		default:
			for _, it := range res.Items {
				if len(it.Reasons) > 0 {
					e.ReasonCode = it.Reasons[0].Code
					break
				}
			}
		}
	}
	return ap.Record(ctx, tx, e)
}
