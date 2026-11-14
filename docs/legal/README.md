# Legal drafts for app and website (S-023)

**Phase P01 · Epic EP02 · Depends on:** S-009 (lawyer) · **Compliance:** C08, C19 (plus proposed row N3) · **Contract:** K-PAY-05 · **Date:** 2026-11-15 · **Status:** In Testing (lawyer review pending)

**Who this is for:** the lawyer, to review before publishing; the founder, to fill in the company and officer details.

| File | What it is |
|---|---|
| [terms-of-use.md](terms-of-use.md) | Terms of Use for patients (TOU-1.0-draft), with a key-points summary shown before sign-up |
| [refund-policy.md](refund-policy.md) | Public Refund and Cancellation Policy, from the S-019 policy file |
| [review-policy.md](review-policy.md) | Review Policy, for v1.1 when reviews launch |
| [grievance-redressal.md](grievance-redressal.md) | Grievance Officer details, timelines and escalation routes |
| [provider-agreement.md](provider-agreement.md) | **S-275:** Provider Agreement for doctors (Part A), clinics (Part B) and both (Part C) |

## Law change found in this story

**The IT Intermediary Rules were amended (notified 10 Feb 2026, in force 20 Feb 2026).** Grievance resolution is now **7 days** (was 15). Unlawful-content complaints must be handled in **36 hours** (was 72), court or government orders in **3 hours** (was 36), and intimate or morphed images in **2 hours** (was 24). Users must be reminded of the rules **at least every 3 months** (was yearly). Sources: [Khaitan & Co](https://www.khaitanco.com/thought-leadership/MeitY-notifies-the-IT-Amendment-Rules-2026), [Bar & Bench](https://www.barandbench.com/amp/story/news/law-policy/centre-amends-it-rules-to-regulate-ai-generated-content-takedown-timelines-reduced). Rule numbers vary between sources [VALIDATE].

**Already updated to match:** the privacy notice §11, the data-rights doc timelines, the Practo analysis, and the Practo Review sheet's design responses.

## Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Terms of Use, refund policy, review policy, grievance officer details drafted | **Yes** | The four files above |
| Lawyer-reviewed | **No, pending** | S-009 |
| Ready for app and website | **After review**, and once the founder fills in the company name, address, Grievance Officer, nodal contact and jurisdiction city | — |

## Gaps and questions

1. **[Founder]** Name the Grievance Officer and the nodal contact (open decision 1). This is needed before v0.
2. **Closed:** the Provider Agreement and the doctor and clinic privacy notice are now story **S-275** (added 2026-11-15). Drafts: [provider-agreement.md](provider-agreement.md) and [provider-privacy-notice.md](../privacy/provider-privacy-notice.md). S-110, S-163 and S-188 now depend on it.
3. **[Lawyer]** Are we an intermediary for each feature (C08)? Limitation-of-liability wording; the jurisdiction clause; the Grievance Appellate Committee route; seller-of-record wording in clinic-collected mode.
4. **[Engineering, S-142]** Show users an in-app reminder of the rules at least every 3 months; add takedown timers (36 h, 2 h, 3 h) to the grievance queue; propose a contract rule for these in P08 (S-008 suggested one for C08).
