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

// kiro-cli 3.2.1 sends the slash menu and the steering documents after the boot observer pass's door answered, and
// drops a push for a session closed before it. The pass stays open until both came, so the menu is ready before any
// chat opens.
func TestBootPass_StaysOpenForWhatKASSendsAfterTheDoor(t *testing.T) {
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
	var observer *fakeBridge
	for deadline := time.Now().Add(5 * time.Second); observer == nil; time.Sleep(time.Millisecond) {
		opened, open := openPasses()
		switch {
		case time.Now().After(deadline):
			t.Fatalf("%d observer passes opened and %d stayed open after 5s, want one boot pass left open", len(opened), len(open))
		case time.Now().Before(settled):
		case len(opened) > 0 && len(open) == 0:
			t.Fatal("the boot observer pass closed its session before KAS sent the slash menu and the steering documents")
		case len(open) == 1:
			observer = open[0]
		}
	}
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
