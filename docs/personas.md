# Personas (S-003) — DRAFT v0.1 for founder approval

**Phase P00 · Epic EP01 · Depends on:** S-002 (product vision)
**Sources:** Resources/feature-roadmap.md v0.3, docs/product-vision.md v0.2.
**Important:** The names, ages and details below are **illustrative assumptions** to make each
persona concrete. They are not research findings. Validate every **[VALIDATE]** item in the
v0 concierge test (P02) and in interviews, then update this file.

Primary roles: Patient, Doctor, Nurse. Caregiver is an extension of the patient role (acts only
with the patient's permission).

---

## 1. Patient (chronic condition) — "Lakshmi", 58

| | |
|---|---|
| **Who** | Manages a long-term condition such as diabetes or hypertension; sees the same doctor every 1–3 months; takes several medicines daily. **[VALIDATE]** |
| **Goals** | Take the right medicine at the right time. Never run out. Know which tests are due before the next visit. Keep all reports in one place. Feel in control, and keep family informed if she chooses. |
| **Pains** | Paper prescriptions get lost or are hard to read. Forgets doses, refills and test dates. Has to chase labs and carry old reports. Not sure which clinic or doctor to trust. Privacy worry: does not want a reminder on the lock screen to reveal her condition. |
| **Devices** | Mid-range Android smartphone, often shared or with limited storage; patchy mobile data. Uses WhatsApp daily. **[VALIDATE]** |
| **Languages** | Tamil or the regional language first; some English. Reads WhatsApp messages; may prefer voice or large text. (Roadmap: en-IN at v1, hi-IN and ta-IN later; regional language support was not selected for v1.0, so confirm the launch language.) **[VALIDATE]** |
| **Needs from product** | Automatic reminders on by default; discreet wording; WhatsApp channel; simple, large UI; works on weak data; consent she can see and revoke. |

**Key journey: after a check-up**
1. Books a follow-up slot with her verified doctor and pays in the app.
2. Consents to the doctor seeing her history for this visit.
3. Doctor writes a structured prescription (medicines, one injection schedule, HbA1c test due in 30 days).
4. Reminders are created automatically; she receives them on WhatsApp and the app, discreetly worded.
5. She marks doses taken; a refill-due alert arrives before she runs out.
6. Before the review, the app prompts her to book the test, then the follow-up appointment.
7. The doctor sees the result before the visit.

---

## 2. Doctor (clinic GP or specialist) — "Dr. Arun", 42

| | |
|---|---|
| **Who** | Runs or works at a small clinic; sees 30–60 patients a day; writes prescriptions by hand or in a basic system. **[VALIDATE]** |
| **Goals** | Write a prescription **faster than on paper**. Get patients to take medicines and return for follow-up. See relevant history (with consent) in the consult. Get paid reliably. Stay legally safe on telemedicine prescribing. |
| **Pains** | Little time per patient. Does not know whether the patient followed the plan. Patients miss follow-ups and tests. Payment and no-show problems. Fear of liability for telemedicine rules (drug lists, Schedule X). Does not want a heavy new system that slows the clinic. |
| **Devices** | Smartphone and a clinic laptop or desktop; reception may use a separate device. Reliable clinic Wi-Fi, but video on the move depends on mobile data. **[VALIDATE]** |
| **Languages** | English for clinical notes; regional language or Hindi with patients. |
| **Needs from product** | Fast templates; registration number shown on the prescription PDF; automatic blocking of prohibited drugs; clinic-side day view; availability management; consent-based history; overdue-follow-up list (v2.0); fair verification (clear, quick). |

**Key journey: a consultation**
1. Signs up; submits registration number, council and identity; verified before appearing in search.
2. Sets availability. Receives a booking and sees the day's list.
3. Starts the (in-person or video) consult after patient consent; views consented history.
4. Writes notes and a structured prescription from a template; the system blocks disallowed drugs.
5. Reminders are created for the patient automatically.
6. Later, sees the payment released after the consult and which patients are overdue (v1.2 / v2.0).

---

## 3. Nurse (home-care and injection visits) — "Nurse Meena", 34

*Arrives in v4.0. Open founder decision: keep that order or bring a smaller nurse feature
forward before v1.0 scope freeze.* **[DECISION]**

| | |
|---|---|
| **Who** | Registered nurse working through a partner agency; gives prescribed injections and provides home care. **[VALIDATE]** |
| **Goals** | Steady, well-planned visits. Safe practice with clear prescriptions and patient history. Fair pay and manageable travel. Protection if there is a reaction. |
| **Pains** | Unclear or missing prescription details. Unknown allergy history. Cold-chain and kit responsibilities. Liability if something goes wrong. Last-minute cancellations. Unsafe or unclear home settings. |
| **Devices** | Smartphone, mobile data while travelling. **[VALIDATE]** |
| **Languages** | Regional language plus English for clinical terms. |
| **Needs from product** | Verification (council registration or NUID, identity, agency confirmation, insurance); pre-visit safety checklist (valid prescription, allergies, first-dose policy, cold chain, emergency kit); route and schedule; visit record written to the patient history; escalation path for adverse reactions. |

**Key journey: an injection visit**
1. Verified once through the nurse checks.
2. Receives a booked visit tied to the patient's injection schedule.
3. Pre-visit: sees prescription, allergy history, first-dose policy and cold-chain notes.
4. Gives the injection; completes the observation period; handles sharps and waste.
5. Records the visit; it appears in the patient's history and the doctor can see it with consent.
6. If a reaction occurs, follows the escalation path.

---

## 4. Caregiver (extension of the patient role) — "Karthik", 32

| | |
|---|---|
| **Who** | Son living in another city or abroad, supporting an elderly parent (Lakshmi). Acts only with her permission and only for what she allows. **[VALIDATE]** |
| **Goals** | Know if his parent missed doses or appointments. Book and pay for care remotely. Get alerted in emergencies. Not intrude beyond what she consents to. |
| **Pains** | Cannot see whether medicines are taken. Finds out about problems late. Cannot arrange appointments or tests from a distance. |
| **Devices** | Modern smartphone, good connectivity; uses WhatsApp and UPI. |
| **Languages** | English and the regional language. |
| **Needs from product** | Invitation and permission set by the adult; revocable at any time; alerts for missed doses (after the family's grace period), missed appointments, upcoming tests and SOS; family view in the calendar; book and pay on behalf; all access audited. |

**Key journey: supporting a parent remotely**
1. Lakshmi invites Karthik and chooses what he can see and do.
2. Karthik sees the family calendar: medicines, appointments, tests.
3. A dose is missed past the grace period; he gets an alert and calls her.
4. He books the due test and the follow-up appointment for her and pays.
5. Lakshmi can revoke his access at any time; each access is recorded.

---

## 5. Supporting roles (not full personas; smaller portals)
Clinic admin (v1.0), lab partner (v2.0), pharmacy partner (v3.0), internal verifier, support
agent, administrator. Their permissions are in the Roles sheet; internal access is
time-limited, needs a stated reason and is audit-logged.

## 6. Open items for the founder
1. Launch languages for v1.0: English only, or English plus Hindi/Tamil/other?
2. Which chronic conditions and which city/state do we start with for v0?
3. Nurse timing decision (see section 3).
4. Do you want any persona added or removed?
