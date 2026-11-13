package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// heldBootPass starts a runtime whose observer doors deliver no slash menu or steering documents, and returns it with
// the boot observer pass, which stays open for them under a 10 s bound.
func heldBootPass(t *testing.T) (*Runtime, *fakeBridge) {
	t.Helper()
	old := bootSeedTimeout
	bootSeedTimeout = 10 * time.Second
	t.Cleanup(func() { bootSeedTimeout = old })
	src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
	var mu sync.Mutex
	var passes []*fakeBridge
	src.setObserver(func() engineSession {
		b := newFakeBridge()
		b.account, b.noDoorSeeds = &src.account, true
		mu.Lock()
		passes = append(passes, b)
		mu.Unlock()
		return b
	})
	// The boot's other observer passes read and close at once; the boot pass is the one left open.
	openPasses := func() (opened, open []*fakeBridge) {
		mu.Lock()
		defer mu.Unlock()
		for _, b := range passes {
			if b.startCount() > 0 {
				opened = append(opened, b)
				if !sessionClosed(b) {
					open = append(open, b)
				}
			}
		}
		return opened, open
	}
	cs := newTestChatStore()
	// newRuntimeOn without its awaitBoot, which would wait out the pass this test holds open.
	h := New(context.Background(), t.TempDir(), src, cs)
	src.mu.Lock()
	src.rt = h
	src.mu.Unlock()
	h.runs.cancelRetryBase = time.Hour
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })

	settled := time.Now().Add(300 * time.Millisecond)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		opened, open := openPasses()
		switch {
		case time.Now().After(deadline):
			t.Fatalf("%d observer passes opened and %d stayed open after 5s, want one boot pass left open", len(opened), len(open))
		case time.Now().Before(settled):
		case len(opened) > 0 && len(open) == 0:
			t.Fatal("the boot observer pass closed its session before KAS sent the slash menu and the steering documents")
		case len(open) == 1:
			return h, open[0]
		}
	}
}

// kiro-cli 3.2.1 sends the slash menu and the steering documents after the boot observer pass's door answered, and
// drops a push for a session closed before it. The pass stays open until both came, so the menu is ready before any
// chat opens.
func TestBootPass_StaysOpenForWhatKASSendsAfterTheDoor(t *testing.T) {
	h, observer := heldBootPass(t)
	observer.deliver(availableCommandsFrame(t))
	observer.deliver(&marotte.RPCResponse{Method: methodV3SteeringDocs, Params: mustJSON(t, map[string]any{
		"sessionId": string(observer.SessionID()), "status": "success", "documents": []any{},
	})})
	settleBoot(t, h)

	if !sessionClosed(observer) {
		t.Error("the boot observer pass left its session open after both arrived")
	}
	rec := httptest.NewRecorder()
	h.handleSlashCommands(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/slash-commands", http.NoBody))
	if body := rec.Body.String(); !strings.Contains(body, `"ready":true`) || !strings.Contains(body, `"review"`) {
		t.Errorf("GET /api/slash-commands = %s, want the boot pass's menu, ready", body)
	}
}

// A failed steering read is the documents_changed frame KAS sends at that door (SteeringManager.emitDocumentsFailed),
// and no success frame follows it, so the pass closes on it rather than waiting out its bound.
func TestBootPass_ClosesOnAFailedSteeringFrame(t *testing.T) {
	logs := captureLogs(t)
	h, observer := heldBootPass(t)
	observer.deliver(availableCommandsFrame(t))
	observer.deliver(&marotte.RPCResponse{Method: methodV3SteeringDocs, Params: mustJSON(t, map[string]any{
		"sessionId": string(observer.SessionID()), "status": "failed", "error": "EACCES: permission denied",
	})})
	for deadline := time.Now().Add(3 * time.Second); !sessionClosed(observer); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the boot observer pass held its session 3s after KAS sent a failed steering frame")
		}
	}
	settleBoot(t, h)

	if out := logs.String(); strings.Contains(out, "no steering documents") {
		t.Errorf("logs = %s, want no WARN for steering documents KAS answered", out)
	}
	if out := logs.String(); !strings.Contains(out, `"msg":"steering documents read failed"`) || !strings.Contains(out, "EACCES: permission denied") {
		t.Errorf("logs = %s, want a WARN naming the failed read's error", out)
	}
	if got := h.steeringIssues.snapshot(); len(got) != 0 {
		t.Errorf("steering issues = %+v, want none from a failed frame", got)
	}
}
