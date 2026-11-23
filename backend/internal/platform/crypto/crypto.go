// Package crypto is the one field-level encryption library for every module (S-098,
// K-DP-03, encryption-design §3-§10).
//
//   - Envelope encryption: random 256-bit data keys, wrapped by a KMS root key through
//     the keys.Provider interface, stored in a separate key store database.
//   - Key kinds: a patient key (erasable data: uploads, contact details, profile health
//     fields), a care key per patient-doctor/clinic relationship (legal-retention clinical
//     records, RET-02), service keys and verification keys.
//   - AES-256-GCM with a random 96-bit nonce. The associated data binds each ciphertext to
//     its table, column and row, and to its key header, so a ciphertext moved to another
//     row, column or key fails to decrypt.
//   - Fail closed: no plaintext fallback, ever (E-DP-06).
//   - Crypto-shredding: destroying a key deletes it from the key store and writes a
//     tombstone to write-once storage first; Replay re-applies tombstones after a restore
//     (E-DP-04).
//   - Typed ciphertexts (Sealed[PatientKey], Sealed[CareKey], ...) make it a compile error
//     to store one kind of ciphertext in another kind's field.
package crypto

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql/driver"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// KeystoreSource returns the key store migrations (run in the key store database).
func KeystoreSource() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "keystore", Schema: "keystore", FS: sub}
}

// Errors. Callers map them to 503 (unavailable) or treat the data as gone (destroyed).
var (
	ErrKeyUnavailable = errors.New("crypto: key unavailable")
	ErrKeyDestroyed   = errors.New("crypto: key destroyed")
	ErrDecrypt        = errors.New("crypto: decryption failed")
	ErrWrongKind      = errors.New("crypto: ciphertext belongs to another key kind")
)

// Kind is a data-key kind.
type Kind byte

// Key kinds (encryption-design §3).
const (
	KindPatient      Kind = 1
	KindCare         Kind = 2
	KindService      Kind = 3
	KindVerification Kind = 4
	KindPilot        Kind = 5
)

func (k Kind) String() string {
	switch k {
	case KindPatient:
		return "patient"
	case KindCare:
		return "care"
	case KindService:
		return "service"
	case KindVerification:
		return "verification"
	case KindPilot:
		return "pilot"
	}
	return "unknown"
}

// KindMarker is implemented by the phantom key types below.
type KindMarker interface{ kind() Kind }

// Phantom types naming the key a ciphertext is sealed under.
type (
	PatientKey      struct{}
	CareKey         struct{}
	ServiceKey      struct{}
	VerificationKey struct{}
)

func (PatientKey) kind() Kind      { return KindPatient }
func (CareKey) kind() Kind         { return KindCare }
func (ServiceKey) kind() Kind      { return KindService }
func (VerificationKey) kind() Kind { return KindVerification }

// Sealed is a ciphertext sealed under a key of kind K. It is stored as bytea.
type Sealed[K KindMarker] struct{ b []byte }

// Bytes returns the raw ciphertext.
func (s Sealed[K]) Bytes() []byte { return s.b }

// IsZero reports an empty (absent) value.
func (s Sealed[K]) IsZero() bool { return len(s.b) == 0 }

// Scan implements sql.Scanner (pgx supports it).
func (s *Sealed[K]) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		s.b = nil
	case []byte:
		s.b = append([]byte(nil), v...)
	default:
		return fmt.Errorf("crypto: cannot scan %T into Sealed", src)
	}
	return nil
}

// Value implements driver.Valuer.
func (s Sealed[K]) Value() (driver.Value, error) {
	if len(s.b) == 0 {
		return nil, nil
	}
	return s.b, nil
}

// AAD binds a ciphertext to where it is stored.
type AAD struct{ Table, Column, RowID string }

func (a AAD) bytes(header []byte) []byte {
	var b bytes.Buffer
	b.Write(header)
	for _, s := range []string{a.Table, a.Column, a.RowID} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], lenU32(len(s)))
		b.Write(n[:])
		b.WriteString(s)
	}
	return b.Bytes()
}

const (
	formatV1  = 0x01
	headerLen = 1 + 1 + 16 + 4 // format | kind | key_id | key_version
	nonceLen  = 12
	cacheTTL  = 5 * time.Minute
	cacheMax  = 50_000
)

var reReason = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,60}$`)

// cacheEntry holds a ready AEAD, never raw key bytes: an AEAD is immutable once built, so
// evicting an entry can never alter a key another goroutine is still using (review H1).
type cacheEntry struct {
	aead cipher.AEAD
	exp  time.Time
}

type refEntry struct {
	keyID   uuid.UUID
	version uint32
	exp     time.Time
}

// Service seals and opens fields.
type Service struct {
	store    *pgxpool.Pool
	kp       keys.Provider
	blob     blobstore.Store
	blindKey []byte
	now      func() time.Time

	mu   sync.Mutex
	deks map[string]cacheEntry
	refs map[string]refEntry
}

// NewService builds the library. store must use the key store role in the key store
// database; blob is write-once storage for destruction tombstones; blindIndexKey comes
// from the secrets manager (at least 32 bytes) and never from the database.
func NewService(store *pgxpool.Pool, kp keys.Provider, blob blobstore.Store, blindIndexKey []byte) (*Service, error) {
	if len(blindIndexKey) < 32 {
		return nil, errors.New("crypto: blind index key must be at least 32 bytes")
	}
	if store == nil || kp == nil || blob == nil {
		return nil, errors.New("crypto: store, key provider and blob store are required")
	}
	return &Service{store: store, kp: kp, blob: blob, blindKey: append([]byte(nil), blindIndexKey...),
		now: time.Now, deks: map[string]cacheEntry{}, refs: map[string]refEntry{}}, nil
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// CareOwner builds the owner reference of a care key.
func CareOwner(patientRef, careOwnerRef string) string { return patientRef + "|" + careOwnerRef }

// SealPatient encrypts with the patient's key.
func SealPatient(ctx context.Context, s *Service, patientRef string, aad AAD, plaintext []byte) (Sealed[PatientKey], error) {
	b, err := s.seal(ctx, KindPatient, patientRef, aad, plaintext)
	return Sealed[PatientKey]{b: b}, err
}

// SealCare encrypts with the care key of a patient and a treating doctor or clinic.
func SealCare(ctx context.Context, s *Service, patientRef, careOwnerRef string, aad AAD, plaintext []byte) (Sealed[CareKey], error) {
	b, err := s.seal(ctx, KindCare, CareOwner(patientRef, careOwnerRef), aad, plaintext)
	return Sealed[CareKey]{b: b}, err
}

// SealService encrypts with a service key for a record type (grievance, support).
func SealService(ctx context.Context, s *Service, recordType string, aad AAD, plaintext []byte) (Sealed[ServiceKey], error) {
	b, err := s.seal(ctx, KindService, recordType, aad, plaintext)
	return Sealed[ServiceKey]{b: b}, err
}

// SealVerification encrypts with a doctor's verification key (ID evidence).
func SealVerification(ctx context.Context, s *Service, doctorRef string, aad AAD, plaintext []byte) (Sealed[VerificationKey], error) {
	b, err := s.seal(ctx, KindVerification, doctorRef, aad, plaintext)
	return Sealed[VerificationKey]{b: b}, err
}

// Open decrypts a sealed value stored at aad.
func Open[K KindMarker](ctx context.Context, s *Service, aad AAD, v Sealed[K]) ([]byte, error) {
	var k K
	return s.open(ctx, k.kind(), aad, v.b)
}

// Reseal re-encrypts v under the current key version if it uses an older one (background
// re-encryption during data-key rotation, E-DP-05). changed is false when v is current.
func Reseal[K KindMarker](ctx context.Context, s *Service, owner string, aad AAD, v Sealed[K]) (Sealed[K], bool, error) {
	var k K
	_, _, ver, err := parseHeader(v.b)
	if err != nil {
		return v, false, err
	}
	ref, err := s.currentRef(ctx, k.kind(), owner, false)
	if err != nil {
		return v, false, err
	}
	if ver == ref.version {
		return v, false, nil
	}
	pt, err := s.open(ctx, k.kind(), aad, v.b)
	if err != nil {
		return v, false, err
	}
	b, err := s.seal(ctx, k.kind(), owner, aad, pt)
	clear(pt)
	return Sealed[K]{b: b}, err == nil, err
}

func (s *Service) seal(ctx context.Context, kind Kind, owner string, aad AAD, plaintext []byte) ([]byte, error) {
	ref, err := s.currentRef(ctx, kind, owner, true)
	if err != nil {
		return nil, err
	}
	g, err := s.aeadFor(ctx, ref.keyID, ref.version)
	if err != nil {
		return nil, err
	}
	out := make([]byte, headerLen, headerLen+nonceLen+len(plaintext)+16)
	out[0], out[1] = formatV1, byte(kind)
	copy(out[2:18], ref.keyID[:])
	binary.BigEndian.PutUint32(out[18:22], ref.version)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ad := aad.bytes(out[:headerLen])
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, ad), nil
}

func parseHeader(b []byte) (Kind, uuid.UUID, uint32, error) {
	if len(b) < headerLen+nonceLen+16 || b[0] != formatV1 {
		return 0, uuid.UUID{}, 0, ErrDecrypt
	}
	var id uuid.UUID
	copy(id[:], b[2:18])
	v := binary.BigEndian.Uint32(b[18:22])
	if v == 0 || v > math.MaxInt32 {
		return 0, uuid.UUID{}, 0, ErrDecrypt
	}
	return Kind(b[1]), id, v, nil
}

func (s *Service) open(ctx context.Context, want Kind, aad AAD, b []byte) ([]byte, error) {
	kind, id, ver, err := parseHeader(b)
	if err != nil {
		return nil, err
	}
	if kind != want {
		return nil, ErrWrongKind
	}
	g, err := s.aeadFor(ctx, id, ver)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, b[headerLen:headerLen+nonceLen], b[headerLen+nonceLen:], aad.bytes(b[:headerLen]))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func gcm(dek []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func refKey(kind Kind, owner string) string { return kind.String() + "/" + owner }

func dekAAD(id uuid.UUID, ver uint32) []byte {
	return []byte(fmt.Sprintf("mugizh-dek|v1|%s|%d", id, ver))
}

// currentRef returns the key ID and current version for (kind, owner), creating the key
// on first use when create is set. A destroyed key is never re-created: data must not be
// written again for an erased person.
func (s *Service) currentRef(ctx context.Context, kind Kind, owner string, create bool) (refEntry, error) {
	if owner == "" || len(owner) > 200 {
		return refEntry{}, errors.New("crypto: invalid key owner")
	}
	k := refKey(kind, owner)
	now := s.now()
	s.mu.Lock()
	if e, ok := s.refs[k]; ok && now.Before(e.exp) {
		s.mu.Unlock()
		return e, nil
	}
	s.mu.Unlock()

	var id uuid.UUID
	var ver int32
	var status string
	err := s.store.QueryRow(ctx, `SELECT key_id, current_version, status FROM key_ref WHERE kind=$1 AND owner_ref=$2`,
		kind.String(), owner).Scan(&id, &ver, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if !create {
			return refEntry{}, ErrKeyDestroyed
		}
		if err := s.createKey(ctx, kind, owner); err != nil {
			return refEntry{}, err
		}
		return s.currentRef(ctx, kind, owner, false)
	case err != nil:
		return refEntry{}, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	if status != "active" {
		return refEntry{}, ErrKeyDestroyed
	}
	e := refEntry{keyID: id, version: verU(ver), exp: now.Add(cacheTTL)}
	s.mu.Lock()
	if len(s.refs) >= cacheMax {
		s.refs = map[string]refEntry{}
	}
	s.refs[k] = e
	s.mu.Unlock()
	return e, nil
}

func (s *Service) createKey(ctx context.Context, kind Kind, owner string) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return err
	}
	defer clear(dek)
	wrapped, root, err := s.kp.Wrap(ctx, dek, dekAAD(id, 1))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	err = db.InTx(ctx, s.store, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO key_ref (kind, owner_ref, key_id) VALUES ($1,$2,$3) ON CONFLICT (kind, owner_ref) DO NOTHING`,
			kind.String(), owner, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // created concurrently by another caller; theirs wins
		}
		_, err = tx.Exec(ctx, `INSERT INTO data_key (key_id, version, wrapped, root_key) VALUES ($1,1,$2,$3)`, id, wrapped, root)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	return nil
}

func (s *Service) aeadFor(ctx context.Context, id uuid.UUID, ver uint32) (cipher.AEAD, error) {
	ck := fmt.Sprintf("%s:%d", id, ver)
	now := s.now()
	s.mu.Lock()
	if e, ok := s.deks[ck]; ok && now.Before(e.exp) {
		s.mu.Unlock()
		return e.aead, nil
	}
	s.mu.Unlock()
	var wrapped []byte
	err := s.store.QueryRow(ctx, `SELECT wrapped FROM data_key WHERE key_id=$1 AND version=$2`, id, ver32(ver)).Scan(&wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrKeyDestroyed
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	dek, err := s.kp.Unwrap(ctx, wrapped, dekAAD(id, ver))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	g, err := gcm(dek)
	clear(dek) // our private copy; the cipher keeps its own key schedule
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.deks) >= cacheMax {
		s.dropAllLocked()
	}
	s.deks[ck] = cacheEntry{aead: g, exp: now.Add(cacheTTL)}
	s.mu.Unlock()
	return g, nil
}

func (s *Service) dropAllLocked() {
	clear(s.deks)
}

func (s *Service) evict(id uuid.UUID) {
	prefix := id.String() + ":"
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.deks {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(s.deks, k)
		}
	}
	for k, e := range s.refs {
		if e.keyID == id {
			delete(s.refs, k)
		}
	}
}

// Tombstone is the record of a destroyed key (never key material).
type Tombstone struct {
	KeyID       string    `json:"keyId"`
	Version     *uint32   `json:"version,omitempty"` // nil = all versions
	Kind        string    `json:"kind"`
	Reason      string    `json:"reason"`
	DestroyedAt time.Time `json:"destroyedAt"`
}

const tombstonePrefix = "key-destruction/"

// DestroyPatientKey crypto-shreds a patient's erasable data. Call it only when the
// RET-18 legal-minimum hold has ended (review R-01), never at the erasure request.
func (s *Service) DestroyPatientKey(ctx context.Context, patientRef, reason string) error {
	return s.destroy(ctx, KindPatient, patientRef, reason)
}

// DestroyCareKey destroys one patient-doctor relationship's clinical records key when
// its RET-02 period ends (unless a legal hold applies; the caller checks that).
func (s *Service) DestroyCareKey(ctx context.Context, patientRef, careOwnerRef, reason string) error {
	return s.destroy(ctx, KindCare, CareOwner(patientRef, careOwnerRef), reason)
}

// DestroyVerificationKey destroys a doctor's ID-evidence key (90 days after decision, RET-08).
func (s *Service) DestroyVerificationKey(ctx context.Context, doctorRef, reason string) error {
	return s.destroy(ctx, KindVerification, doctorRef, reason)
}

func (s *Service) destroy(ctx context.Context, kind Kind, owner, reason string) error {
	if !reReason.MatchString(reason) {
		return errors.New("crypto: reason must be a code")
	}
	var id uuid.UUID
	var status string
	err := s.store.QueryRow(ctx, `SELECT key_id, status FROM key_ref WHERE kind=$1 AND owner_ref=$2`, kind.String(), owner).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		// Never had a key. Record a tombstone-like ref so no key is ever created later.
		id, _ = uuid.NewV7()
		_, err = s.store.Exec(ctx, `INSERT INTO key_ref (kind, owner_ref, key_id, status, destroyed_at) VALUES ($1,$2,$3,'destroyed',now())
			ON CONFLICT (kind, owner_ref) DO NOTHING`, kind.String(), owner, id)
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	ts := Tombstone{KeyID: id.String(), Kind: kind.String(), Reason: reason, DestroyedAt: s.now().UTC()}
	// 1. Durable tombstone first, in write-once storage outside the key store database.
	doc, _ := json.Marshal(ts)
	key := fmt.Sprintf("%s%s/%s-%d.json", tombstonePrefix, ts.DestroyedAt.Format("2006-01-02"), id, ts.DestroyedAt.UnixNano())
	if err := s.blob.PutWriteOnce(ctx, key, doc); err != nil {
		return fmt.Errorf("crypto: write tombstone: %w", err)
	}
	// 2. Destroy in the key store.
	if err := s.applyTombstone(ctx, ts); err != nil {
		return err
	}
	s.evict(id)
	return nil
}

func (s *Service) applyTombstone(ctx context.Context, ts Tombstone) error {
	id, err := uuid.Parse(ts.KeyID)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.store, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if ts.Version == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM data_key WHERE key_id=$1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE key_ref SET status='destroyed', destroyed_at=coalesce(destroyed_at, $2) WHERE key_id=$1`, id, ts.DestroyedAt); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `DELETE FROM data_key WHERE key_id=$1 AND version=$2`, id, ver32(*ts.Version)); err != nil {
			return err
		}
		var v any
		if ts.Version != nil {
			v = ver32(*ts.Version)
		}
		_, err := tx.Exec(ctx, `INSERT INTO destruction_log (key_id, version, kind, reason, destroyed_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (key_id, destroyed_at) DO NOTHING`, id, v, ts.Kind, ts.Reason, ts.DestroyedAt)
		return err
	})
}

// Replay re-applies every tombstone from write-once storage. It must run after any
// restore of the key store, before the service opens (encryption-design §4 step 4,
// TC-04). It returns how many tombstones were applied.
func (s *Service) Replay(ctx context.Context) (int, error) {
	keysList, err := s.blob.List(ctx, tombstonePrefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keysList {
		b, err := s.blob.Get(ctx, k)
		if err != nil {
			return n, err
		}
		var ts Tombstone
		if err := json.Unmarshal(b, &ts); err != nil {
			return n, fmt.Errorf("crypto: bad tombstone %s: %w", k, err)
		}
		if err := s.applyTombstone(ctx, ts); err != nil {
			return n, err
		}
		if id, err := uuid.Parse(ts.KeyID); err == nil {
			s.evict(id)
		}
		n++
	}
	return n, nil
}

// Rotate creates a new version of a data key (suspected compromise, or the service-key
// policy). New writes use it at once on this instance, and on others within the cache
// TTL; reads keep using the version in each ciphertext header.
func (s *Service) Rotate(ctx context.Context, kind Kind, owner string) (uint32, error) {
	var newVer int32
	err := db.InTx(ctx, s.store, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id uuid.UUID
		var cur int32
		var status string
		if err := tx.QueryRow(ctx, `SELECT key_id, current_version, status FROM key_ref WHERE kind=$1 AND owner_ref=$2 FOR UPDATE`,
			kind.String(), owner).Scan(&id, &cur, &status); err != nil {
			return err
		}
		if status != "active" {
			return ErrKeyDestroyed
		}
		dek := make([]byte, 32)
		if _, err := rand.Read(dek); err != nil {
			return err
		}
		defer clear(dek)
		newVer = cur + 1
		wrapped, root, err := s.kp.Wrap(ctx, dek, dekAAD(id, verU(newVer)))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO data_key (key_id, version, wrapped, root_key) VALUES ($1,$2,$3,$4)`, id, newVer, wrapped, root); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE key_ref SET current_version=$2 WHERE key_id=$1`, id, newVer)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	delete(s.refs, refKey(kind, owner))
	s.mu.Unlock()
	return verU(newVer), nil
}

// RetireVersion destroys an old data-key version after the re-encryption job has moved
// every row to the current version. Wait at least the cache TTL after Rotate first.
func (s *Service) RetireVersion(ctx context.Context, kind Kind, owner string, version uint32, reason string) error {
	if !reReason.MatchString(reason) {
		return errors.New("crypto: reason must be a code")
	}
	ref, err := s.currentRef(ctx, kind, owner, false)
	if err != nil {
		return err
	}
	if version >= ref.version {
		return errors.New("crypto: cannot retire the current version")
	}
	v := version
	ts := Tombstone{KeyID: ref.keyID.String(), Version: &v, Kind: kind.String(), Reason: reason, DestroyedAt: s.now().UTC()}
	doc, _ := json.Marshal(ts)
	key := fmt.Sprintf("%s%s/%s-v%d-%d.json", tombstonePrefix, ts.DestroyedAt.Format("2006-01-02"), ref.keyID, v, ts.DestroyedAt.UnixNano())
	if err := s.blob.PutWriteOnce(ctx, key, doc); err != nil {
		return fmt.Errorf("crypto: write tombstone: %w", err)
	}
	if err := s.applyTombstone(ctx, ts); err != nil {
		return err
	}
	s.evict(ref.keyID)
	return nil
}

// RewrapRoot re-wraps data keys whose root key is not the current one (yearly KMS
// rotation). Only the key store changes; encrypted data is untouched.
func (s *Service) RewrapRoot(ctx context.Context) (int, error) {
	cur := s.kp.CurrentRoot()
	rows, err := s.store.Query(ctx, `SELECT key_id, version, wrapped FROM data_key WHERE root_key <> $1`, cur)
	if err != nil {
		return 0, err
	}
	type item struct {
		id  uuid.UUID
		ver int32
		w   []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.ver, &it.w); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	n := 0
	for _, it := range items {
		dek, err := s.kp.Unwrap(ctx, it.w, dekAAD(it.id, verU(it.ver)))
		if err != nil {
			return n, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
		}
		w, root, err := s.kp.Wrap(ctx, dek, dekAAD(it.id, verU(it.ver)))
		clear(dek)
		if err != nil {
			return n, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
		}
		if _, err := s.store.Exec(ctx, `UPDATE data_key SET wrapped=$3, root_key=$4 WHERE key_id=$1 AND version=$2`, it.id, it.ver, w, root); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// BlindIndex returns a keyed hash for exact-match lookup without decrypting (phone
// sign-in, encryption-design §5). purpose separates index spaces ("identity.phone").
// value must already be normalised by the caller (E.164 for phones).
func (s *Service) BlindIndex(purpose, value string) []byte {
	m := hmac.New(sha256.New, s.blindKey)
	m.Write([]byte("mugizh-blind-index|v1|" + purpose))
	derived := m.Sum(nil)
	m2 := hmac.New(sha256.New, derived)
	m2.Write([]byte(value))
	return m2.Sum(nil)
}

// Keyed returns HMAC-SHA-256(purpose-derived key, value) as a stable pseudonym, for
// example the IP hash in audit events. It uses the same secret as BlindIndex with a
// separate purpose, so values cannot be correlated across purposes.
func (s *Service) Keyed(purpose, value string) []byte { return s.BlindIndex("keyed."+purpose, value) }

// Bounded integer conversions. Key versions are 1..MaxInt32 (key store CHECK constraint
// and header validation); field lengths are small strings chosen by code.
func ver32(v uint32) int32 {
	if v == 0 || v > math.MaxInt32 {
		panic("crypto: key version out of range")
	}
	return int32(v) // #nosec G115 -- range checked above
}

func verU(v int32) uint32 {
	if v <= 0 {
		panic("crypto: key version out of range")
	}
	return uint32(v) // #nosec G115 -- range checked above
}

func lenU32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic("crypto: length out of range")
	}
	return uint32(n) // #nosec G115 -- range checked above
}
