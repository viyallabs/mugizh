package app

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config"
	cpub "gitlab.com/viyallabs/mugizh/backend/internal/modules/config/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
)

// taxDocuments is the part of the pack's tax configuration that patient documents use (S-134).
type taxDocuments struct {
	Status   string `yaml:"status"`
	Receipts struct {
		Registered   string `yaml:"registered_clinic"`
		Unregistered string `yaml:"unregistered_clinic"`
	} `yaml:"receipts"`
	FYStartMonth int    `yaml:"financial_year_start_month"`
	RegIDPattern string `yaml:"registration_id_pattern"`
}

// documentConfig builds the payments document configuration from the pack's tax file. In
// production documents are issued only from a tax configuration the CA approved (status
// "approved"); elsewhere a draft is used so the flow can be tested. A nil result means no
// documents are issued (the patient API answers "not available"), never a guessed format.
func documentConfig(s Settings, pack *cpub.Pack, validators []string) (*payments.DocumentConfig, error) {
	var t taxDocuments
	if err := yaml.Unmarshal(pack.Money().Tax, &t); err != nil {
		return nil, fmt.Errorf("tax configuration: %w", err)
	}
	if s.Env == config.EnvProduction && t.Status != "approved" {
		return nil, nil // money-flow section 5: CA sign-off first
	}
	if t.Receipts.Registered == "" || t.Receipts.Unregistered == "" || t.FYStartMonth == 0 || t.RegIDPattern == "" {
		if t.Status == "approved" {
			return nil, errors.New("tax configuration: receipts, financial_year_start_month and registration_id_pattern are required")
		}
		return nil, nil
	}
	re, err := regexp.Compile(t.RegIDPattern)
	if err != nil {
		return nil, fmt.Errorf("tax configuration: registration_id_pattern: %w", err)
	}
	zone, err := time.LoadLocation(pack.Locale().DefaultTimeZone)
	if err != nil {
		return nil, fmt.Errorf("tax configuration: zone: %w", err)
	}
	return &payments.DocumentConfig{RegisteredType: t.Receipts.Registered, UnregisteredType: t.Receipts.Unregistered,
		FYStartMonth: time.Month(t.FYStartMonth), TaxIDPattern: re, Zone: zone, ProhibitedValidators: validators}, nil
}
