package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
)

const bucket = "synthetic-blobs"

// fakeS3 implements the four calls the adapter uses, path-style. It checks the behaviour the
// adapter relies on (If-None-Match, checksum header, pagination). Real-S3 semantics (Object
// Lock, SSE-KMS, conditional writes on versioned buckets) are verified on staging [VALIDATE].
type fakeS3 struct {
	mu       sync.Mutex
	objs     map[string][]byte
	pageSize int
	failWith int // non-zero: every request answers this status with an AccessDenied body
	puts     int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	xmlErr := func(status int, code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>synthetic</Message><RequestId>1</RequestId></Error>`, code)
	}
	if f.failWith != 0 {
		xmlErr(f.failWith, "AccessDenied")
		return
	}
	prefix := "/" + bucket
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		xmlErr(404, "NoSuchBucket")
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	switch {
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		if got := r.Header.Get("X-Amz-Checksum-Sha256"); got != "" {
			sum := sha256.Sum256(body)
			if got != base64.StdEncoding.EncodeToString(sum[:]) {
				xmlErr(400, "BadDigest")
				return
			}
		} else {
			xmlErr(400, "MissingChecksum")
			return
		}
		if r.Header.Get("If-None-Match") == "*" {
			if _, ok := f.objs[key]; ok {
				xmlErr(412, "PreconditionFailed")
				return
			}
		}
		f.objs[key] = body
		f.puts++
		w.WriteHeader(200)
	case r.Method == http.MethodGet && key != "":
		b, ok := f.objs[key]
		if !ok {
			xmlErr(404, "NoSuchKey")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	case r.Method == http.MethodGet:
		f.list(w, r)
	case r.Method == http.MethodDelete:
		delete(f.objs, key)
		w.WriteHeader(204)
	default:
		xmlErr(400, "Unsupported")
	}
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	Name                  string
	Prefix                string
	KeyCount              int
	MaxKeys               int
	IsTruncated           bool
	NextContinuationToken string       `xml:",omitempty"`
	Contents              []listObject `xml:"Contents"`
}

type listObject struct {
	Key string
}

func (f *fakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pre := q.Get("prefix")
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, pre) {
			keys = append(keys, k)
		}
	}
	// Deliberately unsorted across pages would hide bugs; S3 returns sorted, so do the same
	// but reverse the page order check in the test by sorting the final result.
	sort.Strings(keys)
	start := 0
	if tok := q.Get("continuation-token"); tok != "" {
		start, _ = strconv.Atoi(tok)
	}
	end := start + f.pageSize
	if end > len(keys) {
		end = len(keys)
	}
	res := listResult{Name: bucket, Prefix: pre, KeyCount: end - start, MaxKeys: 1000}
	for _, k := range keys[start:end] {
		res.Contents = append(res.Contents, listObject{Key: k})
	}
	if end < len(keys) {
		res.IsTruncated, res.NextContinuationToken = true, strconv.Itoa(end)
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func newStore(t *testing.T, f *fakeS3) *Store {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:           "ap-south-1",
		Credentials:      credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", ""), // test only
		RetryMaxAttempts: 1,
		HTTPClient:       srv.Client(),
	}
	return fromAWSConfig(cfg, Config{Bucket: bucket, Region: "ap-south-1", Endpoint: srv.URL, PathStyle: true})
}

func newFake() *fakeS3 { return &fakeS3{objs: map[string][]byte{}, pageSize: 2} }

func TestPutGetRoundTripAndWriteOnce(t *testing.T) {
	s := newStore(t, newFake())
	ctx := context.Background()
	if err := s.PutWriteOnce(ctx, "documents/u1/a.bin", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	b, err := s.Get(ctx, "documents/u1/a.bin")
	if err != nil || string(b) != "hello" {
		t.Fatalf("get: %q %v", b, err)
	}
	if err := s.PutWriteOnce(ctx, "documents/u1/a.bin", []byte("other")); !errors.Is(err, blobstore.ErrExists) {
		t.Fatalf("second put: %v", err)
	}
	if b, _ = s.Get(ctx, "documents/u1/a.bin"); string(b) != "hello" {
		t.Fatal("object overwritten")
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	s := newStore(t, newFake())
	if _, err := s.Get(context.Background(), "documents/none"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestListPaginatesAndSorts(t *testing.T) {
	f := newFake()
	s := newStore(t, f)
	ctx := context.Background()
	for _, k := range []string{"x/e", "x/b", "x/d", "x/a", "x/c", "y/z"} {
		if err := s.PutWriteOnce(ctx, k, []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, "x/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "x/a,x/b,x/c,x/d,x/e" {
		t.Fatalf("got %v", got)
	}
	if got, _ = s.List(ctx, "nothing/"); len(got) != 0 {
		t.Fatalf("empty prefix: %v", got)
	}
}

func TestDeleteMissingIsNotError(t *testing.T) {
	s := newStore(t, newFake())
	ctx := context.Background()
	_ = s.PutWriteOnce(ctx, "documents/a", []byte("1"))
	if err := s.Delete(ctx, "documents/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "documents/a"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatal("still there")
	}
	if err := s.Delete(ctx, "documents/a"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestInvalidKeysRefused(t *testing.T) {
	f := newFake()
	s := newStore(t, f)
	ctx := context.Background()
	for _, k := range []string{"", "/a", "a/../b", "a//b", "./a", "A", ".hidden"} {
		if err := s.PutWriteOnce(ctx, k, nil); err == nil {
			t.Errorf("put %q accepted", k)
		}
		if _, err := s.Get(ctx, k); err == nil {
			t.Errorf("get %q accepted", k)
		}
		if err := s.Delete(ctx, k); err == nil {
			t.Errorf("delete %q accepted", k)
		}
	}
	if f.puts != 0 {
		t.Fatal("an invalid key reached the store")
	}
}

func TestGetRefusesObjectsOverTheCap(t *testing.T) {
	f := newFake()
	s := newStore(t, f)
	f.objs["documents/big"] = make([]byte, MaxObjectBytes+1)
	f.objs["documents/max"] = make([]byte, MaxObjectBytes)
	if _, err := s.Get(context.Background(), "documents/big"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big: %v", err)
	}
	b, err := s.Get(context.Background(), "documents/max")
	if err != nil || len(b) != MaxObjectBytes {
		t.Fatalf("max: %d %v", len(b), err)
	}
}

func TestPermissionAndTransportErrorsAreWrapped(t *testing.T) {
	f := newFake()
	s := newStore(t, f)
	f.failWith = 403
	ctx := context.Background()
	if err := s.PutWriteOnce(ctx, "documents/a", []byte("1")); err == nil || errors.Is(err, blobstore.ErrExists) {
		t.Errorf("put: %v", err)
	}
	if _, err := s.Get(ctx, "documents/a"); err == nil || errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := s.List(ctx, "documents/"); err == nil {
		t.Error("list: no error")
	}
	if err := s.Delete(ctx, "documents/a"); err == nil {
		t.Error("delete: no error")
	}

	// Server gone: transport error, no panic.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	cfg := aws.Config{Region: "ap-south-1", RetryMaxAttempts: 1, Credentials: credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", "")}
	dead := fromAWSConfig(cfg, Config{Bucket: bucket, Region: "ap-south-1", Endpoint: url, PathStyle: true})
	if _, err := dead.Get(ctx, "documents/a"); err == nil || errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("transport: %v", err)
	}
}

func TestPutSendsLargeBodyIntact(t *testing.T) {
	s := newStore(t, newFake())
	ctx := context.Background()
	data := bytes.Repeat([]byte("abc123"), 200_000)
	if err := s.PutWriteOnce(ctx, "documents/large", data); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Get(ctx, "documents/large"); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("large round trip failed: %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
}
