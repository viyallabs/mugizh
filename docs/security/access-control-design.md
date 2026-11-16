# Access control design (S-058)

**Phase P06 · Epic EP08 · Depends on:** S-057 ([consent-design.md](consent-design.md)) · **Contract refs:** K-SEC-04, K-SEC-08 (also K-SEC-03, K-DP-02, K-CS-01) · **Edge case:** E-DP-16 (also E-DP-14, E-SEC-03) · **Compliance:** C01 · **Date:** 2026-11-15
**Status:** In Testing. The acceptance criterion is "permission matrix (Roles sheet) signed off". **This document is the full v1.0 matrix;** the Roles sheet was corrected to match and points here. Founder sign-off pending.

**Who this is for:** the backend lead (policy layer S-106, authorisation tests S-116 and S-181), the Admin console builder (S-128), and reviewers.

## 1. Principles

1. **Deny by default.** An endpoint with no declared policy is unreachable (API standards: `x-roles` required; lint).
2. **Role and ownership, every time.** Having the right role is not enough. The resource must belong to the caller, their clinic, or a patient who granted access (tiers T1 to T4). Others' resources return 404 (E-SEC-03).
3. **No standing access to patient data for staff.** Only just-in-time, reason-coded, time-boxed and audited access (K-SEC-08).
4. **Separation of duties.** No one approves their own access, their own refund, or their own verification override.
5. **The policy lives in one place.** A policy module evaluated by the API and workers alike. Its rules are generated into the authorisation test suite, so every operation is tested with every role (S-116).

## 2. Roles

| Role | Type | v1.0 | Sign-in |
|---|---|---|---|
| Patient | Primary | Yes | OTP + device binding |
| Doctor | Primary | Yes | OTP + **MFA** (authenticator or passkey); step-up to sign |
| Nurse | Primary | **Reserved** (v4.0, S-031) | — |
| Clinic admin | Supporting | Yes | OTP + MFA |
| **Front desk** (clinic staff) | Supporting | Yes | OTP + MFA |
| Caregiver | Linked to patient | v1.1 | OTP + device binding |
| Lab partner, pharmacy partner | Supporting | v2.0, v3.0 | — |
| Verification reviewer | Internal | Yes | SSO + phishing-resistant MFA |
| Support | Internal | Yes | SSO + phishing-resistant MFA |
| **Grievance Officer** | Internal | Yes | SSO + phishing-resistant MFA |
| **Security owner / access approver** | Internal | Yes | SSO + hardware key |
| Ops (payments exceptions) | Internal | Yes | SSO + phishing-resistant MFA |

A person can hold more than one role (for example a doctor who is also the clinic admin). Each permission is checked against the role used for that action.

## 3. Permission matrix (v1.0)

Legend:
- **Own** = their own data.
- **T1 to T4** = access tiers ([consent-texts.md](../privacy/consent-texts.md) A2).
- **Clinic** = resources of their clinic.
- **JIT** = just-in-time grant (section 5).
- **—** = never.

| Action | Patient | Doctor | Clinic admin | Front desk | Reviewer | Support | Grievance Officer | Security owner | Ops |
|---|---|---|---|---|---|---|---|---|---|
| Book, pay, cancel an appointment | Own | — | Clinic (on patient's behalf at the desk) | Clinic | — | — | — | — | — |
| View booking fields (name, time, mode, status) | Own | T1 (own bookings) | Clinic | Clinic | — | JIT (no clinical data) | JIT | — | JIT (payment-linked) |
| Mark arrived, no-show | — | Own bookings | Clinic | Clinic | — | — | — | — | — |
| View own health record | Own | — | — | — | — | — | — | — | — |
| View a patient's clinical data | Own | **T1 current consult; T2 own records; T3/T4 only via P-SHARE** | — | — | — | — | JIT, only for a complaint or rights request, with a reason | Break-glass only | — |
| Write notes, compose prescription | — | T1, **if Verified** (K-CS-01) | — | — | — | — | — | — | — |
| **Sign** a prescription | — | T1, if Verified, **step-up (P08) + e-signature (P09)** | — | — | — | — | — | — | — |
| Amend a signed prescription (new version) | — | Own prescriptions, step-up | — | — | — | — | — | — | — |
| Manage availability | — | Own | Clinic doctors | View | — | — | — | — | — |
| Edit public profile | — | Own | Clinic | — | — | — | — | — | — |
| **Manage clinic staff** (invite, role, remove) | — | — | Clinic | — | — | — | — | — | — |
| View payments and refunds | Own | Own consults | Clinic | — | — | JIT | JIT | — | Yes |
| Issue a refund | Request (policy applies automatically) | — | Request | — | — | **Policy refunds only** | Policy refunds only | — | **Manual refund needs a second approver** (TM-12) |
| Approve verification | — | — | — | — | Yes; **second reviewer for overriding a failed check** | — | — | — | — |
| Handle complaints and data-rights requests | Request own | — | — | — | — | Triage (no clinical data) | Yes | — | — |
| Approve just-in-time access | — | — | — | — | — | — | — | **Yes, never their own** | — |
| View audit logs | Own access log | — | — | — | — | — | Entries for the case being handled (JIT) | Yes; **reads of the audit log are audited** | — |
| Change country pack or config | — | — | — | — | — | — | — | **No runtime changes:** reviewed PR only (clinical reviewer, founder, CA approvals) | — |
| Export own data; close account | Own (step-up) | Own account (step-up) | Own account | Own account | — | — | — | — | — |

**Front desk never sees clinical notes, prescriptions or shared history.** Their view is limited to booking fields (provider privacy notice §1a).

## 4. Ownership rules (what "Own" and "Clinic" mean)

| Resource | Owner | Also allowed |
|---|---|---|
| Account, profile, consents, uploads | Patient | — |
| Appointment | Patient | Doctor and clinic of the booking (T1 for clinical parts) |
| Clinical note, prescription | Patient (subject) | The authoring doctor and clinic (T2); others via T3/T4 |
| Availability, profile | Doctor | Clinic admin of a clinic where the doctor practises |
| Clinic settings, staff, payments | Clinic | Clinic admins of that clinic |
| Verification case | The doctor (read status) | Reviewers |
| Complaint, rights request | Requester | Grievance Officer |

**When a doctor leaves a clinic (E-DP-14):**
- **The clinic's admins lose access to that doctor's schedule.**
- **The doctor keeps T2 access to their own records** while RET-02 runs.
- **The clinic keeps records it must hold legally.**
- **Patient-granted T3 grants to that doctor at that clinic are revoked,** and the patient is told [VALIDATE who holds the record duty, linked to the fiduciary question].

## 5. Just-in-time access for staff (K-SEC-08, E-DP-16)

```mermaid
sequenceDiagram
  participant S as Staff member
  participant A as Admin console
  participant P as Approver (security owner)
  participant R as records.authorize
  S->>A: Request access (patient ref, scope, reason code, ticket ref, duration)
  A->>P: Approval request
  P-->>A: Approve (not self) / reject
  A->>R: Create JIT grant (scope, expires_at <= 4 h)
  S->>R: Read within scope
  R-->>S: Allowed + audit event (reason, ticket)
  Note over R: Grant expires automatically; reads after that are denied
```

- **Reason codes:** complaint (ticket), rights request, payment dispute, security incident, legal order. **No free-form "other"** [CONFIRM].
- **Scope:** one patient, and the smallest data kind needed. **Booking and payment data is the default scope. Clinical data only for complaints, rights requests or legal orders.**
- **Duration:** at most 4 hours [CONFIRM]; renewal needs a new approval.
- **Break-glass:** for a live security incident only. Access is immediate without approval, but it **alerts** the founder and security owner and needs a written justification within 24 hours.
- **Anomaly alerts:** more than N patients in a day for one staff member, access outside working hours, or access without a linked ticket.
- **Monthly access review** by the security owner. Evidence kept for the audit (S-051).

## 6. Sessions by role

| Role | Session | Re-authentication |
|---|---|---|
| Patient | Access token ~15 min; device-bound refresh token; app lock optional | Step-up for export, erasure, phone change, new device |
| Doctor, clinic staff (Pro web) | Idle timeout **30 min** [CONFIRM]; absolute timeout 12 h | Step-up to sign a prescription (P08); MFA on every new sign-in |
| Internal staff | Idle 15 min; absolute 8 h | Hardware key for approvers |

## 7. How it is enforced and tested

1. Every OpenAPI operation declares `x-roles`, `x-ownership` and `x-consent` (API standards §8; lint rule `roles-declared`).
2. The policy module evaluates role → ownership → tier (records.authorize) → purpose.
3. **The authorisation test suite is generated from the spec.** For every operation × every role × (own, other's, no grant), it checks the expected allow or deny. Wrong-ID cases must return 404 (S-116, TC-57).
4. S-181 attacks authorisation and consent by hand: role escalation, IDOR, consent bypass.
5. Admin-console actions all produce audit events. The audit viewer itself is audited.

## 8. Changes made to the Roles sheet

- **"View a patient's history":** doctor → T1/T2/T3-T4 wording. Support → booking and payment data only (JIT, no clinical data). Admin → JIT with reason, audited.
- **"Issue refunds":** support → policy refunds only; admin → manual refunds need a second approver.
- **"View audit logs":** admin → security owner only; audit-log reads are audited.
- **"Change country pack / config":** no runtime changes, reviewed PR only.
- **New rows:** sign prescription; manage clinic staff; approve JIT access.
- **A note** that front desk, Grievance Officer and security owner columns are in this document.

## 9. Decisions [CONFIRM]

1. JIT maximum duration of 4 hours; reason codes without free-form "other".
2. Pro web idle timeout of 30 minutes.
3. Support never gets clinical data, even with JIT. Only the Grievance Officer does, for a case.
