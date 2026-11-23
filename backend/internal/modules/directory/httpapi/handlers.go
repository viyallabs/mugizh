package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface for the directory module. Errors are
// *problem.Problem values returned by the module; nothing is written by hand.
type Handlers struct{ M *directory.Module }

var _ StrictServerInterface = Handlers{}

var errBody = problem.BadRequest

// actor builds the acting user from the principal that the policy middleware checked.
func actor(ctx context.Context) (directory.Actor, *ip.Principal, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return directory.Actor{}, nil, problem.Unauthenticated
	}
	return directory.Actor{UserID: p.UserID, Role: p.ActingRole, ClinicRef: p.ActingClinic, RequestID: httpx.RequestID(ctx)}, p, nil
}

func modesOut(in []string) []ConsultMode {
	out := make([]ConsultMode, len(in))
	for i, s := range in {
		out[i] = ConsultMode(s)
	}
	return out
}

func modesIn(in []ConsultMode) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func uuidOf(s string) openapi_types.UUID { return uuid.MustParse(s) }

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func card(c directory.Card) ProfessionalCard {
	return ProfessionalCard{ProfessionalId: uuidOf(c.ProfessionalRef), DisplayName: c.DisplayName, Specialty: optStr(c.Specialty),
		SpecialtyLabelKey: directory.SpecialtyLabelKey(c.Specialty), Languages: nonNil(c.Languages), PhotoRef: optStr(c.PhotoRef)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func clinicCard(c directory.ClinicCard) ClinicCard {
	return ClinicCard{ClinicId: uuidOf(c.ID), Name: c.Name, BranchId: uuidOf(c.BranchID), BranchName: c.BranchName, Address: c.Address,
		Location: GeoPoint{Lat: c.Lat, Lng: c.Lng}}
}

func fee(amount int64, currency, version string, at time.Time) Fee {
	return Fee{Amount: amount, Currency: currency, Version: version, UpdatedAt: at}
}

func managed(p directory.ManagedPractice) ManagedPractice {
	return ManagedPractice{PracticeId: uuidOf(p.ID), DoctorId: uuidOf(p.DoctorRef), ClinicId: uuidOf(p.ClinicRef), BranchId: uuidOf(p.BranchRef), Modes: modesOut(p.Modes),
		Fee: fee(p.FeeAmount, p.Currency, p.FeeVersion, p.FeeUpdatedAt), Timings: p.Timings, Active: p.Active, Listed: p.Listed}
}

// SearchPractices is public: no personal data is involved. The body carries the search
// location; it is passed to the module for the query and not kept (RET-12).
func (h Handlers) SearchPractices(ctx context.Context, req SearchPracticesRequestObject) (SearchPracticesResponseObject, error) {
	if req.Body == nil {
		return nil, errBody
	}
	b := req.Body
	q := directory.SearchQuery{MinFee: b.MinFee, MaxFee: b.MaxFee}
	if b.Location != nil {
		q.Location = &directory.Point{Lat: b.Location.Lat, Lng: b.Location.Lng}
	}
	if b.PostalCode != nil {
		q.PostalCode = *b.PostalCode
	}
	if b.RadiusKm != nil {
		q.RadiusKm = *b.RadiusKm
	}
	if b.Specialty != nil {
		q.Specialty = *b.Specialty
	}
	if b.Mode != nil {
		q.Mode = string(*b.Mode)
	}
	if b.AvailableWithinDays != nil {
		q.AvailableWithinDays = *b.AvailableWithinDays
	}
	if b.Limit != nil {
		q.Limit = *b.Limit
	}
	if b.Cursor != nil {
		q.Cursor = *b.Cursor
	}
	res, err := h.M.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	out := SearchPractices200JSONResponse{Items: make([]SearchItem, 0, len(res.Items)), NextCursor: optStr(res.NextCursor)}
	for _, it := range res.Items {
		out.Items = append(out.Items, SearchItem{PracticeId: uuidOf(it.PracticeID), Professional: card(it.Doctor), Clinic: clinicCard(it.Clinic),
			Modes: modesOut(it.Modes), Fee: fee(it.FeeAmount, it.Currency, it.FeeVersion, it.FeeUpdatedAt), DistanceKm: it.DistanceKm, Verified: true})
	}
	return out, nil
}

// GetPractice returns the public profile; unverified or unknown is 404.
func (h Handlers) GetPractice(ctx context.Context, req GetPracticeRequestObject) (GetPracticeResponseObject, error) {
	p, err := h.M.GetProfile(ctx, req.PracticeId.String())
	if err != nil {
		return nil, err
	}
	out := GetPractice200JSONResponse{PracticeId: uuidOf(p.PracticeID), Professional: card(p.Doctor), Clinic: clinicCard(p.Clinic), Modes: modesOut(p.Modes),
		Fee: fee(p.FeeAmount, p.Currency, p.FeeVersion, p.FeeUpdatedAt), Timings: p.Timings, TimeZone: p.TimeZone, Qualifications: nonNil(p.Qualifications)}
	out.Verification.Verified = true
	vat := p.VerifiedAt
	out.Verification.VerifiedAt = &vat
	out.Verification.Checks = nonNil(p.Checks)
	return out, nil
}

// UpsertMyProfessional creates or changes the caller's own doctor profile.
func (h Handlers) UpsertMyProfessional(ctx context.Context, req UpsertMyProfessionalRequestObject) (UpsertMyProfessionalResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBody
	}
	in := directory.ProfessionalInput{DisplayName: req.Body.DisplayName, Languages: req.Body.Languages}
	if req.Body.Specialty != nil {
		in.Specialty = *req.Body.Specialty
	}
	if req.Body.PhotoRef != nil {
		in.PhotoRef = *req.Body.PhotoRef
	}
	p, err := h.M.UpsertProfessional(ctx, a, in)
	if err != nil {
		return nil, err
	}
	return UpsertMyProfessional200JSONResponse{ProfessionalId: uuidOf(p.ID), DisplayName: p.DisplayName, Specialty: optStr(p.Specialty),
		SpecialtyLabelKey: directory.SpecialtyLabelKey(p.Specialty), Languages: nonNil(p.Languages), PhotoRef: optStr(p.PhotoRef), Listed: p.Listed}, nil
}

// SubmitClinic creates a clinic; the caller must be in the professional app with MFA done.
func (h Handlers) SubmitClinic(ctx context.Context, req SubmitClinicRequestObject) (SubmitClinicResponseObject, error) {
	a, p, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if p.App != ip.AppPro {
		return nil, ip.ProblemWrongApp
	}
	if p.MFAAt == nil {
		return nil, problem.MFARequired
	}
	if req.Body == nil {
		return nil, errBody
	}
	b := req.Body
	in := directory.ClinicInput{Name: b.Name, Address: b.Address, LeadDoctorRef: b.LeadDoctorId.String()}
	if b.Pin != nil {
		in.Pin = &directory.Point{Lat: b.Pin.Lat, Lng: b.Pin.Lng}
	}
	if b.TimeZone != nil {
		in.TimeZone = *b.TimeZone
	}
	if b.RegistrationDocRef != nil {
		in.RegistrationDocRef = *b.RegistrationDocRef
	}
	if b.HfrId != nil {
		in.HFRID = *b.HfrId
	}
	c, err := h.M.SubmitClinic(ctx, a, in)
	if err != nil {
		return nil, err
	}
	return SubmitClinic201JSONResponse{ClinicId: uuidOf(c.ID), Name: c.Name, Address: c.Address, Location: GeoPoint{Lat: c.Lat, Lng: c.Lng},
		LocationSource: ManagedClinicLocationSource(c.LocationSource), LeadDoctorId: uuidOf(c.LeadDoctorRef), Listed: c.Listed}, nil
}

// CreatePractice adds a doctor's practice at the caller's clinic.
func (h Handlers) CreatePractice(ctx context.Context, req CreatePracticeRequestObject) (CreatePracticeResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBody
	}
	in := directory.PracticeInput{DoctorRef: req.Body.DoctorId.String(), Modes: modesIn(req.Body.Modes), FeeAmount: req.Body.FeeAmount}
	if req.Body.Timings != nil {
		in.Timings = *req.Body.Timings
	}
	if req.Body.BranchId != nil {
		in.BranchRef = req.Body.BranchId.String()
	}
	p, err := h.M.CreatePractice(ctx, a, req.ClinicId.String(), in)
	if err != nil {
		return nil, err
	}
	return CreatePractice201JSONResponse(managed(p)), nil
}

// UpdatePractice changes a practice the caller owns (doctor) or administers (clinic admin).
func (h Handlers) UpdatePractice(ctx context.Context, req UpdatePracticeRequestObject) (UpdatePracticeResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBody
	}
	ch := directory.PracticeChange{FeeAmount: req.Body.FeeAmount, Timings: req.Body.Timings, Active: req.Body.Active}
	if req.Body.Modes != nil {
		m := modesIn(*req.Body.Modes)
		ch.Modes = &m
	}
	p, err := h.M.UpdatePractice(ctx, a, req.PracticeId.String(), ch)
	if err != nil {
		return nil, err
	}
	return UpdatePractice200JSONResponse(managed(p)), nil
}

// ListMyPracticeInvitations lists the doctor's pending invitations (review M5).
func (h Handlers) ListMyPracticeInvitations(ctx context.Context, _ ListMyPracticeInvitationsRequestObject) (ListMyPracticeInvitationsResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.M.MyInvitations(ctx, a)
	if err != nil {
		return nil, err
	}
	items := make([]PracticeInvitation, 0, len(list))
	for _, i := range list {
		it := PracticeInvitation{InvitationId: uuidOf(i.ID), Kind: PracticeInvitationKind(i.Kind), ClinicId: uuidOf(i.ClinicRef), ClinicName: i.ClinicName,
			Status: PracticeInvitationStatus(i.Status), CreatedAt: i.CreatedAt}
		if i.PracticeRef != "" {
			id := uuidOf(i.PracticeRef)
			it.PracticeId = &id
		}
		items = append(items, it)
	}
	return ListMyPracticeInvitations200JSONResponse{Items: items}, nil
}

// AcceptPracticeInvitation lets the invited doctor accept; others get 404.
func (h Handlers) AcceptPracticeInvitation(ctx context.Context, req AcceptPracticeInvitationRequestObject) (AcceptPracticeInvitationResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.AcceptInvitation(ctx, a, req.InvitationId.String()); err != nil {
		return nil, err
	}
	return AcceptPracticeInvitation204Response{}, nil
}

// DeclinePracticeInvitation lets the invited doctor decline; others get 404.
func (h Handlers) DeclinePracticeInvitation(ctx context.Context, req DeclinePracticeInvitationRequestObject) (DeclinePracticeInvitationResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.DeclineInvitation(ctx, a, req.InvitationId.String()); err != nil {
		return nil, err
	}
	return DeclinePracticeInvitation204Response{}, nil
}

func managedBranch(b directory.Branch) ManagedBranch {
	return ManagedBranch{BranchId: uuidOf(b.ID), ClinicId: uuidOf(b.ClinicRef), Name: b.Name, Address: b.Address, Location: GeoPoint{Lat: b.Lat, Lng: b.Lng},
		LocationSource: ManagedBranchLocationSource(b.LocationSource), TimeZone: b.TimeZone, Main: b.Main, Active: b.Active, Listed: b.Listed}
}

// ListBranches lists the branches of the caller's clinic.
func (h Handlers) ListBranches(ctx context.Context, req ListBranchesRequestObject) (ListBranchesResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	list, err := h.M.ListBranches(ctx, a, req.ClinicId.String())
	if err != nil {
		return nil, err
	}
	out := ListBranches200JSONResponse{Items: make([]ManagedBranch, 0, len(list))}
	for _, b := range list {
		out.Items = append(out.Items, managedBranch(b))
	}
	return out, nil
}

// CreateBranch adds a branch to the caller's clinic.
func (h Handlers) CreateBranch(ctx context.Context, req CreateBranchRequestObject) (CreateBranchResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBody
	}
	in := directory.BranchInput{Name: req.Body.Name, Address: req.Body.Address}
	if req.Body.Pin != nil {
		in.Pin = &directory.Point{Lat: req.Body.Pin.Lat, Lng: req.Body.Pin.Lng}
	}
	if req.Body.TimeZone != nil {
		in.TimeZone = *req.Body.TimeZone
	}
	b, err := h.M.CreateBranch(ctx, a, req.ClinicId.String(), in)
	if err != nil {
		return nil, err
	}
	return CreateBranch201JSONResponse(managedBranch(b)), nil
}

// UpdateBranch changes a branch of the caller's clinic.
func (h Handlers) UpdateBranch(ctx context.Context, req UpdateBranchRequestObject) (UpdateBranchResponseObject, error) {
	a, _, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBody
	}
	ch := directory.BranchChange{Name: req.Body.Name, Address: req.Body.Address, TimeZone: req.Body.TimeZone, Active: req.Body.Active}
	if req.Body.Pin != nil {
		ch.Pin = &directory.Point{Lat: req.Body.Pin.Lat, Lng: req.Body.Pin.Lng}
	}
	b, err := h.M.UpdateBranch(ctx, a, req.ClinicId.String(), req.BranchId.String(), ch)
	if err != nil {
		return nil, err
	}
	return UpdateBranch200JSONResponse(managedBranch(b)), nil
}
