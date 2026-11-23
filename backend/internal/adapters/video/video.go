// Package video is the video-room adapter port (adapter-interfaces section 9, ADR-13, K-ARC-03).
//
// Rules every implementation must meet:
//   - One room per appointment, identified by our session reference only.
//   - Join tokens are issued for the two participants only, bound to one room and one opaque
//     participant reference, and are short-lived.
//   - Recording is never on. The port has no "record" switch; implementations force it off
//     and report it in Room.Recording so the consult module can fail closed if a vendor
//     ever reports otherwise.
//   - Media is relayed in the India region only (K-DP-01); the vendor config pins it.
//   - Only an opaque participant reference and a display role label leave our system, never
//     names, phone numbers or clinical data (K-DP-04).
//
// Errors are mapped to internal/adapters kinds (timeout, unavailable, ...).
package video

import (
	"context"
	"time"
)

// Role of a participant. It is the only display label sent to the vendor.
type Role string

// Participant roles.
const (
	RoleDoctor  Role = "doctor"
	RolePatient Role = "patient"
)

// RoomRequest creates the room of one appointment.
type RoomRequest struct {
	SessionRef  string    // our opaque reference; the only identifier sent
	StartsAt    time.Time // slot start (UTC)
	MaxDuration time.Duration
	// AudioOnly asks for an audio-only room: the fallback when video cannot be created.
	AudioOnly bool
}

// Room is a created room.
type Room struct {
	ID        string
	AudioOnly bool
	// Recording reports whether the vendor has recording on. It must always be false.
	Recording bool
}

// TokenRequest asks for a join token for one participant.
type TokenRequest struct {
	RoomID         string
	ParticipantRef string // opaque
	Role           Role
	ExpiresAt      time.Time
}

// Token is a short-lived join token.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// EventKind is a participant event kind.
type EventKind string

// Participant event kinds.
const (
	Joined EventKind = "joined"
	Left   EventKind = "left"
)

// Event is one participant event from the vendor (webhook or poll). ID is unique per
// event so replays are harmless.
type Event struct {
	ID             string
	ParticipantRef string
	Kind           EventKind
	At             time.Time
}

// Client is the port.
type Client interface {
	CreateRoom(ctx context.Context, r RoomRequest) (Room, error)
	IssueJoinToken(ctx context.Context, r TokenRequest) (Token, error)
	// ParticipantEvents returns every join and leave event of the room so far, oldest first.
	ParticipantEvents(ctx context.Context, roomID string) ([]Event, error)
	EndRoom(ctx context.Context, roomID string) error
}
