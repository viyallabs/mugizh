# Security code review: backend build (2026-11-17)

**Who this is for:** the founder and the security owner (evidence for K-DOD-01 "Claude review against contracts done", K-SEC-07, and the VAPT scope S-183), and developers fixing findings.
**How it was done:** independent reviewers (fresh context, read-only) reviewed the code against `CLAUDE.md`, the contracts and the security designs; Opus verified each finding in code, fixed it with a regression test, and re-ran the suites. Tests run on real PostgreSQL 17 + PostGIS 3.5 in containers.

## 1. Foundation (platform, audit, crypto, identity, records, HTTP layer)

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| R0 | Critical | Found during the build, before the review: wrong OTP attempts hit a SQL type error and were **not counted**, so the 5-attempt lockout could be bypassed | Fixed | `TestTC55OTPAbuseControls` (now asserts the exact error on each wrong attempt) |
| R0b | Critical | Found by Opus while reviewing: holder of a recycled number could sign into the **pro** app on the old patient account, enrol their own authenticator and approve their pending patient device (E-DP-22 bypass) | Fixed: any new device on an existing account is pending unless it is a pro device on an account with a confirmed authenticator; restricted sign-ins add no roles | `TestRecycledNumberCannotEscalateViaProApp` |
| H1 | High | Crypto cache handed out a data key that eviction could zero while another request was still encrypting, so data could be sealed under an all-zero key | Fixed: the cache holds immutable AEAD objects, never raw key bytes | `TestConcurrentSealWhileEvicting` (run with `-race`) |
| H2 | High | Device lookup ignored the app: a pro device row (active because the account had TOTP) made the same key active in the patient app | Fixed: devices bound per app (lookup and unique index) | `TestReviewFixesIdentity` |
| M1 | Medium | A consent/grant snapshot read before a revoke could be cached after it (stale access up to 5 s) | Fixed: per-patient generation counter; one REPEATABLE READ snapshot | `TestTC01...` (same and second instance) |
| M2 | Medium | Per-IP limits keyed on full IPv6 addresses (rotation within a /64 escapes limits); `TRUSTED_PROXIES` defaulted to 0 | Fixed: IPv6 bucketed by /64; `TRUSTED_PROXIES` required outside local/test/dev | `TestIPBucketGroupsIPv6By64` |
| M3 | Medium | Anyone could lock any number out of sign-in (5 wrong codes on a challenge they requested) | Fixed: lockout keyed by (number, client network); per-number request limits still cap guessing at 50 a day | `TestReviewFixesIdentity` |
| M4 | Medium | Replay cache stored error responses (a 403 "step-up required" replayed for 24 h) | Fixed: only 2xx responses are stored | `TestHTTPEndToEnd` |
| M5 | Medium | Just-in-time grant trusted a caller-supplied approver ID | Fixed: requester and approver roles verified through identity (`security_owner`); no role checker = no grant | `TestReviewFixesRecords` |
| L1 | Low | A crashed request left its idempotency key "in progress" for 24 h | Fixed: 2-minute lease; cleanup on panic | — |
| L2 | Low | Replay-cache hash omitted the query string and acting role/clinic headers | Fixed | — |
| L3 | Low | Key destruction evicts only the local cache; other instances may use a destroyed key for up to 5 minutes | **Accepted:** destruction runs when the RET-18 hold ends, so minutes do not matter; data written meanwhile is unreadable | — |
| L4 | Low | One service key protects all idempotency bodies (GCM nonce budget ~2^32) | **Accepted for now:** far below the budget; rotate service keys yearly (encryption-design §6) | — |
| L5 | Low | Sign-in did not prove possession of the device key | Fixed: the device signs the challenge ID | `TestReviewFixesIdentity` |
| L6 | Low | Refresh did not check account status; no grace for concurrent refresh retries | Status check fixed; the no-grace rotation is **accepted** (apps must serialise refreshes) | `TestRefreshNeedsDeviceProofAndDetectsReuse` |
| L7 | Low | Phone validation was E.164 only (number variants = duplicate accounts, limit bypass) | Fixed: full-number pattern from the country pack | `TestReviewFixesIdentity` |
| L8 | Low | The gate trusted callers to block restricted sessions | Fixed: `Actor.Restricted` denied (and audited) inside the gate | `TestReviewFixesRecords` |
| L9 | Low | No purge of OTP challenges, counters, locks, old sessions | Fixed: `identity.PurgeExpired` job | — |
| L10 | Low | Unbounded unconfirmed MFA rows; audit role hard-coded | Fixed (purge; role "professional") | — |
| L11 | Low | A finished day without an anchor passed verification (possible tail truncation) | Fixed: reported as a break with an alert | `TestMissingAnchorIsABreak` |
| — | Medium | Idempotency cache would have stored responses that carry tokens or the TOTP secret | Fixed before review: credential endpoints are never cached (D-31) | — |

**Clean areas confirmed by the reviewer:** OTP generation and storage; token generation, rotation and reuse detection; the device-signed refresh proof; the policy check (deny by default, app/MFA/restricted/step-up order); start-up refusal of undeclared operations; AES-GCM binding and fail-closed errors; tombstone-first destruction and replay; logging allow-list and scrubber; audit chain and grants; parameterised SQL everywhere; security headers and size limits.

## 2. Agent-built modules (verification, directory, payments, notifications, rules engine, booking, records rights, admin, composition root)

No Critical findings. Status of each:

| # | Sev | Finding | Status |
|---|---|---|---|
| H1 | High | Erasure destroys the patient key when the RET-18 hold ends even if another module keeps legal-retention records | **Mitigated by key design:** clinical records use care keys (RET-02), not the patient key; a new contract check (`TestH1_ClinicalUsesCareKeys`) fails the build if clinical code ever seals with the patient key |
| H2 | High | JIT approval created the grant before claiming the request (double grants, grants without approval records) | Fixed: request claimed first; a grant whose approval record fails is revoked at once (`TestReviewH2ConcurrentApproval`, `TestReviewH2FailedGrantReturnsToPending`) |
| H3 | High | Booking trusted only directory's event-fed copy of verification status, so an expired licence stayed bookable until the job ran | Fixed: live verification check at hold, reschedule and capture; capture for a doctor no longer verified cancels with RF04 full refund (`TestReviewH3*`) |
| H4 | High | Rules engine and drug classification could load drafts in production | Fixed in the composition root: production loads only an approved rule file with the pack checksum and refuses a non-approved classification |
| M1 | Medium | Vendor webhook and scheduler callback were outside the rate limiter | Fixed: per-network limit on all self-authenticating callbacks |
| M2 | Medium | Callers could raise a message to critical; held reminders could be sent hours late; promotional quiet hours switchable | Fixed (`TestReviewM2*`) |
| M3 | Medium | Clinic payment link: no audit, secret references not namespaced, silent overwrite; checkout URL not validated; reconciliation not scheduled | Fixed: audited create/update, clinic-namespaced secret refs, https-only checkout, daily reconciliation job (`TestReviewM3*`) |
| M4 | Medium | Erasure consumers in other modules not built | Fixed 2026-11-17 (D-32): identity, booking, payments, clinical, notifications and consult report; an erasure completes only when all seven modules have reported |
| M5 | Medium | A clinic admin could attach any verified doctor (lead doctor or practice) without the doctor's consent and collect payments | Fixed: invitation that the doctor must accept before listing (`TestReviewM5*`) |
| M6 | Medium | No upload quota; expired exports never purged; 403 instead of 404 on a foreign document link | Fixed: 200 files / 500 MB per patient, no orphan blobs, export purge, 404 (`TestReviewM6*`) |
| M7 | Medium | Doctor evidence limited by the 1 MB body cap; no malware scan on evidence | Fixed: route-specific body limit; every evidence file is scanned before it is encrypted or stored, and a scanner error or timeout rejects it (`TestReviewM7EvidenceMalwareScan`) |
| Low | Low | Admin text not scrubbed, IDs not validated, review row overwritable; path-like photo refs; payments tables deletable; queue listing not audited | Admin items and opaque photo/document refs fixed; payments module role has no DELETE or TRUNCATE on any financial table (migration 0002, tested); reviewer queue listing audited (`verification.queue_listed`) |

## 3. Verification after fixes (2026-11-17, 20:40 IST)

- `go test -count=1 ./...`: all 29 backend packages and all 4 scheduler-core packages pass on real PostgreSQL 17 + PostGIS 3.5; `gofmt` and `go vet` clean.
- Contract checks (`backend/archtest`) pass, including the new care-key check.
- `gosec` (high severity) and `govulncheck`: no findings in either service. Gitleaks: no secrets in tracked files (the only hits are the git-ignored local `.env` the setup script generates).
- Spectral: 0 errors on all 10 OpenAPI specs.
- Local stack: every migration applies; API and worker start; readiness, OTP request, unauthenticated refusal and the prescription verification page answer correctly; no worker errors.

## 4. Second round (2026-11-18): consult, profile, authorisation suite

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| S1 | Medium | The generated authorisation suite (S-116, TC-57) found that the generated wrappers parse the body and parameters **before** the policy check, so a caller with the wrong role got 400 instead of 403 and the server parsed bodies for unauthorised callers | Fixed: an HTTP-level gate applies the operation policy before the replay cache and any decoding (D-43); the strict-handler check remains as a second layer | `TestS116GeneratedAuthorisationSuite` (every operation of every spec, as anonymous, patient and MFA doctor) |
| S2 | Medium | Prescriptions would have printed the doctor's registration details from the live case row, which a re-verification draft edits before any reviewer sees it | Fixed: reviewed snapshot at the final "verified" decision (D-44) | `TestPrescriberFactsAreTheReviewedSnapshot` |
| S3 | Medium | Signing had no check that the prescriber details and the patient's name and age were present | Fixed: signing refuses and audits | `TestSigningRefusesMissingPrescriberOrPatientDetails` |
| S4 | Medium | The facts given to the rules engine at signing were a stub; composing them carelessly could let identity count for a consult that never happened | Fixed: identity counts only when both sides connected and the call is live or ended "completed", no minor flagged; in person blocks until an identity step exists (D-39); unknown modes are errors | `TestConsultFactsFailClosed` |
| F1 | High | The consult join response (with the vendor join token) would have been stored in the idempotency replay cache for 24 h | Fixed: `/join` is never cached (D-31 family) | `TestCredentialRoutesAreNeverCached` |
| F2 | Medium | Either side could record a technical failure from any state, ending a consult early or dodging a no-show | Fixed (D-48) | `TestReviewF2TechnicalFailureNeedsAnAttempt` |
| F3 | Medium | Identity confirmation was not bound to the profile; a later name or date-of-birth change would have printed unconfirmed details | Fixed (D-49) | `TestReviewF3IdentityChangeTime`, `TestConsultFactsFailClosed` |
| F4 | Medium | The pre-join consent and "in the country now" answer was overwritten with no history | Fixed: every answer and its consent text version are audited | `TestReviewF4PreJoinAnswersAreAudited` |
| F5 | Medium | Nothing consumed `consult.ended`, so outcomes never reached booking or the refund policy | Fixed (D-46) | `TestConsultOutcomesReachTheAppointment` |
| F6 | Medium | Booking was told "doctor no-show" before consult re-checked under its lock | Fixed (D-50) | `TestTC25DoctorNoShow` (consult) |
| F7 | Low | The policy gate matched the decoded path while chi routes on the raw path | Fixed: the gate matches exactly as chi does | — |
| F8 | Low | Profile facts are read before clinical's gate | Accepted: every clinical path authorises (and audits) before returning anything; nothing is logged | — |
| F9 | Low | In-person prescriptions are blocked by P02 | Accepted as fail-closed (D-39) | — |
| F10 | Low | A problem code and title named the country | Fixed: `consult.patient_location_not_allowed`, country-neutral wording | — |
| F11 | Low | Participant references (RET-12) were never purged | Fixed (D-51) | `TestReviewF11ParticipantRefsExpire` |
| F12 | Low | The minor flag did not update `updated_at` | Fixed | — |

**Verification after the second round (2026-11-18, 10:45 IST):** all 32 backend packages and all 4 scheduler-core packages pass on real PostgreSQL 17 + PostGIS 3.5 (`go test -count=1 ./...`), `gofmt` and `go vet` clean; contract checks pass; `gosec` (high) clean and `govulncheck` reports nothing reachable in either service; Gitleaks finds nothing in tracked files (only the git-ignored local `.env`); Spectral 0 errors on all 10 OpenAPI specs; local stack boots with every migration applied, readiness 200, OTP 202, unauthenticated consult/profile/join calls 401 before any parsing, prescription check page `not_valid`, worker 0 errors.

## 5. Third round (2026-11-20 and 21): sharing, calendar, closures, receipts, messages, end-to-end journeys

**End-to-end journeys** now drive the fully assembled API (doctor and clinic onboarding with four-eyes review, search, booking, payment, receipt, messages, reminders, video consult, identity confirmation, history share and revoke, prescription blocked by the empty drug table, completion; refunds RF07, RF04 by closure and RF03 by doctor no-show). They found one serious gap, now fixed:

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| J1 | High | No doctor, clinic admin or front desk member could record a D-* consent: the consent endpoints admitted patients only, so doctor verification was impossible through the API | Fixed (D-66): professionals manage their own D-* consents, patients P-*; each consent is recorded with the notice in force for its kind | `TestJourneyVideoConsultation` |

**Independent review** of the new code (no Critical or High):

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| F1 | Medium | A test reminder could show the doctor's free-text test name on the lock screen (K-DP-09) | Fixed: test reminders are always neutral | `TestReviewF1TestRemindersStayNeutral` |
| F2 | Medium | Turning an item's reminders back on saved the switch before scheduling; a scheduler failure left it on with nothing scheduled | Fixed: schedule first, save last, optimistic check against concurrent taps | `TestS155_PerItemReminderSwitch` |
| F3 | Medium | The doctor's document list did not re-check Verified on each read (K-CS-01) | Fixed | `TestS168HandlersFailClosedAndMapErrors` |
| F4 | Low | No cooldown after a declined extension request | Fixed: 24 h cooldown ([CONFIRM]) | `TestTC02ExtensionNeedsPatientApproval` |
| F5 | Low | Consent audits always said "patient"; a missing notice setting did not stop consent | Fixed: real acting role; start-up refused without both notice versions | — |
| F6 | Low | Payments clinic routes ignored the acting clinic | Fixed | — |
| F7 | Low | Support lookup limit not atomic; refusals not audited | Fixed: per-staff lock; refusals audited (no ticket store exists to check ticket references) | `TestS172SupportAccountLookup` |
| F8 | Low | The test clock could in principle be set outside tests | Fixed: refused outside local, test and dev | — |
| F9 | Low | Registration ID not checked for identity numbers or length | Fixed | `TestReviewF9TaxIDChecks` |
| F10 | Low | A double tap could create two shares | Fixed: check and create in one transaction under a transaction-scoped lock (a first fix that held a connection while waiting was caught by the test and replaced) | `TestReviewF10ConcurrentShareTapsCreateOneGrant` |
| F11 | Low | Approving an extension recorded the consent outside the grant's transaction | Fixed: same transaction | — |
| F12 | Low | Closure-released holds might be told "cancelled by the clinic" | No change: they emit `appointment.expired`, which sends no message | — |
| F13 | Low | No document for captures before the profile exists; calendar-year label | Label fixed; the rest is [VALIDATE-CA] (D-56) | `TestFinancialYearLabels` |
| G1 | High (gosec) | scheduler-core event IDs folded the round into one byte | Fixed: decimal round | — |

**Verification after the third round (2026-11-21):** all 32 backend packages and all 4 scheduler-core packages pass on real PostgreSQL 17 + PostGIS 3.5, including the authorisation suite (129 operations x 3 callers) and both end-to-end journeys; `gofmt` and `go vet` clean; `gosec` (high) 0 issues and `govulncheck` nothing reachable in both services; pack checksums current; Spectral 0 errors on all 11 specs; Gitleaks finds nothing in tracked files. The local stack migrated its existing database forward through every new migration; API and worker started; every new route refuses an anonymous caller with 401 before parsing; the worker ran all its minute jobs for 80 seconds with 0 errors and 0 warnings.

## 6. Fourth round (2026-11-21 and 22): offline reminders, attack tests, synthetic country

Scope: S-165 backend (device schedule, replace key, silent refresh push, remote sign-out wipe), S-181 attack tests, S-174 journey on the synthetic country XX, D-73 push audiences. Reviewed by Opus line by line (the planned independent agent stopped before reporting).

- **R4-1 (High, fixed before merge):** the first version of the sign-out push said only `{"signout":"device"}`. If the same phone signed in again before a delayed wipe arrived (new device ID, same OS push token), the push would have wiped the fresh session. It now carries the revoked device ID and the app wipes only on a match (D-69).
- **R4-2 (Medium, fixed):** a patient reminder could reach the same person's pro-app device (identity returned every app's devices). Templates now have an audience and identity filters by app; reminder templates must be patient-only (D-73).
- **R4-3 (Medium, fixed):** revoked devices kept their encrypted push token for ever when the wipe push never ran. The identity purge now clears it 24 hours after revocation.
- **R4-4 (Low, accepted):** an offline lost phone shows its backup reminders (neutral, or named with P-MEDNAMES) until it reconnects or 72 hours pass (D-69 residual risk, [CONFIRM]).
- **R4-5 (Low, accepted):** the developer tool image `tools/devimage` runs as root (Trivy AVD-DS-0002) because the tests drive Docker; it is never shipped. Production images, when built for staging, must run as a non-root user.
- **Checked and fine:** the device schedule asks only about the caller, needs a positive P-REMIND check (a failed check is 503), names a medicine only with P-MEDNAMES and discreet mode off, drops denylisted text, and mirrors the server's grouping, snooze and quiet-hours timing (tested against the real push path); refresh requests merge per patient, leases prevent double sending, a request arriving mid-send stays pending; erasure and the one-day purge remove refresh rows; the wipe target must belong to the revoking user; the attack tests found no bypass (D-71).
- **Open naming item:** "India" in API field names that mean the pack's consult country (D-72, [CONFIRM]).

**Verification (2026-11-22):** 33 backend and 4 scheduler-core packages pass, including the authorisation suite (130 operations), the attack test and three journeys (video, refunds, prescription on XX); gofmt and vet clean; gosec 0 issues in both services; govulncheck nothing reachable; Trivy 0 vulnerabilities and 0 secrets (one accepted dev-image finding, R4-5); Spectral 0 errors on every spec; Gitleaks only the git-ignored local `.env`.

## 6a. Fifth round (2026-11-23): threat-model verification (S-178) and restore drill (S-185)

Every mitigation for TM-01 to TM-51 was traced to code and to a test whose body asserts it ([threat-model-verification.md](../security/threat-model-verification.md)). Two Sonnet agents mapped the threats; Opus checked each cited test and every finding before acting. Fixes were test-first.

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| R5-1 | **High** | TM-13: staff could mark an online consult completed or patient no-show (0% refund) with no join evidence | Fixed (D-78) | `TestTM13StaffCannotMarkOnlineConsults` |
| R5-2 | **High** | TM-32/33: no patient step-up existed (pro TOTP only), so export, erasure and undo answered 403 through the API; the tests had injected the step-up time | Fixed: SMS step-up to the phone on file, session-bound, shared limits and lockout (D-75) | `TestTM32PatientStepUpThroughTheAPI` |
| R5-3 | **High** | E-DP-04: no erasure log existed, so a restore inside the undo window or hold showed an erased account again | Fixed: erasure journal replayed at start-up (D-74) | `TestTC04ErasureJournalReplayAfterRestore` and 4 more |
| R5-4 | High | No Alert hook was wired in production (break-glass, chain break, SMS cap, refresh reuse, dead letters, payment mismatches) | Fixed: `ops.alert` sink (D-76) | `TestS178AlertHooksAreWired` |
| R5-5 | Medium | TM-27: links and markup accepted in names; the send filter missed bare domains | Fixed (D-77). The agent's first detector would have refused "M.Ch" and "Opp.Bus Stand" in degree and address; Opus split strict (names) and loose (other fields) rules | `TestTM27*`, `TestHasLinkOrMarkup*` |
| R5-6 | Medium | TM-33: notifications erasure step 1 could delete the queued erasure notice | Fixed | `TestTM33ErasureNoticeSurvivesStep1` |
| R5-7 | Medium | TM-04: new pro device with an authenticator sent no SMS-14 | Fixed | `TestTM04ProNewDeviceIsNotified` |
| R5-8 | Medium | TM-39: key-service outage was a bare 500 with no alert | Fixed: 503 + Retry-After + alert | `TestTM39KeyServiceOutageIs503AndAlerts` |
| R5-9 | Low | The pro idle-timeout test passed on access-token expiry alone; the panic path and brace injection had no tests | Tests added | `TestTM04ProIdleTimeoutAtRefresh`, `TestTM40RecoverLogsNoRequestData`, `TestTM41BracePayloadInStoredVariableRefusedAtRender` |
| R5-10 | Low | The erasure journal accepted an entry with an unknown event name (found in Opus's review of its own change) | Fixed: forged entries stop start-up | `TestErasureJournalRefusesForgedEntries` |
| R5-11 | Low | A first version of the alert sink passed a named constant as the log message; the K-DP-04 archtest refused it | Fixed (literal event name) | `TestKDP04_LoggingDiscipline` |

**Checked and fine:**
- No user value is ever formatted into SQL. Every `fmt.Sprintf` or concatenated query uses constant fragments and `$n` placeholders.
- No server-side fetch of a user URL.
- Strict JSON everywhere.
- No template engine.
- Webhook, refund, ledger, rules-engine, signing, consent, audit-chain and upload controls all hold (section 2 of the verification document).

**Verification (2026-11-23):**
- Tests: all backend packages pass on PostgreSQL 17 + PostGIS 3.5, including the authorisation suite, the attack test and four journeys.
- Formatting and static checks: gofmt and vet are clean; gosec 0 issues; govulncheck nothing reachable.
- API spec: Spectral 0 errors (128 existing description warnings).
- Secrets: Gitleaks finds only the git-ignored local `.env`.
- Local stack: migrated forward (records 0005, identity 0006); the API started with the start-up replay; readiness 200; new routes answer 401 to anonymous callers before parsing.

## 6b. Sixth round (2026-11-23): reviews module (S-209 backend, S-214)

Built by a Sonnet agent to a fixed brief (D-80 to D-82), reviewed line by line by Opus.

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| R6-1 | **Medium** | A doctor who booked their own practice as a patient could rate themselves (manipulated reviews; NMC guidelines, CD-08) | Fixed: a visit whose doctor is the reviewer is not eligible | `TestCD08DoctorCannotReviewThemselves` |

**Checked and fine:**
- Unknown or another patient's appointment answers 404, through booking's public interface. There are no cross-schema reads.
- One review per visit is enforced by a unique constraint, with `ON CONFLICT` under concurrency.
- The aggregate moves in the same transaction as create, withdraw and erasure, and is CHECKed non-negative.
- Text is trimmed and checked for control and format characters, identity numbers, phone numbers, e-mail addresses, links and markup before it is sealed with the patient key under a row-bound AAD.
- Text is never logged, never published and returned only to its author. Audit rows carry no text.
- The public rating needs a Verified doctor (K-CS-01) and has no commercial input (CD-05).
- Erasure step 1 deletes and adjusts the aggregate; step 2 sweeps; both are idempotent.
- Routes exist only with the pack flag (IN off → 404). The generated authorisation suite covers the new operations.

**Residual:**
- Withdrawing and re-reviewing inside the 14-day window is allowed (D-80).
- The platform mean is computed on each rating read, which is fine at v1.1 volumes. Add a cached platform row if `GET /v1/doctors/{id}/rating` becomes hot.

## 6c. Seventh round (2026-11-23): agent reviews of S-095, S-189 and TM-06

| # | Sev | Finding | Status | Regression test |
|---|---|---|---|---|
| R7-1 | **Medium** | Identity and phone numbers split across line breaks or punctuation, or typed in Devanagari digits, passed every free-text check (K-DP-05), in every module | Fixed (D-86): digits normalised everywhere; strict joining for feedback and review text | `TestDigitHardening`, `TestS189ReviewFixesRefuseHiddenNumbersAndDomains` |
| R7-2 | Medium | Feedback erasure step 1 reported "deleted" or "restricted RET-09" depending on whether feedback existed | Fixed: one rule (RET-09 whenever grievances are kept; "none" when nothing is held) | `TestS189ErasureOutcomeIsConsistent` |
| R7-3 | Medium | A user's own feedback list returned blank messages on a key outage instead of a 503 (TM-39) | Fixed: only a destroyed key is tolerated | — |
| R7-4 | Low | Feedback code fields (app version, OS version, error code) could carry a number | Fixed | `TestS189ReviewFixesRefuseHiddenNumbersAndDomains` |
| R7-5 | Low | A 40-bit feedback reference clash gave a 500 | Fixed: retried with a new reference | — |
| R7-6 | Low | Bare domains accepted in feedback | Fixed: strict link rule | same |
| R7-7 | Low | A client's traceparent decided whether it was sampled | Fixed: server-side random sampling | `TestS095SamplingIgnoresTheTraceID` |
| R7-8 | Low | An aborted request left its span unended | Fixed: deferred | — |
| R7-9 | Low | OpenTelemetry modules marked indirect in go.mod | Fixed: `go mod tidy`; CI job `go-mod-tidy` added | — |

TM-06 (booking audit context) and the alert catalogue in production-readiness.md were checked and found correct.

## 7. Open before real patient data (also in the build decisions log)

Staff SSO (D-21); cloud KMS, S3, SMS gateway, e-signature, payment aggregator, video and malware-scan adapters (only fakes exist); the CA-approved tax file (patient documents stay off in production) and our subscription invoices; the doctor-approved drug table and rules; in-person identity confirmation (D-39); follow-up and refill facts (D-41); account recovery without a device (E-DP-23, [CONFIRM]); the [CONFIRM] and [VALIDATE] items in the decisions log; independent VAPT (S-183).
