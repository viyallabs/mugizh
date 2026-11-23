package app

import (
	"context"
	"errors"
	"log/slog"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// shareAppointments implements records' AppointmentReader over booking (S-168): a history
// share is tied to the patient's own live appointment with that doctor (consent-design: T3 is
// created from a booking). Confirmed or in progress only; anything else is ErrNoAppointment.
type shareAppointments struct{ booking bp.Service }

func (s shareAppointments) ConfirmedAppointment(ctx context.Context, patientRef, doctorRef, appointmentRef string) (rp.AppointmentFacts, error) {
	info, err := s.booking.Get(ctx, appointmentRef)
	if errors.Is(err, bp.ErrNotFound) {
		return rp.AppointmentFacts{}, rp.ErrNoAppointment
	}
	if err != nil {
		return rp.AppointmentFacts{}, err
	}
	if info.PatientRef != patientRef || info.ProfessionalRef != doctorRef || (info.State != "confirmed" && info.State != "in_progress") {
		return rp.AppointmentFacts{}, rp.ErrNoAppointment
	}
	return rp.AppointmentFacts{ClinicRef: info.ClinicRef, EndsAt: info.EndsAt}, nil
}

// shareMessages tells the patient when a doctor asks for more time on a history share, and the
// doctor when the patient decides (E-DP-02). Push only; no patient name on the doctor's lock
// screen and never a specialty or diagnosis (K-DP-09).
type shareMessages struct {
	send  np.Sender
	names dp.NameReader
	log   *slog.Logger
}

func shareMessageTemplates() []notifications.Template {
	t := func(key, body string, vars []notifications.VarSpec) notifications.Template {
		return notifications.Template{Key: key, MessageType: "sharing", Category: np.Service, Priority: np.Normal,
			Channels: []notifications.Channel{notifications.Push}, Vars: vars, TitleKey: "notif.sharing.title", BodyKey: body,
			LinkScreen: "sharing", LinkRefVar: "request_id"}
	}
	pro := func(tp notifications.Template) notifications.Template {
		tp.Audience = notifications.AudiencePro
		return tp
	}
	req := []notifications.VarSpec{{Name: "request_id", Type: np.Ref}}
	return []notifications.Template{
		t("share.extension_requested", "notif.sharing.extension_requested", []notifications.VarSpec{{Name: "doctor_name", Type: np.Alphanumeric}, {Name: "request_id", Type: np.Ref}}),
		// The doctor hears the decision in the pro app (D-73).
		pro(t("share.extension_approved", "notif.sharing.extension_approved", req)),
		pro(t("share.extension_declined", "notif.sharing.extension_declined", req)),
		// A caregiver accepted the patient's invitation: neutral push, no name, no scopes (K-DP-09).
		{Key: "caregiver.linked", MessageType: "caregiving", Category: np.Service, Priority: np.Normal, Channels: []notifications.Channel{notifications.Push},
			TitleKey: "notif.caregiving.title", BodyKey: "notif.caregiving.linked", LinkScreen: "caregiving"},
		// AAD-05: the patient hears that a document was hidden for a prohibited identifier and will be deleted.
		{Key: "document.quarantined", MessageType: "documents", Category: np.Service, Priority: np.Normal, Channels: []notifications.Channel{notifications.Push},
			TitleKey: "notif.documents.title", BodyKey: "notif.documents.quarantined", LinkScreen: "documents"},
	}
}

// OnExtensionRequested: share.extension_requested {requestId, doctorId}; the subject is the patient.
func (s *shareMessages) OnExtensionRequested(ctx context.Context, e outbox.Event) error {
	req, doctor, patient := e.Payload["requestId"], e.Payload["doctorId"], e.SubjectRef
	if !ids.Valid(req) || !ids.Valid(patient) {
		s.log.Error("notify.event_incomplete", "event_id", e.ID, "template", "share.extension_requested")
		return nil
	}
	name, err := s.names.DisplayName(ctx, doctor)
	if errors.Is(err, dp.ErrNotFound) {
		s.log.Error("notify.doctor_name_missing", "event_id", e.ID)
		return nil
	}
	if err != nil {
		return err
	}
	return s.deliver(ctx, np.Request{TemplateKey: "share.extension_requested", RecipientRef: patient, Category: np.Service, Reason: "sharing.extension",
		DedupeKey: "share.extension_requested:" + req, Vars: []np.Var{{Name: "doctor_name", Type: np.Alphanumeric, Value: smsName(name)}, {Name: "request_id", Type: np.Ref, Value: req}}})
}

// OnExtensionDecided: share.extension_decided {requestId, doctorId, decision} → the doctor.
func (s *shareMessages) OnExtensionDecided(ctx context.Context, e outbox.Event) error {
	req, doctor := e.Payload["requestId"], e.Payload["doctorId"]
	key := map[string]string{"approved": "share.extension_approved", "declined": "share.extension_declined"}[e.Payload["decision"]]
	if !ids.Valid(req) || !ids.Valid(doctor) || key == "" {
		s.log.Error("notify.event_incomplete", "event_id", e.ID, "template", "share.extension_decided")
		return nil
	}
	return s.deliver(ctx, np.Request{TemplateKey: key, RecipientRef: doctor, Category: np.Service, Reason: "sharing.extension",
		DedupeKey: key + ":" + req, Vars: []np.Var{{Name: "request_id", Type: np.Ref, Value: req}}})
}

func (s *shareMessages) deliver(ctx context.Context, r np.Request) error {
	_, err := s.send.Send(ctx, r)
	if errors.Is(err, np.ErrInvalid) || errors.Is(err, np.ErrUnknownTemplate) {
		s.log.Error("notify.message_refused", "template", r.TemplateKey)
		return nil
	}
	return err
}

// OnDocumentQuarantined: document.quarantined {documentRef}; the subject is the patient (AAD-05). The message names
// neither the document nor the number.
func (s *shareMessages) OnDocumentQuarantined(ctx context.Context, e outbox.Event) error {
	doc, patient := e.Payload["documentRef"], e.SubjectRef
	if !ids.Valid(doc) || !ids.Valid(patient) {
		s.log.Error("notify.event_incomplete", "event_id", e.ID, "template", "document.quarantined")
		return nil
	}
	return s.deliver(ctx, np.Request{TemplateKey: "document.quarantined", RecipientRef: patient, Category: np.Service, Reason: "records.quarantine",
		DedupeKey: "document.quarantined:" + doc})
}

// OnCaregiverLinked: caregiving.linked {linkRef}; the subject is the patient. The message names nobody and no scope.
func (s *shareMessages) OnCaregiverLinked(ctx context.Context, e outbox.Event) error {
	link, patient := e.Payload["linkRef"], e.SubjectRef
	if !ids.Valid(link) || !ids.Valid(patient) {
		s.log.Error("notify.event_incomplete", "event_id", e.ID, "template", "caregiver.linked")
		return nil
	}
	return s.deliver(ctx, np.Request{TemplateKey: "caregiver.linked", RecipientRef: patient, Category: np.Service, Reason: "caregiving.linked",
		DedupeKey: "caregiver.linked:" + link})
}
