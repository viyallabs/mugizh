# Children's data policy (S-017) — DRAFT for lawyer approval

**Phase P01 · Epic EP02 · Depends on:** S-010 · **Compliance:** C04 (plus C01, C09, C23) · **Contract:** K-DP-10 · **Edge cases:** E-DP-11, E-DP-09 · **Feeds:** F13 design (S-200, S-211, S-212, S-207)
**Date:** 2026-11-15 · **Not legal advice.** **[VALIDATE]** = legal point for the lawyer (S-009). **[CONFIRM]** = proposal for the founder.

**Who this is for:** the lawyer to approve (acceptance criterion), the founder to decide the [CONFIRM] items, and design and engineering to build F13 (family profiles, v1.1) against it.

## 1. Summary

- **v1.0: adults only** (founder decision 3, 14 Nov 2026). There are no child profiles and no booking for anyone else. The age gate is AGE-GATE@1 in [consent-texts.md](consent-texts.md), and we keep nothing from an under-18 attempt except a daily count (RET-16).
- **v1.1: guardian-managed child profiles (S-200).** A child never has their own login. A verified adult guardian creates and manages the profile, and nothing is processed for the child until the guardian's consent is verified (K-DP-10).
- **Never for children:** behavioural tracking, analytics, targeted advertising, marketing, or personalisation based on a child's data.
- **At 18 (S-212):** the guardian's access ends. The new adult can claim the record into their own account.

## 2. Legal basis [VALIDATE all of section 2]

| Source | What it requires | Effect on us |
|---|---|---|
| DPDP Act 2023, s.2(f) and s.9 | A child is anyone under 18. We need **verifiable consent of a parent or lawful guardian** before processing a child's data. No processing likely to harm a child's well-being. **No tracking, behavioural monitoring or targeted advertising** directed at children. | Guardian consent gate (CH-03); no analytics or marketing on child profiles (CH-07) |
| DPDP Rules 2025, rule 10 | Verifiable consent means checking that the person giving it is an identifiable adult. This can use **reliable identity and age details we already hold**, or a **virtual token** from a government-authorised entity (for example DigiLocker). | Section 4 options |
| DPDP Rules 2025, Fourth Schedule | Exemptions from the parental-consent and tracking rules for **clinical establishments, mental-health establishments and healthcare professionals**, to the extent needed to provide health services to the child. Other exemptions cover child safety. | Whether **a platform** (not the clinic or doctor) can rely on this is open. S-008 flagged the same question on C04. We plan **not** to rely on it, and get verified consent anyway. |
| DPDP commencement | Children's duties start with the main duties, around **13 May 2027**. | Already in force when v1.1 ships, if v1.1 is after May 2027 |
| DPDP penalties | Up to ₹200 crore for breaching the children's duties | Treat as a high-risk area |
| Telemedicine Practice Guidelines 2020 | A minor can be consulted **only with an adult whose identity is confirmed** present. | Teleconsult rule CH-09; S-012 identification section |
| Majority Act 1875; guardianship laws | Majority at 18. Parents are natural guardians. Courts can appoint guardians. | Guardian definition CH-02 |

## 3. Policy rules (CH-xx; to copy into K-DP-10 in S-071)

| ID | Rule | Version | Build in |
|---|---|---|---|
| CH-01 | Only adults (18+) can hold an account. Anyone giving a date of birth under 18 is stopped at the age gate. Nothing is stored except a daily count. | v1.0 | S-104, done in AGE-GATE@1 |
| CH-02 | **Guardian** means a parent or court-appointed lawful guardian aged 18+, who declares the relationship. Step-parents and other relatives need a court order or the parent's in-app delegation [CONFIRM]. | v1.1 | S-200 |
| CH-03 | A child profile stays **locked** (no booking, no records, no reminders) until verifiable guardian consent is recorded (K-DP-10). | v1.1 | S-200 |
| CH-04 | The child profile holds only name, date of birth, sex, relationship to the guardian, and clinical data from care. **No photo ID, no Aadhaar number** (AAD rules in [aadhaar-policy.md](aadhaar-policy.md)), no school details. | v1.1 | S-200 |
| CH-05 | Guardian consent is per purpose, like adults' consent, **with one checkbox per purpose**: P-CARE and P-RECORD (needed), P-REMIND (optional), and P-SHARE per consult. P-ANALYTICS, P-NEWS and P-MEDNAMES are **not offered** for a child. Every consent record stores `on_behalf_of` (child) and `given_by` (guardian), plus the verification method. | v1.1 | S-057, S-200 |
| CH-06 | A second guardian can be invited. Each guardian can withdraw consent for themselves. If the guardians disagree, the more restrictive choice applies until they resolve it. We act on a court order when one is provided. We do not judge custody disputes. [CONFIRM] | v1.1 | S-200, support runbook |
| CH-07 | **No tracking or targeting:** no analytics events, no A/B tests, no marketing, no recommendations and no third-party SDKs on child profile screens. Logs keep only what security needs. | v1.1 | S-200, S-095 |
| CH-08 | Payer and patient are recorded separately. The guardian pays, and the child is the patient (E-FAM-01, S-211). | v1.1 | S-211 |
| CH-09 | **Teleconsult with a child:** the guardian (or an adult the guardian named) must be present. The doctor confirms the adult's identity and relationship before starting. Prescribing checks P02 and P04 cover the child's name and age and the guardian's consent. | v1.1 | S-138, S-012 update |
| CH-10 | **At 18** (S-212, E-DP-11): the guardian and child are told 30 days before. On the birthday, guardian access ends and the profile is frozen. The new adult can claim it with their own phone, OTP and fresh consent, and then can choose to grant the former guardian caregiver access (S-207). Unclaimed records follow RET-02 retention, then are deleted. | v1.1 | S-212 |
| CH-11 | **Adolescent confidentiality (13–17):** sensitive categories such as sexual health and mental health raise a conflict between guardian access and the teenager's privacy. **Launch blocker: child profiles do not launch until a confidentiality and safeguarding protocol is approved by the doctor reviewer and the lawyer.** Keeping sensitive matters for an in-person talk does not stop them reaching the guardian through the digital record, so it is not a fix. The protocol must define: (1) which record categories a guardian can and cannot see, and at which ages; (2) how sensitive disclosures are recorded and who can access them; (3) emergencies, when a guardian must be told; (4) mandatory reporting duties (for example POCSO); (5) what the teenager is told about who can see their record. **No hide/show toggle is built before that.** [VALIDATE doctor + lawyer; doctor review pack Q10] | v1.1 (gate) | S-200, S-207 |
| CH-12 | If we find an under-18 user holding an adult account (wrong date of birth), we close the account. We delete its data except clinical records that the law makes us keep (RET-02), and tell the user. | v1.0 | Support runbook, privacy notice §2 |
| CH-13 | Adults who lack capacity, with a lawful guardian under DPDP rule 11, are **out of scope until a later version**. In v1.x, an adult's caregiver access always comes from the adult's own consent (S-207) or the nominee process. | v1.x | — |

## 4. How we verify the guardian (rule 10) — choose one [CONFIRM founder · VALIDATE lawyer]

| Option | How it works | Strength | Cost and friction |
|---|---|---|---|
| **A. DigiLocker age token (recommended)** | The guardian proves they are 18+ through a DigiLocker or other government-authorised token. We store only the "adult: yes" result, the token reference and the date, not the document or any Aadhaar number. | Strongest; this is the method rule 10 names | Integration in v1.1 (adapter, S-037); one extra step for parents |
| B. Data we already hold | The guardian is an existing account holder with an OTP-verified phone, a declared date of birth showing 18+, and a declared relationship. Optionally, the payment made in their name. | Weak. A declared date of birth may not count as "reliable" under rule 10. | No extra cost |
| C. B now, A later | Ship v1.1 with B plus a declaration, and add A before the DPDP duties start (May 2027). | Depends on the timing of v1.1 | Low at first |

The relationship (parent or guardian) is a **declaration** in every option, kept with the consent record. Proof such as a birth certificate or court order is asked for only when disputed [VALIDATE that this is enough].

## 5. Guardian consent text (draft, GUARD-CONSENT@1, v1.1)

> **Add a child to your account**
> You are adding [child's name], born [date], as your [son / daughter / ward].
> ☐ I am this child's parent or lawful guardian, and I am 18 or older. *(declaration, not a consent)*
> ☐ **Bookings and consultations (P-CARE, needed):** I allow [Product Name] to use [child's name]'s booking details, and the health information added in consultations, to book and run their care.
> ☐ **Medical record (P-RECORD, needed):** I allow [Product Name] to store [child's name]'s consultation notes, prescriptions and the documents I upload.
> ☐ **Reminders (P-REMIND, optional):** Send me reminders from [child's name]'s prescriptions.
> *(Sharing history with a doctor is asked at each consultation, P-SHARE. Each choice is stored with `on_behalf_of` and `given_by`, CH-05.)*
> You can stop this at any time in **Settings → Family**. We never use a child's information for advertising or tracking. When [child's name] turns 18, your access ends and they can take over their record.
> [Verify that you are an adult] → (option chosen in section 4)

## 6. What this changes in other documents

| Document or story | Change |
|---|---|
| [privacy-notice.md](privacy-notice.md) §2 | Already states adults only and family profiles later. For v1.1, add a "Children" section based on section 3. |
| [consent-texts.md](consent-texts.md) | AGE-GATE@1 unchanged. GUARD-CONSENT@1 (section 5) to be added when v1.1 consent texts are drafted. |
| [data-inventory-v1.0.md](../data-inventory-v1.0.md) | v1.0 unchanged (RET-16). For v1.1, add rows for the child profile and the guardian verification result. |
| [telemedicine-rules-spec.md](../clinical/telemedicine-rules-spec.md) | Add CH-09 to the identification section when minors are enabled (YAML `minors: not_supported_in_v1` stays for v1.0). |
| S-057 consent design | Consent record needs `on_behalf_of`, `given_by` and `verification_method` (CH-05). |
| S-200, S-211, S-212, S-207 | Build to CH-02 to CH-11. |
| S-037 adapters | Add an age-verification adapter (DigiLocker) if option A is chosen. |

**Proposed edge cases (for the Edge Cases sheet):**
- **E-FAM-03:** two guardians disagree, for example one withdraws consent. The more restrictive choice applies, and both guardians are told.
- **E-FAM-04:** a doctor finds the "adult" patient in a consult is actually a minor. The doctor stops the consult. The account is handled under CH-12.

## 7. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Children's data policy written: guardian-managed profiles and verifiable parental consent | **Yes** | Sections 3 to 5 |
| Policy approved by lawyer | **No, pending** | Needs S-009 review of section 2, option A/B/C, and CH-02, CH-06 and CH-11 |
| Feeds F13 design | **Yes** | Rules mapped to S-200, S-207, S-211 and S-212 (section 6) |

**Story status:** In Testing.

## 8. Questions

**Lawyer (S-009)**
1. Can our platform rely on the Fourth Schedule healthcare exemption for consult data, or only the clinic and doctor? (The draft assumes not.)
2. Does option B meet rule 10, or is a token (option A) needed?
3. Is a declared relationship enough, with proof only on dispute?
4. Retention for children's clinical records: does any rule require keeping them until a set age after majority, rather than RET-02 (3 years after the last consult)?

**Founder**
5. Guardian verification: option A, B or C?
6. Do you agree that step-parents and relatives need a court order or a parent's delegation (CH-02)?

**Doctor reviewer**
7. Adolescent confidentiality default (CH-11).
