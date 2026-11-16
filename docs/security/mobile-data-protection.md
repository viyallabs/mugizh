# Mobile data protection (S-061)

**Phase P06 · Epic EP08 · Depends on:** S-056 · **Contract refs:** K-SEC-07 (also K-DP-04, K-DP-09, K-SEC-03) · **Edge cases:** E-DP-07, E-DP-08 (also E-DP-12, E-REM-02) · **Compliance:** C03 · **Date:** 2026-11-15
**Status:** In Testing, until the founder approves the design (acceptance criterion). Tested in S-180 (MASVS).

**Who this is for:** the mobile lead building the Patient app and Pro app, and the auditor (S-180, S-183).

**Baseline:** OWASP **MASVS v2**, at the **MAS-L2** testing profile for both apps (they hold health data), plus selected resilience (**MAS-R**) controls for the Pro app, because prescribing power makes it a target.

## 1. What may be stored on the phone

| Data | Patient app | Pro app | Why |
|---|---|---|---|
| Tokens (access, refresh) | Keychain / Keystore only | Same | PER-K |
| Next 72 h of reminder occurrences, with **neutral text** (medicine name only with P-MEDNAMES) | Yes, encrypted (offline backup, S-165, E-REM-02) | — | Reminders must fire offline |
| Recently viewed prescription and notes | Encrypted cache, cleared on sign-out and after 7 days unused [CONFIRM] | **No offline patient data.** Today's list shows names and times only, cleared at sign-out. | Minimise what a lost phone exposes |
| Uploads in progress | Encrypted temp file, deleted after upload | — | — |
| Anything else (search history, analytics) | Not stored | Not stored | — |

**Storage rules:**
- **iOS:** Keychain items `WhenUnlockedThisDeviceOnly`. Files with Data Protection **Complete**. Sensitive files **excluded from iCloud backup**.
- **Android:** keys in the **Android Keystore** (hardware-backed where available); the local database encrypted with a Keystore-wrapped key. `allowBackup=false`, with data-extraction rules excluding app data from cloud backup and device transfer. *Check the status of any helper library before use (some Jetpack Security APIs have been deprecated).*

## 2. App lock and sign-out (E-DP-07)

| Control | Patient app | Pro app |
|---|---|---|
| App lock (biometric or device PIN) | **Optional, offered at first prescription; on by default** [CONFIRM] | **Mandatory** |
| Auto-lock | After 5 min in the background | After 2 min in the background |
| Remote sign-out (from another device or support) | Refresh token revoked; at next launch or push, the app wipes local data | Same |
| Sign-out | Clears tokens, caches, local database, scheduled local notifications | Same |
| Lost device | Patient uses "signed-in devices" (SC-52) | Doctor uses Pro web; MFA re-enrolment |

## 3. Screens (E-DP-08)

| Platform | What we do on sensitive screens (Home, Calendar, Records, prescription, consult, chart) |
|---|---|
| **Android** | `FLAG_SECURE`: blocks screenshots and screen recording, and hides content in the recent-apps view |
| **iOS** | Screenshots **cannot be blocked**. On a screenshot, show a gentle warning ("This screen contains health information"). **While the screen is being recorded or mirrored, hide the content.** Blur the app in the app switcher. |

Matches E-DP-08 ("Blocked on Android for sensitive screens; warning on iOS").

## 4. Rooted or jailbroken devices

| Signal | Patient app | Pro app |
|---|---|---|
| Device-integrity check fails (Android Play Integrity; iOS App Attest + jailbreak signals) | **Warn, then continue.** Patients must not lose access to their own health information. Health caches are disabled on that device. | **Block signing prescriptions and opening patient charts on that device** [CONFIRM]; doctor can use Pro web instead |
| App tampered with or re-signed | API refuses requests that fail attestation on sensitive endpoints | Same |

Attestation runs on the server for sensitive endpoints (OTP request, signing, export). The app never decides on its own (TM-45).

## 5. Network and certificate pinning (decision)

| Option | For | Against |
|---|---|---|
| No pinning; platform trust only | Simple; no risk of bricking apps when certificates change | A user-installed or compromised CA could intercept traffic |
| Pin the leaf certificate | Strongest | **Breaks every installed app at certificate rotation** |
| **Pin the public keys (SPKI) of the issuing CA intermediates, with a backup pin from a second CA** | Stops interception with a rogue CA; survives normal certificate renewal | Needs care when the CA changes: ship new pins in advance |

**Recommendation:** SPKI pinning with **at least two pins** (current CA plus a backup CA) for our API domains, in **both apps**. Pins expire, and the update path is a normal app release with an overlap period. **No remote "turn off pinning" switch**, because that could be abused.

**Also:**
- Android network security config **does not trust user-added CAs** (the default for modern target SDKs; stated explicitly).
- iOS App Transport Security on.
- **Cleartext traffic disabled.**
- TLS 1.2+.

[CONFIRM]

## 6. Other rules

| # | Rule | Why |
|---|---|---|
| M1 | **No secrets in the app** (no API keys that grant data access). Vendor keys stay on the server. | TM-45 |
| M2 | OTP autofill through the platform APIs (Android SMS Retriever, iOS one-time-code). **No permission to read all SMS.** | Least privilege |
| M3 | Notification payloads carry an opaque ID only. Text follows discreet rules (K-DP-09); locally scheduled notifications use the same neutral text. | E-DP-12 |
| M4 | Deep links carry opaque IDs; content loads after the app lock (IA rule N1) | E-DP-12 |
| M5 | **No third-party analytics, ads or attribution SDKs.** An SDK allow-list is checked in CI. **Crash reporting and app performance monitoring must be self-hosted or India-processing,** listed in the vendor register with a processor agreement, and scrubbed of personal data. Crash reports still carry device IDs and IP addresses, which are personal data (K-DP-01, K-DP-08; compliance review R-05). | E-DP-25, K-DP-04 |
| M6 | No console logging in release builds. No health data in error messages. | K-DP-04 |
| M7 | No WebViews for patient data. If one is unavoidable: no JavaScript bridges, and our domain only. | MASVS-PLATFORM |
| M8 | Sensitive text fields (notes, diagnosis) disable keyboard learning and autocorrect suggestions where the platform allows | Keyboard caches |
| M9 | Release builds obfuscated; debug flags off; signed in CI (release policy §4) | MASVS-CODE/RESILIENCE |
| M10 | Camera and files permissions only when uploading; location only for "near me" search and never in the background | Minimisation |

## 7. Tests (S-180)

- MASVS L2 checklist for both apps.
- Backup extraction shows no app data.
- A screenshot on Android sensitive screens fails.
- iOS screen recording hides content.
- A rooted test device triggers the warning (patient) or the block (Pro).
- A proxy with a user-installed CA cannot intercept API traffic.
- Remote sign-out wipes data at next launch.

## 8. Decisions [CONFIRM]

1. Patient app lock on by default, but optional.
2. Pro app blocks signing and charts on rooted or jailbroken devices.
3. SPKI pinning with backup pins in both apps.
4. Patient cache of viewed records cleared after 7 days unused.
