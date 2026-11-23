package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Vendor is one entry of the vendor register (K-DP-08, adapter rule A3).
type Vendor struct {
	ID                  string   `yaml:"id"`
	Kind                string   `yaml:"kind"`
	ProcessorAgreement  bool     `yaml:"processor_agreement"`
	ProcessingCountries []string `yaml:"processing_countries"`
	Status              string   `yaml:"status"`
	// Environments limits where the vendor may run; empty means everywhere.
	Environments []string `yaml:"environments,omitempty"`
}

type vendorRegister struct {
	Vendors []Vendor `yaml:"vendors"`
}

var vendorStatuses = []string{"approved", "internal", "pending_legal", "test_only", "retired"}

const fakePrefix = "fake-"

func loadRegister(path string) (map[string]Vendor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vendor register: %w", err)
	}
	var r vendorRegister
	if err := strictDecode(data, &r); err != nil {
		return nil, fmt.Errorf("vendor register: %w", err)
	}
	out := map[string]Vendor{}
	for i, v := range r.Vendors {
		switch {
		case v.ID == "" || v.Kind == "":
			return nil, fmt.Errorf("vendor register: entry %d needs id and kind", i)
		case !isAdapterKind(v.Kind):
			return nil, fmt.Errorf("vendor register: %q has unknown kind %q", v.ID, v.Kind)
		case !slices.Contains(vendorStatuses, v.Status):
			return nil, fmt.Errorf("vendor register: %q has unknown status %q", v.ID, v.Status)
		}
		if _, dup := out[v.ID]; dup {
			return nil, fmt.Errorf("vendor register: duplicate id %q", v.ID)
		}
		for _, e := range v.Environments {
			if !Env(e).valid() {
				return nil, fmt.Errorf("vendor register: %q lists unknown environment %q", v.ID, e)
			}
		}
		out[v.ID] = v
	}
	return out, nil
}

// validateAdapters enforces country-pack-model section 3 rule 4 and adapter rule A3. It
// returns warnings (lenient environments only).
func validateAdapters(m *manifest, reg map[string]Vendor, env Env, add func(string, ...any)) []string {
	var warnings []string

	whatsappRegistered := false
	for _, v := range reg {
		if v.Kind == "whatsapp" {
			whatsappRegistered = true
		}
	}
	if !whatsappRegistered {
		// WhatsApp is ON HOLD (founder, 14 Nov 2026): nothing may be built against a provider.
		if m.Messaging.ChannelsEnabled["whatsapp"] {
			add("messaging.channels_enabled.whatsapp is true but WhatsApp is on hold and the vendor register has no WhatsApp vendor")
		}
		if id := m.Adapters["whatsapp"]; id != none {
			add("adapters.whatsapp is %q but WhatsApp is on hold: it must be %q", id, none)
		}
	}

	for _, kind := range adapterKinds {
		id := m.Adapters[kind]
		if id == none || id == "" {
			continue
		}
		if strings.HasPrefix(id, "[") {
			if !env.lenient() {
				add("adapters.%s: %q is not a vendor (unfinished value)", kind, id)
			}
			warnings = append(warnings, fmt.Sprintf("adapter %s is not chosen yet (%s); treated as none", kind, id))
			continue
		}
		v, ok := reg[id]
		if !ok {
			add("adapters.%s: vendor %q is not in the vendor register (K-DP-08)", kind, id)
			continue
		}
		if v.Kind != kind {
			add("adapters.%s: vendor %q is registered as kind %q", kind, id, v.Kind)
			continue
		}
		if v.Status == "retired" {
			add("adapters.%s: vendor %q is retired", kind, id)
			continue
		}
		if strings.HasPrefix(id, fakePrefix) && !env.lenient() {
			add("adapters.%s: fake adapter %q may not run in %s", kind, id, env)
			continue
		}
		if len(v.Environments) > 0 && !slices.Contains(v.Environments, string(env)) {
			add("adapters.%s: vendor %q may not run in %s (allowed: %s)", kind, id, env, strings.Join(v.Environments, ", "))
			continue
		}
		if strings.HasPrefix(id, fakePrefix) {
			continue // synthetic data only in lenient environments
		}
		if vendorApproved(m, v) {
			continue
		}
		if env.lenient() {
			warnings = append(warnings, fmt.Sprintf("adapter %s: vendor %q lacks a processor agreement or an allowed processing country or an approved exception; usable with synthetic data only", kind, id))
			continue
		}
		add("adapters.%s: vendor %q has no processor agreement, processes outside %v, and has no approved residency exception (K-DP-08, K-DP-01)",
			kind, id, m.Residency.VendorProcessingMustBeIn)
	}
	return warnings
}

// vendorApproved: processor agreement in place and every processing country allowed, or
// an exception for the vendor with a named approver.
func vendorApproved(m *manifest, v Vendor) bool {
	for _, e := range m.Residency.Exceptions {
		if e.Vendor == v.ID && e.ApprovedBy != nil && strings.TrimSpace(*e.ApprovedBy) != "" {
			return true
		}
	}
	if !v.ProcessorAgreement {
		return false
	}
	for _, c := range v.ProcessingCountries {
		if !slices.Contains(m.Residency.VendorProcessingMustBeIn, c) {
			return false
		}
	}
	return true
}
