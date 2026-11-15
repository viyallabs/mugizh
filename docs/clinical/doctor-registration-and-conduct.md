# Doctor registration checks and professional-conduct limits (S-018)

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C10, C11 (plus proposed row N2), C23 · **Contract:** K-CS-01 (plus a proposed new rule, section 5) · **Edge cases:** E-VER-01 to E-VER-04
**Feeds:** F17 verification (S-109 to S-111, S-118, S-129, S-160, S-188), doctor profiles and search (S-112, S-119, S-120, S-162, S-209, S-214), and the marketing compliance guide (S-048)
**Date:** 2026-11-15 · **Not legal advice.** **[VALIDATE]** = legal point for the lawyer (S-009). **[CONFIRM]** = founder decision.

**Who this is for:** the founder and lawyer, to accept the rules; engineering, to build verification and search; marketing, to write the guide in S-048.

## 1. Sources and how sure we are

| Source | Status | How I checked it |
|---|---|---|
| National Medical Commission Act 2019 (s.33–34: only people on a State or National Register may practise) | In force | General knowledge [VALIDATE] |
| IMC (Professional Conduct, Etiquette and Ethics) Regulations 2002, as amended | In force. The 2023 RMP conduct regulations were put on hold in Aug 2023 (S-008). | General knowledge. Clause numbers below are from memory [VALIDATE]. |
| **NMC Guidelines on Ethical Advertising and Public Communication, 6 Oct 2026** (Ethics and Medical Registration Board), in force at once | In force | Two news reports that quote clauses ([Medical Dialogues](https://medicaldialogues.in/health-news/nmc/nmc-issues-ethical-advertising-public-communication-guidelines-for-doctors-hospitals-details-180731), [South First](https://thesouthfirst.com/health/nmc-bars-patient-referral-commissions-restricts-discounts-and-paid-rankings/)). I downloaded the 13-page notice PDF, but it is a scan I could not read here, so the lawyer must read it [VALIDATE]. |
| Telemedicine Practice Guidelines 2020 | In force | See [telemedicine-rules-spec.md](telemedicine-rules-spec.md) |
| Verification sheet in the roadmap | Internal | Sources: State Medical Council registers, NMC's Indian Medical Register (IMR) and National Medical Register (NMR), ABDM HPR (v2.0) |

**What the 6 Oct 2026 guidelines say, as reported:**
- **Who they cover:** they apply to doctors, hospitals, **healthcare platforms that host doctors**, and agencies and influencers. Publishing through a platform does not clear the doctor of responsibility.
- **Platforms:** platforms must follow the advertising norms, must not rank doctors for payment, and must "maintain transparency in listing practices".
- **Referral payments (clause vii):** no "commission, rebate, bonus, gift, gratuity, referral fee, lead-generation fee" linked to referring or procuring patients.
- **Agency and platform deals (clause viii):** no payment tied to "the procurement or referral of individual patients".
- **Discounts (clause x):** no discounts, offers, coupons or free procedures that are likely to encourage unnecessary care. Charges may be stated if they are "factual, transparent and not misleading".
- **Endorsements (clauses ii and xi):** no testimonials or endorsements by patients, celebrities, influencers or staff, and doctors may not let their name be used to promote. **Patient consent does not make a testimonial allowed.**
- **Manipulation (clause xii):** no fake or bought reviews, ratings, likes or followers, and no manipulating "search rankings, visibility or algorithms".
- **Photos:** a doctor's photo may not be used as self-advertisement.
- **Directories:** a directory showing name, recognised qualifications, specialty, registration details and availability is "materially distinct" from an advertisement.
- **Claims:** "best", "No.1" or "top" must be objectively verifiable, and awards must be independently verifiable.
- **Enforcement:** State Medical Councils penalise doctors in grades, from a warning up to removal from the register for 1–3 years. No separate penalty for platforms is reported, but the doctors on our platform carry the risk.

## 2. Registration checks (rules for F17)

| ID | Rule | Build in |
|---|---|---|
| RG-01 | **Who can be listed, booked or prescribe:** only a doctor whose registration with a State Medical Council, or on the NMC's Indian or National Medical Register, is **active** and verified by us (K-CS-01). This is checked on the server for every list, book, prescribe and assign action. | S-109, S-118, S-120 |
| RG-02 | **Captured at sign-up:** full name as registered, registration number, **council (mandatory)**, year of registration, recognised qualifications, and specialty. Also identity proof under AAD-04 (no Aadhaar card; see [aadhaar-policy.md](../privacy/aadhaar-policy.md)). | S-110 |
| RG-03 | **Matching:** registration number, council, name and year must all match the register. Any mismatch, more than one hit across councils (E-VER-02), or a registry or vendor outage (E-VER-04) goes to **manual review**. Nothing is ever auto-approved (E-VER-01). | S-111, S-129 |
| RG-04 | **Specialty claims:** a profile can show a specialty only if a recognised postgraduate qualification for it appears in the register or in verified certificates. Otherwise the profile shows "General practice (MBBS)". | S-110, S-129, S-112 |
| RG-05 | **Who we do not list in v1.0:** doctors registered only outside India; AYUSH practitioners (a separate legal regime); dentists (Dental Council) [CONFIRM: dentists in v1.0?]; and interns or provisionally registered doctors. | S-110 |
| RG-06 | **Telemedicine across states:** a doctor registered in any State Council or the national register may consult patients across India [VALIDATE]. We show the registering council on the profile and on the prescription. | S-120, S-150 |
| RG-07 | **Re-verification:** on licence expiry, every 12 months, and after a complaint. If re-verification fails, or the register shows suspension, listing and prescribing are suspended **immediately** (E-VER-03). Patients with upcoming bookings are told and refunded. | S-160 |
| RG-08 | **Evidence:** keep the register lookup result (source, time, status, reviewer, reason) encrypted. Delete ID images 90 days after the decision (RET-08), or at once if an Aadhaar card is uploaded (AAD-04). The four-eyes rule applies to rejections and overrides. | S-109, S-129 |
| RG-09 | **v0 concierge doctors (S-014):** before a doctor sees any pilot patient, the founder manually checks the State Council or IMR/NMR listing, and records the doctor's name, number, council, the date checked, a screenshot and who checked. | S-014 |
| RG-10 | **Registration number shown** on the profile, at the start of every consult and on every prescription, receipt and certificate (IMC Regulations, registration-display amendment) [VALIDATE]. | S-120, S-150 |

## 3. Professional-conduct limits (rules for product, business model and marketing)

### 3.1 Money

| ID | Rule | Effect |
|---|---|---|
| CD-01 | **No payment to a doctor linked to a referral or a prescription.** This includes revenue from labs, pharmacies or any partner. Doctors gain nothing when a patient books a test or buys medicine through us. | The register's own C11 rule; v2.0 labs and v3.0 pharmacy |
| CD-02 | **No fee charged to a doctor per patient or per booking acquired** (it may count as a "lead-generation fee", clause vii/viii) until the lawyer confirms in writing [VALIDATE]. Safer models: a flat subscription for practice software, and/or a fee the patient sees and pays. | Fee model (S-019, open decision 7) |
| CD-03 | **No discounts, coupons, cashback, "first consult free" or referral rewards** that could encourage unnecessary consultations or tests. Factual fee display is allowed and required. | Marketing (S-048); growth plans |
| CD-04 | No gifts, sponsorship or hospitality from pharmaceutical companies passed to doctors through the platform (IMC Regulations 6.8 [VALIDATE]). No pharma-sponsored placement. | v3.0 partner deals |

### 3.2 Search, listings and reviews

| ID | Rule | Effect |
|---|---|---|
| CD-05 | **Search ranking is never paid**, and there are no "sponsored" or "featured" doctor slots. Ranking uses only distance, availability, specialty match, verification, doctor reliability (attendance record, if the founder approves Provider Agreement A11), and from v1.1 a Bayesian rating. The ranking factors are published in plain words ("transparency in listing practices"). | S-162, S-214. **Proposed answer to open decision 7: drop paid placement.** |
| CD-06 | **Profile content (a directory, not an advert):** name, recognised qualifications, specialty (RG-04), registration number and council, languages, clinic address, timings, consult modes and fees. **Not allowed:** "best", "top" or "No.1"; awards in v1.0; success rates; before-and-after images; claims of cure. | S-112, S-120 |
| CD-07 | **Doctor photo:** optional, a plain head-and-shoulders photo for patients to recognise the doctor. No promotional images, slogans or logos [VALIDATE: the guidelines restrict photos as self-advertisement]. | S-112 |
| CD-08 | **Reviews (v1.1):** only patients with a completed visit can review (E-REV-01). We never pay for, solicit selectively, edit or hide genuine negative reviews, and never let doctors buy rating changes. Reviews are not used in adverts. Moderation removes health details and abuse, with the reason logged (S-213). [VALIDATE: the guidelines do not address genuine platform reviews.] | S-209, S-213, S-214 |

### 3.3 Marketing and communication

| ID | Rule | Effect |
|---|---|---|
| CD-09 | **No patient testimonials promoting a doctor**, even with written consent. Testimonials about the app are for the lawyer to judge, so avoid them in v1.0 [VALIDATE]. | **MK-10 changes** ("testimonials only with written consent" is no longer enough); S-048, S-182 |
| CD-10 | **No doctor endorsing our app**, a product or a drug with their name, photo or voice, paid or unpaid (clauses ii and xi). | MK-05, MK-09, S-182, S-193 |
| CD-11 | **Health education by doctors is allowed:** factual, not promotional, no booking call-to-action for that doctor, and any commercial link disclosed. | MK-05 health-literacy videos |
| CD-12 | No bought followers, likes or reviews, and no paid manipulation of app-store or search rankings (clause xii). | All campaigns |
| CD-13 | No promotion of prescription drugs to the public (Drugs and Magic Remedies Act; C14). This links to S-048. | S-048 |

## 4. What this changes elsewhere

| Item | Change needed | Owner |
|---|---|---|
| [feature-roadmap.md](../../Resources/feature-roadmap.md) F15 line "Any paid placement is clearly labelled", and the same line in `docs/product-vision.md` | Change to "Search ranking is never for sale; no paid placement" **once you decide open decision 7**. Not edited yet. | Founder |
| Marketing sheet MK-10 | Replace "testimonials only with written consent" with "no patient testimonials (NMC guidelines, Oct 2026)" | Founder, then S-048 |
| MK-05 | "Featured doctors must consent" becomes "health education only; no promotion of the doctor (CD-10, CD-11)" | S-048 |
| S-019, S-040 fee model | Avoid per-booking fees to doctors until the lawyer confirms (CD-02) | Founder, CA, lawyer |
| S-112, S-120 profile | Fields per CD-06 and CD-07, specialty per RG-04 | Engineering |
| S-162, S-214 ranking | Factors published; no paid input (CD-05) | Engineering |
| Compliance register C11 | Add the Oct 2026 guidelines as the in-force source (proposed row N2 or a C11 update; open decision 17) | Founder |

## 5. Proposed contract rules (for S-072; no contract covers C11 today)

- **K-CS-01 (keep):** only verified professionals with active registration may be listed, booked, prescribe or be assigned visits; checked on the server.
- **Proposed K-CS-08:** search ranking and listing visibility MUST NOT take payment, subscription tier or partner revenue as an input. The ranking factors are documented and tested (C11).
- **Proposed K-PAY-xx:** no payout, discount or credit to a doctor may be calculated from referrals, prescriptions or partner purchases (C11).

## 6. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Doctor registration checks documented | **Yes** | Section 2 (RG-01 to RG-10) |
| Professional-conduct limits documented (no commissions, advertising limits) | **Yes** | Section 3 (CD-01 to CD-13) |
| Rules feed F17 verification and the marketing guide | **Yes** | RG rules are mapped to the F17 stories; CD-09 to CD-13 are ready for S-048; tracker notes added |

**Story status:** Done. The acceptance criteria do not require sign-off. The [VALIDATE] items go to the lawyer through S-009, and two founder decisions are open (section 7).

## 7. Decisions needed

1. **[Founder, open decision 7]** Drop paid placement entirely (CD-05, recommended), and change the roadmap and vision line?
2. **[Founder, open decision 8]** Drop patient testimonials and doctor endorsements from marketing (CD-09, CD-10)? The guidelines appear to require this.
3. **[Founder]** Fee model: flat subscription and/or a patient-paid fee instead of a per-booking fee charged to doctors (CD-02)?
4. **[Founder]** Dentists in v1.0 (RG-05)?
5. **[Lawyer]** Read the 6 Oct 2026 notice itself (I could only read news reports). Confirm CD-02, CD-07, CD-08 and RG-06.
