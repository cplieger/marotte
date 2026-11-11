package composition

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/slogx/capture"
	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
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
		steer := steering.New(steering.Config{WorkDir: t.TempDir(), ConfigDir: t.TempDir()})
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

// TestApp_ShutdownStopsTheToolsEngineAfterTheRuntimeAndSignIn drives App.Shutdown: the boot
// regeneration may wait in Inventory, which takes no ctx, so the runtime teardown and the sign-in
// vault's save come first, and a job event the engine still broadcasts is dropped.
func TestApp_ShutdownStopsTheToolsEngineAfterTheRuntimeAndSignIn(t *testing.T) {
	logs := &lockedBuffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(logs, nil)))

	chats, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	var signInWaited, stopped, tornDownFirst, signInFirst bool
	app := &App{
		Runtime:    agent.New(t.Context(), t.TempDir(), nil, chats),
		waitSignIn: func() { signInWaited = true },
	}
	app.stopToolsSteering = func() {
		stopped = true
		tornDownFirst = strings.Contains(logs.String(), "runtime shutdown complete")
		signInFirst = signInWaited
		app.Runtime.Broadcast(t.Context(), marotte.NewEvent(marotte.EventToolJobChanged, "",
			marotte.ToolJobChangedPayload{Job: &toolbelt.Job{ID: "late", State: toolbelt.JobCancelled}}))
	}

	app.Shutdown()

	if !stopped {
		t.Fatal("App.Shutdown never stopped the tools steering regeneration")
	}
	if !tornDownFirst {
		t.Error("the tools steering stop ran before the runtime teardown, so a hung probe could cut it short")
	}
	if !signInFirst {
		t.Error("the tools steering stop ran before the sign-in vault's wait, so a hung probe could cut its save")
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Errorf("a tool job event broadcast after the runtime teardown logged an error:\n%s", out)
	}
}
