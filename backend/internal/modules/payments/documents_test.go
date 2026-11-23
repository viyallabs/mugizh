package payments_test

import (
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Synthetic test values (K-GLB-01: shipped code names no country). The registration ID has the
// structure the IN pack describes but belongs to nobody.
const (
	testRegID   = "27ABCDE1234F1Z5"
	docZoneName = "Asia/Kolkata"
)

func docConfig(t *testing.T) *payments.DocumentConfig {
	t.Helper()
	zone, err := time.LoadLocation(docZoneName)
	if err != nil {
		t.Fatal(err)
	}
	return &payments.DocumentConfig{RegisteredType: "bill_of_supply", UnregisteredType: "receipt", FYStartMonth: time.April,
		TaxIDPattern: regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`), Zone: zone,
		ProhibitedValidators: []string{"verhoeff_12_digit_first_2_to_9", "digits_16"}}
}

// docSetup is setup with patient documents on (S-134).
func docSetup(t *testing.T) *fx {
	t.Helper()
	f := setup(t)
	m, err := payments.New(f.env.Pools.For("payments"), f.fk, payments.Config{Currency: f.pol.Currency, Policy: f.pol,
		AllowedModes: []string{"clinic_collected", "pay_at_clinic"}, Documents: docConfig(t)}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(func() time.Time { return f.now })
	f.m = m
	return f
}

func admin() payments.ClinicReader {
	return payments.ClinicReader{UserRef: ids.New(), Role: "clinic_admin", RequestID: ids.New()}
}

func (f *fx) profile(t *testing.T, status, taxID string) {
	t.Helper()
	if err := f.m.SetDocumentProfile(ctx, admin(), f.clinic, payments.DocumentProfile{LegalName: "Synthetic Health Clinic LLP",
		Address: "12 Test Street, Sample Town", TaxStatus: status, TaxID: taxID}); err != nil {
		t.Fatal(err)
	}
}

// S-134, money-flow T1/T5: an unregistered clinic's patient gets a plain receipt with no tax
// fields; a registered clinic's patient gets the registered type with its registration ID.
func TestS134DocumentTypeFollowsTheClinicsTaxStatus(t *testing.T) {
	f := docSetup(t)
	f.profile(t, payments.TaxUnregistered, "")
	appt, _, _ := f.paid(t)
	d, err := f.m.DocumentFor(ctx, appt)
	if err != nil || d.DocType != "receipt" || d.ClinicTaxID != "" || d.Number != "2026-27/000001" || d.AmountMinor != amount ||
		d.ClinicLegalName == "" || d.IssuedOn != "2026-11-17" || d.PaymentRef == "" {
		t.Fatalf("unregistered: %+v %v", d, err)
	}
	f.profile(t, payments.TaxRegistered, strings.ToLower(testRegID)) // stored upper-case
	appt2, _, _ := f.paid(t)
	d2, err := f.m.DocumentFor(ctx, appt2)
	if err != nil || d2.DocType != "bill_of_supply" || d2.ClinicTaxID != testRegID || d2.Number != "2026-27/000001" {
		t.Fatalf("registered (own number series): %+v %v", d2, err)
	}
}

// money-flow section 6: a change of tax status applies only to bookings made afterwards.
func TestS134StatusChangeAppliesToNewBookingsOnly(t *testing.T) {
	f := docSetup(t)
	f.profile(t, payments.TaxUnregistered, "")
	appt, co := f.open(t) // booked while unregistered
	f.profile(t, payments.TaxRegistered, testRegID)
	pay := "pay_" + strings.ReplaceAll(ids.New(), "-", "")[16:]
	if err := f.fk.PayOrder(co.ProviderOrderID, pay, f.now); err != nil {
		t.Fatal(err)
	}
	f.deliver(t)
	if d, err := f.m.DocumentFor(ctx, appt); err != nil || d.DocType != "receipt" || d.ClinicTaxID != "" {
		t.Fatalf("the profile at booking time applies: %+v %v", d, err)
	}
}

// Numbers run without gaps per clinic, type and financial year; the year turns over at the pack's
// start month in the pack's zone, not in UTC.
func TestS134NumberingAndFinancialYear(t *testing.T) {
	f := docSetup(t)
	f.profile(t, payments.TaxUnregistered, "")
	var nums []string
	for i := 0; i < 3; i++ {
		appt, _, _ := f.paid(t)
		d, err := f.m.DocumentFor(ctx, appt)
		if err != nil {
			t.Fatal(err)
		}
		nums = append(nums, d.Number)
	}
	if strings.Join(nums, ",") != "2026-27/000001,2026-27/000002,2026-27/000003" {
		t.Fatalf("gapless: %v", nums)
	}
	zone, _ := time.LoadLocation(docZoneName)
	f.now = time.Date(2027, 3, 31, 23, 0, 0, 0, zone) // 31 March locally (17:30 UTC): still the old financial year
	appt, _, _ := f.paid(t)
	if d, _ := f.m.DocumentFor(ctx, appt); d.Number != "2026-27/000004" {
		t.Fatalf("31 March local: %s", d.Number)
	}
	f.now = time.Date(2027, 4, 1, 0, 30, 0, 0, zone) // 31 March 19:00 UTC, but 1 April locally
	appt, _, _ = f.paid(t)
	if d, _ := f.m.DocumentFor(ctx, appt); d.Number != "2027-28/000001" || d.IssuedOn != "2027-04-01" {
		t.Fatalf("1 April local starts a new series: %+v", d)
	}
}

// No guessed document: a clinic without a complete profile gets a ticket, the patient gets none.
func TestS134MissingProfileOpensATicket(t *testing.T) {
	f := docSetup(t)
	appt, _, _ := f.paid(t)
	if _, err := f.m.DocumentFor(ctx, appt); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("no document without a profile: %v", err)
	}
	if f.n(t, `SELECT count(*) FROM payments.ops_ticket WHERE kind='document_profile_missing'`) != 1 {
		t.Fatal("ticket expected")
	}
}

func TestS134ProfileValidationAndSwitchOff(t *testing.T) {
	f := docSetup(t)
	bad := []payments.DocumentProfile{
		{LegalName: "Synthetic Clinic", Address: "12 Test Street", TaxStatus: payments.TaxRegistered},                     // no ID
		{LegalName: "Synthetic Clinic", Address: "12 Test Street", TaxStatus: payments.TaxRegistered, TaxID: "NOT-AN-ID"}, // bad format
		{LegalName: "Synthetic Clinic", Address: "12 Test Street", TaxStatus: payments.TaxUnregistered, TaxID: testRegID}, // ID when unregistered
		{LegalName: "Synthetic Clinic", Address: "12 Test Street", TaxStatus: "exempt"},                                   // unknown status
		{LegalName: "X", Address: "12 Test Street", TaxStatus: payments.TaxUnregistered},                                  // name too short
	}
	for i, p := range bad {
		if err := f.m.SetDocumentProfile(ctx, admin(), f.clinic, p); !errors.Is(err, rp.ErrInvalid) {
			t.Fatalf("case %d must be refused: %v", i, err)
		}
	}
	if err := f.m.SetDocumentProfile(ctx, admin(), ids.New(), payments.DocumentProfile{LegalName: "Synthetic Clinic", Address: "12 Test Street",
		TaxStatus: payments.TaxUnregistered}); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("a clinic without a payment link: %v", err)
	}
	// Issued documents are financial records: the module role cannot change or delete them.
	f.profile(t, payments.TaxUnregistered, "")
	f.paid(t)
	pool := f.env.Pools.For("payments")
	for _, q := range []string{`UPDATE patient_document SET amount_minor = 1`, `DELETE FROM patient_document`, `TRUNCATE patient_document`, `DELETE FROM document_series`} {
		if _, err := pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Documents switched off (production until the CA approves the tax configuration).
	off := setup(t)
	if _, err := off.m.DocumentFor(ctx, ids.New()); !errors.Is(err, payments.ErrDocumentsOff) {
		t.Fatalf("off: %v", err)
	}
	if err := off.m.SetDocumentProfile(ctx, admin(), off.clinic, payments.DocumentProfile{}); !errors.Is(err, payments.ErrDocumentsOff) {
		t.Fatalf("off: %v", err)
	}
}

// Review F9 (K-DP-05): an Aadhaar-like number typed as the registration ID is refused, and an
// over-long ID is a validation error, not a database error. The number is made at run time.
func TestReviewF9TaxIDChecks(t *testing.T) {
	f := docSetup(t)
	var aadhaar string
	for d := '0'; d <= '9'; d++ {
		if s := "29876543210" + string(d); scrub.Verhoeff(s) {
			aadhaar = s
		}
	}
	for _, id := range []string{aadhaar, strings.Repeat("A", 21)} {
		err := f.m.SetDocumentProfile(ctx, admin(), f.clinic, payments.DocumentProfile{LegalName: "Synthetic Clinic", Address: "12 Test Street",
			TaxStatus: payments.TaxRegistered, TaxID: id})
		if !errors.Is(err, rp.ErrInvalid) {
			t.Fatalf("%q must be refused: %v", id, err)
		}
	}
}
