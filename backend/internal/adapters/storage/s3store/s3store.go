// Package s3store is the S3-compatible implementation of blobstore.Store (ADR-31 dual-cloud
// rule, adapter-interfaces §14). It runs on AWS S3 in staging and production and, through
// Config.Endpoint, on other S3-compatible stores (OCI Object Storage, MinIO) in development.
//
// Encryption at rest is the bucket's default SSE-KMS setting (infrastructure); this adapter
// does not override it. Content is already field-encrypted by the caller where sensitive.
// Credentials come from the default chain (the ECS task role); there is no static key option.
package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
)

// MaxObjectBytes is the largest object Get will read; larger objects are refused.
const MaxObjectBytes = 64 << 20

// ErrTooLarge is returned by Get for an object above MaxObjectBytes.
var ErrTooLarge = errors.New("s3store: object exceeds the 64 MiB read limit")

// callTimeout bounds one request when the caller set no deadline.
const callTimeout = 60 * time.Second

// Config selects the bucket. Endpoint is optional (S3-compatible stores); PathStyle is
// needed by most of them.
type Config struct {
	Bucket    string
	Region    string
	Endpoint  string
	PathStyle bool
}

// Store implements blobstore.Store (and Delete) on one bucket.
type Store struct {
	client *s3.Client
	bucket string
}

var _ blobstore.Store = (*Store)(nil)

// New builds a Store using the default AWS credential chain.
func New(ctx context.Context, c Config) (*Store, error) {
	if c.Bucket == "" || c.Region == "" {
		return nil, errors.New("s3store: bucket and region are required")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("s3store: load AWS configuration: %w", err)
	}
	return fromAWSConfig(cfg, c), nil
}

func fromAWSConfig(cfg aws.Config, c Config) *Store {
	cl := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = c.PathStyle
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			// Other S3-compatible stores may not accept the SDK's default checksums.
			// Put sets an explicit SHA-256 itself.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	return &Store{client: cl, bucket: c.Bucket}
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, callTimeout)
}

// errCode reports the S3 error code and HTTP status of err, if it carries them.
func errCode(err error) (code string, status int) {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		code = ae.ErrorCode()
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		status = re.HTTPStatusCode()
	}
	return code, status
}

// wrapErr keeps the error chain (so context errors stay detectable) under a store-specific prefix.
func wrapErr(op string, err error) error { return fmt.Errorf("s3store: %s: %w", op, err) }

// PutWriteOnce implements blobstore.Store: If-None-Match "*" makes S3 refuse an existing key.
func (s *Store) PutWriteOnce(ctx context.Context, key string, data []byte) error {
	if err := blobstore.ValidKey(key); err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	sum := sha256.Sum256(data)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(key),
		Body:           bytes.NewReader(data),
		IfNoneMatch:    aws.String("*"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	if err != nil {
		// 409 ConditionalRequestConflict: a concurrent conditional write to the same key won the race.
		if code, status := errCode(err); code == "PreconditionFailed" || status == http.StatusPreconditionFailed || code == "ConditionalRequestConflict" {
			return blobstore.ErrExists
		}
		return wrapErr("put", err)
	}
	return nil
}

// Get implements blobstore.Store. It reads at most MaxObjectBytes.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := blobstore.ValidKey(key); err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if code, status := errCode(err); errors.As(err, &nsk) || code == "NoSuchKey" || status == http.StatusNotFound {
			return nil, blobstore.ErrNotFound
		}
		return nil, wrapErr("get", err)
	}
	defer func() { _ = out.Body.Close() }()
	if out.ContentLength != nil && *out.ContentLength > MaxObjectBytes {
		return nil, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(out.Body, MaxObjectBytes+1))
	if err != nil {
		return nil, wrapErr("read", err)
	}
	if len(b) > MaxObjectBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// List implements blobstore.Store: every key with the prefix, across all pages, sorted.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	var out []string
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, wrapErr("list", err)
		}
		for _, o := range page.Contents {
			out = append(out, aws.ToString(o.Key))
		}
		if !aws.ToBool(page.IsTruncated) || aws.ToString(page.NextContinuationToken) == "" {
			break
		}
		token = page.NextContinuationToken
	}
	sort.Strings(out)
	return out, nil
}

// Delete removes an object; a missing object is not an error. It is used for erasure and
// the prohibited-identifier purge on the data bucket only. The bucket is versioned, so
// earlier versions expire under its lifecycle rule (crypto-shredding covers backups).
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := blobstore.ValidKey(key); err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var nsk *types.NoSuchKey
		if code, status := errCode(err); errors.As(err, &nsk) || code == "NoSuchKey" || status == http.StatusNotFound {
			return nil
		}
		return wrapErr("delete", err)
	}
	return nil
}
