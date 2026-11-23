package crypto_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

func rnd(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

type fixture struct {
	env  *testkit.Env
	kp   *keys.Local
	blob blobstore.LocalDir
	svc  *crypto.Service
}

func setup(t *testing.T) *fixture {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Key: "keystore", Sources: []migrate.Source{crypto.KeystoreSource()},
		NoModuleLogins: true, ExtraLogins: map[string]string{"keystore": "mugizh_keystore"}})
	kp, err := keys.NewLocal("test", rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	blob := blobstore.LocalDir{Root: t.TempDir()}
	svc, err := crypto.NewService(env.Pools.For("keystore"), kp, blob, rnd(32))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{env: env, kp: kp, blob: blob, svc: svc}
}

func TestLocalProviderRefusedOutsideDev(t *testing.T) {
	for _, env := range []string{"staging", "production", "prod", ""} {
		if _, err := keys.NewLocal(env, rnd(32)); !errors.Is(err, keys.ErrRefused) {
			t.Errorf("env %q: local provider must be refused, got %v", env, err)
		}
	}
}

func TestSealOpenRoundTripAndBinding(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doctor := ids.New(), ids.New()
	aad := crypto.AAD{Table: "records.document", Column: "title", RowID: ids.New()}
	pt := []byte("synthetic: blood test report")

	s, err := crypto.SealPatient(ctx, f.svc, patient, aad, pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Bytes(), pt) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := crypto.Open(ctx, f.svc, aad, s)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	// Same plaintext twice gives different ciphertexts (random nonce).
	s2, _ := crypto.SealPatient(ctx, f.svc, patient, aad, pt)
	if bytes.Equal(s.Bytes(), s2.Bytes()) {
		t.Fatal("nonce reuse: identical ciphertexts")
	}
	// Moving the ciphertext to another row or column fails (row-bound AAD).
	for _, other := range []crypto.AAD{{aad.Table, aad.Column, ids.New()}, {aad.Table, "body", aad.RowID}, {"x.y", aad.Column, aad.RowID}} {
		if _, err := crypto.Open(ctx, f.svc, other, s); !errors.Is(err, crypto.ErrDecrypt) {
			t.Errorf("AAD %+v: want ErrDecrypt, got %v", other, err)
		}
	}
	// Tampering with any byte fails.
	for _, i := range []int{0, 1, 5, 19, 25, len(s.Bytes()) - 1} {
		b := append([]byte(nil), s.Bytes()...)
		b[i] ^= 0x01
		var tampered crypto.Sealed[crypto.PatientKey]
		_ = tampered.Scan(b)
		if _, err := crypto.Open(ctx, f.svc, aad, tampered); err == nil {
			t.Errorf("byte %d tampered but decryption succeeded", i)
		}
	}
	// A care ciphertext cannot be opened as a patient ciphertext (header kind check).
	c, err := crypto.SealCare(ctx, f.svc, patient, doctor, aad, pt)
	if err != nil {
		t.Fatal(err)
	}
	var wrong crypto.Sealed[crypto.PatientKey]
	_ = wrong.Scan(c.Bytes())
	if _, err := crypto.Open(ctx, f.svc, aad, wrong); !errors.Is(err, crypto.ErrWrongKind) {
		t.Fatalf("want ErrWrongKind, got %v", err)
	}
}

// TC-06 (E-DP-06): key service unavailable → reads and writes fail with a clear error;
// no plaintext fallback, nothing written unencrypted.
func TestTC06KeyServiceUnavailableFailsClosed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	aad := crypto.AAD{Table: "t", Column: "c", RowID: "1"}
	f.kp.SetUnavailable(true)
	s, err := crypto.SealPatient(ctx, f.svc, ids.New(), aad, []byte("x"))
	if !errors.Is(err, crypto.ErrKeyUnavailable) || !s.IsZero() {
		t.Fatalf("seal with KMS down: want ErrKeyUnavailable and no output, got %v len=%d", err, len(s.Bytes()))
	}
	f.kp.SetUnavailable(false)
	p := ids.New()
	s, err = crypto.SealPatient(ctx, f.svc, p, aad, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	// New service instance (empty cache) with KMS down cannot open.
	svc2, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	f.kp.SetUnavailable(true)
	if _, err := crypto.Open(ctx, svc2, aad, s); !errors.Is(err, crypto.ErrKeyUnavailable) {
		t.Fatalf("open with KMS down: want ErrKeyUnavailable, got %v", err)
	}
	// Key store database down also fails closed.
	f.kp.SetUnavailable(false)
	f.env.Pools.For("keystore").Close()
	svc3, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	if _, err := crypto.Open(ctx, svc3, aad, s); !errors.Is(err, crypto.ErrKeyUnavailable) {
		t.Fatalf("open with key store down: want ErrKeyUnavailable, got %v", err)
	}
}

// TC-05 (E-DP-05): rotate a data key while reads and writes run; no errors; old data
// readable; re-encryption moves rows to the new version; then the old version retires.
func TestTC05RotationDuringReadsAndWrites(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient := ids.New()
	type rec struct {
		aad crypto.AAD
		pt  []byte
		s   crypto.Sealed[crypto.PatientKey]
	}
	var mu sync.Mutex
	var recs []rec
	write := func() error {
		r := rec{aad: crypto.AAD{Table: "identity.user", Column: "phone", RowID: ids.New()}, pt: rnd(24)}
		s, err := crypto.SealPatient(ctx, f.svc, patient, r.aad, r.pt)
		if err != nil {
			return err
		}
		r.s = s
		mu.Lock()
		recs = append(recs, r)
		mu.Unlock()
		return nil
	}
	for i := 0; i < 20; i++ {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if err := write(); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				r := recs[i%len(recs)]
				mu.Unlock()
				got, err := crypto.Open(ctx, f.svc, r.aad, r.s)
				if err != nil || !bytes.Equal(got, r.pt) {
					errs <- errors.New("read failed during rotation")
					return
				}
			}
		}()
	}
	if _, err := f.svc.Rotate(ctx, crypto.KindPatient, patient); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Background re-encryption.
	for i := range recs {
		ns, _, err := crypto.Reseal(ctx, f.svc, patient, recs[i].aad, recs[i].s)
		if err != nil {
			t.Fatal(err)
		}
		recs[i].s = ns
	}
	if err := f.svc.RetireVersion(ctx, crypto.KindPatient, patient, 1, "rotation.retired"); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if got, err := crypto.Open(ctx, f.svc, r.aad, r.s); err != nil || !bytes.Equal(got, r.pt) {
			t.Fatalf("after retire: %v", err)
		}
	}
	if err := f.svc.RetireVersion(ctx, crypto.KindPatient, patient, 2, "rotation.retired"); err == nil {
		t.Fatal("retiring the current version must be refused")
	}
}

// TC-04 (E-DP-04), library part: destroying a patient key makes its data unreadable;
// care-key records stay readable; a restore that brings the key back is undone by
// replaying tombstones from write-once storage; a destroyed key is never re-created.
func TestTC04CryptoShredAndRestoreReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patient, doctor := ids.New(), ids.New()
	aad := crypto.AAD{Table: "records.document", Column: "body", RowID: ids.New()}
	ps, _ := crypto.SealPatient(ctx, f.svc, patient, aad, []byte("upload"))
	cs, _ := crypto.SealCare(ctx, f.svc, patient, doctor, aad, []byte("note under RET-02"))

	// "Backup" of the key store taken before the erasure.
	type row struct {
		id      any
		version int32
		wrapped []byte
		root    string
	}
	var backup []row
	rows, err := f.env.Owner.Query(ctx, `SELECT key_id, version, wrapped, root_key FROM keystore.data_key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.version, &r.wrapped, &r.root); err != nil {
			t.Fatal(err)
		}
		backup = append(backup, r)
	}
	rows.Close()

	if err := f.svc.DestroyPatientKey(ctx, patient, "erasure.ret18_hold_ended"); err != nil {
		t.Fatal(err)
	}
	if _, err := crypto.Open(ctx, f.svc, aad, ps); !errors.Is(err, crypto.ErrKeyDestroyed) {
		t.Fatalf("patient data must be unreadable after destruction, got %v", err)
	}
	if got, err := crypto.Open(ctx, f.svc, aad, cs); err != nil || string(got) != "note under RET-02" {
		t.Fatalf("care-key record must stay readable: %v", err)
	}
	if _, err := crypto.SealPatient(ctx, f.svc, patient, aad, []byte("new")); !errors.Is(err, crypto.ErrKeyDestroyed) {
		t.Fatalf("a destroyed key must never be re-created, got %v", err)
	}

	// Restore the old key store backup (the attack the design defends against).
	_, _ = f.env.Owner.Exec(ctx, `UPDATE keystore.key_ref SET status='active', destroyed_at=NULL`)
	_, _ = f.env.Owner.Exec(ctx, `TRUNCATE keystore.destruction_log`)
	for _, r := range backup {
		_, _ = f.env.Owner.Exec(ctx, `INSERT INTO keystore.data_key (key_id, version, wrapped, root_key) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.id, r.version, r.wrapped, r.root)
	}
	fresh, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	if _, err := crypto.Open(ctx, fresh, aad, ps); err != nil {
		t.Fatalf("precondition: restored backup makes data readable again: %v", err)
	}
	n, err := fresh.Replay(ctx)
	if err != nil || n == 0 {
		t.Fatalf("replay: n=%d err=%v", n, err)
	}
	after, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	if _, err := crypto.Open(ctx, after, aad, ps); !errors.Is(err, crypto.ErrKeyDestroyed) {
		t.Fatalf("after replay the erased data must be unreadable again, got %v", err)
	}
	if _, err := crypto.Open(ctx, after, aad, cs); err != nil {
		t.Fatalf("care record must survive replay: %v", err)
	}
}

func TestRootRotationRewrapsKeysOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := ids.New()
	aad := crypto.AAD{Table: "t", Column: "c", RowID: "r"}
	s, _ := crypto.SealPatient(ctx, f.svc, p, aad, []byte("x"))
	if err := f.kp.AddRoot(2, rnd(32)); err != nil {
		t.Fatal(err)
	}
	n, err := f.svc.RewrapRoot(ctx)
	if err != nil || n != 1 {
		t.Fatalf("rewrap n=%d err=%v", n, err)
	}
	fresh, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	if got, err := crypto.Open(ctx, fresh, aad, s); err != nil || string(got) != "x" {
		t.Fatalf("data must stay readable after root rotation: %v", err)
	}
}

func TestBlindIndex(t *testing.T) {
	f := setup(t)
	a := f.svc.BlindIndex("identity.phone", "+10000000001")
	if !bytes.Equal(a, f.svc.BlindIndex("identity.phone", "+10000000001")) {
		t.Fatal("blind index must be deterministic")
	}
	if bytes.Equal(a, f.svc.BlindIndex("identity.email", "+10000000001")) {
		t.Fatal("purposes must not share index values")
	}
	other, _ := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(32))
	if bytes.Equal(a, other.BlindIndex("identity.phone", "+10000000001")) {
		t.Fatal("a different secret must give a different index")
	}
	if _, err := crypto.NewService(f.env.Pools.For("keystore"), f.kp, f.blob, rnd(16)); err == nil {
		t.Fatal("short blind-index key must be refused")
	}
}

// Review H1: sealing and opening while keys are evicted and destroyed concurrently must
// never use a zeroed key. Run with -race.
func TestConcurrentSealWhileEvicting(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	patients := make([]string, 8)
	for i := range patients {
		patients[i] = ids.New()
	}
	aad := crypto.AAD{Table: "t", Column: "c", RowID: "r"}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 100)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				p := patients[(w+i)%len(patients)]
				s, err := crypto.SealPatient(ctx, f.svc, p, aad, []byte("payload"))
				if errors.Is(err, crypto.ErrKeyDestroyed) {
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				got, err := crypto.Open(ctx, f.svc, aad, s)
				if errors.Is(err, crypto.ErrKeyDestroyed) {
					continue
				}
				if err != nil || string(got) != "payload" {
					errs <- errors.New("round trip failed under concurrent eviction")
					return
				}
			}
		}(w)
	}
	for i := 0; i < 4; i++ {
		_ = f.svc.DestroyPatientKey(ctx, patients[i], "test.evict")
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
