package email_test

import (
	"testing"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/email"
)

func TestNoHealthAttachments(t *testing.T) {
	m := email.Message{To: "a@example.test", TemplateKey: "receipt.paid", IdempotencyKey: "k",
		Attachments: []email.Attachment{{Kind: email.Receipt, Ref: "doc1"}}}
	if err := email.Validate(m); err != nil {
		t.Fatal(err)
	}
	m.Attachments = []email.Attachment{{Kind: "prescription", Ref: "doc1"}}
	if email.Validate(m) == nil {
		t.Fatal("a health document must never be attachable")
	}
}
