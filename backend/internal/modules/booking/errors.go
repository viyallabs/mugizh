package booking

import (
	"net/http"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Problems returned to clients. The code is stable; detail never carries personal data.
var (
	ProblemNotFound          = problem.NotFound
	ProblemSlotTaken         = problem.New(http.StatusConflict, "booking.slot_taken", "That slot is no longer available")
	ProblemTooManyHolds      = problem.New(http.StatusTooManyRequests, "booking.too_many_holds", "Too many active holds")
	ProblemOutsideIndia      = problem.New(http.StatusUnprocessableEntity, "booking.patient_outside_india", "An online consult needs the patient to confirm they are in India now (P10)")
	ProblemModeNotOffered    = problem.New(http.StatusUnprocessableEntity, "booking.mode_not_offered", "This practice does not offer that consult mode at that time")
	ProblemInvalidTimeZone   = problem.New(http.StatusUnprocessableEntity, "booking.invalid_time_zone", "The time zone is not valid")
	ProblemPayAtClinicOnline = problem.New(http.StatusUnprocessableEntity, "booking.pay_at_clinic_in_person_only", "This clinic takes payment at the clinic, for in-person visits only")
	ProblemPaymentsPaused    = problem.New(http.StatusConflict, "booking.clinic_payments_paused", "This clinic is not taking new paid bookings right now")
	ProblemPaymentUnavail    = problem.New(http.StatusConflict, "booking.payment_unavailable", "Online payment is not available for this clinic")
	ProblemNotPayable        = problem.New(http.StatusConflict, "booking.not_payable", "This booking cannot be paid now")
	ProblemHoldExpired       = problem.New(http.StatusConflict, "booking.hold_expired", "The hold has expired")
	ProblemPracticeGone      = problem.New(http.StatusConflict, "booking.practice_unavailable", "This practice is not available for booking")
	ProblemCannotCancel      = problem.New(http.StatusConflict, "booking.cannot_cancel", "This appointment can no longer be cancelled")
	ProblemRefundChanged     = problem.New(http.StatusConflict, "booking.refund_changed", "The refund outcome changed; review it and confirm again")
	ProblemPreviewDown       = problem.New(http.StatusServiceUnavailable, "booking.refund_preview_unavailable", "The refund outcome cannot be shown right now")
	ProblemCannotReschedule  = problem.New(http.StatusConflict, "booking.cannot_reschedule", "This appointment cannot be rescheduled")
	ProblemRescheduleLimit   = problem.New(http.StatusConflict, "booking.reschedule_limit", "No free reschedules left; cancel and book again")
	ProblemBadMark           = problem.New(http.StatusConflict, "booking.mark_not_allowed", "That mark is not allowed in the appointment's current state")
	ProblemMarkTooEarly      = problem.New(http.StatusConflict, "booking.mark_too_early", "It is too early to mark this")
	// TM-13: an online consult's outcome comes only from the consult module (join evidence, D-46).
	ProblemOnlineOutcome   = problem.New(http.StatusConflict, "booking.online_outcome_by_consult", "An online consultation's outcome is recorded by the consultation itself")
	ProblemBadAvailability = problem.New(http.StatusUnprocessableEntity, "booking.invalid_availability", "The availability is not valid")
)

// SlotTakenError is returned by Hold when the slot was not open. Next lists the next open
// slots of the same practice so the app can offer them (TC-22).
type SlotTakenError struct{ Next []SlotInfo }

func (e *SlotTakenError) Error() string { return ProblemSlotTaken.Code }

// Unwrap lets callers match ProblemSlotTaken with errors.Is.
func (e *SlotTakenError) Unwrap() error { return ProblemSlotTaken }
