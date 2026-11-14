# Compliance applicability review (S-008) — DRAFT v0.1 for founder approval

**Phase P01 · Epic EP02 · Depends on:** S-004 (docs/requirements-v1.0.md)
**Date:** 2026-11-14

**Who this is for and what it decides:** for the founder, to confirm for each of the 34 Compliance Register items (C01–C34) whether it applies, who owns it and by which version it is due. It then becomes the lawyer's brief in S-009.

**Not legal advice.** This is a product-scope reading of the register, not a legal opinion. Items marked "Lawyer: Yes" must be confirmed by a qualified lawyer; tax items by a CA.

**How to read the markers**
- **[CONFIRM]** = my inference or proposal; the founder (or the named adviser) must confirm it.
- **[VALIDATE]** = a legal or factual point I recalled or read in secondary sources only; check the primary text.
- "Verified" = checked by web search on 2026-11-14 against secondary sources (law-firm notes, vendor docs, news). No gazette or regulator PDF was opened. Sources are in section 7.

**Inputs read:** Compliance, Contracts, Backlog, Epics, Phases, Releases, Roles, Country Pack, Verification, Marketing, Start Here and Lists sheets; Resources/feature-roadmap.md v0.3; docs/product-vision.md; docs/requirements-v1.0.md; docs/process/*.
**Repo state:** the repo holds only README.md, Resources/ and docs/. There is no application code, no `/contracts` folder and no `CLAUDE.md` yet (those arrive in P08). Nothing in this review is checked against code.

---

## 1. Summary

### 1.1 By decision
| Decision | Count | C-IDs |
|---|---|---|
| Applies (something is needed by v1.0 or earlier) | 24 | C01–C14, C17–C23, C28–C30 |
| Applies – later version (nothing needed for v1.0) | 5 | C15, C26, C27, C32, C33 |
| Conditional (depends on a decision or a lawyer's view) | 3 | C16, C24, C34 |
| Voluntary / optional | 2 | C25, C31 |
| Does not apply | 0 | — |

No item can be struck off. The product's non-goals (no own inventory, no ambulance dispatch, no diagnosis by software) narrow C12, C16 and C33 but do not remove them.

### 1.2 By proposed first due version
| First due | Count | C-IDs |
|---|---|---|
| v0 (concierge test, waitlist page, first marketing content) | 8 | C01, C03, C06, C07, C10, C11, C14, C22 |
| v1.0 | 17 | C02, C04, C05, C08, C09, C12, C13, C17–C21, C23, C24, C28–C30 |
| v1.0 or v1.2 (depends on money flow) | 1 | C34 |
| v1.1 | 3 | C16, C32, C33 |
| v2.0 | 3 | C15, C25, C27 |
| v4.0 | 1 | C26 |
| Not set (founder decision) | 1 | C31 |

**20 due versions confirmed as in the register; 14 proposed changes** (C01, C03, C04, C06, C07, C10, C11, C12, C14, C22, C27, C31, C32, C34). All 14 are [CONFIRM]. Most changes are "earlier", because v0 and the waitlist page already handle personal and health data, and marketing starts in week 2.

### 1.3 Lawyer check
Register today: 20 Yes, 14 No. Proposed: 20 Lawyer, 2 CA (C20, C34), 12 No. Changes: C09 and C28 move No → Yes; C20 and C34 move from Lawyer to CA.

### 1.4 The five things to know first
1. **v1.0 lands on the DPDP deadline.** The Releases sheet puts v1.0 at about 2 May 2027 [CONFIRM: my conversion of the sheet's date number]. The main DPDP duties (notice, consent, security, breach, children) start about 13 May 2027 (verified; sources differ by a day). There is no grace period after launch.
2. **Before May 2027 the older privacy rules still apply and are not in the register.** The IT Act section 43A and the SPDI Rules 2011 stay in force until then (verified) and treat health information as sensitive data. They cover v0 and the waitlist page.
3. **C11 is out of date.** The NMC issued binding advertising guidelines on 6 Oct 2026 (verified). They ban patient testimonials even with consent, ban paid ranking of doctors on platforms, and ban payments linked to referring individual patients. This touches the roadmap's "paid placement is labelled" line, the marketing plan's testimonials, and possibly a per-booking platform fee.
4. **C34 cites a repealed section.** The Income-tax Act 2025 replaced the 1961 Act from 1 Apr 2026; the e-commerce TDS rule (old 194-O) now sits in section 393(1) (verified).
5. **A new company is unlikely to qualify for split payments at v1.0.** Razorpay Route needs over ₹40 lakh domestic turnover (verified). So the "clinic collects" fallback is probably the real v1.0 money flow [CONFIRM], and v1.2 hold-and-release is at risk.

---

## 2. Decision table (one row per C-ID)

Owners are roles, not people, and all are proposals **[CONFIRM]**. Lawyer and CA advise; the founder stays accountable. "Stories" lists the Backlog stories whose Compliance refs cite the C-ID (count, then the key ones); "K" lists Contract rules that cite it.

| C-ID | Regulation (short) | Applies? | Why (for our product) | Due by | Proposed owner | Lawyer check | Stories / contracts | Notes and flags |
|---|---|---|---|---|---|---|---|---|
| C01 | DPDP notice and consent | Applies | Every feature processes personal and health data; consent-gated history is a core promise. | **Changed:** v0 (short notice + consent for waitlist and concierge), v1.0 (full) | Founder | Yes | 51 stories (v0: S-014, S-024, S-025; waitlist S-047; S-010, S-057, S-106–108). K-DP-02, -04, -06, -09, K-SEC-08 | Legal duty starts ~13 May 2027 (verified); before that see proposed row N1. The register's "consent manager screen" should be renamed: under DPDP a "Consent Manager" is a separately registered entity, which we are not [VALIDATE]. Source is a vendor blog. |
| C02 | DPDP data principal rights | Applies | Patients and doctors can ask for access, correction, erasure, grievance and nomination (R-04). | v1.0 (confirmed) | Founder | Yes | 4: S-010, S-060, S-142, S-143. K-DP-07 | Features column says "F2, Shared"; rights cover all personal data, so it should be "All" [CONFIRM]. A named grievance contact is needed. Response time limits under the Rules not stated in the register [VALIDATE]. |
| C03 | DPDP security and breach notice | Applies | We hold clinical notes, prescriptions and documents. | **Changed:** v0 (basic safeguards for concierge data), v1.0 (full) | Platform/Engineering lead | Yes | 23 stories (S-043, S-056, S-061–064, S-178–185). K-DP-03, -04, K-SEC-02 to -08 | Register does not state the breach-notice timing (I recall 72 hours for the detailed report to the Board) or a one-year minimum log retention in the Rules [VALIDATE both]. If one year is right, K-SEC-01's "at least 180 days" is too short as a target. |
| C04 | DPDP children's data | Applies | v1.0 has no family mode, but under-18 patients will still turn up unless we block them. | **Changed:** v1.0 (an age rule), v1.1 (guardian profiles, F13) | Founder | Yes | 3: S-017, S-200, S-212. K-DP-10 | Gap: nothing in v1.0 says what happens when a parent books for a child. See open decision 3. The Rules exempt clinical establishments and healthcare professionals from parental-consent duties for health services (verified, secondary); whether a platform can rely on that is a lawyer question. The same DPDP section covers adults with a lawful guardian (relevant to F13 elderly parents) [VALIDATE]. |
| C05 | DPDP cross-border transfer | Applies | We promise "health data stays in India" (vision §5; K-DP-01). | v1.0 (confirmed) | Platform/Engineering lead | Yes | 5: S-039, S-055, S-093, S-135, S-205. K-DP-01, (K-GLB-03) | Register wording looks wrong: sources describe a "negative list" (transfers allowed unless the Government restricts a country), with localisation duties only for notified Significant Data Fiduciaries [VALIDATE; sources conflict]. So India-only is mainly **our rule**, stricter than the law. It may clash with real vendors (WhatsApp/Meta, push notifications, video, crash reporting) and with v0 tools. See open decision 4. Source is an exam-prep site. |
| C06 | DPDP processors, retention, deletion | Applies | Vendors (verification API, WhatsApp, SMS, video, payments) will receive personal data. | **Changed:** v0 (list the concierge tools and their terms), v1.0 (full) | Founder | Yes | 4: S-011, S-042, S-111, S-205. K-DP-06, -08 | Stated source is "general knowledge". |
| C07 | CERT-In Directions 2022 | Applies | Any live system we run (landing page, concierge tools, app) is in scope. | **Changed:** v0 (know the 6-hour duty; name a contact), v1.0 (180-day logs, NTP) [CONFIRM] | Platform/Engineering lead | No (agree) | 12 stories (S-020, S-059, S-093, S-095, S-183–186). K-DP-01, K-SEC-01, -02 | Still in force with no major change as of mid-2026 (verified, secondary). K-DP-01 cites C07 as a reason to keep all personal data in India; C07 only requires **logs** in India. |
| C08 | IT Act + Intermediary Rules 2021 | Applies | We host doctor profiles and, from v1.1, patient reviews; we publish terms and a privacy policy. | v1.0 (confirmed) | Founder | Yes | 3: S-009, S-023, S-142. **No contract** | No K-rule covers grievance timers or takedown handling. The Rules were amended after 2021, including in Feb 2026 (seen in search; details not checked) [VALIDATE]. Whether we are an "intermediary" for each feature is a lawyer question. |
| C09 | Telemedicine Practice Guidelines 2020 | Applies | F1-b video consults and F1-c prescriptions are the core of v1.0. | v1.0 (confirmed). v0 only if any concierge consult is remote [CONFIRM] | Doctor advisor | **Change No → Yes** | 19 stories (S-012, S-135–139, S-147–150). K-CS-02, -05, K-DP-02 | The register lists doctors' duties only. The Guidelines also put duties on **technology platforms** (check doctors are registered, show name/qualification/registration number, no AI prescribing, grievance route) [VALIDATE]. S-009 already lists C09 for the lawyer while the register says No. An NMC notice of Oct 2026 still cites the 2020 Guidelines (verified). Features should add F17-a and F2-a. Source is a third-party drug-list page. |
| C10 | Doctor registration (NMC Act, state registers) | Applies | "Verified professionals only" is a headline promise (F17-a). | **Changed:** v0 (manual register check of the 10–15 concierge doctors), v1.0 (pipeline) [CONFIRM] | Founder | No (agree) | 12 stories (S-018, S-042, S-109–111, S-118). K-CS-01 | S-014 (recruit v0 doctors) does not cite C10. The "1,800 certificates" figure was not re-checked. |
| C11 | Doctors' professional conduct (advertising, commissions) | Applies | We list doctors, market with doctors, and take a fee around consultations. | **Changed:** v0 (before the first marketing content or doctor recruitment), was v1.0 | Founder | Yes | 4: S-018, S-048, S-182, S-193. **No contract** | **Stale.** See section 3.3 item 1. In force: the 2002 Regulations (the 2023 ones were put on hold in Aug 2023; no sign of revival found) plus the 6 Oct 2026 advertising guidelines [VALIDATE]. Features column lists only F1.1, F1.2 and Marketing; add F5-a, F15, F17 profiles and the F6 fee model. |
| C12 | Drugs & Cosmetics Act/Rules; Pharmacy Act; draft e-pharmacy rules | Applies | No own inventory (non-goal), so the pharmacy duties sit with partners at v3.0. But prescription rules and "no prescription-drug promotion" apply from v1.0 and from the first blog post. | **Changed:** v1.0 (prescription rules, marketing), v3.0 (pharmacy go/no-go) | Founder | Yes | 12 stories (S-012, S-126, S-138, S-206, S-251–257). K-CS-02 | Draft e-pharmacy rules still not notified as of mid-2026 (verified, secondary). Features should add F1-c. Lawyer question for v1.0: is our in-app signed prescription PDF a valid prescription for a pharmacist (electronic signature)? [VALIDATE] |
| C13 | NDPS Act | Applies | The rules engine must block these drugs in v1.0 (non-goal; R-52). | v1.0 (confirmed) | Doctor advisor | No (agree) | 5: S-012, S-072, S-138, S-251, S-254. K-CS-02 | Lawyer check is No but the source says "confirm with lawyer". Blocking the whole category is the safe direction; the doctor advisor should check the drug list [CONFIRM]. |
| C14 | Drugs and Magic Remedies Act | Applies | Health content starts in week 2; our target users have diabetes and heart disease, which I recall are on the Act's list of conditions [VALIDATE]. | **Changed:** v0 (before first content), then every release; "Ongoing" is not a version | Marketing | Yes (one-off review of the marketing guide) | 13 stories, all Marketing plus S-089. **No contract** | Reasonable to leave without a code contract, but in-app copy (S-089) is also covered. |
| C15 | ABDM integration and certification | Applies – later version | F2-b and F17-b are v2.0. Joining ABDM is our choice, not a legal duty [VALIDATE]. | v2.0 (confirmed) | Platform/Engineering lead | No (agree) | 11 stories (S-051, S-083, S-225–249). No contract (K-ARC-05 is related) | Certification has long lead times; start sandbox work before P16 [CONFIRM]. ABDM's own data policy should be read under this row. |
| C16 | CDSCO medical device software guidance | Conditional | "No diagnosis by software" keeps us mostly outside, but F8 (symptom → specialty), F12 (scan → reminders) and F16 need a per-feature view. | v1.1 (confirmed) for F8, F12; v2.0 for F16. Assessment itself is S-022 in P01. | Founder | Yes | 6: S-022, S-072, S-194, S-196, S-202, S-215. K-CS-04 | Guidance issued 21 Jul 2026 and described as clarificatory, not a new rule (verified, secondary). Ask the lawyer to also look at the v1.0 prescription rules engine and at F7-b refill alerts [CONFIRM]. |
| C17 | RBI payment aggregator rules | Applies | F6-a takes money for clinics. We must never hold funds ourselves. | v1.0 (confirmed) | Founder | Yes | 13 stories (S-019, S-040, S-124, S-125, S-217–222). K-PAY-03 | RBI Directions of 15 Sep 2025 and the ₹40 lakh Route threshold are verified. The register's "FY25 or FY26" window will have rolled forward by launch [CONFIRM]. Only one vendor's rule is cited; S-040 compares others. |
| C18 | Card data rules; PCI DSS | Applies | Hosted checkout only; we never see card numbers (R-60). | v1.0 (confirmed) | Platform/Engineering lead | No (agree) | 3: S-019, S-040, S-124. K-PAY-01 | Lawyer check is No but the source says "confirm with lawyer". |
| C19 | Consumer Protection Act; E-Commerce Rules 2020 | Applies | We sell bookings to consumers, show prices and refunds, and (v1.1) reviews. | v1.0 (confirmed); review rules at v1.1 with F15 | Founder | Yes | 15 stories (S-019, S-023, S-112, S-142–145, S-161, S-162). K-PAY-05 | In "clinic collects" mode it is unclear who owes the refund; the lawyer should cover this. Dark-pattern and review standards are not in the register (proposed row N3). |
| C20 | GST | Applies | We charge a platform fee; consultation fees may be exempt. | v1.0 (confirmed) for invoicing; registration status is S-007, now | CA | **CA, not lawyer** | 5: S-007, S-019, S-132, S-134, S-220. **No contract** | An online platform may have to register for GST whatever its turnover, with tax collection at source on taxable supplies (verified in general; not for healthcare platforms) [VALIDATE with CA]. |
| C21 | TRAI commercial SMS (DLT) | Applies | OTP login (R-01) and SMS fallback need registered headers and templates. | v1.0 (confirmed); start registration in Sprint 0 (S-102) | Platform/Engineering lead | No (agree) | 7: S-021, S-041, S-102, S-104, S-141, S-167, S-197. K-MSG-01 | The rules were amended in Feb 2025 and again in 2026 (verified; final 2026 text not read). Register does not mention this. F9 (v1.1) sends SMS to emergency contacts who are not users; template category needs checking [CONFIRM]. |
| C22 | WhatsApp Business policy | Applies | v0 reminders go by WhatsApp; F11-a is in v1.0. | **Changed:** v0, was v1.0 | Platform/Engineering lead (Founder during v0) | No (agree) | 10 stories (S-021, S-025, S-041, S-102, S-156, S-157). K-MSG-02, K-DP-09 | This is a contract with Meta, not a law. S-025 (v0) already cites it. Whether v0 uses the official Business Platform or an ordinary WhatsApp account is undecided (open decision 5). Meta's limits on health content were not checked [VALIDATE]. |
| C23 | Aadhaar Act | Applies (as a ban) | We never plan to use Aadhaar numbers; the rule is "do not store". | v1.0 (confirmed) | Platform/Engineering lead | Yes | 6: S-013, S-071, S-104, S-110, S-229, S-258. K-DP-05 | Gap: R-10 asks doctors for "ID", and patients upload old documents (R-81). Either may contain an Aadhaar card or number, which would sit in our storage against K-DP-05. Needs a rule (other ID only, or masked) [CONFIRM]. |
| C24 | Clinical Establishments Act / state laws | Conditional (by state) | Applies to partner clinics (v1.0) and labs (v2.0), which we check in F17. Depends on the launch state. | v1.0 (confirmed) | Founder | Yes | 3: S-109, S-117, S-223. **No contract** | Launch state is not recorded anywhere (open decision 13). Lawyer should also confirm the platform itself is not a "clinical establishment". |
| C25 | NABL accreditation | Voluntary / optional | Voluntary for labs. It becomes our rule only if we choose "accredited scope only" (roadmap §7 leaves this open). | v2.0 (confirmed) | Founder | No (agree) | 2: S-223, S-224. No contract | Open decision 10. |
| C26 | Nursing registration | Applies – later version | Nurses arrive in v4.0 (F17-c). | v4.0 (confirmed); moves if nurse timing changes | Founder | No now; re-check before v4.0 | 6: S-258–261, S-266, S-267. K-CS-01 | The register cites the 1947 Act. A 2023 Act (National Nursing and Midwifery Commission) is meant to replace it; status not checked [VALIDATE]. Features lists "F4", but F4-a (v2.0 home sample collection) is done by lab staff, not nurses. |
| C27 | Bio-Medical Waste Rules 2016 | Applies – later version | Nurse injections at home (v4.0) create sharps. Home sample collection (v2.0, F4-a) does too, through partner labs. | **Changed:** v2.0 (clause in lab partner agreements), v4.0 (full) [CONFIRM] | Founder | Yes | 2: S-264, S-267. **No contract** | No v2.0 story cites C27. |
| C28 | Accessibility (RPwD Act; WCAG 2.1 AA) | Applies | Older patients are a core persona. | v1.0 (confirmed) | Design | **Change No → Yes** (one short question) | 2: S-077, S-176. **No contract** | The register calls this "best practice for us". I recall the RPwD Act and a 2024 Supreme Court ruling extending accessibility duties to private service providers [VALIDATE]. GIGW is for government sites and does not apply to us. The target itself is still an open decision in the requirements spec (§7.3). |
| C29 | App store policies (Google, Apple) | Applies | We ship Android and iOS apps with health data. | v1.0 (confirmed); accounts in Sprint 0 (S-103) | Platform/Engineering lead | No (agree) | 3: S-103, S-187, S-271. **No contract** | Store rules, not law. Google's health apps declaration is verified. Both stores require in-app account deletion, which ties to C02 [VALIDATE]. Google may require an organisation account for health apps [VALIDATE]; that needs the legal entity from S-007. |
| C30 | Medical record retention | Applies | Notes and prescriptions must be kept even when a patient asks for erasure. | v1.0 (confirmed) | Founder (with Doctor advisor) | Yes | 3: S-011, S-139, S-150. K-DP-07, K-CS-05 | The retention period is not stated in the register (I recall 3 years in the 2002 Regulations) [VALIDATE]. The duty sits with the doctor; our role must be defined. |
| C31 | ISO 27001 / SOC 2 | Voluntary / optional | Not required by law. Useful for clinic chains, enterprise buyers and markets abroad. | **"Later" is not a version.** Founder to set (suggest: decide at the v2.0 gate) [CONFIRM] | Platform/Engineering lead | No (agree) | 2: S-051, S-183. No contract | Separate from the CERT-In-empanelled VAPT (S-183) and ABDM security tests, which are planned anyway. |
| C32 | Target-market laws (GDPR, HIPAA) | Applies – later version | Second country is v5.0. | **Changed:** v1.1 (one question, see note), v5.0 (full) [CONFIRM] | Founder | Yes | 8: S-054, S-075, S-081, S-099, S-173, S-270, S-273, S-274. No contract (K-GLB rules are related) | F13 (v1.1) invites caregivers "living elsewhere or abroad". A caregiver in the EU or UK may bring GDPR into play and means health data is viewed outside India. Ask the lawyer before v1.1. |
| C33 | Emergency (112) and claims | Applies – later version | SOS is v1.1 (F9). No ambulance dispatch (non-goal). | v1.1 (confirmed) | Design (with Doctor advisor) | No (agree) | 3: S-195, S-203, S-265. K-CS-06 | Suggest a "not for emergencies, call 112" line in v1.0 consult screens [CONFIRM]. |
| C34 | TDS on payouts | Conditional | Applies only when money passes through us to clinics. | **Changed:** v1.0 if split settlement is used at v1.0; otherwise v1.2 | CA | **CA, not lawyer** | 2: S-019, S-220. **No contract** | **Stale reference** (see section 3.3 item 2). The CA should also say whether other TDS (for example on professional fees) applies [VALIDATE]. |

---

## 3. Gaps and inconsistencies

### 3.1 Story and contract coverage
- **Every C-ID is cited by at least one Backlog story.** No C-ID is uncovered.
- **No story cites a C-ID that does not exist.** The highest reference is C34. Three stories use "All C-IDs" (S-008, S-070, S-177) and two use ranges ("C01-C06" in S-071, "C17-C22" in S-073). These are valid but will not be found by a filter on a single C-ID.
- **13 C-IDs are cited by no Contract rule:** C08, C11, C14, C15, C20, C24, C25, C27, C28, C29, C31, C32, C34. For most this is fine (they are not coding rules). Four look like they need a rule when contracts are written in P08 [CONFIRM]:
  - C08: grievance and takedown timers.
  - C11: no paid ranking; no payment to a doctor linked to a referral or prescription.
  - C24: clinic registration evidence where the state requires it.
  - C20/C34: tax rules come from config signed off by the CA, never hard-coded.
- **Thin coverage (2–3 stories):** C25, C27, C28, C31, C34 (2 each); C04, C08, C18, C24, C29, C30, C33 (3 each). Check C04, C08 and C30 in particular.
- **S-009 does not match the register.** Its acceptance criterion is a written opinion on every C-ID marked "Lawyer check: Yes", but it lists only C01, C08, C09, C17, C19. Twenty items are marked Yes, and C09 is marked No.

### 3.2 Register inconsistencies
- **Owner is blank for all 34 rows; Status is "Not started" for all.** There is no "Applies?" column. Section 6 gives values to paste; a new column is needed.
- **"Due by" is not a version for two rows:** C14 ("Ongoing") and C31 ("Later"). S-177 (v1.0 compliance sign-off) filters on due version, so these would be missed.
- **Due versions earlier than the features listed:** C11 lists F1.1 (v3.0) and F1.2 (v2.0) but is due v1.0; C19 lists F15 (v1.1); C21 lists F9 (v1.1); C23 lists F2-b (v2.0). Not wrong, but the Features column mixes versions.
- **Due versions later than first use:** C01, C22 (v0 stories already cite them), C11 and C14 (marketing from week 2), C12 (prescription rules in v1.0).
- **21 rows give "General knowledge — confirm with lawyer" as the source.** Eight of those are marked "Lawyer check: No" (C13, C18, C21, C22, C28, C29, C31, C33). The two cannot both be right.
- **C20 and C34 say "confirm with a CA" but are marked for a lawyer.** There is no CA column.
- **Weak sources:** C01 (vendor blog), C05 (exam-prep site), C09 (third-party drug list), C26 (exam-prep site).
- **Contract cross-reference:** K-DP-01 cites C07 for keeping all personal data in India; C07 covers logs only.

### 3.3 Content that looks stale or wrong
1. **C11.** The register says "check which version is in force" and cites general knowledge. The NMC's Ethics and Medical Registration Board issued "Guidelines on Ethical Advertising and Public Communication" on 6 Oct 2026, in force at once, with penalties through state medical councils (verified via news report, not the NMC notice). Reported points that touch our plans:
   - Platforms hosting doctors must not sell ranking and must be transparent about listings. The roadmap (F15) and vision say "any paid placement is clearly labelled", which may no longer be enough.
   - Patient testimonials promoting a doctor are banned; consent does not cure this. The Marketing sheet (MK-10) and S-048 assume "testimonials with written consent".
   - No payment to or from a doctor linked to referring or procuring individual patients, including "lead-generation fees". A per-booking platform fee needs a lawyer's view.
   - Doctor photos used as self-promotion are restricted; F17 collects a photo and profiles may show it.
   - Genuine patient reviews (F15) are not addressed in the report; fake or manipulated reviews are banned.
2. **C34.** "Section 194-O" belongs to the Income-tax Act 1961, replaced from 1 Apr 2026. The rule now sits in section 393(1) of the 2025 Act; most sources give a 0.1% rate (verified, secondary; one source disagrees).
3. **C05.** "A government committee will decide which data categories cannot leave India" does not match what I found (see C05 row) [VALIDATE].
4. **C09.** Omits the platform's own duties under the Guidelines [VALIDATE].
5. **C26.** Cites the 1947 Act without the 2023 replacement Act [VALIDATE].
6. **C28.** Understates the duty on private providers [VALIDATE].
7. **C17.** The FY25/FY26 turnover window is time-bound and will be out of date at launch.
8. **C01.** Uses "consent manager" for an in-app screen; under DPDP that term means a registered entity whose rules took effect about 13 Nov 2026 (verified).

### 3.4 Timing risk (dates are my conversion of the sheet's date numbers) [CONFIRM]
| Milestone | Date |
|---|---|
| Project start | 16 Nov 2026 |
| v0 concierge runs | about 23 Nov 2026 – 3 Jan 2027 |
| Waitlist landing page (MK-02) | weeks 2–4 |
| v1.0 target | 2 May 2027 |
| DPDP main duties start | about 13 May 2027 |
| v1.1 target | 13 Jun 2027 |

Personal and health data is handled from week 2, long before the v1.0 controls exist. That is why eight items move to v0.

### 3.5 Regulations that may be missing (proposals only; nothing added to the register)
| # | Proposed row | Why it matters to us | Suggested due | Lawyer |
|---|---|---|---|---|
| N1 | IT Act s.43A + SPDI Rules 2011 | In force until about 13 May 2027 (verified). Health information is "sensitive" data: consent, a privacy policy and reasonable security are expected. Covers v0, the waitlist and any beta before that date. | v0 | Yes |
| N2 | NMC advertising guidelines, 6 Oct 2026 | See 3.3 item 1. Could be folded into C11 instead of a new row. | v0 | Yes |
| N3 | Dark-pattern guidelines (2023), misleading-ads and endorsement guidelines (2022), online reviews standard IS 19000:2022 (voluntary) | Consent screens, default-on settings, refund flows, marketing, and F15 reviews [VALIDATE]. Could be folded into C19. | v1.0; reviews at v1.1 | Yes |
| N4 | Platform duties under the Telemedicine Guidelines | See C09. Fold into C09. | v1.0 | Yes |
| N5 | Electronic signature on prescriptions (IT Act) | Whether an in-app signed prescription PDF is valid for pharmacists and records [VALIDATE]. | v1.0 | Yes |
| N6 | Allied and Healthcare Professions Act 2021 | Phlebotomists for home collection (F4-a) and dietitians (F10-b) in v2.0 are neither doctors nor nurses; F17 has no check for them [VALIDATE status of the registers]. | v2.0 | Yes |
| N7 | National Nursing and Midwifery Commission Act 2023 | Update to C26. | v4.0 | Check before v4.0 |
| N8 | Mental Healthcare Act 2017; HIV and AIDS Act 2017 | Extra confidentiality rules if psychiatry is offered or HIV status is recorded [VALIDATE]. Depends on launch specialties. | v1.0 (decision) | Yes |
| N9 | PCPNDT Act 1994 | Ultrasound centres must be registered; applies when scans are booked through us (v2.0) and to related advertising [VALIDATE]. | v2.0 | Yes |
| N10 | DPDP "Significant Data Fiduciary" and "Consent Manager" status | Neither applies today [VALIDATE]. Watch-item only: extra audits and a data protection officer if the Government ever notifies us. | Watch | Yes (one line) |
| N11 | TRAI rule changes 2025–2026; voice-call number series | Update to C21. Matters more if voice or IVR reminders are added later. | v1.0 | No |
| N12 | Foreign investment rules for online marketplaces | Only if foreign funding is raised [CONFIRM]. | When fundraising | Yes |

Checked and not proposed: DISHA (a 2018 draft health-data law that I understand was never enacted) [VALIDATE]; ABDM's Health Data Management Policy (belongs under C15); app-store rules (already C29).

---

## 4. Check against the acceptance criterion

Criterion: "Every C-ID has an applicability decision, owner and due version."

| Part | Met? | Evidence |
|---|---|---|
| Applicability decision for every C-ID | Proposed for 34 of 34 | Section 2. Three are "Conditional" and wait on a decision or a lawyer (C16, C24, C34). |
| Owner for every C-ID | Proposed for 34 of 34, by role | Section 2 and section 6. No names assigned. |
| Due version for every C-ID | Proposed for 33 of 34 | C31 has no version until the founder sets one. C34 has two possible versions until the money flow is decided. |

**Status: values entered; awaiting founder sign-off.** On 2026-11-14 the section 6 values were entered in the Compliance sheet (Owner L, Due by H, Lawyer check J, new columns O "Applies?" and P "S-008 review notes"). C31 was entered as "Decide at v2.0 gate". C34 and C01 texts were corrected (sections 3.3 items 2 and 8). Fourteen Backlog stories received matching refs or notes (S-008, S-009, S-014, S-017, S-019, S-020, S-025, S-048, S-070, S-071, S-074, S-110, S-177, S-182), and S-009's lawyer scope now lists the 20 lawyer items. S-008 is "In Testing". It is Done when the founder names people against the owner roles and settles the open decisions in section 5. The Contracts sheet, Status column and phase dates were not changed, and no rows were added (N1–N12 remain proposals).

---

## 5. Open decisions for the founder

**Decided 14 Nov 2026 (during S-010):**
- **3:** adults only in v1.0.
- **4:** follow the law. As currently understood, it does not require all data to stay in India, so health data stays India-only. The lawyer confirms in S-009; if the law requires all data in India, switch to that.
- **5:** WhatsApp is **ON HOLD** (founder, 14 Nov 2026). If resumed, it uses the official Business Platform (cost note in docs/research/whatsapp-business-cost.md). Still open: the v0 reminder channel while WhatsApp is on hold, and which v0 tools hold pilot data. C22 stays in the register, with its due version paused.
- **Launch language:** English only for now.

1. **Owners.** Confirm or change each proposed role, and put a named person against each. In particular, name the grievance contact (C02, C08, C19) and the CERT-In point of contact (C07).
2. **v1.0 date against the DPDP date.** Accept launching with full DPDP controls from day one, or move the date?
3. **Under-18 patients in v1.0.** Adults only until v1.1, or bring guardian profiles forward?
4. **"India only" rule (K-DP-01).** Is it a hard rule for all personal data, or for health data only? Which vendor exceptions, if any, are allowed?
5. **How compliant must v0 be?** Which tools hold concierge data, what consent form is used, and is WhatsApp sent through the official Business Platform?
6. **v1.0 money flow.** Split settlement or clinic-collected (S-019). This sets C17, C19, C20 and C34.
7. **Fee model and paid placement** in light of the Oct 2026 NMC guidelines: keep "labelled paid placement" or drop paid placement? Is a per-booking fee acceptable?
8. **Marketing with doctors and testimonials:** keep, change or drop the testimonial and featured-doctor items.
9. **Lawyer scope for S-009.** Which C-IDs go to the lawyer now (the register says 20; S-009 lists 5), and whether to engage a CA separately for C20 and C34.
10. **Labs:** NABL-accredited only, or wider (C25)?
11. **ISO 27001 / SOC 2:** when to decide (C31).
12. **Accessibility target:** WCAG 2.1 AA or other (C28; also open in the requirements spec).
13. **Launch state and launch specialties** (C24, N8).
14. **Nurse timing** (C26, C27; already open in the roadmap).
15. **Caregivers outside India in v1.1:** allow, restrict or defer (C32).
16. **Identity documents:** may doctors submit an Aadhaar card as ID, masked or not at all (C23)?
17. **Register edits:** accept, change or reject the proposed corrections in 3.2–3.3 and the proposed rows in 3.5.

---

## 6. Paste-ready block for the Compliance sheet

Rows are in C01–C34 order, tab-separated: Applies? · Owner · Due by. Add an "Applies?" column to the sheet first; Owner and Due by already exist. All values are proposals until confirmed.

```
Applies	Founder	v0 (lite); v1.0 (full)
Applies	Founder	v1.0
Applies	Platform/Engineering lead	v0 (basic); v1.0 (full)
Applies	Founder	v1.0 (age rule); v1.1 (guardian profiles)
Applies	Platform/Engineering lead	v1.0
Applies	Founder	v0 (vendor list); v1.0 (full)
Applies	Platform/Engineering lead	v0 (contact, reporting); v1.0 (logs)
Applies	Founder	v1.0
Applies	Doctor advisor	v1.0
Applies	Founder	v0 (manual check); v1.0 (pipeline)
Applies	Founder	v0
Applies	Founder	v1.0 (prescription rules, marketing); v3.0 (pharmacy)
Applies	Doctor advisor	v1.0
Applies	Marketing	v0, then every release
Applies - later version	Platform/Engineering lead	v2.0
Conditional	Founder	v1.1
Applies	Founder	v1.0
Applies	Platform/Engineering lead	v1.0
Applies	Founder	v1.0
Applies	CA	v1.0
Applies	Platform/Engineering lead	v1.0
Applies	Platform/Engineering lead	v0
Applies	Platform/Engineering lead	v1.0
Conditional	Founder	v1.0
Voluntary	Founder	v2.0
Applies - later version	Founder	v4.0
Applies - later version	Founder	v2.0 (lab clause); v4.0 (full)
Applies	Design	v1.0
Applies	Platform/Engineering lead	v1.0
Applies	Founder	v1.0
Voluntary	Platform/Engineering lead	Not set
Applies - later version	Founder	v1.1 (caregivers abroad); v5.0 (full)
Applies - later version	Design	v1.1
Conditional	CA	v1.0 if split settlement; else v1.2
```

If the sheet's Due-by column must hold a single version (for the S-177 filter), use the first version in each cell.

---

## 7. Sources used for verification (2026-11-14)

Secondary sources only; check primary texts before relying on any of them.

- DPDP Rules timeline: https://www.legal500.com/firms/237811-saga-legal/c-india/news-and-developments/the-privacy-timer-starts-now-analysing-the-dpdp-rules-and-its-18-month-countdown · https://www.amsshardul.com/wp-content/uploads/2025/11/Regulatory-Alert-Enforcement-of-DPDP-Act-and-Notification-of-DPDP-Rules.pdf
- SPDI Rules until May 2027: https://ksandk.com/data-protection-and-data-privacy/what-happens-spdi-rules-after-dpdp-enforcement/
- DPDP children's exemptions: https://www.storyboard18.com/digital/dpdp-rules-carve-out-key-exemptions-for-healthcare-providers-schools-and-childcare-services-processing-childrens-data-84208.htm
- DPDP cross-border: https://ksandk.com/data-protection-and-data-privacy/indias-new-cross-border-data-transfer-framework/ · https://www.dpdpa.com/dpdparules/rule13.html
- CERT-In Directions status: https://www.tcsa.in/learn/cert-in-directions-2022-explained
- CDSCO software guidance: https://www.emergobyul.com/news/india-cdsco-finalizes-guidance-medical-device-software · https://www.nishithdesai.com/research-and-articles/hotline/pharma-healthcare-update/from-code-to-compliance-decoding-indias-medical-device-software-guidance-15623
- Razorpay Route eligibility: https://razorpay.com/docs/payments/route/faqs.md
- RBI Payment Aggregator Directions 2025: https://www.azbpartners.com/bank/rbi-issues-consolidated-reserve-bank-of-india-regulation-of-payment-aggregators-directions-2025/
- NMC advertising guidelines, Oct 2026: https://medicaldialogues.in/health-news/nmc/nmc-issues-ethical-advertising-public-communication-guidelines-for-doctors-hospitals-details-180731
- NMC 2023 conduct regulations on hold: https://medicaldialogues.in/mdtv/healthshorts/nmc-puts-on-hold-its-controversial-registered-medical-practitioner-regulations-2023-116395
- Telemedicine Guidelines 2020 still cited, Oct 2026: https://thesouthfirst.com/health/can-a-doctor-approve-your-test-report-without-being-physically-present-nmc-clarifies/
- E-pharmacy rules still draft: https://www.business-standard.com/topic/e-pharmacies · https://businesstoday.in/industry/pharma/story/chemists-protest-online-medicine-sales-seek-tighter-regulation-of-e-pharmacies-532550-2026-05-20
- TDS under the Income-tax Act 2025: https://blog.tdsman.com/2026/05/tds-on-e-commerce-transactions-section-3931-194o/
- GST for online platforms: https://cleartax.in/s/gst-registration-for-ecommerce
- TRAI rule changes: https://www.trai.gov.in/sites/default/files/2026-09/PR_No119of2026.pdf · https://www.fonearena.com/blog/446114/trai-tcccpr-2025-tougher-rules-spam-calls-sms.html
- Google Play health apps policy: https://support.google.com/googleplay/android-developer/answer/12253906?hl=en-GB

**Not verified (no search run or no result):** C10 register figures, C13, C18, C22 health-content limits, C23–C27, C28 legal duty, C30 retention period, C33, the DPDP breach and log-retention periods, and all rows N3–N12.
