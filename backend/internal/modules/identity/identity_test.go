package identity_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

// Synthetic test country code: a test pack value, never a real market (CLAUDE.md §5.4).
const cc = "+999"

func phone(n string) string { return cc + "55500" + n }

type fx struct {
	m   *identity.Module
	sms *smsfake.Sender
	env *testkit.Env
	now time.Time
}

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), identity.Source()}})
	ks := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()}, NoModuleLogins: true,
		ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, _ := keys.NewLocal("test", rnd(32))
	cr, err := crypto.NewService(ks.Pools.For("keystore"), kp, blobstore.LocalDir{Root: t.TempDir()}, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	regBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "country-packs", "IN", "messaging", "sms-templates.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := sms.ParseRegistry(regBytes)
	if err != nil {
		t.Fatal(err)
	}
	fake := &smsfake.Sender{Registry: reg}
	cfg := identity.DefaultConfig()
	cfg.PhoneCountryCodes = []string{cc}
	cfg.PhonePattern = `^\+999[0-9]{7,10}$`
	m, err := identity.New(env.Pools.For("identity"), cfg, cr, fake, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{m: m, sms: fake, env: env, now: time.Now().UTC()}
	m.SetClock(func() time.Time { return f.now })
	return f
}

type device struct {
	key *ecdsa.PrivateKey
	der []byte
}

func newDevice(t *testing.T) device {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return device{key: k, der: der}
}

func (f *fx) lastCode(t *testing.T, ph string) string {
	t.Helper()
	sent := f.sms.Sent(ph)
	if len(sent) == 0 {
		t.Fatal("no SMS sent")
	}
	for i := len(sent) - 1; i >= 0; i-- {
		if sent[i].Template == "SMS-01" {
			return sent[i].Vars[0].Value
		}
	}
	t.Fatal("no OTP SMS")
	return ""
}

func (f *fx) signIn(t *testing.T, ph string, app ip.App, d device, intent string) *identity.Tokens {
	t.Helper()
	ctx := context.Background()
	ch, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: app, IPHash: "ip-" + ph})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.m.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch.ID, Phone: ph, Code: f.lastCode(t, ph), App: app, ProIntent: intent,
		Device: identity.DeviceInfo{PublicKey: d.der, Platform: "android", Proof: proof(t, d, ch.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func code(p error) string {
	var pr *problem.Problem
	if errors.As(p, &pr) {
		return pr.Code
	}
	return ""
}

func TestSignUpThenSignIn(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := newDevice(t)
	tok := f.signIn(t, phone("01"), ip.AppPatient, d, "")
	if !tok.NewAccount || tok.Restricted {
		t.Fatalf("new account on first device must be active: %+v", tok)
	}
	p, err := f.m.Authenticate(ctx, tok.AccessToken)
	if err != nil || !p.HasRole(ip.RolePatient) || p.Restricted {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	// The phone is stored encrypted only (K-DP-03): no plaintext digits in the row.
	var ct []byte
	if err := f.env.Owner.QueryRow(ctx, `SELECT phone_ct FROM identity.user_account WHERE id=$1`, tok.UserID).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if contains(ct, []byte(phone("01"))) || contains(ct, []byte("5550001")) {
		t.Fatal("phone number stored in plaintext")
	}
	// Second sign-in on the same device: same account, not new.
	tok2 := f.signIn(t, phone("01"), ip.AppPatient, d, "")
	if tok2.NewAccount || tok2.UserID != tok.UserID || tok2.Restricted {
		t.Fatalf("second sign-in: %+v", tok2)
	}
	// SMS-01 is the only template used for the code; the code is never in the audit log.
	var n int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE resource_ref LIKE '%'||$1||'%' OR request_id LIKE '%'||$1||'%'`, f.lastCode(t, phone("01"))).Scan(&n)
	if n != 0 {
		t.Fatal("OTP value found in audit events")
	}
}

func contains(h, n []byte) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == string(n) {
			return true
		}
	}
	return false
}

// TC-55 (E-SEC-01): wrong codes lock with backoff; OTP floods are throttled per number
// and per IP; numbers outside the pack's country codes are refused; the daily SMS cap
// trips the kill switch; codes expire.
func TestTC55OTPAbuseControls(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := newDevice(t)

	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: "+15550001234", App: ip.AppPatient}); code(err) != "identity.phone_not_supported" {
		t.Fatalf("foreign number: %v", err)
	}
	ph := phone("02")
	ch, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-a"})
	if err != nil {
		t.Fatal(err)
	}
	good := f.lastCode(t, ph)
	wrong := "000000"
	if wrong == good {
		wrong = "111111"
	}
	in := identity.VerifyInput{ChallengeID: ch.ID, Phone: ph, Code: wrong, App: ip.AppPatient, IPHash: "ip-a",
		Device: identity.DeviceInfo{PublicKey: d.der, Platform: "ios", Proof: proof(t, d, ch.ID)}}
	for i := 0; i < 5; i++ {
		if _, err := f.m.VerifyOTP(ctx, in); err == nil {
			t.Fatal("wrong code accepted")
		} else if code(err) != "identity.otp_invalid" && code(err) != "identity.locked" {
			t.Fatalf("wrong code: unexpected error %v", err)
		}
	}
	in.Code = good
	if _, err := f.m.VerifyOTP(ctx, in); err == nil {
		t.Fatal("after 5 wrong attempts even the right code must fail")
	}
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-a"}); code(err) != "identity.locked" {
		t.Fatalf("number must be locked: %v", err)
	}
	f.now = f.now.Add(16 * time.Minute) // first lock is 15 minutes
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-a"}); err != nil {
		t.Fatalf("lock must lift after backoff: %v", err)
	}

	// Per-number limit: 3 codes per 10 minutes.
	ph3 := phone("03")
	for i := 0; i < 3; i++ {
		if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph3, App: ip.AppPatient, IPHash: "ip-b"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph3, App: ip.AppPatient, IPHash: "ip-b"}); code(err) != "identity.otp_rate_limited" {
		t.Fatalf("4th code in 10 min: %v", err)
	}
	// Per-IP limit: 10 per 10 minutes across numbers (bot spraying many numbers).
	limited := false
	for i := 10; i < 40; i++ {
		_, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: cc + "7770" + twoDigits(i), App: ip.AppPatient, IPHash: "ip-bot"})
		if code(err) == "identity.otp_rate_limited" {
			limited = true
			if i-10 != 10 {
				t.Fatalf("IP limit fired after %d requests, want 10", i-10)
			}
			break
		}
	}
	if !limited {
		t.Fatal("IP flood not throttled")
	}
	// Expired code.
	ph4 := phone("04")
	ch4, _ := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph4, App: ip.AppPatient, IPHash: "ip-c"})
	f.now = f.now.Add(6 * time.Minute)
	if _, err := f.m.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch4.ID, Phone: ph4, Code: f.lastCode(t, ph4), App: ip.AppPatient,
		Device: identity.DeviceInfo{PublicKey: d.der, Platform: "ios", Proof: proof(t, d, ch4.ID)}}); err == nil {
		t.Fatal("expired code accepted")
	}
	// A code is bound to its number: using it with another number fails.
	ph5, ph6 := phone("05"), phone("06")
	ch5, _ := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph5, App: ip.AppPatient, IPHash: "ip-d"})
	if _, err := f.m.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch5.ID, Phone: ph6, Code: f.lastCode(t, ph5), App: ip.AppPatient,
		Device: identity.DeviceInfo{PublicKey: d.der, Platform: "ios", Proof: proof(t, d, ch5.ID)}}); err == nil {
		t.Fatal("code accepted for a different number")
	}
}

func twoDigits(i int) string { return string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + "123" }

func TestSMSCapTripsKillSwitch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alerts := 0
	m2 := f.m
	m2.Alert = func(context.Context, string) { alerts++ }
	// The default cap is large; simulate reaching it by filling the counter.
	day := f.now.Truncate(24 * time.Hour)
	if _, err := f.env.Owner.Exec(ctx, `INSERT INTO identity.otp_counter (scope, key, window_start, count) VALUES ('global_day', 'all', $1, 20000)`, day); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.RequestOTP(ctx, identity.OTPRequest{Phone: phone("07"), App: ip.AppPatient, IPHash: "ip-e"}); code(err) != "identity.otp_unavailable" {
		t.Fatalf("cap reached: %v", err)
	}
	if alerts != 1 {
		t.Fatal("cap must alert")
	}
	if f.sms.Count() != 0 {
		t.Fatal("no SMS may be sent after the cap")
	}
}

// TC-16 (E-DP-22): a recycled number signs in on a new device: the session is restricted
// (no health data) until an already verified device approves it; the new owner, having
// no such device, never gets past re-verification.
func TestTC16RecycledNumberNewDevice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := newDevice(t)
	first := f.signIn(t, phone("08"), ip.AppPatient, owner, "")

	stranger := newDevice(t)
	tok := f.signIn(t, phone("08"), ip.AppPatient, stranger, "")
	if !tok.Restricted || tok.UserID != first.UserID {
		t.Fatalf("new device must be restricted: %+v", tok)
	}
	p, err := f.m.Authenticate(ctx, tok.AccessToken)
	if err != nil || !p.Restricted {
		t.Fatalf("restricted principal: %+v %v", p, err)
	}
	// Policy: any operation not marked x-restricted-ok is refused.
	err = ip.Check(p, ip.Policy{Roles: []string{ip.RolePatient}}, "", "", f.now)
	if code(err) != "auth.reverification_required" {
		t.Fatalf("restricted session reached a normal operation: %v", err)
	}
	// SMS-14 security notice went to the number.
	got14 := false
	for _, m := range f.sms.Sent(phone("08")) {
		got14 = got14 || m.Template == "SMS-14"
	}
	if !got14 {
		t.Fatal("new-device notice (SMS-14) not sent")
	}
	// The restricted device cannot approve itself.
	if err := f.m.ApproveDevice(ctx, p, tok.DeviceID); err == nil {
		t.Fatal("a device must not approve itself")
	}
	// The original owner approves from the verified device.
	op, _ := f.m.Authenticate(ctx, first.AccessToken)
	if err := f.m.ApproveDevice(ctx, op, tok.DeviceID); err != nil {
		t.Fatal(err)
	}
	p, _ = f.m.Authenticate(ctx, tok.AccessToken)
	if p.Restricted {
		t.Fatal("approved device must be unrestricted")
	}
}

// TC-07 (E-DP-07): sign out a lost device from another device; its tokens stop working
// on the next request.
func TestTC07RemoteSignOut(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, b := newDevice(t), newDevice(t)
	ta := f.signIn(t, phone("09"), ip.AppPatient, a, "")
	tb := f.signIn(t, phone("09"), ip.AppPatient, b, "")
	pa, _ := f.m.Authenticate(ctx, ta.AccessToken)
	if err := f.m.ApproveDevice(ctx, pa, tb.DeviceID); err != nil {
		t.Fatal(err)
	}
	pb, _ := f.m.Authenticate(ctx, tb.AccessToken)
	if err := f.m.SetPushToken(ctx, pb, "lost-phone-token-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.m.RevokedPushTarget(ctx, pa.UserID, tb.DeviceID); ok {
		t.Fatal("an active device is not a revoked target")
	}
	if err := f.m.RevokeDevice(ctx, pa, tb.DeviceID); err != nil {
		t.Fatal(err)
	}
	// S-165: the revocation is announced so the lost phone can be told to wipe itself (and its reminder backups).
	var payload string
	if err := f.env.Owner.QueryRow(ctx, `SELECT payload::text FROM identity.event_outbox WHERE type='device.revoked' AND subject_ref=$1`, pa.UserID).Scan(&payload); err != nil ||
		!strings.Contains(payload, tb.DeviceID) {
		t.Fatalf("device.revoked event: %q %v", payload, err)
	}
	if tg, ok, err := f.m.RevokedPushTarget(ctx, pa.UserID, tb.DeviceID); err != nil || !ok || tg.Token != "lost-phone-token-0123456789ab" {
		t.Fatalf("revoked target: %+v %v %v", tg, ok, err)
	}
	if _, ok, _ := f.m.RevokedPushTarget(ctx, ids.New(), tb.DeviceID); ok {
		t.Fatal("someone else's device")
	}
	// The token is purged once the 24-hour wipe window is over, even if the wipe push never ran.
	if _, err := f.env.Owner.Exec(ctx, `UPDATE identity.device SET revoked_at = revoked_at - interval '25 hours' WHERE id=$1`, tb.DeviceID); err != nil {
		t.Fatal(err)
	}
	if r, err := f.m.PurgeExpired(ctx, time.Now()); err != nil || r.RevokedTokens != 1 {
		t.Fatalf("purge: %+v %v", r, err)
	}
	var left int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM identity.device WHERE id=$1 AND push_token_ct IS NOT NULL`, tb.DeviceID).Scan(&left)
	if left != 0 {
		t.Fatal("no token after the wipe window")
	}
	if _, err := f.m.Authenticate(ctx, tb.AccessToken); err == nil {
		t.Fatal("revoked device's access token still works")
	}
	ts := f.now.Unix()
	digest := sha256.Sum256(identity.RefreshMessage(tb.RefreshToken, ts))
	sig, _ := ecdsa.SignASN1(rand.Reader, b.key, digest[:])
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tb.RefreshToken, Timestamp: ts, Signature: sig}); err == nil {
		t.Fatal("revoked device's refresh token still works")
	}
	// Another user's device ID answers not found (K-SEC-04).
	other := f.signIn(t, phone("10"), ip.AppPatient, newDevice(t), "")
	if err := f.m.RevokeDevice(ctx, pa, other.DeviceID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("revoking someone else's device: %v", err)
	}
}

func TestRefreshNeedsDeviceProofAndDetectsReuse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := newDevice(t)
	tok := f.signIn(t, phone("11"), ip.AppPatient, d, "")
	sign := func(k *ecdsa.PrivateKey, rt string, ts int64) []byte {
		digest := sha256.Sum256(identity.RefreshMessage(rt, ts))
		s, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		return s
	}
	ts := f.now.Unix()
	// Wrong device key: refused.
	thief := newDevice(t)
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tok.RefreshToken, Timestamp: ts, Signature: sign(thief.key, tok.RefreshToken, ts)}); err == nil {
		t.Fatal("refresh signed by another key accepted")
	}
	// Stale timestamp: refused.
	old := f.now.Add(-10 * time.Minute).Unix()
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tok.RefreshToken, Timestamp: old, Signature: sign(d.key, tok.RefreshToken, old)}); err == nil {
		t.Fatal("stale proof accepted")
	}
	n1, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tok.RefreshToken, Timestamp: ts, Signature: sign(d.key, tok.RefreshToken, ts)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Authenticate(ctx, tok.AccessToken); err == nil {
		t.Fatal("old access token must stop working after rotation")
	}
	// Replaying the old refresh token = theft: the whole session is revoked.
	f.now = f.now.Add(time.Second)
	ts2 := f.now.Unix()
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tok.RefreshToken, Timestamp: ts2, Signature: sign(d.key, tok.RefreshToken, ts2)}); err == nil {
		t.Fatal("reused refresh token accepted")
	}
	if _, err := f.m.Authenticate(ctx, n1.AccessToken); err == nil {
		t.Fatal("session must be revoked after refresh-token reuse")
	}
}

// TC-56 (E-SEC-02), identity part: a doctor on a new device must complete MFA with an
// authenticator; codes are single-use; repeated failures end the session; step-up is
// separate and short-lived.
func TestTC56DoctorMFA(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok := f.signIn(t, phone("12"), ip.AppPro, newDevice(t), "doctor")
	if !tok.MFARequired {
		t.Fatal("pro session must require MFA")
	}
	p, _ := f.m.Authenticate(ctx, tok.AccessToken)
	if !p.HasRole(ip.RoleDoctor) {
		t.Fatal("doctor role expected")
	}
	if err := ip.Check(p, ip.Policy{Roles: []string{ip.RoleDoctor}}, "", "", f.now); code(err) != "auth.mfa_required" {
		t.Fatalf("doctor without MFA reached an operation: %v", err)
	}
	if err := ip.Check(p, ip.Policy{Roles: []string{ip.RoleDoctor}, MFAExempt: true}, "", "", f.now); err != nil {
		t.Fatalf("MFA enrolment endpoints must be reachable: %v", err)
	}
	en, err := f.m.EnrolTOTP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := totp.GenerateCode(en.Secret, f.now)
	if err := f.m.ConfirmTOTP(ctx, p, c); err != nil {
		t.Fatal(err)
	}
	p, _ = f.m.Authenticate(ctx, tok.AccessToken)
	if err := ip.Check(p, ip.Policy{Roles: []string{ip.RoleDoctor}}, "", "", f.now); err != nil {
		t.Fatalf("after MFA: %v", err)
	}
	// Signing needs a step-up: refused until done, and only for 5 minutes.
	sign := ip.Policy{Roles: []string{ip.RoleDoctor}, StepUp: true}
	if err := ip.Check(p, sign, "", "", f.now); code(err) != "auth.step_up_required" {
		t.Fatalf("sign without step-up: %v", err)
	}
	// The same code cannot be replayed.
	if err := f.m.StepUp(ctx, p, c); err == nil {
		t.Fatal("TOTP code replay accepted")
	}
	f.now = f.now.Add(30 * time.Second)
	c2, _ := totp.GenerateCode(en.Secret, f.now)
	if err := f.m.StepUp(ctx, p, c2); err != nil {
		t.Fatal(err)
	}
	p, _ = f.m.Authenticate(ctx, tok.AccessToken)
	if err := ip.Check(p, sign, "", "", f.now); err != nil {
		t.Fatalf("after step-up: %v", err)
	}
	if err := ip.Check(p, sign, "", "", f.now.Add(6*time.Minute)); code(err) != "auth.step_up_required" {
		t.Fatal("step-up must expire after 5 minutes")
	}
	// A new device: a fresh session needs MFA again.
	tok2 := f.signIn(t, phone("12"), ip.AppPro, newDevice(t), "doctor")
	p2, _ := f.m.Authenticate(ctx, tok2.AccessToken)
	if err := ip.Check(p2, ip.Policy{Roles: []string{ip.RoleDoctor}}, "", "", f.now); code(err) != "auth.mfa_required" {
		t.Fatalf("new device must need MFA: %v", err)
	}
	// Five wrong codes end the session.
	for i := 0; i < 5; i++ {
		_ = f.m.VerifyMFA(ctx, p2, "000000")
	}
	if _, err := f.m.Authenticate(ctx, tok2.AccessToken); err == nil {
		t.Fatal("session must end after 5 wrong MFA codes")
	}
	// Patient-app sessions cannot use professional roles.
	pp, _ := f.m.Authenticate(ctx, f.signIn(t, phone("12"), ip.AppPatient, newDevice(t), "").AccessToken)
	if err := ip.Check(pp, ip.Policy{Roles: []string{ip.RoleDoctor}}, "", "", f.now); err == nil {
		t.Fatal("doctor role used from the patient app")
	}
}

func TestProIdleTimeout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok := f.signIn(t, phone("13"), ip.AppPro, newDevice(t), "doctor")
	f.now = f.now.Add(31 * time.Minute)
	if _, err := f.m.Authenticate(ctx, tok.AccessToken); err == nil {
		t.Fatal("pro session must end after 30 minutes idle")
	}
}

// TM-04 (S-178): the idle timeout really ends a pro session. The access token expires
// after 15 minutes, so the control that matters is at refresh: a refresh after 30 minutes
// idle is refused, one inside the window works. A patient session is not subject to it.
func TestTM04ProIdleTimeoutAtRefresh(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sign := func(k *ecdsa.PrivateKey, rt string, ts int64) []byte {
		digest := sha256.Sum256(identity.RefreshMessage(rt, ts))
		s, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
		return s
	}
	d := newDevice(t)
	tok := f.signIn(t, phone("15"), ip.AppPro, d, "doctor")
	f.now = f.now.Add(20 * time.Minute) // access token expired, still inside the idle window
	if _, err := f.m.Authenticate(ctx, tok.AccessToken); err == nil {
		t.Fatal("access token must expire after 15 minutes")
	}
	n, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: tok.RefreshToken, Timestamp: f.now.Unix(), Signature: sign(d.key, tok.RefreshToken, f.now.Unix())})
	if err != nil {
		t.Fatalf("refresh inside the idle window: %v", err)
	}
	f.now = f.now.Add(31 * time.Minute)
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: n.RefreshToken, Timestamp: f.now.Unix(), Signature: sign(d.key, n.RefreshToken, f.now.Unix())}); err == nil {
		t.Fatal("a pro refresh after 30 minutes idle must be refused")
	}
	// A patient keeps a long-lived session (device-bound refresh, 30 days).
	pd := newDevice(t)
	pt := f.signIn(t, phone("16"), ip.AppPatient, pd, "")
	f.now = f.now.Add(3 * time.Hour)
	if _, err := f.m.Refresh(ctx, identity.RefreshInput{RefreshToken: pt.RefreshToken, Timestamp: f.now.Unix(), Signature: sign(pd.key, pt.RefreshToken, f.now.Unix())}); err != nil {
		t.Fatalf("patient refresh after 3 hours: %v", err)
	}
}

// TM-04 (S-178): a professional with an authenticator signing in on a new device is bound
// at once (the authenticator is the re-verification), but the owner is still told by
// SMS-14, so a SIM swap or stolen credentials on a doctor's number do not go unnoticed.
func TestTM04ProNewDeviceIsNotified(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tok := f.signIn(t, phone("17"), ip.AppPro, newDevice(t), "doctor")
	p, _ := f.m.Authenticate(ctx, tok.AccessToken)
	en, err := f.m.EnrolTOTP(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := totp.GenerateCode(en.Secret, f.now)
	if err := f.m.ConfirmTOTP(ctx, p, c); err != nil {
		t.Fatal(err)
	}
	count14 := func() int {
		n := 0
		for _, m := range f.sms.Sent(phone("17")) {
			if m.Template == "SMS-14" {
				n++
			}
		}
		return n
	}
	if count14() != 0 {
		t.Fatal("the first device of a new account sends no notice")
	}
	f.now = f.now.Add(time.Minute)
	second := f.signIn(t, phone("17"), ip.AppPro, newDevice(t), "doctor")
	if second.Restricted || !second.MFARequired {
		t.Fatalf("an authenticator holder's new device is bound but still needs MFA: %+v", second)
	}
	if count14() != 1 {
		t.Fatal("the new-device notice (SMS-14) must reach the number on file")
	}
}

func TestSMSRegistryRefusesUnregisteredAndBadVariables(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, m := range []sms.Message{
		{To: phone("14"), Template: "SMS-99", Vars: nil},
		{To: phone("14"), Template: "SMS-01", Vars: []sms.Var{{Name: "otp", Value: "12ab"}}},
		{To: phone("14"), Template: "SMS-01", Vars: []sms.Var{{Name: "otp", Value: "123456"}, {Name: "x", Value: "1"}}},
		{To: phone("14"), Template: "SMS-14", Vars: []sms.Var{{Name: "date_time", Value: "visit https://evil.example"}}},
		{To: phone("14"), Template: "SMS-14", Vars: []sms.Var{{Name: "date_time", Value: "call 9876543210"}}},
	} {
		if _, err := f.sms.Send(ctx, m); adapters.KindOf(err) != adapters.Invalid {
			t.Errorf("%+v: want Invalid, got %v", m, err)
		}
	}
}

// Push targets exclude devices awaiting re-verification (E-DP-22): a recycled number's
// new owner must not receive the old owner's reminders. Tokens are stored encrypted.
func TestPushTargetsAndPhoneLookup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.signIn(t, phone("15"), ip.AppPatient, newDevice(t), "")
	po, _ := f.m.Authenticate(ctx, owner.AccessToken)
	if err := f.m.SetPushToken(ctx, po, "owner-token-0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	stranger := f.signIn(t, phone("15"), ip.AppPatient, newDevice(t), "")
	ps, _ := f.m.Authenticate(ctx, stranger.AccessToken)
	if err := f.m.SetPushToken(ctx, ps, "stranger-token-0123456789abcd"); err != nil {
		t.Fatal(err)
	}
	ts, err := f.m.PushTargets(ctx, owner.UserID, "")
	if err != nil || len(ts) != 1 || ts[0].Token != "owner-token-0123456789abcdef" {
		t.Fatalf("push targets: %+v %v", ts, err)
	}
	var raw []byte
	_ = f.env.Owner.QueryRow(ctx, `SELECT push_token_ct FROM identity.device WHERE id=$1`, owner.DeviceID).Scan(&raw)
	if contains(raw, []byte("owner-token")) {
		t.Fatal("push token stored in plaintext")
	}
	if ph, err := f.m.PhoneFor(ctx, owner.UserID); err != nil || ph != phone("15") {
		t.Fatalf("phone lookup: %q %v", ph, err)
	}
	if err := f.m.SetPushToken(ctx, po, "short"); err == nil {
		t.Fatal("malformed token accepted")
	}
	// D-73: the same person's pro-app device (approved from the patient device) gets only pro-app messages.
	proTok := f.signIn(t, phone("15"), ip.AppPro, newDevice(t), "doctor")
	if err := f.m.ApproveDevice(ctx, po, proTok.DeviceID); err != nil {
		t.Fatal(err)
	}
	pp, _ := f.m.Authenticate(ctx, proTok.AccessToken)
	if err := f.m.SetPushToken(ctx, pp, "pro-device-token-0123456789ab"); err != nil {
		t.Fatal(err)
	}
	for app, want := range map[string][]string{
		"patient": {"owner-token-0123456789abcdef"},
		"pro":     {"pro-device-token-0123456789ab"},
		"":        {"owner-token-0123456789abcdef", "pro-device-token-0123456789ab"},
		"nurse":   nil,
	} {
		got, err := f.m.PushTargets(ctx, owner.UserID, app)
		if err != nil || len(got) != len(want) {
			t.Fatalf("app %q: %+v %v", app, got, err)
		}
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || g.Token == w
			}
			if !found {
				t.Fatalf("app %q misses %s: %+v", app, w, got)
			}
		}
	}
}

// Recycled number, escalation attempt: the new holder signs into the PRO app on the old
// patient's account to enrol their own authenticator and then approve their pending
// patient device. Every step must fail: the pro device is pending too, no doctor role is
// added, MFA enrolment is refused for a restricted session, and approval is refused.
func TestRecycledNumberCannotEscalateViaProApp(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.signIn(t, phone("16"), ip.AppPatient, newDevice(t), "")

	strangerPatient := f.signIn(t, phone("16"), ip.AppPatient, newDevice(t), "")
	strangerPro := f.signIn(t, phone("16"), ip.AppPro, newDevice(t), "doctor")
	if !strangerPro.Restricted || strangerPro.UserID != owner.UserID {
		t.Fatalf("pro device on an existing account without an authenticator must be restricted: %+v", strangerPro)
	}
	pp, err := f.m.Authenticate(ctx, strangerPro.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if pp.HasRole(ip.RoleDoctor) {
		t.Fatal("a restricted sign-in must not add a doctor role")
	}
	enrol := ip.Policy{Roles: []string{ip.RoleDoctor, ip.RoleClinicAdmin, ip.RoleFrontDesk}, MFAExempt: true}
	if err := ip.Check(pp, enrol, "", "", f.now); err == nil {
		t.Fatal("restricted pro session reached MFA enrolment")
	}
	if _, err := f.m.EnrolTOTP(ctx, pp); err == nil {
		// Defence in depth is the policy check; the service call itself must also not help.
		approve := ip.Policy{Roles: []string{ip.RoleAuthenticated}}
		if ip.Check(pp, approve, "", "", f.now) == nil {
			t.Fatal("restricted pro session passes the approve policy")
		}
	}
	if err := f.m.ApproveDevice(ctx, pp, strangerPatient.DeviceID); err == nil {
		t.Fatal("restricted pro session approved a pending patient device")
	}
	sp, _ := f.m.Authenticate(ctx, strangerPatient.AccessToken)
	if !sp.Restricted {
		t.Fatal("stranger's patient device must still be restricted")
	}
}

func proof(t *testing.T, d device, challengeID string) []byte {
	t.Helper()
	digest := sha256.Sum256(identity.SignInMessage(challengeID))
	s, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Review fixes: the device must prove key possession (L5); a lockout from one network
// does not lock the owner out elsewhere (M3); a variant of a number is refused (L7); a
// pro-app device row never satisfies a patient sign-in for the same key (H2).
func TestReviewFixesIdentity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	d := newDevice(t)
	ph := phone("17")

	// L5: someone else's public key without its private key is refused.
	ch, _ := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-x"})
	thief := newDevice(t)
	if _, err := f.m.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch.ID, Phone: ph, Code: f.lastCode(t, ph), App: ip.AppPatient,
		Device: identity.DeviceInfo{PublicKey: d.der, Platform: "ios", Proof: proof(t, thief, ch.ID)}}); err == nil {
		t.Fatal("sign-in without proof of the device key accepted")
	}

	// M3: five wrong codes from network A lock A, not B.
	ch2, _ := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-a"})
	for i := 0; i < 5; i++ {
		_, _ = f.m.VerifyOTP(ctx, identity.VerifyInput{ChallengeID: ch2.ID, Phone: ph, Code: "000000", App: ip.AppPatient, IPHash: "ip-a",
			Device: identity.DeviceInfo{PublicKey: d.der, Platform: "ios", Proof: proof(t, d, ch2.ID)}})
	}
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-a"}); code(err) != "identity.locked" {
		t.Fatalf("network A must be locked: %v", err)
	}
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: ph, App: ip.AppPatient, IPHash: "ip-b"}); err != nil {
		t.Fatalf("the owner on network B must not be locked out: %v", err)
	}

	// L7: a variant with extra digits is not the same number and is refused.
	if _, err := f.m.RequestOTP(ctx, identity.OTPRequest{Phone: cc + "05550001712345", App: ip.AppPatient, IPHash: "ip-c"}); code(err) != "identity.phone_not_supported" {
		t.Fatalf("number variant accepted: %v", err)
	}

	// H2: an account with a confirmed authenticator; the stranger's pro device row (active
	// because of the factor) must not make the same key active for the patient app.
	doc := phone("18")
	tok := f.signIn(t, doc, ip.AppPro, newDevice(t), "doctor")
	p, _ := f.m.Authenticate(ctx, tok.AccessToken)
	en, _ := f.m.EnrolTOTP(ctx, p)
	c, _ := totp.GenerateCode(en.Secret, f.now)
	if err := f.m.ConfirmTOTP(ctx, p, c); err != nil {
		t.Fatal(err)
	}
	_ = f.signIn(t, doc, ip.AppPatient, newDevice(t), "") // the owner's patient app (pending; fine)
	f.now = f.now.Add(11 * time.Minute)                   // stay within the per-number request limit (3 per 10 minutes)
	k := newDevice(t)
	_ = f.signIn(t, doc, ip.AppPro, k, "doctor") // stranger, pro app, cannot pass MFA
	pt := f.signIn(t, doc, ip.AppPatient, k, "")
	if !pt.Restricted {
		t.Fatal("the same key used in the patient app after a pro sign-in must still be restricted")
	}
}
