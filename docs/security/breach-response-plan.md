# Breach response plan: DPDP and CERT-In (S-064)

**Phase P06 · Epic EP08 · Depends on:** S-020 ([incident-response-runbook.md](incident-response-runbook.md)) · **Contract refs:** K-SEC-02 · **Edge case:** E-DP-24 (also E-DP-17) · **Compliance:** C03, C07 · **Date:** 2026-11-15
**Status:** In Testing. The acceptance criterion needs the plan **approved** and a **drill scheduled**; the schedule is proposed in section 6 for the founder to fix dates.

**Who this is for:** the incident team. **The runbook (S-020) is the step-by-step for the night of the incident.** This plan adds what the runbook leaves open: the DPDP notice contents, vendor breaches, patient-safety incidents, evidence and drills. Nothing here changes the runbook's clocks.

## 1. Is it a personal data breach?

**Definition (DPDP s.2(u)):** any unauthorised processing, or accidental disclosure, acquisition, sharing, use, alteration, destruction or loss of access to personal data, that compromises its confidentiality, integrity or availability.

| Situation | Personal data breach? | Notes |
|---|---|---|
| Health data read by someone without permission (outsider or staff) | **Yes** | Also CERT-In reportable (data breach) |
| Database or backup copied, data **field-encrypted and keys safe** | **Yes**, it is still a breach under the definition | Impact on people is low. Whether individual notices are needed is a lawyer call [VALIDATE]. Always tell the Board. |
| Ransomware locks records (loss of access) | **Yes** (availability) | Also CERT-In |
| Wrong patient's prescription shown to another patient (bug) | **Yes** | Also a clinical-safety incident (section 3) |
| An SMS sent to the wrong number with discreet text only | Likely yes (a phone number is personal data); low impact | Record it; lawyer decides on notices |
| Blocked attack, no data touched | No | May still be CERT-In reportable (scanning, attempts) |

**If in doubt, treat it as a breach** and let the lawyer narrow it down. The clocks start at T0 regardless.

## 2. What the DPDP notices must contain (rule 7; duties from about 13 May 2027; follow them earlier as good practice)

| To | When | Must include [VALIDATE against the Gazette text] |
|---|---|---|
| **Each affected person** | Without delay | What happened (nature, extent, timing, location); the likely consequences for them; what we have done to reduce harm; what they can do to protect themselves; who to contact (Grievance Officer) |
| **Data Protection Board**, first notice | Without delay | A description of the breach: nature, extent, timing, location, likely impact |
| **Data Protection Board**, detailed report | **Within 72 hours** of becoming aware (longer only if the Board allows it in writing) | Updated facts; events, circumstances and reasons; mitigation done; findings about who caused it; measures to stop it happening again; a report on the notices given to affected people |

**Templates B (Board) and C (people) in the runbook §6** must carry these headings. SMS-15 is the short, discreet SMS version: it never names a condition, and points to the app or email for detail.

**CERT-In (now):** report within **6 hours** of noticing (runbook step 4). The DPDP notices are **in addition**, not instead.

## 3. Incidents that can harm patients (clinical safety)

Some incidents are not only data problems:
- a tampered rule file;
- a prescription shown to the wrong patient;
- reminders sent with wrong doses;
- a fake doctor verified.

| Step | Action | Owner |
|---|---|---|
| 1 | **Stop the harm first:** pause prescribing (kill switch), or reminders, or the affected doctor's listing | On-call |
| 2 | Bring in the **clinical reviewer** (registered doctor) to judge the patient risk | Security owner |
| 3 | Contact affected patients **by phone** where there is a safety risk (for example wrong dose), not only by notice | Clinical reviewer + support |
| 4 | Tell affected doctors and clinics | Founder |
| 5 | Report the rule-file or prescription error in the incident review; fix through a reviewed PR (K-CS-07) | Backend lead |

A fake doctor is also reported to the relevant State Medical Council or the police [VALIDATE].

## 4. Vendor breaches (E-DP-17)

1. **Processor agreements** require vendors to tell us **within 6 hours** of an incident affecting our data (runbook §2) [CONFIRM clause with the lawyer].
2. **On a vendor notice:** start our own incident at T0 = when we were told.
3. **Check what the vendor held:** the vendor register shows the data each one receives. Adapter rule A4 keeps it minimal; for example, the SMS vendor has phone numbers and discreet texts only.
4. **CERT-In:** we report if our systems or our users' data are affected. **DPDP:** we are the data fiduciary, so **we** notify the Board and the people affected, even though the vendor caused it.
5. **Contain:** rotate the credentials we gave the vendor; disable the adapter if needed (kill switch); switch to the backup provider (SMS) where there is one.
6. Record the vendor's report and their fixes; review whether to keep the vendor.

## 5. Evidence

- Snapshot affected systems before changing them; export the relevant logs to a separate, write-once incident bucket.
- **Chain of custody:** who collected what, when, and its hash.
- Keep incident records for at least **3 years** [CONFIRM] and as long as any dispute lasts.
- The audit-log anchors (audit design §4) show whether logs were altered.

## 6. Drills (acceptance criterion: drill scheduled)

| Drill | When | Scenario | Pass when |
|---|---|---|---|
| Tabletop 1 | **Before v0 starts** (runbook §4) | Waitlist or v0 tool exposes contact data | CERT-In template filled within 6 h; roles clear |
| **Full drill (S-184)** | **Before the closed beta** (after the VAPT, S-183) | Out-of-hours alert: health records exposed through an API bug | Paged in 10 min; contained; CERT-In report ready within 6 h; Board first notice and the 72-hour report drafted; patient notice drafted; review within 5 days |
| Vendor tabletop | First quarter after beta | The SMS provider reports a breach | Section 4 followed; backup provider switch tested |
| Clinical-safety tabletop | First quarter after beta | A rule file allowed a prohibited drug for 2 hours | Section 3 followed; affected prescriptions found from the stored rule-set versions |
| Repeat | **Quarterly** from v1.0 | Rotate scenarios: ransomware, staff misuse, KMS outage, DR failover | Findings fixed or logged |

**Proposed dates:** the founder sets them in the sprint plan [CONFIRM]:
- **Tabletop 1:** in the week before the v0 pilot starts.
- **Full drill:** in the sprint after the VAPT retest.

## 7. Decisions

1. **[Founder]** Approve this plan and set the drill dates (section 6).
2. **[Lawyer]** Notice duty when the copied data was field-encrypted and the keys are safe.
3. **[Lawyer]** Wording and contents of templates B and C against rule 7.
4. **[Lawyer]** The 6-hour vendor notification clause.
5. **[Founder]** Keep incident records 3 years.
