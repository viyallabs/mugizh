package push_test

import (
	"strings"
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
)

func TestValidateDataIsOpaque(t *testing.T) {
	m := push.Message{Target: push.Target{Token: "t", Platform: push.Android}, TitleKey: "notif.title", Body: "Hi", IdempotencyKey: "k",
		Data: map[string]string{"screen": "booking", "ref": "0192-abc"}}
	if err := push.Validate(m); err != nil {
		t.Fatal(err)
	}
	m.Data = map[string]string{"note": "free text with spaces"}
	if push.Validate(m) == nil {
		t.Fatal("free text in data must be refused")
	}
}

// S-165: a silent message carries data only; a replace key is a short opaque token.
func TestValidateSilentAndReplaceKey(t *testing.T) {
	tg := push.Target{Token: "t", Platform: push.IOS}
	ok := push.Message{Target: tg, Silent: true, IdempotencyKey: "k", Data: map[string]string{"sync": "reminders"}}
	if err := push.Validate(ok); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]push.Message{
		"silent with body":    {Target: tg, Silent: true, Body: "Time for your medicine", IdempotencyKey: "k", Data: map[string]string{"sync": "reminders"}},
		"silent with title":   {Target: tg, Silent: true, TitleKey: "notif.title", IdempotencyKey: "k", Data: map[string]string{"sync": "reminders"}},
		"silent without data": {Target: tg, Silent: true, IdempotencyKey: "k"},
		"replace key text":    {Target: tg, TitleKey: "notif.title", Body: "Hi", IdempotencyKey: "k", ReplaceKey: "Metformin 500"},
		"replace key long":    {Target: tg, TitleKey: "notif.title", Body: "Hi", IdempotencyKey: "k", ReplaceKey: strings.Repeat("a", 65)},
	} {
		if push.Validate(m) == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	good := push.Message{Target: tg, TitleKey: "notif.title", Body: "Hi", IdempotencyKey: "k", ReplaceKey: "rem-29881234-c"}
	if err := push.Validate(good); err != nil {
		t.Fatal(err)
	}
}
