# CERT-In readiness: incident response runbook (S-020) — DRAFT

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C07 (plus C03) · **Contracts:** K-SEC-01, K-SEC-02 · **Edge case:** E-DP-24 · **Feeds:** S-064 (breach plan and drill), S-059 (audit log), S-074 (security-logging contract), S-095 (observability)
**Date:** 2026-11-15 · **Not legal advice.** **[VALIDATE]** = check with the lawyer or the primary text. **[FILL]** = detail we must supply. **[CONFIRM]** = founder decision.

**Who this is for:** whoever is on call when something goes wrong, at 2 am, possibly alone. The steps are short on purpose. The founder must name the point of contact (acceptance criterion).

## 1. Duties and clocks

| Duty | Rule | Applies from | Source |
|---|---|---|---|
| **Report listed cyber incidents to CERT-In within 6 hours** of noticing them, or of being told about them | CERT-In Directions, 28 Apr 2022, direction (ii) | **Now** (any live system, including v0 tools and the waitlist page) | C07; [PSA Legal overview](https://psalegal.com/new-cert-in-directions-overview-and-implications/) |
| Name a **point of contact** and send the details to CERT-In in its prescribed format | Directions (iii) [VALIDATE format and address] | Now | C07 |
| Keep ICT system logs securely for a **rolling 180 days in India**, and give them to CERT-In on request | Directions (iv) | Now | C07 |
| **Sync all system clocks** to NTP servers of NIC or NPL, or to servers traceable to them | Directions (i) | Now | C07 |
| Personal data breach: **tell the Data Protection Board without delay**, then send a **detailed report within 72 hours** (longer only if the Board allows it in writing) | DPDP Rules 2025, rule 7 | **About 13 May 2027** | C03; [DSCI brief](https://www.dsci.in/files/content/knowledge-centre/2025/Insight-Brief-on-DPDP-Rules.pdf), [Mondaq](https://www.mondaq.com/india/data-protection/1709066/key-highlights-of-the-digital-personal-data-protection-rules-2025) |
| Tell **each affected person** without delay: what happened, the likely impact, what we are doing, what they can do, and who to contact | DPDP Rules 2025, rule 7 | About 13 May 2027 | Same |
| Keep logs and related personal data for **at least 1 year** | DPDP Rules 2025, rule 6 (sub-rule numbering differs between sources) [VALIDATE] | About 13 May 2027 | Same |
| Until May 2027: reasonable security practices for health data | IT Act s.43A and SPDI Rules 2011 | Now | C01 |

**Our standard:** we meet the strictest rule now. **Logs are kept 1 year in India from day one**, which covers both 180 days and 1 year (RET-07 in [data-inventory-v1.0.md](../data-inventory-v1.0.md)). K-SEC-01 and ADR-21 say 180 days and need an ADR to move to 1 year (S-059).

**What CERT-In wants reported (Annex I of the Directions, summarised) [VALIDATE the full list]:**
- Targeted scanning or probing of our systems.
- Compromise of, or unauthorised access to, systems or data.
- Website defacement or intrusion.
- Malicious code (ransomware, malware).
- Attacks on servers such as databases, mail or DNS, and on network devices.
- Identity theft, spoofing or phishing.
- Denial of service (DoS or DDoS).
- Attacks on applications, including e-commerce.
- **Data breach and data leak.**
- Attacks affecting digital payments.
- Malicious or fake mobile apps.
- Unauthorised access to our social media accounts.
- Attacks on cloud systems.
- Attacks on AI or machine-learning systems.

**If in doubt, report.** A first report can be partial and updated later.

## 2. Point of contact and team [FILL]

| Role | Person | Phone (24×7) | Backup |
|---|---|---|---|
| **CERT-In point of contact** (also leads incidents until a security lead is hired) | [Founder name] **[CONFIRM: founder or named engineer]** | [FILL] | [FILL] |
| Technical lead (contain and investigate) | [FILL] | [FILL] | [FILL] |
| Communications and legal (users, Board, partners) | [Founder] plus lawyer [FILL, S-009] | [FILL] | — |
| Grievance Officer (user questions after notice) | [FILL; S-010 open decision 1] | — | — |

**CERT-In contacts (check before v0) [VALIDATE]:** incident reports to `incident@cert-in.org.in`, using the incident reporting form on cert-in.org.in. Phone and fax as listed on that site. Copy the current numbers here: [FILL].

**Vendor contacts:** cloud provider, payment aggregator, SMS provider, video provider, and email or workspace provider. Each processor agreement must make the vendor tell us of an incident **within 6 hours** so we can meet our own clock (E-DP-17; K-DP-08) [CONFIRM clause].

## 3. The runbook (T0 = the moment anyone on the team notices or is told)

| Step | When | Do this | Output |
|---|---|---|---|
| **1. Alert** | T0 | Anyone who sees a possible incident pages the on-call person (phone call, not chat). Write down T0. | Incident ID, T0 |
| **2. Triage** | by T0 + 30 min | Is it real? Which systems? Is personal or health data involved? Is it in the Annex I list? Is it still happening? **Severity:** SEV1 = health data exposed or service down; SEV2 = other personal data or a contained compromise; SEV3 = attempt with no impact. | Severity, reportable yes/no |
| **3. Contain** | at once, in parallel | Revoke keys and sessions, block IPs, isolate the host, rotate secrets, disable the affected feature (kill switch). **Keep evidence:** snapshot before wiping, keep logs, do not reboot unless needed. | Containment log |
| **4. Report to CERT-In** | **by T0 + 6 h** | Point of contact sends the CERT-In form (template A). Partial facts are fine; add "update to follow". | Sent email and reference |
| **5. DPDP notices** (personal data breach, from May 2027; earlier if the lawyer advises) | Board: without delay. Detailed report: **by T0 + 72 h**. Users: without delay. | Template B to the Board, template C to users (in app, SMS and email, discreet: no health details in SMS). | Copies saved in the incident record |
| **6. Partners** | within 24 h | Tell affected clinics, the payment aggregator if payments are involved, and others as contracts require. | — |
| **7. Recover** | after containment | Fix the root cause, restore from clean backups, monitor closely for 14 days. | — |
| **8. Review** | within 5 working days | Blameless review: timeline, cause, what worked, actions with owners. Update this runbook. | Review document |

**Timeline log:** every action is written down with time (IST) and person, in the incident record. CERT-In and the Board may ask for it.

## 4. On-call (E-DP-24: breach found at 2 am on a holiday)

- **v0:** the founder is on call 24×7 with a named backup. Alerts from the waitlist host and workspace go to the founder's phone.
- **v1.0:** a weekly rota of at least 2 people, a paging tool with phone-call escalation (no answer in 10 minutes → backup → founder), and an on-call handbook linking this runbook.
- **Templates A to C are pre-filled** with company details, so a single person can send a report within 6 hours.
- **Quarterly drill** from v1.0, plus one tabletop exercise **before v0 starts** (S-064).

## 5. Logging and time sync (K-SEC-01; built in S-059, S-095)

| Item | Standard |
|---|---|
| What we log | Sign-in and MFA events, admin and staff access to patient data (K-SEC-08), API gateway and WAF, database audit, cloud control-plane logs, deployment and CI events, payment webhooks, DNS and email security |
| What we never log | Health data, full identifiers, card data, OTPs, Aadhaar numbers (K-DP-04, K-PAY-01, AAD-03) |
| Where | Central log store in an Indian cloud region, encrypted, write-once for at least 1 year, access restricted and audited |
| Retention | **1 year rolling** (RET-07). Longer only under a legal hold. |
| Clocks | All servers and containers use the cloud provider's time service, traceable to NIC or NPL, or directly `samay1.nic.in` or `time.nplindia.org` [VALIDATE hostnames]. Timestamps in UTC with offset. Alert if drift exceeds 1 second. |
| v0 tools | For each SaaS tool holding v0 data (forms, sheets, email), confirm audit logs are on and kept 180 days or more, or export them monthly [CONFIRM per tool] |

## 6. Templates

**A. CERT-In incident report** (fields from CERT-In's form [VALIDATE]):
- Organisation name, sector (healthcare), point of contact.
- Incident type (Annex I).
- Time it happened, if known, and time noticed (T0).
- Systems affected: IP addresses, domains, cloud region.
- Description and indicators: IPs, hashes, URLs.
- Impact: data, users, service.
- Actions taken.
- Whether it is still ongoing.
- Contact for follow-up.

**B. Data Protection Board intimation** (rule 7):
- *First notice:* what happened, its nature, extent, time and location, and the likely impact on people.
- *Detailed report (72 h):* the facts and circumstances, the cause, mitigation, who caused it if known, remedial steps, and the notices sent to people.

**C. Notice to affected users** (see [data-rights-and-grievance.md](../privacy/data-rights-and-grievance.md) §5): what happened, the data involved, possible consequences, what we did, what you can do (for example change your PIN, watch for calls claiming to be us), and the Grievance Officer contact. Plain English, reading age checked.

## 7. Before v0 starts (C07 is due at v0)

- [ ] Founder names the point of contact and backup (section 2).
- [ ] Point of contact details sent to CERT-In in its format [VALIDATE].
- [ ] CERT-In contacts checked and copied into section 2.
- [ ] Inventory of v0 systems and tools, with logging status (section 5).
- [ ] Templates A to C pre-filled with company details.
- [ ] One 30-minute tabletop drill: "the concierge sheet was shared publicly by mistake".

## 8. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Runbook written | **Yes** | Sections 1 to 6 |
| Point of contact named | **No, pending** | Founder to fill section 2 |

**Story status:** In Testing.

## 9. Decisions and questions

1. **[Founder]** Who is the CERT-In point of contact and who is the backup?
2. **[Founder]** Approve 1-year log retention now (meets CERT-In and DPDP), so K-SEC-01 and ADR-21 change through S-059.
3. **[Lawyer]** Before May 2027, should we also give DPDP-style notices to users and the Board as best practice, or only report to CERT-In?
4. **[Lawyer]** Confirm the vendor 6-hour notification clause for processor agreements.
