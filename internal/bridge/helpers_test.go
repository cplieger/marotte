package bridge

import (
	"bytes"
	"log"
	"log/slog"
	"testing"
)

// captureLogs swaps the slog default to a buffer-backed debug handler for the test and restores it; not for parallel
// tests. The log package's writer and flags are restored too, since slog.SetDefault redirects log and does not
// restore it for the stock handler.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}
