# Build blockers

**Last updated:** 2026-11-23 · **Source:** [build-decisions-log.md](build-decisions-log.md), [code review](../review/code-review-2026-11-17.md) section 7, the tracker

What stops stories from reaching Done, who can unblock each, and what is built in the meantime. Nothing here may be decided by Claude (CLAUDE.md section 5).

## 1. Founder decisions

| # | Blocker | Blocks | Built meanwhile | Ref |
|---|---|---|---|---|
| F1 | Account recovery when the patient has lost every verified device: which identity check (proposal: verified email OTP + date of birth + a recent booking detail; fallback a support video call with non-Aadhaar photo ID) | S-115 | Device list, remote sign-out with wipe push, approve a new device from a verified one | E-DP-23, D-69 |
| F2 | Fairness of late-cancellation and no-show charges (RF08, RF09 stay blocked) | Refund policy approval | RF01–RF07 from the stored policy | FAIRNESS-01, K-PAY-05 |
| F3 | Numbers and rules marked [CONFIRM]: dose-action windows and snooze limits; critical message list; share extension limits and the 24 h decline cooldown; device schedule 72 h, 60 entries, 12 h refresh; clinic closure roles; reminder SMS fallback | Approval of those defaults | Built with the proposed values, each logged | D-52 to D-67 |
| F4 | Accept the residual risk that an offline lost phone shows its backup reminders until it reconnects or 72 h pass | S-165 sign-off | Wipe push on remote sign-out | D-69 |
| F5 | Rename "India" API fields to "consult country" before the first app release | API freeze | Behaviour already pack-driven | D-72 |
| F6 | Staff sign-in method (SSO + phishing-resistant factor) | S-128, S-172 screens, staff-role attack tests in S-181 | Staff roles fail closed; support lookup API | D-21, D-57 |
| F9 | **Fakes in staging?** Adapter rule A10 says staging uses "the fake or the vendor's sandbox", but the vendor register and `TestFakeAdaptersOnlyInLocalTestDev` allow fakes only in local, test and dev. Allow the SMS, payment, video, push, e-mail, e-sign and geocoding fakes in staging (synthetic data only) until vendor sandboxes exist, or wait for the sandboxes? Claude will not loosen the test itself (CLAUDE.md section 5.5) | Staging, and with it every story's Done (K-DOD-01) | KMS and S3 adapters built (D-93) | A10, K-DP-08 |
| F8 | **Review moderation vs K-SEC-08 (S-213):** moderators are staff reading a patient's unpublished review text; K-SEC-08 allows staff access to patient data only through just-in-time grants (per patient, approved). Options: (a) a moderation grant type covering the queue for a shift, approved and audited; (b) treat text the patient submitted for publication as outside K-SEC-08 [needs an ADR]; (c) automatic checks only, no human reading [policy change] | S-213, review text publication | Ratings publish; text held sealed (D-81) | K-SEC-08, D-81 |
| F7 | New [CONFIRM] items from S-178/S-185: erasure-journal retention; strictness of the link rule for names ("Dr.Kumar" refused); residual one-minute window if a journal write fails just before a restore point | Approval | Built with the proposed behaviour | D-74, D-77 |

## 2. Doctor, lawyer and CA reviews

| # | Blocker | Who | Blocks | Ref |
|---|---|---|---|---|
| R1 | Drug table (empty, so every item is blocked) and telemedicine rules v1.1.0-draft | Registered doctor (K-CS-07), then lawyer | Prescribing in India (S-150) | docs/clinical/ |
| R2 | Electronic signature method under the IT Act (N5) | Lawyer | Signing in India (S-150, P09) | C12 |
| R3 | Tax file (GST treatment, receipts, numbering) | CA | Patient receipts in production; our GST invoices (S-134) | D-56, D-61 |
| R4 | [VALIDATE] items: retention periods, branch registration, notice texts and languages, grievance timings | Lawyer | Compliance sign-off (S-177) | data-inventory, D-63 |
| R5 | In-person identity confirmation and follow-up/refill facts | Doctor reviewer | In-person prescribing, follow-up rules | D-39, D-41 |

## 3. Vendors and infrastructure

| # | Blocker | Blocks | Ref |
|---|---|---|---|
| V1 | AWS India accounts for staging, production and DR | Every story's Done (K-DOD-01: deployed to staging), S-179 ZAP, S-184 and S-185 drills | ADR §6 |
| V2 | Vendors with processor agreements in the vendor register: SMS (DLT registration), push, email, payment aggregator, video, e-signature, malware scan, geocoding, doctor-register check | Real delivery; S-111; adapter kill switches and circuit breakers (A9, D-79); a DLT SMS template for erasure notices (TM-33) | K-DP-08, K-ARC-03 |
| V3 | ~~Cloud key management and S3-compatible storage adapters~~ **Built 2026-11-23 (D-93)**; remaining: verify on staging, DR replica key | Real patient data | encryption-design |
| V4 | PDF library ADR | Prescription and receipt PDFs (S-151, S-134) | K-DOD-03 |
| V5 | Independent CERT-In empanelled VAPT | S-183, launch | K-SEC-07 |

## 4. On hold or not in v1.0

WhatsApp (S-167, K-MSG-02); config screen (S-173, Won't Do); children (K-DP-10, v1.1); app screens and web (frontend planned later).
