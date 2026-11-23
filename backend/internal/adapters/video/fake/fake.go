// Package fake is the video test double (adapter rule A10). Refused outside local, test and
// dev by the config loader. It records every call so tests can prove what left the system.
package fake

import (
	"context"
	"strconv"
	"sync"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
)

// Client is the fake.
type Client struct {
	mu     sync.Mutex
	n      int
	Rooms  map[string]video.RoomRequest
	Tokens map[string]video.TokenRequest // token value -> request
	Events map[string][]video.Event
	Ended  []string

	// FailCreateVideo makes a video room fail (audio fallback tests).
	FailCreateVideo error
	// FailCreateAudio makes an audio-only room fail too (technical failure tests).
	FailCreateAudio error
	// FailEvents makes ParticipantEvents fail.
	FailEvents error
	// BrokenRecording simulates a vendor that reports recording as on.
	BrokenRecording bool
}

func (c *Client) init() {
	if c.Rooms == nil {
		c.Rooms, c.Tokens, c.Events = map[string]video.RoomRequest{}, map[string]video.TokenRequest{}, map[string][]video.Event{}
	}
}

// CreateRoom implements video.Client. Recording is always off unless BrokenRecording is set.
func (c *Client) CreateRoom(_ context.Context, r video.RoomRequest) (video.Room, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	if r.AudioOnly && c.FailCreateAudio != nil {
		return video.Room{}, c.FailCreateAudio
	}
	if !r.AudioOnly && c.FailCreateVideo != nil {
		return video.Room{}, c.FailCreateVideo
	}
	c.n++
	id := "room-" + strconv.Itoa(c.n)
	c.Rooms[id] = r
	return video.Room{ID: id, AudioOnly: r.AudioOnly, Recording: c.BrokenRecording}, nil
}

// IssueJoinToken implements video.Client.
func (c *Client) IssueJoinToken(_ context.Context, r video.TokenRequest) (video.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	if _, ok := c.Rooms[r.RoomID]; !ok {
		return video.Token{}, adapters.E(adapters.NotFound, "ROOM")
	}
	c.n++
	tok := "tok-" + strconv.Itoa(c.n)
	c.Tokens[tok] = r
	return video.Token{Value: tok, ExpiresAt: r.ExpiresAt}, nil
}

// Validate is what the vendor media server does with a token: it returns the room and
// participant the token is bound to, or false when unknown or expired.
func (c *Client) Validate(token string, at time.Time) (roomID, participantRef string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, found := c.Tokens[token]
	if !found || !at.Before(r.ExpiresAt) {
		return "", "", false
	}
	return r.RoomID, r.ParticipantRef, true
}

// Simulate adds a participant event, as the vendor would report it.
func (c *Client) Simulate(roomID, participantRef string, kind video.EventKind, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.n++
	c.Events[roomID] = append(c.Events[roomID], video.Event{ID: "ev-" + strconv.Itoa(c.n), ParticipantRef: participantRef, Kind: kind, At: at.UTC()})
}

// ParticipantEvents implements video.Client.
func (c *Client) ParticipantEvents(_ context.Context, roomID string) ([]video.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	if c.FailEvents != nil {
		return nil, c.FailEvents
	}
	return append([]video.Event(nil), c.Events[roomID]...), nil
}

// EndRoom implements video.Client.
func (c *Client) EndRoom(_ context.Context, roomID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.Ended = append(c.Ended, roomID)
	return nil
}
