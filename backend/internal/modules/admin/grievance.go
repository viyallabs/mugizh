package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// resolveWindow is the time to resolve an item of this kind (grievance-redressal.md).
func (m *Module) resolveWindow(k pub.GrievanceKind) (time.Duration, bool) {
	switch k {
	case pub.KindComplaint:
		return m.cfg.ResolveWithin, true
	case pub.KindContentTakedown:
		return m.cfg.ContentTakedown, true
	case pub.KindOrderTakedown:
		return m.cfg.OrderCompliance, true
	case pub.KindIntimateImage:
		return m.cfg.IntimateImage, true
	case pub.KindLawfulRequest:
		return m.cfg.LawfulRequest, true
	case pub.KindMedicalRecordCopy:
		return m.cfg.MedicalRecordCopy, true
	case pub.KindDataRights:
		return m.cfg.DataRightsResponse, true
	case pub.KindChildAccount:
		return m.cfg.ResolveWithin, true // [CONFIRM] D-42
	}
	return 0, false
}

// deadlines: acknowledgement is due within the acknowledgement time, or earlier when the
// whole window is shorter (2 h and 3 h items).
func (m *Module) deadlines(k pub.GrievanceKind, received time.Time) (ack, due time.Time, ok bool) {
	w, ok := m.resolveWindow(k)
	if !ok {
		return
	}
	a := m.cfg.AckWithin
	if w < a {
		a = w
	}
	return received.Add(a), received.Add(w), true
}

func newReference(kind pub.GrievanceKind) (string, error) {
	t, err := ids.Token(16)
	if err != nil {
		return "", err
	}
	prefix := map[pub.GrievanceKind]string{pub.KindComplaint: "GR", pub.KindContentTakedown: "CT", pub.KindOrderTakedown: "OT", pub.KindIntimateImage: "II",
		pub.KindLawfulRequest: "LR", pub.KindMedicalRecordCopy: "MR", pub.KindDataRights: "DR", pub.KindChildAccount: "CA"}[kind]
	return prefix + "-" + strings.ToUpper(t[:10]), nil
}

const grievanceCols = `id::text, reference, kind, status, coalesce(submitter_ref::text,''), source_ref, received_at, ack_due_at, due_at, acknowledged_at, resolved_at, outcome`

func scanGrievance(s interface{ Scan(...any) error }) (pub.Grievance, error) {
	var g pub.Grievance
	var kind string
	err := s.Scan(&g.ID, &g.Reference, &kind, &g.Status, &g.SubmitterRef, &g.SourceRef, &g.ReceivedAt, &g.AckDueAt, &g.DueAt, &g.AcknowledgedAt, &g.ResolvedAt, &g.Outcome)
	g.Kind = pub.GrievanceKind(kind)
	return g, err
}

func (m *Module) checkText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 4000 {
		return "", fmt.Errorf("%w: text", pub.ErrInvalid)
	}
	if scrub.ContainsProhibitedID(text, m.cfg.ProhibitedValidators) {
		return "", fmt.Errorf("%w: text holds a prohibited identifier", pub.ErrInvalid)
	}
	return text, nil
}

func (m *Module) insertGrievance(ctx context.Context, tx pgx.Tx, kind pub.GrievanceKind, submitter, source, text string, received time.Time, autoAck bool, actor string) (string, error) {
	ack, due, ok := m.deadlines(kind, received)
	if !ok {
		return "", pub.ErrInvalid
	}
	ref, err := newReference(kind)
	if err != nil {
		return "", err
	}
	id := ids.New()
	var sealed any
	if text != "" {
		s, err := crypto.SealService(ctx, m.d.Crypto, "grievance", crypto.AAD{Table: "grievance", Column: "text", RowID: id}, []byte(text))
		if err != nil {
			return "", err
		}
		sealed = s
	}
	var sub, ackedAt any
	status := "open"
	if submitter != "" {
		sub = submitter
	}
	if autoAck {
		status, ackedAt = "acknowledged", received
	}
	tag, err := tx.Exec(ctx, `INSERT INTO grievance (id, reference, kind, status, submitter_ref, source_ref, text_sealed, received_at, ack_due_at, due_at, acknowledged_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, id, ref, string(kind), status, sub, source, sealed, received, ack, due, ackedAt)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", errDuplicate
	}
	if _, err := tx.Exec(ctx, `INSERT INTO grievance_event (id, grievance_id, action, actor_ref, occurred_at) VALUES ($1,$2,'received',$3,$4)`, ids.New(), id, actor, received); err != nil {
		return "", err
	}
	if autoAck {
		if _, err := tx.Exec(ctx, `INSERT INTO grievance_event (id, grievance_id, action, actor_ref, occurred_at) VALUES ($1,$2,'acknowledged',$3,$4)`, ids.New(), id, actor, received); err != nil {
			return "", err
		}
	}
	return id, nil
}

// OpenGrievance records a patient's complaint. The patient is given the reference at once,
// which counts as the acknowledgement (grievance-redressal.md: "you get a reference number
// straight away"); resolution is due in the pack's period.
func (m *Module) OpenGrievance(ctx context.Context, submitterRef, text string) (pub.Grievance, error) {
	if !reUUID.MatchString(submitterRef) {
		return pub.Grievance{}, pub.ErrInvalid
	}
	text, err := m.checkText(text)
	if err != nil {
		return pub.Grievance{}, err
	}
	now := m.now().UTC()
	var id string
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		id, err = m.insertGrievance(ctx, tx, pub.KindComplaint, submitterRef, "", text, now, true, submitterRef)
		if err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: submitterRef, ActorRole: "patient", Action: "admin.grievance_opened", SubjectRef: submitterRef,
			ResourceType: "grievance", ResourceRef: id, Result: ap.Allowed})
	})
	if err != nil {
		return pub.Grievance{}, err
	}
	return m.getGrievance(ctx, id)
}

// MyGrievances lists the patient's own complaints, newest first.
func (m *Module) MyGrievances(ctx context.Context, submitterRef string, limit int) ([]pub.Grievance, error) {
	if !reUUID.MatchString(submitterRef) {
		return nil, pub.ErrInvalid
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := m.pool.Query(ctx, `SELECT `+grievanceCols+` FROM grievance WHERE submitter_ref=$1 ORDER BY received_at DESC, id DESC LIMIT $2`, submitterRef, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pub.Grievance
	for rows.Next() {
		g, err := scanGrievance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (m *Module) getGrievance(ctx context.Context, id string) (pub.Grievance, error) {
	g, err := scanGrievance(m.pool.QueryRow(ctx, `SELECT `+grievanceCols+` FROM grievance WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, pub.ErrNotFound
	}
	return g, err
}

// Intake lets the grievance officer log an item that arrived outside the app (email,
// post, an authority's order): a takedown, an intimate-image report, a lawful request or
// a medical-record copy. The timer starts at receivedAt, the moment it arrived.
func (m *Module) Intake(ctx context.Context, s pub.Staff, kind pub.GrievanceKind, sourceRef, text string, receivedAt time.Time) (pub.Grievance, error) {
	if err := m.holds(ctx, s, "grievance_officer"); err != nil {
		return pub.Grievance{}, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_:\-]{0,80}$`).MatchString(sourceRef) {
		return pub.Grievance{}, pub.ErrInvalid
	}
	if text != "" {
		var err error
		if text, err = m.checkText(text); err != nil {
			return pub.Grievance{}, err
		}
	}
	now := m.now().UTC()
	if receivedAt.IsZero() || receivedAt.After(now) {
		receivedAt = now
	}
	var id string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		id, err = m.insertGrievance(ctx, tx, kind, "", sourceRef, text, receivedAt.UTC(), false, s.Ref)
		if err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.grievance_intake", "", "grievance", id, "", "", "", ap.Allowed)
	})
	if err != nil {
		return pub.Grievance{}, err
	}
	return m.getGrievance(ctx, id)
}

// OnRightsRequestReceived consumes rights.request_received {requestRef, rightsType} from
// the records module and queues it with its timer: 72 hours for a copy of the medical
// record (export), 15 days for every other data right. In-app requests were acknowledged
// on receipt. Idempotent.
func (m *Module) OnRightsRequestReceived(ctx context.Context, e outbox.Event) error {
	ref, typ := e.Payload["requestRef"], e.Payload["rightsType"]
	if !reUUID.MatchString(ref) {
		return fmt.Errorf("%w: request ref", pub.ErrInvalid)
	}
	kind := pub.KindDataRights
	switch typ {
	case "export":
		kind = pub.KindMedicalRecordCopy
	case "access", "correction", "erasure", "nomination":
	default:
		return fmt.Errorf("%w: rights type", pub.ErrInvalid)
	}
	return outbox.Once(ctx, m.pool, "admin.rights_request_received", e, func(tx pgx.Tx) error {
		_, err := m.insertGrievance(ctx, tx, kind, e.SubjectRef, "rights:"+ref, "", e.OccurredAt.UTC(), true, "system")
		if errors.Is(err, errDuplicate) {
			return nil
		}
		return err
	})
}

// OnMinorFlagged opens a CH-12 case when a doctor finds the patient in a consult is a minor
// (consult.minor_flagged; TC-48, E-FAM-04). Support staff then follow the CH-12 runbook;
// nothing is closed or deleted automatically on one doctor's flag. Once only per session.
func (m *Module) OnMinorFlagged(ctx context.Context, e outbox.Event) error {
	patient, session := e.Payload["patientRef"], e.Payload["sessionRef"]
	if !reUUID.MatchString(patient) || !reUUID.MatchString(session) {
		return fmt.Errorf("%w: minor flag refs", pub.ErrInvalid)
	}
	return outbox.Once(ctx, m.pool, "admin.consult_minor_flagged", e, func(tx pgx.Tx) error {
		_, err := m.insertGrievance(ctx, tx, pub.KindChildAccount, patient, "consult:"+session, "", e.OccurredAt.UTC(), false, "system")
		if errors.Is(err, errDuplicate) {
			return nil
		}
		return err
	})
}

// Acknowledge marks an item acknowledged (grievance officer or support).
func (m *Module) Acknowledge(ctx context.Context, s pub.Staff, id string) error {
	if err := m.holds(ctx, s, "grievance_officer", "support"); err != nil {
		return err
	}
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE grievance SET status='acknowledged', acknowledged_at=$2 WHERE id=$1 AND status='open'`, id, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pub.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO grievance_event (id, grievance_id, action, actor_ref, occurred_at) VALUES ($1,$2,'acknowledged',$3,$4)`, ids.New(), id, s.Ref, now); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.grievance_acknowledged", "", "grievance", id, "", "", "", ap.Allowed)
	})
}

// Resolve closes an item with an outcome code; the evidence trail keeps who and when (RET-09).
func (m *Module) Resolve(ctx context.Context, s pub.Staff, id, outcome string) error {
	if err := m.holds(ctx, s, "grievance_officer"); err != nil {
		return err
	}
	if !regexp.MustCompile(`^[a-z_]{1,40}$`).MatchString(outcome) {
		return pub.ErrInvalid
	}
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE grievance SET status='resolved', resolved_at=$2, outcome=$3, acknowledged_at=coalesce(acknowledged_at,$2) WHERE id=$1 AND status<>'resolved'`, id, now, outcome)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pub.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO grievance_event (id, grievance_id, action, actor_ref, outcome, occurred_at) VALUES ($1,$2,'resolved',$3,$4,$5)`, ids.New(), id, s.Ref, outcome, now); err != nil {
			return err
		}
		return m.auditStaff(ctx, tx, s, "admin.grievance_resolved", "", "grievance", id, "", "", "", ap.Allowed)
	})
}

// Open returns an item with its text, for the officer working it.
func (m *Module) Open(ctx context.Context, s pub.Staff, id string) (pub.Grievance, string, error) {
	if err := m.holds(ctx, s, "grievance_officer"); err != nil {
		return pub.Grievance{}, "", err
	}
	g, err := m.getGrievance(ctx, id)
	if err != nil {
		return g, "", err
	}
	var sealed crypto.Sealed[crypto.ServiceKey]
	if err := m.pool.QueryRow(ctx, `SELECT text_sealed FROM grievance WHERE id=$1`, id).Scan(&sealed); err != nil {
		return g, "", err
	}
	text := ""
	if !sealed.IsZero() {
		b, err := crypto.Open(ctx, m.d.Crypto, crypto.AAD{Table: "grievance", Column: "text", RowID: id}, sealed)
		if err != nil {
			return g, "", err
		}
		text = string(b)
	}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return m.auditStaff(ctx, tx, s, "admin.grievance_opened_by_staff", g.SubmitterRef, "grievance", id, "", "", "", ap.Allowed)
	})
	return g, text, err
}

// Queue lists unresolved items, most urgent first, with an opaque cursor.
func (m *Module) Queue(ctx context.Context, s pub.Staff, cursor string, limit int) ([]pub.Grievance, string, error) {
	if err := m.holds(ctx, s, "grievance_officer", "support"); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var at *time.Time
	curID := "00000000-0000-0000-0000-000000000000"
	if cursor != "" {
		us, id, ok := strings.Cut(cursor, ".")
		n, err := strconv.ParseInt(us, 10, 64)
		if !ok || err != nil || !reUUID.MatchString(id) {
			return nil, "", pub.ErrInvalid
		}
		t := time.UnixMicro(n).UTC()
		at, curID = &t, id
	}
	rows, err := m.pool.Query(ctx, `SELECT `+grievanceCols+` FROM grievance WHERE status<>'resolved'
		AND ($1::timestamptz IS NULL OR (due_at, id) > ($1::timestamptz, $2::uuid)) ORDER BY due_at, id LIMIT $3`, at, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []pub.Grievance
	for rows.Next() {
		g, err := scanGrievance(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		l := out[len(out)-1]
		next = strconv.FormatInt(l.DueAt.UnixMicro(), 10) + "." + l.ID
	}
	return out, next, nil
}

// DueSoon lists the alerts for the timers at time now: not acknowledged in time, data
// rights past the pre-alert point (10 days), other items past the alert share of their
// window (80%), breached items, and break-glass justifications that are overdue. It
// changes nothing; the worker sends one alert per item and reason.
func (m *Module) DueSoon(ctx context.Context, now time.Time) ([]pub.Alert, error) {
	now = now.UTC()
	rows, err := m.pool.Query(ctx, `SELECT `+grievanceCols+` FROM grievance WHERE status<>'resolved' ORDER BY due_at, id`)
	if err != nil {
		return nil, err
	}
	var items []pub.Grievance
	for rows.Next() {
		g, err := scanGrievance(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []pub.Alert
	for _, g := range items {
		add := func(reason string) {
			out = append(out, pub.Alert{GrievanceID: g.ID, Reference: g.Reference, Kind: g.Kind, Reason: reason, DueAt: g.DueAt})
		}
		if g.AcknowledgedAt == nil && now.After(g.AckDueAt) {
			add("ack_overdue")
		}
		switch {
		case now.After(g.DueAt):
			add("breached")
		case g.Kind == pub.KindDataRights && !now.Before(g.ReceivedAt.Add(m.cfg.DataRightsAlertAt)):
			add("alert_10_days")
		case g.Kind != pub.KindDataRights:
			window := g.DueAt.Sub(g.ReceivedAt)
			if !now.Before(g.ReceivedAt.Add(time.Duration(float64(window) * m.cfg.AlertFraction))) {
				add("alert_80_percent")
			}
		}
	}
	jr, err := m.pool.Query(ctx, `SELECT id::text, justification_due_at FROM access_request WHERE break_glass AND justified_at IS NULL AND justification_due_at < $1`, now)
	if err != nil {
		return nil, err
	}
	defer jr.Close()
	for jr.Next() {
		var id string
		var due time.Time
		if err := jr.Scan(&id, &due); err != nil {
			return nil, err
		}
		out = append(out, pub.Alert{GrievanceID: id, Reference: "AR-" + strings.ToUpper(strings.ReplaceAll(id, "-", "")[:10]), Reason: "justification_overdue", DueAt: due})
	}
	return out, jr.Err()
}

// Evidence returns the append-only trail of an item (RET-09).
func (m *Module) Evidence(ctx context.Context, s pub.Staff, id string) ([]string, error) {
	if err := m.holds(ctx, s, "grievance_officer"); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `SELECT action || ':' || outcome FROM grievance_event WHERE grievance_id=$1 ORDER BY occurred_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// errDuplicate means the same source item is already queued.
var errDuplicate = errors.New("admin: duplicate queue item")
