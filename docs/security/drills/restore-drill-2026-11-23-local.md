# Restore and deletion-key drill: local, 2026-11-23 (S-185)

**Story:** S-185 · **Contract:** K-DP-07 · **Edge case:** E-DP-04 (TC-04) · **Procedure:** [backup-and-restore.md](../backup-and-restore.md) sections 2 to 4
**Kind:** pre-staging drill on synthetic data. **The full DR drill on AWS (failover to the DR region, RTO and RPO) is still open:** it needs the accounts (blocker V1). This drill checks the part that is code: what happens to erased and restricted people when a backup is restored.

## 1. Finding before the drill

The procedure says every restore "replays the key destruction log **and the erasure log** before the system opens". The key part existed: key tombstones in write-once storage, replayed at start-up (TC-04). **The erasure log did not exist.**

Erasure works in two stages, and only the second destroys the patient key:
- the 7-day undo window;
- then the RET-18 legal-minimum hold.

So a Core database restored to a point before an erasure request, or before an undo, would bring the account back visible. The erasure would never finish, because the request itself was gone. Crypto-shredding does not help while the key still exists.

## 2. What was built (D-74)

- **Erasure journal:**
  - each request, undo and completion is written to write-once storage after it commits (`erasure-journal/<request>/<event>.json`, references and times only);
  - the worker writes any entry an object-store outage left unwritten.
- **Replay at start-up,** after the key tombstones and before the API or worker serves anything:
  - a request missing from the database is rebuilt with its original times, the account is hidden again, and every module is told again (consumers are idempotent);
  - an undo after the restore point is re-applied, so the account is not hidden against the person's choice;
  - a completed erasure's restored rows are deleted again by the normal step 2, and the key stays destroyed;
  - a corrupt or forged entry stops start-up.
- **Data inventory:** a new row in 4.2.

## 3. Drill steps and results

| Step | How | Result |
|---|---|---|
| Backup before the request, erasure, restore, replay | `TestTC04ErasureJournalReplayAfterRestore`: the Core rights tables are snapshotted, the patient asks for erasure, the snapshot is restored, then the service replays | Before replay the account was visible again (the gap, shown on purpose). After replay: hidden from every role; the same request ID, received time and undo deadline; modules told again; the patient not notified twice; same RET-18 hold end |
| Backup during the request, undo, restore | `TestErasureJournalReplaysUndo` | After the restore the account was hidden again; after replay the undo is back and the account is visible |
| Backup inside the hold, completion (key destroyed), restore | `TestErasureAfterRestoreDeletesRestoredRowsAgain` | The restored upload row is unreadable (key destroyed) and is deleted again on the next worker run; the erasure completes again |
| Key-store restore | `TestTC04CryptoShredAndRestoreReplay` (existing) | A restored key-store backup that brings a destroyed key back is shredded again before serving |
| Object-store outage while journaling; forged entries | `TestErasureJournalCatchUpAndFailClosed`, `TestErasureJournalRefusesForgedEntries` | The request still succeeds; the worker catches up; a corrupt, mis-keyed or unknown-event entry stops start-up |
| Real database | `deploy/local/up.sh` on the existing local database, then the API started | Migrations `records 0005` and `identity 0006` applied forward; start-up replay ran with the module roles' real grants; readiness 200 |

All tests run on PostgreSQL 17 + PostGIS 3.5 with the module login roles (not a superuser), in `tools/dev.sh`.

## 4. Not covered here (open)

- **RTO/RPO timing** on production-sized data, snapshot and point-in-time restore, the cross-region copy, and the restore-test account. These need AWS (V1, S-093). The local database is nearly empty, so a laptop time would mean nothing.
- **Object-lock** (true write-once) for tombstones, the journal and audit anchors. Locally the store is a file directory with no-overwrite files (TM-38).
- **The monthly automated restore test and the 35-day key-store backup expiry check.** These are infra jobs (backup-and-restore.md section 4).
- **Residual risk:** an erasure committed in the seconds before a restore point, whose journal write also failed in those seconds, is lost with the database. The person would have to ask again [CONFIRM accepted]. The worker retries every minute, so the window is about a minute in the worst case.
