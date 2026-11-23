package reviews

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// CreateInput is the body of a review. Text is optional.
type CreateInput struct {
	Rating int
	Text   *string
}

// Review is a patient's own review. Text is set only for its author and only when there is one.
type Review struct {
	ID, AppointmentRef, DoctorRef string
	Rating                        int
	Text                          *string
	TextState                     string
	CreatedAt                     time.Time
}

func textAAD(id string) crypto.AAD { return crypto.AAD{Table: "review", Column: "text", RowID: id} }

func (m *Module) audit(ctx context.Context, tx pgx.Tx, a Actor, action, reviewID string) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: a.UserID, ActorRole: a.Role, Action: action, SubjectRef: a.UserID,
		ResourceType: "review", ResourceRef: reviewID, Result: ap.Allowed, RequestID: a.RequestID})
}

func checkActor(a Actor) error {
	if a.Role != rolePatient {
		return problem.Forbidden
	}
	if !ids.Valid(a.UserID) {
		return problem.Unauthenticated
	}
	return nil
}

type visitRow struct {
	patientRef, doctorRef string
	completedAt           time.Time
	reviewed              bool
}

func (m *Module) loadVisit(ctx context.Context, appt string) (visitRow, bool, error) {
	var v visitRow
	err := m.pool.QueryRow(ctx, `SELECT v.patient_ref::text, v.doctor_ref::text, v.completed_at, EXISTS (SELECT 1 FROM review r WHERE r.appointment_ref = v.appointment_ref)
		FROM visit v WHERE v.appointment_ref=$1`, appt).Scan(&v.patientRef, &v.doctorRef, &v.completedAt, &v.reviewed)
	if errors.Is(err, pgx.ErrNoRows) {
		return visitRow{}, false, nil
	}
	return v, err == nil, err
}

// Create records the patient's review of a completed appointment (S-209, TC-51). Unknown or
// someone else's appointment: not found. Not completed, outside the window: review.not_eligible.
// Already reviewed: review.already_reviewed. A refused rating or text: 422 with field codes.
// The star rating counts in the doctor's aggregate in the same transaction; the text is sealed with
// the patient's key and waits for moderation.
func (m *Module) Create(ctx context.Context, a Actor, appointmentRef string, in CreateInput) (Review, error) {
	if err := checkActor(a); err != nil {
		return Review{}, err
	}
	if !ids.Valid(appointmentRef) {
		return Review{}, problem.NotFound
	}
	v, found, err := m.loadVisit(ctx, appointmentRef)
	if err != nil {
		return Review{}, err
	}
	if !found {
		// No completed visit on record. Tell "not mine / unknown" (404) from "mine but not completed" (409)
		// through booking's public interface, so a visit that never completed is not reviewable (E-REV-01).
		info, err := m.d.Booking.Get(ctx, appointmentRef)
		if errors.Is(err, bp.ErrNotFound) || (err == nil && info.PatientRef != a.UserID) {
			return Review{}, problem.NotFound
		}
		if err != nil {
			return Review{}, err
		}
		return Review{}, ErrNotEligible
	}
	if v.patientRef != a.UserID {
		return Review{}, problem.NotFound
	}
	// CD-08: a doctor who booked their own practice cannot rate themselves (manipulated reviews).
	if v.doctorRef == a.UserID {
		return Review{}, ErrNotEligible
	}
	if v.reviewed {
		return Review{}, ErrAlreadyReviewed
	}
	now := m.now()
	if now.Sub(v.completedAt) > m.cfg.Window {
		return Review{}, ErrNotEligible
	}

	if in.Rating < 1 || in.Rating > 5 {
		return Review{}, ErrInvalid.WithFields(problem.FieldError{Field: fieldRat, Code: "out_of_range"})
	}
	text := ""
	if in.Text != nil {
		text = normaliseText(*in.Text)
	}
	if text != "" {
		if code := m.checkText(text); code != "" {
			return Review{}, ErrInvalid.WithFields(problem.FieldError{Field: fieldText, Code: code})
		}
	}

	id := ids.New()
	var sealed crypto.Sealed[crypto.PatientKey]
	state := rp.TextStateNone
	if text != "" {
		if sealed, err = crypto.SealPatient(ctx, m.d.Crypto, a.UserID, textAAD(id), []byte(text)); err != nil {
			return Review{}, fmt.Errorf("reviews: seal text: %w", err)
		}
		state = rp.TextStatePendingModeration
	}
	var createdAt time.Time
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO review (id, appointment_ref, patient_ref, doctor_ref, rating, text_sealed, text_state, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (appointment_ref) DO NOTHING`,
			id, appointmentRef, a.UserID, v.doctorRef, in.Rating, sealed, state, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrAlreadyReviewed
		}
		if _, err := tx.Exec(ctx, `INSERT INTO doctor_rating (doctor_ref, n, sum) VALUES ($1, 1, $2)
			ON CONFLICT (doctor_ref) DO UPDATE SET n = doctor_rating.n + 1, sum = doctor_rating.sum + EXCLUDED.sum`, v.doctorRef, in.Rating); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT created_at FROM review WHERE id=$1`, id).Scan(&createdAt); err != nil {
			return err
		}
		return m.audit(ctx, tx, a, "review.create", id)
	})
	if err != nil {
		return Review{}, err
	}
	r := Review{ID: id, AppointmentRef: appointmentRef, DoctorRef: v.doctorRef, Rating: in.Rating, TextState: state, CreatedAt: createdAt}
	if text != "" {
		r.Text = &text
	}
	return r, nil
}

// Withdraw deletes the patient's own review and removes it from the doctor's aggregate. Someone
// else's or an unknown review is not found.
func (m *Module) Withdraw(ctx context.Context, a Actor, reviewID string) error {
	if err := checkActor(a); err != nil {
		return err
	}
	if !ids.Valid(reviewID) {
		return problem.NotFound
	}
	return m.inTx(ctx, func(tx pgx.Tx) error {
		var doctor string
		var rating int
		err := tx.QueryRow(ctx, `DELETE FROM review WHERE id=$1 AND patient_ref=$2 RETURNING doctor_ref::text, rating`, reviewID, a.UserID).Scan(&doctor, &rating)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem.NotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE doctor_rating SET n = n - 1, sum = sum - $2 WHERE doctor_ref=$1`, doctor, rating); err != nil {
			return err
		}
		return m.audit(ctx, tx, a, "review.withdraw", reviewID)
	})
}

// ListMine lists the patient's own reviews, newest first, with a cursor. The text is opened for its
// author only.
func (m *Module) ListMine(ctx context.Context, a Actor, limit int, cursor string) ([]Review, string, error) {
	if err := checkActor(a); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{a.UserID, limit + 1}
	where := ``
	if cursor != "" {
		ns, id, ok := parseCursor(cursor)
		if !ok {
			return nil, "", problem.BadRequest
		}
		args = append(args, time.Unix(0, ns).UTC(), id)
		where = ` AND (created_at, id) < ($3, $4::uuid)`
	}
	rows, err := m.pool.Query(ctx, `SELECT id::text, appointment_ref::text, doctor_ref::text, rating, text_sealed, text_state, created_at
		FROM review WHERE patient_ref=$1`+where+` ORDER BY created_at DESC, id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, "", err
	}
	type row struct {
		r      Review
		sealed crypto.Sealed[crypto.PatientKey]
	}
	var got []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.r.ID, &x.r.AppointmentRef, &x.r.DoctorRef, &x.r.Rating, &x.sealed, &x.r.TextState, &x.r.CreatedAt); err != nil {
			rows.Close()
			return nil, "", err
		}
		got = append(got, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(got) > limit {
		got = got[:limit]
		last := got[len(got)-1].r
		next = strconv.FormatInt(last.CreatedAt.UnixNano(), 10) + ":" + last.ID
	}
	out := make([]Review, 0, len(got))
	for _, x := range got {
		if !x.sealed.IsZero() {
			plain, err := crypto.Open(ctx, m.d.Crypto, textAAD(x.r.ID), x.sealed)
			if err != nil {
				// The key may be gone (erasure in progress). Never log the text or the error detail beyond its kind.
				m.log.Warn("reviews.text_unreadable", logging.Err(err))
			} else {
				t := string(plain)
				x.r.Text = &t
			}
		}
		out = append(out, x.r)
	}
	return out, next, nil
}

func parseCursor(c string) (int64, string, bool) {
	a, b, ok := strings.Cut(c, ":")
	if !ok || !ids.Valid(b) {
		return 0, "", false
	}
	n, err := strconv.ParseInt(a, 10, 64)
	return n, b, err == nil
}

// Rating returns the doctor's public rating: the count always, and the Bayesian average
// (C x m + sum) / (C + n) on the 1 to 5 scale, rounded to one decimal, when there is at least one
// review (S-214). m is the platform mean once the platform has MinPlatformRatings ratings, else
// PriorMean. Nothing commercial is an input (CD-05). A doctor who is not Verified, or unknown, is
// not found (K-CS-01).
func (m *Module) Rating(ctx context.Context, doctorRef string) (rp.Rating, error) {
	if !ids.Valid(doctorRef) {
		return rp.Rating{}, problem.NotFound
	}
	ok, err := m.d.Verifier.IsVerified(ctx, doctorRef)
	if err != nil {
		return rp.Rating{}, err
	}
	if !ok {
		return rp.Rating{}, problem.NotFound
	}
	var n int
	var sum int64
	err = m.pool.QueryRow(ctx, `SELECT n, sum FROM doctor_rating WHERE doctor_ref=$1`, doctorRef).Scan(&n, &sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return rp.Rating{}, nil
	}
	if err != nil {
		return rp.Rating{}, err
	}
	if n <= 0 {
		return rp.Rating{Count: 0}, nil
	}
	mean := m.cfg.PriorMean
	var total, totalSum int64
	if err := m.pool.QueryRow(ctx, `SELECT COALESCE(sum(n), 0)::bigint, COALESCE(sum(sum), 0)::bigint FROM doctor_rating`).Scan(&total, &totalSum); err != nil {
		return rp.Rating{}, err
	}
	if total >= int64(m.cfg.MinPlatformRatings) {
		mean = float64(totalSum) / float64(total)
	}
	v := Bayesian(m.cfg.BayesianC, mean, n, sum)
	return rp.Rating{Count: n, Value: &v}, nil
}

// Bayesian is (C x m + sum) / (C + n) rounded to one decimal (docs/arch/algorithms.md).
func Bayesian(c, mean float64, n int, sum int64) float64 {
	v := (c*mean + float64(sum)) / (c + float64(n))
	return math.Round(v*10) / 10
}
