package audit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
)

var _ ap.AccessLogReader = (*Module)(nil)

const readPrefix = "records.read."

// AccessLogFor returns allowed reads of subjectRef's record by people, newest first
// (S-170). Only events already moved into the chained store are visible. System actors,
// unknown roles, the patient themself and every denial are left out.
func (m *Module) AccessLogFor(ctx context.Context, subjectRef, cursor string, limit int) ([]ap.AccessEntry, string, error) {
	if subjectRef == "" || len(subjectRef) > 80 {
		return nil, "", ap.ErrInvalid
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var curAt *time.Time
	curID := "00000000-0000-0000-0000-000000000000"
	if cursor != "" {
		us, id, ok := strings.Cut(cursor, ".")
		n, err := strconv.ParseInt(us, 10, 64)
		if !ok || err != nil || len(id) != 36 {
			return nil, "", fmt.Errorf("%w: cursor", ap.ErrInvalid)
		}
		t := time.UnixMicro(n).UTC()
		curAt, curID = &t, id
	}
	rows, err := m.pool.Query(ctx, `SELECT event_id::text, occurred_at, actor_ref, actor_role, action, purpose
		FROM audit_event
		WHERE subject_ref=$1 AND action LIKE 'records.read.%' AND result='allowed'
		  AND actor_role NOT IN ('system','unknown','patient','') AND actor_ref<>$1
		  AND ($2::timestamptz IS NULL OR (occurred_at, event_id) < ($2::timestamptz, $3::uuid))
		ORDER BY occurred_at DESC, event_id DESC LIMIT $4`, subjectRef, curAt, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []ap.AccessEntry
	for rows.Next() {
		var e ap.AccessEntry
		var action string
		if err := rows.Scan(&e.EventID, &e.OccurredAt, &e.ActorRef, &e.ActorRole, &action, &e.Purpose); err != nil {
			return nil, "", err
		}
		e.OccurredAt = e.OccurredAt.UTC()
		e.Kind = strings.TrimPrefix(action, readPrefix)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(last.OccurredAt.UnixMicro(), 10) + "." + last.EventID
	}
	return out, next, nil
}
