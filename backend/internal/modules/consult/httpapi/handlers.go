// Package httpapi implements the consult OpenAPI operations (api/core/consult.v1.yaml).
package httpapi

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface. Errors are *problem.Problem values returned by
// the module; nothing is written by hand.
type Handlers struct{ M *consult.Module }

var _ StrictServerInterface = Handlers{}

// actor builds the acting person from the principal: patient or doctor only (K-SEC-04).
func actor(ctx context.Context) (consult.Actor, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return consult.Actor{}, problem.Unauthenticated
	}
	if p.ActingRole != ip.RolePatient && p.ActingRole != ip.RoleDoctor {
		return consult.Actor{}, problem.Forbidden
	}
	return consult.Actor{UserID: p.UserID, Role: p.ActingRole, ClinicRef: p.ActingClinic, RequestID: httpx.RequestID(ctx)}, nil
}

func uid(s string) openapi_types.UUID { return uuid.MustParse(s) }

func session(v consult.View) ConsultSession {
	out := ConsultSession{
		ConsultId: uid(v.ID), AppointmentId: uid(v.AppointmentRef), BookedMode: ConsultSessionBookedMode(v.BookedMode),
		CurrentMode: CallMode(v.CurrentMode), State: ConsultState(v.State), SlotStart: v.SlotStart, SlotEnd: v.SlotEnd,
		JoinOpensAt: v.JoinOpensAt, JoinClosesAt: v.JoinClosesAt, ConsentRecorded: v.ConsentRecorded,
		PatientInIndiaDeclared: v.PatientInIndia, IdentityConfirmed: v.IdentityConfirmed,
		BothConnectedAt: v.BothConnectedAt, EndedAt: v.EndedAt,
	}
	if v.Outcome != "" {
		o := ConsultOutcome(v.Outcome)
		out.Outcome = &o
	}
	if v.ShortCall {
		t := true
		out.ShortCall = &t
	}
	if v.Patient != nil {
		out.Patient = &PatientIdentity{FullName: v.Patient.FullName, AgeYears: v.Patient.AgeYears}
	}
	return out
}

// GetConsultByAppointment returns the session of one of my online appointments.
func (h Handlers) GetConsultByAppointment(ctx context.Context, req GetConsultByAppointmentRequestObject) (GetConsultByAppointmentResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.M.Get(ctx, a, req.AppointmentId.String(), true)
	if err != nil {
		return nil, err
	}
	return GetConsultByAppointment200JSONResponse(session(v)), nil
}

// GetConsult returns a session.
func (h Handlers) GetConsult(ctx context.Context, req GetConsultRequestObject) (GetConsultResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.M.Get(ctx, a, req.ConsultId.String(), false)
	if err != nil {
		return nil, err
	}
	return GetConsult200JSONResponse(session(v)), nil
}

// PreJoinConsult records the device check and the consent.
func (h Handlers) PreJoinConsult(ctx context.Context, req PreJoinConsultRequestObject) (PreJoinConsultResponseObject, error) {
	a, err := actor(ctx)
	if err != nil || req.Body == nil {
		if err == nil {
			err = problem.BadRequest
		}
		return nil, err
	}
	b := req.Body
	in := consult.PreJoinInput{CameraOK: b.DeviceCheck.CameraOk, MicrophoneOK: b.DeviceCheck.MicrophoneOk, NetworkOK: b.DeviceCheck.NetworkOk,
		ConsentTextID: b.Consent.TextId, ConsentTextVersion: b.Consent.TextVersion, InIndiaNow: b.Consent.InIndiaNow}
	if b.AcceptAudioFallback != nil {
		in.AcceptAudioFallback = *b.AcceptAudioFallback
	}
	v, err := h.M.PreJoin(ctx, a, req.ConsultId.String(), in)
	if err != nil {
		return nil, err
	}
	return PreJoinConsult200JSONResponse(session(v)), nil
}

// JoinConsult issues a join token to the caller.
func (h Handlers) JoinConsult(ctx context.Context, req JoinConsultRequestObject) (JoinConsultResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.M.Join(ctx, a, req.ConsultId.String())
	if err != nil {
		return nil, err
	}
	return JoinConsult200JSONResponse(JoinToken{ConsultId: uid(r.ConsultID), Mode: CallMode(r.Mode), Token: r.Token, TokenExpiresAt: r.ExpiresAt, State: ConsultState(r.State)}), nil
}

// ChangeConsultMode records a drop from video to audio.
func (h Handlers) ChangeConsultMode(ctx context.Context, req ChangeConsultModeRequestObject) (ChangeConsultModeResponseObject, error) {
	a, err := actor(ctx)
	if err != nil || req.Body == nil {
		if err == nil {
			err = problem.BadRequest
		}
		return nil, err
	}
	v, err := h.M.ChangeMode(ctx, a, req.ConsultId.String(), string(req.Body.Mode), string(req.Body.Reason))
	if err != nil {
		return nil, err
	}
	return ChangeConsultMode200JSONResponse(session(v)), nil
}

// ConfirmConsultIdentity records the doctor's identity confirmation (P02).
func (h Handlers) ConfirmConsultIdentity(ctx context.Context, req ConfirmConsultIdentityRequestObject) (ConfirmConsultIdentityResponseObject, error) {
	a, err := actor(ctx)
	if err != nil || req.Body == nil {
		if err == nil {
			err = problem.BadRequest
		}
		return nil, err
	}
	v, err := h.M.ConfirmIdentity(ctx, a, req.ConsultId.String(), req.Body.NameConfirmed, req.Body.AgeConfirmed)
	if err != nil {
		return nil, err
	}
	return ConfirmConsultIdentity200JSONResponse(session(v)), nil
}

// SetConsultOutcome ends the consult.
func (h Handlers) SetConsultOutcome(ctx context.Context, req SetConsultOutcomeRequestObject) (SetConsultOutcomeResponseObject, error) {
	a, err := actor(ctx)
	if err != nil || req.Body == nil {
		if err == nil {
			err = problem.BadRequest
		}
		return nil, err
	}
	v, err := h.M.SetOutcome(ctx, a, req.ConsultId.String(), string(req.Body.Outcome))
	if err != nil {
		return nil, err
	}
	return SetConsultOutcome200JSONResponse(session(v)), nil
}

// ReportConsultMinor stops the consult because the patient is a minor.
func (h Handlers) ReportConsultMinor(ctx context.Context, req ReportConsultMinorRequestObject) (ReportConsultMinorResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.M.ReportMinor(ctx, a, req.ConsultId.String())
	if err != nil {
		return nil, err
	}
	return ReportConsultMinor200JSONResponse(session(v)), nil
}
