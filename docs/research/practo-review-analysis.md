# Practo review analysis (S-015)

**Phase P03 · Epic EP04 · Compliance:** C01 · **Date:** 2026-11-15 · **Status:** Done (acceptance criterion: at least 300 rows categorised)
**Data:** Practo Review sheet, rows 6–305, in [healthcare-app-agile-roadmap.xlsx](../../Resources/healthcare-app-agile-roadmap.xlsx). Row 5 is the template's example row and is not counted.

**Who this is for:** the founder and the design team (P03 to P09). This document summarises what patients complain about, so the v1.0 design can answer it.

## 1. Method

| Item | Detail |
|---|---|
| Sources | App Store India public reviews feed (latest 500, keeping those dated 15 Nov 2025 – 9 Nov 2026), and Google Play India (newest 450, dated 14 Jul – 13 Nov 2026) |
| Sample | 300 reviews: 150 from each store. 240 are rated 1–2 stars (80%) and 60 are rated 3–5 stars. Random draw within each store and rating band (seed 15). Reviews under 40 characters were dropped because they cannot be categorised. |
| Privacy | No reviewer names were collected. Every issue is paraphrased with no names, phone numbers or order IDs, and was machine-checked for names, numbers and copied text. Raw downloads stay in the working scratchpad and are not in the repo. |
| Coding | Category (from the sheet's 16-item list), a short theme and a severity. An AI agent coded every row and Claude checked the values. **A human should spot-check about 30 rows** before the findings drive big decisions. |
| "Times seen" | 1 per row, because each row is one review. |

**Limits:**
- Google Play covers only the last 4 months.
- The 80% weighting to low ratings is deliberate, so the averages show where the pain is, not Practo's overall rating.
- Categories overlap (for example "doctor didn't join and no refund"), and only the main complaint is coded.

## 2. What patients complain about (1–2 star reviews, n = 240)

| Rank | Category | Reviews | What it looks like |
|---|---|---|---|
| 1 | App bugs / performance | 37 | Chat will not open; messages appear only after restarting the app; an iOS two-minute lock after booking; video calls not connecting |
| 2 | Consultation quality | 34 | Rushed or one-word answers; unqualified-seeming doctors; no way to change doctor; prescriptions without proper assessment |
| 3 | Booking reliability | 29 | Confirmed slot, but on arrival the doctor was on leave or no longer at that hospital; automated calls "confirmed" bookings anyway |
| 4 | Refunds & payments | 22 | Doctor cancelled or never joined, and the refund was refused, partial or never arrived |
| 5 | Customer support | 21 | Bots only; no human; canned replies; no refund reference |
| 5 | Pricing / fees | 21 | App fee differed from the clinic's fee; extra charges; membership plans that excluded the doctor |
| 7 | Doctor no-show / delays | 14 | Paid, but the doctor never connected or left after minutes, and the consult was marked complete |
| 7 | Search accuracy | 14 | Wrong doctor or clinic details; profile errors not corrected |
| 9 | Reviews trust | 10 | Negative reviews hidden or removed; rating forced before treatment |
| 9 | Notifications / spam | 10 | Sales calls after browsing a profile; repeated bot calls |
| | Others | 29 | Lab tests 7, Other 7, Medicine orders 5, Wait times 5, Login/OTP 2, Privacy 2 |

## 3. Six findings that matter for our design

1. **Money taken without care delivered is the biggest trust breaker.** No-shows, refunds and fee mismatches add up to 57 of the 240 low-rated reviews (24%).
   - **Our answer:** an automatic refund when the doctor doesn't join (E-BK-04) or the payment fails (E-PAY-02); the refund policy and full price shown before paying; the app fee always equals the clinic fee (C19).
2. **Stale directory data causes wasted trips.** About 20 reviews describe a confirmed booking where the doctor was on leave, had left, or was never at that clinic.
   - **Our answer:** slots come only from the doctor's live calendar (S-113); verified profiles with re-verification (S-129).
   - **Gap:** nothing covers "the clinic forgot to mark the doctor's leave". Proposed **E-BK-07** below.
3. **Bots instead of people.** 16 reviews complain about AI or bot support or calls.
   - **Our answer:** a human escalation path and grievance timelines of 24-hour acknowledgement and 7-day resolution ([data-rights-and-grievance.md](../privacy/data-rights-and-grievance.md)); no automated sales calls (P-NEWS is opt-in only).
4. **Clinical safety complaints are rare but serious.** All 9 Critical rows are about consultation quality: rushed mental-health consults ending in sleeping pills, prescriptions without a proper look, children's care, and doctors contradicting each other.
   - **Our answer:** only verified doctors can prescribe; the S-012 rules engine (for example, a List A drug's first prescription needs video, and Schedule X or NDPS drugs are blocked); diagnosis required before prescribing (check P05); and adults only in v1.0.
5. **Review manipulation is noticed.** Patients see hidden negative reviews and forced ratings.
   - **Our answer:** only completed visits can be reviewed (E-REV-01); negative reviews are not hidden; no paid ranking (C11); moderation reasons are logged (S-213).
   - **Gap:** forced rating prompts. Proposed **E-REV-03** below.
6. **What people praise:** a doctor who listens and explains, quick access in an emergency, and easy booking.
   - These are the strengths to match. Most 3–5 star reviews are about the doctor, not the app.

## 4. Proposed additions (for the Edge Cases sheet; founder to accept)

| ID | Situation | Expected behaviour |
|---|---|---|
| E-BK-07 | A doctor is on leave, or no longer at the clinic, on the booked day, but the calendar was not updated | The clinic must reconfirm the doctor's presence the day before. If it is not confirmed, the patient is warned and offered a reschedule or full refund. A booking is never "confirmed" by an automated call without checking the calendar. |
| E-REV-03 | The app asks for a rating before the consult has ended or treatment has started | A rating prompt appears only after a completed consult. It can be dismissed and never blocks navigation. |

## 5. Next

- **S-052:** about 50 reviews each for Apollo 24/7, Tata 1mg, Aarogya Setu and Eka Care, using the same method and the same sheet.
- Feed sections 2 and 3 into persona and journey work, and into the usability test (S-087).
