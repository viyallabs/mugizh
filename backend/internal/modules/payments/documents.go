package payments

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Patient payment documents (S-134, money-flow T1 and T5). In mode B the clinic is the seller,
// so the document is issued in the clinic's name, on its behalf. Its type follows the clinic's
// tax registration status recorded at onboarding, through the tax configuration (never code):
// a registered clinic issues the registered type (a bill of supply, with its registration ID);
// an unregistered clinic issues the unregistered type (a plain receipt, with no tax fields).
// [VALIDATE-CA] formats, numbering, and the written authorisation in the Provider Agreement.

// DocumentConfig comes from the country pack's tax configuration. A nil Config.Documents means
// no documents are issued (production until the tax configuration is approved by the CA).
type DocumentConfig struct {
	RegisteredType   string         // tax receipts.registered_clinic, e.g. "bill_of_supply"
	UnregisteredType string         // tax receipts.unregistered_clinic, e.g. "receipt"
	FYStartMonth     time.Month     // tax financial_year_start_month
	TaxIDPattern     *regexp.Regexp // tax registration_id_pattern
	Zone             *time.Location // document dates and the financial year (pack default zone)
	// ProhibitedValidators reject identity numbers typed into the free-text fields (K-DP-05).
	ProhibitedValidators []string
}

var reDocType = regexp.MustCompile(`^[a-z_]{1,30}$`)

func (c *DocumentConfig) validate() error {
	if !reDocType.MatchString(c.RegisteredType) || !reDocType.MatchString(c.UnregisteredType) || c.RegisteredType == c.UnregisteredType {
		return errors.New("payments: document types must be two different codes (pack tax receipts)")
	}
	if c.FYStartMonth < time.January || c.FYStartMonth > time.December || c.TaxIDPattern == nil || c.Zone == nil || len(c.ProhibitedValidators) == 0 {
		return errors.New("payments: financial year start, registration ID pattern, zone and validators are required (pack tax)")
	}
	return nil
}

// Clinic tax registration statuses.
const (
	TaxRegistered   = "registered"
	TaxUnregistered = "unregistered"
)

// DocumentProfile is what a clinic admin records for the patient documents.
type DocumentProfile struct {
	LegalName, Address, TaxStatus, TaxID string
}

// ErrDocumentsOff: documents are not issued in this environment (tax configuration not approved).
var ErrDocumentsOff = errors.New("payments: patient documents are not available")

// SetDocumentProfile records the clinic's legal name, address and tax registration (S-134).
// It applies to orders created afterwards only. Audited; the clinic must have a payment link.
func (m *Module) SetDocumentProfile(ctx context.Context, who ClinicReader, clinicRef string, p DocumentProfile) error {
	dc := m.cfg.Documents
	if dc == nil {
		return ErrDocumentsOff
	}
	p.LegalName, p.Address, p.TaxID = strings.TrimSpace(p.LegalName), strings.TrimSpace(p.Address), strings.ToUpper(strings.TrimSpace(p.TaxID))
	nameLen, addrLen := utf8.RuneCountInString(p.LegalName), utf8.RuneCountInString(p.Address)
	switch {
	case !ids.Valid(clinicRef):
		return rp.ErrInvalid
	case nameLen < 2 || nameLen > 120:
		return fmt.Errorf("%w: legal name", rp.ErrInvalid)
	case addrLen < 5 || addrLen > 300:
		return fmt.Errorf("%w: address", rp.ErrInvalid)
	case len(p.TaxID) > 20:
		return fmt.Errorf("%w: registration ID too long", rp.ErrInvalid)
	case scrub.ContainsProhibitedID(p.LegalName+" "+p.Address+" "+p.TaxID, dc.ProhibitedValidators):
		return fmt.Errorf("%w: identity number in free text", rp.ErrInvalid) // K-DP-05
	case p.TaxStatus == TaxRegistered && !dc.TaxIDPattern.MatchString(p.TaxID):
		return fmt.Errorf("%w: registration ID", rp.ErrInvalid)
	case p.TaxStatus == TaxUnregistered && p.TaxID != "":
		return fmt.Errorf("%w: an unregistered clinic has no registration ID", rp.ErrInvalid)
	case p.TaxStatus != TaxRegistered && p.TaxStatus != TaxUnregistered:
		return fmt.Errorf("%w: tax status", rp.ErrInvalid)
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE clinic_merchant_link SET doc_legal_name=$2, doc_address=$3, tax_status=$4, tax_id=$5, updated_at=$6 WHERE clinic_ref=$1`,
			clinicRef, p.LegalName, p.Address, p.TaxStatus, p.TaxID, m.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return rp.ErrNotFound
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: who.UserRef, ActorRole: who.Role, Action: "payments.document_profile_set",
			ResourceType: "clinic_merchant_link", ResourceRef: clinicRef, ReasonCode: p.TaxStatus, Result: ap.Allowed, RequestID: who.RequestID})
	})
}

// documentSnapshot returns the profile to copy onto a new order, or an empty type when no
// document can be issued (documents off, or the clinic has not completed its profile).
func (m *Module) documentSnapshot(ctx context.Context, tx pgx.Tx, clinicRef string) (docType, name, addr, taxID string, err error) {
	dc := m.cfg.Documents
	if dc == nil {
		return "", "", "", "", nil
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT doc_legal_name, doc_address, tax_status, tax_id FROM clinic_merchant_link WHERE clinic_ref=$1`, clinicRef).
		Scan(&name, &addr, &status, &taxID); err != nil {
		return "", "", "", "", err
	}
	switch {
	case name == "" || addr == "":
		return "", "", "", "", nil
	case status == TaxRegistered:
		return dc.RegisteredType, name, addr, taxID, nil
	case status == TaxUnregistered:
		return dc.UnregisteredType, name, addr, "", nil
	}
	return "", "", "", "", nil
}

// fyLabel is the financial year containing t, in the pack's zone, as "2026-27".
func (c *DocumentConfig) fyLabel(t time.Time) string {
	l := t.In(c.Zone)
	start := l.Year()
	if l.Month() < c.FYStartMonth {
		start--
	}
	if c.FYStartMonth == time.January { // review F13: a calendar-year financial year is "2026-26" no longer
		return fmt.Sprintf("%04d-CY", start)
	}
	return fmt.Sprintf("%04d-%02d", start, (start+1)%100)
}

// issueDocument issues the patient document for a captured order, inside the capture
// transaction, with the next number of its series. An order without a document profile gets
// no document and an operations ticket, never a guessed one.
func (m *Module) issueDocument(ctx context.Context, tx pgx.Tx, fx *effects, o orderRow, paymentID string, at time.Time) error {
	dc := m.cfg.Documents
	if dc == nil {
		return nil
	}
	if o.DocType == "" {
		return m.ticket(ctx, tx, fx, "document_profile_missing:"+o.Ref, "document_profile_missing", o.Clinic, o.Appt, AlertDocumentProfileMissing)
	}
	fy := dc.fyLabel(at)
	var no int
	if err := tx.QueryRow(ctx, `INSERT INTO document_series (clinic_ref, doc_type, fy_label, next_no) VALUES ($1,$2,$3,2)
		ON CONFLICT (clinic_ref, doc_type, fy_label) DO UPDATE SET next_no = document_series.next_no + 1
		RETURNING next_no - 1`, o.Clinic, o.DocType, fy).Scan(&no); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO patient_document (doc_ref, order_ref, appointment_ref, clinic_ref, doc_type, number, issued_at, issued_on,
		clinic_legal_name, clinic_address, clinic_tax_id, amount_minor, currency, payment_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (order_ref) DO NOTHING`,
		ids.New(), o.Ref, o.Appt, o.Clinic, o.DocType, fmt.Sprintf("%s/%06d", fy, no), at.UTC(), at.In(dc.Zone).Format(time.DateOnly),
		o.DocLegalName, o.DocAddress, o.DocTaxID, o.Amount, o.Currency, o.Merchant)
	return err
}

// PatientDocument is the issued document (S-134). PDF rendering waits for the PDF library ADR
// (as S-151); the app renders these fields.
type PatientDocument struct {
	DocRef, AppointmentRef, DocType, Number string
	IssuedOn                                string // date in the pack zone
	ClinicLegalName, ClinicAddress          string
	ClinicTaxID                             string // registered clinics only
	AmountMinor                             int64
	Currency, PaymentRef                    string
}

// DocumentFor returns the document of an appointment's payment, or rp.ErrNotFound. The caller
// has checked that the appointment is the patient's own.
func (m *Module) DocumentFor(ctx context.Context, appointmentRef string) (PatientDocument, error) {
	if m.cfg.Documents == nil {
		return PatientDocument{}, ErrDocumentsOff
	}
	var d PatientDocument
	var issued time.Time
	err := m.pool.QueryRow(ctx, `SELECT doc_ref::text, appointment_ref::text, doc_type, number, issued_on, clinic_legal_name, clinic_address, clinic_tax_id,
		amount_minor, currency, payment_ref FROM patient_document WHERE appointment_ref=$1`, appointmentRef).
		Scan(&d.DocRef, &d.AppointmentRef, &d.DocType, &d.Number, &issued, &d.ClinicLegalName, &d.ClinicAddress, &d.ClinicTaxID, &d.AmountMinor, &d.Currency, &d.PaymentRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PatientDocument{}, rp.ErrNotFound
	}
	d.IssuedOn = issued.Format(time.DateOnly)
	return d, err
}
