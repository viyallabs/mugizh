package archtest

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"testing"
)

// moduleSchemas are the schema names of the module map (module-boundaries-and-domain-model.md §2).
var moduleSchemas = []string{"admin", "audit", "booking", "clinical", "consult", "directory", "identity", "notifications", "payments", "platform", "records", "verification"}

var migrationName = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.(up|down)\.sql$`)

// ---- B1: a module's migrations touch only its own schema ----

func checkB1MigrationSchemas(files []srcFile) []string {
	var v []string
	const pre = "internal/modules/"
	for _, f := range files {
		if f.Mod != modBackend || !f.isSQL() || !strings.HasPrefix(f.Rel, pre) {
			continue
		}
		mod, rest, _ := strings.Cut(strings.TrimPrefix(f.Rel, pre), "/")
		if !strings.HasPrefix(rest, "migrations/") {
			continue
		}
		sql := stripSQLComments(f.Src)
		for _, s := range moduleSchemas {
			if s == mod {
				continue
			}
			re := regexp.MustCompile(`(?i)(^|[^a-z0-9_.])"?` + s + `"?\.[a-z_"]`)
			if re.MatchString(sql) {
				v = append(v, fmt.Sprintf("%s: B1/K-ARC-01 module %q migration references schema %q", f.display(), mod, s))
			}
		}
	}
	return v
}

func TestB1_MigrationSchemas(t *testing.T) { requireClean(t, checkB1MigrationSchemas(loadTree(t))) }

func TestB1_MigrationSchemas_Negative(t *testing.T) {
	bad := []srcFile{be("internal/modules/booking/migrations/0001_x.up.sql", "CREATE TABLE booking.a (id uuid REFERENCES payments.p(id));")}
	requireFires(t, checkB1MigrationSchemas(bad), "payments")
	bad = []srcFile{be("internal/modules/booking/migrations/0001_x.up.sql", "INSERT INTO audit.audit_outbox VALUES (1);")}
	requireFires(t, checkB1MigrationSchemas(bad), "audit")
	ok := []srcFile{
		be("internal/modules/booking/migrations/0001_x.up.sql", "-- payments.p is not touched\nCREATE TABLE booking.a (id uuid, payments_ref uuid);"),
		be("internal/modules/audit/migrations/0001_x.up.sql", "CREATE TABLE audit.e (id uuid);"),
	}
	requireClean(t, checkB1MigrationSchemas(ok))
}

// ---- Every migration has a matching up and down file and a well-formed name ----

func checkMigrationPairs(files []srcFile) []string {
	var v []string
	have := map[string]bool{}
	for _, f := range files {
		if f.Mod != modBackend || !f.isSQL() || path.Base(path.Dir(f.Rel)) != "migrations" {
			continue
		}
		have[f.Rel] = true
	}
	for rel := range have {
		base := path.Base(rel)
		if !migrationName.MatchString(base) {
			v = append(v, "backend/"+rel+": migration name must be NNNN_name.up.sql or NNNN_name.down.sql")
			continue
		}
		var other string
		if strings.HasSuffix(rel, ".up.sql") {
			other = strings.TrimSuffix(rel, ".up.sql") + ".down.sql"
		} else {
			other = strings.TrimSuffix(rel, ".down.sql") + ".up.sql"
		}
		if !have[other] {
			v = append(v, "backend/"+rel+": missing counterpart "+path.Base(other))
		}
	}
	return v
}

func TestMigrations_UpAndDownPairs(t *testing.T) { requireClean(t, checkMigrationPairs(loadTree(t))) }

func TestMigrations_UpAndDownPairs_Negative(t *testing.T) {
	requireFires(t, checkMigrationPairs([]srcFile{be("internal/modules/booking/migrations/0001_x.up.sql", "x")}), "missing counterpart")
	requireFires(t, checkMigrationPairs([]srcFile{be("internal/modules/booking/migrations/0001_x.down.sql", "x")}), "missing counterpart")
	requireFires(t, checkMigrationPairs([]srcFile{be("internal/modules/booking/migrations/x.sql", "x")}), "migration name")
	requireClean(t, checkMigrationPairs([]srcFile{
		be("internal/modules/booking/migrations/0001_x.up.sql", "x"),
		be("internal/modules/booking/migrations/0001_x.down.sql", "x"),
	}))
}
