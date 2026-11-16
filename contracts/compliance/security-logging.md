# Contract: Security and logging (K-SEC)

**Story:** S-074 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-SEC-01

**Rule:** All system logs kept securely for at least 180 days in India; clocks synced to NTP.

| Why | How it is enforced | Applies to |
|---|---|---|
| C07 | IaC config check | infra |

*Implementation notes (not part of the rule):* Our standard is 1 year in India for both audit and security logs (RET-06, RET-07), which meets CERT-In (180 days) and DPDP Rules rule 6(1)(e) (1 year). NTP: servers of NIC or NPL or traceable to them. Proposed rule change below.

## K-SEC-02

**Rule:** Incident runbook: detect → triage → CERT-In report within 6 hours → DPDP notifications.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03, C07 | Drills | ops |

*Implementation notes (not part of the rule):* docs/security/incident-response-runbook.md (S-020) and docs/security/breach-response-plan.md (S-064): DPDP rule 7 notice contents, vendor and clinical-safety branches, drills.

## K-SEC-03

**Rule:** Patients: OTP + device binding. Doctors, nurses, admins: mandatory MFA; step-up to sign prescriptions; session timeouts.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | Tests | identity |

*Implementation notes (not part of the rule):* Doctor and staff MFA with an authenticator app or passkey, not SMS-only; signing also needs the electronic signature (P09); session limits in docs/security/access-control-design.md section 6.

## K-SEC-04

**Rule:** Deny-by-default role checks plus ownership checks on every endpoint (no access to other users' records by changing IDs).

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | Automated authz suite | All APIs |

*Implementation notes (not part of the rule):* docs/security/access-control-design.md sections 3-4 and 7: others' resources return 404; the authorisation suite is generated from the OpenAPI `x-roles` / `x-ownership` fields.

## K-SEC-05

**Rule:** No secrets in code or config files; secret scanning blocks merge.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | CI secret scan | All |

*Implementation notes (not part of the rule):* Secrets manager only; any leaked secret is rotated at once.

## K-SEC-06

**Rule:** Dependencies pinned and scanned; Critical vulnerabilities block release.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | CI dependency scan | All |

*Implementation notes (not part of the rule):* Pinned dependencies; SDK allow-list for mobile (no analytics, ads or attribution SDKs).

## K-SEC-07

**Rule:** OWASP ASVS Level 2 (web/API) and MASVS (mobile) as the baseline.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | Security testing | All |

*Implementation notes (not part of the rule):* Mobile at MASVS v2 MAS-L2 (docs/security/mobile-data-protection.md); independent VAPT before real patient data (S-183, docs/security/certification-plan.md).

## K-SEC-08

**Rule:** Internal access to patient data is time-limited, reason-coded, approved and audited; break-glass raises an alert.

| Why | How it is enforced | Applies to |
|---|---|---|
| C01, C03 | Admin console + alerts | admin |

*Implementation notes (not part of the rule):* docs/security/access-control-design.md section 5: reason codes, approver never the requester, at most 4 hours, break-glass alert, monthly review.

## Proposed changes (each needs an ADR approved by the founder)

- **K-SEC-01:** change "at least 180 days" to "at least 1 year" (DPDP Rules rule 6(1)(e); RET-07; founder decision from S-020).
- **K-SEC-03:** add "and an electronic signature on the prescription (P09; method decided under N5)".

