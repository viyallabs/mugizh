package itest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
)

// B10 / K-ARC-02 contract test: scheduler-core's schema has no free-text columns except rrule and time_zone.
// Every other text column must be an opaque reference or an enum-like value, which means a CHECK constraint
// restricts it. The full column list is also pinned, so adding a column is a visible, reviewed change.
func TestB10SchedulerSchemaHasNoFreeText(t *testing.T) {
	ctx := context.Background()
	env := New(t)
	rows, err := env.Owner.Query(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'scheduler' AND table_name <> 'schema_migrations' ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ table, name, typ string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.name, &c.typ); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()

	checks := map[string]string{} // table -> all CHECK definitions
	crows, err := env.Owner.Query(ctx, `SELECT c.relname, pg_get_constraintdef(k.oid) FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'scheduler' AND k.contype = 'c'`)
	if err != nil {
		t.Fatal(err)
	}
	for crows.Next() {
		var tbl, def string
		if err := crows.Scan(&tbl, &def); err != nil {
			t.Fatal(err)
		}
		checks[tbl] += def + "\n"
	}
	crows.Close()

	var got []string
	for _, c := range cols {
		got = append(got, c.table+"."+c.name+":"+c.typ)
		switch c.typ {
		case "text", "character varying", "character":
			if c.name == "rrule" || c.name == "time_zone" {
				continue
			}
			if !strings.Contains(checks[c.table], c.name) {
				t.Errorf("%s.%s is free text: it must be constrained by a CHECK (enum or opaque reference)", c.table, c.name)
			}
		case "json", "jsonb", "xml", "ARRAY", "tsvector", "USER-DEFINED":
			t.Errorf("%s.%s has type %s, which can hold arbitrary content", c.table, c.name, c.typ)
		}
	}
	sort.Strings(got)
	want := []string{
		"activities.app_ref:text", "activities.created_at:timestamp with time zone", "activities.ends_at:timestamp with time zone",
		"activities.external_ref:text", "activities.generated_through:timestamp with time zone", "activities.id:uuid",
		"activities.rrule:text", "activities.starts_at:timestamp with time zone", "activities.state:text",
		"activities.subject_ref:uuid", "activities.time_zone:text", "activities.type_key:text",
		"activities.updated_at:timestamp with time zone", "activities.version:integer",
		"idempotency_keys.activity_id:uuid", "idempotency_keys.created_at:timestamp with time zone",
		"idempotency_keys.idem_key:uuid", "idempotency_keys.request_hash:bytea",
		"occurrences.acknowledged_at:timestamp with time zone", "occurrences.activity_id:uuid",
		"occurrences.created_at:timestamp with time zone", "occurrences.dispatched_at:timestamp with time zone",
		"occurrences.due_at:timestamp with time zone", "occurrences.enqueued_at:timestamp with time zone", "occurrences.id:uuid",
		"occurrences.state:text", "occurrences.subject_ref:uuid",
		"occurrences.action:text", "occurrences.acted_at:timestamp with time zone", "occurrences.notify_at:timestamp with time zone", "occurrences.snooze_count:integer",
		"occurrence_actions.client_action_id:uuid", "occurrence_actions.occurrence_id:uuid", "occurrence_actions.action:text",
		"occurrence_actions.acted_at:timestamp with time zone", "occurrence_actions.snooze_minutes:integer", "occurrence_actions.created_at:timestamp with time zone",
		"subjects.created_at:timestamp with time zone", "subjects.subject_ref:uuid",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the scheduler schema changed; review it against K-ARC-02 and update this list\ngot:\n%s", strings.Join(got, "\n"))
	}
	// The enum-like columns really reject anything else.
	for _, q := range []string{
		`INSERT INTO scheduler.subjects(subject_ref) VALUES (gen_random_uuid())`,
	} {
		if _, err := env.App.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	bad := fmt.Sprintf(`INSERT INTO scheduler.activities (id, subject_ref, app_ref, external_ref, type_key, rrule, time_zone, starts_at, generated_through)
		SELECT gen_random_uuid(), subject_ref, 'core', 'has a space and free text', 'med.dose', 'FREQ=DAILY', 'UTC', now(), now() FROM scheduler.subjects LIMIT 1`)
	if _, err := env.App.Exec(ctx, bad); err == nil {
		t.Fatal("external_ref must reject free text")
	}
	bad = `INSERT INTO scheduler.activities (id, subject_ref, app_ref, external_ref, type_key, rrule, time_zone, starts_at, generated_through)
		SELECT gen_random_uuid(), subject_ref, 'core', 'rx1', 'Aspirin 75 mg', 'FREQ=DAILY', 'UTC', now(), now() FROM scheduler.subjects LIMIT 1`
	if _, err := env.App.Exec(ctx, bad); err == nil {
		t.Fatal("type_key must reject anything outside the closed enum")
	}
}

// The application's login role has least privilege (decision D-02): data rights only in its own schemas.
func TestApplicationRoleHasLeastPrivilege(t *testing.T) {
	ctx := context.Background()
	env := New(t)
	for _, q := range []string{
		`CREATE TABLE scheduler.extra (id int)`,
		`CREATE TABLE public.extra (id int)`,
		`CREATE SCHEMA other`,
		`TRUNCATE scheduler.occurrences`,
		`DROP TABLE scheduler.occurrences`,
		`ALTER TABLE scheduler.activities ADD COLUMN notes text`,
		`SELECT * FROM scheduler.schema_migrations`,
		`CREATE ROLE sneaky`,
	} {
		_, err := env.App.Exec(ctx, q)
		if err == nil || !db.IsInsufficientPrivilege(err) {
			t.Errorf("%q should be refused with insufficient_privilege, got %v", q, err)
		}
	}
	for _, q := range []string{
		`SELECT count(*) FROM scheduler.occurrences`,
		`SELECT count(*) FROM scheduler_queue.river_job`,
		`DELETE FROM scheduler.subjects WHERE false`,
	} {
		if _, err := env.App.Exec(ctx, q); err != nil {
			t.Errorf("%q should be allowed: %v", q, err)
		}
	}
	var super bool
	if err := env.App.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil || super {
		t.Fatalf("the login role must not be superuser/createdb/createrole: %v %v", super, err)
	}
}
