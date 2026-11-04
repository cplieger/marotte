package git

import (
	"context"
	"testing"
	"time"
)

// newTestHandler is NewHandler with Shutdown registered as cleanup, so no status scan a test
// starts outlives it.
func newTestHandler(t *testing.T, workDir string, opts ...Option) *Handler {
	t.Helper()
	h := NewHandler(workDir, opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return h
}
