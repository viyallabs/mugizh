# Copy deck: safety, privacy and legal strings (S-089, part 1)

**Phase P09 · Epic EP11 · Depends on:** S-086 (high-fidelity designs; not done) · **Contract refs:** K-GLB-02, K-CS-07 (also K-DP-09) · **Compliance:** C14 · **Date:** 2026-11-15
**Status:** In Testing.
- **Done (part 1):** the key registry for every string that carries legal or clinical meaning.
- **Still needed:** part 2, the general UI copy, comes from the high-fidelity designs (S-086). **The doctor reviews the clinical wording** (K-CS-07), and the lawyer reviews legal wording.

**Who this is for:** app developers (translation keys), translators, the doctor reviewer and the lawyer.

## 1. Rules

1. **Every user-facing string is a translation key** (K-GLB-02). No literal text in code; CI lint (S-092).
2. **Key format:** `<area>.<screen or purpose>.<item>`, lower case with dots, for example `consent.p_care.text`.
3. **Sensitive strings have one source of truth, the document named below,** identified by version. The translation file copies the text exactly; **changing it means a new version in the source document first**, approved by the named reviewer.
4. **Translations of sensitive strings** (Hindi, Tamil later) are checked for **meaning**, not just translated word for word, and versioned separately (consent texts rule 6).
5. **Plain English, short sentences, no jargon.** The emergency line appears wherever a consult is offered (C33).
6. **Placeholders:** `{doctorName}`, `{dateTime}`, `{amount}`, `{ref}`. Never a diagnosis or specialty in any notification placeholder (K-DP-09).

## 2. Registry of sensitive strings

| Key prefix | What | Source of truth (version) | Approver |
|---|---|---|---|
| `consent.age_gate.*` | Age gate | consent-texts.md `AGE-GATE@1` | Lawyer |
| `consent.p_account.*`, `consent.p_care.*`, `consent.p_record.*` | Needed consents | consent-texts.md `P-ACCOUNT@1`, `P-CARE@2`, `P-RECORD@2` | Lawyer |
| `consent.p_remind.*`, `consent.p_mednames.*`, `consent.p_analytics.*`, `consent.p_news.*` | Optional consents | consent-texts.md `P-REMIND@2`, `P-MEDNAMES@1`, `P-ANALYTICS@1`, `P-NEWS@1` | Lawyer |
| `consent.p_share.*` | Share history with a doctor; extension request | consent-texts.md `P-SHARE@1` | Lawyer + doctor |
| `consent.teleconsult.*` | Pre-join consent and the emergency line | consent-texts.md §C | Lawyer + doctor |
| `consent.withdraw.*` | Withdrawal and change texts | consent-texts.md §D | Lawyer |
| `provider.consent.*` | D-VERIFY, D-PROFILE, D-ANALYTICS, D-STAFF | provider-privacy-notice.md (`@1` each) | Lawyer |
| `rx.block.*` | Prescribing block messages (13 reasons) | telemedicine-rules.v1.draft.yaml `reasons` (v1.1.0-draft) | **Doctor** (K-CS-07) |
| `rx.pdf.*` | Prescription PDF fixed lines (for example "Issued after a teleconsultation (mode: …)") | telemedicine-rules-spec.md §8 | Doctor + lawyer |
| `notify.sms.*` | SMS-01 to SMS-16 (incl. 07b, 07c) | messaging-compliance-plan.md §4 (DLT-registered text must match exactly) | Founder; SMS-09 to SMS-11 also the doctor |
| `notify.push.*` | Push texts (same wording as SMS where they share a purpose) | messaging-compliance-plan.md §4; privacy-ux-spec.md §7 | Founder + doctor |
| `refund.*` | Refund summary, late-cancel and no-show messages | refund-policy.v1.draft.yaml `messages`; legal/refund-policy.md | Founder + lawyer (**FAIRNESS-01 open**) |
| `legal.*` | Terms, policies, grievance page links and short versions | docs/legal/*.md | Lawyer |
| `rights.*` | Data-rights request, export, close-account explanation ("what is kept and why") | data-rights-and-grievance.md; data-rights-flows.md §5 | Lawyer |
| `verification.*` | Doctor verification states and reasons (needs info, rejected, suspended) | user-journeys.md §3; access-control-design.md | Founder |
| `emergency.*` | "Not for emergencies, call {emergencyNumber}"; SOS texts (v1.1) | consent-texts.md §C; E-SOS-01 (v1.1) | Doctor + lawyer |
| `upload.aadhaar_warning` | "Do not upload an Aadhaar card…" | aadhaar-policy.md AAD-05 | Lawyer |
| `breach.*` | Breach notices (templates B and C; SMS-15) | incident-response-runbook.md §6; breach-response-plan.md §2 | Lawyer |

## 3. Clinical wording for the doctor's review (K-CS-07)

Already collected in [doctor-review-pack.md](../clinical/doctor-review-pack.md) Part 4:
- the `rx.block.*` messages;
- SMS-09, SMS-10, SMS-10-N, SMS-11.

**Add to that review:**
- the teleconsult pre-join text;
- the prescription PDF fixed lines;
- the emergency line.

## 4. Part 2 (after S-086)

The general UI copy (buttons, empty states, errors, onboarding) is written from the high-fidelity designs. It gets keys under `ui.<screen>.*` and is added to this deck. Error messages follow the API `code` values (API standards §4); the app maps codes to keys and **never shows server `detail` text directly**.
