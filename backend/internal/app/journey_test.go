package app_test

// End-to-end journeys through the fully assembled API (S-174 groundwork). Patients, doctors and
// clinic admins act through HTTP exactly as the apps will; reviewers and other staff act through
// module calls because staff sign-in does not exist yet (D-21). Events move between modules
// through the same subscribers the worker uses (Bus.Dispatch), and one clock drives every module
// so a journey can move to the appointment time. Synthetic data only.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"

	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

type journey struct {
	t   *testing.T
	c   *app.Core
	srv *httptest.Server
	bus *outbox.Bus
	mu  sync.Mutex
	now time.Time
}

// journeyLog receives the assembled core's log (io.Discard unless a test captures it; journeys do not run in parallel).
var journeyLog io.Writer = io.Discard

func (j *journey) clock() time.Time { j.mu.Lock(); defer j.mu.Unlock(); return j.now }
func (j *journey) at(t time.Time)   { j.mu.Lock(); j.now = t; j.mu.Unlock() }

func newJourney(t *testing.T, start time.Time) *journey {
	t.Helper()
	return newJourneyIn(t, start, "IN")
}

// newJourneyIn assembles the core with the given country pack (IN, or the synthetic XX test country).
func newJourneyIn(t *testing.T, start time.Time, pack string) *journey {
	t.Helper()
	app.UseStrictJSON()
	j := &journey{t: t, now: start}
	core := testkit.New(t, testkit.Spec{Key: "fullcore", Sources: app.MigrationSources()})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	s := app.Settings{Env: config.EnvTest, PackDir: filepath.Join("..", "..", "..", "config", "country-packs", pack),
		VendorRegister: filepath.Join("..", "..", "..", "config", "vendor-register.yaml"), DBURL: map[string]string{},
		KeystoreURL: ks.URL("keystore"), BlindIndexKey: rnd(32), LocalRootKey: rnd(32), BlobDir: t.TempDir(), SchedulerSecret: rnd(32), Now: j.clock}
	for _, m := range db.Modules {
		s.DBURL[m] = core.URL(m)
	}
	if pack == "XX" {
		// The synthetic country has no server text of its own: reuse the English catalog under its locales.
		text, err := os.ReadFile(filepath.Join("i18n", "en-IN.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		s.Catalogs = fstest.MapFS{"i18n/pt-PT.yaml": {Data: text}, "i18n/en-GB.yaml": {Data: text}}
	}
	log := slog.New(slog.NewTextHandler(journeyLog, nil))
	c, err := app.BuildCore(context.Background(), s, log)
	if err != nil {
		t.Fatalf("BuildCore: %v", err)
	}
	t.Cleanup(c.Close)
	c.Records.SetCaregiversEnabled(c.Config.Flag("caregivers")) // as cmd/api does
	j.c = c
	j.bus = outbox.NewBus(log)
	app.Subscribe(j.bus, c)
	pol, err := app.Specs()
	if err != nil {
		t.Fatal(err)
	}
	h := app.NewRouter(app.RouterConfig{CaregiversOn: c.Records.CaregiversEnabled, Log: log, Crypto: c.Crypto, Auth: c.Identity, Idempotency: c.Idem, IPPerMinute: 1_000_000, UserPerMinute: 1_000_000, Now: j.clock,
		Mount: app.Mounts(app.Modules{Identity: c.Identity, Records: c.Records, Rights: c.Rights, Verification: c.Verification, Directory: c.Directory,
			Booking: c.Booking, Payments: c.Payments, Notify: c.Notify, Admin: c.Admin, Rx: c.Rx, Consult: c.Consult, Reviews: c.ReviewsRoutes(), Sharing: c.Sharing, History: c.History},
			pol, log, j.clock)})
	j.srv = httptest.NewUnstartedServer(h)
	if addr := os.Getenv("MUGIZH_DAST_ADDR"); addr != "" { // S-179: the DAST target (dast_test.go) listens where ZAP can reach it
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = j.srv.Listener.Close()
		j.srv.Listener = l
	}
	j.srv.Start()
	t.Cleanup(j.srv.Close)
	return j
}

// pump delivers every pending event of every module to its subscribers until none are left,
// as the worker's relay and River would (subscribers are idempotent; failures fail the test).
func (j *journey) pump() {
	j.t.Helper()
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		moved := 0
		for _, m := range app.RelayModules {
			pool := j.c.Pools.For(m)
			var evs []outbox.Event
			err := db.InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
				evs = nil
				rows, err := tx.Query(ctx, `SELECT event_id, type, version, occurred_at, subject_ref, payload FROM event_outbox ORDER BY occurred_at, event_id`)
				if err != nil {
					return err
				}
				for rows.Next() {
					var e outbox.Event
					var p []byte
					if err := rows.Scan(&e.ID, &e.Type, &e.Version, &e.OccurredAt, &e.SubjectRef, &p); err != nil {
						rows.Close()
						return err
					}
					e.Producer = m
					_ = json.Unmarshal(p, &e.Payload)
					evs = append(evs, e)
				}
				rows.Close()
				if err := rows.Err(); err != nil {
					return err
				}
				for _, e := range evs {
					if err := j.bus.Dispatch(ctx, e); err != nil {
						return fmt.Errorf("%s from %s: %w", e.Type, m, err)
					}
					if _, err := tx.Exec(ctx, `DELETE FROM event_outbox WHERE event_id=$1`, e.ID); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				j.t.Fatalf("pump: %v", err)
			}
			moved += len(evs)
		}
		if moved == 0 {
			return
		}
	}
	j.t.Fatal("pump: events still flowing after 20 rounds")
}

type reply struct {
	status int
	body   map[string]any
	raw    []byte
}

func (r reply) str(path ...string) string {
	var cur any = r.body
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	s, _ := cur.(string)
	return s
}

// call sends one request as token (empty: anonymous) with the extra headers given in pairs.
func (j *journey) call(method, path, token string, body any, headers ...string) reply {
	j.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			j.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, j.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", ids.New())
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	r := reply{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &r.body)
	return r
}

// must fails the test unless the reply has the wanted status.
func (j *journey) must(r reply, want int, what string) reply {
	j.t.Helper()
	if r.status != want {
		j.t.Fatalf("%s: want %d, got %d: %s", what, want, r.status, r.raw)
	}
	return r
}

type person struct {
	id, token, phone string
	totp             string // pro accounts
	refreshToken     string
	key              *ecdsa.PrivateKey
}

// signIn creates or signs into an account: OTP from the SMS fake and a fresh device key that
// signs the challenge, exactly as the app does. The refresh token and key are kept for refresh.
func (j *journey) signIn(phone string, appKind ip.App, intent string) person {
	j.t.Helper()
	ctx := context.Background()
	ch, err := j.c.Identity.RequestOTP(ctx, identity.OTPRequest{Phone: phone, App: appKind, IPHash: "journey"})
	if err != nil {
		j.t.Fatal(err)
	}
	sent := j.c.SMS.(*smsfake.Sender).Sent(phone)
	code := sent[len(sent)-1].Vars[0].Value
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	digest := sha256.Sum256(identity.SignInMessage(ch.ID))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	tok, err := j.c.Identity.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch.ID, Phone: phone, Code: code, App: appKind, ProIntent: intent,
		Device: identity.DeviceInfo{PublicKey: der, Platform: "android", Proof: sig}})
	if err != nil {
		j.t.Fatal(err)
	}
	return person{id: tok.UserID, token: tok.AccessToken, phone: phone, refreshToken: tok.RefreshToken, key: k}
}

// proBack signs a professional in again after the 30-minute idle timeout ended their session
// (access-control-design §6) and completes MFA with their authenticator, as the pro app does.
func (j *journey) proBack(p *person) {
	j.t.Helper()
	again := j.signIn(p.phone, ip.AppPro, "doctor")
	code, err := totp.GenerateCode(p.totp, j.clock())
	if err != nil {
		j.t.Fatal(err)
	}
	j.must(j.call(http.MethodPost, "/v1/auth/mfa/verify", again.token, map[string]any{"code": code}), http.StatusNoContent, "MFA after sign-in")
	p.token, p.refreshToken, p.key = again.token, again.refreshToken, again.key
}

// refresh rotates a person's tokens with a device-signed proof, as the app does when its
// access token expires (used after the journey moves the clock).
func (j *journey) refresh(p *person) {
	j.t.Helper()
	ts := j.clock().Unix()
	digest := sha256.Sum256(identity.RefreshMessage(p.refreshToken, ts))
	sig, _ := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	tok, err := j.c.Identity.Refresh(context.Background(), identity.RefreshInput{RefreshToken: p.refreshToken, Timestamp: ts, Signature: sig})
	if err != nil {
		j.t.Fatalf("refresh: %v", err)
	}
	p.token, p.refreshToken = tok.AccessToken, tok.RefreshToken
}

// pro signs a professional in and completes TOTP enrolment, so MFA is satisfied.
func (j *journey) pro(phone string) person {
	j.t.Helper()
	p := j.signIn(phone, ip.AppPro, "doctor")
	ctx := context.Background()
	pr, err := j.c.Identity.Authenticate(ctx, p.token)
	if err != nil {
		j.t.Fatal(err)
	}
	en, err := j.c.Identity.EnrolTOTP(ctx, pr)
	if err != nil {
		j.t.Fatal(err)
	}
	code, _ := totp.GenerateCode(en.Secret, j.clock())
	if err := j.c.Identity.ConfirmTOTP(ctx, pr, code); err != nil {
		j.t.Fatal(err)
	}
	p.totp = en.Secret
	return p
}

func (j *journey) consent(p person, purpose string, version int) {
	j.t.Helper()
	legal := j.c.Config.Pack().Legal()
	notice := legal.NoticeVersions["patient"]
	if purpose[0] == 'D' {
		notice = legal.NoticeVersions["provider"]
	}
	j.must(j.call(http.MethodPost, "/v1/me/consents", p.token, map[string]any{"purposeId": purpose, "textVersion": version, "noticeVersion": notice}),
		http.StatusNoContent, "consent "+purpose)
}

func jpegBytes() string {
	var b bytes.Buffer // a real, decodable image: uploads are re-encoded (TM-42)
	_ = jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 8)), nil)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// verifyDoctor takes a doctor from sign-up to Verified: D-VERIFY consent, draft, the two
// required files and submission through HTTP; the register check cannot confirm a synthetic
// doctor, so two different reviewers decide (four-eyes override), as in production.
func (j *journey) verifyDoctor(doc person, name, number string) {
	j.t.Helper()
	ctx := context.Background()
	pack := j.c.Config.Pack()
	j.consent(doc, "D-VERIFY", pack.Legal().ConsentTextVersions["D-VERIFY"])
	j.must(j.call(http.MethodPut, "/v1/me/verification/draft", doc.token, map[string]any{"councilCode": "Synthetic Medical Council",
		"registrationNumber": number, "registrationYear": 2012, "registeredName": name, "qualifications": []string{"MBBS"}, "idType": pack.People().AcceptedPhotoIDTypes[0]}),
		http.StatusOK, "verification draft")
	for _, kind := range []string{verification.EvidenceRegistrationCertificate, verification.EvidencePhotoID} {
		j.must(j.call(http.MethodPost, "/v1/me/verification/evidence", doc.token, map[string]any{"kind": kind, "contentBase64": jpegBytes(), "contentType": "image/jpeg"}),
			http.StatusCreated, "evidence "+kind)
	}
	j.must(j.call(http.MethodPost, "/v1/me/verification/submit", doc.token, nil), http.StatusOK, "submit verification")
	if _, err := j.c.Verification.ProcessPending(ctx, 50); err != nil {
		j.t.Fatal(err)
	}
	r := j.must(j.call(http.MethodGet, "/v1/me/verification", doc.token, nil), http.StatusOK, "verification status")
	caseID := r.str("id")
	if caseID == "" {
		j.t.Fatalf("no case ID: %s", r.raw)
	}
	for i, reviewer := range []string{ids.New(), ids.New()} {
		if _, err := j.c.Verification.RecordDecision(ctx, verification.DecisionInput{CaseID: caseID, ReviewerRef: reviewer, Outcome: verification.OutcomeVerified,
			ReasonCode: "register_confirmed_manually"}); err != nil {
			j.t.Fatalf("reviewer %d: %v", i+1, err)
		}
	}
	if ok, err := j.c.Verification.IsVerified(ctx, doc.id); err != nil || !ok {
		j.t.Fatalf("doctor not verified after two reviewers: %v", err)
	}
	j.pump()
}
