// Package awskms is the AWS KMS implementation of keys.Provider (ADR-31 dual-cloud rule,
// encryption-design §7 and §10.1). The root key never leaves KMS: data keys are wrapped
// with kms:Encrypt and unwrapped with kms:Decrypt, both bound to the caller's AAD through
// the KMS encryption context.
//
// Credentials come from the default chain (the ECS task role in staging and production);
// there is no way to pass a static key. The adapter never logs key material, plaintext,
// ciphertext or the AAD, and its error texts carry only the KMS error code.
package awskms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
)

// ErrRejected means KMS answered and refused the ciphertext: it is corrupt, was wrapped
// under another key, or the AAD (encryption context) does not match. It is not an outage
// (keys.ErrUnavailable) and never leads to a plaintext fallback.
var ErrRejected = errors.New("awskms: ciphertext rejected by the key service")

// contextKey is the encryption-context name that carries the AAD binding.
const contextKey = "mugizh:aad"

// callTimeout bounds one KMS call when the caller set no deadline.
const callTimeout = 5 * time.Second

// Config selects the key and region. KeyID is the ARN or alias of the (multi-region) key.
type Config struct {
	KeyID  string
	Region string
}

// Provider implements keys.Provider on AWS KMS.
type Provider struct {
	client *kms.Client
	keyID  string
}

var _ keys.Provider = (*Provider)(nil)

// New builds a Provider using the default AWS credential chain.
func New(ctx context.Context, c Config) (*Provider, error) {
	if strings.TrimSpace(c.KeyID) == "" || strings.TrimSpace(c.Region) == "" {
		return nil, errors.New("awskms: key id and region are required")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("awskms: load AWS configuration: %w", err)
	}
	return fromAWSConfig(cfg, c), nil
}

func fromAWSConfig(cfg aws.Config, c Config) *Provider {
	return &Provider{client: kms.NewFromConfig(cfg), keyID: c.KeyID}
}

// Name implements keys.Provider.
func (p *Provider) Name() string { return "aws-kms" }

// CurrentRoot implements keys.Provider: the configured key ID.
func (p *Provider) CurrentRoot() string { return p.keyID }

func encryptionContext(aad []byte) map[string]string {
	sum := sha256.Sum256(aad)
	return map[string]string{contextKey: hex.EncodeToString(sum[:])}
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, callTimeout)
}

// Wrap implements keys.Provider. root is the key ARN KMS reports.
func (p *Provider) Wrap(ctx context.Context, dataKey, aad []byte) ([]byte, string, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	out, err := p.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(p.keyID),
		Plaintext:         dataKey,
		EncryptionContext: encryptionContext(aad),
	})
	if err != nil {
		return nil, "", mapError("encrypt", err)
	}
	if len(out.CiphertextBlob) == 0 {
		return nil, "", fmt.Errorf("%w: kms encrypt returned no ciphertext", keys.ErrUnavailable)
	}
	// The recorded root is the configured key ID, the same value CurrentRoot returns, so
	// crypto.RewrapAll only re-wraps when the configured key changes (KMS answers with the
	// key ARN even when an alias is configured; review of D-93).
	return out.CiphertextBlob, p.keyID, nil
}

// Unwrap implements keys.Provider. The configured KeyId is sent so KMS refuses a
// ciphertext produced under any other key.
func (p *Provider) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	out, err := p.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             aws.String(p.keyID),
		EncryptionContext: encryptionContext(aad),
	})
	if err != nil {
		return nil, mapError("decrypt", err)
	}
	if len(out.Plaintext) == 0 {
		return nil, fmt.Errorf("%w: kms decrypt returned no plaintext", keys.ErrUnavailable)
	}
	return out.Plaintext, nil
}

// mapError separates "KMS says no to this ciphertext" (ErrRejected) from everything else,
// which fails closed as keys.ErrUnavailable (callers answer 503 and alert). Only the KMS
// error code is kept in the text: never the request, the ciphertext or the AAD.
func mapError(op string, err error) error {
	var invalid *types.InvalidCiphertextException
	var wrongKey *types.IncorrectKeyException
	var usage *types.InvalidKeyUsageException
	switch {
	case errors.As(err, &invalid), errors.As(err, &wrongKey), errors.As(err, &usage):
		return fmt.Errorf("%w: kms %s: %s", ErrRejected, op, apiCode(err))
	}
	return fmt.Errorf("%w: kms %s: %s", keys.ErrUnavailable, op, apiCode(err))
}

func apiCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "transport error"
}
