package app

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/telemetry"
)

// Operational alerts (S-178; TM-25, TM-35, TM-38, TM-39, TM-51; K-SEC-02, K-SEC-08).
//
// Every module's Alert hook ends here. Until the paging vendor and log store are chosen
// (S-095, vendor register V2), an alert is one ERROR log line with the event name
// "ops.alert" and a code in "reason"; the log pipeline must route every "ops.alert" line to
// the on-call person within minutes [FILL: alert rule in the log store, S-095]. Alerts
// carry codes and pseudonymous references only, never personal or health data (K-DP-04).
// The same code is written at most once a minute per process, so an outage cannot flood
// the log; every occurrence is still counted.

// AlertEvent is the log event name the alert rule matches on.
const AlertEvent = "ops.alert"

const alertEvery = time.Minute

var (
	reAlertCode = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	alertMu     sync.Mutex
	alertLast   = map[string]time.Time{}
	alertsTotal atomic.Int64
	// alertCounter also counts each alert as a metric once telemetry is set up (S-095).
	alertCounter atomic.Pointer[telemetry.Telemetry]

	// keyUnavailable is the answer while the key service is down (TM-39).
	keyUnavailable = func() *problem.Problem {
		p := problem.New(http.StatusServiceUnavailable, "server.key_service_unavailable", "Temporarily unavailable; try again shortly")
		p.RetryAfter = 30
		return p
	}()
)

func alertCode(code string) string {
	if !reAlertCode.MatchString(code) || len(code) > 80 {
		return "alert.invalid_code" // a caller can never smuggle free text into the line
	}
	return code
}

// raiseAlert writes one alert line (throttled per code) and counts it.
func raiseAlert(ctx context.Context, log *slog.Logger, code string, attrs ...any) {
	code = alertCode(code)
	alertsTotal.Add(1)
	if t := alertCounter.Load(); t != nil {
		t.Alert(ctx, code)
	}
	now := time.Now()
	alertMu.Lock()
	last, seen := alertLast[code]
	if !seen || now.Sub(last) >= alertEvery {
		alertLast[code] = now
		seen = false
	}
	alertMu.Unlock()
	if seen {
		return
	}
	log.ErrorContext(ctx, "ops.alert", append([]any{"reason", code}, attrs...)...) // AlertEvent; a literal for the K-DP-04 archtest
}

// wireAlerts connects every module's Alert hook to the sink. Called by BuildCore, so the
// API and the worker both raise alerts.
func (c *Core) wireAlerts(log *slog.Logger) {
	raise := func(ctx context.Context, code string) { raiseAlert(ctx, log, code) }
	c.Identity.Alert = raise
	c.Records.Alert = raise
	c.Payments.Alert = raise
	c.Audit.Alert = raise
	c.Notify.Alert = func(ctx context.Context, dispatchID, reason string) {
		code := "notifications.dead_letter"
		if reAlertCode.MatchString(reason) && len(reason) <= 40 {
			code += "." + reason
		}
		raiseAlert(ctx, log, code, "resource_ref", dispatchID)
	}
}

// AlertsRaised reports how many alerts this process raised (tests and the health page).
func AlertsRaised() int64 { return alertsTotal.Load() }
