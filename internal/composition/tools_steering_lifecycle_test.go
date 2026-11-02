package composition

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/steering"
	"github.com/cplieger/toolbelt/v3"
)

// The regeneration is held past runBackground's grace, which the bubble's clock passes without
// waiting: stop must still be waiting then, and its cancel must stop the write.
func TestWireToolsSteering_StopCancelsAndJoinsTheBootRegeneration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	synctest.Test(t, func(t *testing.T) {
		down := newToolsSlot(filepath.Join(t.TempDir(), toolsManifestName), func() (*toolbelt.Engine, error) {
			return nil, errToolsDirUnusable
		})
		steer := steering.New(t.TempDir(), t.TempDir(), "")
		release := make(chan struct{})
		steer.SetMCPSnapshot(func() steering.MCPSnapshot {
			<-release
			return steering.MCPSnapshot{}
		})

		stop := wireToolsSteering(t.Context(), down, steer)
		synctest.Wait()
		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		time.Sleep(2 * backgroundStopGrace)
		select {
		case <-stopped:
			t.Error("stop returned while the boot regeneration was still reading its snapshots")
		default:
		}
		close(release)
		<-stopped
	})
	if _, err := os.Stat(filepath.Join(home, ".kiro", "steering", "environment.md")); !os.IsNotExist(err) {
		t.Errorf("the regeneration stop cancelled still wrote environment.md (stat error %v)", err)
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
