package identity_test

import (
	"context"
	"errors"
	"testing"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// S-172: the support lookup finds an active account by number and returns only the opaque
// reference, the account's age, roles and active device count: no name, contact detail or
// health data (AccountSummary has no such fields). Unknown, invalid and closed accounts are
// all "not found".
func TestS172LookupByPhone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ph := phone("71")
	tok := f.signIn(t, ph, ip.AppPatient, newDevice(t), "")
	p, err := f.m.Authenticate(ctx, tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := f.m.LookupByPhone(ctx, ph)
	if err != nil || sum.UserID != p.UserID || sum.ActiveDevices != 1 || sum.CreatedOn == "" || len(sum.Roles) == 0 {
		t.Fatalf("lookup: %+v %v", sum, err)
	}
	for _, bad := range []string{phone("72"), "12345", "", "not a number"} {
		if _, err := f.m.LookupByPhone(ctx, bad); !errors.Is(err, ip.ErrAccountNotFound) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}
