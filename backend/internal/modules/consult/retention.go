package consult

import (
	"context"
	"time"
)

// ParticipantRefTTL is how long after a consult ends its vendor-facing participant references
// are kept (RET-12: short-lived technical data; late vendor events arrive within minutes).
const ParticipantRefTTL = 24 * time.Hour

// PurgeParticipantRefs is a job (daily): for consults that ended more than ParticipantRefTTL
// ago, each participant reference is replaced with a fresh random value and the token time
// cleared (review F11, data inventory section 4.7). Join and leave events stay (RET-02).
func (m *Module) PurgeParticipantRefs(ctx context.Context, now time.Time) (int64, error) {
	tag, err := m.pool.Exec(ctx, `UPDATE participant p SET participant_ref = gen_random_uuid(), last_token_at = NULL, ref_purged = true
		FROM session s WHERE s.id = p.session_id AND NOT p.ref_purged AND s.ended_at IS NOT NULL AND s.ended_at < $1`, now.Add(-ParticipantRefTTL))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
