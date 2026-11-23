package records_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// The revocation journal (backup-and-restore.md section 2 item 4, K-DP-02): a consent
// withdrawal, a grant revocation or a caregiver link revocation made after a backup must
// not come back as active after a Core restore. Each test follows the same steps: back up,
// stop, restore, show the restored database allows access again (the precondition), replay,
// show access is denied through Authorize.

func (x *rx) revocationKeys(t *testing.T) []string {
	t.Helper()
	k, err := x.blobs.List(context.Background(), "revocation-journal/")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (x *rx) historyRequest(doc, clinic, patient string) rp.Request {
	return rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalHistory, ResourceType: "note", ResourceRef: ids.New()}
}

func (x *rx) replayRevocations(t *testing.T) int {
	t.Helper()
	n, err := x.m.ReplayRevocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (x *rx) auditActions(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := x.env.Owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.audit_outbox WHERE action=$1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (x *rx) withdraw(t *testing.T, patient, purpose string) {
	t.Helper()
	if err := x.m.WithdrawConsent(context.Background(), rp.ConsentChange{SubjectRef: patient, PurposeID: purpose, NoticeVersion: "PN-1.0", Channel: "app"}); err != nil {
		t.Fatal(err)
	}
}

func TestRevocationJournalConsentWithdrawalReplay(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-SHARE")
	if _, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(60*day), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	req := x.historyRequest(doc, clinic, patient)
	if d := mustDecide(t, x.m, req); !d.Allowed {
		t.Fatalf("before: %+v", d)
	}
	before := x.backup(t)
	x.now = x.now.Add(time.Hour)
	x.withdraw(t, patient, "P-SHARE")
	if k := x.revocationKeys(t); len(k) != 1 || !strings.Contains(k[0], "revocation-journal/consent/") {
		t.Fatalf("the withdrawal must be journaled in write-once storage: %v", k)
	}
	if d := mustDecide(t, x.m, req); d.Allowed {
		t.Fatalf("withdrawn: %+v", d)
	}
	x.restore(t, before, patient)
	if d := mustDecide(t, x.m, req); !d.Allowed {
		t.Fatalf("precondition: the restore brought the consent back: %+v", d)
	}
	if n := x.replayRevocations(t); n != 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := mustDecide(t, x.m, req); d.Allowed {
		t.Fatalf("after replay access is denied again: %+v", d)
	}
	if n := x.auditActions(t, "consent.withdrawal_replayed"); n != 1 {
		t.Fatalf("audited once: %d", n)
	}
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("second replay changed %d", n)
	}
	if n := x.auditActions(t, "consent.withdrawal_replayed"); n != 1 {
		t.Fatalf("still audited once: %d", n)
	}
}

// A consent granted again after the withdrawal is the patient's later choice: replay never
// withdraws it, and never grants anything either.
func TestRevocationJournalKeepsLaterRegrant(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-SHARE")
	x.now = x.now.Add(time.Hour)
	x.withdraw(t, patient, "P-SHARE")
	x.now = x.now.Add(time.Hour)
	x.consent(t, patient, "P-SHARE")
	bk := x.backup(t)
	x.restore(t, bk, patient)
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("a later re-grant must stand: %d", n)
	}
	if ok, _ := x.m.HasConsent(ctx, patient, "P-SHARE"); !ok {
		t.Fatal("consent must still be granted")
	}
}

func TestRevocationJournalShareRevokeReplay(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-SHARE")
	gid, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(60*day), versions["P-SHARE"], "PN-1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	req := x.historyRequest(doc, clinic, patient)
	before := x.backup(t)
	x.now = x.now.Add(time.Hour)
	if err := x.m.Revoke(ctx, gid, patient, true, ""); err != nil {
		t.Fatal(err)
	}
	if k := x.revocationKeys(t); len(k) != 1 || !strings.HasSuffix(k[0], "revocation-journal/grant/"+gid+".json") {
		t.Fatalf("journaled: %v", k)
	}
	x.restore(t, before, patient)
	if d := mustDecide(t, x.m, req); !d.Allowed {
		t.Fatalf("precondition: the restore brought the share back: %+v", d)
	}
	if n := x.replayRevocations(t); n != 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := mustDecide(t, x.m, req); d.Allowed {
		t.Fatalf("after replay the share is gone: %+v", d)
	}
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("second replay changed %d", n)
	}
}

// T1 revoked because a booking was cancelled goes through the same journal.
func TestRevocationJournalT1Replay(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE")
	if _, err := x.m.GrantT1(ctx, patient, doc, clinic, booking, x.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := rp.Request{Actor: doctor(doc, clinic), PatientRef: patient, Kind: rp.KindClinicalCurrent, BookingRef: booking}
	before := x.backup(t)
	if err := x.m.RevokeForBooking(ctx, booking); err != nil {
		t.Fatal(err)
	}
	if k := x.revocationKeys(t); len(k) != 1 {
		t.Fatalf("journaled: %v", k)
	}
	x.restore(t, before, patient)
	if d := mustDecide(t, x.m, req); !d.Allowed {
		t.Fatalf("precondition: %+v", d)
	}
	if n := x.replayRevocations(t); n != 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := mustDecide(t, x.m, req); d.Allowed {
		t.Fatalf("after replay: %+v", d)
	}
}

func TestRevocationJournalCaregiverRevokeReplay(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	patient, cg := ids.New(), ids.New()
	link := x.redeem(t, cg, x.invite(t, patient, nil).Code)
	if d := x.read(t, cg, patient, rp.KindReminders); !d.Allowed {
		t.Fatalf("before: %+v", d)
	}
	before := x.backup(t)
	if err := x.r.RevokeCaregiver(ctx, x.patientRef(patient, false), link.ID); err != nil {
		t.Fatal(err)
	}
	if k := x.revocationKeys(t); len(k) != 1 || !strings.HasSuffix(k[0], "revocation-journal/link/"+link.ID+".json") {
		t.Fatalf("journaled: %v", k)
	}
	x.restore(t, before, patient)
	if d := x.read(t, cg, patient, rp.KindReminders); !d.Allowed {
		t.Fatalf("precondition: the restore brought the link back: %+v", d)
	}
	if n := x.replayRevocations(t); n != 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := x.read(t, cg, patient, rp.KindReminders); d.Allowed {
		t.Fatalf("after replay: %+v", d)
	}
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("second replay changed %d", n)
	}
}

func TestRevocationJournalCaregiverLeaveReplay(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	patient, cg := ids.New(), ids.New()
	link := x.redeem(t, cg, x.invite(t, patient, nil).Code)
	before := x.backup(t)
	if err := x.r.LeaveCaregiving(ctx, x.patientRef(cg, false), patient); err != nil {
		t.Fatal(err)
	}
	if k := x.revocationKeys(t); len(k) != 1 || !strings.HasSuffix(k[0], "revocation-journal/link/"+link.ID+".json") {
		t.Fatalf("journaled: %v", k)
	}
	x.restore(t, before, patient)
	if d := x.read(t, cg, patient, rp.KindReminders); !d.Allowed {
		t.Fatalf("precondition: %+v", d)
	}
	if n := x.replayRevocations(t); n != 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := x.read(t, cg, patient, rp.KindReminders); d.Allowed {
		t.Fatalf("after replay: %+v", d)
	}
}

// Withdrawing P-CAREGIVER ends every link; the restore brings both the consent and the
// links back; replay ends them again (the consent through the same code path as the
// patient's own withdrawal, the links from their own entries).
func TestRevocationJournalConsentWithdrawalEndsLinksReplay(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	patient, cg := ids.New(), ids.New()
	x.redeem(t, cg, x.invite(t, patient, nil).Code)
	before := x.backup(t)
	x.now = x.now.Add(time.Hour)
	x.withdraw(t, patient, "P-CAREGIVER")
	if k := x.revocationKeys(t); len(k) != 2 {
		t.Fatalf("one consent entry and one link entry: %v", k)
	}
	x.restore(t, before, patient)
	if d := x.read(t, cg, patient, rp.KindReminders); !d.Allowed {
		t.Fatalf("precondition: %+v", d)
	}
	if n := x.replayRevocations(t); n < 1 {
		t.Fatalf("replay changed %d", n)
	}
	if d := x.read(t, cg, patient, rp.KindReminders); d.Allowed {
		t.Fatalf("after replay: %+v", d)
	}
	if x.activeLinks(t, patient) != 0 {
		t.Fatal("the link must be revoked")
	}
	if ok, _ := x.m.HasConsent(ctx, patient, "P-CAREGIVER"); ok {
		t.Fatal("consent must be withdrawn")
	}
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("second replay changed %d", n)
	}
}

// Replay removes only: a grant made after the backup is not recreated, and nothing that
// was revoked comes back.
func TestRevocationJournalNeverRecreates(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-SHARE")
	before := x.backup(t)
	gid, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(60*day), versions["P-SHARE"], "PN-1.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.m.Revoke(ctx, gid, patient, true, ""); err != nil {
		t.Fatal(err)
	}
	x.restore(t, before, patient)
	if n := x.replayRevocations(t); n != 0 {
		t.Fatalf("nothing to remove: %d", n)
	}
	var rows int
	if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.permission_grant WHERE id=$1`, gid).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a grant made after the backup is not recreated: %d %v", rows, err)
	}
}

// A corrupt, forged or unknown-kind entry, or a key that does not match its content, stops start-up.
func TestRevocationJournalRefusesBadEntries(t *testing.T) {
	ctx := context.Background()
	id, subj, at := ids.New(), ids.New(), time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	consent := `{"v":1,"kind":"consent","id":"` + id + `","subjectRef":"` + subj + `","purposeId":"P-SHARE","at":"` + at + `"}`
	cases := map[string]string{
		"corrupt":      "consent/" + id + ".json|{not json",
		"unknown kind": "purge/" + id + ".json|" + `{"v":1,"kind":"purge","id":"` + id + `","subjectRef":"` + subj + `","at":"` + at + `"}`,
		"key mismatch": "consent/" + ids.New() + ".json|" + consent,
		"kind mismatch": "link/" + id + ".json|" + consent,
		"wrong version": "consent/" + id + ".json|" + strings.Replace(consent, `"v":1`, `"v":2`, 1),
		"bad purpose":   "consent/" + id + ".json|" + strings.Replace(consent, "P-SHARE", "bad", 1),
		"bad subject":   "consent/" + id + ".json|" + strings.Replace(consent, subj, "not-a-uuid", 1),
		"no time":       "consent/" + id + ".json|" + strings.Replace(consent, `,"at":"`+at+`"`, "", 1),
		"extra field":   "consent/" + id + ".json|" + strings.Replace(consent, `"v":1`, `"v":1,"contact":"x"`, 1),
		"link without caregiver": "link/" + id + ".json|" + `{"v":1,"kind":"link","id":"` + id + `","subjectRef":"` + subj + `","linkId":"` + id + `","at":"` + at + `"}`,
		"grant id differs":       "grant/" + id + ".json|" + `{"v":1,"kind":"grant","id":"` + id + `","subjectRef":"` + subj + `","grantId":"` + ids.New() + `","at":"` + at + `"}`,
	}
	for name, c := range cases {
		key, doc, _ := strings.Cut(c, "|")
		y := setupRights(t)
		if err := y.blobs.PutWriteOnce(ctx, "revocation-journal/"+key, []byte(doc)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.m.ReplayRevocations(ctx); err == nil {
			t.Fatalf("%s: a bad entry must stop start-up", name)
		}
	}
}

// failingRevocations refuses revocation journal writes while down.
type failingRevocations struct {
	blobstore.LocalDir
	down atomic.Bool
}

func (f *failingRevocations) PutWriteOnce(ctx context.Context, key string, data []byte) error {
	if f.down.Load() && strings.HasPrefix(key, "revocation-journal/") {
		return errors.New("object store unavailable")
	}
	return f.LocalDir.PutWriteOnce(ctx, key, data)
}

// An object-store outage never blocks the patient's withdrawal; the worker writes the
// missing entries on its next run.
func TestRevocationJournalCatchUp(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	store := &failingRevocations{LocalDir: x.blobs}
	r, err := records.NewRights(x.m, rightsConfig(), records.RightsDeps{Crypto: x.cr, Blobs: store, Scanner: x.scan, Notifier: x.note, AccessLog: x.audit})
	if err != nil {
		t.Fatal(err)
	}
	patient := ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD", "P-SHARE")
	store.down.Store(true)
	x.withdraw(t, patient, "P-SHARE")
	if k := x.revocationKeys(t); len(k) != 0 {
		t.Fatalf("nothing journaled during the outage: %v", k)
	}
	store.down.Store(false)
	if err := r.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if k := x.revocationKeys(t); len(k) != 1 {
		t.Fatalf("the worker catches up: %v", k)
	}
	if err := r.RunDue(ctx); err != nil {
		t.Fatalf("catch-up is idempotent: %v", err)
	}
	if k := x.revocationKeys(t); len(k) != 1 {
		t.Fatalf("no duplicate: %v", k)
	}
}
