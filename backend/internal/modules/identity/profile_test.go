package identity_test

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

const aadhaarValidator = "verhoeff_12_digit_first_2_to_9" // pack validator name (test value)

func profileSetup(t *testing.T) (*fx, string) {
	t.Helper()
	f := setup(t)
	f.m.SetProfileConfig(identity.ProfileConfig{MinAccountAge: 18, ProhibitedValidators: []string{aadhaarValidator, "digits_16"}})
	tok := f.signIn(t, phone("31"), ip.AppPatient, newDevice(t), "")
	return f, tok.UserID
}

func validProfile(f *fx) identity.Profile {
	dob := f.now.AddDate(-30, 0, -10).Format("2006-01-02")
	return identity.Profile{FullName: "Test Patient", DateOfBirth: dob, Sex: "female", PreferredLanguage: "ta",
		BloodGroup: "O+", Allergies: []string{"penicillin"}, EmergencyContact: &identity.EmergencyContact{Name: "Test Relative", Phone: phone("77")}}
}

// verhoeffValid12 builds a synthetic 12-digit number that passes the Verhoeff check, only
// to prove it is rejected (K-DP-05); it is not a real identifier.
func verhoeffValid12(t *testing.T) string {
	t.Helper()
	for d := 0; d < 10; d++ {
		s := "23456789012" + strconv.Itoa(d)
		if scrub.Verhoeff(s) {
			return s
		}
	}
	t.Fatal("no valid check digit")
	return ""
}

func TestS108ProfileRoundTripAndFacts(t *testing.T) {
	f, uid := profileSetup(t)
	ctx := context.Background()
	p := validProfile(f)
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatal(err)
	}
	got, err := f.m.GetProfile(ctx, uid)
	if err != nil || got.FullName != p.FullName || got.BloodGroup != "O+" || got.PreferredLanguage != "ta" ||
		len(got.Allergies) != 1 || got.EmergencyContact == nil || got.EmergencyContact.Phone != phone("77") {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	name, age, sex, err := f.m.ProfileFacts(ctx, uid)
	if err != nil || name != p.FullName || age != 30 || sex != "female" {
		t.Fatalf("facts: %q %d %q %v", name, age, sex, err)
	}
	// A birthday not yet reached this year: still 29 (completed years).
	p.DateOfBirth = f.now.AddDate(-30, 0, 5).Format("2006-01-02")
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatal(err)
	}
	if _, age, _, _ = f.m.ProfileFacts(ctx, uid); age != 29 {
		t.Fatalf("age = %d, want 29", age)
	}
}

// K-DP-03: every personal profile column holds ciphertext, never plaintext.
func TestS108ProfileFieldsAreSealed(t *testing.T) {
	f, uid := profileSetup(t)
	ctx := context.Background()
	vp := validProfile(f)
	if err := f.m.PutProfile(ctx, uid, vp); err != nil {
		t.Fatal(err)
	}
	var cts [6][]byte
	if err := f.env.Owner.QueryRow(ctx, `SELECT full_name_ct, dob_ct, sex_ct, blood_group_ct, allergies_ct, emergency_contact_ct FROM identity.profile WHERE user_id=$1`, uid).
		Scan(&cts[0], &cts[1], &cts[2], &cts[3], &cts[4], &cts[5]); err != nil {
		t.Fatal(err)
	}
	for i, plain := range []string{"Test Patient", vp.DateOfBirth, "female", "O+", "penicillin", "Test Relative"} {
		if len(cts[i]) == 0 || bytes.Contains(cts[i], []byte(plain)) {
			t.Fatalf("column %d is not sealed", i)
		}
	}
}

// K-DP-10: adults only; the pack's min_account_age decides.
func TestS108UnderageProfileRefused(t *testing.T) {
	f, uid := profileSetup(t)
	ctx := context.Background()
	p := validProfile(f)
	p.DateOfBirth = f.now.AddDate(-17, 0, -1).Format("2006-01-02")
	if err := f.m.PutProfile(ctx, uid, p); code(err) != "identity.profile_underage" {
		t.Fatalf("got %v", err)
	}
	p.DateOfBirth = f.now.AddDate(-18, 0, 1).Format("2006-01-02") // turns 18 tomorrow
	if err := f.m.PutProfile(ctx, uid, p); code(err) != "identity.profile_underage" {
		t.Fatalf("day before 18th birthday: %v", err)
	}
	p.DateOfBirth = f.now.AddDate(-18, 0, 0).Format("2006-01-02") // 18 today
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatalf("exactly 18 must pass: %v", err)
	}
	p.DateOfBirth = f.now.AddDate(0, 0, 1).Format("2006-01-02")
	if err := f.m.PutProfile(ctx, uid, p); code(err) != "identity.profile_invalid" {
		t.Fatalf("future date: %v", err)
	}
}

// TC-20 / K-DP-05: an Aadhaar-like number in any free-text field is refused.
func TestTC20ProfileRejectsAadhaarLikeNumbers(t *testing.T) {
	f, uid := profileSetup(t)
	ctx := context.Background()
	num := verhoeffValid12(t)
	spaced := num[:4] + " " + num[4:8] + " " + num[8:]
	for name, mut := range map[string]func(*identity.Profile){
		"name":      func(p *identity.Profile) { p.FullName = "Test " + spaced },
		"allergy":   func(p *identity.Profile) { p.Allergies = []string{"dust " + num} },
		"ec name":   func(p *identity.Profile) { p.EmergencyContact.Name = num },
		"vid 16":    func(p *identity.Profile) { p.Allergies = []string{"1234567890123456"} },
		"long text": func(p *identity.Profile) { p.Allergies = []string{strings.Repeat("a", 5) + " " + spaced + " note"} },
	} {
		p := validProfile(f)
		mut(&p)
		if err := f.m.PutProfile(ctx, uid, p); code(err) != "identity.profile_prohibited_identifier" {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	if _, err := f.m.GetProfile(ctx, uid); code(err) != "identity.profile_missing" {
		t.Fatalf("nothing must be stored after refusals: %v", err)
	}
}

func TestS108ProfileFailsClosedWithoutConfigAndValidatesFields(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	uid := f.signIn(t, phone("32"), ip.AppPatient, newDevice(t), "").UserID
	if err := f.m.PutProfile(ctx, uid, validProfile(f)); code(err) != "identity.profile_unavailable" {
		t.Fatalf("unconfigured: %v", err)
	}
	f.m.SetProfileConfig(identity.ProfileConfig{MinAccountAge: 18, ProhibitedValidators: []string{aadhaarValidator, "digits_16"}})
	for name, mut := range map[string]func(*identity.Profile){
		"sex":        func(p *identity.Profile) { p.Sex = "x" },
		"blood":      func(p *identity.Profile) { p.BloodGroup = "Z+" },
		"empty name": func(p *identity.Profile) { p.FullName = "  " },
		"ec phone":   func(p *identity.Profile) { p.EmergencyContact.Phone = "+1 555 0100" },
		"language":   func(p *identity.Profile) { p.PreferredLanguage = "not a language" },
	} {
		p := validProfile(f)
		mut(&p)
		if err := f.m.PutProfile(ctx, uid, p); code(err) != "identity.profile_invalid" {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	if _, _, _, err := f.m.ProfileFacts(ctx, uid); code(err) != "identity.profile_missing" {
		t.Fatalf("facts without a profile must fail: %v", err)
	}
}

// Review F3: only a change of name, date of birth or sex moves the identity-change time;
// allergies or the emergency contact do not.
func TestReviewF3IdentityChangeTime(t *testing.T) {
	f, uid := profileSetup(t)
	ctx := context.Background()
	if _, err := f.m.ProfileIdentityChangedAt(ctx, uid); err == nil {
		t.Fatal("no profile: must fail closed")
	}
	p := validProfile(f)
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatal(err)
	}
	first, err := f.m.ProfileIdentityChangedAt(ctx, uid)
	if err != nil || first.IsZero() {
		t.Fatalf("first: %v %v", first, err)
	}
	f.now = f.now.Add(time.Hour)
	p.Allergies = []string{"penicillin", "latex"}
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.m.ProfileIdentityChangedAt(ctx, uid); !again.Equal(first) {
		t.Fatalf("an allergy edit moved the identity time: %v -> %v", first, again)
	}
	f.now = f.now.Add(time.Hour)
	p.FullName = "Test Patient Renamed"
	if err := f.m.PutProfile(ctx, uid, p); err != nil {
		t.Fatal(err)
	}
	if moved, _ := f.m.ProfileIdentityChangedAt(ctx, uid); !moved.After(first) {
		t.Fatalf("a name change must move the identity time: %v -> %v", first, moved)
	}
}
