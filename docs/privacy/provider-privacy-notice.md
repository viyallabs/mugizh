# Privacy Notice for Doctors, Clinics and Clinic Staff — [Product Name] (S-275)

**Version:** PPN-1.0-draft · **Status:** DRAFT for lawyer review (S-009). Not legal advice. **[VALIDATE]** = lawyer. **[FILL]** = detail to supply. **[CONFIRM]** = founder.
**Applies to:** doctors, clinic owners and admins, and clinic staff who use [Product Name], and people whose details a clinic gives us (for example its doctors before they sign up). The patient notice is [privacy-notice.md](privacy-notice.md). Purposes and retention rules come from [data-inventory-v1.0.md](../data-inventory-v1.0.md).
**Who we are:** [Company legal name], [registered address].

---

## At a glance

- **We collect** your identity details, registration and qualifications, an identity document, clinic details, payment-account and tax references, and how you use the product.
- **Why:** to verify that you may practise, list you for patients, run bookings and payments, keep the service secure, and meet our legal duties.
- **Public:** your name, specialty, qualifications, registration number and council, languages, clinic address, timings, consult modes, fees and (optional) photo are shown to patients. Ratings are shown from v1.1.
- **Not public:** your phone number, email, identity document, PAN, GSTIN, verification results and internal notes.
- **Kept in India.** Identity document images are deleted 90 days after the verification decision.
- **Your rights:** see, correct and delete your data, and complain to our Grievance Officer, then to the Data Protection Board of India.

## 1. What we collect and why

| Data | Why (purpose) | Basis [VALIDATE] | How long |
|---|---|---|---|
| Name, mobile number, email, sign-in and MFA details | Account and security (D-PROFILE, K-SEC-03) | Consent; needed to provide the service | While you have an account (RET-01) |
| Registration number, council, year, qualifications, specialty | Verification and re-verification (D-VERIFY; C10, K-CS-01) | Consent; legal duty to list only registered practitioners | While listed + 3 years (RET-08) |
| Identity document image (PAN, passport, driving licence or voter ID; **never Aadhaar**) | Confirm you are the registered doctor (D-VERIFY) | Consent | **90 days after the decision**; we keep the result and document type (RET-08, AAD-04) |
| Verification photo, register lookups, vendor check results, reviewer decision and reasons | Verification evidence (D-VERIFY) | Consent; legal duty | While listed + 3 years (RET-08) |
| Public profile: display name, specialty, qualifications, registration number and council, languages, fees, timings, consult modes, optional photo | Directory listing for patients (D-PROFILE; C09, C10 display duties) | Consent | While listed; removed from search when you leave |
| Clinic details: legal name, address, location, clinic registration (C24), staff list | Listing and verification | Consent | While listed + 3 years (RET-08) |
| Payment-aggregator merchant reference, PAN, GSTIN | Payments, subscription invoices, tax (S-019; C20, C34) | Legal duty (tax); contract | 8 years for tax records (RET-04) |
| Availability, bookings, attendance, consultation outcomes, reliability metrics | Run bookings; refunds; service quality | Needed to provide the service | As the booking and payment records (RET-02, RET-04) |
| Prescriptions you sign and your signing record | Clinical record; integrity (C12) | Legal duty to keep records | As clinical records (RET-02) |
| Record access log (which patient records you opened, when, why) | Shown to patients; security (K-DP-02) | Legal duty; security | As the related record, at least 1 year (RET-06) |
| Device, sign-in and security logs | Security and CERT-In duties (C07) | Legal duty | 1 year rolling (RET-07) |
| Reviews about you and your replies (v1.1) | Patient information; moderation | Consent | While listed |
| Complaints about you, and your responses | Grievance handling (C08, C19) | Legal duty | 3 years after closure (RET-09) |
| Product usage (random code, no patient data) | Improve the product | Consent (optional) | 13 months (RET-11) |

## 2. Who we share it with

| Who | What | Why |
|---|---|---|
| Patients | Public profile fields only | Choosing and booking a doctor |
| State Medical Councils, NMC registers, ABDM HPR (v2.0) | Registration number, council, name (lookups) | Verification |
| Verification vendor [FILL after S-042], in India | Registration details, ID document, photo | Verification checks |
| Payment aggregator [FILL after S-040] | Clinic and merchant details | Payments and refunds on your account |
| SMS and email providers | Phone number or email, message text | Sign-in codes and service messages |
| Cloud hosting provider, in India | All data, encrypted | Running the service |
| Government, councils or courts | Only what the law requires, or evidence of a serious conduct complaint where the law allows [VALIDATE] | Legal duties |

Every processor has a written contract that limits use and requires India hosting (K-DP-01, K-DP-08). **We never sell your data**, and never use it for advertising.

## 3. Your rights

See your data; correct it (profile fields in the app; registration details after re-verification); delete it (we close your listing and delete what we can; verification evidence, signed prescriptions, tax and grievance records are kept for the periods above); withdraw optional consents; name a nominee; and complain. Use **Settings → Privacy** or write to the Grievance Officer.

## 4. Security

MFA on every provider account; role-based access for clinic staff; encryption in transit and at rest; every access to patient records is logged; India hosting. If a breach affects you, we will tell you and the authorities as the law requires (S-020).

## 5. Grievance Officer

[Name], [email], [phone]. We acknowledge within **24 hours** and aim to resolve within **7 days**. See the [Grievance Redressal](../legal/grievance-redressal.md) page.

## 6. Changes

We tell you in the app before changes take effect, and ask again where new consent is needed.

---

**Consent texts for provider sign-up (to add to [consent-texts.md](consent-texts.md) as D-* purposes):**
- `D-VERIFY@1`: "I agree that [Product Name] may check my registration with official registers and a verification partner, using the details and identity document I provide. ☐"
- `D-PROFILE@1`: "I agree that my public profile (name, specialty, qualifications, registration number and council, languages, clinic, timings, fees and optional photo) is shown to patients. ☐"
- `D-ANALYTICS@1` (optional): "Help improve [Product Name] with usage data linked to a random code. ☐"
