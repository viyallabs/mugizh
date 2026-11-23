//go:build dast || load

package app_test

import (
	"context"
	"os"
	"time"
)

// serveUntilStopped keeps a DAST or load target up until ctx ends or the script creates
// /tmp/stop. Returning normally lets the test framework remove its database container
// (Ryuk is disabled); a killed target used to leave it running.
func serveUntilStopped(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := os.Stat("/tmp/stop"); err == nil {
				return
			}
		}
	}
}
