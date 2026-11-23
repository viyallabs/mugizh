package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
)

var (
	reDays     = regexp.MustCompile(`^days_([0-9]{1,4})`)
	reYears    = regexp.MustCompile(`^years_([0-9]{1,2})`)
	reFromDate = regexp.MustCompile(`_from_([0-9]{4})_([0-9]{2})_([0-9]{2})$`)
)

// ret18 reads the legal-minimum hold from the pack's RET-18 rule (data-inventory RET-18,
// review R-01): registration data N days after closure; from a given date, processing
// data for M years. Anything it cannot read fails start-up (no guessed legal periods).
func ret18(rules map[string]map[string]any) (registration, processing time.Duration, from time.Time, err error) {
	r, ok := rules["RET-18"]
	if !ok {
		return 0, 0, time.Time{}, errors.New("retention: RET-18 missing from the country pack")
	}
	reg, _ := r["registration_data"].(string)
	m := reDays.FindStringSubmatch(reg)
	if m == nil {
		return 0, 0, time.Time{}, fmt.Errorf("retention: RET-18 registration_data %q not understood", reg)
	}
	d, _ := strconv.Atoi(m[1])
	registration = time.Duration(d) * 24 * time.Hour
	for k, v := range r {
		dm := reFromDate.FindStringSubmatch(k)
		if dm == nil {
			continue
		}
		s, _ := v.(string)
		ym := reYears.FindStringSubmatch(s)
		if ym == nil {
			return 0, 0, time.Time{}, fmt.Errorf("retention: RET-18 %s value %q not understood", k, s)
		}
		y, _ := strconv.Atoi(ym[1])
		processing = time.Duration(y) * 365 * 24 * time.Hour
		from, err = time.Parse("2006-01-02", dm[1]+"-"+dm[2]+"-"+dm[3])
		if err != nil {
			return 0, 0, time.Time{}, err
		}
	}
	if processing == 0 {
		return 0, 0, time.Time{}, errors.New("retention: RET-18 processing-data hold not found")
	}
	return registration, processing, from, nil
}

// rightsNotifier tells the person, through the notifications module, that an erasure was
// requested, cancelled or completed (TC-63: notice to the phone on file). Push is used for
// request and cancellation (no registered SMS template exists for them yet [FILL: DLT
// template, S-102]); completion also uses SMS-13.
type rightsNotifier struct{ n *notifications.Module }

func (r rightsNotifier) NoticeToPhoneOnFile(ctx context.Context, subjectRef, kind, requestRef string) error {
	key := map[string]string{"erasure_requested": "rights.erasure_requested", "erasure_cancelled": "rights.erasure_cancelled",
		"erasure_completed": "rights.completed"}[kind]
	if key == "" {
		return fmt.Errorf("rights notice: unknown kind %q", kind)
	}
	_, err := r.n.Send(ctx, np.Request{TemplateKey: key, RecipientRef: subjectRef, Priority: np.Critical, Category: np.Service,
		Reason: "data_rights", DedupeKey: kind + ":" + requestRef, Vars: []np.Var{{Name: "reference", Type: np.Alphanumeric, Value: shortRef(requestRef)}}})
	return err
}

// shortRef is the human-facing request reference (first block of the UUID).
func shortRef(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func rightsTemplates() []notifications.Template {
	push, smsCh := notifications.Push, notifications.SMS
	ref := []notifications.VarSpec{{Name: "reference", Type: np.Alphanumeric}}
	// Account messages: the data-rights requester may be a patient or a professional, so every app of the account.
	everyApp := func(ts []notifications.Template) []notifications.Template {
		for i := range ts {
			ts[i].Audience = notifications.AudienceAny
		}
		return ts
	}
	return everyApp([]notifications.Template{
		{Key: "rights.erasure_requested", MessageType: "security", Category: np.Service, Priority: np.Critical, QuietExempt: true,
			Channels: []notifications.Channel{push}, Vars: ref, TitleKey: "notif.security.title", BodyKey: "notif.rights.erasure_requested", LinkScreen: "privacy"},
		{Key: "rights.erasure_cancelled", MessageType: "security", Category: np.Service, Priority: np.Critical, QuietExempt: true,
			Channels: []notifications.Channel{push}, Vars: ref, TitleKey: "notif.security.title", BodyKey: "notif.rights.erasure_cancelled", LinkScreen: "privacy"},
		{Key: "rights.completed", MessageType: "security", Category: np.Service, Priority: np.Normal,
			Channels: []notifications.Channel{push, smsCh}, Vars: ref, TitleKey: "notif.security.title", BodyKey: "notif.rights.completed", LinkScreen: "privacy",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-13", Vars: []notifications.SMSVar{{Name: "reference", Source: "var:reference"}}}},
	})
}
