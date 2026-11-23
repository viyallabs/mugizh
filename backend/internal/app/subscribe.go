package app

import (
	"context"
	"time"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rvp "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Subscribe registers every module's event handlers on the bus. Subscribers are listed
// here so the whole event graph is visible in one place (module-boundaries §4).
func Subscribe(bus *outbox.Bus, c *Core) {
	// Search and profiles hide a suspended doctor on the next read (K-CS-01, B5).
	bus.Subscribe("directory.verification_status_view", "verification.status_changed", c.Directory.OnVerificationStatusChanged)

	// Booking: the hold → pay → confirm saga, suspensions (E-VER-06, TC-66), practice changes.
	bus.Subscribe(bp.SubscriberPaymentCaptured, pp.EventPaymentCaptured, c.Booking.OnPaymentCaptured)
	bus.Subscribe(bp.SubscriberPaymentFailed, pp.EventPaymentFailed, c.Booking.OnPaymentFailed)
	bus.Subscribe(bp.SubscriberVerification, "verification.status_changed", c.Booking.OnVerificationStatusChanged)
	bus.Subscribe(bp.SubscriberPracticeUpdated, "practice.updated", c.Booking.OnPracticeUpdated)
	// An online consult's outcome reaches its appointment (D-46): completion, no-shows, and
	// technical failure or conversion as system cancellations under RF05 / RF06.
	bus.Subscribe(bp.SubscriberConsultEnded, cp.EventEnded, c.Booking.OnConsultEnded)

	// Payments: refunds by the policy stored with the booking (K-PAY-05).
	bus.Subscribe(pp.SubscriberCancelled, pp.EventAppointmentCancelled, c.Payments.OnAppointmentCancelled)
	bus.Subscribe(pp.SubscriberExpired, pp.EventAppointmentExpired, c.Payments.OnAppointmentExpired)
	bus.Subscribe(pp.SubscriberNoShow, pp.EventAppointmentNoShow, c.Payments.OnAppointmentNoShow)

	// Records: current-care access (T1) starts at confirmation and follows reschedules
	// (consent-design §2). GrantT1 is idempotent per booking.
	bus.Subscribe("records.grant_t1", bp.EventConfirmed, func(ctx context.Context, e outbox.Event) error {
		end, err := time.Parse(time.RFC3339, e.Payload["slotEnd"])
		if err != nil {
			return err
		}
		_, err = c.Records.GrantT1(ctx, e.Payload["patientRef"], e.Payload["doctorRef"], e.Payload["clinicRef"], e.Payload["appointmentRef"], end)
		return err
	})
	for _, ev := range []string{bp.EventCancelled, bp.EventExpired} {
		bus.Subscribe("records.revoke_t1."+ev, ev, func(ctx context.Context, e outbox.Event) error {
			return c.Records.RevokeForBooking(ctx, e.Payload["appointmentRef"])
		})
	}
	// Consult: an online appointment gets a session at confirmation; it follows reschedules and
	// cancellations. A doctor's minor flag opens a CH-12 support case (TC-48).
	bus.Subscribe(cp.SubscriberAppointmentConfirmed, bp.EventConfirmed, c.Consult.OnAppointmentConfirmed)
	bus.Subscribe(cp.SubscriberAppointmentRescheduled, bp.EventRescheduled, c.Consult.OnAppointmentRescheduled)
	bus.Subscribe(cp.SubscriberAppointmentCancelled, bp.EventCancelled, c.Consult.OnAppointmentCancelled)
	bus.Subscribe("admin.consult_minor_flagged", cp.EventMinorFlagged, c.Admin.OnMinorFlagged)
	// Reviews: the eligibility read model (rule B5) is built from completed appointments, however they
	// were completed (staff mark for in-person, consult outcome for online; both emit appointment.completed).
	bus.Subscribe(rvp.SubscriberAppointmentCompleted, bp.EventCompleted, c.Reviews.OnAppointmentCompleted)
	// Patient messages (S-157, D-35): booking changes, reminders and refunds, composed at send
	// time; booking events never mention money, refund events say exactly what was decided.
	bm := c.BookingMsgs
	for _, s := range []struct {
		name, event string
		h           outbox.Handler
	}{
		{"notify.booking_confirmed", bp.EventConfirmed, bm.OnConfirmed},
		{"notify.booking_rescheduled", bp.EventRescheduled, bm.OnRescheduled},
		{"notify.booking_reminder", bp.EventReminderDue, bm.OnReminderDue},
		{"notify.booking_cancelled", bp.EventCancelled, bm.OnCancelled},
		{"notify.booking_no_show", bp.EventNoShow, bm.OnNoShow},
		{"notify.booking_reschedule_needed", bp.EventRescheduleNeeded, bm.OnRescheduleNeeded},
		{"notify.refund_requested", pp.EventRefundRequested, bm.OnRefundRequested},
		{"notify.refund_succeeded", pp.EventRefundSucceeded, bm.OnRefundSucceeded},
		{"notify.refund_failed", pp.EventRefundFailed, bm.OnRefundDelayed},
		{"notify.refund_escalated", pp.EventRefundEscalated, bm.OnRefundDelayed},
	} {
		bus.Subscribe(s.name, s.event, s.h)
	}
	// History sharing (S-168, E-DP-02): the patient hears of a doctor's extension request; the doctor
	// hears the decision.
	bus.Subscribe("notify.share_extension_requested", "share.extension_requested", c.ShareMsgs.OnExtensionRequested)
	bus.Subscribe("notify.share_extension_decided", "share.extension_decided", c.ShareMsgs.OnExtensionDecided)
	bus.Subscribe("notify.document_quarantined", records.EventDocumentQuarantined, c.ShareMsgs.OnDocumentQuarantined)
	// Clinical: after signing or amending, T2, reminders (only with P-REMIND) and the patient notice.
	bus.Subscribe("clinical.on_prescription_signed", "prescription.signed", c.Rx.HandleSigned)
	bus.Subscribe("clinical.on_prescription_amended", "prescription.amended", c.Rx.HandleAmended)
	// The on-device reminder schedule (S-165): devices refresh when the reminders change or the reminders or
	// medicine-names consent changes.
	bus.Subscribe("notifications.reminder_sync_changed", clinical.EventRemindersChanged, c.Notify.OnRemindersChanged)
	bus.Subscribe("notifications.reminder_sync_consent_granted", "consent.granted", c.Notify.OnConsentChanged)
	bus.Subscribe("notifications.reminder_sync_consent_revoked", "consent.revoked", c.Notify.OnConsentChanged)
	// A device signed out remotely is told to wipe itself, including its reminder backups (S-115, S-165).
	bus.Subscribe("notifications.device_signout", ip.EventDeviceRevoked, c.Notify.OnDeviceRevoked)
	// Erasure (K-DP-07, D-32): every module that holds patient data handles both steps.
	erasure := []struct {
		module              string
		req, due, cancelled outbox.Handler
	}{
		{"identity", c.Identity.OnErasureRequested, c.Identity.OnErasureDue, c.Identity.OnErasureCancelled},
		{"booking", c.Booking.OnErasureRequested, c.Booking.OnErasureDue, c.Booking.OnErasureCancelled},
		{"payments", c.Payments.OnErasureRequested, c.Payments.OnErasureDue, c.Payments.OnErasureCancelled},
		{"clinical", c.Rx.OnErasureRequested, c.Rx.OnErasureDue, c.Rx.OnErasureCancelled},
		{"consult", c.Consult.OnErasureRequested, c.Consult.OnErasureDue, c.Consult.OnErasureCancelled},
		{"reviews", c.Reviews.OnErasureRequested, c.Reviews.OnErasureDue, c.Reviews.OnErasureCancelled},
		{"admin", c.Admin.OnErasureRequested, c.Admin.OnErasureDue, c.Admin.OnErasureCancelled},
		{"notifications", c.Notify.OnErasureRequested, c.Notify.OnErasureDue, c.Notify.OnErasureCancelled},
	}
	for _, e := range erasure {
		bus.Subscribe(e.module+".erasure_requested", "rights.erasure_requested", e.req)
		bus.Subscribe(e.module+".erasure_due", "rights.erasure_due", e.due)
		bus.Subscribe(e.module+".erasure_cancelled", "rights.erasure_cancelled", e.cancelled)
	}
	// Data rights: per-module erasure tasks report back; requests enter the admin queue.
	bus.Subscribe("records.rights_task_done", "rights.task_done", c.Rights.OnRightsTaskDone)
	bus.Subscribe("admin.rights_request_received", "rights.request_received", c.Admin.OnRightsRequestReceived)
	bus.Subscribe("records.move_t1", bp.EventRescheduled, func(ctx context.Context, e outbox.Event) error {
		end, err := time.Parse(time.RFC3339, e.Payload["slotEnd"])
		if err != nil {
			return err
		}
		return c.Records.ExtendT1(ctx, e.Payload["appointmentRef"], end)
	})
}

// RelayModules are the modules whose event_outbox the worker relays.
var RelayModules = []string{"admin", "booking", "clinical", "consult", "directory", "identity", "notifications", "payments", "records", "reviews", "verification"}
