# v1.0 money flow, refunds and tax (S-019) — DRAFT for founder decision and CA review

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C17, C18, C19, C20, C34 · **Contracts:** K-PAY-01 to K-PAY-05 · **Edge cases:** E-PAY-01 to E-PAY-04, E-BK-04 to E-BK-06
**Machine-readable refund policy:** [refund-policy.v1.draft.yaml](refund-policy.v1.draft.yaml)
**Date:** 2026-11-15 · **Not legal or tax advice.** **[VALIDATE-CA]** = tax point for the CA. **[VALIDATE]** = legal point for the lawyer (S-009). **[CONFIRM]** = founder decision.

**Who this is for:** the founder, to make the written decision this story needs; the CA, to confirm the tax rows; and engineering, to build F6 (S-124, S-125, S-132 to S-134, S-144, S-146, S-163).

## 1. Recommendation (the decision to sign)

> **v1.0 money flow: clinic-collected.** Each clinic has its own payment-aggregator merchant account. Our app creates the payment on the clinic's account through the aggregator's partner (platform) integration. **Patient money goes straight to the clinic and never to us.** We earn a **flat monthly subscription** from each clinic, waived during the beta. **In v1.0 there is no fee per booking and no fee charged to patients.** Split settlement (mode A) waits for v1.2 (S-217), when we meet the aggregator's eligibility rules and the CA has set up TDS.

**Why:**
1. **We can use it at launch.** Razorpay Route (a split product) needs over ₹40 lakh domestic turnover in the previous financial year (C17, verified in S-008). A new company will not have that.
2. **It is the lowest regulatory risk.** We never hold or route patient money, so the RBI rules for payment aggregators are not in play (K-PAY-03).
3. **It fits the NMC guidelines.** A flat subscription is not tied to "procurement or referral of individual patients" (CD-02 in [doctor-registration-and-conduct.md](../clinical/doctor-registration-and-conduct.md)).
4. **It is simpler tax.** The consultation is the clinic's supply, which is exempt healthcare. Our only taxable supply is the subscription. TDS under the e-commerce rule is still a CA question (section 5).
5. **The roadmap already plans it.** S-163 builds mode B, switchable per clinic.

**Costs of this choice:**
- **Clinic onboarding:** each clinic needs a merchant account with KYC (about 2–7 days). Mitigation: we help clinics apply through the aggregator's partner programme.
- **Refunds run from the clinic's account.** If the clinic's balance is short, refunds can fail. Mitigation: a clause in the clinic agreement, monitoring, and escalation (section 4).
- **No patient-side revenue in v1.0.**

## 2. Options compared

| | A. Split settlement (marketplace) | **B. Clinic-collected via partner integration (recommended)** | C. Pay at clinic / direct UPI to the doctor | D. Platform collects and pays clinics itself |
|---|---|---|---|---|
| How money moves | Patient pays our merchant account; the aggregator splits it to the clinic's linked account | Patient pays the clinic's own merchant account; we create the order using partner access | Paid offline, or by UPI to the doctor's ID, outside the app | Into our bank, then we pay out |
| Allowed for us now | No: eligibility (₹40 lakh turnover) [VALIDATE across aggregators in S-040] | Yes, if the aggregator offers partner or platform access [VALIDATE in S-040] | Yes | **No.** Acting as an unlicensed payment aggregator (RBI); breaks K-PAY-03 |
| Booking confirmed by verified payment (S-125) | Yes | Yes (webhooks from the clinic account, routed to us) | No: no-shows rise, and online consults are unprotected | — |
| Automatic refunds (E-BK-04) | Yes, from our account | Yes, through the API on the clinic's account | No | — |
| Our revenue | Commission or fee deducted in the split (CD-02 risk) | Subscription invoiced to the clinic | Subscription | — |
| TDS (C34) | Yes, we deduct when paying out | Possibly deemed to apply (section 5, T3) | Possibly (T3) | — |
| Effort | High (KYC for linked accounts, payouts, reconciliation; S-217 to S-221) | Medium (per-clinic credentials, webhook routing) | Low | — |

**Option C is allowed only as a per-clinic setting for in-person visits** (pay at the clinic), never for online consults [CONFIRM].

## 3. What the patient sees (consumer rules, C19)

Before paying, the patient sees, in this order:
1. Doctor and clinic name, the clinic's address, and that **the clinic is the seller** (E-Commerce Rules 2020: marketplace duty to show seller details [VALIDATE]).
2. The full fee, with "No extra charges".
3. The refund and cancellation summary for this booking, with the policy version (K-PAY-05).
4. The grievance contact.

The same details are on the confirmation and the receipt. **No pre-ticked add-ons, no countdown pressure, no hidden fees** (dark-pattern guidelines, proposed row N3).

## 4. Refund and cancellation policy v1 (summary of the YAML) [CONFIRM values]

| Case | Online consult | In-person (prepaid) | Who triggers it |
|---|---|---|---|
| Patient cancels **2 hours or more** before the slot | Full refund | Full refund | Patient (S-144) |
| Patient cancels less than 2 hours before, or doesn't attend after a 10-minute grace period (E-BK-05) | No refund | No refund | Patient, or clinic marks no-show (S-131) |
| Patient reschedules 2 hours or more before | Free, once | Free, once | Patient |
| **Doctor doesn't join within 10 minutes** (E-BK-04) | **Full automatic refund** | — | System |
| Doctor or clinic cancels, or the clinic closes (E-BK-06, S-145) | Full automatic refund, or free reschedule (patient's choice) | Same | Clinic |
| Technical failure recorded by the doctor (S-148) | Full refund or free reschedule (patient's choice) | — | Doctor |
| Doctor converts an online consult to in-person (S-148) | The fee counts towards an in-person visit with the same doctor within 7 days, or a 50% refund if the patient declines [CONFIRM] | — | Doctor |
| Money debited but booking failed (E-PAY-02) | Automatic refund at once | Same | System |
| Duplicate payment (E-PAY-03) | Refund the duplicate | Same | System |

**Rules for the policy itself:**
- A clinic can set a **more generous** policy, never a stricter one, in v1.0.
- The policy version is stored with each booking, and the policy applied always equals the policy shown (S-133).
- **Fairness check [VALIDATE]:** the E-Commerce Rules bar cancellation charges on consumers unless the e-commerce entity bears similar charges when it cancels. Our late-cancellation "no refund" is matched by a full refund plus a free reschedule when the doctor cancels. The lawyer must confirm this is enough.
- **Timing:** refunds start within 1 hour of the trigger and go back to the original payment method. The patient sees the status and a reference number. For failed transactions, the aggregator's RBI turnaround rules apply: automatic reversal, with compensation if late [VALIDATE].
- **Mode B refund duty:** the clinic, as seller, owes the refund. The clinic agreement authorises us to issue policy refunds from its account through the API. If a refund fails (low balance), we alert the clinic, escalate to grievance, and pause new bookings for that clinic after 48 hours [CONFIRM]. The lawyer decides who the consumer can claim against (C19 note).

## 5. Tax questions for the CA [VALIDATE-CA all]

| # | Question | Draft assumption |
|---|---|---|
| T1 | GST on consultations | Exempt. Healthcare services by a clinical establishment or an authorised medical practitioner, including teleconsultation, are exempt (notification 12/2017, entry 74). The clinic issues a bill of supply, not a tax invoice. |
| T2 | GST on our subscription to clinics | Taxable at 18%. We issue a GST tax invoice (S-134). Clinics making exempt supplies cannot claim input tax credit, so the price should be quoted including GST. |
| T3 | **TDS for e-commerce operators** (old s.194-O, now s.393(1) of the Income-tax Act 2025, reportedly 0.1%) | **Open.** Under the old s.194-O, payments made directly by the buyer to the seller were *deemed* to be credited by the operator. So TDS may apply even in mode B, even though no money passes through us. There is an exemption for individual sellers under ₹5 lakh a year with PAN. **This changes the C34 row ("conditional on split").** |
| T4 | GST registration | Do we have to register from day one as an e-commerce operator, or only above the ₹20 lakh services threshold? Does GST tax collected at source (s.52) apply when the underlying supplies are exempt? Assumed: register voluntarily before the first subscription invoice (links to S-007). |
| T5 | Receipts in mode B | We generate the patient's receipt **in the clinic's name** (with the clinic's GSTIN if it has one) on its behalf. Confirm the format and numbering. |
| T6 | Record keeping | Invoices and payment records kept 8 years (RET-04 in [data-inventory-v1.0.md](../data-inventory-v1.0.md)). |
| T7 | Mode A later (v1.2) | Payout TDS, GST on our commission, and TCS. To answer before S-217. |

Tax rates and rules go into CA-signed configuration, never into code (S-008 note on C20).

## 6. Engineering rules this sets

| Rule | Source | Story |
|---|---|---|
| Hosted checkout only; no card or UPI credentials on our servers or in logs | K-PAY-01, C18 | S-124, S-092 |
| `settlement_mode` per clinic: `clinic_collected` (v1.0 default), `split` (v1.2), `pay_at_clinic` (in-person only) | Section 2 | S-163 |
| Each clinic's aggregator credentials kept in the secrets manager, never in the database or logs; webhook signatures checked per clinic | K-PAY-02, K-SEC | S-124 |
| Booking confirmed only on a signature-verified webhook, or polling; a pending booking times out after 15 minutes and auto-refunds a late debit | K-PAY-02, E-PAY-01, E-PAY-02 | S-125 |
| Ledger records payments, refunds and subscription invoices in double entry, **even though the money is not ours** (memo entries for clinic-collected money) | K-PAY-04 | S-132 |
| Refund rules only from the versioned policy file; version stored with each booking | K-PAY-05 | S-133, S-144 |
| Daily reconciliation per clinic account | C17 | S-146 |

## 7. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Written decision on v1.0 money flow (split, or clinic-collected fallback) | **Drafted, awaiting founder signature** | Section 1 |
| Payment aggregator product confirmed | **Partly** | Needs the S-040 spike to confirm partner or platform access and fees |
| Split eligibility confirmed | **Yes, for Razorpay** (₹40 lakh); others in S-040 | C17 |
| Refund and cancellation policy | **Drafted** | Section 4 and the YAML |
| GST and TDS treatment with a CA | **No, pending** | Section 5 questions |

**Story status:** In Testing (founder decision, CA answers, and confirmation from the S-040 spike).

## 8. Decisions for the founder

1. **Sign the v1.0 money flow:** mode B, clinic-collected (recommended).
2. **Revenue in v1.0:** flat monthly subscription per clinic, free during the beta. No per-booking fee and no patient fee. And the price?
3. **In-person visits:** prepay in the app, or allow pay-at-clinic per clinic?
4. **Refund values** in section 4: the 2-hour window, 10-minute grace, 7-day conversion credit, and 48-hour pause.
