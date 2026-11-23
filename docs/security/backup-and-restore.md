# Backup, restore and deletion propagation (S-062)

**Phase P06 · Epic EP08 · Depends on:** S-056 ([encryption-design.md](encryption-design.md)) · **Contract refs:** K-DP-07 (also K-DP-01, K-DP-03) · **Edge case:** E-DP-04 · **Compliance:** C03 · **Date:** 2026-11-15
**Status:** In Testing, until the founder approves the design (acceptance criterion). Drilled in S-185. The DR topology is in [infrastructure-and-environments.md](../arch/infrastructure-and-environments.md) §5.

**Who this is for:** the infra lead, the security owner, and the person running the restore drill.

## 1. What is backed up

| Store | Method | Frequency | Kept (RET-17) | Copy in DR region |
|---|---|---|---|---|
| Core PostgreSQL (all module schemas) | Provider snapshots + continuous log archiving (point-in-time recovery) | Snapshot daily; logs every 5 min | **35 days** [CONFIRM] | Yes, snapshots copied |
| scheduler-core database | Same | Same | 35 days | Yes |
| **Key store** (wrapped data keys) | Same, **under a separate backup key** | Daily + logs | **35 days maximum, never longer** (encryption design §4) | Yes |
| **Key destruction log and erasure log** | Replicated table, plus a daily export to write-once storage | Continuous | **Kept** (holds IDs only; needed to replay erasures) | Yes |
| Object storage (uploads, PDFs, exports) | Versioning + cross-region replication | Continuous | Non-current versions expire after 35 days | Yes |
| Audit store | Partitions + daily anchors in write-once storage | Continuous | RET-06 | Yes |
| Logs | Log store retention | — | RET-07 (1 year) | Per residency check R3 |
| Redis / queue | **Not backed up** (not a source of truth) | — | — | — |
| Config, IaC, country packs | Git (the repo is the backup) | Every change | Forever (no personal data) | Repo hosting |

**Rules:**
1. **All backups are encrypted** with customer-managed keys. Field-encrypted data stays field-encrypted inside backups.
2. Backups live in **India regions only** (residency check R2).
3. **Immutable backups:** a backup vault lock (write-once for the retention period) stops a stolen admin account or ransomware from deleting backups early. Backups still expire at 35 days.
4. **Separate access:** backups sit in a separate backup account or vault. Restoring needs the break-glass or infra role, with an approval and an alert.

## 2. How deletions reach backups (E-DP-04)

A backup cannot be edited to remove one person, so deletion works in three ways together:

1. **Crypto-shredding.** Erasable data is encrypted with the patient key. Destroying that key in the key store makes every copy unreadable: database backups, object versions, replicas. **The key is destroyed when the legal-minimum hold ends (RET-18)**; during the hold the data is restricted, and a restore must keep it restricted.
2. **Short key-store backups.** Key-store backups are kept for 35 days at most. After that, **no copy of a destroyed key exists anywhere**, and the erasure is permanent.
3. **Replay on restore.** Within the 35 days, a restore could bring back an old key or old rows. So **every restore replays the key destruction log and the erasure log before the system opens** (section 3, step 4). Restored keys that are tombstoned are destroyed again. Restored rows of erased subjects are deleted or restricted again.

4. **Revocations survive a restore too (added 2026-11-23, review of S-207; Q24).** A consent withdrawal, a history-share or other grant revocation, or a caregiver link revocation made after the backup was taken would otherwise come back as active. Each of these "stop" actions is also written to write-once storage (a revocation journal, like the erasure journal D-74) and **replayed before the service opens**. Replay applies only actions that **remove** access, never grants: after a restore the worst case is that a patient must grant something again, never that a withdrawn permission returns (K-DP-02).

Clinical records under RET-02 use **care keys**, which are not destroyed at account erasure. They stay readable until their retention period ends, and are then destroyed the same way.

## 3. Restore procedure (any restore: drill, incident or DR)

1. **Approve and record:** reason, approver, target environment (an incident ticket for real restores).
2. **Restore into an isolated account with production-level controls.** For drills this is a dedicated "restore-test" account inside the production organisation unit. **Never into staging or dev**, which hold synthetic data only (S-044).
3. Restore the database, key store and objects to the chosen point in time.
4. **Replay the key destruction log and erasure log** from their current, unrestored copies. Re-apply legal holds.
5. **Verify:**
   - schema integrity;
   - row counts against expectations;
   - audit chain continuity up to the restore point;
   - **a sample of erased subjects is unreadable;**
   - a sample of RET-02 records is still readable.
6. **Open the service.** In a DR failover: point traffic to it. In a drill: tear the environment down and record the result.
7. **Write the report:** times against the RPO and RTO targets, and any failures. Evidence is kept for the audit (S-051).

## 4. Restore tests

| Test | Frequency | Pass when |
|---|---|---|
| Automated database restore into the restore-test account, with step 4 replay and step 5 checks | **Monthly** | All checks pass; time recorded |
| Object restore of a sample of deleted and non-current files | Quarterly | Encrypted files restored; erased patients' files unreadable |
| **Full DR drill** (failover to the DR region) | **Twice a year, and before the beta** (S-185) | RTO and RPO met; erasures replayed; audit chain intact |
| Key-store backup expiry check | Monthly | No key-store backup older than 35 days exists |

## 5. Edge case

| Edge case | Covered by | Test |
|---|---|---|
| **E-DP-04** deleted user's data still exists in backups | Crypto-shredding + 35-day key-store backups + replay on restore (§2, §3) | TC-04 (drill) |

## 6. Decisions [CONFIRM]

1. Backup retention 35 days (RET-17).
2. Immutable backup vault lock for the retention period.
3. Monthly automated restore test into an isolated production-controlled account.
