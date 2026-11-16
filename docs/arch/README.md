# Architecture (phases P05 and P07, epics EP07 and EP09)

These documents are **stack-neutral**: they name roles ("relational database", "job queue"), not products, so they stay valid whatever tech stack the founder chooses. The open stack decisions are in [adr-decisions.md](adr-decisions.md) §3.

| File | Story | Status | What it is |
|---|---|---|---|
| [architecture-overview.md](architecture-overview.md) | S-034 | Done | Context, containers, module map, key data flows, cross-cutting design, review against contracts |
| [adr-decisions.md](adr-decisions.md) | S-035 | In Testing (founder decides) | A recommendation for every ADR; open tech stack ADRs; new ADR-27 to ADR-29; scheduler-core deployment options (§2a) |
| [module-boundaries-and-domain-model.md](module-boundaries-and-domain-model.md) | S-036 | In Testing (founder approves) | Boundary rules B1–B10, module map, domain model, event catalogue |
| [adapter-interfaces.md](adapter-interfaces.md) | S-037 | Done | Adapter rules A1–A12, common error model, interfaces for every external provider |
| [api-standards.md](api-standards.md) | S-038 | In Testing | REST + OpenAPI standards, errors, pagination, two-layer idempotency, security |
| [openapi-lint.spectral.yaml](openapi-lint.spectral.yaml) + [lint-fixtures/](lint-fixtures/) | S-038 | Not verified until CI | Spectral lint rules and the fixtures that prove them |
| [country-pack-model.md](country-pack-model.md) + [country-packs/IN/pack.draft.yaml](country-packs/IN/pack.draft.yaml) | S-054 | Done | Country-pack schema, loader rules, draft India pack |
| [infrastructure-and-environments.md](infrastructure-and-environments.md) | S-055 | In Testing (cloud choice) | Environments, topology, cloud mapping, residency checks, DR |
| [performance-targets.md](performance-targets.md) | S-065 | In Testing (sign-off) | Targets per service, reviewed and extended |
| [algorithms.md](algorithms.md) | S-069 | Done | Rules engine, search ranking, notification fallback |
| [fhir-r4-mapping.md](fhir-r4-mapping.md) | S-083 | Done | FHIR R4 mapping, fitted to ABDM for v2.0 |

Diagrams are written in Mermaid, which GitLab and GitHub render inside Markdown.

**The rules that come from here** are in `/contracts` (K-ARC, K-GLB; drafted in S-075).
