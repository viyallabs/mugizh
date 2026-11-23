# Adapter interfaces for external providers (S-037)

**Phase P05 · Epic EP07 · Depends on:** S-036 ([module-boundaries-and-domain-model.md](module-boundaries-and-domain-model.md)) · **Contract refs:** K-ARC-03 (also K-DP-01, K-DP-08, K-PAY-01, K-PAY-02, K-MSG-01, K-MSG-02, K-GLB-01) · **Date:** 2026-11-15 · **Status:** Done (acceptance criterion: interfaces written with error and timeout behaviour)

**Who this is for:** developers who build adapters, and anyone running the vendor spikes (S-039 video, S-040 payments, S-041 messaging, S-042 verification).

Interfaces are written in a **neutral notation**, not tied to any language: `operation(input) -> output`. The chosen language (ADR-02, open) turns them into real types. Field names are logical.

## 1. Rules for every adapter (K-ARC-03)

| # | Rule |
|---|---|
| A1 | **Vendor SDKs and URLs appear only inside `adapters/<kind>/<vendor>/`.** Lint blocks them anywhere else. Modules depend on the interface, never on a vendor. |
| A2 | **Which vendor is used comes from the country pack** (`IN` → provider per adapter kind). Changing vendor is a config change plus a new adapter, not a change to business code (K-GLB-01). |
| A3 | **An adapter can be enabled only if its vendor is in the vendor register** with a processor agreement and India processing (K-DP-08, K-DP-01). The config loader refuses to start otherwise. |
| A4 | **Send the minimum data.** Each operation below lists exactly what leaves our system. Never send diagnoses, notes or free-text health data unless the operation needs it. |
| A5 | **Every call has a timeout, a retry policy and an idempotency key** where the vendor supports one. Calls that move money or send messages must be safe to repeat. |
| A6 | **Every adapter maps vendor errors to the common error model** (section 3). Business code never sees a vendor error code. |
| A7 | **Webhooks:** signature checked before anything else; raw event stored and de-duplicated by vendor event ID; processed by a worker, never inline. Reply 2xx only after the event is safely stored. |
| A8 | **Credentials** live in the secrets manager, scoped per vendor and per clinic where needed (payments). Never in the database, config files or logs (K-SEC-05). |
| A9 | **Each adapter has a kill switch** (feature flag) and a **circuit breaker**: after repeated failures it fails fast for a cool-down period, and the module uses the fallback listed below. |
| A10 | **Each adapter ships with a fake** (a test double that behaves like the vendor, including failures). Staging and CI use the fake or the vendor's sandbox, never live accounts with real data. |
| A11 | **Logs:** request and response metadata only (operation, vendor, latency, outcome, our reference). Never payloads with personal data (K-DP-04). |
| A12 | **Erasure:** an adapter that sends personal data to a vendor implements `requestDeletion(subjectRef)`, or documents the vendor's own retention in the vendor register (K-DP-07). |

## 2. Common types

- `Ref`: our opaque reference (for example an appointment or prescription item ID).
- `Money`: amount in minor units (paise) plus ISO 4217 currency.
- `IdempotencyKey`: a stable key, derived from our entity ID and operation, so a retry never repeats the effect.
- `Instant`: UTC timestamp; `ZoneId`: IANA time zone (K-ARC-04).
- `Result<T>`: either the value `T` or an `AdapterError` (section 3).

## 3. Common error model

| Error kind | Meaning | Caller behaviour | Retried by the adapter? |
|---|---|---|---|
| `Timeout` | No answer within the timeout | **Outcome unknown.** For money or messages, check the status before trying again, never blind re-send. | Yes, only if the operation is idempotent |
| `Unavailable` | Vendor down, 5xx, network error, circuit open | Use the fallback; try later | Yes, backoff with jitter |
| `RateLimited` | Vendor throttling | Wait for the time the vendor gives, then retry | Yes |
| `Rejected` | Vendor refused the request for a business reason (for example, card declined, template not approved, number on the DND list) | Show or record the reason; no retry | No |
| `Invalid` | Our request was wrong (bug or bad data) | Alert; fix; no retry | No |
| `AuthFailed` | Credentials invalid or expired | Alert ops at once; circuit opens | No |
| `NotFound` | The vendor doesn't know the reference | Handle in the caller | No |

**Default retry policy:** up to 3 retries, exponential backoff starting at 1 s, with full jitter, inside the operation's total time budget. Workers retry further from the queue (up to 5 minutes for notifications; Performance sheet), then send the job to the dead-letter queue and raise an alert. **Every timeout below is a starting value**, to be tuned by the spikes.

## 4. Payments adapter (ADR-14; vendor from S-040)

Mode B: orders and refunds are created **on the clinic's own merchant account** through the aggregator's partner access ([money-flow-v1.0.md](../payments/money-flow-v1.0.md)). Split settlement (mode A) is v1.2 and adds `createSplitOrder` later.

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `createOrder` | clinicLinkRef, Money, IdempotencyKey, **merchantOrderRef** (our order ID, sent as the provider's receipt/reference field), appointmentRef → providerOrderId, checkout details | Amount, our reference, patient phone for the checkout page (if required) | 5 s | Hosted checkout only. **No card or UPI details ever reach us** (K-PAY-01). On `Timeout`, call `findOrder` before trying again. |
| `findOrder` | clinicLinkRef, merchantOrderRef → providerOrderId and status, or `NotFound` | — | 3 s | Recovers from a timed-out `createOrder` when we never received the provider's ID |
| `getPaymentStatus` | providerOrderId → status (created / authorised / captured / failed), paymentId, amount | — | 3 s | The polling fallback when a webhook is late (K-PAY-02) |
| `refund` | clinicLinkRef, paymentId, Money, IdempotencyKey, **merchantRefundRef** (our refund ID, sent in the provider's reference or notes field), reason code → providerRefundId, status | Amount, our reference | 5 s | A `Timeout` means **outcome unknown**: call `findRefund`, never refund blindly again. A low clinic balance maps to `Rejected(INSUFFICIENT_BALANCE)` → alert the clinic and escalate (mode B refund duty in the money-flow doc). |
| `getRefundStatus` | providerRefundId → status | — | 3 s | When we have the provider's ID |
| `findRefund` | clinicLinkRef, paymentId, merchantRefundRef → providerRefundId and status, or `NotFound` | — | 3 s | **Recovers from a timed-out `refund`.** Lists the payment's refunds and matches our reference. If `NotFound` after the provider's settle time, the refund is retried with the **same** IdempotencyKey and merchantRefundRef. The S-040 spike must confirm each aggregator supports a merchant reference on refunds, and lookup by payment. |
| `verifyWebhook` | headers, raw body, clinicLinkRef → verified event or `Invalid` | — | local | Per-clinic webhook secret (A7) |
| `fetchSettlementReport` | clinicLinkRef, date → report lines | — | 30 s | Daily reconciliation |
| `onboardingLink` | clinic details → partner onboarding URL | Clinic business details | 5 s | Helps clinics open merchant accounts through the partner programme |

**Fallback:** "pay at clinic" for new bookings (kill switch); confirmed bookings are unaffected.

## 5. SMS adapter (ADR-15; vendor from S-041)

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `send` | phone, principalEntityId, header, dltTemplateId, typed variables, IdempotencyKey → providerMessageId | Phone number, DLT identifiers, template variables | 3 s | **Only registered DLT templates; free text is impossible** (K-MSG-01, [messaging plan](../messaging/messaging-compliance-plan.md)). The adapter rejects a template ID not in the country pack registry with `Invalid`. Variables never contain a diagnosis (K-DP-09). |
| `getStatus` | providerMessageId → queued / delivered / failed (reason) | — | 3 s | |
| `verifyDeliveryWebhook` | headers, body → delivery event | — | local | |

- **Errors:** a DND or blocked number maps to `Rejected`; a template mismatch (scrubbed by the operator) maps to `Rejected(TEMPLATE_MISMATCH)` and alerts.
- **Fallback:** a second SMS provider (config), then push only. OTP SMS has its own priority queue.

## 6. Push adapter

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `send` | deviceToken, platform, title key, body (already rendered and privacy-safe), data refs → providerMessageId | Device token, notification text | 3 s | Text follows K-DP-09: no diagnosis; medicine name only with the patient's opt-in. |
| `send` (replaceKey) | optional display-replacement key (`rem-<minute>-c` or `-n`) | The key | — | S-165: a later notification with the same key replaces the earlier one on the device (the reminder push and the on-device backup for the same minute). Map it to the Android notification tag and the APNs collapse identifier, **never to the FCM collapse_key** (that makes the message collapsible and droppable while the device is offline). |
| `send` (silent) | deviceToken, platform, data only (`{"sync":"reminders"}`, or `{"signout": <device ID>}` after a remote sign-out), no title, no body | Device token | 3 s | S-165: a content-free hint that the app should fetch its reminder schedule again, or wipe itself (D-69) (Android data message, APNs background push). The OS may delay or drop it, so it is never relied on. |
| `invalidToken` (callback) | token → | — | — | Remove dead tokens |

**Fallback:** SMS for reminder types where the patient allowed SMS. The app's local reminders are the last line (ADR-19).

## 7. WhatsApp adapter (ON HOLD: interface only, disabled)

Kept so WhatsApp can return without redesign. No vendor is chosen, and nothing is built against a provider until the hold is lifted (founder, 14 Nov 2026).

| Operation | Input → output | Notes |
|---|---|---|
| `send` | phone, approvedTemplateName, locale, variables, IdempotencyKey → providerMessageId | Only with a stored opt-in (time, source); opt-out honoured at once (K-MSG-02) |
| `getStatus`, `verifyWebhook` | as for SMS | Includes inbound "STOP" handling |

The adapter's config flag is `false` in the `IN` pack, and the config loader refuses `true` while the vendor register has no WhatsApp provider (A3).

## 8. Email adapter

| Operation | Input → output | Timeout | Notes |
|---|---|---|---|
| `send` | to, templateKey, variables, attachments refs, IdempotencyKey → messageId | 5 s | Receipts, subscription invoices, staff alerts. Health documents are **never attached**; the email carries an in-app link only. |

## 9. Video adapter (ADR-13; vendor from S-039)

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `createRoom` | sessionRef, expected participants, starts_at, max duration → providerRoomId | Our reference only | 5 s | **No recording** (setting forced off; checked by a test). Media region: India. |
| `issueJoinToken` | providerRoomId, participantRef, role (doctor / patient), expiry → token | Opaque participant reference, display name ("Dr. X", "Patient") | 3 s | Token valid only around the slot time |
| `getParticipantEvents` / webhook | providerRoomId → joined / left events with times | — | 3 s | Proves "both sides connected" (E-BK-08) |
| `endRoom` | providerRoomId → | — | 3 s | |

- **Network:** the client SDK uses adaptive bitrate and switches to audio below the bandwidth threshold (Performance sheet, join under 5 s).
- **Fallback:** if `createRoom` fails, the consult switches to an audio call through the same provider, or to a phone call by the doctor with consent. The consult is recorded as "technical failure" so no-show refund rules apply fairly. The telemedicine rules engine treats audio as audio (List O only for a first consult).

## 10. Geocoding adapter (ADR-16)

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `geocode` | address text, country → lat/lng, accuracy, normalised address | **Clinic addresses only.** Patient locations never leave the device or our database. | 3 s | Results cached; radius search runs in our own database (ADR-16, ADR-22) |
**No reverse geocoding of patient locations.** A `reverseGeocode` operation was removed (2026-11-15) because it would send the patient's position to the provider. "Near me" labels come from the area the patient picked, or from our own locality and PIN-code table, matched in our database. Any future need to send a patient location to a vendor needs a privacy-notice change and a new review.

**Fallback:** manual pin on the map by the clinic admin; search uses the last stored point.

## 11. Verification adapters (F17; vendor from S-042)

The verification module runs a state machine with **several sources** behind one interface. During the beta, only the manual source is used (S-111 deferred, [scope-v1.0.md](../scope-v1.0.md)).

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `checkRegistration` | council, registration number, name → match / no_match / not_found / inconclusive, details, evidence ref | Council, registration number, name | 15 s (vendor), async for registers | Registration numbers can repeat across councils, so the council is always required |
| `checkDocument` | evidence ref (image), type → extracted fields, authenticity score | The document image | 30 s, async | **Never an Aadhaar card** (rejected before the adapter, AAD-04) |
| `manualReview` | case ref → task in the Admin console queue | — | — | Always available; the fallback for every other source |

**Rules:** an `inconclusive` result or any error sends the case to manual review. It never auto-verifies. Auto checks aim for under 1 minute; manual review under 48 hours (Performance sheet).

## 12. Age verification adapter (only if the founder picks option A in S-017; v1.1)

| Operation | Input → output | Notes |
|---|---|---|
| `verifyAdult` | redirect / consent flow → adult yes/no, token ref, date | We store only "adult: yes", the token reference and the date. No document, no Aadhaar number ([children-data-policy.md](../privacy/children-data-policy.md)). |

## 12a. Electronic signature adapter (P09; method decided by the lawyer, N5)

Step-up authentication (identity) and the electronic signature (the document) are separate ([telemedicine-rules-spec.md](../clinical/telemedicine-rules-spec.md) P08, P09). The signature provider is an external vendor, so it sits behind this adapter.

| Operation | Input → output | Data that leaves us | Timeout | Notes |
|---|---|---|---|---|
| `startSigning` | signerRef, documentHash (SHA-256), document display summary → signing session / redirect | **The hash only, never the prescription content.** Signer's name and registration number if the method needs them. | 10 s | If the provider's method uses the signer's Aadhaar OTP, that happens on the provider's side only. **We never receive or store the Aadhaar number** (K-DP-05, AAD rules). |
| `completeSigning` (callback) | session → signature value, certificate chain, signing time | — | async | Stored with the prescription version (RET-02) |
| `verify` | documentHash, signature → valid / invalid / revoked | — | 5 s | Used when the PDF is opened or re-issued |

- **Errors:** a timeout or cancelled session leaves the prescription **unsigned and not issued**. There is no fallback to "unsigned", because P09 fails closed.
- **Until N5 is decided:** the adapter has only a fake, and production signing stays blocked.

## 13. OCR adapter (ADR-20; v1.1, F12)

| Operation | Input → output | Notes |
|---|---|---|
| `extract` | document ref → fields, each with a confidence score | India processing only. **Human review is always required** (K-CS-03). The output never sets a dose on its own, and low-confidence items never create reminders. |

## 14. Infrastructure services (not "external providers", but treated the same)

Key management, object storage, secrets and the queue are reached through thin internal wrappers (`crypto`, `blobstore`, `secrets`, `jobs`), so the cloud choice (ADR-09, open) does not leak into module code. The same A1, A5, A6 and A11 rules apply.

## 15. Testing each adapter

- **Contract tests** run against the vendor sandbox (nightly) and the fake (every build).
- **Fault injection:** timeout, 5xx, duplicate webhook, out-of-order webhook, invalid signature, rate limit.
- **Money and message paths:** every `Timeout` case is tested to prove there is no double charge, double refund or duplicate SMS.
