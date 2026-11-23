package fake_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign/fake"
)

var _ esign.Signer = (*fake.Signer)(nil)

// TC-60 at adapter level: a signature is bound to the exact hash.
func TestTC60_OneChangedByteFailsVerification(t *testing.T) {
	s := &fake.Signer{}
	ctx := context.Background()
	h := sha256.Sum256([]byte("document"))
	sess, err := s.StartSigning(ctx, esign.StartRequest{SignerRef: "doc", DocumentHash: h[:]})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.CompleteSigning(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Verify(ctx, h[:], sig); v != esign.Valid {
		t.Fatalf("%s", v)
	}
	h2 := h
	h2[0] ^= 1
	if v, _ := s.Verify(ctx, h2[:], sig); v != esign.Invalid {
		t.Fatalf("%s", v)
	}
	if _, err := s.StartSigning(ctx, esign.StartRequest{SignerRef: "doc", DocumentHash: []byte("not a hash")}); err == nil {
		t.Fatal("only a 32-byte hash is accepted")
	}
}
