package app

import (
	"log/slog"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/admin"
	admapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bookapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	clinapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult"
	conapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	dirapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	idapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	notapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	payapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	recapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews"
	revapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/httpapi"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	verapi "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/httpapi"
)

// SpecDocs loads every module's embedded OpenAPI spec.
func SpecDocs() ([]*openapi3.T, error) {
	loaders := []func() (*openapi3.T, error){idapi.GetSpec, recapi.GetSpec, verapi.GetSpec, dirapi.GetSpec, bookapi.GetSpec, payapi.GetSpec, notapi.GetSpec, admapi.GetSpec, clinapi.GetSpec, conapi.GetSpec, revapi.GetSpec}
	specs := make([]*openapi3.T, 0, len(loaders))
	for _, l := range loaders {
		s, err := l()
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, nil
}

// Specs builds the policy table from every spec. A spec that fails to load, or an operation
// without x-roles, stops the service from starting.
func Specs() (Policies, error) {
	specs, err := SpecDocs()
	if err != nil {
		return nil, err
	}
	return BuildPolicies(specs...)
}

// Modules are the built modules the HTTP layer exposes. A nil module is not mounted
// (tests that build only some modules).
type Modules struct {
	Identity     *identity.Module
	Records      *records.Module
	Rights       *records.Rights
	Verification *verification.Module
	Directory    *directory.Module
	Booking      *booking.Module
	Payments     *payments.Module
	Notify       *notifications.Module
	Admin        *admin.Module
	Rx           *clinical.Rx
	Consult      *consult.Module
	Reviews      *reviews.Module // mount only when the "reviews" flag is on (Core.ReviewsRoutes)
	Sharing      *records.Sharing
	History      *clinical.History
}

// Mounts returns the route registrations for every module.
func Mounts(m Modules, pol Policies, log *slog.Logger, now func() time.Time) []func(chi.Router) {
	var out []func(chi.Router)
	if m.Identity != nil {
		out = append(out, func(r chi.Router) {
			si := idapi.NewStrictHandlerWithOptions(idapi.Handlers{M: m.Identity},
				[]idapi.StrictMiddlewareFunc{idapi.StrictMiddlewareFunc(PolicyMiddleware[idapi.StrictHandlerFunc](pol, now))},
				idapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			idapi.HandlerWithOptions(si, idapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Records != nil {
		out = append(out, func(r chi.Router) {
			si := recapi.NewStrictHandlerWithOptions(recapi.Handlers{M: m.Records, R: m.Rights, S: m.Sharing},
				[]recapi.StrictMiddlewareFunc{recapi.StrictMiddlewareFunc(PolicyMiddleware[recapi.StrictHandlerFunc](pol, now))},
				recapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			recapi.HandlerWithOptions(si, recapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Verification != nil {
		out = append(out, func(r chi.Router) {
			si := verapi.NewStrictHandlerWithOptions(verapi.Handlers{M: m.Verification},
				[]verapi.StrictMiddlewareFunc{verapi.StrictMiddlewareFunc(PolicyMiddleware[verapi.StrictHandlerFunc](pol, now))},
				verapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			verapi.HandlerWithOptions(si, verapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Directory != nil {
		out = append(out, func(r chi.Router) {
			si := dirapi.NewStrictHandlerWithOptions(dirapi.Handlers{M: m.Directory},
				[]dirapi.StrictMiddlewareFunc{dirapi.StrictMiddlewareFunc(PolicyMiddleware[dirapi.StrictHandlerFunc](pol, now))},
				dirapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			dirapi.HandlerWithOptions(si, dirapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Booking != nil {
		out = append(out, func(r chi.Router) {
			si := bookapi.NewStrictHandlerWithOptions(bookapi.Handlers{M: m.Booking},
				[]bookapi.StrictMiddlewareFunc{bookapi.StrictMiddlewareFunc(PolicyMiddleware[bookapi.StrictHandlerFunc](pol, now))},
				bookapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			bookapi.HandlerWithOptions(si, bookapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Rx != nil {
		out = append(out, func(r chi.Router) {
			si := clinapi.NewStrictHandlerWithOptions(clinapi.Handlers{Rx: m.Rx, H: m.History},
				[]clinapi.StrictMiddlewareFunc{clinapi.StrictMiddlewareFunc(PolicyMiddleware[clinapi.StrictHandlerFunc](pol, now))},
				clinapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			clinapi.HandlerWithOptions(si, clinapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Consult != nil {
		out = append(out, func(r chi.Router) {
			si := conapi.NewStrictHandlerWithOptions(conapi.Handlers{M: m.Consult},
				[]conapi.StrictMiddlewareFunc{conapi.StrictMiddlewareFunc(PolicyMiddleware[conapi.StrictHandlerFunc](pol, now))},
				conapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			conapi.HandlerWithOptions(si, conapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Reviews != nil {
		out = append(out, func(r chi.Router) {
			si := revapi.NewStrictHandlerWithOptions(revapi.Handlers{M: m.Reviews},
				[]revapi.StrictMiddlewareFunc{revapi.StrictMiddlewareFunc(PolicyMiddleware[revapi.StrictHandlerFunc](pol, now))},
				revapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			revapi.HandlerWithOptions(si, revapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Admin != nil {
		out = append(out, func(r chi.Router) {
			si := admapi.NewStrictHandlerWithOptions(admapi.Handlers{M: m.Admin},
				[]admapi.StrictMiddlewareFunc{admapi.StrictMiddlewareFunc(PolicyMiddleware[admapi.StrictHandlerFunc](pol, now))},
				admapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			admapi.HandlerWithOptions(si, admapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Notify != nil {
		out = append(out, func(r chi.Router) {
			si := notapi.NewStrictHandlerWithOptions(notapi.Handlers{M: m.Notify},
				[]notapi.StrictMiddlewareFunc{notapi.StrictMiddlewareFunc(PolicyMiddleware[notapi.StrictHandlerFunc](pol, now))},
				notapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			notapi.HandlerWithOptions(si, notapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	if m.Payments != nil && m.Booking != nil {
		out = append(out, func(r chi.Router) {
			si := payapi.NewStrictHandlerWithOptions(payapi.Handlers{M: m.Payments, Owners: m.Booking},
				[]payapi.StrictMiddlewareFunc{payapi.StrictMiddlewareFunc(PolicyMiddleware[payapi.StrictHandlerFunc](pol, now))},
				payapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError(log)})
			payapi.HandlerWithOptions(si, payapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: RequestError})
		})
	}
	return out
}
