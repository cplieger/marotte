package composition

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/agent"
	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/slogx/capture"
)

// lockedBuffer is a log sink safe for the runtime's goroutines and the test reading it at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestApp_ShutdownWaitsForTheSignInVaultLast drives App.Shutdown: the vault's goroutines end with the app's
// lifetime and the runtime teardown may still ask the vault for a credential, so the wait comes after both and
// Shutdown returns only once it does.
func TestApp_ShutdownWaitsForTheSignInVaultLast(t *testing.T) {
	logs := &lockedBuffer{}
	capture.SwapDefault(t, slog.New(slog.NewTextHandler(logs, nil)))

	chats, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	entered, release := make(chan struct{}), make(chan struct{})
	var lifetimeEnded, tornDown bool
	app := &App{
		Runtime: agent.New(t.Context(), t.TempDir(), nil, chats),
		stopApp: stopApp,
		waitSignIn: func() {
			lifetimeEnded = appCtx.Err() != nil
			tornDown = strings.Contains(logs.String(), "runtime shutdown complete")
			close(entered)
			<-release
		},
	}
	done := make(chan struct{})
	go func() {
		app.Shutdown()
		close(done)
	}()

	select {
	case <-entered:
	case <-done:
		t.Fatal("App.Shutdown returned without waiting for the sign-in vault")
	case <-time.After(15 * time.Second):
		t.Fatal("App.Shutdown did not reach the sign-in vault's wait within 15s")
	}
	select {
	case <-done:
		t.Fatal("App.Shutdown returned while the sign-in vault was still saving")
	default:
	}
	close(release)
	<-done
	if !lifetimeEnded {
		t.Error("the vault wait began before the app's lifetime ended, so its goroutines were never told to stop")
	}
	if !tornDown {
		t.Error("the vault wait began before the runtime teardown finished, which may still ask it for a credential")
	}
}
