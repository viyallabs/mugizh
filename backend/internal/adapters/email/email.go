// Package email is the email adapter port (adapter-interfaces §8): receipts, subscription
// invoices and staff alerts. Health documents are NEVER attached; a health email carries an
// in-app link only. The attachment kinds below make a health attachment unrepresentable.
// Vendor implementations live in email/<vendor>/.
package email

import (
	"context"
	"regexp"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
)

// AttachmentKind is the only kind of file an email may carry.
type AttachmentKind string

// Allowed attachment kinds: financial documents only.
const (
	Receipt AttachmentKind = "receipt"
	Invoice AttachmentKind = "invoice"
)

// Attachment refers to a stored financial document.
type Attachment struct {
	Kind AttachmentKind
	Ref  string // opaque document reference
}

// Var is one template variable.
type Var struct{ Name, Value string }

// Message is one email from a registered template.
type Message struct {
	To             string
	TemplateKey    string
	Locale         string
	Vars           []Var
	Attachments    []Attachment
	IdempotencyKey string
}

// Sender sends email. Implementations never log the address or variables (K-DP-04).
type Sender interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
}

var (
	reTemplate = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,80}$`)
	reRef      = regexp.MustCompile(`^[A-Za-z0-9_\-.]{1,64}$`)
)

// Validate checks the message shape and that no attachment is anything but a receipt or invoice.
func Validate(m Message) error {
	if m.To == "" || !reTemplate.MatchString(m.TemplateKey) || m.IdempotencyKey == "" {
		return adapters.E(adapters.Invalid, "MESSAGE")
	}
	for _, a := range m.Attachments {
		if (a.Kind != Receipt && a.Kind != Invoice) || !reRef.MatchString(a.Ref) {
			return adapters.E(adapters.Invalid, "ATTACHMENT")
		}
	}
	return nil
}
