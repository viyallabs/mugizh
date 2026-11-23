package app

import (
	"errors"
	"time"

	"go.yaml.in/yaml/v3"
)

// policyWindows reads the booking-related windows from the pack's refund policy file
// (free cancellation notice, free reschedules, grace and join timeouts). The refund
// engine itself lives in payments; booking needs only these numbers.
type policyWindows struct {
	Version string `yaml:"version"`
	Windows struct {
		FreeCancelBeforeMinutes      int `yaml:"free_cancel_before_minutes"`
		FreeReschedules              int `yaml:"free_reschedules"`
		PatientGraceMinutes          int `yaml:"patient_grace_minutes"`
		DoctorJoinTimeoutMinutes     int `yaml:"doctor_join_timeout_minutes"`
		PendingPaymentTimeoutMinutes int `yaml:"pending_payment_timeout_minutes"`
	} `yaml:"windows"`
}

func parseWindows(b []byte) (policyWindows, error) {
	var p policyWindows
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, err
	}
	w := p.Windows
	if p.Version == "" || w.FreeCancelBeforeMinutes <= 0 || w.PatientGraceMinutes <= 0 || w.DoctorJoinTimeoutMinutes <= 0 || w.PendingPaymentTimeoutMinutes <= 0 {
		return p, errors.New("refund policy: windows are incomplete (fail closed)")
	}
	return p, nil
}

func (p policyWindows) holdTTL() time.Duration {
	return time.Duration(p.Windows.PendingPaymentTimeoutMinutes) * time.Minute
}
