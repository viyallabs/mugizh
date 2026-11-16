# Contract: Messaging (K-MSG)

**Story:** S-073 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-MSG-01

**Rule:** SMS only through DLT-registered headers and templates.

| Why | How it is enforced | Applies to |
|---|---|---|
| C21 | Adapter guard | notifications |

*Implementation notes (not part of the rule):* docs/messaging/messaging-compliance-plan.md: typed variables, at most 3 per template, at least 30% fixed text; SMS-01 (sign-in OTP) registered as a Service message under the TCCCPR Third Amendment (05 of 2026); the adapter sends Principal Entity ID, header and DLT template ID; variables never logged.

## K-MSG-02

**Rule:** WhatsApp only with stored opt-in (time, source) and approved templates; opt-out honoured immediately.

| Why | How it is enforced | Applies to |
|---|---|---|
| C22 | Adapter guard + tests | notifications |

*Implementation notes (not part of the rule):* WhatsApp is ON HOLD (founder, 14 Nov 2026). The adapter exists as an interface only and the config loader refuses to enable it.

## K-MSG-03

**Rule:** Non-critical messages respect quiet hours in the user's time zone; medication reminders follow the user's schedule.

| Why | How it is enforced | Applies to |
|---|---|---|
| User trust | Tests | notifications |

*Implementation notes (not part of the rule):* Quiet hours come from the country pack (21:00-08:00 in India). OTPs, security alerts, consult-starting messages and medicine reminders at the patient's chosen time are exempt (docs/arch/algorithms.md section 3).

