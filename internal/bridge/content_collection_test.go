package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func contentCollectionOff(context.Context) (bool, bool) { return false, true }
func contentCollectionOn(context.Context) (bool, bool)  { return true, true }

// startCapturing starts e on sessionDoorScript with eo and returns the path every request and notification is
// appended to, one JSON line each. The engine stops at cleanup.
func startCapturing(t *testing.T, e *Engine, eo *marotte.EngineOpts) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	e.cliPath = scriptPath
	t.Setenv("HOME", t.TempDir())
	capturePath := filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capturePath)
	eo.Lifetime = context.Background()
	if err := e.Start(t.Context(), eo); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(e.Stop)
	return capturePath
}

func capturedLines(t *testing.T, capturePath string) []string {
	t.Helper()
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// contentCollectionNotified returns the enabled value of every content-collection notification in wire order.
func contentCollectionNotified(t *testing.T, lines []string) []bool {
	t.Helper()
	var values []bool
	for _, line := range lines {
		if !strings.Contains(line, `"method":"`+marotte.MethodContentCollectionSettingsChanged+`"`) {
			continue
		}
		cc := digObject(t, "the contentCollection notification", line, "params", "contentCollection")
		on, ok := cc["enabled"].(bool)
		if !ok {
			t.Fatalf("contentCollection notification carries enabled %#v, want a bool:\n%s", cc["enabled"], line)
		}
		values = append(values, on)
	}
	return values
}

// notifiedOn returns the values written to w as content-collection notifications, in write order.
func notifiedOn(t *testing.T, w *captureWriter) []bool {
	t.Helper()
	w.mu.Lock()
	raw := w.buf.String()
	w.mu.Unlock()
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return contentCollectionNotified(t, strings.Split(strings.TrimSpace(raw), "\n"))
}

func TestEngineStart_InitializeCarriesContentCollection(t *testing.T) {
	for _, tc := range []struct {
		resolve func(context.Context) (bool, bool)
		name    string
		want    bool
	}{
		{name: "off", resolve: contentCollectionOff},
		{name: "on", resolve: contentCollectionOn, want: true},
		{name: "unreadable_sends_the_answer", resolve: func(context.Context) (bool, bool) { return true, false }, want: true},
		{name: "no_resolver_fails_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine("", t.TempDir())
			capturePath := startCapturing(t, e, &marotte.EngineOpts{ContentCollection: tc.resolve})
			e.Stop()
			lines := capturedLines(t, capturePath)
			cc := digObject(t, "settings.contentCollection", lines[0],
				"params", "clientCapabilities", "_meta", metaKeyKiro, "settings", "contentCollection")
			if got, ok := cc["enabled"].(bool); !ok || got != tc.want {
				t.Errorf("initialize settings.contentCollection.enabled = %#v, want %v", cc["enabled"], tc.want)
			}
			if got := contentCollectionNotified(t, lines); len(got) != 0 {
				t.Errorf("Start sent content-collection notifications %v after an initialize that carried the value", got)
			}
		})
	}
}

// A resolver that moves between the initialize write and Start returning is notified before Start returns.
func TestEngineStart_AChangeDuringStartIsNotified(t *testing.T) {
	var calls atomic.Int32
	resolve := func(context.Context) (bool, bool) { return calls.Add(1) > 1, true }
	e := NewEngine("", t.TempDir())
	capturePath := startCapturing(t, e, &marotte.EngineOpts{ContentCollection: resolve})
	e.Stop()
	lines := capturedLines(t, capturePath)
	cc := digObject(t, "settings.contentCollection", lines[0],
		"params", "clientCapabilities", "_meta", metaKeyKiro, "settings", "contentCollection")
	if cc["enabled"] != false {
		t.Fatalf("Setup: initialize carried contentCollection %v, want the first resolution, false", cc["enabled"])
	}
	if got := contentCollectionNotified(t, lines); !slices.Equal(got, []bool{true}) {
		t.Errorf("content-collection notifications during Start = %v, want [true]: the change landed after initialize", got)
	}
}

// KAS holds one value for the process, so a refresh needs no session to carry it.
func TestRefreshContentCollection_NotifiesWithNoOpenSession(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin.Store(&stdinPipe{w: w})
	b.contentCollection = contentCollectionOff
	if _, err := b.RefreshContentCollection(t.Context()); err != nil {
		t.Fatalf("RefreshContentCollection: %v", err)
	}
	if got := notifiedOn(t, w); !slices.Equal(got, []bool{false}) {
		t.Errorf("RefreshContentCollection with no session open notified %v, want [false]", got)
	}
}

func TestRefreshContentCollection_SendsOnlyAReadableValueKASHasNotConfirmed(t *testing.T) {
	unreadable := func(context.Context) (bool, bool) { return false, false }
	for _, tc := range []struct {
		live func(context.Context) (bool, bool)
		name string
		want []bool
	}{
		{name: "unreadable_document", live: unreadable},
		{name: "value_already_confirmed", live: contentCollectionOn},
		{name: "moved_value", live: contentCollectionOff, want: []bool{false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var atStart atomic.Bool
			atStart.Store(true)
			resolve := func(ctx context.Context) (bool, bool) {
				if atStart.Load() {
					return contentCollectionOn(ctx)
				}
				return tc.live(ctx)
			}
			e := NewEngine("", t.TempDir())
			capturePath := startCapturing(t, e, &marotte.EngineOpts{ContentCollection: resolve})
			atStart.Store(false)

			_, err := e.RefreshContentCollection(t.Context())
			e.Stop()

			if err != nil {
				t.Fatalf("RefreshContentCollection: %v", err)
			}
			if got := contentCollectionNotified(t, capturedLines(t, capturePath)); !slices.Equal(got, tc.want) {
				t.Errorf("RefreshContentCollection after an initialize that carried enabled notified %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRefreshContentCollection_SendsOverAnUnreadableDocumentWhenKASConfirmedNothing(t *testing.T) {
	var atStart atomic.Bool
	atStart.Store(true)
	resolve := func(ctx context.Context) (bool, bool) {
		if atStart.Load() {
			return contentCollectionOn(ctx)
		}
		return false, false
	}
	e := NewEngine("", t.TempDir())
	capturePath := startCapturing(t, e, &marotte.EngineOpts{ContentCollection: resolve})
	atStart.Store(false)
	// The state a failed notification leaves.
	e.mu.Lock()
	e.contentCollectionKnown = false
	e.mu.Unlock()

	_, err := e.RefreshContentCollection(t.Context())
	e.Stop()

	if err != nil {
		t.Fatalf("RefreshContentCollection: %v", err)
	}
	if got := contentCollectionNotified(t, capturedLines(t, capturePath)); !slices.Equal(got, []bool{false}) {
		t.Errorf("RefreshContentCollection over an unreadable document with nothing confirmed notified %v, want [false]", got)
	}
}

func TestRefreshContentCollection_AFailedNotifyLeavesTheValueUnconfirmed(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{failErr: os.ErrClosed}
	b.stdin.Store(&stdinPipe{w: w})
	b.contentCollection = contentCollectionOff
	if _, err := b.RefreshContentCollection(t.Context()); err == nil {
		t.Fatal("RefreshContentCollection over a failing pipe = nil, want the write error")
	}
	b.mu.Lock()
	known := b.contentCollectionKnown
	b.mu.Unlock()
	if known {
		t.Error("a failed notification left content collection known, so the next sync would not resend it")
	}
}

// Two refreshes race; the one that resolved later must be KAS's last write. The first resolve gives an unserialized
// second refresh 200ms to land first.
func TestRefreshContentCollection_AStaleResolutionNeverLandsLast(t *testing.T) {
	b := NewEngine("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin.Store(&stdinPipe{w: w})
	var (
		mu       sync.Mutex
		resolves int
	)
	secondResolved := make(chan struct{})
	secondDone := make(chan error, 1)
	b.contentCollection = func(context.Context) (bool, bool) {
		mu.Lock()
		resolves++
		n := resolves
		mu.Unlock()
		if n > 1 {
			close(secondResolved)
			return false, true
		}
		go func() {
			_, err := b.RefreshContentCollection(t.Context())
			secondDone <- err
		}()
		select {
		case <-secondResolved:
		case <-time.After(200 * time.Millisecond):
		}
		return true, true
	}
	if _, err := b.RefreshContentCollection(t.Context()); err != nil {
		t.Fatalf("first RefreshContentCollection: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second RefreshContentCollection: %v", err)
	}
	if got := notifiedOn(t, w); !slices.Equal(got, []bool{true, false}) {
		t.Errorf("content-collection notifications in wire order = %v, want [true false] (the newer value last)", got)
	}
}

func TestSessionDoors_SendNoContentCollection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
	}{
		{name: "new"},
		{name: "load", session: "sess_resumed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := captureEngineRequests(t, &marotte.EngineOpts{ContentCollection: contentCollectionOn},
				&marotte.SessionOpts{SessionID: tc.session})
			for line := range strings.SplitSeq(strings.TrimSpace(data), "\n") {
				var req struct {
					Params struct {
						ConfigID string `json:"configId"`
					} `json:"params"`
					Method string `json:"method"`
				}
				if err := json.Unmarshal([]byte(line), &req); err != nil {
					t.Fatalf("captured request is not JSON: %v\n%s", err, line)
				}
				if req.Method == marotte.MethodSetConfigOption && req.Params.ConfigID == "contentCollection" {
					t.Errorf("session/%s wrote contentCollection on the session: %s", tc.name, line)
				}
				if req.Method == marotte.MethodContentCollectionSettingsChanged {
					t.Errorf("session/%s re-sent the value initialize carried: %s", tc.name, line)
				}
			}
		})
	}
}
