# Data-protection edge cases checked against the design (S-063)

**Phase P06 · Epic EP08 · Depends on:** S-056 to S-062 · **Contract refs:** K-DP-* · **Edge cases:** E-DP-01 to E-DP-27 · **Compliance:** C01, C03 · **Date:** 2026-11-15
**Status:** Done (acceptance criterion: every E-DP is "Design verified", or a design change is logged). Results are written in the Edge Cases sheet "Status" and "Notes" columns.

**Who this is for:** the security owner and reviewers. "Design verified" means the design documents describe a complete answer; it becomes "Test passing" only when the TC test passes in its story.

**Designs checked:**
- [encryption-design.md](encryption-design.md) (ENC)
- [consent-design.md](consent-design.md) (CON)
- [access-control-design.md](access-control-design.md) (ACC)
- [audit-logging-design.md](audit-logging-design.md) (AUD)
- [data-rights-flows.md](data-rights-flows.md) (DR)
- [mobile-data-protection.md](mobile-data-protection.md) (MOB)
- [backup-and-restore.md](backup-and-restore.md) (BAK)
- [threat-model-v1.0.md](threat-model-v1.0.md) (TM)
- data inventory §4.0 classification (CLS)
- the Aadhaar policy (AAD)
- the children's policy (CH)
- the incident runbook (IR)

## Results

| E-ID | Scenario (short) | Result | Where the design answers it | Open point |
|---|---|---|---|---|
| E-DP-01 | Revoke while the doctor has the record open | **Design verified** | CON §3, §4, §7 (synchronous revoke, view cleared within 30 s) | 30 s [CONFIRM] |
| E-DP-02 | Sharing expires mid-consult | **Design verified** | CON §7 (warning, extension request, no silent access) | — |
| E-DP-03 | Deletion vs legal retention | **Design verified** | DR §5; ENC §3 (care keys) | Fiduciary question (lawyer) |
| E-DP-04 | Erased data in backups | **Design verified** | ENC §4; BAK §2, §3 (crypto-shredding, 35-day key-store backups, replay on restore). **A flaw was found and fixed** in S-056. | — |
| E-DP-05 | Key rotation during reads and writes | **Design verified** | ENC §6 | — |
| E-DP-06 | Key service unavailable | **Design verified** | ENC §7 (fail closed, 5-min key cache) | — |
| E-DP-07 | Phone lost while signed in | **Design verified** | MOB §1, §2; ACC §6 | — |
| E-DP-08 | Screenshots of health screens | **Design verified** | MOB §3 (FLAG_SECURE; iOS warning and hide while recording) | — |
| E-DP-09 | Shared family phone, several adult profiles (v1.1) | **Design change needed** | **Not designed yet.** Family profiles are v1.1 (S-212). | Add a profile-switch PIN design to the F13 design before v1.1. **Logged.** |
| E-DP-10 | Nominee after death or incapacity | **Design verified** | DR §6 (four-eyes, proof, time-limited grant) | Proof standards (lawyer) |
| E-DP-11 | Minor turns 18 (v1.1) | **Design verified** | CH-10 | v1.1 |
| E-DP-12 | Health details on the lock screen | **Design verified** | CLS handling rules; MOB M3; IA rule N1 | — |
| E-DP-13 | Personal data in logs | **Design verified** | AUD §3 (allow-list logger, scrubber, CI planted-data test) | — |
| E-DP-14 | Doctor leaves a clinic | **Design verified** | ACC §4; CON §7 | Record duty (lawyer, fiduciary question) |
| E-DP-15 | Export with images and PDFs | **Design verified** | DR §3 (encrypted, in-app, single-use 24 h link) | — |
| E-DP-16 | Staff browsing patient records | **Design verified** | ACC §5 (JIT, approver not self, alerts, monthly review) | — |
| E-DP-17 | SMS or other vendor breach | **Design verified** | Adapter rule A4 (minimum data); IR vendor contacts and vendor notification clause | S-064 adds explicit vendor-breach steps |
| E-DP-18 | Caregiver abroad views a parent's data (v1.1) | **Design verified** | Journeys §6: data stays hosted in India, view only | **Lawyer to confirm** that viewing from abroad fits DPDP s.16 (transfers allowed except to restricted countries) |
| E-DP-19 | Sensitive data in search or caches | **Design verified** | ENC §5, §9; CLS rules (no SH-C in indexes, encrypted caches) | — |
| E-DP-20 | Malware or disguised upload | **Design verified** | TM-42 (scan, content-type check, re-encode, separate download domain) | — |
| E-DP-21 | Download link shared or leaked | **Design verified** | ENC §8; S-171 (user-bound, short expiry, single use) | — |
| E-DP-22 | Telco reassigns a number | **Design verified** | TM-03 (no old data until re-verification on a new device) | — |
| E-DP-23 | Patient loses their number | **Design change needed** | Threat model says "identity check beyond OTP", but **the check itself is not defined** | Define the recovery check in S-115. **Logged.** Proposal: verified email OTP plus date of birth plus details of a recent booking; if that fails, a support video call matching a non-Aadhaar photo ID, then the new number is bound and all old sessions are revoked [CONFIRM]. |
| E-DP-24 | Breach at 2 am on a holiday | **Design verified** | IR §4 (on-call); S-064 plan | Drill S-184 |
| E-DP-25 | Analytics SDK collects health screens | **Design verified** | MOB M5 (no third-party SDKs; CI allow-list); CLS rules | No v1.0 analytics story yet (S-053) |
| E-DP-26 | Aadhaar typed into a chat or note | **Design verified** | AAD-02, AAD-03; AUD §3 | — |
| E-DP-27 | Doctor uploads an Aadhaar card | **Design verified** | AAD-04 | Open decision 16 |

## Summary

- **Design verified: 25 of 27.**
- **Design change needed: 2, both logged:**
  - **E-DP-09:** profile-switch PIN, v1.1 F13 design (S-212).
  - **E-DP-23:** account-recovery identity check (S-115), with a proposal above.
- **Lawyer points carried forward:** E-DP-03 and E-DP-14 (fiduciary and record duty), E-DP-10 (nominee proof), E-DP-18 (viewing from abroad).
