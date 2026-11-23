// Package sms is the SMS adapter port (adapter-interfaces §5, K-MSG-01): only templates
// from the country pack's DLT registry, with typed variables, can be sent. Free text is
// impossible by construction. Vendor implementations live in sms/<vendor>/.
package sms

import (
	"context"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Var is one template variable.
type Var struct {
	Name  string
	Value string
}

// Message is a request to send one registered template.
type Message struct {
	To             string // E.164
	Template       string // registry ID, e.g. "SMS-01"
	Vars           []Var
	IdempotencyKey string
}

// Sender sends messages. Implementations add the Principal Entity ID, header and DLT
// template ID from configuration, and never log variables (K-DP-04).
type Sender interface {
	Send(ctx context.Context, m Message) (providerMessageID string, err error)
}

// Template is one registry entry.
type Template struct {
	ID        string
	Category  string
	Variables []TemplateVar
}

// TemplateVar is a typed variable.
type TemplateVar struct {
	Name string
	Type string // numeric | alphanumeric
}

// Registry is the parsed DLT template registry from the country pack.
type Registry struct{ byID map[string]Template }

type regFile struct {
	Templates []struct {
		ID        string `yaml:"id"`
		Category  string `yaml:"category"`
		Variables []struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
		} `yaml:"variables"`
	} `yaml:"templates"`
}

// ParseRegistry parses the pack's sms-templates file.
func ParseRegistry(b []byte) (*Registry, error) {
	var f regFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("sms: registry: %w", err)
	}
	r := &Registry{byID: map[string]Template{}}
	for _, t := range f.Templates {
		tt := Template{ID: t.ID, Category: t.Category}
		for _, v := range t.Variables {
			if v.Type != "numeric" && v.Type != "alphanumeric" {
				return nil, fmt.Errorf("sms: template %s variable %s: unknown type %q", t.ID, v.Name, v.Type)
			}
			tt.Variables = append(tt.Variables, TemplateVar{Name: v.Name, Type: v.Type})
		}
		if len(tt.Variables) > 3 { // TCCCPR Third Amendment: at most 3 variables
			return nil, fmt.Errorf("sms: template %s has more than 3 variables", t.ID)
		}
		r.byID[t.ID] = tt
	}
	return r, nil
}

var (
	reNumeric = regexp.MustCompile(`^[0-9]{1,10}$`)
	// Alphanumeric variables: letters, digits, spaces and light punctuation. No links,
	// e-mail addresses or long digit runs (phone numbers are fixed text, never variables).
	reAlnum  = regexp.MustCompile(`^[\p{L}\p{N} .,:/'()\-]{1,30}$`)
	reDigits = regexp.MustCompile(`[0-9]{7,}`)
)

// Validate checks that m uses a registered template with exactly its typed variables.
func (r *Registry) Validate(m Message) error {
	t, ok := r.byID[m.Template]
	if !ok {
		return adapters.E(adapters.Invalid, "TEMPLATE_NOT_REGISTERED")
	}
	if len(m.Vars) != len(t.Variables) {
		return adapters.E(adapters.Invalid, "VARIABLE_COUNT")
	}
	for i, v := range m.Vars {
		want := t.Variables[i]
		if v.Name != want.Name {
			return adapters.E(adapters.Invalid, "VARIABLE_NAME")
		}
		switch want.Type {
		case "numeric":
			if !reNumeric.MatchString(v.Value) {
				return adapters.E(adapters.Invalid, "VARIABLE_TYPE")
			}
		default:
			if !reAlnum.MatchString(v.Value) || reDigits.MatchString(v.Value) || scrub.HasLinkOrMarkup(v.Value) {
				return adapters.E(adapters.Invalid, "VARIABLE_TYPE")
			}
		}
	}
	return nil
}

// Category returns a template's DLT category (service_implicit, promotional, ...).
func (r *Registry) Category(id string) (string, bool) { t, ok := r.byID[id]; return t.Category, ok }
