package composition

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/toolbelt/v3"
)

func TestWireToolsSteering_StopWaitsForTheBootRegeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	down := newToolsSlot(filepath.Join(t.TempDir(), toolsManifestName), func() (*toolbelt.Engine, error) {
		return nil, errToolsDirUnusable
	})
	steer := steering.New(t.TempDir(), t.TempDir(), "")
	entered, release := make(chan struct{}), make(chan struct{})
	steer.SetMCPSnapshot(func() steering.MCPSnapshot {
		close(entered)
		<-release
		return steering.MCPSnapshot{}
	})

	stop := wireToolsSteering(context.WithoutCancel(t.Context()), down, steer)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the boot regeneration did not start within 10s")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		close(release)
		t.Fatal("stop returned while the boot regeneration was still rendering")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not return within 10s of the regeneration finishing")
	}
}

// TestApp_ShutdownStopsToolsSteeringBeforeClosingTheEngine drives App.Shutdown: the boot
// regeneration reads the engine's inventory, so its wait must come before the engine closes.
func TestApp_ShutdownStopsToolsSteeringBeforeClosingTheEngine(t *testing.T) {
	tools := newToolsSlot(filepath.Join(t.TempDir(), toolsManifestName), func() (*toolbelt.Engine, error) {
		return nil, errToolsDirUnusable
	})
	var called, closedFirst bool
	app := &App{
		tools: tools,
		stopToolsSteering: func() {
			called = true
			tools.mu.Lock()
			closedFirst = tools.closed
			tools.mu.Unlock()
		},
	}

	app.Shutdown()

	if !called {
		t.Fatal("App.Shutdown never stopped the tools steering regeneration")
	}
	if closedFirst {
		t.Error("App.Shutdown closed the tools engine before the steering regeneration that reads it had stopped")
	}
}
