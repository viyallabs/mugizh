# Review and test queue

**Started:** 2026-11-23 · **Why:** on 2026-11-23 the founder asked Claude to keep developing and to mark stories for testing and code review later, with agents doing that work in parallel. Each row is one piece of work: what was built, what has been checked, and what is still to do. When a row's checks are done, it moves to the tracker (Code review "Approved", Tests "Passing") and to the code review document.

**Memory rule:** the laptop runs out of memory with more than one heavy job. Only one scan, load test or full test run at a time (`docs/process/build-decisions-log.md`, build-environment notes).

## Queue

| # | Story | Built | Unit/integration tests | Code review | Heavy runs | Status |
|---|---|---|---|---|---|---|
| Q1 | S-179 ZAP scan | Harness (`task dast`) | — | Opus | Full run 2026-11-23: 33 scans, 0 FAIL | Done locally; staging run open |
| Q2 | S-175 | See Q11 | | | | Superseded |
| Q3 | S-095 traces and metrics (OpenTelemetry) | `platform/telemetry`, router middleware, alert counter, API and worker wiring, inventory row | `TestS095*` pass (3) | Agent review: 3 Low, all fixed (server-side sampling, deferred span end, go mod tidy) | Needs a collector to see real exports (staging) | Reviewed; full-suite run pending |
| Q4 | S-189 | See Q8 | | | | Superseded by Q8 |
| Q5 | S-186 production readiness (backend half) | `docs/ops/production-readiness.md`: services, start-up refusals, alert catalogue with runbooks R1–R8, deploy and rollback, go-live checklist | N/A (document) | Founder review | — | Written |
| Q6 | TM-06 booking audit context | Booking audit events carry the app and the keyed IP hash | booking package passes | Agent review: fine (values match the audit format; system paths unaffected) | — | Reviewed |
| Q7 | S-213 moderation | **Not built:** conflict with K-SEC-08 (staff reading unpublished patient text), logged as blocker F8 | — | — | — | Waiting for founder |
| Q8 | S-189 in-app feedback (backend) — update to Q4 | Built by the agent: `POST/GET /v1/me/feedback` (sealed with the user's key, refuses identity numbers, contact details, links, markup; 10 a day), `GET /admin/v1/feedback` (support, no message: F8), status change, admin erasure consumer, 1-year purge scheduled daily in the worker | admin, records, app packages pass (agent) | Agent review: 3 Medium + 3 Low, all fixed (strict number checks incl. Devanagari digits and split numbers, one erasure rule, no silent blank on key outage, code fields checked, reference retry, bare domains refused) | — | Reviewed; app run pending |
| Q9 | S-091 CI additions | `go-mod-tidy` job (go.mod and go.sum must match the code) and vet of the `dast` and `load` build tags | Local equivalents pass | Not started | Not run on GitLab yet | Built |
| Q10 | S-093 infrastructure as code (AWS) | Being written by a Sonnet agent: org region-deny policy, network, RDS 17 (+ key-store instance), S3 with Object Lock, KMS, ECS Fargate, ALB + WAF, GitLab OIDC, CloudWatch alarm on `ops.alert` | `tofu validate`, Trivy config scan (agent) | Opus review when it reports | Cannot apply (no AWS accounts, V1) | Building |
| Q11 | S-175 k6 smoke load | Run 2026-11-23: every threshold met (L1 p95 16.7 ms, L2 21.7 ms, L5 16.7 ms, L3 exactly 1 win, L11 throttled, 0 errors); `docs/test/load-smoke-2026-11-23.md` | — | Opus | Launch profile on staging open | Done locally |
| Q12 | D-86 number-detection hardening (platform/scrub, used by every module) | `prepare` (Unicode digits, line breaks), strict variants for feedback and reviews | scrub, admin pass | Opus | **Full backend suite to re-run** (every module that checks free text) | Built; full run pending |
| Q13 | DAST and load scripts stop their target cleanly (stop file), so the test removes its database container (Ryuk is disabled) | `targets_test.go` (`serveUntilStopped`), cleanup in both scripts | Tagged builds vet | Not started | Proven on the next `task dast` / `task load:smoke` | Built |
| Q14 | S-094 groundwork: production images | `deploy/images/core.Dockerfile` (api, worker, migrate; scratch, non-root, CA certs, packs and vendor register), `scheduler-core.Dockerfile`, `.dockerignore` (no secrets, no tracker), CI job `images` (build, Trivy HIGH/CRITICAL fails, push) | Image build and Trivy scan locally after the full run | Not started | Build is heavy | Built, untested |
