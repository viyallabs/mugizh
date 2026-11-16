# Data-rights process and grievance handling

**Story:** S-010 · **Status:** DRAFT for lawyer review (S-009). Not legal advice.
**For:** the founder and support team who will run it, and engineering (S-060, S-142, S-143).
**Compliance:** C02 (DPDP rights), C08 (Intermediary Rules grievance), C19 (E-Commerce Rules grievance), C30 (record retention), N1 (SPDI grievance officer). **Contracts:** K-DP-07 (erasure reaches every store), K-SEC-08 (staff access logged).
**Edge cases covered:** E-DP-03 (deletion vs legal retention), E-DP-10 (nominee after death or incapacity).

## 1. Timelines

The same complaint can fall under three rule sets. We use the strictest for everything so staff follow one clock.

| Step | Our target | Strictest source **[VALIDATE all]** |
|---|---|---|
| Acknowledge any request or complaint | **24 hours** | IT Intermediary Rules 2021 (24 h acknowledgement) |
| Resolve a complaint | **7 days** | IT Intermediary Rules as amended 10 Feb 2026, in force 20 Feb 2026 (7 days, was 15); E-Commerce Rules 2020 (one month); DPDP Rules (up to 90 days) |
| Remove content reported as unlawful (for example a profile or review) | **36 hours**; intimate or morphed images **2 hours** | IT Rules amendment 2026 (was 72 h and 24 h) |
| Act on a court order or government notice to remove content | **3 hours** | IT Rules amendment 2026 (was 36 h) |
| Act on a data-rights request (access, correction, erasure, nominee) | **15 days.** For erasure: data hidden and processing stopped within 15 days; data under the legal-minimum hold (RET-18: 180 days for registration data; from May 2027, 1 year for processing records) stays locked, then is deleted | DPDP Rules (up to 90 days); Intermediary Rules 3(1)(h); DPDP Rules 8(3); our own target |
| Give a copy of medical records | **72 hours** | IMC Regulations 2002, reg. 1.3.2 [VALIDATE] |
| Withdraw an optional consent | **Immediately**, automatic | DPDP s.6(4): as easy as giving |
| Stop sharing with a doctor | **Immediately**, automatic | K-DP-02, E-DP-01 |

## 2. How a person makes a request

- **In the app:** Settings → Privacy → My data rights. Choose: *See my data*, *Correct my data*, *Delete my account*, *Name a nominee*, *Make a complaint*.
- **By email or post** to the Grievance Officer (for people who have lost access to the app).
- **Identity check:** in-app requests use the signed-in session plus a fresh OTP. Email or post requests need an OTP to the registered number, or, if the number is lost, the account-recovery check (S-115). Never ask for an Aadhaar number (C23, K-DP-05).

Every request gets a reference number and the 24-hour acknowledgement, which states the target date.

## 3. What happens for each right

| Right | What we do | Who does it | Notes |
|---|---|---|---|
| **Access** | Give a summary: data categories held, purposes, consents and dates, who we shared with (section 5 of the notice), and the record access log. Download of the medical record in PDF. | Automated; support checks | Free of charge. **[VALIDATE: format required by the Rules]** |
| **Correction** | Profile details: the user edits them directly. Clinical content: we send the request to the treating doctor, who adds a corrected version; the original stays (K-CS-05). We tell the user the outcome. | User / doctor | If the doctor disagrees, the user's note of disagreement is attached to the record. **[VALIDATE]** |
| **Erasure** | Close the account. Delete everything not under legal retention from every store: database, search index, caches, object storage, vendors, analytics. Backups: per-user key destroyed (crypto-shredding). Records under legal retention are **restricted, not deleted**: hidden from the app, reachable only by the user, the treating doctor or a legal order, then deleted when the period ends. | Automated (K-DP-07) + support checks vendors | Tell the user exactly what was kept and why, and until when (E-DP-03). |
| **Withdraw consent** | Optional items: self-service toggle, immediate. Needed items: through account closure (as erasure). | User | Consent record written for every change (K-DP-02). |
| **Nominee** | User names one person (name, relation, phone). If the user dies or cannot act, the nominee sends proof (death certificate, or medical or court document of incapacity) and an OTP to the nominee's own phone. Two staff members check it (four-eyes) before access is given. | Support + second reviewer | Logged in the audit trail. **[VALIDATE: proof standards; E-DP-10]** |
| **Complaint** | Logged with a reference number, acknowledged within 24 h, resolved within 7 days. The reply explains the outcome and how to go to the Data Protection Board of India. | Grievance Officer | Complaints about doctors' conduct are passed to the clinic and, if serious, noted for re-verification (R-13). |

## 4. Grievance Officer

- **Must be named** before the v0 pilot starts (the SPDI Rules require a grievance officer now; open decision 1). **[Name, email, phone, address: FILL]**
- Shown in the app (Settings → Help), on the website, in the privacy notice and on the v0 pilot form.
- Must be resident in India. **[VALIDATE: E-Commerce Rules / Intermediary Rules residency requirement]**
- Keeps a log: reference, date received, type, acknowledged at, resolved at, outcome. Monthly summary to the founder. If the platform is ever classed as a significant social media intermediary, a monthly compliance report becomes mandatory; this is not expected **[VALIDATE]**.

## 5. Breach communication (full steps in [incident-response-runbook.md](../security/incident-response-runbook.md), S-020)

If a personal data breach affects users:
1. Report to CERT-In within **6 hours** of noticing it (C07).
2. Tell affected users and the Data Protection Board of India without delay, with a detailed report to the Board within **72 hours** (DPDP Rules). **[VALIDATE]**
3. The message to users says what happened, what data, what we are doing, what they can do, and who to contact.

## 6. Records to keep (evidence for C02 and S-177)

- Request and complaint log with timestamps (for SLA reports).
- Consent records (K-DP-02).
- Erasure job reports showing each store cleared, with legal-retention exceptions listed.
- Nominee verification evidence.

## 7. Build stories that use this

S-060 (erasure across stores), S-142 and S-143 (data-rights and grievance flows), S-106 to S-108 (consent screens and records), S-115 (account recovery), S-020 (breach runbook).
