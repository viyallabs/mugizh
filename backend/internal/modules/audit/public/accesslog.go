package public

import (
	"context"
	"time"
)

// AccessEntry is one allowed read of a patient's record by a person (S-170,
// consent-design §8). References and codes only; the app resolves names for display.
type AccessEntry struct {
	EventID    string
	OccurredAt time.Time
	ActorRef   string
	ActorRole  string // role used for the read, e.g. "doctor"
	Kind       string // data kind read, e.g. "clinical_history"
	Purpose    string // access tier or grant basis, e.g. "T3_history_share"
}

// AccessLogReader answers "who viewed my record and when". It returns allowed reads by
// people only; system reads and denials are kept for security but never returned here
// (consent-design §8 [CONFIRM]). Newest first; cursor is opaque and empty at the end.
type AccessLogReader interface {
	AccessLogFor(ctx context.Context, subjectRef, cursor string, limit int) (entries []AccessEntry, next string, err error)
}
