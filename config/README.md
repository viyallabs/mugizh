# Configuration (S-099)

Everything that changes by country lives in a **country pack** (K-GLB-01). Code holds no country values. Spec: [docs/arch/country-pack-model.md](../docs/arch/country-pack-model.md). Loader: `backend/internal/modules/config`.

```
config/
  vendor-register.yaml        vendors with processor agreement and processing countries (K-DP-08)
  country-packs/IN/           India pack (status: draft; has [FILL]/[CONFIRM]/[VALIDATE]/[CA] items)
  country-packs/XX/           synthetic test country for TC-53 (fake adapters; local, test, dev only)
    pack.yaml                 manifest
    rules/ drugs/ payments/ messaging/ tax/   files the manifest references
```

## Loader rules (all fail closed: the service refuses to start)

- Strict YAML: unknown or duplicate keys, a missing required key, or a wrong type is an error.
- Every referenced file must exist inside the pack folder, parse as YAML, and match its SHA-256 in `pack.checksums` (TC-59).
- `production` loads only `status: approved`. `staging` may load drafts. Values with `[FILL`, `[S-0`, `[CONFIRM`, `[VALIDATE`, `[CA` or `[CLINICAL` are refused in staging and production.
- An adapter must be in the vendor register with a processor agreement and processing countries inside `residency.vendor_processing_must_be_in`, or have an approved entry in `residency.exceptions`. `fake-*` adapters load only in local, test and dev. WhatsApp stays `none` and off while it is on hold.
- Feature flags can never switch off consent, the rules engine, the prescribing gate, audit or encryption. Adapter kill switches are `adapter.<kind>.enabled`.

## Who approves a change (country-pack-model section 2)

| File or key | Approver |
|---|---|
| `pack.code`, `pack.status`, `locale` (languages), `people.phone_country_codes`, `payments.settlement_modes_allowed`, `payments.adapter`, `adapters`, `features`, `messaging.channels_enabled`, `sms_registry`, `sms_principal_entity_id`, `quiet_hours`, `clinical.minors_supported` | Founder |
| `residency.region_primary`, `region_dr` | Founder + infra |
| `residency.vendor_processing_must_be_in` | Founder + lawyer |
| `residency.exceptions`, `people.age_of_majority`, `min_account_age`, `prohibited_identifiers`, `retention`, `legal` (grievance, notice and consent versions), `locale.legal_text_languages` | Lawyer (retention RET-04 also the CA) |
| `money.tax` file | Chartered accountant (CA) |
| `emergency` | Clinical reviewer |
| `rules/telemedicine-rules.yaml`, `drugs/classification.yaml` (`clinical.prescribing_rules`, `drug_classification`) | Clinical reviewer (K-CS-07), rules also the lawyer |
| `payments/refund-policy.yaml` | Founder + lawyer |
| `pack.checksums` | Generated, never edited by hand |

## Checksums

After any change to a referenced file, regenerate the checksums and commit them with the change:

```sh
tools/dev.sh backend go run ./cmd/packsum ../config/country-packs/IN      # rewrite pack.checksums
tools/dev.sh backend go run ./cmd/packsum -check ../config/country-packs/IN  # CI: exit 1 if stale
```

Checksums are over the raw bytes, so keep these files with LF line endings. Regenerating checksums is not an approval: the approver in the table above still signs off the PR.
