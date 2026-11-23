// Package push is the push adapter port (adapter-interfaces §6). The text it receives is
// already rendered and privacy-safe (K-DP-09): no diagnosis, no specialty, and a medicine
// name only when the patient consented to it. Vendor implementations live in push/<vendor>/.
package push

import (
	"context"
	"regexp"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
)

// Platform of a device.
type Platform string

// Platforms.
const (
	Android Platform = "android"
	IOS     Platform = "ios"
)

// Target is one registered device. The token is personal data owned by identity; this
// package only carries it to the vendor and never logs it (K-DP-04).
type Target struct {
	Token    string
	Platform Platform
}

// Message is one notification for one device.
type Message struct {
	Target Target
	// TitleKey is a translation key; the app shows the localised title (K-GLB-02).
	TitleKey string
	// Body is already rendered and privacy-safe.
	Body string
	// Data carries opaque references for deep links (screen name and IDs), never content.
	Data map[string]string
	// IdempotencyKey: the same key never produces a second notification.
	IdempotencyKey string
	// ReplaceKey (optional) is the display-replacement identifier: a later notification with the same key
	// replaces the earlier one on the device instead of showing a second one (S-165: the server push and the
	// on-device backup for the same reminder minute). Vendor adapters map it to the Android notification tag
	// and the APNs collapse identifier, never to the FCM collapse_key, which makes a message collapsible and
	// droppable while the device is offline.
	ReplaceKey string
	// Silent is a content-free, data-only message that wakes the app (S-165: "refresh your reminder
	// schedule"). It has no title and no body; the OS may delay or drop it, so it is only ever a hint.
	Silent bool
}

// Sender sends push notifications. An unusable token is reported as
// adapters.NotFound with reason ReasonTokenInvalid so the caller can remove it.
type Sender interface {
	Send(ctx context.Context, m Message) (providerMessageID string, err error)
}

// ReasonTokenInvalid is the Reason of the NotFound error for a dead device token.
const ReasonTokenInvalid = "TOKEN_INVALID"

var (
	reTitleKey = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,80}$`)
	reDataKey  = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]{0,30}$`)
	reDataVal  = regexp.MustCompile(`^[A-Za-z0-9_\-.]{1,64}$`)
	reReplace  = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

// Validate checks the message shape: a known platform, a title key, a body, and data that
// holds only short opaque values (no free text can travel in the data payload). A silent
// message has no title and no body, only data.
func Validate(m Message) error {
	if m.Target.Token == "" || (m.Target.Platform != Android && m.Target.Platform != IOS) {
		return adapters.E(adapters.Invalid, "TARGET")
	}
	if m.Silent {
		if m.TitleKey != "" || m.Body != "" || len(m.Data) == 0 {
			return adapters.E(adapters.Invalid, "SILENT")
		}
	} else if !reTitleKey.MatchString(m.TitleKey) || m.Body == "" || len(m.Body) > 400 {
		return adapters.E(adapters.Invalid, "TEXT")
	}
	if m.ReplaceKey != "" && !reReplace.MatchString(m.ReplaceKey) {
		return adapters.E(adapters.Invalid, "REPLACE_KEY")
	}
	if m.IdempotencyKey == "" {
		return adapters.E(adapters.Invalid, "IDEMPOTENCY_KEY")
	}
	for k, v := range m.Data {
		if !reDataKey.MatchString(k) || !reDataVal.MatchString(v) {
			return adapters.E(adapters.Invalid, "DATA")
		}
	}
	return nil
}
