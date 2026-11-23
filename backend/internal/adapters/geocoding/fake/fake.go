// Package fake is the geocoding test double (adapter rule A10). It answers only for
// addresses registered with Add, so tests never depend on a provider, and it keeps no
// real addresses. It is refused outside local, test and dev by the config loader.
package fake

import (
	"context"
	"strings"
	"sync"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
)

// Geocoder is the fake.
type Geocoder struct {
	FailWith error // when set, Geocode returns it (fault injection)

	mu    sync.Mutex
	known map[string]geocoding.Result
	calls int
}

func key(addr, country string) string {
	return strings.ToLower(strings.Join(strings.Fields(addr), " ")) + "|" + strings.ToUpper(country)
}

// Add registers the answer for an address.
func (g *Geocoder) Add(addressText, countryCode string, r geocoding.Result) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.known == nil {
		g.known = map[string]geocoding.Result{}
	}
	g.known[key(addressText, countryCode)] = r
}

// Calls returns how many times Geocode was called.
func (g *Geocoder) Calls() int { g.mu.Lock(); defer g.mu.Unlock(); return g.calls }

// Geocode implements geocoding.Geocoder.
func (g *Geocoder) Geocode(ctx context.Context, addressText, countryCode string) (geocoding.Result, error) {
	g.mu.Lock()
	g.calls++
	fail := g.FailWith
	r, ok := g.known[key(addressText, countryCode)]
	g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return geocoding.Result{}, adapters.E(adapters.Timeout, "CONTEXT")
	}
	if fail != nil {
		return geocoding.Result{}, fail
	}
	if !ok {
		return geocoding.Result{}, adapters.E(adapters.NotFound, "ADDRESS_NOT_FOUND")
	}
	return r, nil
}

var _ geocoding.Geocoder = (*Geocoder)(nil)
