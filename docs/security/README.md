# Security (phase P06, epic EP08)

| File | Story | Status | What it is |
|---|---|---|---|
| [incident-response-runbook.md](incident-response-runbook.md) | S-020 | In Testing | CERT-In 6-hour runbook, on-call, templates |
| [threat-model-v1.0.md](threat-model-v1.0.md) | S-043 | Done | STRIDE threats TM-01 to TM-51 with owners; edge cases E-SEC-01 to E-SEC-12 |
| [certification-plan.md](certification-plan.md) | S-051 | In Testing (auditor quotes) | VAPT, ABDM WASA and Safe-to-Host, ISO 27001 / SOC 2 timing |
| [encryption-design.md](encryption-design.md) | S-056 | In Testing | Keys (patient, care, service), key store, crypto-shredding, rotation, fail closed |
| [consent-design.md](consent-design.md) | S-057 | In Testing | Purpose consent vs access permission (T1–T4), the `records.authorize` gate |
| [access-control-design.md](access-control-design.md) | S-058 | In Testing | Roles, full v1.0 permission matrix, JIT staff access, sessions |
| [audit-logging-design.md](audit-logging-design.md) | S-059 | In Testing | Audit vs security logs, never-logged list, tamper evidence |
| [data-rights-flows.md](data-rights-flows.md) | S-060 | In Testing (lawyer) | Access/export, correction, erasure orchestration, nominee |
| [mobile-data-protection.md](mobile-data-protection.md) | S-061 | In Testing | Device storage, app lock, screenshots, rooted devices, pinning |
| [backup-and-restore.md](backup-and-restore.md) | S-062 | In Testing | Backups, deletion reaching backups, restore procedure and tests |
| [data-protection-edge-case-review.md](data-protection-edge-case-review.md) | S-063 | Done | E-DP-01 to E-DP-27 against the designs (25 verified, 2 changes logged) |
| [breach-response-plan.md](breach-response-plan.md) | S-064 | In Testing (drill dates) | DPDP rule 7 notices, vendor and clinical-safety branches, drills |
| [information-security-policy.md](information-security-policy.md) | Review R-04 | DRAFT (approve before v0) | The documented security programme the SPDI Rules (rule 8) expect |

The rules these designs meet are in `/contracts/compliance/` (K-DP, K-SEC).
