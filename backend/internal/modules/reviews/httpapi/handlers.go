// Package httpapi implements the reviews OpenAPI operations (api/core/reviews.v1.yaml).
package httpapi

import (
	"context"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Handlers implements StrictServerInterface. Errors are *problem.Problem values returned by the
// module; nothing is written by hand.
type Handlers struct{ M *reviews.Module }

var _ StrictServerInterface = Handlers{}

// actor builds the acting patient from the principal (K-SEC-04).
func actor(ctx context.Context) (reviews.Actor, error) {
	p, ok := ip.FromContext(ctx)
	if !ok {
		return reviews.Actor{}, problem.Unauthenticated
	}
	if p.ActingRole != ip.RolePatient {
		return reviews.Actor{}, problem.Forbidden
	}
	return reviews.Actor{UserID: p.UserID, Role: p.ActingRole, RequestID: httpx.RequestID(ctx)}, nil
}

func uid(s string) openapi_types.UUID { return uuid.MustParse(s) }

func review(r reviews.Review) Review {
	return Review{ReviewId: uid(r.ID), AppointmentId: uid(r.AppointmentRef), DoctorId: uid(r.DoctorRef), Rating: r.Rating,
		Text: r.Text, TextState: TextState(r.TextState), CreatedAt: r.CreatedAt}
}

// CreateReview records the patient's review of a completed appointment.
func (h Handlers) CreateReview(ctx context.Context, req CreateReviewRequestObject) (CreateReviewResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, problem.BadRequest
	}
	r, err := h.M.Create(ctx, a, req.AppointmentId.String(), reviews.CreateInput{Rating: req.Body.Rating, Text: req.Body.Text})
	if err != nil {
		return nil, err
	}
	return CreateReview201JSONResponse(review(r)), nil
}

// ListMyReviews lists the patient's own reviews.
func (h Handlers) ListMyReviews(ctx context.Context, req ListMyReviewsRequestObject) (ListMyReviewsResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	items, next, err := h.M.ListMine(ctx, a, limit, cursor)
	if err != nil {
		return nil, err
	}
	out := ListMyReviews200JSONResponse{Items: make([]Review, 0, len(items))}
	for _, r := range items {
		out.Items = append(out.Items, review(r))
	}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// WithdrawReview deletes one of the patient's own reviews.
func (h Handlers) WithdrawReview(ctx context.Context, req WithdrawReviewRequestObject) (WithdrawReviewResponseObject, error) {
	a, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.M.Withdraw(ctx, a, req.ReviewId.String()); err != nil {
		return nil, err
	}
	return WithdrawReview204Response{}, nil
}

// GetDoctorRating returns the public rating of a Verified doctor.
func (h Handlers) GetDoctorRating(ctx context.Context, req GetDoctorRatingRequestObject) (GetDoctorRatingResponseObject, error) {
	r, err := h.M.Rating(ctx, req.DoctorId.String())
	if err != nil {
		return nil, err
	}
	return GetDoctorRating200JSONResponse(DoctorRating{Count: r.Count, Rating: r.Value}), nil
}
