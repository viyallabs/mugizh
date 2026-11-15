# API standards (S-038)

**Phase P05 · Epic EP07 · Depends on:** S-034 ([architecture-overview.md](architecture-overview.md)) · **Contract refs:** K-ARC-06, K-PAY-02 (also K-SEC-04, K-DP-04, K-ARC-04, K-GLB-02) · **Date:** 2026-11-15 · **Status:** In Testing. The standards doc is done; the lint rule is written but **not verified** until CI runs it on [lint-fixtures/](lint-fixtures/) (acceptance criterion: standards doc; lint rule for OpenAPI)

**Who this is for:** everyone who designs or calls our APIs: backend, mobile and web developers, and reviewers.

**Lint rule:** [openapi-lint.spectral.yaml](openapi-lint.spectral.yaml). It is a Spectral ruleset, which works with any backend language, so the open stack decision doesn't affect it. CI runs it on every change to an OpenAPI file, together with an OpenAPI diff check against the last release (release policy §1).

## 1. Basics

| Topic | Standard |
|---|---|
| Style | REST over HTTPS, JSON (`application/json`, UTF-8). TLS 1.2+ only. |
| Source of truth | **OpenAPI 3.1, written first** (design-first). Server stubs, client SDKs and request validation come from it. Code that doesn't match the spec fails CI (K-ARC-06). |
| One spec per deployable | `core-api.v1.yaml` and `scheduler-core.v1.yaml`. Each module owns a tagged section of the core spec. |
| Base paths | `/v1/...` for apps; `/admin/v1/...` for the Admin console; `/webhooks/<provider>/...` for incoming webhooks (not in the public spec). |
| Naming | Paths: plural nouns in kebab-case (`/v1/appointments`, `/v1/prescription-items`). JSON fields: `camelCase`. Enum values: `snake_case` strings. Query parameters: `camelCase`. |
| Actions that aren't plain CRUD | A sub-resource with POST: `POST /v1/appointments/{id}/cancel`, `POST /v1/prescriptions/{id}/sign`. |
| IDs | Opaque strings (proposed: UUIDv7). Never sequential numbers, never meaningful. |

## 2. Versioning (K-ARC-06)

- **No breaking change inside `/v1`.** Breaking means: removing or renaming a field, path or enum value; making an optional input required; changing a type or meaning. The OpenAPI diff check in CI blocks these.
- **Allowed in `/v1`:** new endpoints, new optional input fields, new output fields, and new enum values **only where the field is marked `x-extensible-enum: true`**. Clients must ignore unknown fields and treat unknown values of extensible enums as "other".
- **`/v2`** needs an ADR and the founder's approval. It runs side by side with `/v1` for at least 6 months, or until fewer than 2% of active installs use `/v1`, whichever is later.
- **Deprecation:** mark the operation `deprecated: true`, and send the `Deprecation` and `Sunset` headers with a link to the notes.
- Apps send `X-App-Version` (for example `patient-android/1.0.3+142`). The server can return `426 Upgrade Required` **only** for security or legal reasons (release policy §1).

## 3. Requests and responses

- **Timestamps:** RFC 3339 in UTC (`2026-11-15T03:30:00Z`). When local time matters (slots, reminders), add a separate IANA time zone field (`timeZone: "Asia/Kolkata"`) (K-ARC-04).
- **Dates without time** (date of birth): `YYYY-MM-DD`.
- **Money:** `{ "amount": 50000, "currency": "INR" }`. Amount is an integer in minor units (paise). Never floats.
- **Phone numbers:** E.164 (`+919876543210`).
- **Text shown to users:** the API returns codes and data. Apps turn them into words with translation keys (K-GLB-02). Clinical and legal text that must be exact (consent text, refund policy) is returned with its version ID.
- **Nulls:** omit absent optional fields rather than sending `null`, unless null carries meaning (documented).
- **Personal data is never in URLs** (paths or query strings), because URLs end up in logs. No phone numbers, names or health terms in query parameters; use POST with a body for searches that need them (K-DP-04).

## 4. Errors (one format everywhere)

Errors use **RFC 9457 Problem Details** (`application/problem+json`):

```json
{
  "type": "https://errors.example/booking/slot-taken",
  "title": "Slot no longer available",
  "status": 409,
  "code": "booking.slot_taken",
  "detail": "Another patient booked this slot.",
  "instance": "/v1/appointments/holds",
  "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "errors": [
    { "field": "slotId", "code": "slot_taken" }
  ]
}
```

- `code` is stable and machine-readable (`<module>.<reason>`). Apps branch on `code`, never on `title` or `detail`.
- `detail` must never contain health data, other people's data or internal details (stack traces, SQL).
- `traceId` links to our scrubbed logs.

| Status | When |
|---|---|
| 400 | Malformed request; validation failed (`errors[]` lists fields) |
| 401 | Not authenticated or token expired |
| 403 | Authenticated but not allowed (role or ownership, K-SEC-04). Also `consent.required` when there is no active consent (K-DP-02). |
| 404 | Not found, **or exists but the caller may not know it exists**. Use 404 instead of 403 for other people's resources, so IDs can't be probed. |
| 409 | Conflict: slot taken, version mismatch, idempotency key reused with a different body |
| 412 | `If-Match` precondition failed (stale ETag) |
| 422 | Valid request that breaks a business rule (for example `clinical.rule_blocked` with the rule IDs) |
| 426 | App too old (security or legal reasons only) |
| 429 | Rate limited (`Retry-After` header) |
| 500 / 503 | Our fault / temporarily unavailable (`Retry-After`) |

## 5. Pagination, filtering and sorting

- **Cursor pagination** on every list: `?limit=20&cursor=<opaque>`. The response has `items[]` and `nextCursor` (absent on the last page). Default limit 20, maximum 100.
- No offset pagination on large or changing lists (appointments, audit, search).
- **Filtering:** named query parameters per field (`status=confirmed&from=2026-11-15`). No generic query language.
- **Sorting:** `sort=startTime` or `sort=-startTime` on the fields the spec allows.
- **Search** with location or personal terms uses `POST /v1/<resource>/search` with a JSON body (section 3).

## 6. Idempotency (K-PAY-02)

- **Every POST that creates something or has a side effect accepts an `Idempotency-Key` header** (a UUID made by the client). It is **required** on: booking holds and checkout, payments and refunds, prescription signing, cancellations, and any operation that sends a message.
- The server stores the key with a hash of the request body and the response, for **24 hours**, scoped per user.
  - Same key and same body: return the stored response (with `Idempotent-Replayed: true`).
  - Same key with a different body: `409 idempotency.key_reused`.
  - Same key while the first request is still running: `409 idempotency.in_progress`; the client retries after a short wait.
- PUT and DELETE are idempotent by definition. PATCH uses `If-Match`.

**Two layers. The 24-hour cache is not what protects money.**

| Layer | What it is | Kept for | Protects against |
|---|---|---|---|
| **HTTP replay cache** | Idempotency-Key + request hash + stored response, per user | 24 hours | A client re-sending the same request (double tap, flaky network) |
| **Durable operation records** (payments module) | Every order, payment, refund and payout is a row with a unique **operation key** (for example `refund:{bookingId}:{rule}`), our merchant reference and its state | As financial records (RET-04, 8 years) | Duplicate money movements at **any** time later: a worker retry days later, a re-run of reconciliation, a manual retry by support |

- A refund for the same booking and rule can exist **only once**, enforced by a unique constraint, whatever key the client sent.
- Retries of a money operation always reuse the stored operation key and merchant reference (adapter-interfaces §4: `findOrder`, `findRefund`).
- The same pattern applies to anything else that must happen once: prescription signing (one signed version per draft) and message sends (one per dispatch ID).
- Calls **to** providers use our own idempotency keys too ([adapter-interfaces.md](adapter-interfaces.md) A5).

## 7. Concurrency

- Resources that can be edited return an `ETag`. Updates send `If-Match`; a mismatch returns 412. This is used on availability rules, profiles and draft prescriptions.
- **Signed prescriptions and notes can't be updated.** Changes are new versions through `POST .../amend` (K-CS-05).

## 8. Security

- **Authentication:** bearer access tokens (short-lived, about 15 minutes) plus refresh tokens bound to the device (patients, K-SEC-03). Professionals and staff need MFA; `POST /v1/prescriptions/{id}/sign` needs a fresh step-up token.
- **Authorisation:** every operation declares the roles allowed (`x-roles`) and whether an ownership or consent check applies (`x-ownership`, `x-consent`). The server is deny-by-default. The automated authorisation suite (S-116, S-181) is generated from these fields (K-SEC-04).
- **Rate limits** per user and per IP, stricter on OTP and login. Responses include `RateLimit-*` headers.
- **Webhooks:** signature checked first; raw body stored; reply 2xx only once stored (adapter rule A7).
- **CORS:** only our web origins. **No cookies on API calls from apps.**
- **Request size:** 1 MB by default. Uploads go straight to object storage through short-lived signed URLs, not through the API.

## 9. Operational conventions

- `GET /health/live` and `/health/ready` (not in the public spec).
- Every response carries `traceId` (W3C `traceparent` accepted on requests).
- Long jobs (exports, PDF generation) return `202 Accepted` with a status resource to poll.
- Responses with personal or health data send `Cache-Control: no-store`.

## 10. scheduler-core API

Same standards, plus:
- It is called only by the Core API with a service credential, never by apps directly.
- Inputs are opaque references, RRULE strings and IANA time zones only. The lint rule `no-health-fields-in-scheduler` rejects free-text fields other than `rrule` and `timeZone` (K-ARC-02).

## 11. What the lint rule checks

[openapi-lint.spectral.yaml](openapi-lint.spectral.yaml) extends Spectral's standard OpenAPI rules and adds:

| Rule | Checks | Standard |
|---|---|---|
| `paths-versioned` | Every path starts with `/v<number>/` or `/admin/v<number>/` | §2 |
| `paths-kebab-case` | Path segments are kebab-case | §1 |
| `properties-camel-case` | Schema property names are camelCase | §1 |
| `operation-id-required`, `operation-tags-required` | Every operation has an operationId and a module tag | §1 |
| `post-idempotency-key` | Every POST declares the `Idempotency-Key` header (as `required` on operations marked `x-side-effect: money` or `message`) | §6 |
| `error-responses-problem-json` | 4xx and 5xx responses use `application/problem+json` | §4 |
| `roles-declared` | Every operation declares `x-roles` | §8 |
| `list-cursor-pagination` | GET operations returning arrays declare `limit` and `cursor`; no `offset` or `page` | §5 |
| `no-pii-in-query` | No query parameter named like a phone, email, name, address or diagnosis | §3 |
| `money-is-integer` | Properties named `amount` are integers | §3 |
| `timestamps-date-time` | Properties ending in `At` use `format: date-time` | §3 |
| `no-health-fields-in-scheduler` | In the scheduler-core spec, string properties other than `rrule`, `timeZone`, IDs and enums are not allowed | §10 |
