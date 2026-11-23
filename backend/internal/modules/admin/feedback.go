package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// In-app feedback and bug reports (S-189, data inventory 4.11). The message is sealed with the
// sender's own key (K-DP-03), so erasure crypto-shreds it (K-DP-07); it is never logged or
// audited (K-DP-04). Only codes travel beside it: no health data, IDs, logs or screenshots.

// Problems. A refused message or field answers 422 with field codes so the person can edit it.
var (
	ErrFeedbackInvalid = problem.New(http.StatusUnprocessableEntity, "feedback.invalid", "The feedback is not valid")
	ErrFeedbackLimit   = problem.New(http.StatusTooManyRequests, "feedback.limit", "Too many feedback items today; try again tomorrow")
)

// Field codes for refused input.
const (
	fbRequired    = "required"
	fbInvalid     = "invalid"
	fbControl     = "control_characters"
	fbTooLong     = "too_long"
	fbIdentifier  = "contains_identifier"
	fbContact     = "contains_contact_detail"
	fbLinkMarkup  = "contains_link_or_markup"
	feedbackRunes = 2000
)

var (
	reFbScreen   = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,60}$`)
	reFbVersion  = regexp.MustCompile(`^[0-9A-Za-z.+\-]{1,40}$`)
	reFbOS       = regexp.MustCompile(`^[0-9A-Za-z.+\-]{1,20}$`)
	reFbErrCode  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
	fbPlatforms  = []string{"android", "ios", "web"}
	fbNetworks   = []string{"wifi", "cellular", "none", "unknown"}
	fbKinds      = []string{pub.FeedbackKindFeedback, pub.FeedbackKindBug}
	fbSetStatus  = []string{pub.FeedbackTriaged, pub.FeedbackClosed}
	feedbackCols = `id::text, reference, kind, message_sealed, coalesce(screen,''), coalesce(app_version,''), coalesce(platform,''), diagnostics, status, received_at`
)

// checkFeedbackMessage returns the code of the first check the (trimmed) message fails, or "".
// The message is never echoed or logged.
func (m *Module) checkFeedbackMessage(s string) string {
	if s == "" {
		return fbRequired
	}
	if !utf8.ValidString(s) {
		return fbInvalid
	}
	for _, r := range s {
		// Newlines and tabs are fine; other control characters and invisible format characters
		// (zero-width, bidirectional override) hide links and numbers from the checks below.
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fbControl
		}
	}
	if utf8.RuneCountInString(s) > feedbackRunes {
		return fbTooLong
	}
	// Strict checks: staff read this text, so digits split by punctuation or written in another
	// script are still found, and bare domains count as links (review of S-189).
	if scrub.ContainsProhibitedIDStrict(s, m.cfg.ProhibitedValidators) { // K-DP-05
		return fbIdentifier
	}
	if scrub.ContainsPhoneNumberStrict(s) || scrub.ContainsEmail(s) {
		return fbContact
	}
	// A link in a bug report is refused so phishing links never reach staff.
	if scrub.HasLinkOrMarkup(s) {
		return fbLinkMarkup
	}
	return ""
}

// validateFeedback returns the trimmed message and every refused field.
func (m *Module) validateFeedback(in pub.FeedbackInput) (string, []problem.FieldError) {
	var bad []problem.FieldError
	if !slices.Contains(fbKinds, in.Kind) {
		bad = append(bad, problem.FieldError{Field: "kind", Code: fbInvalid})
	}
	msg := strings.TrimSpace(in.Message)
	if c := m.checkFeedbackMessage(msg); c != "" {
		bad = append(bad, problem.FieldError{Field: "message", Code: c})
	}
	if in.Screen != "" && !reFbScreen.MatchString(in.Screen) {
		bad = append(bad, problem.FieldError{Field: "screen", Code: fbInvalid})
	}
	if in.AppVersion != "" && !reFbVersion.MatchString(in.AppVersion) {
		bad = append(bad, problem.FieldError{Field: "appVersion", Code: fbInvalid})
	}
	// Code fields are free enough to carry a number: refuse identity and phone numbers there too
	// (K-DP-05; review of S-189).
	for field, v := range map[string]string{"appVersion": in.AppVersion, "diagnostics.osVersion": diagField(in.Diagnostics, "os"),
		"diagnostics.lastErrorCode": diagField(in.Diagnostics, "err")} {
		if v != "" && (scrub.ContainsProhibitedIDStrict(v, m.cfg.ProhibitedValidators) || scrub.ContainsPhoneNumberStrict(v)) {
			bad = append(bad, problem.FieldError{Field: field, Code: fbIdentifier})
		}
	}
	if in.Platform != "" && !slices.Contains(fbPlatforms, in.Platform) {
		bad = append(bad, problem.FieldError{Field: "platform", Code: fbInvalid})
	}
	if d := in.Diagnostics; d != nil {
		if d.OSVersion != "" && !reFbOS.MatchString(d.OSVersion) {
			bad = append(bad, problem.FieldError{Field: "diagnostics.osVersion", Code: fbInvalid})
		}
		if d.NetworkType != "" && !slices.Contains(fbNetworks, d.NetworkType) {
			bad = append(bad, problem.FieldError{Field: "diagnostics.networkType", Code: fbInvalid})
		}
		if d.LastErrorCode != "" && (len(d.LastErrorCode) > 60 || !reFbErrCode.MatchString(d.LastErrorCode)) {
			bad = append(bad, problem.FieldError{Field: "diagnostics.lastErrorCode", Code: fbInvalid})
		}
	}
	return msg, bad
}

func feedbackAAD(id string) crypto.AAD {
	return crypto.AAD{Table: "feedback", Column: "message", RowID: id}
}

func (m *Module) feedbackPerDay() int {
	if m.cfg.FeedbackPerDay > 0 {
		return m.cfg.FeedbackPerDay
	}
	return 10
}

func (m *Module) feedbackRetention() time.Duration {
	if m.cfg.FeedbackRetention > 0 {
		return m.cfg.FeedbackRetention
	}
	return 365 * 24 * time.Hour // [CONFIRM] 1 year from receipt
}

// SubmitFeedback stores one feedback item or bug report from a signed-in person. At most
// FeedbackPerDay items per person in any 24 hours: the count and the insert run under one
// per-person lock, so parallel requests cannot pass the limit.
func (m *Module) SubmitFeedback(ctx context.Context, u pub.User, in pub.FeedbackInput) (pub.FeedbackReceipt, error) {
	if !reUUID.MatchString(u.Ref) {
		return pub.FeedbackReceipt{}, pub.ErrForbidden
	}
	msg, bad := m.validateFeedback(in)
	if len(bad) > 0 {
		return pub.FeedbackReceipt{}, ErrFeedbackInvalid.WithFields(bad...)
	}
	var diag any
	if in.Diagnostics != nil {
		b, err := json.Marshal(in.Diagnostics)
		if err != nil {
			return pub.FeedbackReceipt{}, err
		}
		diag = string(b)
	}
	ref, err := ids.Token(16)
	if err != nil {
		return pub.FeedbackReceipt{}, err
	}
	now := m.now().UTC()
	id := ids.New()
	rec := pub.FeedbackReceipt{ID: id, Reference: "FB-" + strings.ToUpper(ref[:10]), ReceivedAt: now}
	limited := false
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		limited = false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, u.Ref); err != nil {
			return err
		}
		var recent int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM feedback WHERE user_ref=$1 AND received_at > $2`, u.Ref, now.Add(-24*time.Hour)).Scan(&recent); err != nil {
			return err
		}
		if recent >= m.feedbackPerDay() {
			limited = true
			return nil
		}
		sealed, err := crypto.SealPatient(ctx, m.d.Crypto, u.Ref, feedbackAAD(id), []byte(msg))
		if err != nil {
			return err
		}
		// The reference has 40 random bits; on the rare clash, draw a new one (review of S-189).
		inserted := false
		for try := 0; try < 5 && !inserted; try++ {
			if try > 0 {
				r, err := ids.Token(16)
				if err != nil {
					return err
				}
				rec.Reference = "FB-" + strings.ToUpper(r[:10])
			}
			tag, err := tx.Exec(ctx, `INSERT INTO feedback (id, reference, user_ref, kind, message_sealed, screen, app_version, platform, diagnostics, status, received_at)
				VALUES ($1,$2,$3,$4,$5,nullif($6,''),nullif($7,''),nullif($8,''),$9::jsonb,'new',$10) ON CONFLICT (reference) DO NOTHING`,
				id, rec.Reference, u.Ref, in.Kind, sealed, in.Screen, in.AppVersion, in.Platform, diag, now)
			if err != nil {
				return err
			}
			inserted = tag.RowsAffected() == 1
		}
		if !inserted {
			return errors.New("admin: no free feedback reference")
		}
		// The audit event carries references only, never the message.
		return ap.Record(ctx, tx, ap.Event{ActorRef: u.Ref, ActorRole: u.Role, Action: "admin.feedback_submitted", SubjectRef: u.Ref,
			ResourceType: "feedback", ResourceRef: id, Result: ap.Allowed, RequestID: u.RequestID})
	})
	if err != nil {
		return pub.FeedbackReceipt{}, err
	}
	if limited {
		return pub.FeedbackReceipt{}, ErrFeedbackLimit
	}
	return rec, nil
}

func parseFeedbackCursor(c string) (time.Time, string, bool) {
	a, b, ok := strings.Cut(c, ".")
	n, err := strconv.ParseInt(a, 10, 64)
	if !ok || err != nil || !reUUID.MatchString(b) {
		return time.Time{}, "", false
	}
	return time.UnixMicro(n).UTC(), b, true
}

func feedbackCursor(t time.Time, id string) string {
	return strconv.FormatInt(t.UnixMicro(), 10) + "." + id
}

func decodeDiagnostics(raw []byte) (*pub.FeedbackDiagnostics, error) {
	if raw == nil {
		return nil, nil
	}
	var d pub.FeedbackDiagnostics
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// feedbackPage runs the paged query shared by the user's list and the staff triage list.
type feedbackRow struct {
	item   pub.FeedbackItem
	sealed crypto.Sealed[crypto.PatientKey]
}

func (m *Module) feedbackPage(ctx context.Context, where string, arg any, limit int, cursor string) ([]feedbackRow, string, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	args := []any{limit + 1}
	cur := ``
	if where != "" {
		args = append(args, arg)
		where = ` AND ` + where + `=$2`
	}
	if cursor != "" {
		t, id, ok := parseFeedbackCursor(cursor)
		if !ok {
			return nil, "", pub.ErrInvalid
		}
		args = append(args, t, id)
		cur = ` AND (received_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `::uuid)`
	}
	rows, err := m.pool.Query(ctx, `SELECT `+feedbackCols+` FROM feedback WHERE true`+where+cur+` ORDER BY received_at DESC, id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []feedbackRow
	for rows.Next() {
		var x feedbackRow
		var raw []byte
		it := &x.item
		if err := rows.Scan(&it.ID, &it.Reference, &it.Kind, &x.sealed, &it.Screen, &it.AppVersion, &it.Platform, &raw, &it.Status, &it.ReceivedAt); err != nil {
			return nil, "", err
		}
		if it.Diagnostics, err = decodeDiagnostics(raw); err != nil {
			return nil, "", err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		l := out[len(out)-1].item
		next = feedbackCursor(l.ReceivedAt, l.ID)
	}
	return out, next, nil
}

// MyFeedback lists the person's own items, newest first, with their own message.
func (m *Module) MyFeedback(ctx context.Context, u pub.User, limit int, cursor string) ([]pub.FeedbackItem, string, error) {
	if !reUUID.MatchString(u.Ref) {
		return nil, "", pub.ErrForbidden
	}
	rows, next, err := m.feedbackPage(ctx, "user_ref", u.Ref, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]pub.FeedbackItem, 0, len(rows))
	for _, x := range rows {
		b, err := crypto.Open(ctx, m.d.Crypto, feedbackAAD(x.item.ID), x.sealed)
		if errors.Is(err, crypto.ErrKeyDestroyed) {
			// Erasure finished for this person: nothing to show (never log the message).
			m.log.Warn("admin.feedback_unreadable")
		} else if err != nil {
			return nil, "", err // a key outage answers 503 (TM-39), never a silent blank
		} else {
			x.item.Message = string(b)
		}
		out = append(out, x.item)
	}
	return out, next, nil
}

// FeedbackQueue lists feedback for triage, newest first, WITHOUT the message (support only).
//
// Reading the message is deliberately not built: whether staff may read user-written text
// needs a founder decision (K-SEC-08, blockers F8). Until then support triages on the kind,
// screen, app version, platform, diagnostics codes and status alone.
func (m *Module) FeedbackQueue(ctx context.Context, s pub.Staff, cursor string, limit int) ([]pub.FeedbackTriage, string, error) {
	if err := m.holds(ctx, s, "support"); err != nil {
		return nil, "", err
	}
	rows, next, err := m.feedbackPage(ctx, "", nil, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]pub.FeedbackTriage, 0, len(rows))
	for _, x := range rows {
		i := x.item
		out = append(out, pub.FeedbackTriage{ID: i.ID, Reference: i.Reference, Kind: i.Kind, Screen: i.Screen, AppVersion: i.AppVersion,
			Platform: i.Platform, Diagnostics: i.Diagnostics, Status: i.Status, ReceivedAt: i.ReceivedAt})
	}
	return out, next, nil
}

// SetFeedbackStatus marks an item triaged or closed (support). Closed is final. Setting the
// status an item already has changes nothing. The change is audited.
func (m *Module) SetFeedbackStatus(ctx context.Context, s pub.Staff, id, status string) error {
	if err := m.holds(ctx, s, "support"); err != nil {
		return err
	}
	if !reUUID.MatchString(id) || !slices.Contains(fbSetStatus, status) {
		return pub.ErrInvalid
	}
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT status FROM feedback WHERE id=$1 FOR UPDATE`, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return pub.ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur == status {
			return nil
		}
		if cur == pub.FeedbackClosed {
			return pub.ErrInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE feedback SET status=$2, status_changed_at=$3 WHERE id=$1`, id, status, now); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.feedback_status_set", "", "feedback", id, status, "", "", ap.Allowed)
	})
}

// PurgeFeedback deletes feedback older than the retention period (1 year from receipt
// [CONFIRM]; data inventory 4.11, DEL-JOB). It returns how many items it deleted.
// The worker calls it once a day (admin.purge_feedback).
func (m *Module) PurgeFeedback(ctx context.Context) (int, error) {
	tag, err := m.pool.Exec(ctx, `DELETE FROM feedback WHERE received_at < $1`, m.now().UTC().Add(-m.feedbackRetention()))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// diagField returns one diagnostics code field, or "" when there are no diagnostics.
func diagField(d *pub.FeedbackDiagnostics, which string) string {
	if d == nil {
		return ""
	}
	if which == "os" {
		return d.OSVersion
	}
	return d.LastErrorCode
}
