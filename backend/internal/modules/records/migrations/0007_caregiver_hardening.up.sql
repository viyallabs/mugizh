-- records module, part 7: caregiver hardening (review of phase 1).
-- The patient's optional label for an invitation and its link is sealed with the patient key (K-DP-03); label_ref is
-- the invitation id the seal is bound to. Wrong-code counters also carry a keyed hash of the network for a
-- per-network limit.
ALTER TABLE caregiver_invitation ADD COLUMN label_sealed bytea;
ALTER TABLE caregiver_link ADD COLUMN label_sealed bytea, ADD COLUMN label_ref uuid;
ALTER TABLE caregiver_redeem_attempt ADD COLUMN ip_hash text NOT NULL DEFAULT '';
CREATE INDEX caregiver_redeem_attempt_ip_idx ON caregiver_redeem_attempt (ip_hash, attempted_at) WHERE ip_hash <> '';
