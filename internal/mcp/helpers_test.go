package mcp

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/cplieger/slogx/capture"
)

// captureSlog redirects slog.Default to an in-memory Debug buffer for the test and restores it on
// cleanup, for tests whose only observable effect is a log line.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}
