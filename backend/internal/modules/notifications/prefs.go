package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// ErrInvalidPreferences is returned for unknown message types, channels or zones.
var ErrInvalidPreferences = errors.New("notifications: invalid preferences")

// Preferences of one user.
type Preferences struct {
	// Channels maps a message type to the channels the patient allows for it.
	Channels          map[string][]Channel
	Discreet          bool // never show medicine names, even with P-MEDNAMES
	QuietHoursEnabled bool
	TimeZone          string // IANA
	Locale            string
}

func (m *Module) defaults() Preferences {
	return Preferences{Channels: map[string][]Channel{}, QuietHoursEnabled: true, TimeZone: m.cfg.DefaultTimeZone, Locale: m.cfg.DefaultLocale}
}

// Preferences returns the user's preferences, or the defaults.
func (m *Module) Preferences(ctx context.Context, userID string) (Preferences, error) {
	if !ids.Valid(userID) {
		return Preferences{}, ErrInvalidPreferences
	}
	p := m.defaults()
	var raw []byte
	err := m.pool.QueryRow(ctx, `SELECT channels, discreet, quiet_hours_enabled, time_zone, locale FROM preference WHERE user_ref=$1`, userID).
		Scan(&raw, &p.Discreet, &p.QuietHoursEnabled, &p.TimeZone, &p.Locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return Preferences{}, err
	}
	p.Channels = map[string][]Channel{}
	if err := json.Unmarshal(raw, &p.Channels); err != nil {
		return Preferences{}, err
	}
	return p, nil
}

// SetPreferences validates and stores the user's preferences.
func (m *Module) SetPreferences(ctx context.Context, userID string, p Preferences) error {
	if !ids.Valid(userID) {
		return ErrInvalidPreferences
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil || p.TimeZone == "Local" || p.TimeZone == "" {
		return ErrInvalidPreferences
	}
	if !slices.Contains(m.cfg.SupportedLocales, p.Locale) {
		return ErrInvalidPreferences
	}
	clean := map[string][]Channel{}
	for typ, chs := range p.Channels {
		if !m.types[typ] {
			return ErrInvalidPreferences
		}
		var out []Channel
		for _, c := range chs {
			switch c {
			case Push, SMS, Email:
			default:
				return ErrInvalidPreferences // includes whatsapp
			}
			if c == Email && !emailTypes[typ] {
				return ErrInvalidPreferences
			}
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		clean[typ] = out
	}
	raw, _ := json.Marshal(clean)
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO preference (user_ref, channels, discreet, quiet_hours_enabled, time_zone, locale, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_ref) DO UPDATE SET channels=$2, discreet=$3, quiet_hours_enabled=$4, time_zone=$5, locale=$6, updated_at=$7`,
			userID, raw, p.Discreet, p.QuietHoursEnabled, p.TimeZone, p.Locale, now); err != nil {
			return err
		}
		// Discreet mode, quiet hours, zone and language all shape the on-device schedule (S-165).
		return requestSync(ctx, tx, userID, now)
	})
}

// plan returns the ordered channels for a message: the template's order, limited to the
// channels the patient allowed for this type (default: everything except SMS) plus SMS for
// critical items (TC-40, decided on the server), and to channels the pack enables. WhatsApp
// never appears.
func (m *Module) plan(t Template, p Preferences, critical bool) []Channel {
	allowed, explicit := p.Channels[t.MessageType]
	var out []Channel
	for _, c := range t.Channels {
		if c == WhatsApp || !m.cfg.ChannelsEnabled[string(c)] {
			continue
		}
		ok := false
		switch {
		case c == SMS && critical:
			ok = true
		case explicit:
			ok = slices.Contains(allowed, c)
		default:
			ok = c != SMS
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// inQuiet reports whether t falls in quiet hours in loc, and when the window ends.
func (m *Module) inQuiet(t time.Time, loc *time.Location) (bool, time.Time) {
	l := t.In(loc)
	min := l.Hour()*60 + l.Minute()
	var in bool
	if m.qS > m.qE {
		in = min >= m.qS || min < m.qE
	} else {
		in = min >= m.qS && min < m.qE
	}
	if !in {
		return false, time.Time{}
	}
	end := time.Date(l.Year(), l.Month(), l.Day(), m.qE/60, m.qE%60, 0, 0, loc)
	if !end.After(l) {
		end = time.Date(l.Year(), l.Month(), l.Day()+1, m.qE/60, m.qE%60, 0, 0, loc)
	}
	return true, end.UTC()
}

var _ = public.Normal
