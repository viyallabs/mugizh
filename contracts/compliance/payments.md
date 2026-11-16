# Contract: Payments (K-PAY)

**Story:** S-073 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-PAY-01

**Rule:** Never collect, store or log card data; use hosted checkout/tokenisation only.

| Why | How it is enforced | Applies to |
|---|---|---|
| C18 | CI pattern scan | payments |

*Implementation notes (not part of the rule):* Hosted checkout only (docs/arch/adapter-interfaces.md section 4); no card, UPI or bank fields anywhere (data inventory: Prohibited class).

## K-PAY-02

**Rule:** Every payment, refund and payout call uses an idempotency key; state changes come only from signature-verified webhooks (with polling fallback).

| Why | How it is enforced | Applies to |
|---|---|---|
| Money correctness | Tests | payments |

*Implementation notes (not part of the rule):* Two layers (docs/arch/api-standards.md section 6): 24-hour HTTP replay cache, and durable operation records with unique operation keys kept as financial records. Timeouts recovered with findOrder/findRefund by merchant reference. Webhook checks: E-SEC-05.

## K-PAY-03

**Rule:** Money moves only through authorised payment aggregator products; no platform wallet or escrow outside them.

| Why | How it is enforced | Applies to |
|---|---|---|
| C17 | Architecture review | payments |

*Implementation notes (not part of the rule):* v1.0 is clinic-collected (mode B, docs/payments/money-flow-v1.0.md): patient money goes to the clinic's own merchant account. Collecting into our account and paying out (option D) is forbidden.

## K-PAY-04

**Rule:** The ledger is append-only and double-entry; balances are derived, never edited.

| Why | How it is enforced | Applies to |
|---|---|---|
| Audit | DB constraints + tests | payments |

*Implementation notes (not part of the rule):* Clinic-collected money is recorded as memo entries (the money is not ours). The module database role has no UPDATE or DELETE on ledger tables.

## K-PAY-05

**Rule:** Refund and cancellation rules come from one versioned policy config, shown to the user before payment and stored with the booking.

| Why | How it is enforced | Applies to |
|---|---|---|
| C19 | Tests | booking, payments |

*Implementation notes (not part of the rule):* docs/payments/refund-policy.v1.draft.yaml. RF08 and RF09 are blocked (FAIRNESS-01) until the founder chooses option A, B or C and the lawyer confirms the E-Commerce Rules r.4(6) position.

## Proposed changes (each needs an ADR approved by the founder)

- **K-PAY-02:** add "money operations also keep durable operation records with unique operation keys, independent of the HTTP idempotency cache".

