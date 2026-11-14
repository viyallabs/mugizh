# Top 10 complaint themes and our design response (S-027)

**Phase P03 · Epic EP04 · Depends on:** S-015 (Done), S-026 (hands-on teardown, not yet done) · **Date:** 2026-11-15 · **Status:** Done (acceptance criterion: each theme linked to an edge case or story)
**Data:** the 240 one- and two-star Practo reviews coded in S-015 ([practo-review-analysis.md](practo-review-analysis.md); Practo Review sheet rows 6–305).

**Who this is for:** the founder and design team, to freeze v1.0 scope (S-030) and design the flows; and S-028, which adds the new edge cases to the sheet.

## How the themes were built

Each low-rated review was matched to themes using its category and keywords in the paraphrased issue. **A review can match more than one theme** (for example "doctor never joined and the refund was refused"), so the counts add up to more than 240. They show how often a problem is *mentioned*, not exclusive shares.

**S-026, the hands-on teardown, is still to be done** by someone using the Practo and Aarogya Setu 2.0 apps. The column "Teardown should confirm" lists what to check. If the teardown contradicts a theme, update this file.

## The 10 themes

| # | Theme | Low-rated reviews mentioning it | Example (paraphrased) | Our design response | Linked edge case / story | Teardown should confirm |
|---|---|---|---|---|---|---|
| T01 | **Paid, but the doctor never joined or left after a few minutes**, and the consult was marked "complete" | 23 | Paid for an online consult; the doctor dropped off within minutes and it was marked complete with no refund | Automatic full refund if the doctor doesn't join within 10 minutes (RF03). A consult cannot be closed as "Completed" unless both sides connected. A patient can dispute a very short consult within 24 hours. Doctor reliability is recorded. | E-BK-04; **new E-BK-08**; S-133, S-148 | How Practo closes a consult; whether patients can dispute |
| T02 | **Refund refused, partial or slow**, with no reference number | 33 | Clinic cancelled; refund requested several times and never paid | Published refund rules (RF01–RF09) applied automatically. Refund status and reference shown in the app and by SMS (SMS-07). Grievance resolution within 7 days. **In clinic-collected mode, a failed refund from a clinic's account is escalated and blocks new bookings after 48 hours.** | E-PAY-02, E-PAY-04; **new E-PAY-05**; S-133, S-144, S-163 | Refund screens and timelines in Practo |
| T03 | **Booking confirmed, but the doctor was on leave, had left the clinic, or the details were wrong** | 44 | An automated call confirmed the slot; at the hospital, reception said the doctor no longer works there | Slots only from the doctor's live calendar. Leave blocks slots and notifies booked patients. The clinic reconfirms the day before. Re-verification yearly and on complaint. A "report wrong details" link on every profile. Never confirm by automated call without checking the calendar. | E-BK-02, E-BK-06; **new E-BK-07**; S-113, S-129, S-160, S-120 | Whether Practo shows calendar-backed slots or request-based bookings |
| T04 | **The app fee differs from the clinic fee, hidden charges, or membership plans that exclude the doctor** | 34 | Charged about ₹479 in the app for a doctor whose clinic fee is far lower | The fee shown equals the clinic fee (Provider Agreement A10). The full price is shown before payment, with no add-ons. No membership plans in v1.0. The clinic is shown as the seller. If a clinic charges more at the counter, the patient can report it and the difference is refunded. | **New E-BK-09**; S-112, S-122; CD-06; [refund-policy.md](../legal/refund-policy.md) | Practo's fee display and plan upsells |
| T05 | **Chat, video or messages broken**: messages appear only after restarting, a lock after booking, video won't connect | 37 | Doctor messages showed only after closing and reopening the app | Per-screen performance and crash-free targets. Real-time message delivery tested on weak networks. A video provider chosen for weak 4G (S-039), with a fallback to audio that the prescribing rules handle (E-RX-06). No forced lock or wait screen after booking. | Performance sheet; **new E-RX-06** (mode at signing); S-039, S-091, S-135, S-136 | Reproduce the iOS post-booking lock and the message-delay bug |
| T06 | **Bots only; no human support**; canned replies | 35 | Support was a bot for over 30 minutes and gave no refund reference | A human escalation path in the app. Grievance acknowledged in 24 hours and resolved within 7 days. Bots never close a complaint about money or safety. | S-142; [grievance-redressal.md](../legal/grievance-redressal.md); C08, C19 | Practo's support entry points |
| T07 | **Rushed or poor consultations; the patient cannot choose or change the doctor** | 34 | Assigned an inexperienced doctor and could not switch | **The patient always chooses a named, verified doctor**, and sees their qualifications, registration and (from v1.1) verified reviews. There is no auto-assignment in v1.0. Specialty is shown only with a recognised qualification. | S-119, S-120, S-209; RG-04 | Whether Practo's online consults auto-assign doctors |
| T08 | **Unsafe prescribing or advice** (all Critical): sleeping pills after a 3-minute mental-health consult, prescriptions without a proper look, medicine for a baby causing harm | 9 | Psychiatric consult under three minutes, with sleeping pills prescribed | The telemedicine rules engine (List A needs video first; Schedule X and NDPS blocked; diagnosis required, check P05). Adults only in v1.0. **Ask the doctor reviewer whether some specialties, such as psychiatry, need a minimum consult standard or video-only rule.** | E-RX-01; S-012, S-138; S-029 review question | — |
| T09 | **Negative reviews hidden, or the app forces a rating** before treatment | 16 | The app forced a rating when going back during an active consult | Only completed visits can review. A rating prompt only after completion, never blocking navigation. Negative reviews never hidden. No paid ranking. | E-REV-01; **new E-REV-03**; S-209, S-213, S-214; CD-05, CD-08 | Rating prompt timing in Practo |
| T10 | **Spam and sales calls** after browsing a profile; repeated bot calls | 21 | An AI bot kept calling about doctor advice and tests despite hang-ups | No marketing without the P-NEWS opt-in. No automated or sales calls. SMS only through registered service templates. Quiet hours. | **New E-MSG-01**; S-141; K-MSG-01, K-MSG-03; consent-texts P-NEWS | Whether calls follow profile views |

**Outside v1.0 scope:**
- **Lab tests** (7 reviews): add these themes when lab partners arrive in v2.0 (S-231).
- **Medicine orders** (5 reviews): add these themes when the pharmacy arrives in v3.0.
- Wait times (5) and login problems (2) are covered by T05, T07 and S-104.

## What this means for v1.0 scope (input to S-030)

**Five themes are core to v1.0 and must ship in the beta:** T01 (no-show refund), T02 (refund status), T03 (calendar-backed slots), T04 (one price) and T07 (patient chooses a named doctor). These are the trust promises that set us apart from the complaints above.

## New edge cases for S-028

| E-ID | From |
|---|---|
| E-BK-07 | T03: doctor absent on the booked day and the calendar was not updated |
| E-BK-08 | T01: consult closed as complete without both sides connecting, or ended within 2 minutes |
| E-BK-09 | T04: clinic charges more at the counter than the fee shown in the app |
| E-PAY-05 | T02: refund from the clinic's account fails (low balance) in clinic-collected mode |
| E-REV-03 | T09: rating prompt before the consult has finished |
| E-MSG-01 | T10: a marketing message attempted without opt-in |
| E-RX-06 | T05: video drops to audio before the prescription is signed (first proposed in S-012) |
