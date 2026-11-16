# FHIR R4 mapping for clinical data (S-083)

**Phase P05 · Epic EP07 · Depends on:** S-036 ([module-boundaries-and-domain-model.md](module-boundaries-and-domain-model.md)) · **Contract refs:** K-ARC-05 · **Compliance:** C15 · **Date:** 2026-11-15
**Status:** Done (acceptance criterion: mapping doc; fits ABDM needs in v2.0). The exact ABDM implementation-guide version is pinned when v2.0 starts [VALIDATE].

**Who this is for:** developers building the export (S-143) and, in v2.0, the ABDM integration; reviewers.

**Rule (K-ARC-05):** we keep **our own data model** inside the system, and map to **FHIR R4 at the boundary**: data export, ABDM sharing, and future integrations (ADR-17). Mapping code lives in the records and clinical modules' public interfaces, never spread through business code.

**ABDM target:** the **NRCeS "FHIR Implementation Guide for ABDM"**. NRCeS is the National Resource Centre for EHR Standards. ABDM shares health records as FHIR **document Bundles** whose Composition follows a record-type profile (for example `OPConsultRecord`, `PrescriptionRecord`, `DiagnosticReportRecord`). Published versions on nrces.in include 6.x; a 7.0 build is in development. **Pin the version current at v2.0 start, and re-check the profile names.** [VALIDATE]

## 1. Resource mapping

| Our entity (module) | FHIR R4 resource | ABDM use | Key mapping notes |
|---|---|---|---|
| Patient user (identity) | **Patient** | All records | `name`, `birthDate`, `gender` (male / female / other / unknown), `telecom` (export only). `identifier`: our opaque ID; **ABHA number and ABHA address from v2.0**. **Never Aadhaar** (K-DP-05). |
| Professional (directory) | **Practitioner** | Author of records | `name`, `qualification`. `identifier`: registration number with a **system URI per council** [FILL URIs]; **HPR ID from v2.0** |
| Clinic (directory) | **Organization** | Custodian | `name`, `address`, `telecom`; **HFR ID from v2.0** |
| Practice (directory) | **PractitionerRole** | — | practitioner + organization + specialty |
| Appointment (booking) | **Appointment** | Follow-up section | Status: held or pending_payment → `proposed`; confirmed → `booked`; completed → `fulfilled`; cancelled → `cancelled`; no_show → `noshow` |
| Consult session (consult) | **Encounter** | Context of every record | `class`: `AMB` in person, **`VR` (virtual) for teleconsult**; `period` from join times; mode (video or audio) as an extension [VALIDATE against the IG] |
| Consult note (clinical) | **Composition** (`OPConsultRecord`) | OP consultation | Sections: chief complaints (Condition or Observation), findings (Observation), diagnosis (Condition), advice, follow-up (Appointment / ServiceRequest) |
| Diagnosis (clinical) | **Condition** | In OPConsult | `code`: SNOMED CT where coded, otherwise text; `verificationStatus`: `provisional` or `confirmed` |
| Prescription (clinical) | **Composition** (`PrescriptionRecord`) + **MedicationRequest** per item | Prescription | MedicationRequest: `status` active (cancelled for a cancelled item); `intent` order; `authoredOn`; `requester` → Practitioner; `medicationCodeableConcept` (section 2); `dosageInstruction` (section 3) |
| Signature (P08 + P09) | **Composition.attester** (mode `legal`) + **Provenance** with `signature` | Integrity | The electronic signature over the document hash (P09) goes in `Provenance.signature`. Amended versions are new Compositions that **replace** the old one, with Provenance linking them (K-CS-05). |
| Prescription PDF | **DocumentReference** / **Binary** | Attachment in the Composition | Content type `application/pdf` |
| Tests and scans ordered | **ServiceRequest** | In OPConsult / Prescription | `code`: LOINC or SNOMED CT where coded; `occurrence` = due date |
| Patient upload (records) | **DocumentReference** (`HealthDocumentRecord`) | Health document | `type` from the upload category; Binary content |
| Allergies (profile, S-108) | **AllergyIntolerance** | In OPConsult | Coded where possible, otherwise text |
| Lab report (v2.0) | **DiagnosticReport** + **Observation** (`DiagnosticReportRecord`) | Diagnostic report | LOINC codes |
| Consent and sharing grants (records) | **Consent** (export only) | **Not used for ABDM sharing** | ABDM uses its own consent artefacts through the gateway (consent manager flow), not FHIR Consent. Our T1 to T4 grants stay internal. [VALIDATE] |
| Rules-engine result | Not exported | — | Internal safety record (RET-02) |
| Reminders (scheduler-core) | Not a clinical record | — | The source is MedicationRequest timing. Adherence (v2.0) may use MedicationStatement [design in v2.0]. |

## 2. Coding systems

| Data | Preferred code system | Fallback |
|---|---|---|
| Diagnoses, findings, procedures | **SNOMED CT** (India national release, distributed through NRCeS; check the licence terms for our use [VALIDATE]) | `text` only, never a guessed code |
| Lab tests and results | **LOINC** | Text |
| Medicines | SNOMED CT medicinal product concepts where available; our drug-master ID as a local `system` URI | Brand or generic name as text |
| Gender, statuses | FHIR value sets | — |

**Never invent a code.** If the doctor did not choose a coded term, export text only. This is the same principle as K-CS-03: no inference.

## 3. Dose (MedicationRequest.dosageInstruction)

| Our field (structured, rules check P07) | FHIR |
|---|---|
| Dose amount and unit | `doseAndRate.doseQuantity` |
| Frequency (for example 1-0-1) | `timing.repeat` (frequency, period, `when`: MORN/AFT/EVE/NIGHT) |
| Duration | `timing.repeat.boundsDuration` |
| Route | `route` (SNOMED CT) |
| As needed (E-RX-04) | `asNeededBoolean` + `asNeededCodeableConcept` if a reason was given |
| Tapering (E-RX-05) | **Several `dosageInstruction` entries**, each with its own `timing.repeat.boundsPeriod` and `sequence` |
| Instructions (free text) | `patientInstruction` / `text` |

## 4. Bundles we produce

| Bundle | When | Contents |
|---|---|---|
| **Patient export** (data rights, S-143) | v1.0 export can ship JSON first; FHIR document bundles from v2.0 | One `document` Bundle per consult (OPConsultRecord or PrescriptionRecord), plus a `collection` Bundle for uploads |
| **ABDM share** (v2.0, as an HIP) | When a patient's ABDM consent artefact allows it | Document Bundles per the IG, encrypted as the ABDM gateway protocol requires [VALIDATE] |

## 5. Fits ABDM in v2.0 (acceptance criterion)

| ABDM need | Covered by |
|---|---|
| Patient identified by ABHA | Patient.identifier (v2.0); our model already keeps external identifiers separate |
| Practitioner and facility registries (HPR, HFR) | Practitioner and Organization identifiers (v2.0) |
| OP consultation and prescription records | OPConsultRecord and PrescriptionRecord Compositions (section 1) |
| Health documents and lab reports | HealthDocumentRecord (uploads), DiagnosticReportRecord (v2.0 labs) |
| Signed, versioned records | Composition attester + Provenance signature; replaces-links for amendments |
| Consent-based sharing | ABDM consent artefacts handled by a new adapter (v2.0); data released only for a valid artefact, and audited (K-DP-02) |

## 6. Testing (v2.0)

- Validate every generated Bundle with the HL7 FHIR validator against the pinned NRCeS IG profiles, in CI.
- Round-trip tests: our model → FHIR → reading back the key fields.
- No personal data in test fixtures (synthetic patients).
