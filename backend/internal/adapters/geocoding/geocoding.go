// Package geocoding is the geocoding adapter port (adapter-interfaces §10, ADR-16).
//
// It turns a CLINIC address into a point. Patient locations never come here: there is no
// reverse geocoding operation (removed 2026-11-15) and the port takes no patient data.
// If the provider is down or unsure, the caller falls back to a manual pin placed by the
// clinic admin. Vendor implementations live in geocoding/<vendor>/ and are imported only
// by the composition root (rule A1).
package geocoding

import "context"

// Accuracy says how precisely the provider placed the address, finest first.
type Accuracy string

// Accuracy levels. A vendor maps its own scale onto these.
const (
	Rooftop  Accuracy = "rooftop"  // the building
	Street   Accuracy = "street"   // the street or a block
	Locality Accuracy = "locality" // the neighbourhood or town
	Region   Accuracy = "region"   // district, state or country: too coarse for a clinic
)

var rank = map[Accuracy]int{Rooftop: 0, Street: 1, Locality: 2, Region: 3}

// Valid reports whether a is a known level.
func (a Accuracy) Valid() bool { _, ok := rank[a]; return ok }

// AtLeast reports whether a is as precise as, or more precise than, min. An unknown
// accuracy is never good enough (fail closed).
func (a Accuracy) AtLeast(min Accuracy) bool {
	ra, ok1 := rank[a]
	rm, ok2 := rank[min]
	return ok1 && ok2 && ra <= rm
}

// Result is one geocoding answer.
type Result struct {
	Lat, Lng          float64
	Accuracy          Accuracy
	NormalisedAddress string
}

// Geocoder is the port. Errors use the common model in package adapters: NotFound when
// the address cannot be located, Unavailable or Timeout when the provider fails.
type Geocoder interface {
	// Geocode locates a clinic address. countryCode comes from the country pack.
	Geocode(ctx context.Context, addressText, countryCode string) (Result, error)
}
