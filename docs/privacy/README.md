# Privacy notice, consent, data rights and grievance (S-010)

**Phase P01 · Epic EP02 · Depends on:** S-008 (docs/compliance-applicability.md; In Testing)
**Compliance refs:** C01, C02 (plus C04, C05, C08, C19, C22, C30 and proposed N1, applied from the S-008 review) · **Contracts:** K-DP-02 (plus K-DP-07, K-DP-09, K-MSG-02) · **Status:** DRAFT for lawyer review (S-009). Not legal advice.

**Who this is for and what it decides:** for the S-009 lawyer to review and the founder to approve. Once approved, these texts are the source for the consent screens (S-079, S-106 to S-108), the v0 pilot form (S-025), the waitlist form (S-047) and the data-rights flows (S-060, S-142, S-143).

| File | Contents |
|---|---|
| [privacy-notice.md](privacy-notice.md) | Short notice ("At a glance") and full privacy notice, PN-1.0-draft |
| [consent-texts.md](consent-texts.md) | Itemised consent per purpose with versioned IDs; v1.0 sign-up, doctor-sharing, video consult, withdrawal, v0 pilot form, waitlist; purpose register |
| [data-rights-and-grievance.md](data-rights-and-grievance.md) | How each right is handled, timelines, nominee, Grievance Officer, breach messages, evidence |
| [provider-privacy-notice.md](provider-privacy-notice.md) | **S-275:** privacy notice and consent texts (D-VERIFY, D-PROFILE) for doctors, clinics and clinic staff; closes issue 2 below |
| [aadhaar-policy.md](aadhaar-policy.md) | Aadhaar handling policy (S-013): rules AAD-01 to AAD-10 for K-DP-05 |
| [children-data-policy.md](children-data-policy.md) | Children's data policy (S-017): rules CH-01 to CH-13 for K-DP-10; guardian verification options |
| [../research/whatsapp-business-cost.md](../research/whatsapp-business-cost.md) | WhatsApp Business Platform cost (asked with decision 5) |

## Founder decisions applied (14 Nov 2026)
| S-008 decision | Choice | Where it shows |
|---|---|---|
| 3. Under-18s in v1.0 | Adults only; date-of-birth gate; family profiles in v1.1 | Notice §2; consent `AGE-GATE@1` |
| 4. India-only rule | Follow the law. The S-008 review found it does not require all data in India, so **health data India-only**; other data only to named providers in countries not restricted by the Government. Becomes "all data" if the lawyer finds the law requires it. | Notice §6; `P-WHATSAPP@1` |
| 5. v0 WhatsApp | **ON HOLD (founder, 14 Nov 2026).** If resumed: official Business Platform. The v0 pilot needs another reminder channel decided before it starts. | Consent §E, `P-WHATSAPP@1` and notice §5 marked ON HOLD; cost note |
| Launch language | English only for now | Consent rules item 6 |

## Acceptance criteria check
| Criterion | Met? | Evidence |
|---|---|---|
| Lawyer-reviewed drafts | **No, pending S-009** | Drafts are ready for the lawyer; all legal points are marked [VALIDATE]. |
| Plain-language version | Yes | "At a glance" short notice; consent texts written as plain "I agree…" statements. |
| Reading age checked | Yes | Flesch-Kincaid grade, measured by script on 14 Nov 2026 with drafting notes removed. Target is grade 8 or below. Short notice **5.2**; v1.0 consent texts **6.1**; v0 pilot form **6.5**; full notice **7.7**. The formula is approximate (syllable heuristic). A usability read-through with 3–5 patients from the v0 pilot is recommended. |

**Story status:** In Testing. It is Done when the lawyer's comments are applied and the founder approves.

## Things this draft surfaced (not decided here)
1. **Analytics consent model conflicts with docs/metrics-and-events.md.** That doc assumes opt-out; DPDP needs an affirmative act, so this draft makes `P-ANALYTICS` opt-in. Ask the lawyer (S-009), then update the metrics doc. Opt-in will lower analytics coverage.
2. **Doctor and clinic privacy notice is not in scope** (S-010 covers patients: F2, F13). Doctors' and clinic staff's data (registration, ID, photo, bank details) needs its own notice before verification starts (S-110). Suggest adding a story. **[CONFIRM]**
3. **Grievance Officer must be named before the v0 pilot** (SPDI Rules apply now; open decision 1).
4. **Default length of doctor access** (draft: 7 days after the appointment) needs founder and doctor-advisor input (S-057).
5. **Retention periods** in notice §8 are placeholders until S-011 and the CA (records: 3 years [VALIDATE]; tax records: CA).
6. **Meta Business verification needs the legal entity (S-007)**, which may delay WhatsApp for the v0 start (about 23 Nov 2026).
7. **v0 tools** (where pilot data is kept) are still open (decision 5, second half); the pilot form says "stored in India", which must be true before it is used.
8. **Paid placement wording** in notice §3 follows decision 7 (open).
