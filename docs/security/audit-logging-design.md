# Audit log and security logging design (S-059)

**Phase P06 · Epic EP08 · Depends on:** S-058 · **Contract refs:** K-SEC-01, K-DP-04 (also K-DP-02, K-CS-05, K-SEC-08) · **Edge case:** E-DP-13 (also E-SEC-12) · **Compliance:** C07 (also C03) · **Date:** 2026-11-15
**Status:** In Testing, until the founder approves the design (acceptance criterion). Built in S-096 (audit service) and S-095 (observability).

**Who this is for:** the backend lead, the infra lead and the security owner.

## 1. Two streams, two purposes

| | **Audit log** | **Security and system logs** |
|---|---|---|
| What | Who did what to which record, and why: business and access events | Requests, errors, sign-ins, infrastructure and network events |
| Why | Patients see who read their record (R-83). Evidence for K-DP-02, K-CS-05 and disputes. | Detect and investigate incidents; CERT-In reporting (C07) |
| Integrity | **Append-only, hash-chained, daily anchored** (section 4) | Write-once store; application roles cannot delete |
| Retention | **RET-06:** as long as the related record (RET-02), and **at least 1 year** | **RET-07: 1 year rolling** (CERT-In needs 180 days; DPDP rule 6(1)(e) needs 1 year) |
| Location | India (primary + DR) | India (residency check R3) |
| Contains personal data? | References only (pseudonymous IDs), no content | **No personal or health data**, apart from pseudonymous IDs and IP addresses (S-044) |

**The story title says "180+ days".** Both streams keep **at least 1 year**, which meets CERT-In's 180 days and the DPDP Rules' 1 year. **K-SEC-01 (180 days) needs an ADR to change to 1 year**, together with S-095 and ADR-21 (founder decision from S-020).

## 2. What the audit log records

Every event has these fields: `event_id`, `occurred_at` (UTC, NTP-synced), `actor_ref`, `actor_role`, `action`, `subject_ref` (patient or doctor), `resource_type`, `resource_ref`, `purpose` or `tier`, `grant_id`, `reason_code`/`ticket_ref` (staff), `result` (allowed / denied / failed), `client` (app and version), `ip_hash`, `request_id`, plus the chain fields (section 4).

| Category | Events |
|---|---|
| Access (K-DP-02) | Every **read** of patient data by anyone other than the patient: tier, grant and data kind. Every denial. |
| Consent | Grant, withdraw, permission created, extended, expired, revoked |
| Clinical (K-CS-05) | Note signed; prescription checked (rule IDs, rule-set version), signed (P08/P09), amended, cancelled; rules-engine blocks |
| Booking and money | Hold, confirm, cancel, no-show mark, refund request and decision, manual refund approvals |
| Verification | Submission, each check, decision with reason, overrides, suspensions |
| Staff | JIT request, approval, use and expiry; break-glass; audit-log reads; Admin console actions |
| Data rights | Request, identity check, export generated, erasure executed and its report, restrictions applied |
| Security | Sign-in, MFA set-up, step-up, device bound or revoked, password-less recovery, role changes |
| Config | Pack version loaded, feature-flag changes, rule-file version deployed |

## 3. What is never logged (K-DP-04, E-DP-13)

**In either stream, never:**
- clinical content (notes, diagnoses, drug names, upload contents);
- **full phone numbers or emails** (log a pseudonymous user key instead);
- names in security logs;
- **OTPs, tokens, passwords, MFA secrets, API keys**;
- **Aadhaar numbers** (scrubber pattern, AAD-03);
- card or bank data (K-PAY-01);
- request or response **bodies** of patient endpoints;
- precise patient location;
- SMS template **variables**.

**How it is enforced:**
1. A **structured logging library** with an allow-list of fields per event type. Free-text log messages may not interpolate request data.
2. A **scrubber** on the log pipeline: phone and email patterns, Verhoeff-valid 12-digit numbers, JWT and key patterns, and known health-field names. It replaces them with a marker and counts the hits; any hit raises an alert.
3. **A CI test** plants a phone number, name, diagnosis, OTP and Aadhaar-like number in log calls, and fails the build if any reaches output (TC-10).
4. Crash reporting sends stack traces with no local variables or request data. Breadcrumbs are scrubbed.

## 4. Tamper evidence (E-SEC-12)

1. **Durable first** (ADR-28): events are written to `audit_outbox` in the same transaction as the change, or before a read returns. If that write fails, the read fails.
2. **Chaining:** the audit worker appends each event to a partition (one per day). It computes `hash = SHA-256(prev_hash || canonical_event)` and stores `seq`, `prev_hash` and `hash`.
3. **Daily anchor:** at the end of each day, the partition's final hash is written to **write-once object storage** (object lock) in India, and also emailed to the founder and security owner [CONFIRM].
4. **Grants:** module roles can only INSERT into the outbox. The audit store's writer role can only INSERT. No role can UPDATE or DELETE, except the retention job, which deletes whole expired partitions and records a tombstone with the partition's anchor hash.
5. **Verification job** (daily): recompute the chains and compare them with the anchors. Any mismatch raises a security incident.

## 5. Erasure and the audit log

- Audit events hold **opaque references, not content**.
- When a patient account is erased, the mapping from reference to person is deleted (or restricted under legal retention), so old events become pseudonymous.
- The events themselves stay for RET-06, because they are evidence of who accessed what. The chain is unbroken because nothing inside an event changes.

## 6. Who can read the logs

| Reader | What |
|---|---|
| Patient | Their own access log: reads by people (doctor name, date, purpose), in plain words (S-170) |
| Security owner | Full audit log and security logs; **reads are themselves audited** |
| Grievance Officer | Entries for the case being handled, under JIT |
| Engineers | Security and system logs (no personal data). Audit log only via JIT. |

## 7. Alerts built on the logs

- Break-glass used.
- JIT anomalies (S-058 §5).
- Many denials for one actor (possible IDOR probing).
- Scrubber hits.
- OTP spikes (TM-02).
- Rules-engine blocks at unusual volume for one doctor.
- Audit outbox backlog older than 5 minutes.
- Chain verification failure.
- Admin sign-in from a new place.

## 8. Decisions

1. **[Founder]** Approve 1-year minimum for both streams. This needs an ADR to update K-SEC-01 (180 days) and S-095.
2. **[Founder]** Daily anchor hash emailed to the founder and security owner, as well as stored write-once.
