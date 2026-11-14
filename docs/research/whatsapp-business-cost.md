# WhatsApp Business Platform: operating cost in India (Nov 2026)

**For:** the founder, for open decision 5 (v0 uses the official Business Platform) and S-021 / S-041 (messaging plan, vendor spike).
**Status: ON HOLD (founder, 14 Nov 2026).** No WhatsApp provider is chosen and no WhatsApp setup starts until the founder lifts the hold. Research note dated 14 Nov 2026. Rates are from secondary sources; Meta's INR rate card could not be read. Everything marked **[VALIDATE]** must be checked on Meta's rate card or the provider's pricing page before committing.

## Pricing model
- Meta charges **per delivered template message**, by category and recipient country (since 1 Jul 2025). Messages that users send to us are free.
- 1,000 free **service** messages (replies inside the 24-hour window) per phone number per month, from 1 Oct 2026.
- **Utility messages inside the 24-hour window are charged from 1 Oct 2026.** Older "free in window" claims are out of date.
- Volume discounts apply to utility and authentication only, reset monthly (up to ~30% reported) **[VALIDATE]**.
- India accounts move to INR billing by 31 Dec 2026.
- Source: https://developers.facebook.com/documentation/business-messaging/whatsapp/pricing.md

## India rates per message (card effective 1 Oct 2026) [VALIDATE]
| Category | ₹ per message | Our use |
|---|---|---|
| Utility | 0.115 | Reminders, booking updates |
| Authentication (domestic) | 0.115 | OTP fallback |
| Authentication-International | 2.4971 | Only if our WhatsApp account is registered outside India; avoid |
| Service (after 1,000 free) | 0.115 | Replies to patients |
| Marketing | 0.8631 | Not planned (P-NEWS goes by email/app) |

Source: https://whautomate.com/whatsapp-business-api-pricing-india (updated Nov 2026).

**GST:** 18% on Meta charges and provider fees; Meta's part is generally under reverse charge with input credit available **[VALIDATE with CA]**.

## Estimates (list rates, no discounts, every message delivered)
| Scenario | Volume | Cloud API direct | Through a provider (BSP) |
|---|---|---|---|
| **v0 pilot, 4 weeks** | 200 patients × 5 reminders/week × 4 weeks = 4,000 utility + 200 OTP | **≈ ₹570 incl. GST** | ≈ ₹1,300–3,300 (plan ₹1,000–2,500 + markup, + GST) |
| **v1.0, per month** | 3,000 patients × 20 utility + 2 OTP; ~4,000 paid service replies | **≈ ₹9,500 incl. GST** | ≈ ₹9,500–14,600 + GST |

Message charges are small. The real choice is **engineering time vs provider fee**: Cloud API direct has no platform fee but needs our own sending, template and inbox code; a provider gives an inbox and template tools for the v0 pilot without code.

## Providers (vendor claims; verify on their own pages)
| Provider | Monthly fee | Markup |
|---|---|---|
| AiSensy | free tier / ~₹1,500 | ~26% |
| WATI | ₹2,499–16,999 | ~20% |
| Interakt | ~₹999 | ~2–12% (sources conflict) |
| Gupshup | not found | ~₹0.08 on marketing |
| MSG91 | not found | claims Meta rate pass-through |

**Compliance filters before choosing (C05, C06, C22, K-DP-08, K-MSG-02):** processor agreement available; where the provider stores message logs and contact lists (India preferred: founder decision 4 keeps health data in India, and reminder texts must stay discreet so no health data reaches the provider); opt-in records exportable; WhatsApp account registered in India to avoid the international authentication rate.

## Setup needs [VALIDATE]
- Meta Business verification: legal entity plus incorporation, GST or PAN documents and a matching website, so S-007 must finish first. This could delay the v0 start (about 23 Nov 2026).
- Display name approved by Meta; a phone number not already on the WhatsApp app.
- Template approval usually takes minutes to 24 hours. Reminder templates must be discreet (K-DP-09).
- No health-specific restriction found on the pricing pages; check the WhatsApp Business and Commerce policies for medicine content before writing templates.

## Suggestion for the founder [CONFIRM]
For v0, use a low-markup provider's free or entry plan (no code, about ₹1,500–3,300 for the pilot) once it passes the compliance filters. Decide Cloud API direct vs provider for v1.0 in the vendor spike S-041.
