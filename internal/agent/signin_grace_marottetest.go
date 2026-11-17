//go:build marotte_test

package agent

import (
	"log/slog"
	"os"
	"time"
)

// MAROTTE_TEST_SIGNIN_GRACE shortens signInGrace so a browser check of the failing banner need not wait two minutes.
func init() {
	raw := os.Getenv("MAROTTE_TEST_SIGNIN_GRACE")
	if raw == "" {
		return
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Error("MAROTTE_TEST_SIGNIN_GRACE is not a positive duration; keeping the default", "value", raw)
		return
	}
	signInGrace = d
}
