# Messaging compliance plan (S-021) — SMS (DLT) ready; WhatsApp ON HOLD

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C21 (C22 on hold) · **Contracts:** K-MSG-01, K-MSG-03, K-DP-09 (K-MSG-02 on hold) · **Feeds:** S-041 (provider spike), S-102 (registration, Sprint 0), S-104 (OTP), S-141 (notification module), S-157 (booking messages)
**Date:** 2026-11-15 · **Not legal advice.** **[VALIDATE]** = check with the SMS provider or lawyer. **[CONFIRM]** = founder decision.

**Who this is for:** the founder, who registers the company on DLT; and engineering, who builds the notification module only against registered templates.

**WhatsApp is ON HOLD** (founder, 14 Nov 2026). Section 6 lists only what is needed when the hold lifts. Costs are in [whatsapp-business-cost.md](../research/whatsapp-business-cost.md).

## 1. Channels by version

| Message type | v0 concierge | v1.0 |
|---|---|---|
| Sign-in OTP | — | SMS (DLT) |
| Booking confirmation, reminders, cancellation, refund | SMS (DLT) if registered in time, otherwise a phone call from the team **[CONFIRM: v0 reminder channel, S-008 open decision]** | Push first, SMS (DLT) fallback |
| Medicine and test reminders | Same as above | Push first, SMS fallback for critical items only |
| Receipts and invoices | Email | Email and in app |
| Marketing and news | None | None in v1.0 (no promotional SMS) |
| WhatsApp | **On hold** | **On hold** |

## 2. The rules (TRAI TCCCPR 2018, as amended 12 Feb 2025 and by the Third Amendment of 18 Sep 2026) [VALIDATE details with the chosen provider]

**Legal status (checked 15 Nov 2026):**

| Instrument | Status | What matters to us |
|---|---|---|
| TCCCPR 2018, Second Amendment (12 Feb 2025) | In force | Categories, header suffixes, **annual self-certification** by senders (section 3a), suspension for misuse of headers or templates |
| **Third Amendment (05 of 2026), gazetted 18 Sep 2026** | Most rules in force 30 days after publication (about 18 Oct 2026). Regulations 4, 6, 8(a) and 18 come in after 60 days (about 17 Nov 2026). Annexure items come in after 90 days (about 17 Dec 2026). Exact dates follow the gazette publication date [VALIDATE]. | **Authentication OTPs (app login, password reset, two-factor) are now Service messages, and were removed from the Transactional definition** (SMS-01). A transactional message about a service is treated as a Service message. **Senders now have duties when their headers or templates are misused:** hand over access credentials, and file a police complaint within 3 business days (section 3a). Regulation 18 inserts 35A, a charge on automated (A2P) voice calls. **We make no A2P voice calls**, so 35A does not affect us. |
| Fourth Amendment, dated 4 Nov 2026 | Listed on TRAI's regulations page; **text not read**. Reported to defer 35A until a date TRAI will notify [VALIDATE]. | Treat 35A as not in force until TRAI notifies it. No effect on SMS. |

- **Register before sending:** every business SMS must come from a registered **Principal Entity** (us), using a registered **header** (sender ID) and a registered **content template**. The telecom networks block anything that doesn't match (K-MSG-01).
- **Categories:**
  - **Service (Implicit):** messages that follow from something the user did or a relationship they have: booking, reminders, refunds, and, **since the Third Amendment, authentication OTPs such as our sign-in OTP**. No separate consent is needed. Sent at any hour.
  - **Transactional:** now narrower (OTPs that confirm transactions, for example by banks). We send none.
  - **Promotional:** marketing. Needs consent and respects the Do Not Disturb list. Sent only 10:00–21:00.
  - **"Service Explicit"** messages, which needed consent, **moved to promotional from 10 Mar 2026.** We do not use either in v1.0.
- **Header suffix (since May 2025):** the network adds -T, -S, -P or -G to each header automatically, based on the template category (for example "AB-MYBRND-S"). We just classify templates correctly.
- **Chain binding:** our registration must be linked on DLT to the SMS provider (telemarketer) that delivers for us.
- **Links and numbers in SMS:** any URL, app link or call-back number in a template must be whitelisted on DLT. We use only our own domain, and register it.
- **Variables [VALIDATE with the provider; from provider guidance and TRAI directions, not yet checked against the official text]:**
  - Each variable is **typed for its purpose** on the DLT portal (for example numeric, alphanumeric, URL, callback number), not a plain `{#var#}`.
  - **At least 30% of a template must be fixed text.**
  - **At most 3 variables**; more needs a justification and extra checks, so we design every template with 3 or fewer.
  - Each variable has limited length (usually 30 characters).
  - Everything else must match exactly.

## 3. Registration plan (founder; S-102 in Sprint 0; start as soon as the company exists, S-007)

| Step | What | Needs | Time |
|---|---|---|---|
| 1 | **Register as Principal Entity** on one operator's DLT portal (Jio, Airtel, Vi or BSNL; valid on all networks) | Certificate of incorporation, company PAN, GST certificate if registered, authorisation letter on letterhead, KYC of the authorised person; one-time fee of about ₹5,000–6,000 [VALIDATE] | 1–3 working days |
| 2 | **Register headers** | 6-letter brand header for service messages, for example the product name [FILL after S-016]. Proof of brand (trademark application or website) may be asked for. | 1–2 days |
| 3 | **Whitelist** our domain (and app store links if used) | Domain ownership | 1–2 days |
| 4 | **Register content templates** (section 4) | Exact text, category, variables | 1–3 days each batch; rejections are common, so allow a week |
| 5 | **Bind the chain** to the SMS provider chosen in S-041 | Provider's telemarketer ID | 1 day |
| 6 | **Test** each template on all 4 networks | Test numbers | 1 day |

**Start no later than 4 weeks before the first SMS is needed** (v0 if SMS reminders are chosen; otherwise Sprint 0).

## 3a. Recurring compliance (operations calendar)

| Task | When | Owner | Done when |
|---|---|---|---|
| **Annual self-certification** on the DLT portal: registration details, every header, content template and consent template (2025 amendment). **Missing it can lead to suspension.** | Every year, 30 days before the registration anniversary; calendar reminder [VALIDATE exact window with the operator] | Founder (later: compliance owner) | Certification confirmed on the portal; screenshot and date in the compliance log |
| Template review: remove unused templates; check categories still match the rules | Every 6 months, and after any TRAI amendment | Engineering lead + founder | Registry and DLT portal match; CI test passes |
| **Misuse response:** if the operator reports misuse of our header or a template, hand over access credentials as asked, file a police complaint within 3 business days, and share a copy with the operator within 2 business days (Third Amendment). Rotate provider API keys. | On notice | Founder + on-call engineer | Steps logged; incident handled under the [incident response runbook](../security/incident-response-runbook.md) |
| TRAI watch: check for new amendments and directions (for example the Fourth Amendment's 35A notification) | Monthly | Founder | Section 2 updated |

## 4. Template list (English; v1.0 unless marked) [CONFIRM wording; doctor review for clinical wording in S-089]

Discreet by default (K-DP-09): **no condition, test or medicine names** unless the patient chose P-MEDNAMES, in which case a separate "-N" template is used. No health details in any SMS.

**Variable types** (the names follow the DLT portal and are [VALIDATE] with the provider): `N` = numeric, `A` = alphanumeric. **Every template has 3 or fewer variables, and at least 30% fixed text.**

| ID | Category | Purpose | Draft text (`{#var#}` = variable) | Variable types |
|---|---|---|---|---|
| SMS-01 | **Service (Implicit), authentication OTP** (Third Amendment; register in the Service category, header suffix -S) | Sign-in OTP | `{#var#} is your [Product] sign-in code. It expires in 5 minutes. Do not share it with anyone. -[Product]` | N (OTP, 4–6 digits) |
| SMS-02 | Service Implicit | Booking confirmed | `Your appointment with {#var#} is confirmed for {#var#}. Manage it in the [Product] app. -[Product]` |
| SMS-03 | Service Implicit | Appointment reminder (24 hours and 2 hours before) | `Reminder: your appointment with {#var#} is at {#var#}. -[Product]` |
| SMS-04 | Service Implicit | Online consult starting | `Your online consultation with {#var#} starts at {#var#}. Open the [Product] app to join. -[Product]` |
| SMS-05 | Service Implicit | Clinic or doctor cancelled | `Sorry, your appointment on {#var#} was cancelled by the clinic. A full refund has started. Reschedule in the app. -[Product]` |
| SMS-06 | Service Implicit | Doctor did not join | `Your doctor could not join the consultation on {#var#}. A full refund of Rs {#var#} has started. -[Product]` |
| SMS-07 | Service Implicit | Refund started (**redesigned:** was one template with 4 variables, including a free-text status) | `Your refund of Rs {#var#} (ref {#var#}) has started. Banks usually take 5-7 working days. Track it in the [Product] app. -[Product]` | N, A |
| SMS-07b | Service Implicit | Refund reached the bank | `Your refund of Rs {#var#} (ref {#var#}) has been sent to your bank. -[Product]` | N, A |
| SMS-07c | Service Implicit | Refund delayed | `Your refund of Rs {#var#} (ref {#var#}) is delayed. We are working on it; see the [Product] app for help. -[Product]` | N, A |
| SMS-08 | Service Implicit | Payment failed, money returned | `Your payment of Rs {#var#} did not complete a booking. It will be returned automatically. Ref {#var#}. -[Product]` |
| SMS-09 | Service Implicit | Prescription ready | `Your doctor has shared a new prescription. Open the [Product] app to view it. -[Product]` |
| SMS-10 | Service Implicit | Medicine reminder (discreet; fallback when push fails) | `It is time for your {#var#} medicine. Open the app to mark it taken. -[Product]` (var = "morning", "evening" and so on) |
| SMS-10-N | Service Implicit | Medicine reminder with name (P-MEDNAMES only) | `Time to take {#var#}. Open the app to mark it taken. -[Product]` |
| SMS-11 | Service Implicit | Test or follow-up due (discreet) | `You have a health task due on {#var#}. Open the [Product] app for details. -[Product]` |
| SMS-12 | Service Implicit | Grievance acknowledgement | `We received your complaint, ref {#var#}. We will reply by {#var#}. -[Product]` |
| SMS-13 | Service Implicit | Data request done (access, correction or erasure) | `Your request ref {#var#} is complete. Details are in the app or your email. -[Product]` |
| SMS-14 | Service Implicit | Security notice: new device sign-in, or number re-verification (E-DP-22) | `A new sign-in to your [Product] account happened on {#var#}. If this was not you, contact us at [support number, fixed and whitelisted]. -[Product]` | A (date/time) |
| SMS-15 | Service Implicit | Breach notice (runbook template C, short form) | `Important: a security incident may have affected your [Product] account. Details are in the app and your email. Questions: [support number, fixed and whitelisted]. -[Product]` | none |
| SMS-16 (v1.1) | **Unclear** | SOS alert to an emergency contact who is not a user (F9) | Category to confirm with the provider, because the recipient has no relationship with us [VALIDATE] |

**Variables kept short:** for doctor name, use "Dr" plus the surname or a shortened form. The time format is "Mon 17 Nov, 10:30 AM".

**Variable types for the other templates:** doctor name A; date/time A; amount N; reference A; time-of-day word (SMS-10) A; medicine name (SMS-10-N) A. No template uses more than 2 variables. Contact numbers and links are **fixed text** (whitelisted), never variables.

## 5. Engineering rules (S-141, S-102)

| Rule | Source |
|---|---|
| Business code only asks for a template ID plus its variables. Free-text SMS is impossible in code, and a CI test checks that every template ID exists in the registry. | K-MSG-01 |
| **The adapter sends the provider every identifier DLT needs: Principal Entity ID, registered header, DLT content-template ID** (plus the variables). An integration test against the provider sandbox checks that each request carries all three. If the provider fills any of them in itself, that is confirmed in writing and still covered by the test. | TCCCPR; S-041, S-141 |
| A template registry (country-pack config) holds the Principal Entity ID, DLT template ID, header, category, text, variable types and limits, and language. A CI test fails if a template has more than 3 variables or less than 30% fixed text. | K-GLB-01 |
| The medicine-name variant is used only when P-MEDNAMES is active, checked at send time. | K-DP-09 |
| Non-critical messages are held during quiet hours (21:00–08:00 in the user's time zone). OTPs, consult-starting messages, security alerts and medicine reminders at the patient's chosen time are exempt. | K-MSG-03 |
| Delivery logs are kept 90 days (RET-10). The OTP value is never logged. | RET-10, K-DP-04 |
| Fallback order: push → SMS (critical only) → none. No WhatsApp while it is on hold. | S-141 |

## 6. WhatsApp — ON HOLD (when the founder lifts the hold)

When resumed:
- Meta business verification, display name and official Business Platform provider (S-041).
- Opt-in capture with time and source (K-MSG-02), using consent text P-WHATSAPP in [consent-texts.md](../privacy/consent-texts.md).
- Utility and authentication templates mirroring SMS-02 to SMS-11.
- A check of Meta's health-content rules (C22, [VALIDATE]).

Nothing is to be built or registered until then.

## 7. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Registration plan ready | **Yes for SMS (DLT)**; WhatsApp on hold | Section 3 |
| Template list ready | **Yes for SMS** (18 templates, each with typed variables and 3 or fewer variables); WhatsApp on hold | Section 4 |

**Story status:** In Testing. The founder confirms the template wording and the v0 reminder channel. The WhatsApp part waits for the hold to lift.

## 8. Decisions

1. **[Founder]** v0 reminder channel while WhatsApp is on hold: SMS through DLT (register now, which needs the company from S-007), or phone calls by the concierge team?
2. **[Founder]** Approve the template wording, or send it to the doctor reviewer for clinical wording (S-089).
3. **[Provider, at S-041]**
   - Confirm SMS-01 is registered as Service (authentication OTP) under the Third Amendment.
   - Confirm the variable types, length limits and the 30% and 3-variable rules.
   - Confirm the SOS template category (SMS-16).
   - Confirm which DLT identifiers their API requires on each send.
4. **[Lawyer/provider]** Confirm the Fourth Amendment's text and the 35A status. Confirm the gazette publication dates for the Third Amendment's 30, 60 and 90-day starts.
