// Package public is the booking module's interface (module-boundaries section 2). Payments,
// consult, directory and the composition root use only this package.
//
// Event payloads carry references and codes only (module-boundaries section 4).
package public

import (
	"context"
	"errors"
	"time"
)

// Event types booking emits.
const (
	EventHeld                 = "appointment.held"                   // {appointmentRef, practiceRef}
	EventConfirmed            = "appointment.confirmed"              // {appointmentRef, patientRef, doctorRef, clinicRef, practiceRef, mode, slotStart, slotEnd}
	EventRescheduled          = "appointment.rescheduled"            // same payload as confirmed, with the new times
	EventCancelled            = "appointment.cancelled"              // {appointmentRef, patientRef, doctorRef, clinicRef, practiceRef, byWhom, policyVersion, mode, consultMode, minutesBeforeSlot, [rule]}
	EventExpired              = "appointment.expired"                // {appointmentRef, byWhom, policyVersion, mode, consultMode}
	EventNoShow               = "appointment.no_show"                // {appointmentRef, patientRef, clinicRef, byWhom, policyVersion, mode, consultMode, [rule]}
	EventCompleted            = "appointment.completed"              // {appointmentRef, patientRef, doctorRef, clinicRef, practiceRef}
	EventRescheduleNeeded     = "appointment.reschedule_needed"      // {appointmentRef, patientRef, clinicRef, practiceRef}
	EventReconfirmationLapsed = "appointment.reconfirmation_overdue" // {appointmentRef, patientRef, doctorRef, clinicRef, slotStart}
	// EventReminderDue: an appointment message is due (S-157): {appointmentRef, patientRef, doctorRef,
	// practiceRef, mode, slotStart, kind}; kind is one of the Reminder* values.
	EventReminderDue = "appointment.reminder_due"
)

// Subscriber names for the outbox bus (one per handler, unique).
const (
	SubscriberPaymentCaptured = "booking.on_payment_captured"
	SubscriberPaymentFailed   = "booking.on_payment_failed"
	SubscriberVerification    = "booking.on_verification_status_changed"
	SubscriberPracticeUpdated = "booking.on_practice_updated"
	SubscriberConsultEnded    = "booking.on_consult_ended"
)

// Refund modes in event payloads (the refund policy's applies_to values).
const (
	RefundModeOnline   = "online"
	RefundModeInPerson = "in_person"
)

// Errors.
var (
	ErrNotFound = errors.New("booking: appointment not found")
	// ErrTooEarly is returned by MarkDoctorNoShow before the join timeout has elapsed.
	ErrTooEarly = errors.New("booking: the doctor join timeout has not elapsed")
)

// Info is a read-only view of an appointment for other modules.
type Info struct {
	ID              string
	PatientRef      string
	ProfessionalRef string
	ClinicRef       string
	PracticeRef     string
	Mode            string // in_person, video, audio
	StartsAt        time.Time
	EndsAt          time.Time
	State           string
}

// Service is what other modules call.
type Service interface {
	// PatientOf returns the patient of an appointment (payments' AppointmentOwners port).
	PatientOf(ctx context.Context, appointmentRef string) (patientRef string, found bool, err error)
	// Get returns an appointment, or ErrNotFound.
	Get(ctx context.Context, appointmentRef string) (Info, error)
	// MarkDoctorNoShow records that the doctor did not join an online consult within the
	// join timeout (E-BK-04, RF03). The consult module calls it. ErrTooEarly before the timeout.
	MarkDoctorNoShow(ctx context.Context, appointmentRef string) error
	// EarliestSlots implements directory's AvailabilitySource.
	EarliestSlots(ctx context.Context, practiceIDs []string, from time.Time) (map[string]time.Time, error)
}

// Reminder kinds carried in appointment.reminder_due (S-157).
const (
	ReminderDayBefore    = "day_before"    // the day before (SMS-03)
	ReminderSoon         = "soon"          // shortly before (SMS-03; not held in quiet hours)
	ReminderConsultStart = "consult_start" // online consult about to start (SMS-04)
)
