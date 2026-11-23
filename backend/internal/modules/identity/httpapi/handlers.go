package httpapi

import (
	"context"

	"github.com/google/uuid"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface on top of the identity module. Errors are
// returned as *problem.Problem and written by the app's response error handler.
type Handlers struct{ M *identity.Module }

var _ StrictServerInterface = Handlers{}

func principal(ctx context.Context) (*ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return nil, problem.Unauthenticated
	}
	return p, nil
}

func tokens(t *identity.Tokens) SessionTokens {
	out := SessionTokens{
		UserId: uuid.MustParse(t.UserID), SessionId: uuid.MustParse(t.SessionID), DeviceId: uuid.MustParse(t.DeviceID),
		AccessToken: t.AccessToken, AccessExpiresAt: t.AccessExpiresAt, RefreshToken: t.RefreshToken,
		RefreshExpiresAt: t.RefreshExpiresAt, Restricted: t.Restricted, MfaRequired: t.MFARequired,
	}
	if t.NewAccount {
		v := true
		out.NewAccount = &v
	}
	return out
}

// RequestOtp sends SMS-01.
func (h Handlers) RequestOtp(ctx context.Context, req RequestOtpRequestObject) (RequestOtpResponseObject, error) {
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	ch, err := h.M.RequestOTP(ctx, identity.OTPRequest{Phone: req.Body.Phone, App: ip.App(req.Body.App), IPHash: httpx.IPHash(ctx)})
	if err != nil {
		return nil, err
	}
	return RequestOtp202JSONResponse{ChallengeId: uuid.MustParse(ch.ID), ExpiresAt: ch.ExpiresAt}, nil
}

// CreateSession verifies the code and signs in.
func (h Handlers) CreateSession(ctx context.Context, req CreateSessionRequestObject) (CreateSessionResponseObject, error) {
	b := req.Body
	if b == nil {
		return nil, problem.BadRequest
	}
	in := identity.VerifyInput{ChallengeID: b.ChallengeId.String(), Phone: b.Phone, Code: b.Code, App: ip.App(b.App),
		Device: identity.DeviceInfo{PublicKey: b.Device.PublicKey, Platform: string(b.Device.Platform), Proof: b.Device.Proof},
		IPHash: httpx.IPHash(ctx), RequestID: httpx.RequestID(ctx)}
	if b.ProIntent != nil {
		in.ProIntent = string(*b.ProIntent)
	}
	t, err := h.M.VerifyOTP(ctx, in)
	if err != nil {
		return nil, err
	}
	return CreateSession201JSONResponse(tokens(t)), nil
}

// RefreshSession rotates tokens.
func (h Handlers) RefreshSession(ctx context.Context, req RefreshSessionRequestObject) (RefreshSessionResponseObject, error) {
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	t, err := h.M.Refresh(ctx, identity.RefreshInput{RefreshToken: req.Body.RefreshToken, Timestamp: req.Body.Timestamp, Signature: req.Body.Signature})
	if err != nil {
		return nil, err
	}
	return RefreshSession200JSONResponse(tokens(t)), nil
}

// SignOut ends this session.
func (h Handlers) SignOut(ctx context.Context, _ SignOutRequestObject) (SignOutResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.Logout(ctx, p); err != nil {
		return nil, err
	}
	return SignOut204Response{}, nil
}

// GetMe returns IDs and roles only.
func (h Handlers) GetMe(ctx context.Context, _ GetMeRequestObject) (GetMeResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := GetMe200JSONResponse{UserId: uuid.MustParse(p.UserID), App: AppKind(p.App), Restricted: p.Restricted, MfaComplete: p.MFAAt != nil, Roles: []RoleGrant{}}
	for _, g := range p.Roles {
		rg := RoleGrant{Role: g.Role}
		if g.ClinicRef != "" {
			c := uuid.MustParse(g.ClinicRef)
			rg.ClinicId = &c
		}
		out.Roles = append(out.Roles, rg)
	}
	return out, nil
}

// ListDevices lists the caller's devices.
func (h Handlers) ListDevices(ctx context.Context, _ ListDevicesRequestObject) (ListDevicesResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := h.M.Devices(ctx, p)
	if err != nil {
		return nil, err
	}
	out := ListDevices200JSONResponse{Items: []Device{}}
	for _, d := range ds {
		out.Items = append(out.Items, Device{DeviceId: uuid.MustParse(d.ID), Platform: d.Platform, App: d.App, Status: DeviceStatus(d.Status),
			BoundAt: d.BoundAt, LastSeenAt: d.LastSeenAt, Current: d.Current})
	}
	return out, nil
}

// RevokeDevice signs a device out.
func (h Handlers) RevokeDevice(ctx context.Context, req RevokeDeviceRequestObject) (RevokeDeviceResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.RevokeDevice(ctx, p, req.DeviceId.String()); err != nil {
		return nil, err
	}
	return RevokeDevice204Response{}, nil
}

// ApproveDevice re-verifies a pending device.
func (h Handlers) ApproveDevice(ctx context.Context, req ApproveDeviceRequestObject) (ApproveDeviceResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.ApproveDevice(ctx, p, req.DeviceId.String()); err != nil {
		return nil, err
	}
	return ApproveDevice204Response{}, nil
}

// EnrolTotp starts authenticator set-up.
func (h Handlers) EnrolTotp(ctx context.Context, _ EnrolTotpRequestObject) (EnrolTotpResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	e, err := h.M.EnrolTOTP(ctx, p)
	if err != nil {
		return nil, err
	}
	return EnrolTotp201JSONResponse{FactorId: uuid.MustParse(e.FactorID), Secret: e.Secret, Uri: e.URI}, nil
}

// ConfirmTotp confirms set-up.
func (h Handlers) ConfirmTotp(ctx context.Context, req ConfirmTotpRequestObject) (ConfirmTotpResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.M.ConfirmTOTP(ctx, p, req.Body.Code); err != nil {
		return nil, err
	}
	return ConfirmTotp204Response{}, nil
}

// VerifyMfa completes MFA for this session.
func (h Handlers) VerifyMfa(ctx context.Context, req VerifyMfaRequestObject) (VerifyMfaResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.M.VerifyMFA(ctx, p, req.Body.Code); err != nil {
		return nil, err
	}
	return VerifyMfa204Response{}, nil
}

// StepUp re-authenticates.
func (h Handlers) StepUp(ctx context.Context, req StepUpRequestObject) (StepUpResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.M.StepUp(ctx, p, req.Body.Code); err != nil {
		return nil, err
	}
	return StepUp204Response{}, nil
}

// RequestStepUpOtp sends a patient step-up code to the phone on file (TM-32, TM-33).
func (h Handlers) RequestStepUpOtp(ctx context.Context, _ RequestStepUpOtpRequestObject) (RequestStepUpOtpResponseObject, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := h.M.RequestStepUpOTP(ctx, p, httpx.IPHash(ctx))
	if err != nil {
		return nil, err
	}
	return RequestStepUpOtp202JSONResponse{ChallengeId: uuid.MustParse(ch.ID), ExpiresAt: ch.ExpiresAt}, nil
}

// VerifyStepUpOtp checks a patient step-up code.
func (h Handlers) VerifyStepUpOtp(ctx context.Context, req VerifyStepUpOtpRequestObject) (VerifyStepUpOtpResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.M.VerifyStepUpOTP(ctx, p, req.Body.ChallengeId.String(), req.Body.Code, httpx.IPHash(ctx)); err != nil {
		return nil, err
	}
	return VerifyStepUpOtp204Response{}, nil
}

// SetPushToken stores this device's push token.
func (h Handlers) SetPushToken(ctx context.Context, req SetPushTokenRequestObject) (SetPushTokenResponseObject, error) {
	p, err := principal(ctx)
	if err != nil || req.Body == nil {
		return nil, problem.BadRequest
	}
	if err := h.M.SetPushToken(ctx, p, req.Body.Token); err != nil {
		return nil, err
	}
	return SetPushToken204Response{}, nil
}
