# Doctor review pack: clinical edge cases and open clinical questions (S-029)

**Phase P03 · Epic EP05 · Depends on:** S-028 (Done) · **Compliance:** C09 · **Contract:** K-CS-07 · **Date:** 2026-11-15
**Status:** Ready for review. **It needs a registered doctor**: none is named yet (S-008 open decision 1; the same reviewer is needed for S-012).

**Who this is for:** the registered doctor who reviews our clinical rules. Plan for **about 90 minutes**. For each row, mark **Agree**, **Change** (write the change) or **Unsure**. When done, the founder records your name, registration number, council and date in the Edge Cases sheet "Notes" column for each row, and the status becomes "Design verified" or "Design change needed" (acceptance criterion).

**Background in one paragraph:** the product lets patients book verified doctors in person or online (video or audio). Doctors write **structured prescriptions** (drug, dose, frequency, duration). Patients then get **medicine, test and follow-up reminders**. From v1.1 there is an **SOS button** (dials 112, alerts contacts) and a **paper-prescription scan** that a trained reviewer checks before reminders are made. The software **never suggests a drug, dose or diagnosis**.

## Part 1 — Edge cases to sign off (15 rows)

| E-ID | Situation | What the product does | Severity | Your view |
|---|---|---|---|---|
| E-RX-01 | Doctor tries to prescribe a prohibited drug (Schedule X or NDPS) in a teleconsult | Blocked with a reason, no override | Critical | |
| E-RX-02 | Ambiguous dose notation | Structured dose, frequency and duration fields; free text only for instructions | High | |
| E-RX-03 | Prescription changed after the patient saw it | Saved as a new version; reminders updated; patient notified | High | |
| E-RX-04 | As-needed medicine | No fixed reminders; patient logs each dose | Medium | |
| E-RX-05 | Tapering dose | Schedule allows a different dose per day | Medium | |
| **E-RX-06** (new) | Video drops to audio before the prescription is signed | Rules use the mode at signing; a List A first prescription is blocked with a prompt to restore video | High | |
| E-REM-01 | Patient travels across time zones | Reminders follow the patient's chosen home or local time | High | |
| E-REM-02 | Phone offline or notifications off | Backup schedule on the device; SMS fallback for critical items | High | |
| E-REM-03 | Ten medicines due at once | One grouped reminder | Medium | |
| E-REM-04 | Patient stops a medicine without telling the doctor | Marked stopped; visible to the doctor with the patient's consent | Medium | |
| E-REM-05 | Same medicine from a scanned paper prescription and an app prescription | Duplicate flagged for the patient to resolve; never merged automatically | High | |
| E-SOS-01 | No internet when SOS is pressed | Phone dialler calls 112; contact alerts queued | Critical | |
| E-SOS-02 | Accidental SOS tap | A short cancel window. **Please propose the length** (for example 3 or 5 seconds) | Medium | |
| E-OCR-01 | Blurry or dark photo of a prescription | Retake requested before upload | Medium | |
| E-OCR-02 | Handwriting unclear | Reviewer marks it unclear; the patient is asked to confirm with the doctor or pharmacist; **never scheduled** | Critical | |

## Part 2 — Prescribing rules (from S-012, [telemedicine-rules-spec.md](telemedicine-rules-spec.md) §11)

1. **Q1 Unclassified drugs:** until a drug is categorised, treat it as List A (video needed for the first prescription), or block it?
2. **Q2 Off-platform in-person visits:** may a doctor attest a paper or clinic visit, with a date, to unlock follow-up rules? It is audited.
3. **Q3 Video then audio:** after a first video consult on the platform, can a later audio consult refill a List A drug? The strict reading requires an in-person visit first.
4. **Q4 Schedule X and NDPS in person:** block them in the product everywhere (drafted), or allow in-person e-prescriptions with the extra legal requirements?
5. **Q5 Drug categories:** who curates the drug-to-category list, and how often?
6. **Q6 Schedule H1:** confirm nothing extra is needed for v1.0 (no pharmacy until v3.0).
7. **Q7 Follow-up window:** 6 months from the last in-person visit (drafted), or from the first?
8. **Check against the official text:** the 6-month window, and the audio and text rows of the decision table, were not checked against the primary guidelines PDF (it was unreachable). Please confirm them.

## Part 3 — New questions from patient reviews (S-027)

9. **Q8 Minimum standards by specialty:** Practo reviews describe psychiatric consults under 3 minutes ending in sleeping pills. Should the product require **video for a first psychiatry consult**, or show a minimum-time warning before signing a first prescription in some specialties? (The software would still never suggest a drug.)
10. **Q9 Short consults:** we plan to flag consults under 2 minutes and stop them being marked "Completed" unless both sides connected (E-BK-08). Is 2 minutes right? Are there valid very short consults, such as a report review?
11. **Q10 Adolescent confidentiality (S-017, CH-11):** from v1.1, when a guardian manages a 13–17-year-old's profile, should a doctor be able to mark a note "discuss in person" so it is not shown to the guardian in the app?

## Part 4 — Wording

12. Review the clinical wording of the block messages in [rules/IN/telemedicine-rules.v1.draft.yaml](rules/IN/telemedicine-rules.v1.draft.yaml) (`reasons`), and the reminder SMS texts SMS-09 to SMS-11 in [messaging-compliance-plan.md](../messaging/messaging-compliance-plan.md).

## Sign-off record (to copy into the Edge Cases sheet)

| Reviewer name | Registration number | Council | Date | Rows agreed | Rows changed |
|---|---|---|---|---|---|
| | | | | | |
