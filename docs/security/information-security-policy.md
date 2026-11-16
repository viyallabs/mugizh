# Information security policy and programme

**Added:** 2026-11-16 (compliance review R-04) · **Owner:** security owner (founder until a security lead is hired) · **Status:** DRAFT. **Must be approved by the founder before the v0 pilot starts**, because v0 handles health data. **Review:** every year, after any major change, and after every serious incident.

**Why this exists:**
- **SPDI Rules 2011, rule 8:** these apply to health data until the DPDP Act replaces them (expected 13 May 2027). We are treated as having reasonable security practices if we have a **comprehensive documented information-security programme and policies**, with managerial, technical, operational and physical controls matching the data we protect. Certification to IS/ISO/IEC 27001, or an audit against an approved code, gives deemed compliance [VALIDATE].
- **DPDP Rules, rule 6:** sets out the "reasonable security safeguards" that apply from 13 May 2027.

**This policy is short on purpose.** It sets the rules and roles, and points to the design that carries out each control. If a design document and this policy disagree, the stricter one applies until fixed.

## 1. Scope

All systems, people and vendors that handle the personal or health data of patients, doctors, clinic staff or prospects:
- the v0 pilot tools;
- the waitlist;
- the apps, APIs and admin console;
- cloud accounts;
- staff laptops and phones;
- processors.

## 2. Roles

| Role | Responsible for |
|---|---|
| **Founder** | Approves this policy and risk acceptance; names the roles below |
| **Security owner** | Runs the programme: risk register, access reviews, incidents, audits, training |
| **Grievance Officer** | Data-rights requests and complaints (named before v0) |
| **CERT-In point of contact** | Reporting incidents within 6 hours (named before v0) |
| **Clinical reviewer** | Approves clinical rules and wording (K-CS-07) |
| Every team member and contractor | Follows this policy; reports incidents at once |

## 3. Controls

| Area | Rule | Carried out by |
|---|---|---|
| **Risk management** | A risk register based on the threat model, reviewed every quarter and at each release gate | [threat-model-v1.0.md](threat-model-v1.0.md) |
| **Data classification** | Every field has a class (PUB, INT, PER, PER-F, PER-K, SH-L, SH-C); handling follows the class | [data inventory §4.0](../data-inventory-v1.0.md) |
| **Access control** | Least privilege; deny by default; MFA for all staff and professionals; no standing staff access to patient data; just-in-time access with reason and approval; monthly access review; leavers removed the same day | [access-control-design.md](access-control-design.md) |
| **Cryptography** | TLS 1.2+; field-level encryption with per-patient keys; keys in KMS; no secrets in code | [encryption-design.md](encryption-design.md) |
| **Logging and monitoring** | Audit and security logs for at least 1 year in India; no personal data in logs; alerts; NTP sync | [audit-logging-design.md](audit-logging-design.md) |
| **Secure development** | Story and review templates; contracts; tests for every Critical and High edge case; SAST, secret and dependency scans block merges; independent VAPT before real patient data | [CLAUDE.md](../../CLAUDE.md), [contracts](../../contracts/README.md), [certification-plan.md](certification-plan.md) |
| **Operations** | Infrastructure as code; separate environments; **no real data outside production**; change control by reviewed PR | [infrastructure-and-environments.md](../arch/infrastructure-and-environments.md), [release-and-versioning.md](../process/release-and-versioning.md) |
| **Suppliers** | No vendor gets personal data without a processor agreement, India processing and an entry in the vendor register; reviewed yearly | Data inventory §5; adapter rule A3 |
| **Incident management** | Runbook with the CERT-In 6-hour clock and DPDP notices; drills | [incident-response-runbook.md](incident-response-runbook.md), [breach-response-plan.md](breach-response-plan.md) |
| **Business continuity** | Backups in India; restore tests monthly; DR drill twice a year; erasures replayed on restore | [backup-and-restore.md](backup-and-restore.md) |
| **Privacy** | Notices, consent, data-rights flows, retention schedule | [docs/privacy/](../privacy/README.md), [data-rights-flows.md](data-rights-flows.md) |
| **Lawful requests** | Government, police and court requests handled only through the procedure | [lawful-requests-procedure.md](../legal/lawful-requests-procedure.md) |

## 4. People and physical security

| Rule | Detail |
|---|---|
| Confidentiality | Every employee and contractor signs a confidentiality agreement (including patient confidentiality) before access |
| Training | Security and privacy training on joining, then yearly; it covers phishing, Aadhaar rules and health-data handling |
| Laptops and phones | Full-disk encryption, screen lock (5 min), current operating system, device management where possible; **no patient data stored locally**; lost devices reported at once |
| Accounts | Work accounts only; no shared accounts; password manager; phishing-resistant MFA for admin and cloud |
| Paper (v0 consent forms) | Kept in a locked cabinet; scanned into the India-hosted v0 store; paper destroyed per RET-14 after scanning |
| Physical hosting | Cloud provider data centres in India (the provider's own certifications cover physical security) |

## 5. v0 pilot (from late November 2026)

The v0 pilot handles real prescriptions before the full platform exists. **Before it starts:**
1. This policy is approved; the Grievance Officer and CERT-In point of contact are named.
2. v0 tools are **India-hosted**, with access limited to the named team members, MFA on, and access logged (open decision 5).
3. Paper consent forms are in place (SPDI written consent, N1).
4. The tabletop drill (breach plan §6) is done.
5. Data is deleted 30 days after the pilot ends (RET-14). Logs are kept as CERT-In requires (180 days minimum).

## 6. Exceptions and enforcement

- **Exceptions** need the security owner's written approval, with an end date, and are listed in the risk register.
- **Breaking this policy** is a disciplinary matter for staff, and grounds for ending contracts with contractors and vendors.

## 7. Evidence kept (for audits and complaints)

- Approvals of this policy.
- Risk register.
- Access reviews.
- Training records.
- Scan and VAPT reports.
- Drill reports.
- Incident records.
- Vendor agreements.

These are kept for the periods in the data inventory (security records 3 years [CONFIRM]).
