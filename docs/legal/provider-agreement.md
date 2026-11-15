# Provider Agreement — doctors and clinics (S-275) — DRAFT term sheet and clauses

**Version:** PA-1.0-draft · **Status:** DRAFT for lawyer review (S-009). Not legal advice. **[VALIDATE]** = lawyer. **[CONFIRM]** = founder. **[FILL]** = detail to supply.
**Accepted by:** each **clinic** (through its authorised admin) for the clinic, and each **doctor** individually for their own professional duties. A solo doctor accepts both parts. Accepted in the app before verification starts (S-110); the clinic part before payments are switched on (S-163).
**Linked documents:** [doctor-registration-and-conduct.md](../clinical/doctor-registration-and-conduct.md) (RG and CD rules), [money-flow-v1.0.md](../payments/money-flow-v1.0.md), [refund-policy.md](refund-policy.md), [provider-privacy-notice.md](../privacy/provider-privacy-notice.md), [aadhaar-policy.md](../privacy/aadhaar-policy.md), [telemedicine-rules-spec.md](../clinical/telemedicine-rules-spec.md).

**Who this is for:** the lawyer to turn into final clauses; the founder to fix the commercial terms; doctors and clinic admins, who read the key points before accepting.

---

## Key points (shown before accepting)

- We list you only after we verify your registration, and we re-check it regularly. You must tell us at once if your registration changes.
- **You are responsible for your clinical decisions.** We provide tools, not medical judgement.
- **Patients pay your clinic directly** through your own payment-aggregator account. We never hold patient money.
- You pay us a **flat monthly subscription** (free during the beta). **We never charge per patient or per booking, and we never pay you for referrals** (NMC guidelines, Oct 2026).
- You see a patient's history only with the patient's permission. Every access is logged, and the patient can see the log.
- No paid ranking, no testimonials, no "best doctor" claims. Your profile is a factual directory entry.

---

## Part A — Every doctor

| # | Clause | Detail |
|---|---|---|
| A1 | **Eligibility** | You hold an active registration with a State Medical Council or on the NMC register, and the qualifications you list are recognised. You consent to us checking official registers and using a verification vendor (RG-01 to RG-03). |
| A2 | **Keep us informed** | Tell us **within 24 hours** of any suspension, lapse, conditions on your licence, change of council, or disciplinary finding. We suspend your listing and prescribing at once if a check fails (RG-07). |
| A3 | **Identity documents** | Submit PAN, passport, driving licence or voter ID. **Do not submit an Aadhaar card** (AAD-04). |
| A4 | **Independent practice** | You are not our employee or agent. You alone decide on diagnosis, treatment and prescriptions, and you are responsible for them. You may refuse or end a consultation on clinical grounds. **[VALIDATE: liability allocation]** |
| A5 | **Telemedicine** | You follow the Telemedicine Practice Guidelines 2020: confirm the patient's identity, record consent where needed, and judge whether online care is suitable. You accept that the product blocks prescriptions the rules forbid (S-012). Prohibited drugs (Schedule X and NDPS) cannot be prescribed in the product. |
| A6 | **Records** | You keep clinical records as the law requires. The platform stores them for you and the patient (RET-02). Signed prescriptions cannot be edited; corrections are new versions. |
| A7 | **Patient data** | You open a patient's history only while the patient's permission is active, only for their care, and never copy it outside the product except as the law requires. **No recording** of consultations. No sharing of logins. MFA is mandatory (K-SEC-03). |
| A8 | **Security incidents** | Tell us **within 6 hours** if you suspect your account, device or patient data has been compromised (S-020). |
| A9 | **Conduct and advertising** | You follow the IMC Regulations 2002 and the NMC advertising guidelines of 6 Oct 2026, including: no payment or gift linked to referrals or prescriptions (CD-01); no soliciting, buying or editing reviews (CD-08, CD-12); no testimonials and no endorsing our app or products (CD-09, CD-10); no "best", "top" or cure claims, and no unverifiable awards on your profile (CD-06). |
| A10 | **Profile accuracy** | Specialty only with a recognised postgraduate qualification (RG-04). The fee shown in the app equals your clinic fee. Your calendar is kept accurate; leave and absences are marked in advance. |
| A11 | **Attendance** | Join online consultations on time. If you don't join within 10 minutes, the patient is refunded automatically, and your reliability is recorded (E-BK-04). Reliability may affect visibility in search. It is one of the published ranking factors (Terms of Use §2; CD-05), it is calculated only from recorded attendance, and you can see your own record and dispute errors. |
| A12 | **Reviews (v1.1)** | You may post one public reply per review and report reviews that break the policy. No retaliation against reviewers. |
| A13 | **Minors** | v1.0 is adults only. Do not consult a minor through the product until family profiles launch. |
| A14 | **Complaints** | When a patient complains about you, respond to us **within 48 hours** so we can meet our 7-day resolution duty. Serious complaints trigger re-verification. |

## Part B — Clinics (and solo doctors acting as their own clinic)

| # | Clause | Detail |
|---|---|---|
| B1 | **Clinic details** | Legal name, address, registration under the Clinical Establishments Act where your state requires it (C24), GSTIN if registered, and the doctors who practise with you. Before you give us the details of a doctor or staff member, you tell them, and point them to our provider privacy notice (§1a, §1b). You remove staff from the product when they leave. |
| B2 | **Seller of record** | The clinic is the seller of each appointment and is shown as such to patients (E-Commerce Rules 2020). **[VALIDATE]** |
| B3 | **Payment account** | Keep an active merchant account with our supported payment aggregator. You authorise us, through the aggregator's partner access, to **create payment orders and issue refunds** on your account under the published Refund and Cancellation Policy (S-019). |
| B4 | **Refund balance** | Keep enough balance for refunds. If a refund fails, we alert you. If it is not fixed within **48 hours**, new bookings are paused until it is [CONFIRM]. |
| B5 | **Refund policy** | You may offer more generous terms than our policy, never stricter ones (v1.0). |
| B6 | **Receipts and tax** | Receipts are issued in your name. You are responsible for tax on your supplies (consultations are usually exempt from GST [VALIDATE-CA]). |
| B7 | **Our fee** | A flat monthly subscription of ₹[FILL] plus GST, **waived during the beta**, invoiced monthly. **No fee per booking, per patient or per referral** (CD-02). Any TDS follows the CA's advice (C34, T3). |
| B8 | **Staff** | You are responsible for your staff accounts. Each staff member uses their own login with MFA and only the access their role needs. Remove leavers on the day they leave. |
| B9 | **Clinic closures** | Use bulk cancel or reschedule. Affected patients are notified and refunded under the policy (S-145). Reconfirm doctors' presence the day before (proposed E-BK-07). |
| B10 | **Your patient data** | **Who is the data fiduciary for clinical records — the clinic, the doctor or us — is an open lawyer question (S-011).** Until it is settled, the agreement records: we process personal data only to provide the service and as our privacy notices say; you use patient data only for that patient's care. **[VALIDATE]** |

## Part C — Both

| # | Clause | Detail |
|---|---|---|
| C1 | **Our duties** | We verify providers, run the platform with reasonable security (data hosted in India), show ranking factors openly, never sell ranking (CD-05), handle grievances, and give you notice of changes. |
| C2 | **Suspension and termination** | Immediate on registration lapse, a failed check, a serious conduct breach, or unpaid refunds after notice. Otherwise either side gives 30 days' notice. Upcoming bookings are cancelled with full refunds. Patients keep access to their records. Our records of the verification and the agreement follow RET-08. |
| C3 | **Confidentiality** | Each side keeps the other's non-public information confidential. |
| C4 | **Liability and indemnity** | To be drafted by the lawyer. It must not try to shift clinical liability to us or professional duties away from the doctor. **[VALIDATE]** |
| C5 | **Changes** | 30 days' notice in the app for material changes. Continuing to use the service after that means acceptance; otherwise you can leave. |
| C6 | **Law and disputes** | Laws of India; courts or arbitration in [FILL city]. **[VALIDATE]** |
| C7 | **Acceptance record** | We store the version accepted, who accepted it, when, and for which clinic (evidence for C10, C11). |

## Open questions

1. **[Founder]** Subscription price after the beta, and the beta length (B7).
2. **[Founder]** The 48-hour pause for unpaid refunds (B4), and reliability affecting search visibility (A11).
3. **[Lawyer]** Data-fiduciary roles for clinical records (B10, S-011); liability and indemnity (C4); seller-of-record wording (B2); whether click-accept is enough or a signed agreement is needed; stamp duty.
4. **[Lawyer]** A shorter **pilot participation letter** for the v0 concierge doctors (S-014), covering A1 to A9 in plain terms.
