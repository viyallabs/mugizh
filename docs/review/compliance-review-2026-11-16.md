# Compliance review: scenario tests against the law (2026-11-16)

**Status:** **All 11 issues fixed in the docs and tracker on 2026-11-16** (section 1a). Legal points still go to the lawyer (S-009). **Not legal advice:** every legal point still goes to the lawyer (S-009).

**Who this is for:** the founder, to decide the fixes; then Claude, to apply them.

**Method:** 30 end-to-end scenarios run through the current docs, contracts and tracker, checked against:
- DPDP Act 2023 and DPDP Rules 2025;
- SPDI Rules 2011 (still in force until May 2027);
- IT Act and the Intermediary Rules 2021, as amended;
- CERT-In Directions 2022;
- Telemedicine Practice Guidelines 2020 (and the April 2020 amendment);
- IMC Regulations 2002;
- NMC conduct rules;
- Consumer Protection (E-Commerce) Rules 2020;
- CDSCO software guidance;
- TCCCPR 2018 (as amended to 2026);
- Aadhaar rules;
- RBI rules for payment aggregators.

**Only issues that need a fix are listed.** Open decisions already tracked elsewhere are in section 3, and are not repeated as issues.

## 1. Issues to fix (11)

### High: the build would not fit the law as written

**R-01. Minimum legal retention is missing. Erasure deletes too early.**
- **Scenario:** a patient closes their account; a week later the police ask for that account's sign-up details.
- **Law:**
  - Intermediary Rules 2021, rule 3(1)(h): keep information collected at registration for **180 days after the account is cancelled**. Rule 3(1)(g): preserve removed content and its records for 180 days. Both apply now, if we are an intermediary (the Terms assume we are).
  - **DPDP Rules 2025, rule 8(3)** (from 13 May 2027): keep personal data, associated traffic data and processing logs for **at least one year from the date of processing**, for the Seventh Schedule purposes, even after an erasure request.
- **Where the docs conflict:**
  - data inventory RET-01 ("erased within 30 days of account closure");
  - privacy notice §retention ("deleted within 30 days");
  - grievance page ("erasure finished in all systems within 30 days");
  - data-rights-flows §5 (the patient key is destroyed at erasure);
  - encryption design §3–4 and backup-and-restore §2 (crypto-shredding right after erasure).
- **Fix to apply:**
  - Add a **legal-minimum hold**: after erasure, account and processing data is restricted (hidden, access only for a legal order), not deleted. It is hard-deleted when the longest applicable minimum ends: 180 days after closure for registration data, and 1 year from the last processing for data and logs (from May 2027).
  - Destroy the patient key only after that hold.
  - Update RET-01, the privacy notice, the grievance page, the data-rights flow, the encryption design and the backup design.
  - Lawyer to confirm both rules apply to us.

**R-02. Online consultations with patients outside India are outside the telemedicine rules.**
- **Scenario:** a patient in Dubai books an online consult with a Bengaluru doctor (edge case E-BK-03 allows it).
- **Law:** the Telemedicine Practice Guidelines 2020 cover consultations with patients **in India** and exclude consultations outside India's jurisdiction. The patient's country may treat it as unlicensed practice. Cross-border data rules also apply.
- **Where:**
  - Edge case E-BK-03 ("Patient abroad books an Indian doctor"; expected: each sees their local time);
  - user journeys;
  - the booking flow has no location check.
  - (In-person bookings by a patient travelling to India are fine.)
- **Fix to apply:**
  - In v1.0, **online consults only for patients located in India.** The patient confirms their location at booking and again at pre-join, and the doctor can see that declaration. Telemedicine consent (P04) covers it.
  - Change E-BK-03 to in-person bookings only, or a patient travelling within India.
  - Add a block message.
  - Lawyer to confirm.

**R-03. Notices and consent must be offered in the user's language. The plan is English only.**
- **Scenario:** a Tamil-speaking patient signs up and wants the privacy notice in Tamil.
- **Law:**
  - DPDP Act s.5(3) and s.6(3): the notice and the consent request must give the option to read them in **English or any language in the Eighth Schedule** to the Constitution (from 13 May 2027).
  - Intermediary Rules rule 3(1)(a), amended 2022: publish the rules, privacy policy and user agreement in English or an Eighth Schedule language "in the language of his choice" (in force now).
- **Where:** founder decision of 14 Nov 2026 (English only at launch); country pack `launch_locales: [en-IN]`; consent texts rule 6.
- **Fix to apply:**
  - The UI can stay English. **But the privacy notices, consent texts, Terms and grievance page must be available in the scheduled languages on request**, at least as readable documents.
  - Plan translation, with checks for meaning, before the public launch, and before 13 May 2027 at the latest.
  - Lawyer to confirm how many languages and in what form.

**R-04. No documented information-security programme for the SPDI period (now until May 2027).**
- **Scenario:** a breach during the v0 pilot (late Nov 2026, which handles health data). A complainant claims compensation under IT Act s.43A.
- **Law:** SPDI Rules 2011, rule 8: we are treated as having reasonable security only if we have a **comprehensive documented information-security programme and policies**, with managerial, technical, operational and physical controls. Being certified to IS/ISO/IEC 27001, or audited against an approved code, gives deemed compliance.
- **Where:** we have many design documents, but **no information-security policy document**. The certification plan defers ISO 27001 to the v2.0 gate.
- **Fix to apply:**
  - Write a short **information-security policy and programme** (scope, roles, access control, encryption, logging, incident response, vendors, training, review), indexed to the existing designs. Approve it before v0.
  - Note in the certification plan that ISO 27001 is the deemed-compliance route if wanted earlier.

### Medium: gaps that would cause a non-compliant build or a missed duty

**R-05. Crash reporting and other app telemetry vendors are not covered by the residency and vendor rules.**
- **Scenario:** a developer adds a popular crash-reporting SDK that sends device IDs and IP addresses to servers outside India.
- **Law:** K-DP-01 (India processing), K-DP-08 (vendor register), and SPDI rule 7 (transfers). Device IDs and IP addresses are personal data (S-044).
- **Where:** mobile rule M5 allows "crash reporting (scrubbed)"; the vendor register in data inventory §5 has no crash-reporting row; residency check R4 doesn't name it.
- **Fix to apply:** add crash reporting (and any app performance monitoring) to the vendor register. It must be **self-hosted or India-processing**, and on the mobile SDK allow-list.

**R-06. No procedure for government, police or court requests for data.**
- **Scenario:** a police officer emails asking for a patient's consultation records.
- **Law:**
  - Intermediary Rules rule 3(1)(j): provide information to an authorised agency on a lawful order within the stated time (72 hours).
  - DPDP s.36 (government information requests).
  - CERT-In directions (information on request).
  - Court summons.
  - The doctor's duty of confidentiality (IMC regulations).
- **Where:** the grievance page has only "Nodal contact for law-enforcement requests: [FILL]".
- **Fix to apply:** a short **lawful-request procedure** in the legal folder:
  - verify the authority and the legal basis;
  - check the order is in writing;
  - lawyer review for health records;
  - disclose the minimum;
  - log it (audit);
  - tell the patient unless the law forbids it;
  - meet the time limits.
  - Add a JIT reason code "legal order" (already listed) linked to it.

**R-07. A suspended doctor's confirmed bookings are not handled.**
- **Scenario:** a doctor's registration is suspended on Monday; three patients have paid for consults on Tuesday.
- **Law:** K-CS-01 already stops the consult at join. But **consumer protection** (deficiency in service; the refund policy RF04 "doctor cancelled") needs patients to be told and refunded, not left waiting.
- **Where:** the verification and re-verification flows (architecture §5.4, TM-31, S-160) suspend listing and prescribing only.
- **Fix to apply:** on suspension, **cancel future bookings automatically, refund in full (RF04), and notify the patients** (offer another doctor). The clinic is told. Add an edge case and a test.

**R-08. Copies of medical records: the 72-hour duty is not reflected.**
- **Scenario:** after closing the account, a patient asks the Grievance Officer for a copy of last year's prescription.
- **Law:** IMC Regulations 2002, reg. 1.3.2: medical records to be given **within 72 hours** of a request by the patient or authorised attendant.
- **Where:** data-rights flows and the grievance page use 15 days for every rights request. Restricted records are "reachable by the patient on request through the Grievance Officer".
- **Fix to apply:** requests for **copies of medical records** are answered **within 72 hours**. Other rights requests stay at 15 days. In the app the patient can already download them immediately.

**R-09. The prescription verification reference must be unguessable.**
- **Scenario:** someone cycles through verification references on the public page and learns which doctors have issued prescriptions, and when.
- **Law:** DPDP s.8(5) (reasonable safeguards). A prescription's existence reveals a patient–doctor link (SH-L).
- **Where:** threat model TM-20, IA SC-51 and rules spec §8 describe the reference but not its strength.
- **Fix to apply:**
  - The reference is a **random token of at least 128 bits** (or a signed token), never sequential.
  - The page is rate-limited.
  - It shows nothing if the reference is unknown.
  - Optionally it also needs the patient's year of birth [CONFIRM].

**R-10. The requirements spec still contradicts later legal decisions. Claude builds from it.**
- **Scenario:** Claude builds reminders and booking messages from `requirements-v1.0.md`.
- **Law:**
  - DPDP s.6(1): consent limited to what is necessary. Reminders are now opt-in (P-REMIND).
  - WhatsApp is on hold (K-MSG-02).
- **Where:** requirements-v1.0.md:
  - F1-d and **R-70** "reminders on by default";
  - **R-31** "confirmation by app and WhatsApp";
  - **R-72** WhatsApp reminders (Must);
  - **R-03** WhatsApp opt-in.
  - Also personas (Lakshmi's "reminders on by default; WhatsApp channel").
- **Fix to apply:** update these rows to match the current decisions (P-REMIND opt-in, then on per item; push and SMS only). Mark R-72 "on hold". CLAUDE.md §4 overrides today, but the spec should not contradict it.

**R-11. The electronic-signature decision (N5) is now a hard blocker for issuing prescriptions, but it isn't tracked as one.**
- **Scenario:** the beta starts; doctors can't sign any prescription.
- **Law and design:** IT Act (electronic signatures, Second Schedule). P09 fails closed until the lawyer decides N5.
- **Where:** scope-v1.0.md §5 "what would move v1.0's date" doesn't list N5; the lawyer question list has it but not as a launch blocker.
- **Fix to apply:**
  - Add N5 to the scope watch list and to the lawyer's priority questions, with a date needed before the beta.
  - Ask the signature-provider question in the vendor spike (an S-041 or new spike).

## 2. Scenarios that passed (no change needed)

| # | Scenario | Checked against | Result |
|---|---|---|---|
| 1 | Sign-up with the age gate; minor stopped | DPDP s.9, CH-01 | Pass |
| 2 | Patient types an Aadhaar number in a note; doctor uploads an Aadhaar card | Aadhaar Act, AAD rules, K-DP-05 | Pass |
| 3 | First audio consult, List A drug | TPG table, R05, E-RX-06 | Pass (blocked) |
| 4 | Schedule X / NDPS drug in a teleconsult; clonazepam | TPG prohibited list; April 2020 amendment; PL03 | Pass |
| 5 | Follow-up after 6 calendar months; new condition in a follow-up | TPG follow-up definition | Pass |
| 6 | Doctor-initiated consult without explicit consent | TPG consent | Pass (P04 blocks) |
| 7 | Patient revokes sharing mid-view | DPDP s.6(4), K-DP-02 | Pass |
| 8 | Lock-screen reminder with no opt-in for names | K-DP-09 | Pass |
| 9 | Sign-in OTP SMS | TCCCPR Third Amendment (Service message) | Pass (fixed 15 Nov) |
| 10 | Marketing SMS without consent | TCCCPR | Pass (blocked, E-MSG-02) |
| 11 | Doctor doesn't join; refund from the clinic's account | Refund policy, money flow | Pass |
| 12 | Late patient cancellation charge | E-Commerce Rules r.4(6) | **Already tracked** (FAIRNESS-01) |
| 13 | Patient money held by the platform | RBI rules for payment aggregators, K-PAY-03 | Pass (mode B) |
| 14 | Receipt from an unregistered clinic | CGST rules | Pass (fixed 15 Nov) |
| 15 | Breach at 2 am | CERT-In 6 h; DPDP rule 7 | Pass |
| 16 | Vendor breach | DPDP s.8(6) (we notify as fiduciary) | Pass |
| 17 | Staff browsing a record | K-SEC-08, DPDP s.8(5) | Pass |
| 18 | Forged webhook | K-PAY-02 | Pass |
| 19 | Data export and correction | DPDP s.11, s.12 | Pass (summary vs export labelled correctly) |
| 20 | Nominee after death | DPDP s.14 | Pass (lawyer to confirm proof standards) |
| 21 | Prohibited claims ("manage your diabetes") | CDSCO software guidance, Drugs and Magic Remedies Act | Pass (MD-01, MD-05) |
| 22 | Symptom search red flags | CDSCO software guidance (triage) | Pass (option A recommended) |
| 23 | Doctor profile with testimonials or "best doctor" claims | NMC conduct rules (Oct 2026) | Pass (Provider Agreement) |
| 24 | Grievance timers (7 days, 36 h, 3 h, 2 h) | Intermediary Rules amendment 2026 | Pass |
| 25 | User reminder of platform rules every 3 months | Intermediary Rules amendment 2026 | Pass |
| 26 | Recycled phone number | DPDP s.8(5) | Pass (E-DP-22) |
| 27 | Ranking without payment; factors published | E-Commerce Rules; NMC | Pass (reliability still awaiting the founder) |
| 28 | Logs kept 1 year in India; NTP | CERT-In; DPDP rule 6(1)(e) | Pass (contract text update awaits the ADR) |
| 29 | Child profile in v1.1 | DPDP s.9, rule 10, POCSO | Pass (gated by CH-11) |
| 30 | Prescription PDF edited after signing | IT Act; K-CS-05 | Pass (P09; verification page, see R-09) |

## 3. Already tracked elsewhere (not new issues)

- K-SEC-01 change to 1 year, and the other proposed contract ADRs.
- FAIRNESS-01 (cancellation charge).
- Push via Google and Apple (lawyer exception).
- Fiduciary role for clinical records.
- Intermediary status per feature.
- TDS (s.194-O successor).
- GST registration.
- Electronic-signature **method** (N5).
- Amendment drugs (Q11).
- Guardian verification option.
- CH-11 protocol.
- Accessibility duty for private providers (C28).
- Erasure undo window.
- v0 tools hosted in India (open decision 5).
- Grievance Officer and CERT-In point of contact to be named before v0.
- Missing analytics story.
- Recovery check for a lost phone number (E-DP-23).
- Profile-switch PIN (E-DP-09).
- S-052 review-collection permission.

## 4. Sources checked in this review

- **DPDP Rules rule 8(3), one-year retention:**
  - [Mondaq: retention and deletion under DPDP](https://www.mondaq.com/india/data-protection/1844062/data-retention-and-deletion-under-the-dpdp-act-how-long-can-businesses-retain-personal-data-in-india)
  - [dpdprules.org, rule 8](https://dpdprules.org/rules/8)
  - [Consiva: erasure vs retention](https://consiva.ai/blog/dpdp-erasure-vs-retention)
  - [NALSAR Tech Law Forum](https://techlawforum.nalsar.ac.in/privacy-with-a-footnote-data-retention-under-the-dpdp-framework/)
- **Intermediary Rules rule 3(1)(h) and 3(1)(a):**
  - [Khaitan & Co: 2022 amendment](https://khaitanco.com/thought-leaderships/Internet-Intermediaries-Rules-Amended-Significant-Changes-Made)
  - [Nishith Desai: amended intermediary guidelines](https://www.nishithdesai.com/fileadmin/user_upload/Html/Hotline/Technology_Law_Hotline_M_Nov0322.htm)
  - [JSA Prism, April 2026](https://www.jsalaw.com/wp-content/uploads/2026/04/JSA-Prism-Infotech-April-2026-IT-Rules.pdf)
- **Telemedicine guidelines excluding consultations outside India:**
  - [PMC article](https://pmc.ncbi.nlm.nih.gov/articles/PMC8106416)
  - [Khaitan & Co](https://khaitanco.com/thought-leaderships/Telemedicine-Practice-Guidelines)
  - [PSA Legal FAQ](https://psalegal.com/telemedicine-guidelines-2020-faq/)
- DPDP Act s.5(3) and s.6(3) (language option), SPDI Rules rule 8, IMC Regulations reg. 1.3.2, and Intermediary Rules rule 3(1)(j): general knowledge of the statute text. **[VALIDATE wording with the lawyer]**

## 1a. Fixes applied (2026-11-16)

| Issue | Fixed in |
|---|---|
| R-01 legal-minimum hold | data inventory RET-01, **new RET-18**, DEL-ERASE (two steps); privacy notice retention; grievance page; data-rights-and-grievance timelines; data-rights-flows §5; encryption design §3; backup-and-restore §2; country pack RET-01/RET-18; CLAUDE.md K-DP-07; K-DP-07 implementation note |
| R-02 patients outside India | telemedicine rules YAML (`patient_location`, check **P10**, reason RX_PATIENT_OUTSIDE_INDIA); rules spec (P10, §6, test T31); consent texts §C ("I am in India now", TELE-CONSENT@2); Terms §6; journeys stage 9a; privacy UX SC-09; country pack; Edge Cases E-BK-03; test TC-24; CLAUDE.md §4; "P01 to P10" in all docs |
| R-03 language choice | consent texts rule 6; privacy notice (language line); Terms §12a; scope-v1.0; CLAUDE.md §4; country pack `legal_text_languages`; country-pack model |
| R-04 information-security policy | **new** [information-security-policy.md](../security/information-security-policy.md); certification plan note |
| R-05 crash reporting | mobile rule M5; vendor register (crash reporting and e-signature rows); residency check R4 |
| R-06 lawful requests | **new** [lawful-requests-procedure.md](../legal/lawful-requests-procedure.md); grievance page; legal README |
| R-07 suspended doctor | architecture §5.4; booking subscribes to `verification.status_changed`; refund rule RF04; **new edge case E-VER-06** (row 94); **test TC-66** |
| R-08 72-hour record copies | data-rights-flows §2; data-rights-and-grievance; grievance page; country pack |
| R-09 verification reference | IA SC-51; rules spec §8; threat model TM-20 |
| R-10 requirements contradictions | requirements F1-d, F11-a, R-03, R-31, R-70, R-72; personas |
| R-11 N5 blocker | scope-v1.0 §5 (items 5 and 6); legal README items 7 and 8 |

Tracker: E-BK-03 changed; E-VER-06 added; notes on S-009, S-023, S-030, S-051, S-060, S-062, S-095, S-098, S-100, S-122, S-136, S-138, S-142, S-143, S-150, S-160; Compliance rows C01, C08, C09.
