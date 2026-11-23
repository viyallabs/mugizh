# Threat-model verification v1.0 (S-178)

**Story:** S-178 · **Contracts:** K-SEC-07 (also K-DP-02, K-DP-04, K-DP-07, K-SEC-03, K-SEC-04, K-CS-05) · **Edge cases:** E-DP-* · **Compliance:** C03 · **Date:** 2026-11-23
**Status:** In Testing. The backend evidence is complete; the items owned by infrastructure, mobile, vendors or people stay open until their stories run. The story cannot be Done until staging exists (K-DOD-01, blocker V1).

**Who this is for:** the security owner, the founder (approval), and the VAPT auditor (S-183) as a starting map.

**Method:** every mitigation listed for TM-01 to TM-51 in [threat-model-v1.0.md](threat-model-v1.0.md) was traced to the code that implements it and the test that proves it. A claim counted as "tested" only after the test body was read and found to assert the behaviour. Where a backend mitigation was missing or untested, a failing test was written first and then the fix (section 3). All tests run on real PostgreSQL 17 + PostGIS 3.5 through `tools/dev.sh`.

**Status values:**
- **Tested:** implemented, with a test that asserts it.
- **Built:** implemented, with no dedicated test.
- **Open (owner):** not in the backend, or waiting on a decision or vendor.
- **Residual:** accepted for now, with the reason.

## 1. Result

**No High threat is open in the backend scope.** Two High gaps were found and fixed in this story: TM-13 (a clinic could mark an online consult "patient no-show") and TM-32/33 (patients could not step up at all, so export and erasure were unusable through the API). Every remaining open item is outside the backend or waits on a person (section 4).

## 2. Map (TM-01 to TM-51)

Test names are Go test functions in `backend/` unless marked *(CI)*.

| TM | Threat (short) | Backend mitigation and evidence | Status |
|---|---|---|---|
| TM-01 | OTP guessing, SIM swap | 6-digit code, 5 min, 5 attempts then lock with doubling backoff, device binding with key proof, SMS-14 on a new device, restricted session until re-verified: `TestTC55OTPAbuseControls`, `TestTC16RecycledNumberNewDevice`, `TestReviewFixesIdentity` | Tested |
| TM-02 | SMS pumping | Per-number, per-IP and global daily caps with kill switch; country allow-list from the pack: `TestTC55OTPAbuseControls`, `TestSMSCapTripsKillSwitch`. App attestation, per-device and IPv4-subnet limits are not built | Tested; **Open** (mobile and S-111: attestation) |
| TM-03 | Recycled phone number | New device on an existing account is restricted until approved from a verified device: `TestTC16RecycledNumberNewDevice`, `TestRecycledNumberCannotEscalateViaProApp`. A dormancy check is not built; device binding already covers a new holder on a new phone | Tested; Residual (dormancy) |
| TM-04 | Doctor account takeover | TOTP MFA on every pro sign-in, replay refused, step-up to sign (P08), e-signature (P09, fake), prescription-volume alert, idle timeout **at refresh**, **SMS-14 for every new pro device (fixed)**: `TestTC56DoctorMFA`, `TestTM04PrescriptionVolumeAlert`, `TestTM04ProIdleTimeoutAtRefresh`, `TestTM04ProNewDeviceIsNotified` | Tested; passkeys **Open** (D-21) |
| TM-05 | Stolen phone | Remote sign-out with wipe push, 15-min access tokens, device-bound refresh with reuse detection: `TestTC07RemoteSignOut`, `TestS165RemoteSignOutTellsTheLostPhoneToWipe`, `TestRefreshNeedsDeviceProofAndDetectsReuse`, `TestTM04ProIdleTimeoutAtRefresh` | Tested; app lock **Open** (mobile) |
| TM-06 | Repudiation | Audit event in the same transaction as each change: marks, cancels, signing, consent, rights. Booking audit rows carry the request ID but no device | Built; Residual (device on booking audits) |
| TM-07 | IDOR | Deny-by-default gate before parsing; 404 for others' resources; generated suite over every operation: `TestS116GeneratedAuthorisationSuite`, `TestS181AttackAuthorisationAndConsent`, `TestOwnershipOthersGet404` | Tested |
| TM-08 | Slot squatting | Max 2 holds, 15-min expiry, P-CARE needed, pay-at-clinic cap: `TestESEC04MaxTwoActiveHolds`, `TestTC22ParallelHoldsOneWinner`, `TestESEC04PayAtClinicCap`. Generic per-user and per-IP limits only (per instance) | Tested; hold-ratio alert **Open** (S-095) |
| TM-09 | Forged or replayed webhook | Per-clinic signature first, event de-duplication, amount and currency match, confirm only from webhook or poll, daily reconciliation: `TestTC58_*`, `TestTC30_*`, `TestTC28_*`, `TestS146_Reconciliation` | Tested (fake vendor) |
| TM-10 | Client changes fee or clinic | Server-side amount from the hold; no amount in the request: `TestFeeAndPolicyVersionStoredWithBooking` | Tested |
| TM-11 | Merchant credentials leak | Only `secretref:` references stored (CHECK constraint): payments tests. Secrets manager and rotation are infra | Tested; **Open** (infra) |
| TM-12 | Repeated or inflated refunds | Versioned policy, one refund per booking and rule, never above the amount paid, append-only ledger: `TestS133_*`, `TestRefund_OnePerAppointmentAndRule`, `TestS132_LedgerAppendOnlyAndBalanced`. No manual-refund path exists; when one is built it needs two people | Tested |
| TM-13 | False no-show or closure | Online outcomes only from consult (both connected, identity confirmed, short-call flag), **and staff marks refused on online appointments (fixed)**: `TestTC27*`, `TestS148PatientNoShow`, `TestTM13StaffCannotMarkOnlineConsults`. A 24 h patient dispute is not built | Tested; dispute **Open** (FAIRNESS-01, F2) |
| TM-14 | Search reveals location or phone | Location in the POST body, never stored or logged; no phone field in the directory: `TestE_GEO_SearchLocationNeverStoredOrLogged`, `TestHTTPPublicSearchAndProfile` | Tested |
| TM-15 | Impostor joins the video | Join tokens bound to participant, room and window; identity confirmed by the doctor: `TestS136*`, `TestTC34*` | Tested (fake video) |
| TM-16 | Video token leaks | Short-lived bound tokens; a room that reports recording is refused: `TestS136RoomReportingRecordingIsRefused`. Room lock and media region are vendor features | Tested; **Open** (vendor, S-039) |
| TM-17 | Rules-engine bypass | Server-only evaluation, re-run at signing, fail closed: `TestTC59_BlockedDrugIsRefusedAtSigning`, `TestT20_UnknownCombinationFailsClosed`, `TestPrescriptionLevelChecksAreNotSkipped` | Tested |
| TM-18 | Rule-file tampering | Checksums verified at load, draft packs refused in production, immutable versions stored on each prescription: `TestTC59BadChecksumRefused`, `TestCommittedPackChecksumsAreCurrent`, `TestDraftPackRefusedInProduction`. CODEOWNERS has placeholder names | Tested; **Open** (GitLab code-owner settings) |
| TM-19 | Doctor denies a prescription | Step-up plus e-signature over the hash; signed rows immutable; audited: `TestTC56_SigningNeedsStepUpAndESignature`, `TestS150_SignIssuesImmutableEncryptedVersion` | Tested (fake e-sign, N5) |
| TM-20 | Forged prescription PDF | 256-bit verification reference (hash stored), rate-limited page, unknown references reveal nothing: `TestTC60_VerificationPage`. QR on the PDF waits for the PDF library | Tested; **Open** (V4 PDF ADR) |
| TM-21 | Unauthorised history read | T1–T4 on every read, audit-or-refuse, instant revocation across instances: `TestTierScopes`, `TestTC01*`, `TestAuditFailureRefusesRead`, `TestAccessLogShowsReadsByPeopleOnly` | Tested |
| TM-22 | Link forwarded | 5-min, single-use, user-bound links; hash only; `attachment`, `no-store`, `nosniff`: `TestTC15SecureLinks` | Tested |
| TM-23 | Health details on lock screen or SMS | Neutral wording by default, medicine names only with P-MEDNAMES: `TestTC09_*`, `TestS165WordingFollowsMedNamesAndDiscreet` | Tested; lock-screen display **Open** (mobile) |
| TM-24 | Health data in scheduler-core | Opaque references only; schema contract test; Spectral rule: `TestB10SchedulerSchemaHasNoFreeText`, `TestB10_SchedulerCoreIsolated`, `TestHTTPClient_SendsOnlyAllowedFields`, *(CI)* `no-health-fields-in-scheduler` | Tested |
| TM-25 | Push or SMS outage | Push then SMS fallback for critical items, 72 h device backup, dead-letter **with an alert (wired, fixed)**: `TestTC40_*`, `TestRetriesThenFallbackThenDeadLetter`, `TestS178AlertHooksAreWired`. A second SMS provider is not chosen | Tested; **Open** (V2) |
| TM-26 | SMS phishing as us | Only registered templates; no free-text SMS path. DLT headers and PE-ID wait for the real adapter | Built; **Open** (S-102, V2) |
| TM-27 | URL or script in a name reaching a message | **Fixed:** names refuse links and markup at input (strict rule for names that reach messages, loose rule for degree, address and timings); every SMS, push and e-mail variable is checked at send and in the SMS adapter: `TestHasLinkOrMarkup`, `TestHasLinkOrMarkupLoose`, `TestTM27LinksAndMarkupRefusedInProfileNames`, `TestTM27LinkAndMarkupVariablesRefusedAndNothingSent`, `TestTM27ResolverLabelWithLinkIsDropped`, `TestValidateRefusesLinksAndMarkup` | Tested |
| TM-28 | Impersonation with a real registration | Council required, register name match, a failed check needs a second reviewer: `TestTC43_CouncilRequired`, `TestTC42_TC62_*`. Telling the registered doctor when a profile goes live needs the register's contact data | Tested; **Open** (S-111 register adapter) |
| TM-29 | Reviewer approves a fake | Reason codes, append-only decisions, second reviewer for overrides and rejections, no self-review: `TestS129_*` | Tested |
| TM-30 | Evidence leaks | Evidence sealed per file, purged 90 days after decision, prohibited ID deleted at once: `TestRET08_*`, `TestTC21_ProhibitedIDDocumentDeletedAtOnce` | Tested; bucket policy **Open** (infra) |
| TM-31 | Suspended licence | 12-month and expiry re-verification, gate on every list, book and sign: `TestS160_*`, `TestTC44_*`. A complaint-triggered check exists but is not called from grievance intake yet | Tested; **Open** (wire grievance → `TriggerReverification`) |
| TM-32 | Someone else exports or erases | **Fixed:** patient step-up by SMS code to the phone on file, bound to the session, same limits and lockout as sign-in; export, erasure and undo need it: `TestTM32PatientStepUpThroughTheAPI` (real HTTP), `TestTC63ErasureNeedsStepUpAndUndoInsideWindow` | Tested |
| TM-33 | Account taker erases records | Step-up (above), 7-day undo, restriction first, RET-18 hold, **the erasure notice can no longer be deleted by step 1 (fixed)**: `TestTC03*`, `TestTM33ErasureNoticeSurvivesStep1`. An SMS template for the notice waits for DLT (push only today) | Tested; **Open** (S-102 template) |
| TM-34 | Export archive leaks | Sealed with the patient key, single-use 5-min link, purged after 24 h: `TestExportIsEncryptedAndDeliveredThroughLink`, `TestReviewM6ExpiredExportIsPurged` | Tested |
| TM-35 | Staff curiosity | JIT grants with reason, approver not self, max 4 h, break-glass **alert wired (fixed)**, support lookup without health data: `TestTC11*`, `TestS172SupportAccountLookup`, `TestS178AlertHooksAreWired` | Tested; anomaly detection **Open** (S-095) |
| TM-36 | Staff phished | Staff roles fail closed until SSO with a phishing-resistant factor exists | Built (fail closed); **Open** (F6) |
| TM-37 | Backup or bucket exposed | Field-level encryption with per-patient keys; erasure survives a restore (**S-185 erasure journal**, `TestTC04ErasureJournalReplayAfterRestore`, `TestTC04CryptoShredAndRestoreReplay`) | Tested; network and IaC **Open** (S-093) |
| TM-38 | Audit tampering | Insert-only roles, hash chain, write-once anchors, hourly verify, **chain-break alert wired (fixed)**: `TestTC65TamperEvidence`, `TestDeletedRowAndAnchorMismatchDetected` | Tested; object lock **Open** (infra) |
| TM-39 | Key service down | Fail closed, no plaintext fallback, **503 with Retry-After and an alert (fixed)**: `TestTC06KeyServiceUnavailableFailsClosed`, `TestTM39KeyServiceOutageIs503AndAlerts` | Tested |
| TM-40 | Personal data in logs | Allow-listed keys, scrubber, event-name messages, planted-data test, **panic path proven (new)**: `TestTC10PlantedPersonalDataNeverReachesLogs`, `TestKDP04_LoggingDiscipline`, `TestTM40RecoverLogsNoRequestData` | Tested |
| TM-41 | Injection, SSRF | Parameterised SQL everywhere (no user value is formatted into SQL; reviewed), strict JSON, no template engine, **brace injection refused (new test)**, no fetch of user URLs: `TestTM41BracePayloadInStoredVariableRefusedAtRender`; gosec *(CI)* | Tested; DAST **Open** (S-179) |
| TM-42 | Malware or disguised file | Content sniffing, scan (fail closed), images re-encoded, size and quota limits: `TestTC14UploadRejections`, `TestReencodeStripsMetadataAndTrailingBytes`. PDFs are not sanitised | Tested; **Residual** (PDF sanitising waits for V4) |
| TM-43 | Unmasked Aadhaar upload | Prohibited-ID check on text, quarantine on report, purge: `TestAAD05QuarantineAndPurge`. No image detection (no OCR) | Tested; warning **Open** (mobile) |
| TM-44 | Screenshots | — | **Open** (mobile, S-180) |
| TM-45 | Reverse engineering | No secrets in clients; every decision on the server; device-key refresh | Built; attestation **Open** (mobile) |
| TM-46 | Rooted device | Device revocation and restricted sessions | **Open** (mobile) |
| TM-47 | Compromised dependency or runner | Pinned modules, dependency allow-list test, govulncheck, Trivy, SAST *(CI)*. Images pinned by tag, not digest; no signed artefacts yet | Built; **Open** (S-093 pipeline) |
| TM-48 | Secret in repo | Gitleaks over full history plus a planted-secret test *(CI)* | Tested |
| TM-49 | Vendor breach | Minimum data per port; vendors without a processor agreement or outside India refused outside dev | Tested; **Open** (V2 register) |
| TM-50 | Flood | Per-IP and per-user limits (per instance) | Tested; WAF, autoscaling and adapter kill switches **Open** (S-093, S-111) |
| TM-51 | Breach at night | Alerts now reach one routable log event (`ops.alert`) | Built; paging and rota **Open** (S-095, people) |

## 3. Gaps fixed in this story (tests written first)

| # | Sev | Gap | Fix | Test |
|---|---|---|---|---|
| G1 | **High** | TM-13: clinic staff could mark an online consult "completed" or "patient no-show" (0% refund) without any join evidence | Every staff mark on an online appointment answers 409 `booking.online_outcome_by_consult`; outcomes come only from `consult.ended` (D-46) | `TestTM13StaffCannotMarkOnlineConsults` |
| G2 | **High** | TM-32/33: patients had no way to step up (only pro TOTP), so export, erasure and undo always answered 403 through the API; tests had injected the step-up time | Patient step-up by SMS code to the phone on file: `POST /v1/auth/step-up/otp-challenges` and `POST /v1/auth/step-up/otp`, bound to the session, shared limits and lockout, audited (D-75) | `TestTM32PatientStepUpThroughTheAPI` |
| G3 | High | C2: no module alert hook was connected in production (break-glass, audit chain break, SMS cap, refresh reuse, MFA failures, dead letters, payment mismatches) | One alert sink: an `ops.alert` ERROR line with a code, throttled per code, counted (D-76) | `TestS178AlertHooksAreWired` |
| G4 | Medium | TM-27: links and markup accepted in doctor, clinic and branch names; send-time filter missed bare domains such as `bit.ly/x` | Shared detector in `platform/scrub`, used at input, at send and in the SMS adapter (D-77) | `TestTM27*`, `TestHasLinkOrMarkup*` |
| G5 | Medium | TM-33: notifications erasure step 1 could delete the queued "erasure requested" notice before it was sent | Step 1 keeps data-rights notices; step 2 sweeps them | `TestTM33ErasureNoticeSurvivesStep1` |
| G6 | Medium | TM-04: a new pro device on an account with an authenticator sent no SMS-14 | SMS-14 for every new device on an existing account | `TestTM04ProNewDeviceIsNotified` |
| G7 | Medium | TM-39: key-service outage surfaced as a generic 500 with no alert | 503 `server.key_service_unavailable`, Retry-After 30, alert | `TestTM39KeyServiceOutageIs503AndAlerts` |
| G8 | Low | TM-04: the pro idle timeout had no real test (the old test passed on access-token expiry alone) | Test at refresh, where the control lives | `TestTM04ProIdleTimeoutAtRefresh` |
| G9 | Low | TM-40: no test of the panic path | Test that a panic logs only the request ID and route | `TestTM40RecoverLogsNoRequestData` |
| G10 | Low | TM-41: no test for brace injection in message variables | Test added | `TestTM41BracePayloadInStoredVariableRefusedAtRender` |

## 4. Open items (not the backend's, or waiting on a person)

- **Mobile (S-061, S-180):**
  - app lock and encrypted local storage (TM-05);
  - screenshot blocking (TM-44);
  - root detection (TM-46);
  - attestation (TM-02, TM-45);
  - the Aadhaar warning before upload (TM-43);
  - lock-screen display (TM-23).
- **Infra (S-093, S-095):**
  - private networks and IaC policy (TM-37);
  - object lock for anchors and journals (TM-38);
  - secrets manager (TM-11);
  - WAF and autoscaling (TM-50);
  - signed artefacts and digest-pinned images (TM-47);
  - the log-store alert rule for `ops.alert` and paging (TM-51);
  - anomaly detection (TM-35, TM-08).
- **Vendors (S-039 to S-042, S-111, V2):**
  - real webhook, video, e-signature, scan and SMS adapters with A9 kill switches and circuit breakers;
  - a second SMS provider (TM-25);
  - room lock and media region (TM-16);
  - register contact for the go-live notice (TM-28).
- **People and decisions:**
  - staff SSO (F6, TM-36);
  - code-owner names in GitLab (TM-18);
  - an SMS template for erasure notices (S-102, TM-33);
  - a patient dispute window for no-shows (F2, TM-13);
  - the PDF library ADR, which unlocks PDF sanitising and the QR code (V4, TM-20, TM-42).
- **Next backend work (small):**
  - call `TriggerReverification` from grievance intake when a complaint names a doctor (TM-31);
  - add the device to booking audit events (TM-06).
