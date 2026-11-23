-- Patient profile (S-108; data inventory 4.1). Every personal field is sealed with the
-- patient key (K-DP-03), so erasure can shred it with the key (K-DP-07). The preferred
-- language stays in user_account (a language code, not sensitive on its own).
-- The health fields (blood group, allergies, emergency contact) live here, not in records:
-- the inventory has no profile rows in 4.2 and the encryption design puts "profile health
-- fields" under the patient key. No Aadhaar field (K-DP-05).
CREATE TABLE profile (
  user_id              uuid        PRIMARY KEY REFERENCES user_account (id),
  full_name_ct         bytea       NOT NULL,
  dob_ct               bytea       NOT NULL,
  sex_ct               bytea       NOT NULL,
  blood_group_ct       bytea,
  allergies_ct         bytea,
  emergency_contact_ct bytea,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
