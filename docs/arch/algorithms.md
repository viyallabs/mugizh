# Algorithm choices: rules engine, search ranking, notification fallback (S-069)

**Phase P07 · Epic EP09 · Depends on:** S-065 · **Contract refs:** K-CS-02 (also K-CS-03, K-MSG-01, K-MSG-03, K-DP-09) · **Date:** 2026-11-15
**Status:** Done (acceptance criterion: choices recorded with reasons). Recorded in the Performance sheet (rows 10, 12, 21). The ranking weights and the reliability factor need founder confirmation before S-162 is built.

**Who this is for:** backend developers building S-138, S-162 and S-141, and reviewers.

## 1. Prescription rules engine (S-138)

**Choice:** a deterministic decision-table interpreter over the country-pack rule file ([telemedicine-rules spec](../clinical/telemedicine-rules-spec.md), v1.1.0-draft). **No machine learning**, and **no suggestions**: the engine only allows or blocks (K-CS-03, MD-04).

**Algorithm, per prescription item:**
1. **Inputs.** The drug's list memberships (O, A, B, prohibited, or none), from the drug classification version. The **mode at signing**. The **consult type for this item**: first, or follow-up, derived in step 2. The context (new drug, refill, add-on).
2. **Follow-up test.**
   - It is a follow-up only if **all** of these hold: same doctor; a last in-person consult (platform record or attested date) within **6 calendar months**, counted in the patient's time zone with month-end clamp; the doctor attests "same condition"; no exclusions.
   - **An item marked "new condition" is always treated as first.**
3. **Regulatory layer:** scan rows R01 to R10 in order; the **first match wins**; **no match means block**.
4. **Platform layer:** if the regulatory layer allows the item, apply PL01 to PL03. Any match blocks it.
5. **Prescription-level checks:** P01 to P07 when the doctor checks; **P01 to P10 again at signing**, on the server.
6. **Output:** allowed or blocked, the reason code, the rule IDs matched, the layer, and both versions. Stored with the prescription (rules spec §5).

**Precedence:** prohibited beats every list. Only a named amendment exception removes a drug from "prohibited". The drug master import rejects conflicts (rules spec §4).

**Data structures:**
- A hash map from drug ID to its membership set, built when the classification version loads.
- The rule tables kept as ordered arrays.

**Cost:** about (items × 10 rows) comparisons, far under the 50 ms target.

**Why this choice:**
- Explainable to doctors and auditors.
- Changes by reviewed config, not code (K-GLB-01, K-CS-07).
- One test per row.
- Low risk under the CDSCO software guidance, because it is a regulatory gate, not decision support.

## 2. Search ranking v1 (S-162)

**Rules first (CD-05, proposed K-CS-08):**
- **Payment, subscription tier or partner revenue are never inputs.**
- The ranking is **never personalised using health data.**
- The factors are **published in plain words** on the search screen and in the Terms (§2).

**Step 1: filter (all must hold).** Verified and active (K-CS-01); specialty match; consult mode offered; within the radius; at least one open slot in the next 14 days.

**Step 2: score** (each factor scaled 0 to 1):

| Factor | Formula | Weight (proposed) |
|---|---|---|
| Distance | `1 − d / radius` | 0.45 |
| Availability | `1 − min(hours to earliest slot, 168) / 168` | 0.35 |
| Reliability (**only if the founder approves A11**) | `1 − smoothed no-show rate`, where `smoothed = (doctor no-shows + 1) / (doctor consults + 20)`, over 90 days | 0.20 |
| Rating (**from v1.1**) | Bayesian average `(C × m + Σ ratings) / (C + n)`, where `m` = platform mean and `C` = 10, scaled to 0–1 | Weights re-balanced at v1.1 [CONFIRM] |

**Notes on the formulas:**
- The smoothing in reliability means a new doctor with few consults is not punished or favoured by chance.
- The Bayesian average stops a doctor with two 5-star reviews outranking one with 200 reviews averaging 4.6.

**Step 3: ties.** Order by a hash of (doctor ID + today's date), so equal doctors rotate daily instead of one always appearing first.

**Closed beta:** sort by **distance only** (scope-v1.0.md beta cut). The full score switches on at public launch.

**Why this choice:**
- Simple and explainable.
- Hard to game: a doctor can't pay, and reliability is measured from attendance.
- Fair to new doctors (smoothing).
- Meets the transparency duty (E-Commerce Rules: explain the main ranking parameters) and the NMC rules on promotion.

## 3. Notification fallback (S-141)

**Priority classes:**

| Class | Messages | Channel order | Quiet hours (K-MSG-03) |
|---|---|---|---|
| P0 Security | OTP, new-device alert, breach notice | **SMS first** (second SMS provider on failure); push in parallel for new-device alerts | **Exempt** |
| P1 Clinical-critical | Medicine reminders, consult starting in 10 min | **Push first**; **SMS if no app receipt within 5 min**, for items the patient allowed SMS for | Exempt (patient's chosen times) |
| P2 Booking | Confirmed, reminder 24 h and 2 h, cancelled, refund | Push + SMS | Held 21:00–08:00 except the 2-hour reminder |
| P3 Informational | Prescription ready, news (opt-in) | Push only; email for receipts | Held |

**Algorithm:**
1. **Dedupe** by dispatch ID (idempotent).
2. **Group** reminders due within a 5-minute window into one message (E-REM-03).
3. **Apply the privacy text rules at send time:** neutral text, unless P-MEDNAMES (K-DP-09).
4. **Send on the first channel.**
5. **Wait for a receipt.** For push, **the app sends an acknowledgement when it shows the notification.** Push services do not reliably report delivery to the device, so we do not depend on them for this.
6. **No receipt in the window** → next channel.
7. **Provider errors:** retry with exponential backoff and full jitter, inside the class's time budget (5 min for P1).
8. Then the **dead-letter queue** and an alert.
9. **The on-device backup schedule** (S-165) still fires P1 reminders offline. The app suppresses a duplicate if the push already arrived.

**Why this choice:**
- No silent loss.
- SMS only where it matters, which keeps cost down and stays within the DLT template rules.
- Privacy text is decided at send time, so a consent change applies at once.

## 4. Recorded in the Performance sheet

- Row 10 (notification fallback): priority classes + app acknowledgement + SMS fallback window.
- Row 12 (rules engine): two-layer table, per-item consult type, fail closed.
- Row 21 (ranking): filter + weighted score + Bayesian rating from v1.1 + daily-rotated tie-break; distance only in beta.

## 5. Decisions [CONFIRM]

1. Ranking weights 0.45 / 0.35 / 0.20 and the reliability factor (depends on the A11 decision).
2. Bayesian constant C = 10 for v1.1.
3. A 5-minute push-to-SMS fallback window for medicine reminders.
