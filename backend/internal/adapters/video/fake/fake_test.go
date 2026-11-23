package fake_test

import (
	"context"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
)

var _ video.Client = (*fake.Client)(nil)

// ADR-13: recording is forced off and reported as off.
func TestVideoFakeNeverRecords(t *testing.T) {
	c := &fake.Client{}
	r, err := c.CreateRoom(context.Background(), video.RoomRequest{SessionRef: "s1", StartsAt: time.Now()})
	if err != nil || r.Recording {
		t.Fatalf("room: %+v %v", r, err)
	}
}

// Tokens are bound to one room and one participant, and expire.
func TestVideoFakeTokenBinding(t *testing.T) {
	c := &fake.Client{}
	ctx := context.Background()
	r, _ := c.CreateRoom(ctx, video.RoomRequest{SessionRef: "s1"})
	exp := time.Now().Add(time.Minute)
	tok, err := c.IssueJoinToken(ctx, video.TokenRequest{RoomID: r.ID, ParticipantRef: "p1", Role: video.RolePatient, ExpiresAt: exp})
	if err != nil {
		t.Fatal(err)
	}
	if room, p, ok := c.Validate(tok.Value, time.Now()); !ok || room != r.ID || p != "p1" {
		t.Fatalf("validate: %s %s %v", room, p, ok)
	}
	if _, _, ok := c.Validate(tok.Value, exp.Add(time.Second)); ok {
		t.Fatal("expired token accepted")
	}
	if _, err := c.IssueJoinToken(ctx, video.TokenRequest{RoomID: "nope", ParticipantRef: "p1"}); err == nil {
		t.Fatal("token for unknown room")
	}
}
