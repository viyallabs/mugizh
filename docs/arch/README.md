# Architecture (phase P05, epic EP07)

These documents are **stack-neutral**: they name roles ("relational database", "job queue"), not products, so they stay valid whatever tech stack the founder chooses. The open stack decisions are in [adr-decisions.md](adr-decisions.md) §3.

| File | Story | Status | What it is |
|---|---|---|---|
| [architecture-overview.md](architecture-overview.md) | S-034 | Done | Context, containers, module map, key data flows, cross-cutting design, review against contracts |
| [adr-decisions.md](adr-decisions.md) | S-035 | In Testing (founder decides) | A recommendation for every ADR in the Architecture sheet; the open tech stack ADRs; new ADR-27 to ADR-29 |
| [module-boundaries-and-domain-model.md](module-boundaries-and-domain-model.md) | S-036 | In Testing (founder approves module map) | Boundary rules B1–B10, module map, domain model, event catalogue |
| [adapter-interfaces.md](adapter-interfaces.md) | S-037 | Done | Adapter rules A1–A12, common error model, interfaces with timeouts and fallbacks for every external provider |
| [api-standards.md](api-standards.md) | S-038 | In Testing | REST + OpenAPI standards: versioning, errors, pagination, idempotency (HTTP cache separate from durable money records), security |
| [openapi-lint.spectral.yaml](openapi-lint.spectral.yaml) | S-038 | **Not yet verified** | Spectral lint rules for the OpenAPI specs (wired into CI in S-092) |
| [lint-fixtures/](lint-fixtures/) | S-038 | Waiting for CI | One spec that must pass, one that must break each rule (expected errors listed in its header), and one scheduler-core spec that must fail the health-field rule. S-038 is Done when CI shows these results. |

Diagrams are written in Mermaid, which GitLab and GitHub render inside Markdown.

**Rules that come from here:** the architecture contracts (`contracts/architecture/modularity.md`, `api-and-data.md`, `country-pack.md`) are written in S-075 from B1–B10 and the API standards. Until then, use the Contracts sheet (K-ARC, K-GLB).
