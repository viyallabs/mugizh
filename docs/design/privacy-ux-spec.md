# Privacy UX specification (S-079)

**Phase P09 · Epic EP11 · Depends on:** S-078 (wireframes; not done), S-010 ([consent-texts.md](../privacy/consent-texts.md)) · **Contract refs:** K-DP-02, K-DP-09 (also K-DP-05, K-GLB-02) · **Edge case:** E-DP-12 · **Compliance:** C01 · **Date:** 2026-11-15
**Status:** In Testing.
- **Done:** a text specification of every privacy screen, wired to the consent-text versions and checked against the contracts.
- **Still needed:** the visual designs (S-078, S-086) and the **lawyer's check of the consent wording** (S-009), which is the acceptance criterion.

**Who this is for:** the designer turning this into wireframes, the lawyer checking wording, and app developers.

**Rule for every screen below:**
- **Wording comes only from consent-texts.md**, identified by purpose ID and version (for example `P-CARE@2`).
- The app shows text by translation key, and the version is recorded with each consent (K-GLB-02, consent design §2).
- **Designers may change layout, never wording.** A wording change creates a new version and needs the lawyer.

## 1. SC-02 Sign-up consent ("Before you start")

| Order | Element | Content | Behaviour |
|---|---|---|---|
| 1 | Title and intro | consent-texts §A intro; link "Read the full privacy notice" | The notice opens in-app; it is one tap away from every consent screen |
| 2 | Age gate | `AGE-GATE@1` date of birth | Under 18 → stop screen; nothing stored except a count (CH-01) |
| 3 | "Needed to use the app" group | `P-ACCOUNT@1`, `P-CARE@2`, `P-RECORD@2`, **each with its own checkbox, none pre-ticked** | Helper text: "These three are needed…" |
| 4 | "Your choice" group, all off | `P-REMIND@2`, `P-MEDNAMES@1`, `P-ANALYTICS@1`, `P-NEWS@1`, each a toggle. **P-WHATSAPP hidden** (on hold). | Optional items never block Continue |
| 5 | Continue button | Enabled only when the three needed boxes are ticked | Writes one ConsentRecord per item with text and notice versions |

**Must not:**
- an "Accept all" control that hides the items;
- pre-ticked boxes;
- making optional items look required;
- countdowns or nagging;
- asking for Aadhaar (K-DP-05).

## 2. P-SHARE dialog (in the booking or consult flow; SC-09 / SC-13)

- **Text:** `P-SHARE@1` (consent texts §B), with the doctor's name and the end date and time.
- **Two equal buttons:** "Share for this consultation" and "Not now". Neither is styled as the "default".
- **"Not now"** is followed by the line "Your consultation still goes ahead", and the consult continues.
- **Asked every time;** never remembered as a blanket permission.
- **Extension request** from the doctor: "Dr [Name] has asked to see your record for [24 hours] longer." [Allow] [No].

## 3. SC-09 Pre-join (teleconsultation consent)

- **Text:** consent texts §C, "Start your online consultation…", including **"It is not for emergencies. In an emergency, call 112"** (number from the country pack) and "The call is not recorded."
- **"I am in India now" checkbox** (TELE-CONSENT@2, compliance review R-02). It must be ticked to join; it is also asked at booking for online slots. The doctor sees the confirmation (rules check P10).
- **Starting the call records the teleconsultation consent** (time, booking ID; rules check P04).
- **Camera or microphone denied:** explain why they are needed, offer audio-only, and give a link to settings.

## 4. SC-13 Who can see my record (consent manager) and access log

| Section | Content |
|---|---|
| Active sharing | Each doctor with an active T3/T4 grant: name, clinic, until when; **"Stop sharing"** with one tap. Confirmation text: consent texts §D, "Dr [Name] can no longer see your record. They keep only the notes from consultations they had with you, as the law requires." |
| Doctors who keep notes about you | Doctors with T2 access (their own consultations): explains in plain words that the law requires them to keep these notes; no stop button |
| Access log | Reads by people only (consent design §8): who, when, which part, why. Newest first. |

**Naming:** this screen is called "Who can see my record". **It must not be called a "Consent Manager"**, which is a registered entity under DPDP (C01 note).

## 5. SC-16 Notification settings (discreet mode, K-DP-09, E-DP-12)

| Setting | Default | Text |
|---|---|---|
| Prescription reminders (P-REMIND) | Off until chosen | `P-REMIND@2` |
| Show medicine names (P-MEDNAMES) | **Off** | `P-MEDNAMES@1`, including "Others may see my lock screen" |
| Reminder channels | App + SMS for critical items | Per reminder type |
| Quiet hours | 21:00–08:00 for non-critical messages | Medicine reminders follow your schedule (K-MSG-03) |
| WhatsApp | **Hidden** while on hold | — |

**Live preview:** a sample lock-screen notification updates as the toggles change:
- names off: "Time for your medicine";
- names on: "Time to take [Medicine]".

**Never** show a condition or a specialty.

## 6. SC-52 Account and privacy

- **Consents:** every optional purpose as a toggle; switching off takes effect at once, with no "are you sure" (consent texts §D).
- **Needed items:** "To withdraw this, you need to close your account."
- **Signed-in devices:** with sign-out (E-DP-07).
- **App lock:** on or off.
- **Download my data:** step-up OTP, then the export flow (data-rights flows §3).
- **Close my account:** step-up OTP, then a screen listing **what will be deleted, what is kept, why and until when** (E-DP-03). It shows the undo window if the lawyer approves it.

## 7. Notification and SMS text (E-DP-12)

| Message | Text source | Check |
|---|---|---|
| Medicine reminder (push or SMS) | SMS-10, or SMS-10-N with P-MEDNAMES | No condition; name only with opt-in |
| Test or follow-up due | SMS-11: "a health task due" | No test name |
| Prescription ready | SMS-09 | No drug names |
| Booking messages | SMS-02 to SMS-08 | Doctor name allowed, **no specialty** |

## 8. Check against contracts

| Rule | Where met |
|---|---|
| K-DP-02 consent before others read | §2, §4 (sharing asked each time; stop at once; access log) |
| K-DP-09 discreet notifications | §5, §7 (names off by default; preview; no specialty) |
| K-DP-05 no Aadhaar | §1 (never asked) |
| K-GLB-02 translation keys | Rule above (wording by key and version) |
| K-MSG-03 quiet hours | §5 |
| DPDP s.6(4) withdrawal as easy as giving | §6 (one tap, no confirmation for optional items) |

## 9. To finish this story

1. The designer builds wireframes for these screens (S-078), with every state (empty, loading, error, offline, permission denied).
2. **The lawyer checks the consent wording** in context (S-009). This is the acceptance criterion.
3. A usability check that patients understand "Not now" and the medicine-names choice (S-087).
