package admin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// reverifyFake records referrals; fail makes the verification module refuse.
type reverifyFake struct {
	got  []string
	fail error
}

func (r *reverifyFake) TriggerReverification(_ context.Context, actor, subject, reason string) error {
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, actor+"|"+subject+"|"+reason)
	return nil
}

// TM-31 (S-178): the grievance officer can refer the professional named in a complaint for
// re-verification; only complaints, only the grievance officer; the referral is on the trail
// and audited; a refusal by verification records nothing.
func TestTM31ComplaintReferredForReverification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	officer := f.staff("grievance_officer")
	doctor := ids.New()
	g, err := f.a.Intake(ctx, officer, pub.KindComplaint, "email:1", "complaint about a doctor", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.ReferForReverification(ctx, f.staff("support"), g.ID, doctor); err == nil {
		t.Fatal("only the grievance officer may refer")
	}
	other, err := f.a.Intake(ctx, officer, pub.KindLawfulRequest, "email:2", "an order", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.ReferForReverification(ctx, officer, other.ID, doctor); !errors.Is(err, pub.ErrInvalid) {
		t.Fatalf("only complaints can be referred: %v", err)
	}
	if err := f.a.ReferForReverification(ctx, officer, ids.New(), doctor); err == nil {
		t.Fatal("an unknown grievance is refused")
	}
	if len(f.rev.got) != 0 {
		t.Fatalf("nothing referred yet: %v", f.rev.got)
	}
	if err := f.a.ReferForReverification(ctx, officer, g.ID, doctor); err != nil {
		t.Fatal(err)
	}
	if len(f.rev.got) != 1 || f.rev.got[0] != officer.Ref+"|"+doctor+"|complaint" {
		t.Fatalf("referral: %v", f.rev.got)
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.grievance_event WHERE grievance_id=$1 AND action='referred_reverification'`, g.ID); n != 1 {
		t.Fatalf("trail: %d", n)
	}
	if n := f.fbCount(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='admin.reverification_referred' AND resource_ref=$1`, doctor); n != 1 {
		t.Fatalf("audit: %d", n)
	}
	f.rev.fail = errors.New("not verified")
	if err := f.a.ReferForReverification(ctx, officer, g.ID, ids.New()); err == nil {
		t.Fatal("a refusal by verification is returned")
	}
	if n := f.fbCount(t, `SELECT count(*) FROM admin.grievance_event WHERE grievance_id=$1 AND action='referred_reverification'`, g.ID); n != 1 {
		t.Fatalf("a refused referral records nothing: %d", n)
	}
}
