package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func allTemplates() []notifications.Template {
	return append(append(append(append(notificationTemplates(), rightsTemplates()...), prescriptionTemplates()...), bookingMessageTemplates()...), shareMessageTemplates()...)
}

var rePlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// K-GLB-02, S-157: every template's text exists in the default catalog and uses only the
// variables the template declares; booking messages never mention money (only refund
// messages, which follow what payments decided); no text names a denylisted term (K-DP-09).
func TestS157CatalogCoversEveryTemplate(t *testing.T) {
	cat, err := loadCatalog(nil, []string{"en-IN"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range allTemplates() {
		for _, key := range []string{tpl.BodyKey, tpl.DiscreetBodyKey} {
			if key == "" {
				continue
			}
			text, ok := cat.Text("en-IN", key)
			if !ok {
				t.Errorf("%s: catalog key %s missing", tpl.Key, key)
				continue
			}
			for _, w := range notificationDenylist {
				if strings.Contains(strings.ToLower(text), w) {
					t.Errorf("%s: text contains a denylisted term %q", tpl.Key, w)
				}
			}
			if tpl.Reminder {
				continue // reminder wording comes from the resolver (and {count} for grouping)
			}
			for _, m := range rePlaceholder.FindAllStringSubmatch(text, -1) {
				if !slices.ContainsFunc(tpl.Vars, func(v notifications.VarSpec) bool { return v.Name == m[1] }) {
					t.Errorf("%s: placeholder {%s} is not a declared variable", tpl.Key, m[1])
				}
			}
			if tpl.MessageType == "booking" && (strings.Contains(text, "Rs") || strings.Contains(strings.ToLower(text), "refund")) {
				t.Errorf("%s: a booking message must not mention money", tpl.Key)
			}
		}
	}
}

type sentLog struct {
	reqs []np.Request
	err  error
}

func (s *sentLog) Send(_ context.Context, r np.Request) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.reqs = append(s.reqs, r)
	return ids.New(), nil
}

type bmBooking struct {
	bp.Service
	info bp.Info
}

func (b bmBooking) Get(_ context.Context, appt string) (bp.Info, error) {
	if appt != b.info.ID {
		return bp.Info{}, bp.ErrNotFound
	}
	return b.info, nil
}

type bmNames map[string]string

func (n bmNames) DisplayName(_ context.Context, ref string) (string, error) {
	if v, ok := n[ref]; ok {
		return v, nil
	}
	return "", dp.ErrNotFound
}

func TestS157ComposerPicksTheRightMessage(t *testing.T) {
	ctx := context.Background()
	appt, patient, doctor := ids.New(), ids.New(), ids.New()
	start := time.Date(2026, 11, 20, 4, 30, 0, 0, time.UTC)
	sent := &sentLog{}
	b := &bookingMessages{send: sent, booking: bmBooking{info: bp.Info{ID: appt, PatientRef: patient, ProfessionalRef: doctor, StartsAt: start}},
		names: bmNames{doctor: "Dr Asha Rao"}, minorUnits: 2, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ev := func(typ string, p map[string]string) outbox.Event {
		base := map[string]string{"appointmentRef": appt, "patientRef": patient, "doctorRef": doctor, "slotStart": start.Format(time.RFC3339)}
		for k, v := range p {
			base[k] = v
		}
		return outbox.Event{ID: ids.New(), Type: typ, Payload: base}
	}
	last := func() np.Request { return sent.reqs[len(sent.reqs)-1] }
	expect := func(name string, err error, tpl string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tpl == "" {
			return
		}
		if len(sent.reqs) == 0 || last().TemplateKey != tpl || last().RecipientRef != patient || last().DedupeKey == "" {
			t.Fatalf("%s: want %s, got %+v", name, tpl, sent.reqs)
		}
	}

	expect("confirmed", b.OnConfirmed(ctx, ev(bp.EventConfirmed, nil)), tplBookingConfirmed)
	if v := last().Vars; v[0].Value != "Dr Asha Rao" || v[1].Type != np.Time || v[1].Value != "2026-11-20T04:30:00Z" || v[2].Value != appt {
		t.Fatalf("confirmation variables: %+v", v)
	}
	for kind, tpl := range map[string]string{bp.ReminderDayBefore: tplReminderDay, bp.ReminderSoon: tplReminderSoon, bp.ReminderConsultStart: tplConsultStarting} {
		expect(kind, b.OnReminderDue(ctx, ev(bp.EventReminderDue, map[string]string{"kind": kind})), tpl)
	}

	// Messages that must not be sent at all.
	n := len(sent.reqs)
	expect("unknown kind", b.OnReminderDue(ctx, ev(bp.EventReminderDue, map[string]string{"kind": "bogus"})), "")
	expect("patient cancelled", b.OnCancelled(ctx, ev(bp.EventCancelled, map[string]string{"byWhom": "patient"})), "")
	expect("patient no-show", b.OnNoShow(ctx, ev(bp.EventNoShow, map[string]string{"byWhom": "patient"})), "")
	expect("no doctor name", b.OnConfirmed(ctx, ev(bp.EventConfirmed, map[string]string{"doctorRef": ids.New()})), "")
	expect("blocked refund", b.OnRefundRequested(ctx, ev("refund.requested", map[string]string{"refundRef": ids.New(), "amountMinor": "0"})), "")
	if len(sent.reqs) != n {
		t.Fatalf("nothing may be sent for these: %+v", sent.reqs[n:])
	}

	expect("clinic cancelled", b.OnCancelled(ctx, ev(bp.EventCancelled, map[string]string{"byWhom": "clinic"})), tplCancelledByClinic)
	expect("suspension", b.OnCancelled(ctx, ev(bp.EventCancelled, map[string]string{"byWhom": "system", "rule": booking.RuleClinicOrDoctorCancelled})), tplCancelledByClinic)
	expect("technical failure", b.OnCancelled(ctx, ev(bp.EventCancelled, map[string]string{"byWhom": "system", "rule": booking.RuleTechnicalFailure})), tplClosedOnline)
	expect("doctor absent", b.OnNoShow(ctx, ev(bp.EventNoShow, map[string]string{"byWhom": "doctor"})), tplDoctorAbsent)
	expect("reschedule needed", b.OnRescheduleNeeded(ctx, ev(bp.EventRescheduleNeeded, nil)), tplRescheduleNeeded)

	// Refund wording follows what payments decided; "full refund" only at 100%.
	refund := func(trigger, pct, minor string) outbox.Event {
		return ev("refund.requested", map[string]string{"refundRef": ids.New(), "trigger": trigger, "pct": pct, "amountMinor": minor})
	}
	expect("RF04 full", b.OnRefundRequested(ctx, refund(trigClinicCancelled, "100", "50000")), tplRefundClinic)
	if v := last().Vars; v[0].Value != "500" || v[0].Type != np.Numeric || len(v[1].Value) != 8 {
		t.Fatalf("refund variables: %+v", v)
	}
	expect("RF04 partial", b.OnRefundRequested(ctx, refund(trigClinicCancelled, "50", "25000")), tplRefundStarted)
	expect("RF03", b.OnRefundRequested(ctx, refund(trigDoctorNotJoined, "100", "50000")), tplRefundDoctorAbsent)
	expect("RF01", b.OnRefundRequested(ctx, refund(trigPaymentFailed, "100", "50000")), tplRefundPaymentReturn)
	expect("paise: push only, exact", b.OnRefundRequested(ctx, refund("converted_to_in_person", "50", "24950")), tplRefundStarted+pushOnlySuffix)
	if v := last().Vars[0]; v.Value != "249.50" || v.Type != np.Alphanumeric {
		t.Fatalf("an amount with paise is never rounded: %+v", v)
	}
	expect("succeeded", b.OnRefundSucceeded(ctx, ev("refund.succeeded", map[string]string{"refundRef": ids.New(), "amountMinor": "50000"})), tplRefundSucceeded)
	expect("delayed", b.OnRefundDelayed(ctx, ev("refund.failed", map[string]string{"refundRef": ids.New(), "amountMinor": "50000"})), tplRefundDelayed)

	// A message notifications refuses is dropped (logged), never retried forever.
	sent.err = np.ErrInvalid
	if err := b.OnConfirmed(ctx, ev(bp.EventConfirmed, nil)); err != nil {
		t.Fatalf("refused message must not block the stream: %v", err)
	}
	sent.err = errors.New("db down")
	if err := b.OnConfirmed(ctx, ev(bp.EventConfirmed, nil)); err == nil {
		t.Fatal("a temporary failure must be retried")
	}
}

func TestS157LongDoctorNamesFitTheSMSVariable(t *testing.T) {
	got := smsName("Dr Venkataraghavan Subramaniam Iyer")
	if utf8.RuneCountInString(got) > 30 || strings.HasSuffix(got, " ") || !strings.HasPrefix(got, "Dr Venkataraghavan") {
		t.Fatalf("%q", got)
	}
	if smsName("Dr Asha Rao") != "Dr Asha Rao" {
		t.Fatal("short names are unchanged")
	}
}
