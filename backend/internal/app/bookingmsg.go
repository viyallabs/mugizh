package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// bookingMessages is the booking, consult and refund message composer (S-157, D-35). It
// reads what each message needs from the module that owns it at send time (the doctor's
// display name from directory, the patient and slot from booking, the amount from the refund
// event) and asks notifications for a registered template. Rules it keeps:
//
//   - wording lives in the catalog and SMS goes only through registered DLT templates
//     (K-MSG-01, K-GLB-02); times travel as instants and are formatted for the recipient;
//   - no message names a specialty or diagnosis (K-DP-09): the doctor's name only;
//   - no message claims a refund that payments has not decided: booking events never mention
//     money; refund wording follows refund events, and "full refund" only when the share is 100%;
//   - an amount the registered SMS variable cannot carry exactly (paise) goes by push only,
//     never rounded (D-52);
//   - every send has a dedupe key, so replayed events never send twice.
type bookingMessages struct {
	send       np.Sender
	booking    bp.Service
	names      dp.NameReader
	minorUnits int
	log        *slog.Logger
}

// Template keys.
const (
	tplBookingConfirmed    = "booking.confirmed"
	tplBookingRescheduled  = "booking.rescheduled"
	tplReminderDay         = "appointment.reminder"
	tplReminderSoon        = "appointment.reminder_soon"
	tplConsultStarting     = "consult.starting"
	tplCancelledByClinic   = "booking.cancelled_by_clinic"
	tplClosedOnline        = "booking.closed_online"
	tplDoctorAbsent        = "booking.doctor_absent"
	tplRescheduleNeeded    = "booking.reschedule_needed"
	tplRefundClinic        = "refund.clinic_cancelled"
	tplRefundDoctorAbsent  = "refund.doctor_absent"
	tplRefundPaymentReturn = "refund.payment_returned"
	tplRefundStarted       = "refund.started"
	tplRefundSucceeded     = "refund.succeeded"
	tplRefundDelayed       = "refund.delayed"
	pushOnlySuffix         = ".push"
)

// Refund policy triggers carried in refund.requested (payments' refund policy).
const (
	trigClinicCancelled = "clinic_or_doctor_cancelled"
	trigDoctorNotJoined = "doctor_not_joined"
	trigPaymentFailed   = "payment_debited_booking_failed"
)

func bookingMessageTemplates() []notifications.Template {
	push, smsCh := notifications.Push, notifications.SMS
	appt := []notifications.VarSpec{{Name: "doctor_name", Type: np.Alphanumeric}, {Name: "date_time", Type: np.Time}, {Name: "appointment_id", Type: np.Ref}}
	when := []notifications.VarSpec{{Name: "date_time", Type: np.Time}, {Name: "appointment_id", Type: np.Ref}}
	nameTime := []notifications.SMSVar{{Name: "doctor_name", Source: "var:doctor_name"}, {Name: "date_time", Source: "var:date_time"}}
	base := func(key, body string, p np.Priority, vars []notifications.VarSpec) notifications.Template {
		return notifications.Template{Key: key, MessageType: "booking", Category: np.Service, Priority: p, QuietExempt: p == np.Critical,
			Channels: []notifications.Channel{push}, Vars: vars, TitleKey: "notif.appointment.title", BodyKey: body,
			LinkScreen: "appointment", LinkRefVar: "appointment_id"}
	}
	withSMS := func(t notifications.Template, id string, vars []notifications.SMSVar) notifications.Template {
		t.Channels = []notifications.Channel{push, smsCh}
		t.SMS = &notifications.SMSBinding{TemplateID: id, Vars: vars}
		return t
	}
	out := []notifications.Template{
		withSMS(base(tplBookingConfirmed, "notif.booking.confirmed", np.Normal, appt), "SMS-02", nameTime),
		base(tplBookingRescheduled, "notif.booking.rescheduled", np.Normal, appt),
		withSMS(base(tplReminderDay, "notif.appointment.reminder", np.Normal, appt), "SMS-03", nameTime),
		withSMS(base(tplReminderSoon, "notif.appointment.reminder", np.Critical, appt), "SMS-03", nameTime),
		withSMS(base(tplConsultStarting, "notif.consult.starting", np.Critical, appt), "SMS-04", nameTime),
		base(tplCancelledByClinic, "notif.booking.cancelled_by_clinic", np.Critical, when),
		base(tplClosedOnline, "notif.booking.closed_online", np.Normal, when),
		base(tplDoctorAbsent, "notif.booking.doctor_absent", np.Critical, when),
		base(tplRescheduleNeeded, "notif.booking.reschedule_needed", np.Critical, when),
	}
	// Refund messages: each has a push-only twin for amounts the numeric SMS variable cannot
	// carry exactly.
	refund := func(key, body string, p np.Priority, sms string, smsVars []notifications.SMSVar) {
		vars := func(amount np.VarType) []notifications.VarSpec {
			return []notifications.VarSpec{{Name: "amount", Type: amount}, {Name: "reference", Type: np.Alphanumeric},
				{Name: "date_time", Type: np.Time}, {Name: "appointment_id", Type: np.Ref}}
		}
		t := base(key, body, p, vars(np.Numeric))
		t.MessageType, t.TitleKey = "refund", "notif.refund.title"
		out = append(out, withSMS(t, sms, smsVars))
		twin := base(key+pushOnlySuffix, body, p, vars(np.Alphanumeric))
		twin.MessageType, twin.TitleKey = "refund", "notif.refund.title"
		out = append(out, twin)
	}
	amountRef := []notifications.SMSVar{{Name: "amount", Source: "var:amount"}, {Name: "reference", Source: "var:reference"}}
	refund(tplRefundClinic, "notif.refund.clinic_cancelled", np.Critical, "SMS-05", []notifications.SMSVar{{Name: "date_time", Source: "var:date_time"}})
	refund(tplRefundDoctorAbsent, "notif.refund.doctor_absent", np.Critical, "SMS-06",
		[]notifications.SMSVar{{Name: "date_time", Source: "var:date_time"}, {Name: "amount", Source: "var:amount"}})
	refund(tplRefundPaymentReturn, "notif.refund.payment_returned", np.Normal, "SMS-08", amountRef)
	refund(tplRefundStarted, "notif.refund.started", np.Normal, "SMS-07", amountRef)
	refund(tplRefundSucceeded, "notif.refund.succeeded", np.Normal, "SMS-07b", amountRef)
	refund(tplRefundDelayed, "notif.refund.delayed", np.Normal, "SMS-07c", amountRef)
	return out
}

// ---- booking events ----

// OnConfirmed: appointment.confirmed → booking confirmation (SMS-02).
func (b *bookingMessages) OnConfirmed(ctx context.Context, e outbox.Event) error {
	return b.apptMessage(ctx, e, tplBookingConfirmed, "booking.confirmed", "confirmed:"+e.Payload["appointmentRef"])
}

// OnRescheduled: appointment.rescheduled → the new time (push).
func (b *bookingMessages) OnRescheduled(ctx context.Context, e outbox.Event) error {
	return b.apptMessage(ctx, e, tplBookingRescheduled, "booking.rescheduled", "rescheduled:"+e.Payload["appointmentRef"]+":"+e.Payload["slotStart"])
}

// OnReminderDue: appointment.reminder_due → day-before or short reminder (SMS-03), or the
// online consult starting notice (SMS-04).
func (b *bookingMessages) OnReminderDue(ctx context.Context, e outbox.Event) error {
	tpl := map[string]string{bp.ReminderDayBefore: tplReminderDay, bp.ReminderSoon: tplReminderSoon, bp.ReminderConsultStart: tplConsultStarting}[e.Payload["kind"]]
	if tpl == "" {
		b.log.Error("notify.reminder_kind_unknown", "event_id", e.ID)
		return nil
	}
	return b.apptMessage(ctx, e, tpl, "booking.reminder", "reminder:"+e.Payload["kind"]+":"+e.Payload["appointmentRef"]+":"+e.Payload["slotStart"])
}

// OnCancelled: a cancellation the patient did not make. Money is never mentioned here.
func (b *bookingMessages) OnCancelled(ctx context.Context, e outbox.Event) error {
	tpl := ""
	switch e.Payload["byWhom"] {
	case "patient":
		return nil // the patient cancelled in the app; a refund, if any, has its own message
	case "clinic", "doctor":
		tpl = tplCancelledByClinic
	case "system":
		switch e.Payload["rule"] {
		case booking.RuleTechnicalFailure, booking.RuleConvertedToInPerson:
			tpl = tplClosedOnline
		default: // the clinic did not reconfirm, or the doctor was suspended (RF04)
			tpl = tplCancelledByClinic
		}
	default:
		return nil
	}
	return b.whenMessage(ctx, e, tpl, "booking.cancelled", "cancelled:"+e.Payload["appointmentRef"])
}

// OnNoShow: the doctor was not available (the refund, if any, has its own message).
func (b *bookingMessages) OnNoShow(ctx context.Context, e outbox.Event) error {
	if e.Payload["byWhom"] != "doctor" {
		return nil
	}
	return b.whenMessage(ctx, e, tplDoctorAbsent, "booking.doctor_absent", "doctor_absent:"+e.Payload["appointmentRef"])
}

// OnRescheduleNeeded: the appointment needs a new time (doctor suspended, practice changed).
func (b *bookingMessages) OnRescheduleNeeded(ctx context.Context, e outbox.Event) error {
	return b.whenMessage(ctx, e, tplRescheduleNeeded, "booking.reschedule_needed", "reschedule_needed:"+e.Payload["appointmentRef"])
}

// ---- refund events ----

// OnRefundRequested picks the wording from what payments decided.
func (b *bookingMessages) OnRefundRequested(ctx context.Context, e outbox.Event) error {
	tpl := tplRefundStarted
	full := e.Payload["pct"] == "100"
	switch e.Payload["trigger"] {
	case trigClinicCancelled:
		if full {
			tpl = tplRefundClinic
		}
	case trigDoctorNotJoined:
		if full {
			tpl = tplRefundDoctorAbsent
		}
	case trigPaymentFailed:
		tpl = tplRefundPaymentReturn
	}
	return b.refundMessage(ctx, e, tpl, "refund.requested:"+e.Payload["refundRef"])
}

// OnRefundSucceeded: the refund reached the bank (SMS-07b).
func (b *bookingMessages) OnRefundSucceeded(ctx context.Context, e outbox.Event) error {
	return b.refundMessage(ctx, e, tplRefundSucceeded, "refund.succeeded:"+e.Payload["refundRef"])
}

// OnRefundDelayed: refund.failed or refund.escalated; told once per refund (SMS-07c).
func (b *bookingMessages) OnRefundDelayed(ctx context.Context, e outbox.Event) error {
	return b.refundMessage(ctx, e, tplRefundDelayed, "refund.delayed:"+e.Payload["refundRef"])
}

// ---- helpers ----

func (b *bookingMessages) apptMessage(ctx context.Context, e outbox.Event, tpl, reason, dedupe string) error {
	appt, patient, doctor := e.Payload["appointmentRef"], e.Payload["patientRef"], e.Payload["doctorRef"]
	start, err := time.Parse(time.RFC3339, e.Payload["slotStart"])
	if err != nil || appt == "" || patient == "" {
		b.log.Error("notify.event_incomplete", "event_id", e.ID, "template", tpl)
		return nil
	}
	name, err := b.names.DisplayName(ctx, doctor)
	if errors.Is(err, dp.ErrNotFound) {
		b.log.Error("notify.doctor_name_missing", "event_id", e.ID, "template", tpl)
		return nil
	}
	if err != nil {
		return err // retried by the bus
	}
	return b.deliver(ctx, np.Request{TemplateKey: tpl, RecipientRef: patient, Category: np.Service, Reason: reason, DedupeKey: dedupe,
		Vars: []np.Var{{Name: "doctor_name", Type: np.Alphanumeric, Value: smsName(name)}, {Name: "date_time", Type: np.Time, Value: start.UTC().Format(time.RFC3339)},
			{Name: "appointment_id", Type: np.Ref, Value: appt}}})
}

// whenMessage sends a message that names only the appointment time, read from booking (the
// cancellation and no-show payloads carry no slot time).
func (b *bookingMessages) whenMessage(ctx context.Context, e outbox.Event, tpl, reason, dedupe string) error {
	appt := e.Payload["appointmentRef"]
	info, err := b.booking.Get(ctx, appt)
	if errors.Is(err, bp.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return b.deliver(ctx, np.Request{TemplateKey: tpl, RecipientRef: info.PatientRef, Category: np.Service, Reason: reason, DedupeKey: dedupe,
		Vars: []np.Var{{Name: "date_time", Type: np.Time, Value: info.StartsAt.UTC().Format(time.RFC3339)}, {Name: "appointment_id", Type: np.Ref, Value: appt}}})
}

func (b *bookingMessages) refundMessage(ctx context.Context, e outbox.Event, tpl, dedupe string) error {
	appt, ref := e.Payload["appointmentRef"], e.Payload["refundRef"]
	minor, err := strconv.ParseInt(e.Payload["amountMinor"], 10, 64)
	if err != nil || minor <= 0 || ref == "" {
		b.log.Error("notify.refund_event_incomplete", "event_id", e.ID)
		return nil
	}
	info, err := b.booking.Get(ctx, appt)
	if errors.Is(err, bp.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	amount, whole := b.major(minor)
	amountType := np.Numeric
	if !whole {
		tpl, amountType = tpl+pushOnlySuffix, np.Alphanumeric
	}
	return b.deliver(ctx, np.Request{TemplateKey: tpl, RecipientRef: info.PatientRef, Category: np.Service, Reason: "refund", DedupeKey: dedupe,
		Vars: []np.Var{{Name: "amount", Type: amountType, Value: amount}, {Name: "reference", Type: np.Alphanumeric, Value: strings.ToUpper(shortRef(ref))},
			{Name: "date_time", Type: np.Time, Value: info.StartsAt.UTC().Format(time.RFC3339)}, {Name: "appointment_id", Type: np.Ref, Value: appt}}})
}

// deliver enqueues the message. A request notifications refuses (bad data, unknown template)
// is logged and dropped: retrying cannot fix it, and it must not block the event stream.
func (b *bookingMessages) deliver(ctx context.Context, r np.Request) error {
	_, err := b.send.Send(ctx, r)
	if errors.Is(err, np.ErrInvalid) || errors.Is(err, np.ErrUnknownTemplate) {
		b.log.Error("notify.message_refused", "template", r.TemplateKey, logging.Err(err))
		return nil
	}
	return err
}

// major renders an amount in major units (pack money.minor_units) and reports whether it is
// a whole number, which is all the registered numeric SMS variable can carry.
func (b *bookingMessages) major(minor int64) (string, bool) {
	div := int64(1)
	for i := 0; i < b.minorUnits; i++ {
		div *= 10
	}
	if minor%div == 0 {
		return strconv.FormatInt(minor/div, 10), true
	}
	frac := strconv.FormatInt(minor%div, 10)
	for len(frac) < b.minorUnits {
		frac = "0" + frac
	}
	return strconv.FormatInt(minor/div, 10) + "." + frac, false
}

// smsName keeps a display name within the 30-character template variable limit, cutting at
// a word boundary.
func smsName(name string) string {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) <= 30 {
		return name
	}
	r := []rune(name)[:30]
	if i := strings.LastIndex(string(r), " "); i > 0 {
		return strings.TrimSpace(string(r)[:i])
	}
	return string(r)
}
