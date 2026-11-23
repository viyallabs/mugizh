package records_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Review findings on caregiver phase 1: races (M1, L11), erasure of the caller (M2), audit of refusals (L4),
// the per-network limit (L5).

func (x *rx) activeLinks(t *testing.T, patient string) int {
	t.Helper()
	var n int
	if err := x.env.Owner.QueryRow(context.Background(), `SELECT count(*) FROM records.caregiver_link WHERE patient_ref=$1 AND status='active'`, patient).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// race runs a and b at the same moment.
func race(a, b func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, f := range []func(){a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f()
		}()
	}
	close(start)
	wg.Wait()
}

// M1: a redeem racing the withdrawal of P-CAREGIVER never leaves an active link behind.
func TestCaregiverWithdrawVersusRedeemNeverLeavesActiveLink(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		p, cg := ids.New(), ids.New()
		inv := x.invite(t, p, nil)
		race(func() {
			_ = x.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-CAREGIVER", NoticeVersion: "PN-1.0", Channel: "app"})
		}, func() {
			_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true)
		})
		if n := x.activeLinks(t, p); n != 0 {
			t.Fatalf("round %d: %d active link(s) after the consent was withdrawn", i, n)
		}
		if ok, _ := x.m.HasConsent(ctx, p, "P-CAREGIVER"); ok {
			t.Fatal("consent should be withdrawn")
		}
	}
}

// M1: a redeem racing erasure step 1 never leaves an active link behind.
func TestCaregiverErasureVersusRedeemNeverLeavesActiveLink(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		p, cg := ids.New(), ids.New()
		inv := x.invite(t, p, nil)
		race(func() {
			_, _ = x.r.RequestRights(ctx, x.patientRef(p, true), rp.RightsErasure, "")
		}, func() {
			_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true)
		})
		if n := x.activeLinks(t, p); n != 0 {
			t.Fatalf("round %d: %d active link(s) after erasure step 1", i, n)
		}
	}
}

// M1: the P-CAREGIVER consent is checked again inside the invitation transaction: a withdrawal racing an
// invitation never leaves an open invitation behind.
func TestCaregiverWithdrawVersusInviteNeverLeavesOpenInvitation(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		p := ids.New()
		x.consent(t, p, "P-CAREGIVER")
		race(func() {
			_ = x.m.WithdrawConsent(ctx, rp.ConsentChange{SubjectRef: p, PurposeID: "P-CAREGIVER", NoticeVersion: "PN-1.0", Channel: "app"})
		}, func() {
			_, _ = x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, "")
		})
		var n int
		if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_invitation WHERE patient_ref=$1 AND revoked_at IS NULL AND redeemed_at IS NULL`, p).Scan(&n); err != nil || n != 0 {
			t.Fatalf("round %d: %d open invitation(s) without consent: %v", i, n, err)
		}
	}
}

// M2: a caller whose own account is under an erasure restriction cannot redeem; the answer is the usual 404.
func TestCaregiverRedeemRefusedForCallerUnderErasure(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	inv := x.invite(t, p, nil)
	if _, err := x.r.RequestRights(ctx, x.patientRef(cg, true), rp.RightsErasure, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("caller under erasure: %v", err)
	}
	if x.activeLinks(t, p) != 0 {
		t.Fatal("no link may be made")
	}
}

// M2: a redeem racing the caller's own erasure step 1 never leaves an active link behind.
func TestCaregiverCallerErasureVersusRedeemNeverLeavesActiveLink(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		p, cg := ids.New(), ids.New()
		inv := x.invite(t, p, nil)
		race(func() {
			_, _ = x.r.RequestRights(ctx, x.patientRef(cg, true), rp.RightsErasure, "")
		}, func() {
			_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true)
		})
		if n := x.activeLinks(t, p); n != 0 {
			t.Fatalf("round %d: %d active link(s) for a caregiver under erasure", i, n)
		}
	}
}

// L11: two simultaneous redeems of one code make exactly one link.
func TestCaregiverTwoRedeemsOfOneCodeMakeOneLink(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		p := ids.New()
		inv := x.invite(t, p, nil)
		var mu sync.Mutex
		var ok int
		try := func() {
			if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), inv.Code, true); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}
		race(try, try)
		if ok != 1 || x.activeLinks(t, p) != 1 {
			t.Fatalf("round %d: %d successes, %d links", i, ok, x.activeLinks(t, p))
		}
	}
}

// insertInvitation stores an open invitation directly (as if made before another was redeemed), with a known code.
func (x *rx) insertInvitation(t *testing.T, patient, code string) {
	t.Helper()
	if _, err := x.env.Owner.Exec(context.Background(), `INSERT INTO records.caregiver_invitation (id, patient_ref, code_hash, scopes, created_at, expires_at)
		VALUES ($1,$2,$3,ARRAY['reminders'],$4,$5)`, ids.New(), patient, x.cr.Keyed("records.caregiver_code", code), x.now, x.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// L11: racing redeems for the last slots never give a patient more than 5 caregivers, nor a caregiver more than 5 patients.
func TestCaregiverRacingRedeemsRespectTheLimits(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p := ids.New()
	x.consent(t, p, "P-CAREGIVER")
	for i := 0; i < 4; i++ {
		x.redeem(t, ids.New(), x.invite(t, p, nil).Code)
	}
	x.insertInvitation(t, p, "AAAAAAA1")
	x.insertInvitation(t, p, "AAAAAAA2")
	x.insertInvitation(t, p, "AAAAAAA3")
	var wg sync.WaitGroup
	for _, c := range []string{"AAAAAAA1", "AAAAAAA2", "AAAAAAA3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), c, true)
		}()
	}
	wg.Wait()
	if n := x.activeLinks(t, p); n != 5 {
		t.Fatalf("a patient has at most 5 caregivers, got %d", n)
	}
	// One caregiver racing for a 6th patient.
	cg := ids.New()
	for i := 0; i < 4; i++ {
		x.redeem(t, cg, x.invite(t, ids.New(), nil).Code)
	}
	var codes []string
	for _, c := range []string{"BBBBBBB1", "BBBBBBB2", "BBBBBBB3"} {
		x.insertInvitation(t, x.somePatient(t), c)
		codes = append(codes, c)
	}
	for _, c := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), c, true)
		}()
	}
	wg.Wait()
	var n int
	if err := x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.caregiver_link WHERE caregiver_ref=$1 AND status='active'`, cg).Scan(&n); err != nil || n != 5 {
		t.Fatalf("a caregiver has at most 5 patients, got %d %v", n, err)
	}
}

// somePatient is a patient who has consented to P-CAREGIVER (so redeeming for them is possible).
func (x *rx) somePatient(t *testing.T) string {
	t.Helper()
	p := ids.New()
	x.consent(t, p, "P-CAREGIVER")
	return p
}

// L4: a failed redeem and a rate-limit refusal are audited as denied events by the caller, without the code.
func TestCaregiverFailedRedeemsAreAudited(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	cg := ids.New()
	for i := 0; i < 5; i++ {
		_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), "ZZZZZZZZ", true)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), "ZZZZZZZZ", true); !errors.Is(err, rp.ErrRedeemLimited) {
		t.Fatalf("sixth: %v", err)
	}
	rows, err := x.env.Owner.Query(ctx, `SELECT reason_code, actor_ref, subject_ref, resource_ref, purpose FROM audit.audit_outbox WHERE action='caregiver.redeem_failed' AND result='denied'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	reasons := map[string]int{}
	for rows.Next() {
		var reason, actor, subject, resource, purpose string
		if err := rows.Scan(&reason, &actor, &subject, &resource, &purpose); err != nil {
			t.Fatal(err)
		}
		if actor != cg || subject != cg || resource != "" {
			t.Fatalf("actor and subject are the caller, no resource: %s %s %s", actor, subject, resource)
		}
		reasons[reason]++
	}
	if reasons["invalid_code"] != 5 || reasons["rate_limited"] != 1 {
		t.Fatalf("audited reasons: %v", reasons)
	}
}

// L5: at most 20 wrong codes per hour per network, across accounts.
func TestCaregiverRedeemPerNetworkLimit(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	from := func(ref, ipHash string) rp.Caller {
		c := x.patientRef(ref, false)
		c.IPHash = ipHash
		return c
	}
	for i := 0; i < 20; i++ {
		if _, err := x.r.RedeemCaregiverCode(ctx, from(ids.New(), "netA"), "ZZZZZZZZ", true); !errors.Is(err, rp.ErrNotFound) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, from(ids.New(), "netA"), "ZZZZZZZZ", true); !errors.Is(err, rp.ErrRedeemLimited) {
		t.Fatalf("21st from one network: %v", err)
	}
	if _, err := x.r.RedeemCaregiverCode(ctx, from(ids.New(), "netB"), "ZZZZZZZZ", true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("another network is not affected: %v", err)
	}
	x.now = x.now.Add(61 * time.Minute)
	if _, err := x.r.RedeemCaregiverCode(ctx, from(ids.New(), "netA"), "ZZZZZZZZ", true); !errors.Is(err, rp.ErrNotFound) {
		t.Fatalf("after the hour: %v", err)
	}
}

// M3(a): the patient may label an invitation (max 40 characters, free text checked like other free text); the label
// is sealed at rest, copied to the link and returned only to the patient.
func TestCaregiverLabelIsSealedCheckedAndShownOnlyToThePatient(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	x.consent(t, p, "P-CAREGIVER")
	for _, bad := range []string{"0123456789012345678901234567890123456789X", "call 98765 43210", "mum@example.com", "<b>Mum</b>", "www.mum.com", "my id 2345 6789 0123"} {
		if _, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, bad); !errors.Is(err, rp.ErrInvalid) {
			t.Fatalf("label %q must be refused: %v", bad, err)
		}
	}
	inv, err := x.r.CreateCaregiverInvitation(ctx, x.patientRef(p, true), nil, "  अम्मा Mum ")
	if err != nil || inv.Label != "अम्मा Mum" {
		t.Fatalf("label: %+v %v", inv, err)
	}
	var invSealed, linkSealed []byte
	if err := x.env.Owner.QueryRow(ctx, `SELECT label_sealed FROM records.caregiver_invitation WHERE id=$1`, inv.ID).Scan(&invSealed); err != nil || len(invSealed) == 0 || bytes.Contains(invSealed, []byte("Mum")) {
		t.Fatalf("the invitation label is sealed: %v", err)
	}
	link, err := x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), inv.Code, true)
	if err != nil {
		t.Fatal(err)
	}
	if link.Label != "" {
		t.Fatalf("the caregiver is never given the patient's label: %q", link.Label)
	}
	if err := x.env.Owner.QueryRow(ctx, `SELECT label_sealed FROM records.caregiver_link WHERE id=$1`, link.ID).Scan(&linkSealed); err != nil || len(linkSealed) == 0 || bytes.Contains(linkSealed, []byte("Mum")) {
		t.Fatalf("the link label is sealed: %v", err)
	}
	mine, _, err := x.r.ListMyCaregivers(ctx, x.patientRef(p, false), "", 20)
	if err != nil || len(mine) != 1 || mine[0].Label != "अम्मा Mum" {
		t.Fatalf("the patient sees the label: %+v %v", mine, err)
	}
	theirs, _, err := x.r.ListCaregivingPatients(ctx, x.patientRef(cg, false), "", 20)
	if err != nil || len(theirs) != 1 || theirs[0].Label != "" {
		t.Fatalf("the caregiver does not: %+v %v", theirs, err)
	}
}

// M3(b): accepting a code emits caregiving.linked for the patient (no names, no scopes); a failed redeem emits nothing.
func TestCaregiverRedeemEmitsLinkedEvent(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	inv := x.invite(t, p, nil)
	_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(cg, false), "ZZZZZZZZ", true)
	var n int
	_ = x.env.Owner.QueryRow(ctx, `SELECT count(*) FROM records.event_outbox WHERE type='caregiving.linked'`).Scan(&n)
	if n != 0 {
		t.Fatal("a failed redeem emits nothing")
	}
	link := x.redeem(t, cg, inv.Code)
	var subject, linkRef string
	var payload string
	if err := x.env.Owner.QueryRow(ctx, `SELECT subject_ref, payload->>'linkRef', payload::text FROM records.event_outbox WHERE type='caregiving.linked'`).Scan(&subject, &linkRef, &payload); err != nil {
		t.Fatal(err)
	}
	if subject != p || linkRef != link.ID || strings.Contains(payload, cg) || strings.Contains(payload, "reminders") {
		t.Fatalf("event: %s %s %s", subject, linkRef, payload)
	}
}

// M3(c): an invitation lives 48 hours.
func TestCaregiverInvitationLivesFortyEightHours(t *testing.T) {
	x := setupCare(t)
	inv := x.invite(t, ids.New(), nil)
	if !inv.ExpiresAt.Equal(x.now.Add(48 * time.Hour)) {
		t.Fatalf("expiry: %v", inv.ExpiresAt)
	}
}

// L6 and L7: the periodic purge removes expired attempt rows and invitations 30 days after they expired, were
// redeemed or were cancelled; the redeem path removes only the caller's own expired attempts.
func TestCaregiverPurge(t *testing.T) {
	x := setupCare(t)
	ctx := context.Background()
	p, cg := ids.New(), ids.New()
	redeemed := x.invite(t, p, nil)
	x.redeem(t, cg, redeemed.Code)
	cancelled := x.invite(t, p, nil)
	if err := x.r.CancelCaregiverInvitation(ctx, x.patientRef(p, false), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	x.invite(t, p, nil) // lapses unused
	_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), "ZZZZZZZZ", true)
	count := func(q string) int {
		var n int
		if err := x.env.Owner.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Another caller's expired attempt survives the redeem path; the periodic purge removes it.
	x.now = x.now.Add(2 * time.Hour)
	_, _ = x.r.RedeemCaregiverCode(ctx, x.patientRef(ids.New(), false), "ZZZZZZZZ", true)
	if count(`SELECT count(*) FROM records.caregiver_redeem_attempt`) != 2 {
		t.Fatal("the redeem path purges only the caller's own attempts")
	}
	if _, _, err := x.r.PurgeCaregiving(ctx); err != nil {
		t.Fatal(err)
	}
	if got := count(`SELECT count(*) FROM records.caregiver_redeem_attempt`); got != 1 {
		t.Fatalf("expired attempts after the periodic purge: %d", got)
	}
	// 29 days after redeem and cancel: nothing yet. 31 days: both go. The lapsed one goes 30 days after its expiry.
	x.now = x.now.Add(29 * 24 * time.Hour)
	if _, inv, _ := x.r.PurgeCaregiving(ctx); inv != 0 || count(`SELECT count(*) FROM records.caregiver_invitation`) != 3 {
		t.Fatalf("too early: %d", inv)
	}
	x.now = x.now.Add(2 * 24 * time.Hour)
	if _, inv, _ := x.r.PurgeCaregiving(ctx); inv != 2 || count(`SELECT count(*) FROM records.caregiver_invitation`) != 1 {
		t.Fatalf("redeemed and cancelled are purged: %d", inv)
	}
	x.now = x.now.Add(3 * 24 * time.Hour)
	if _, inv, _ := x.r.PurgeCaregiving(ctx); inv != 1 || count(`SELECT count(*) FROM records.caregiver_invitation`) != 0 {
		t.Fatalf("the lapsed one is purged: %d", inv)
	}
	if count(`SELECT count(*) FROM records.caregiver_link`) != 1 {
		t.Fatal("link rows are never purged")
	}
}
