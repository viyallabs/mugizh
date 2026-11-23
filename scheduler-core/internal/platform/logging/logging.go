// Package logging builds the JSON slog logger. Messages are constant event names; attributes whose key
// looks like a secret are redacted so a careless call site cannot leak a token or the HMAC secret (K-DP-04).
package logging

import (
	"io"
	"log/slog"
	"strings"
)

var secretKeys = []string{"token", "secret", "authorization", "password", "signature", "dsn"}

// New returns a JSON logger writing to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			k := strings.ToLower(a.Key)
			for _, s := range secretKeys {
				if strings.Contains(k, s) {
					return slog.String(a.Key, "[redacted]")
				}
			}
			return a
		},
	}))
}
