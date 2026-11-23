-- Review F11 (RET-12, DEL-TTL): the opaque participant reference sent to the video vendor is
-- replaced with a fresh random value a day after the consult ends, so vendor records can no
-- longer be linked to our session. Join and leave evidence (participant_event) stays under
-- RET-02.
ALTER TABLE participant ADD COLUMN ref_purged boolean NOT NULL DEFAULT false;
