// Package public is the reviews module's interface (module-boundaries section 2, F15). Directory
// ranking (S-162) and the composition root use only this package.
//
// Event payloads carry references and codes only (module-boundaries section 4).
package public

import "context"

// Subscriber names for the outbox bus (one per handler, unique).
const (
	// SubscriberAppointmentCompleted builds the eligibility read model from booking's
	// appointment.completed (rule B5).
	SubscriberAppointmentCompleted = "reviews.on_appointment_completed"
)

// Review text states. A rating with no text is "none". Text starts as pending_moderation and is
// never published until the human moderation of S-213 exists.
const (
	TextStateNone              = "none"
	TextStatePendingModeration = "pending_moderation"
)

// Rating is a doctor's public rating. Count is always set. Value is the Bayesian average on the
// 1 to 5 scale rounded to one decimal, or nil when Count is 0 (CD-05: nothing commercial feeds it).
type Rating struct {
	Count int
	Value *float64
}

// RatingReader is what directory's ranking will call (S-162, v1.1).
type RatingReader interface {
	// Rating returns the doctor's public rating, or a not-found problem when the doctor is not Verified.
	Rating(ctx context.Context, doctorRef string) (Rating, error)
}
