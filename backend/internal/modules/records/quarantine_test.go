package records_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// AAD-05 (TM-43): a document reported to show a prohibited identifier is hidden at once from the patient, doctors,
// links already issued and the quota; the patient is told; the purge job hard-deletes the row and the blob; the audit
// trail never holds the number. A doctor may report only what they can read now.
func TestAAD05QuarantineAndPurge(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient, doc, clinic, booking := ids.New(), ids.New(), ids.New(), ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	p := x.patientRef(patient, false)
	keep := x.upload(t, p, "Blood test", pdf("synthetic report"))
	bad := x.upload(t, p, "ID scan", pdf("synthetic id card"))
	dc := rp.Caller{UserRef: doc, Role: "doctor", ClinicRef: clinic}

	// A doctor with no relationship learns nothing and changes nothing.
	if err := x.r.ReportProhibitedID(ctx, dc, bad.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("doctor without a share: %v", err)
	}
	if _, err := x.m.ShareT3(ctx, patient, doc, clinic, booking, x.now.Add(7*day), versions["P-SHARE"], "PN-1.0", ""); err != nil {
		t.Fatal(err)
	}
	link, err := x.r.IssueDocumentLink(ctx, dc, bad.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else (another patient) cannot report it.
	if err := x.r.ReportProhibitedID(ctx, x.patient(false), bad.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("another patient: %v", err)
	}
	if err := x.r.ReportProhibitedID(ctx, dc, bad.ID); err != nil {
		t.Fatal(err)
	}
	// Hidden everywhere at once.
	list, _ := x.r.ListDocuments(ctx, p)
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("patient list: %+v", list)
	}
	shared, err := x.r.ListPatientDocuments(ctx, dc, patient)
	if err != nil || len(shared.Items) != 1 || shared.Items[0].ID != keep.ID {
		t.Fatalf("doctor list: %+v %v", shared, err)
	}
	if _, err := x.r.Download(ctx, dc, link.Token); err == nil {
		t.Fatal("a link issued before the report must stop working")
	}
	if _, err := x.r.IssueDocumentLink(ctx, p, bad.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("no new link: %v", err)
	}
	if err := x.r.ReportProhibitedID(ctx, p, bad.ID); err != nil {
		t.Fatalf("the patient reporting again succeeds: %v", err)
	}
	if err := x.r.ReportProhibitedID(ctx, dc, bad.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("the doctor learns nothing about a hidden document: %v", err)
	}
	if !slices.Contains(x.outboxTypes(t), "document.quarantined") {
		t.Fatal("the patient must be told")
	}
	var reason, role string
	_ = x.env.Owner.QueryRow(ctx, `SELECT hidden_reason, hidden_by_role FROM records.document WHERE id=$1`, bad.ID).Scan(&reason, &role)
	if reason != "prohibited_identifier" || role != "doctor" {
		t.Fatalf("hidden as %q by %q", reason, role)
	}
	key := "documents/" + patient + "/" + bad.ID + ".bin"
	if _, err := x.blobs.Get(ctx, key); err != nil {
		t.Fatal("the blob stays until the purge job runs")
	}
	n, err := x.r.PurgeHidden(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if _, err := x.blobs.Get(ctx, key); err == nil {
		t.Fatal("the blob must be hard-deleted")
	}
	var rows int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.document WHERE id=$1`, bad.ID).Scan(&rows)
	if rows != 0 {
		t.Fatal("the row must be hard-deleted")
	}
	var audited int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM audit.audit_outbox WHERE resource_ref=$1 AND action IN ('records.document_quarantined','records.document_deleted')`, bad.ID).Scan(&audited)
	if audited != 2 {
		t.Fatalf("report and deletion audited: %d", audited)
	}
	if n, _ := x.r.PurgeHidden(ctx, 10); n != 0 {
		t.Fatal("nothing left to purge")
	}
	if _, err := x.blobs.Get(ctx, "documents/"+patient+"/"+keep.ID+".bin"); err != nil {
		t.Fatal("the other document is untouched")
	}
}

// RET-03: the patient deletes their own upload; it leaves the list and the quota at once and is purged; someone else's
// document is not found; deleting again succeeds; nothing is announced (no quarantine message).
func TestRET03PatientDeletesOwnDocument(t *testing.T) {
	x := setupRights(t)
	ctx := context.Background()
	patient := ids.New()
	x.consent(t, patient, "P-ACCOUNT", "P-CARE", "P-RECORD")
	p := x.patientRef(patient, false)
	d := x.upload(t, p, "Old scan", pdf("synthetic"))
	if err := x.r.DeleteDocument(ctx, x.patient(false), d.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("someone else's document: %v", err)
	}
	if err := x.r.DeleteDocument(ctx, rp.Caller{UserRef: ids.New(), Role: "doctor"}, d.ID); err == nil {
		t.Fatal("only the patient deletes their uploads")
	}
	for i := 0; i < 2; i++ {
		if err := x.r.DeleteDocument(ctx, p, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := x.r.ListDocuments(ctx, p); len(list) != 0 {
		t.Fatalf("gone from the list: %+v", list)
	}
	if slices.Contains(x.outboxTypes(t), "document.quarantined") {
		t.Fatal("a deletion is not a quarantine")
	}
	x.now = x.now.Add(time.Minute)
	if n, err := x.r.PurgeHidden(ctx, 10); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if err := x.r.DeleteDocument(ctx, p, d.ID); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("after the purge the document no longer exists: %v", err)
	}
}
