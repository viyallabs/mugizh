package geocoding_test

import (
	"context"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding/fake"
)

func TestAccuracyAtLeast(t *testing.T) {
	cases := []struct {
		a, min geocoding.Accuracy
		want   bool
	}{
		{geocoding.Rooftop, geocoding.Street, true},
		{geocoding.Street, geocoding.Street, true},
		{geocoding.Locality, geocoding.Street, false},
		{geocoding.Region, geocoding.Locality, false},
		{"", geocoding.Region, false},       // unknown never good enough
		{geocoding.Rooftop, "weird", false}, // unknown threshold fails closed
	}
	for _, c := range cases {
		if got := c.a.AtLeast(c.min); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.a, c.min, got, c.want)
		}
	}
}

func TestFakeGeocoder(t *testing.T) {
	g := &fake.Geocoder{}
	g.Add("1 Test Street, Testville", "XX", geocoding.Result{Lat: 10, Lng: 20, Accuracy: geocoding.Rooftop})
	r, err := g.Geocode(context.Background(), "1  test street,  TESTVILLE", "xx")
	if err != nil || r.Lat != 10 || r.Lng != 20 {
		t.Fatalf("known address: %+v %v", r, err)
	}
	if _, err := g.Geocode(context.Background(), "nowhere", "XX"); adapters.KindOf(err) != adapters.NotFound {
		t.Fatalf("unknown address: %v", err)
	}
	g.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	if _, err := g.Geocode(context.Background(), "1 Test Street, Testville", "XX"); adapters.KindOf(err) != adapters.Unavailable {
		t.Fatalf("fault injection: %v", err)
	}
	if g.Calls() != 3 {
		t.Fatalf("calls = %d", g.Calls())
	}
}
