# Itemised consent texts (v1.0, v0 pilot, waitlist)

**Story:** S-010 · **Status:** DRAFT for lawyer review (S-009). Not legal advice.
**Notice version these texts belong to:** PN-1.0-draft (docs/privacy/privacy-notice.md)
**Contracts:** K-DP-02 (consent before any read by others; audit), K-MSG-02 (WhatsApp opt-in), K-DP-09 (discreet notifications)
**Compliance:** C01, C04, C05, C22, N1

## Rules for using these texts (for design and engineering)

1. One checkbox or toggle per purpose. **None pre-ticked.** No "accept all" that hides the items. A single "Agree to the needed items" button is allowed only after all three needed items are shown individually. **Only purposes that are truly necessary for booking and consultations may be "needed"**; anything else is optional, and the app works without it (DPDP s.6(1)). **[VALIDATE]**
2. Each text has a **Purpose ID** and a **text version** (e.g. `P-CARE@2`). Changing any word creates a new version.
3. **Store a consent record** for every grant and withdrawal with: user key, purpose ID, text version, notice version, action (grant/withdraw), time (UTC, NTP-synced), channel (app/web/WhatsApp/paper), and evidence reference (for paper or WhatsApp). This record is what K-DP-02 checks. It feeds the `consent_given` and `consent_withdrawn` events in docs/metrics-and-events.md.
4. **Withdrawal must be as easy as giving:** every optional item has a toggle in Settings → Privacy, and withdrawal takes effect immediately.
5. The full notice must be one tap away from every consent screen.
6. **The app screens are in English at launch** (founder decision, 14 Nov 2026). **But the privacy notices, these consent texts, the Terms and the grievance page must be available in English or any language in the Eighth Schedule to the Constitution, at the user's choice** (compliance review R-03):
   - DPDP Act s.5(3) and s.6(3) (from 13 May 2027);
   - Intermediary Rules 2021, rule 3(1)(a), as amended in 2022: "in the language of his choice" (now).

   A "Read in another language" link sits next to every notice and consent. Translations must be checked for meaning, not just translated word for word, and versioned separately [VALIDATE: how many languages and in what form, before the public launch].

## A. v1.0 app: sign-up

**Screen title:** Before you start

Intro line: *We ask for each use of your data separately. Read the short notice, then choose.* [Link: Read the full privacy notice]

### Age confirmation (not a consent; a gate)
`AGE-GATE@1`: **Date of birth** [date field]
If under 18: *Sorry, you must be 18 or older to use [Product Name]. Family profiles for children are coming later.* No data is kept from this attempt except a count. **[Founder decision 3; C04]**

### Needed to use the app

| Purpose ID | Text shown to the user |
|---|---|
| `P-ACCOUNT@1` | **Your account.** I agree that [Product Name] can use my phone number, name, date of birth and device details to create my account and sign me in safely. |
| `P-CARE@2` | **Bookings, consultations and payments.** I agree that [Product Name] can use my booking and payment details, and the health information my doctor and I add during a consultation, to book and run my appointments, send me messages about my bookings, take payment and give refunds. **The doctor I book sees my booking details and what is added in our consultation, and writes my notes and prescription.** |
| `P-RECORD@2` | **My medical record.** I agree that [Product Name] can store my consultation notes and prescriptions (the law also requires these to be kept) and any documents I choose to upload, so I have my medical record in one place. **I can always see it. A doctor can see the notes from their own consultations with me. Any other part of my record is seen by a doctor only if I share it.** |

Helper text under this group: *These three are needed for the app to work. You can withdraw them later by closing your account.*

### Your choice (all off until you switch them on)

| Purpose ID | Text shown to the user |
|---|---|
| `P-REMIND@2` | **Reminders from my prescriptions.** Create reminders from my prescriptions (medicines, injections, tests and follow-up visits) and send them to me in the app, with SMS as backup. I can switch off any reminder, or all of them. *Also offered when a prescription arrives, if it is off. Booking messages (confirmation, appointment reminders) are part of P-CARE.* |
| `P-WHATSAPP@1` *(ON HOLD: do not show until the founder lifts the WhatsApp hold)* | **Reminders on WhatsApp.** Send my reminders and booking updates to my WhatsApp number too. Messages will not name any condition. I can stop them at any time by replying STOP or in Settings. *WhatsApp is run by Meta, which may process messages outside India.* |
| `P-MEDNAMES@1` | **Show medicine names in reminders.** Show the medicine name in my reminders and notifications. If this is off, reminders say "Time for your medicine" only. Others may see my lock screen. |
| `P-ANALYTICS@1` | **Help improve the app.** Let [Product Name] see how I use the app (screens and buttons), linked to a random code, never my name, phone number or health information. |
| `P-NEWS@1` | **Product news.** Send me occasional news about new features. No health advice, no offers from others. |

**[Open point for the lawyer and founder: docs/metrics-and-events.md assumes analytics runs unless the user opts out. Under DPDP consent must be an affirmative act, so this draft makes analytics opt-in. If the lawyer finds a lawful basis for opt-out, P-ANALYTICS can change. VALIDATE.]**

**Button:** Continue (enabled only when the three needed items are ticked).

## A2. Who can see what: doctor access tiers (for the text and for access control, S-057, S-097)

The consent wording and the access-control code must use the same four tiers. Each tier is a **permission record** that the records module checks on every read (K-DP-02), and every read is audit-logged.

| Tier | Who | What | When it starts and ends | Basis |
|---|---|---|---|---|
| **T1 Current care** | The doctor (and clinic front desk, booking fields only) for a confirmed booking | Booking details; identity details needed for the consult (name, age, sex, phone); everything added **in this consultation**. The doctor can **create** notes and the prescription. | From booking confirmation until the consult is closed and signed, plus a short completion window (**default 24 hours** [CONFIRM]) to finish notes | P-CARE |
| **T2 Treating doctor's own records** | The doctor and clinic who held a consultation | Only the notes, prescriptions and documents **from their own consultations** with the patient | As long as the law requires them to keep the record (RET-02). The patient cannot revoke this, which "Stop sharing" already says (section D). | Legal duty to keep records; P-RECORD |
| **T3 History sharing** | A doctor the patient chooses | The rest of the record: other doctors' notes, the patient's uploads | Per consultation, until the set expiry (default 7 days after the appointment), revocable at once | P-SHARE (section B) |
| **T4 Extension after the consult** | The same doctor | Same as T3 | Only if the patient approves the doctor's extension request (section B) | P-SHARE |

Nothing outside these tiers gives a doctor access. Staff access is separate: just-in-time, reason-coded and audited (K-SEC-08).

## B. v1.0 app: sharing history with a doctor (in the booking or consultation flow)

`P-SHARE@1`. Shown each time; never remembered as a blanket permission.

> **Share your medical record with Dr [Name]?**
> Dr [Name] will be able to see your past notes, prescriptions and documents **for this consultation, until [date and time, default 7 days after the appointment]**. You can stop sharing at any time in Settings → Privacy. Every time the doctor opens your record, it is recorded and you can see it.
> [Share for this consultation] [Not now]

If the patient chooses **Not now**, the consultation still goes ahead. The doctor sees only tier T1 (what is added during this consultation) and T2 (notes from their own earlier consultations with this patient).

Expiry warning, shown to the doctor 10 minutes before access ends: *Access to this patient's record ends at [time]. Ask the patient to extend if you need more time.* Shown to the patient: *Dr [Name] has asked to see your record for [24 hours] longer.* [Allow] [No]
**[Default duration to confirm with founder and doctor advisor; edge cases E-DP-01 (revocation mid-view: next request denied, cached views cleared) and E-DP-02 (expiry mid-consultation) apply to the design in S-057.]**

## C. v1.0 app: before a video consultation

`P-CARE@2` already covers the consultation. The Telemedicine Practice Guidelines 2020 also need the patient's consent to a teleconsultation, and patient identification. **[VALIDATE wording with doctor advisor; C09, S-012]**

> **Start your online consultation with Dr [Name]?**
> This is a video consultation. It is not for emergencies. In an emergency, call **112**. The call is not recorded. Dr [Name] may ask you to confirm your name and age.
> **Online consultations are only for patients who are in India.** ☐ I am in India now.
> *(Added 2026-11-16, compliance review R-02; also asked at booking. The checkbox must be ticked to join. Text version TELE-CONSENT@2 [VALIDATE wording with the lawyer].)*
> [Start consultation]

Starting the call is recorded as the teleconsultation consent (time, booking ID), linked to `P-CARE@2`.

## D. Withdrawal and change texts (Settings → Privacy)

| Action | Text |
|---|---|
| Switch off an optional item | *Done. [Purpose name] is off from now.* No "are you sure" step for optional items. |
| Stop sharing with a doctor | *Dr [Name] can no longer see your record. They keep only the notes from consultations they had with you, as the law requires.* |
| Withdraw a needed item | *To withdraw this, you need to close your account. [Close my account] [Cancel]* |
| Notice changed, new consent needed | *We have updated how we use your data for [purpose]. Nothing changes for this until you agree. [Read what changed] [Agree] [Not now]* |

## E. v0 concierge pilot (doctor's clinic, late Nov 2026)

In v0 there is no app. Consent is collected on a one-page form (paper, or a web form) at the clinic, before any data is entered. **Before 13 May 2027 the SPDI Rules require written consent for health information** **[VALIDATE; N1]**, so the paper or form record must be kept as evidence.

> **[Product Name] reminder pilot: your consent**
>
> Dr [Name] is taking part in a 4-week pilot of [Product Name], a service that sends reminders from your prescription.
>
> If you agree, our team will:
> - type your prescription into our system (medicine names, doses, test and visit dates);
> - send you reminders on WhatsApp from our official business number [number];
> - call or message you to ask how the reminders worked.
>
> We will keep your name, phone number and prescription details. Your health details are stored in India [FILL: confirm v0 tools are India-hosted, open decision 5]. WhatsApp is run by Meta, which may process messages outside India; messages will not name your condition.
>
> We will delete your data within 30 days after the pilot ends, unless you join the app later and agree again.
> You can stop at any time by replying STOP, or by telling the clinic or calling [number].
> Questions or complaints: [Grievance Officer, email, phone].
>
> ☐ I agree that [Product Name] can use my prescription details to send me reminders during the pilot.
> ☐ I agree to receive these reminders on WhatsApp.
> ☐ I agree to be contacted for feedback about the pilot. *(optional)*
>
> Name: ______ Phone: ______ Date: ______ Signature: ______
> I confirm I am 18 or older. ☐

**WhatsApp: ON HOLD (founder, 14 Nov 2026).** Do not use the WhatsApp lines of this form until the hold is lifted. The pilot then needs another reminder channel (for example SMS through registered DLT templates, C21), which must be chosen first; the form text changes to match. If WhatsApp is resumed, it goes through the official WhatsApp Business Platform with approved templates (C22, K-MSG-02), and the first message confirms the opt-in and explains STOP. Cost note: docs/research/whatsapp-business-cost.md.

## F. Waitlist form on the landing page (S-047)

> ☐ Add me to the [Product Name] waitlist and send me launch news at this [email/phone]. I can unsubscribe at any time. [Privacy notice]

The waitlist form must not ask for any health information. Store: contact, time, text version `P-WAITLIST@1`, page URL.

## Purpose register (for engineering and the data inventory, S-011)

| Purpose ID | Needed? | Default | Withdraw by | Linked contract |
|---|---|---|---|---|
| P-ACCOUNT | Needed | Off until ticked | Close account | K-DP-02 |
| P-CARE | Needed | Off until ticked | Close account | K-DP-02, K-PAY-01 |
| P-RECORD | Needed | Off until ticked | Close account | K-DP-02, K-DP-03 |
| P-REMIND | **Optional** (was needed; changed 2026-11-15) | Off; offered at sign-up and when a prescription arrives | Settings, immediate; single reminders can be switched off | K-DP-09 |
| P-SHARE | Per consultation | Asked each time | Settings → Privacy, immediate | K-DP-02 |
| P-WHATSAPP | Optional | Off | STOP or Settings, immediate | K-MSG-02 |
| P-MEDNAMES | Optional | Off | Settings, immediate | K-DP-09 |
| P-ANALYTICS | Optional | Off | Settings, immediate | K-DP-04 |
| P-NEWS | Optional | Off | Unsubscribe or Settings | none |
| P-WAITLIST | Optional | Off | Unsubscribe | none |
| V0-PILOT, V0-WHATSAPP, V0-FEEDBACK | v0 only | Off | STOP or tell clinic | K-MSG-02 |
