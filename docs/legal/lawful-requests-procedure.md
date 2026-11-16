# Procedure for government, police, court and regulator requests

**Added:** 2026-11-16 (compliance review R-06) · **Owner:** Grievance Officer and the nodal contact, with the lawyer · **Status:** DRAFT for lawyer review (S-009). **Not legal advice.** Every legal reference is marked [VALIDATE].

**Who this is for:** whoever receives a request for user data or content removal from an authority. **Health records are confidential.** Doctors may disclose them only when the law requires (IMC Regulations 2002), and we hold them for patients and doctors. So **never hand over data on an informal request.**

## 1. What counts as a request

| From | Typical legal basis [VALIDATE] | Time limit |
|---|---|---|
| Police (produce documents or information) | Bharatiya Nagarik Suraksha Sanhita 2023, s.94 (replaced CrPC s.91 from 1 Jul 2024) | As stated in the notice |
| Authorised government agency (information or assistance) | IT Act; Intermediary Rules 2021, rule 3(1)(j) | **As soon as possible, and within 72 hours** of the order |
| Government order to remove or disable content | IT Act s.69A or s.79(3)(b); Intermediary Rules as amended 2026 | **3 hours** |
| Court (summons, order, warrant) | Court's powers | As ordered |
| CERT-In | IT Act s.70B; CERT-In Directions 2022 | As stated in the direction |
| Data Protection Board; central government | DPDP Act s.28 (Board inquiries), s.36 (government may call for information) | As stated |
| State Medical Council or NMC (complaint about a doctor) | NMC Act 2019 and regulations | As stated |
| Tax authorities | Income-tax Act, CGST Act | As stated |

**Not lawful requests on their own:**
- emails from personal accounts;
- phone calls;
- messaging apps;
- requests from private parties, including relatives and employers;
- requests without a legal provision or authority named.

Ask them to send a proper written request.

## 2. Steps

| # | Step | Detail |
|---|---|---|
| 1 | **Receive** | Only through the published nodal contact (grievance page). Log it at once with a reference, time received and deadline. Requests that arrive elsewhere are forwarded to the nodal contact the same day. |
| 2 | **Verify the requester** | A written order on letterhead, with the officer's name, rank and signature. It must come from an official domain (for example `gov.in`, `nic.in`) or by hand or post. **Call back on an official number found independently.** No verification, no data. |
| 3 | **Check the legal basis and scope** | The law cited; which person and what data; the time period. **Every request for health records, or any request that is unclear or broad, goes to the lawyer before anything is disclosed** (same day for 72-hour and 3-hour deadlines). |
| 4 | **Collect the minimum** | Only what the order requires. Access through a just-in-time grant with reason code `legal_order` and the request reference (access-control design §5). This includes data under the legal-minimum hold (RET-18). |
| 5 | **Deliver securely** | An encrypted file to the official address, or by hand against a receipt. Never by personal email or chat. |
| 6 | **Tell the user** | Inform the affected user, **unless the order or the law forbids it** (for example to protect an investigation) [VALIDATE per case]. |
| 7 | **Record** | Request, verification, lawyer advice, what was disclosed, when and to whom. Audit event. Kept 3 years after closure (RET-09). |
| 8 | **Review monthly** | Number of requests, types, response times. Patterns go to the founder. |

**Emergencies:** if there is a real and immediate threat to someone's life, the Grievance Officer may disclose the minimum needed to emergency services at once. DPDP s.7(d) and s.7(f) allow processing for medical emergencies and threats to life [VALIDATE]. Record it, and get the lawyer's review afterwards.

## 3. Content removal orders

- Disable access to the named content **within 3 hours**.
- Preserve it and its records for **180 days** (Intermediary Rules 3(1)(g); RET-18).
- Tell the user who posted it, unless the order forbids it.
- Log the action.
- The grievance queue (S-142) has a timer for this.

## 4. Build notes

- **S-128:** JIT reason code `legal_order`; a release of held data needs a second approver.
- **S-142:** a "lawful request" type with deadline timers (72 h, 3 h, or as stated).
- **Grievance page:** publish the nodal contact (name, email, address) [FILL].
