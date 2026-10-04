package composition

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestApp_ShutdownStopsTheForgeKeeper drives App.Shutdown, the function
// production calls, because Build is handed context.Background() and only the
// stop function the App holds can end the keeper's loop.
func TestApp_ShutdownStopsTheForgeKeeper(t *testing.T) {
	var exited atomic.Bool
	started := make(chan struct{})
	app := &App{
		stopForgeKeeper: runBackground(context.Background(), "forge credential keeper",
			func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				exited.Store(true)
			}),
	}
	<-started

	app.Shutdown()

	if !exited.Load() {
		t.Error("the forge credential keeper was still running after App.Shutdown returned; " +
			"it would keep refreshing tokens into a store the process is leaving")
	}
}
