# v1.0 scope freeze and smallest beta cut (S-030) — PROPOSAL for founder sign-off

**Phase P03 · Epic EP05 · Depends on:** S-004 ([requirements-v1.0.md](requirements-v1.0.md)), S-027 ([top-complaint-themes.md](research/top-complaint-themes.md)) · **Base:** [feature-roadmap.md](../Resources/feature-roadmap.md) v0.3, section 4 (v1.0) · **Date:** 2026-11-15
**Status:** In Testing, until the founder signs section 6.

**Who this is for:** the founder, to sign off the v1.0 scope; and the team, to plan sprints 4–12 against it.

## 1. v1.0 in one line

**"Consult → Prescription → Reminders"** for adult patients in one city, with verified doctors, clinic-collected payments and reminders by push and SMS. It tests one hypothesis: doctors write structured prescriptions in the product if it is faster than paper, and patients with automatic reminders come back.

## 2. Changes to the roadmap's v1.0 because of decisions made since v0.3

| Change | Why | Effect on the backlog |
|---|---|---|
| **F11-a WhatsApp removed from v1.0** while on hold | Founder, 14 Nov 2026 | S-167 → Won't (this version). S-156 and S-157: app, push and SMS only. S-102: DLT only. R-72 is out of v1.0. Comes back as soon as the hold is lifted. |
| **Clinic-collected payments are the v1.0 default** | S-019 recommendation; split settlement needs ₹40 lakh turnover | S-163 Should → **Must**. S-134: receipts in the clinic's name; our GST invoice is for the clinic subscription, not the patient. Split settlement stays at v1.2 (S-217). |
| **Adults only** | Founder decision 3 | No minors anywhere in v1.0 (S-017 CH-01). |
| **English only at launch** | Founder decision, 14 Nov 2026 | S-100 keeps the translation set-up, but only English ships. |
| **Nurse accounts not in v1.0** | Proposed in S-031 | S-105 and S-106 keep "nurse" in the role model but create no nurse accounts until v4.0. |
| **New trust behaviours from patient reviews** | S-027 themes T01–T04, T07 | Added to existing stories (no new stories): consult cannot close without both sides connected (S-148, E-BK-08); day-before clinic reconfirmation (S-113, E-BK-07); one price (S-112, S-122); the patient always picks a named doctor (S-119). |
| **New story S-275** (Provider Agreement and provider privacy notice) | Gap found in S-023 | Must, before S-110 and S-163 |
| **Search ranking without paid placement** | S-018, CD-05 (open decision 7) | S-162 unchanged; no "featured" slots |
| Templates for common prescriptions (R-53) | Should in S-004 | Keep as Should inside S-149; drop first if the doctor flow runs late |

## 3. Must / Should / Won't for v1.0

- **Must:** every Sprint 0 and v1.0 story in the Backlog, apart from the items below. That is **94 of 99**, plus S-275.
- **Should (ship if on time, otherwise v1.1):**
  - S-145: bulk clinic closure. Manual cancellation is acceptable until then.
  - S-161: badge detail ("what we checked").
  - Prescription templates (part of S-149).
- **Won't (this version):**
  - S-167: WhatsApp fallback (WhatsApp on hold).
  - S-173: config management screen. Config changes go through reviewed pull requests with audit, which K-GLB-01 already requires.

## 4. Smallest beta cut (closed beta: v0 doctors, one city, invited patients)

The beta is v1.0 **minus** the stories below. They are still Must for the **public** v1.0 launch. Each one has a manual stand-in that works at beta scale (10–15 doctors, a few hundred patients).

| Deferred to after beta | Manual stand-in during beta | Must before |
|---|---|---|
| S-111: third-party verification adapter | Manual register checks by the reviewer (S-129). This is safe at 15 doctors. | Public launch |
| S-160: re-verification scheduler | Monthly manual check of the beta doctors' registers | Public launch |
| S-130: doctors at several clinics, clinics with several branches | Recruit single-site beta clinics | Public launch |
| S-162: search ranking v1 | Sort by distance only (few doctors) | Public launch |
| S-143: self-service data export, correction and deletion | Requests by the Grievance Officer through the queue (S-142), within the same timelines | Public launch; legally before about 13 May 2027 |
| S-175: load tests at full target scale | Smoke load test at 10× beta volume | Public launch |
| S-145, S-161 (Should) | — | v1.1 if not done |

**Never deferred, even for the beta:**
- Security and compliance gates: VAPT before real patient data (S-183), consent (S-097, S-107, S-168), audit (S-096), encryption (S-098), authorisation tests (S-116, S-181), and incident drill (S-184).
- The prescribing rules engine (S-138) and prescribing gate (S-118).
- Refunds (S-133).
- The S-027 trust behaviours.

## 5. What would move v1.0's date (watch list)

1. Lifting the WhatsApp hold adds back S-167 and the WhatsApp parts of S-156, S-157 and S-102 (about 7 points, plus Meta approval time).
2. Doctor review (S-029, S-012) not done: S-138 cannot be finalised.
3. Payment partner access (S-040) not available from any aggregator: clinic-collected mode needs a different design.
4. Lawyer review (S-009) late: the privacy, terms and provider documents block the beta.

## 6. Sign-off

| Item | Founder decision |
|---|---|
| Scope in sections 2–3 | ☐ Agree ☐ Change: |
| Beta cut in section 4 | ☐ Agree ☐ Change: |
| Name | |
| Date | |

**Backlog already updated as proposed (reversible):**
- S-163 priority set to Must.
- S-167 and S-173 set to Won't (this version).
- Beta-cut notes added to S-111, S-130, S-143, S-160, S-162 and S-175.
