package awskms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
)

const keyARN = "arn:aws:kms:ap-south-1:000000000000:key/mrk-synthetic0001"

// fakeKMS speaks the KMS JSON 1.1 protocol for Encrypt and Decrypt. Real-KMS semantics
// (multi-region keys, key policies) are verified on staging [VALIDATE].
type fakeKMS struct {
	mu        sync.Mutex
	blobs     map[string]fakeBlob
	n         int
	fail      func(target string) (status int, errType string) // nil = healthy
	calls     []fakeCall
	rawBodies []string
}

type fakeBlob struct {
	keyID string
	ctx   map[string]string
	pt    []byte
}

type fakeCall struct {
	Target string
	KeyID  string
	Ctx    map[string]string
}

func newFakeKMS() *fakeKMS { return &fakeKMS{blobs: map[string]fakeBlob{}} }

func (f *fakeKMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.rawBodies = append(f.rawBodies, string(body))
	target := r.Header.Get("X-Amz-Target")
	var in struct {
		KeyId             string
		Plaintext         []byte
		CiphertextBlob    []byte
		EncryptionContext map[string]string
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, fakeCall{Target: target, KeyID: in.KeyId, Ctx: in.EncryptionContext})
	if f.fail != nil {
		if st, typ := f.fail(target); st != 0 {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.Header().Set("X-Amzn-ErrorType", typ)
			w.WriteHeader(st)
			_, _ = fmt.Fprintf(w, `{"__type":%q,"message":"synthetic"}`, typ)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	switch target {
	case "TrentService.Encrypt":
		f.n++
		id := fmt.Sprintf("blob-%d", f.n)
		f.blobs[id] = fakeBlob{keyID: in.KeyId, ctx: in.EncryptionContext, pt: in.Plaintext}
		_ = json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": []byte(id), "KeyId": keyARN})
	case "TrentService.Decrypt":
		b, ok := f.blobs[string(in.CiphertextBlob)]
		if !ok || b.keyID != in.KeyId || len(b.ctx) != len(in.EncryptionContext) || b.ctx[contextKey] != in.EncryptionContext[contextKey] {
			w.Header().Set("X-Amzn-ErrorType", "InvalidCiphertextException")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"InvalidCiphertextException","message":"synthetic"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Plaintext": b.pt, "KeyId": keyARN})
	default:
		http.Error(w, "unknown target "+target, http.StatusBadRequest)
	}
}

func newProvider(t *testing.T, f *fakeKMS) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:           "ap-south-1",
		Credentials:      credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", ""), // test only
		BaseEndpoint:     aws.String(srv.URL),
		RetryMaxAttempts: 1,
		HTTPClient:       srv.Client(),
	}
	return fromAWSConfig(cfg, Config{KeyID: keyARN, Region: "ap-south-1"})
}

func TestNameAndRoot(t *testing.T) {
	p := newProvider(t, newFakeKMS())
	if p.Name() != "aws-kms" || p.CurrentRoot() != keyARN {
		t.Fatalf("name %q root %q", p.Name(), p.CurrentRoot())
	}
}

func TestRoundTripSendsContextAndKeyID(t *testing.T) {
	f := newFakeKMS()
	p := newProvider(t, f)
	ctx := context.Background()
	dk, aad := bytes.Repeat([]byte{7}, 32), []byte("keystore-row-1")
	wrapped, root, err := p.Wrap(ctx, dk, aad)
	if err != nil || root != keyARN {
		t.Fatalf("wrap: %v root %q", err, root)
	}
	got, err := p.Unwrap(ctx, wrapped, aad)
	if err != nil || !bytes.Equal(got, dk) {
		t.Fatalf("unwrap: %v", err)
	}
	sum := sha256.Sum256(aad)
	want := hex.EncodeToString(sum[:])
	if len(f.calls) != 2 {
		t.Fatalf("calls %d", len(f.calls))
	}
	for _, c := range f.calls {
		if c.Ctx[contextKey] != want || len(c.Ctx) != 1 {
			t.Errorf("%s: encryption context %v", c.Target, c.Ctx)
		}
		if c.KeyID != keyARN {
			t.Errorf("%s: KeyId %q", c.Target, c.KeyID)
		}
	}
}

func TestWrongAADIsRejectedNotUnavailable(t *testing.T) {
	p := newProvider(t, newFakeKMS())
	ctx := context.Background()
	wrapped, _, err := p.Wrap(ctx, []byte("0123456789abcdef0123456789abcdef"), []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Unwrap(ctx, wrapped, []byte("row-2"))
	if !errors.Is(err, ErrRejected) || errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("want ErrRejected only, got %v", err)
	}
	// Garbage ciphertext: also rejected, never a plaintext.
	if _, err = p.Unwrap(ctx, []byte("garbage"), []byte("row-1")); !errors.Is(err, ErrRejected) || errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestOtherKeyCiphertextRefused(t *testing.T) {
	f := newFakeKMS()
	p := newProvider(t, f)
	wrapped, _, err := p.Wrap(context.Background(), []byte("0123456789abcdef0123456789abcdef"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	other := newProvider(t, f)
	other.keyID = "arn:aws:kms:ap-south-1:000000000000:key/other"
	if _, err = other.Unwrap(context.Background(), wrapped, []byte("a")); !errors.Is(err, ErrRejected) {
		t.Fatalf("ciphertext of another key must be refused, got %v", err)
	}
}

func TestOutagesAreUnavailable(t *testing.T) {
	cases := map[string]struct {
		status int
		typ    string
	}{
		"500 internal":   {500, "KMSInternalException"},
		"throttling":     {400, "ThrottlingException"},
		"limit exceeded": {400, "LimitExceededException"},
		"access denied":  {400, "AccessDeniedException"},
		"key disabled":   {400, "DisabledException"},
		"invalid state":  {409, "KMSInvalidStateException"},
		"key unavail":    {500, "KeyUnavailableException"},
		"dep timeout":    {503, "DependencyTimeoutException"},
		"plain 503":      {503, "ServiceUnavailable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeKMS()
			p := newProvider(t, f)
			f.fail = func(string) (int, string) { return c.status, c.typ }
			if _, _, err := p.Wrap(context.Background(), []byte("k"), []byte("a")); !errors.Is(err, keys.ErrUnavailable) || errors.Is(err, ErrRejected) {
				t.Errorf("wrap: %v", err)
			}
			if _, err := p.Unwrap(context.Background(), []byte("c"), []byte("a")); !errors.Is(err, keys.ErrUnavailable) || errors.Is(err, ErrRejected) {
				t.Errorf("unwrap: %v", err)
			}
		})
	}
}

func TestTransportErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	cfg := aws.Config{Region: "ap-south-1", RetryMaxAttempts: 1, BaseEndpoint: aws.String(url),
		Credentials: credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", "")}
	p := fromAWSConfig(cfg, Config{KeyID: keyARN, Region: "ap-south-1"})
	if _, _, err := p.Wrap(context.Background(), []byte("k"), []byte("a")); !errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultDeadlineApplied(t *testing.T) {
	ctx, cancel := withTimeout(context.Background())
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > callTimeout || time.Until(d) < callTimeout-time.Second {
		t.Fatalf("deadline %v %v", d, ok)
	}
	own, c2 := context.WithTimeout(context.Background(), time.Hour)
	defer c2()
	got, c3 := withTimeout(own)
	defer c3()
	if d2, _ := got.Deadline(); time.Until(d2) < 59*time.Minute {
		t.Fatal("caller deadline replaced")
	}
}

func TestSlowKMSTimesOutAsUnavailable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release) // runs first, so the blocked handler returns before srv.Close waits
	cfg := aws.Config{Region: "ap-south-1", RetryMaxAttempts: 1, BaseEndpoint: aws.String(srv.URL), HTTPClient: srv.Client(),
		Credentials: credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", "")}
	p := fromAWSConfig(cfg, Config{KeyID: keyARN, Region: "ap-south-1"})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := p.Wrap(ctx, []byte("k"), []byte("a")); !errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsAndLogsCarryNoSecrets(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)

	f := newFakeKMS()
	p := newProvider(t, f)
	dk, aad := []byte("SECRET-DATA-KEY-0123456789abcdef"), []byte("SECRET-AAD-ROW")
	wrapped, _, err := p.Wrap(context.Background(), dk, aad)
	if err != nil {
		t.Fatal(err)
	}
	_, uerr := p.Unwrap(context.Background(), wrapped, []byte("SECRET-OTHER-AAD"))
	f.fail = func(string) (int, string) { return 500, "KMSInternalException" }
	_, _, werr := p.Wrap(context.Background(), dk, aad)
	all := logs.String() + fmt.Sprint(uerr) + fmt.Sprint(werr)
	for _, s := range []string{string(dk), string(aad), "SECRET-OTHER-AAD", base64.StdEncoding.EncodeToString(dk), string(wrapped), base64.StdEncoding.EncodeToString(wrapped)} {
		if strings.Contains(all, s) {
			t.Errorf("secret %q appears in logs or errors", s)
		}
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
}

// Review of the KMS adapter: the root recorded with a wrapped key must equal CurrentRoot(),
// whatever form the key ID is configured in (an alias here, while KMS answers with the key
// ARN). Otherwise crypto.RewrapAll sees every key as stale and re-wraps them on every run.
func TestWrapRootMatchesCurrentRootWithAlias(t *testing.T) {
	f := newFakeKMS()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := aws.Config{Region: "ap-south-1", Credentials: credentials.NewStaticCredentialsProvider("AKIASYNTHETIC", "synthetic-secret", ""),
		BaseEndpoint: aws.String(srv.URL), RetryMaxAttempts: 1, HTTPClient: srv.Client()}
	p := fromAWSConfig(cfg, Config{KeyID: "alias/mugizh-staging-data", Region: "ap-south-1"})
	_, root, err := p.Wrap(context.Background(), make([]byte, 32), []byte("row"))
	if err != nil {
		t.Fatal(err)
	}
	if root != p.CurrentRoot() {
		t.Fatalf("wrap root %q must equal CurrentRoot %q", root, p.CurrentRoot())
	}
}
