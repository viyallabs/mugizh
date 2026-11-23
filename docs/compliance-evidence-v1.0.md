# v1.0 compliance evidence pack (S-177)

**Story:** S-177 · **Compliance:** every C-ID due by v1.0 (Compliance sheet) · **Date:** 2026-11-23
**Status:** In Testing, evidence prepared; **sign-off is the founder's and the lawyer's** (acceptance criterion: "register shows Implemented/Verified"). Claude does not change the register's Status column: marking a legal requirement as met is a legal judgement (CLAUDE.md section 5.2). This pack is the evidence the reviewers need to decide.

**Who this is for:** the founder, the lawyer, the CA and the doctor reviewer, for the v1.0 sign-off meeting.

**How to read the Built column:**
- **Built:** the product side exists, with tests. The evidence column lists the tests.
- **Partly built:** some of it exists; the gap is named.
- **Not built:** nothing exists yet.
- **Not code:** a contract, filing or publication owned by a person.

Every "Built" still needs the reviewer named in the register to confirm it meets the law; items marked [VALIDATE] in the docs stay open until then. Test names refer to the Go test suite (`tools/dev.sh backend go test ./...`), which passed on 2026-11-23.

## 1. Due by v1.0 (or earlier)

| C-ID | Requirement (short) | Built | Evidence | What still blocks sign-off |
|---|---|---|---|---|
| C01 | DPDP notice and itemised consent | Built | Consent per purpose with text and notice version, withdrawal, provider and patient notices kept apart: `records` consent tests, `TestTC01*`, D-66; versions from the pack (`legal.consent_text_versions`, `notice_versions`) | Notice and consent texts final, and offered in an Eighth Schedule language (R-03) [VALIDATE] (R4); app screens (frontend) |
| C02 | DPDP data-principal rights | Built | Access, export, correction, erasure, nomination; deadlines from the pack; step-up by SMS to the phone on file (D-75): `TestTM32PatientStepUpThroughTheAPI`, `TestTC63*`, `TestTC03*`, `TestRightsRequestDeadlines`, `TestExportIsEncryptedAndDeliveredThroughLink`; grievance queue with timers (S-142) | Response timings [VALIDATE]; Grievance Officer named |
| C03 | DPDP security safeguards; breach notice | Built (backend) | Per-patient field encryption, deny-by-default, audit chain, erasure surviving restore: [threat-model-verification.md](security/threat-model-verification.md) (TM-01..TM-51, no open High in backend), `TestTC04*`, `TestTC06*`, `TestTC65TamperEvidence`, `TestS116GeneratedAuthorisationSuite`; DAST [S-179 report](review/dast-zap-2026-11-23.md); CI scans (gosec, govulncheck, Trivy, Gitleaks) | Infra controls (S-093), VAPT (S-183), breach drill (S-184), DPB notice texts |
| C04 | Children's data (age rule in v1.0) | Built | Adults only: profile refused under the pack's minimum age (`identity.profile_underage`); a doctor's "minor" flag opens a CH-12 case (D-42, TC-48) | Age rule wording [VALIDATE]; CH-11 protocol for v1.1 |
| C05 | Cross-border transfer | Partly built | Vendors outside the pack's residency are refused outside dev (`config` vendor-register tests); pack declares residency (K-GLB-03) | AWS India regions (S-093); every vendor's processing location (V2) |
| C06 | Processors, retention, deletion | Built (code) | Vendor register enforced (no agreement → refused); 18 retention rules in the pack; erasure reaches 7 modules and scheduler-core (D-32); erasure journal replayed after restore (D-74): `TestTC03*`, `TestKDP07_*`, `TestTC04ErasureJournalReplayAfterRestore` | Processor agreements (V2); retention periods [VALIDATE] (R4) |
| C07 | CERT-In directions | Partly built | Logs kept 1 year in config (D-03); alerts routable as `ops.alert` (D-76); runbook and breach plan in docs/security | Log store in India and NTP (S-093, S-095); on-call and paging; drill (S-184) |
| C08 | IT Act / Intermediary Rules | Partly built | Grievance queue with acknowledgement, takedown and order timers from the pack (S-142, admin tests) | Terms, privacy policy and grievance page published, in the languages required (founder, R4) |
| C09 | Telemedicine Practice Guidelines | Built, blocked in India | Rules engine on server, fail closed, re-run at signing (`TestTC59_*`, T01–T31), consent capture (TELE-CONSENT), identity confirmation (`TestTC34*`), patient in the pack country (P10), signing needs step-up and e-signature (`TestTC56_*`) | Doctor review of rules v1.1.0-draft and the drug table (R1); e-signature method N5 (R2); in-person identity step (D-39) |
| C10 | Doctor registration | Built | Verification pipeline with four-eyes review, re-verification, gate on list/book/sign (K-CS-01): `TestS129_*`, `TestS160_*`, `TestTC44_*`, `TestTC42_*` | Register check adapter (S-111, after the beta per S-030) |
| C11 | Professional conduct (no commissions, no paid ranking) | Built (ranking) | Search ranking uses distance, availability and verification only (S-162); profile text refuses marketing claims (CD-06) | Commercial terms with clinics (founder, lawyer) |
| C12 | Drugs & Cosmetics (prescription drugs) | Built (rules) | Prescriptions only from Verified doctors through the rules engine; empty drug table blocks every item (fail closed) | Doctor-approved drug table (R1); no pharmacy partner in v1.0 |
| C13 | NDPS | Built (rules) | Narcotic and psychotropic classes blocked by the rules engine: `TestTC59_BlockedDrugIsRefusedAtSigning` | Drug classification approved by the doctor reviewer (R1) |
| C14 | Drugs and Magic Remedies (claims) | Built (profiles) | Directory refuses marketing claims in profile text (CD-06 tests) | Marketing content review (marketing owner) |
| C17 | RBI payment aggregators | Built (fake) | Hosted checkout through the PA adapter; clinic-collected money (mode B); signed webhooks, polls, reconciliation: `TestTC58_*`, `TestTC28_*`, `TestS146_Reconciliation` | PA agreement and real adapter (V2) |
| C18 | Card data storage | Built | No card, UPI or bank field anywhere; checkout hosted by the PA (K-PAY-01); payment credentials only as secret references (CHECK constraint) | PA integration review (V2) |
| C19 | Consumer Protection / E-Commerce Rules | Built | Refund outcome shown before cancelling (cancel preview), versioned policy stored with each booking, RF08/RF09 blocked (K-PAY-05): `TestS133_*`, `TestRefund_OnePerAppointmentAndRule`; grievance officer queue | Published policies and price display (founder); fairness decision (F2) |
| C20 | GST invoicing | Partly built | Patient receipt or bill of supply in the clinic's name, gapless numbering (S-134, D-56) | CA approval of the tax file (R3); our subscription invoices (not built) |
| C21 | TRAI DLT | Built (registry) | SMS only through registered template IDs with typed variables; links and markup refused (D-77): `TestValidateRefusesLinksAndMarkup`, `TestSMSRegistryRefusesUnregisteredAndBadVariables` | DLT registration and template IDs (S-102, currently [FILL]) |
| C23 | Aadhaar | Built | Verhoeff-valid 12-digit and 16-digit VID refused in every free-text field and upload title; reported documents quarantined and purged; CI scan for Aadhaar fields: `TestTC20_*`, `TestTC21_*`, `TestAAD05QuarantineAndPurge`, `TestTC14UploadRejections` | Lawyer check of the Aadhaar policy (R4) |
| C24 | Clinical Establishments Act | Built | Clinic verification with registration details (D-38); branches (D-63) | Branch-level registration [VALIDATE] (R4) |
| C28 | Accessibility (RPwD) | Not built | Apps not built yet (D-12) | S-176 on the app screens |
| C29 | App-store health declarations | Not code | — | S-187 (store listings) |
| C30 | Medical record retention | Built | Clinical records under RET-02 kept with care keys and restricted (not deleted) at erasure: `TestTC03*` | Retention period [VALIDATE] (R4) |
| C34 | TDS on payouts | Not applicable in v1.0 | Mode B: we make no payouts (K-PAY-03) | Returns with split settlement (v1.2) |

## 2. Not due by v1.0

- **Later versions:**
  - C15 (ABDM, v2.0);
  - C16 (medical device software, v1.1);
  - C25 (NABL, v2.0);
  - C26 (nurses, v4.0);
  - C27 (bio-medical waste, v2.0+);
  - C31 (ISO 27001 / SOC 2, decided at the v2.0 gate);
  - C32 (other markets, v1.1+);
  - C33 (SOS and 112, v1.1).
- **On hold:** C22 (WhatsApp).

## 3. Before the sign-off meeting

1. The lawyer goes through every [VALIDATE] item (blockers R4), the C08 publications and C01 language versions.
2. The doctor reviewer signs the rules and drug table (R1), which unblocks C09, C12 and C13.
3. The CA approves the tax file (R3) for C20.
4. The founder names the Grievance Officer and approves the decisions in the build decisions log.
5. Infra (S-093) closes C05 and C07, then staging runs DAST, load and the drills again.
