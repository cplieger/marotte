package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
)

// The sets are independent so a test can stage a configured-but-disabled name; enabledConfig builds
// the nested shape.
type fakeMCPConfig struct {
	enabled    map[string]struct{}
	configured map[string]struct{}
	mu         sync.Mutex
}

func (f *fakeMCPConfig) EnabledNames(_ context.Context) map[string]struct{} {
	return f.copyOf(f.enabled)
}

func (f *fakeMCPConfig) ConfiguredNames(_ context.Context) map[string]struct{} {
	return f.copyOf(f.configured)
}

func (f *fakeMCPConfig) copyOf(src map[string]struct{}) map[string]struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]struct{}, len(src))
	for k := range src {
		out[k] = struct{}{}
	}
	return out
}

func newHubWithMCPConfig(t testing.TB, cfg mcpNameSets) *Runtime {
	t.Helper()
	cs := newTestChatStore()
	factory := func() engineSession { return newFakeBridge() }
	var opts []Option
	if cfg != nil {
		opts = append(opts, WithMCPConfig(cfg))
	}
	h := newRuntimeOn(context.Background(), "/tmp/work", fakeEngineOf(factory, nil), cs, opts...)
	cs.wire(h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	return h
}

// reportMCP records one session's whole _kiro/mcp/status frame.
func reportMCP(t testing.TB, h *Runtime, sid marotte.SessionID, servers ...translate.MCPServerReport) {
	t.Helper()
	h.mcpRegistry.RecordSessionStatus(t.Context(), sid, translate.MCPSessionStatus{Servers: servers})
}

func connectedMCP(name string, src marotte.MCPSource) translate.MCPServerReport {
	return translate.MCPServerReport{Name: name, Source: src, State: translate.MCPReportConnected}
}

func failedMCP(name string, src marotte.MCPSource, reason string) translate.MCPServerReport {
	return translate.MCPServerReport{Name: name, Source: src, State: translate.MCPReportFailed, Error: reason}
}

func oauthMCP(name, url string) translate.MCPServerReport {
	return translate.MCPServerReport{Name: name, State: translate.MCPReportNeedsAuth, AuthorizationURL: url}
}

func disabledMCP(name string, src marotte.MCPSource) translate.MCPServerReport {
	return translate.MCPServerReport{Name: name, Source: src, State: translate.MCPReportDisabled}
}

func snapshotNames(h *Runtime) []string {
	var names []string
	for _, s := range h.mcpRegistry.snapshot() {
		names = append(names, s.Name)
	}
	return names
}

func TestMCPRegistry_AConnectedServerPopulatesTheSnapshot(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "s1", connectedMCP("github", marotte.MCPSource{}))
	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want 1 server", snap)
	}
	if snap[0].Name != "github" || snap[0].State != mcpStateConnected {
		t.Errorf("snapshot[0] = %+v", snap[0])
	}
}

func TestMCPRegistry_ASignInPromptOverridesTheState(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "s1", connectedMCP("linear", marotte.MCPSource{}))
	reportMCP(t, h, "s1", oauthMCP("linear", "https://oauth.example/auth"))

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateOAuth {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateOAuth)
	}
}

func TestMCPRegistry_AFailureRecordsItsError(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	before := h.bus.fanout.Position().Head
	reportMCP(t, h, "s1", failedMCP("broken", marotte.MCPSource{}, "connection refused"))

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 || snap[0].Name != "broken" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateFailed {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateFailed)
	}
	if snap[0].Error != "connection refused" {
		t.Errorf("error = %q", snap[0].Error)
	}
	frames := mcpStatusFrames(t, h, before, "broken")
	if len(frames) != 1 || frames[0].Payload.Status == nil || frames[0].Payload.Status.State != mcpStateFailed ||
		frames[0].Payload.Status.Error != "connection refused" {
		t.Errorf("mcp_status frames for broken = %+v, want one failed row with its error", frames)
	}
}

func TestMCPRegistry_ClearAllEmitsDisconnect(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "s1", connectedMCP("a", marotte.MCPSource{}), connectedMCP("b", marotte.MCPSource{}))

	before := h.bus.fanout.Position().Head
	h.mcpRegistry.clearAll()

	if len(h.mcpRegistry.snapshot()) != 0 {
		t.Error("clearAll left entries in registry")
	}
	if rows, _ := servedMCPStatus(t, h); len(rows) != 0 {
		t.Errorf("served rows after clearAll = %+v, want none", rows)
	}
	for _, name := range []string{"a", "b"} {
		frames := mcpStatusFrames(t, h, before, name)
		if len(frames) != 1 || frames[0].Payload.Status != nil {
			t.Errorf("mcp_status frames for %s = %+v, want one announcing it gone", name, frames)
		}
	}
}

func TestMCPRegistry_ClearAllOnEmptyNoEvents(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	before := h.bus.fanout.Position().Head
	h.mcpRegistry.clearAll()
	if head := h.bus.fanout.Position().Head; head != before {
		t.Error("clearAll on empty registry emitted events")
	}
}

// TestMCPRegistry_RecordsUnconfiguredServerWithOrigin pins that an unconfigured server is recorded with KAS's origin.
func TestMCPRegistry_RecordsUnconfiguredServerWithOrigin(t *testing.T) {
	cases := map[string]struct {
		src        marotte.MCPSource
		wantOrigin marotte.Origin
		wantRoot   string
		wantPower  string
	}{
		"a power's server carries the power's name": {
			src:        marotte.MCPSource{Origin: "power", Power: "aws-docs"},
			wantOrigin: marotte.OriginPower, wantPower: "aws-docs",
		},
		"a workspace server carries the root that defines it": {
			src:        marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"},
			wantOrigin: marotte.OriginWorkspace, wantRoot: "/workspace/app",
		},
		"a bundled server": {
			src:        marotte.MCPSource{Origin: "bundled"},
			wantOrigin: marotte.OriginBundled,
		},
		"an origin marotte has no word for is unattributable": {
			src:        marotte.MCPSource{Origin: "agent"},
			wantOrigin: marotte.OriginUnknown,
		},
		"a user-stamped name marotte does not hold is unattributable": {
			src:        marotte.MCPSource{Origin: "user"},
			wantOrigin: marotte.OriginUnknown,
		},
		"no stamp and an unknown name is unattributable": {
			wantOrigin: marotte.OriginUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("mine"))
			r := connectedMCP("theirs", tc.src)
			r.Tools = []string{"do_thing"}
			reportMCP(t, h, "s1", r)

			snap := h.mcpRegistry.snapshot()
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want the unconfigured server recorded", snap)
			}
			got := snap[0]
			if got.Name != "theirs" || got.State != mcpStateConnected {
				t.Errorf("snapshot[0] = %+v", got)
			}
			if got.Origin != tc.wantOrigin || got.OriginRoot != tc.wantRoot || got.OriginPower != tc.wantPower {
				t.Errorf("provenance = (%q, %q, %q), want (%q, %q, %q)",
					got.Origin, got.OriginRoot, got.OriginPower, tc.wantOrigin, tc.wantRoot, tc.wantPower)
			}
			if got.Shadows {
				t.Error("Shadows = true for a name marotte does not hold")
			}
		})
	}
}

// TestMCPRegistry_WireOriginBeatsTheName pins that a workspace definition can override marotte's name, so the
// status says workspace and marks the shadowing.
func TestMCPRegistry_WireOriginBeatsTheName(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("github"))
	src := marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}
	reportMCP(t, h, "s1", connectedMCP("github", src))

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want one row", snap)
	}
	if snap[0].Origin != marotte.OriginWorkspace || !snap[0].Shadows {
		t.Errorf("row = %+v, want origin workspace shadowing marotte's entry", snap[0])
	}
}

// TestMCPRegistry_RecordsAWorkspaceServerShadowingADisabledOne pins a foreign row for a workspace server reusing a disabled name.
func TestMCPRegistry_RecordsAWorkspaceServerShadowingADisabledOne(t *testing.T) {
	cfg := enabledConfig()
	cfg.configured["github"] = struct{}{}
	h := newHubWithMCPConfig(t, cfg)
	src := marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}
	reportMCP(t, h, "s1", connectedMCP("github", src))

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want the workspace server recorded", snap)
	}
	if snap[0].Origin != marotte.OriginWorkspace || !snap[0].Shadows || snap[0].OriginRoot != "/workspace/app" {
		t.Errorf("row = %+v, want workspace origin, its root, and the shadow mark", snap[0])
	}
}

// TestMCPRegistry_UserStampOnADisabledOwnedNameIsMarottes pins ownership, not enablement, as the attribution.
func TestMCPRegistry_UserStampOnADisabledOwnedNameIsMarottes(t *testing.T) {
	for name, src := range map[string]marotte.MCPSource{
		"user stamp": {Origin: "user"},
		"no stamp":   {},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := enabledConfig()
			cfg.configured["github"] = struct{}{}
			h := newHubWithMCPConfig(t, cfg)
			reportMCP(t, h, "s1", connectedMCP("github", src))
			snap := h.mcpRegistry.snapshot()
			if len(snap) != 1 || snap[0].Origin != marotte.OriginUser || snap[0].Shadows {
				t.Fatalf("after a connected frame: snapshot = %+v, want one user row with no shadow mark", snap)
			}
			reportMCP(t, h, "s1", failedMCP("github", src, "x"))
			snap = h.mcpRegistry.snapshot()
			if len(snap) != 1 || snap[0].Origin != marotte.OriginUser || snap[0].State != mcpStateFailed {
				t.Errorf("after a failed frame: snapshot = %+v, want one failed user row", snap)
			}
		})
	}
}

// TestMCPRegistry_NoMetaFallsBackToTheName pins the fallback for an unstamped entry.
func TestMCPRegistry_NoMetaFallsBackToTheName(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("github"))
	reportMCP(t, h, "s1", connectedMCP("github", marotte.MCPSource{}))

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 || snap[0].Origin != marotte.OriginUser || snap[0].Shadows {
		t.Errorf("snapshot = %+v, want one user row with no shadow mark", snap)
	}
}

// TestMCPRegistry_UserStampOnlyAttributesANameMarotteHolds pins the spoof case.
func TestMCPRegistry_UserStampOnlyAttributesANameMarotteHolds(t *testing.T) {
	cases := map[string]struct {
		server string
		want   marotte.Origin
	}{
		"user stamp on marotte's enabled name": {server: "github", want: marotte.OriginUser},
		"user stamp on a name marotte lacks":   {server: "theirs", want: marotte.OriginUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, enabledConfig("github"))
			reportMCP(t, h, "s1", connectedMCP(tc.server, marotte.MCPSource{Origin: "user"}))
			snap := h.mcpRegistry.snapshot()
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want one row", snap)
			}
			if snap[0].Origin != tc.want {
				t.Errorf("connected %q with a user stamp: Origin = %q, want %q", tc.server, snap[0].Origin, tc.want)
			}
		})
	}
}

// TestMCPRegistry_StampsUserOriginOnConfiguredServers pins that the user's own server never claims a foreign origin.
func TestMCPRegistry_StampsUserOriginOnConfiguredServers(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("github"))
	ctx := t.Context()
	h.mcpRegistry.RecordSessionStatus(ctx, "s1", translate.MCPSessionStatus{Servers: []translate.MCPServerReport{connectedMCP("github", marotte.MCPSource{})}})
	h.mcpRegistry.RecordSessionStatus(ctx, "s1", translate.MCPSessionStatus{Servers: []translate.MCPServerReport{oauthMCP("github", "https://oauth.example/auth")}})
	if got := h.mcpRegistry.snapshot()[0].Origin; got != marotte.OriginUser {
		t.Errorf("origin after a sign-in prompt = %q, want %q", got, marotte.OriginUser)
	}
	h.mcpRegistry.RecordSessionStatus(ctx, "s1", translate.MCPSessionStatus{Servers: []translate.MCPServerReport{failedMCP("github", marotte.MCPSource{}, "boom")}})
	if got := h.mcpRegistry.snapshot()[0].Origin; got != marotte.OriginUser {
		t.Errorf("origin after a failure = %q, want %q", got, marotte.OriginUser)
	}
}

// TestMCPRegistry_ADisabledServer pins a read-only row for a foreign disabled server and a discard for marotte's own.
func TestMCPRegistry_ADisabledServer(t *testing.T) {
	cases := map[string]struct {
		cfg        func() *fakeMCPConfig
		src        marotte.MCPSource
		wantRow    bool
		wantOrigin marotte.Origin
	}{
		"the user's own server, enabled: the config row already says off-or-on": {
			cfg:     func() *fakeMCPConfig { return enabledConfig("mine") },
			wantRow: false,
		},
		"the user's own server, switched off: must not gain a runtime row": {
			cfg: func() *fakeMCPConfig {
				c := enabledConfig()
				c.configured["mine"] = struct{}{}
				return c
			},
			wantRow: false,
		},
		"a power's server: the only evidence it exists": {
			cfg:     func() *fakeMCPConfig { return enabledConfig() },
			src:     marotte.MCPSource{Origin: "power", Power: "aws-docs"},
			wantRow: true, wantOrigin: marotte.OriginPower,
		},
		"an unattributable server: still shown, origin unknown": {
			cfg:     func() *fakeMCPConfig { return enabledConfig() },
			wantRow: true, wantOrigin: marotte.OriginUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHubWithMCPConfig(t, tc.cfg())
			reportMCP(t, h, "s1", disabledMCP("mine", tc.src))

			snap := h.mcpRegistry.snapshot()
			if !tc.wantRow {
				if len(snap) != 0 {
					t.Fatalf("snapshot = %+v, want no row", snap)
				}
				return
			}
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want one row", snap)
			}
			if snap[0].State != mcpStateDisabled {
				t.Errorf("state = %q, want %q", snap[0].State, mcpStateDisabled)
			}
			if snap[0].Origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", snap[0].Origin, tc.wantOrigin)
			}
		})
	}
}

// TestMCPRegistry_StatusJSONCarriesOrigin pins origin on the wire, never omitted.
func TestMCPRegistry_StatusJSONCarriesOrigin(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("mine"))
	reportMCP(t, h, "s1", connectedMCP("mine", marotte.MCPSource{}), connectedMCP("theirs", marotte.MCPSource{Origin: "power", Power: "aws-docs"}))

	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))

	raw := rec.Body.String()
	if !strings.Contains(raw, `"origin":"user"`) || !strings.Contains(raw, `"origin":"power"`) {
		t.Fatalf("body = %s, want both origins on the wire", raw)
	}
	var body struct {
		Servers []marotte.MCPServerStatus `json:"servers"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	// Alphabetical: mine, theirs.
	if body.Servers[0].Origin != marotte.OriginUser || body.Servers[1].Origin != marotte.OriginPower {
		t.Errorf("origins = %q / %q", body.Servers[0].Origin, body.Servers[1].Origin)
	}
}

// TestMCPStatus_CarriesProvenanceFields pins the workspace root, power name and shadow mark on the wire.
func TestMCPStatus_CarriesProvenanceFields(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("github"))
	reportMCP(t, h, "s1",
		connectedMCP("github", marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}),
		connectedMCP("docs", marotte.MCPSource{Origin: "power", Power: "aws-docs"}))

	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
	var body struct {
		Servers []marotte.MCPServerStatus `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("servers = %+v, want 2", body.Servers)
	}
	// Alphabetical: docs, github.
	docs, gh := body.Servers[0], body.Servers[1]
	if docs.Origin != marotte.OriginPower || docs.OriginPower != "aws-docs" || docs.Shadows {
		t.Errorf("docs = %+v, want power aws-docs, no shadow", docs)
	}
	if gh.Origin != marotte.OriginWorkspace || gh.OriginRoot != "/workspace/app" || !gh.Shadows {
		t.Errorf("github = %+v, want workspace /workspace/app shadowing", gh)
	}
	if raw := rec.Body.String(); !strings.Contains(raw, `"origin_root":"/workspace/app"`) ||
		!strings.Contains(raw, `"origin_power":"aws-docs"`) || !strings.Contains(raw, `"shadows":true`) {
		t.Errorf("body = %s, want origin_root, origin_power and shadows on the wire", raw)
	}
}

func TestMCPRegistry_SnapshotIsStableAlphabetically(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "s1", connectedMCP("zulu", marotte.MCPSource{}), connectedMCP("alpha", marotte.MCPSource{}),
		connectedMCP("mike", marotte.MCPSource{}))

	names := snapshotNames(h)
	if !slices.IsSorted(names) {
		t.Errorf("snapshot not sorted: %v", names)
	}
}

func TestMCPRegistry_OnChangeFiresOutsideLock(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	var mu sync.Mutex
	count := 0
	done := make(chan struct{}, 10)
	h.mcpRegistry.setOnChange(func() {
		mu.Lock()
		count++
		mu.Unlock()
		done <- struct{}{}
	})
	reportMCP(t, h, "s1", connectedMCP("a", marotte.MCPSource{}))
	reportMCP(t, h, "s1", oauthMCP("a", "url"))
	reportMCP(t, h, "s1", failedMCP("a", marotte.MCPSource{}, "err"))
	h.mcpRegistry.clearAll()

	// Debounced, so wait for at least one callback.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onChange never fired")
	}
	// Let the debounce settle.
	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case <-done:
		default:
			goto drained
		}
	}
drained:
	mu.Lock()
	defer mu.Unlock()
	if count < 1 {
		t.Errorf("onChange count = %d, want >= 1", count)
	}
	// Shut down the notifier goroutine.
	close(h.lifecycle.done)
}

// A burst of mutations is one change: the subscriber is the steering generator, which rewrites
// environment.md per call. The bubble's clock makes the window exact.
func TestMCPRegistry_SignalsInsideOneWindowFireOneCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHubWithMCPConfig(t, nil)
		// A defer, not t.Cleanup: Test waits for bubble goroutines before cleanups, so a cleanup close deadlocks.
		defer close(h.lifecycle.done)
		start := time.Now()
		var mu sync.Mutex
		var firedAt []time.Duration
		h.mcpRegistry.setOnChange(func() {
			mu.Lock()
			firedAt = append(firedAt, time.Since(start))
			mu.Unlock()
		})

		h.mcpRegistry.signalChange()
		synctest.Sleep(30 * time.Millisecond)
		h.mcpRegistry.signalChange()
		synctest.Sleep(30 * time.Millisecond)
		h.mcpRegistry.signalChange()
		// Past the first signal's window: a coalescing notifier has fired once.
		synctest.Sleep(150 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		if len(firedAt) != 1 {
			t.Errorf("three signals 30ms apart fired onChange %d times (at %v), want 1", len(firedAt), firedAt)
		}
	})
}

// The window coalesces a burst but never swallows a later edit.
func TestMCPRegistry_SignalsBeyondTheWindowFireSeparateCallbacks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHubWithMCPConfig(t, nil)
		defer close(h.lifecycle.done) // see the sibling test for why not t.Cleanup
		var mu sync.Mutex
		calls := 0
		h.mcpRegistry.setOnChange(func() {
			mu.Lock()
			calls++
			mu.Unlock()
		})

		h.mcpRegistry.signalChange()
		synctest.Sleep(150 * time.Millisecond)
		h.mcpRegistry.signalChange()
		synctest.Sleep(150 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		if calls != 2 {
			t.Errorf("two signals 150ms apart fired onChange %d times, want 2", calls)
		}
	})
}

func TestMCPRegistry_HandleStatusReturnsJSON(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "s1", connectedMCP("github", marotte.MCPSource{}), failedMCP("broken", marotte.MCPSource{}, "no auth"))

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Servers []marotte.MCPServerStatus `json:"servers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("body = %+v", body)
	}
	// Alphabetical sort: broken then github.
	if body.Servers[0].Name != "broken" || body.Servers[0].State != "failed" {
		t.Errorf("body[0] = %+v", body.Servers[0])
	}
	if body.Servers[0].Error != "no auth" {
		t.Errorf("error = %q", body.Servers[0].Error)
	}
	if body.Servers[1].Name != "github" || body.Servers[1].State != "connected" {
		t.Errorf("body[1] = %+v", body.Servers[1])
	}
}

// servedMCPStatus is GET /api/mcp/status decoded to the rows by name and the stamp it carries.
func servedMCPStatus(t testing.TB, h *Runtime) (map[string]marotte.MCPServerStatus, *marotte.SubjectStamp) {
	t.Helper()
	body := servedMCPBody(t, h)
	rows := make(map[string]marotte.MCPServerStatus, len(body.Servers))
	for _, s := range body.Servers {
		rows[s.Name] = s
	}
	return rows, body.Subject
}

func servedMCPBody(t testing.TB, h *Runtime) marotte.MCPStatusResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp/status = %d: %s", rec.Code, rec.Body.String())
	}
	var body marotte.MCPStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET /api/mcp/status: %v", err)
	}
	return body
}

// snapshot is the projection the records give now, sorted by name: what the served rows equal once every write
// committed.
func (reg *mcpRegistry) snapshot() []mcpServerRuntime {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]mcpServerRuntime, 0, len(reg.servers))
	for _, row := range reg.projectLocked() {
		out = append(out, mcpServerRuntime(*row))
	}
	slices.SortFunc(out, func(a, b mcpServerRuntime) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// statusRowFrame is one mcp_status frame and the row it announces.
type statusRowFrame struct {
	Type    marotte.EventType        `json:"type"`
	Payload marotte.MCPStatusPayload `json:"payload"`
}

// mcpStatusFrames is every mcp_status frame after sinceID that names server.
func mcpStatusFrames(t *testing.T, h *Runtime, sinceID uint64, server string) []statusRowFrame {
	t.Helper()
	var out []statusRowFrame
	for _, e := range eventsSince(h, sinceID, marotte.EventMCPStatus) {
		var f statusRowFrame
		if err := json.Unmarshal(e.Event.Data, &f); err != nil {
			t.Fatalf("unmarshal mcp_status: %v", err)
		}
		if f.Type == marotte.EventMCPStatus && f.Payload.Server == server {
			out = append(out, f)
		}
	}
	return out
}

// mcpFramesFor is every frame after sinceID whose payload names server, with the stamp each carries.
func mcpFramesFor(t *testing.T, h *Runtime, sinceID uint64, server string) []marotte.ServerEvent {
	t.Helper()
	var out []marotte.ServerEvent
	for _, e := range bufferedSince(h, sinceID) {
		var msg marotte.ServerEvent
		if err := json.Unmarshal(e.Event.Data, &msg); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if p, ok := msg.Payload.(map[string]any); ok && p["server"] == server {
			out = append(out, msg)
		}
	}
	return out
}

func stampVersion(t *testing.T, s *marotte.SubjectStamp) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s.Version, 10, 64)
	if err != nil {
		t.Fatalf("stamp version %q is not a counter: %v", s.Version, err)
	}
	return v
}

// A client orders the MCP rows it copies by the `mcp_status` version: each row frame carries a version above the
// last, and the inventory read carries the version of the last frame it already reflects.
func TestMCPStatusFramesCarryIncreasingVersions(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	before := h.bus.fanout.Position().Head

	reportMCP(t, h, "s1", connectedMCP("s", marotte.MCPSource{}))
	reportMCP(t, h, "s1", failedMCP("s", marotte.MCPSource{}, "boom"))

	frames := mcpFramesFor(t, h, before, "s")
	if len(frames) < 2 {
		t.Fatalf("frames naming s = %d, want one per state change (2)", len(frames))
	}
	var last uint64
	for i, f := range frames {
		if f.Subject == nil || f.Subject.Kind != "mcp_status" {
			t.Fatalf("frame %d (%s) subject = %+v, want an mcp_status stamp", i, f.Type, f.Subject)
		}
		v := stampVersion(t, f.Subject)
		if v <= last {
			t.Errorf("frame %d version %d, want above the previous frame's %d", i, v, last)
		}
		last = v
	}
	rows, stamp := servedMCPStatus(t, h)
	if stamp == nil || stamp.Kind != "mcp_status" || stampVersion(t, stamp) != last {
		t.Errorf("GET /api/mcp/status subject = %+v, want mcp_status at the last frame's version %d", stamp, last)
	}
	if rows["s"].State != mcpStateFailed {
		t.Errorf("served row s = %+v, want failed", rows["s"])
	}
	states, err := h.resolveDigest(t.Context(), []sse.Held{held(subject.KindMCPStatus, "")})
	if err != nil {
		t.Fatalf("resolveDigest: %v", err)
	}
	if st := stateFor(t, states, subject.KindMCPStatus, ""); st.Status != sse.StatusCurrent || st.Version != stamp.Version {
		t.Errorf("digest mcp_status = {%q %q}, want the served version %q", st.Version, st.Status, stamp.Version)
	}
}

// The relay latch changes the served row, so it moves the version and announces the row: a client holding the
// earlier version would otherwise refuse the read that shows it.
func TestRelayLatchMovesTheStatusVersion(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", oauthMCP("remote", "https://auth.example/passive"))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "remote"); err != nil {
		t.Fatalf("claimSignIn: %v", err)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://auth.example/a?state=s", Server: "remote"}, false); shown.how != consentOnRow {
		t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
	}
	rows, stamp := servedMCPStatus(t, h)
	if stamp == nil || rows["remote"].State != mcpStateSigningIn || rows["remote"].Relayed {
		t.Fatalf("before the relay: subject %+v, row %+v; want a stamped signing_in row not yet relayed", stamp, rows["remote"])
	}
	prev := stampVersion(t, stamp)
	before := h.bus.fanout.Position().Head

	if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "s"); err != nil {
		t.Fatalf("beginOAuthRelay: %v", err)
	}

	rows, stamp = servedMCPStatus(t, h)
	if !rows["remote"].Relayed {
		t.Errorf("served row after the relay = %+v, want relayed", rows["remote"])
	}
	if stamp == nil || stampVersion(t, stamp) <= prev {
		t.Errorf("GET /api/mcp/status subject after the relay = %+v, want a version above %d", stamp, prev)
	}
	var announced bool
	for _, f := range mcpFramesFor(t, h, before, "remote") {
		announced = announced || f.Type == "mcp_status"
	}
	if !announced {
		t.Error("the relay latch sent no mcp_status frame for remote")
	}
}

// BenchmarkMCPRegistryStatus measures GET /api/mcp/status, read on every page load and reconcile.
func BenchmarkMCPRegistryStatus(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("servers=%d", n), func(b *testing.B) {
			h := newHubWithMCPConfig(b, nil)
			servers := make([]translate.MCPServerReport, 0, n)
			for i := range n {
				servers = append(servers, connectedMCP(fmt.Sprintf("server-%02d", i), marotte.MCPSource{}))
			}
			reportMCP(b, h, "s1", servers...)
			b.ResetTimer()
			for b.Loop() {
				h.mcpRegistry.handleStatus(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
			}
		})
	}
}

// The real *mcp.Store's MCPConfigContractTest run lives in internal/mcp (TestStore_MCPConfigContract).

func poolBody(t *testing.T, h *Runtime, query string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mcpRegistry.handlePool(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/pool"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mcp/pool%s status = %d, want 200", query, rec.Code)
	}
	return strings.TrimSpace(rec.Body.String())
}

func resourceMCP(name, uri string) translate.MCPServerReport {
	r := connectedMCP(name, marotte.MCPSource{})
	r.Resources = []marotte.MCPResourceInfo{{Name: uri, URI: uri}}
	return r
}

// mcpStatusFrame is a _kiro/mcp/status frame for one session, each server connected offering one resource.
func mcpStatusFrame(t *testing.T, sid marotte.SessionID, uris map[string]string) *marotte.RPCResponse {
	t.Helper()
	servers := []map[string]any{}
	for name, uri := range uris {
		servers = append(servers, map[string]any{
			"name": name, "status": "connected",
			"resources": []map[string]any{{"name": uri, "uri": uri}},
		})
	}
	params, err := json.Marshal(map[string]any{"sessionId": sid, "servers": servers})
	if err != nil {
		t.Fatalf("marshal status frame: %v", err)
	}
	return &marotte.RPCResponse{Method: methodV3MCPStatus, Params: params}
}

// Each session reports only its own servers, so the records are the union of the views, and a view's end takes the
// servers only it listed.
func TestMCPRegistry_UnionAcrossSessionViews(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", connectedMCP("github", marotte.MCPSource{}))
	reportMCP(t, h, "sess_b", connectedMCP("linear", marotte.MCPSource{}), connectedMCP("github", marotte.MCPSource{}))
	if got := snapshotNames(h); !slices.Equal(got, []string{"github", "linear"}) {
		t.Fatalf("records = %v, want [github linear]", got)
	}
	before := h.bus.fanout.Position().Head

	h.mcpRegistry.dropSession(t.Context(), "sess_b")

	if got := snapshotNames(h); !slices.Equal(got, []string{"github"}) {
		t.Errorf("records after sess_b ended = %v, want [github]", got)
	}
	if got := eventsSince(h, before, marotte.EventMCPStatus); len(got) != 1 {
		t.Errorf("mcp_status frames after sess_b ended = %d, want one, for linear", len(got))
	}
	if frames := mcpStatusFrames(t, h, before, "linear"); len(frames) != 1 || frames[0].Payload.Status != nil {
		t.Errorf("mcp_status frames for linear = %+v, want one announcing it gone", frames)
	}
}

// A frame is the session's whole view: a server it no longer lists is no longer the session's.
func TestMCPRegistry_RetiresAServerAWholeFrameOmits(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", connectedMCP("github", marotte.MCPSource{}), connectedMCP("docs", marotte.MCPSource{}))

	reportMCP(t, h, "sess_a", connectedMCP("docs", marotte.MCPSource{}))

	if got := snapshotNames(h); !slices.Equal(got, []string{"docs"}) {
		t.Errorf("records = %v, want [docs]", got)
	}
	for _, s := range h.MCPSnapshot() {
		if s.Name == "github" {
			t.Errorf("MCPSnapshot = %+v, want github gone", h.MCPSnapshot())
		}
	}
}

// marotte's own server switched off leaves the generated steering's connected integrations.
func TestMCPRegistry_OwnDisabledServerLeavesTheSnapshot(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("mine"))
	reportMCP(t, h, "sess_a", connectedMCP("mine", marotte.MCPSource{Origin: "user"}))
	if got := h.MCPSnapshot(); len(got) != 1 {
		t.Fatalf("MCPSnapshot = %+v, want mine connected", got)
	}

	reportMCP(t, h, "sess_a", disabledMCP("mine", marotte.MCPSource{Origin: "user"}))

	if got := h.MCPSnapshot(); len(got) != 0 {
		t.Errorf("MCPSnapshot after mine was disabled = %+v, want none", got)
	}
	if got := snapshotNames(h); len(got) != 0 {
		t.Errorf("records after mine was disabled = %v, want none", got)
	}
}

// Another session's sign-in prompt and the signing-in session's connecting report are not a new attempt: the
// row's relay latch and the client's paste box both stand.
func TestMCPRegistry_IdenticalFrameKeepsTheRelayLatch(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", oauthMCP("remote", "https://auth.example/passive"))
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "remote"); err != nil {
		t.Fatalf("claimSignIn: %v", err)
	}
	if shown := h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://auth.example/a?state=s", Server: "remote"}, false); shown.how != consentOnRow {
		t.Fatalf("anchorConsentURL = %+v, want on the standing sign-in's row", shown)
	}
	if _, err := h.mcpRegistry.beginOAuthRelay(t.Context(), "s"); err != nil {
		t.Fatalf("beginOAuthRelay: %v", err)
	}
	before := h.bus.fanout.Position().Head

	reportMCP(t, h, "sess_b", oauthMCP("remote", "https://auth.example/passive"))
	reportMCP(t, h, "sess_a", translate.MCPServerReport{Name: "remote", State: translate.MCPReportConnecting})

	if row := rowOf(t, h, "remote"); row.State != mcpStateSigningIn || !row.Relayed {
		t.Errorf("row after identical frames = %+v, want it still signing_in and relayed", row)
	}
	if got := mcpStatusFrames(t, h, before, "remote"); len(got) != 0 {
		t.Errorf("mcp_status frames after identical frames = %+v, want none", got)
	}
}

// A server reported connecting keeps the session's previous entry until a settled state arrives.
func TestMCPRegistry_AConnectingServerKeepsItsEntry(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", failedMCP("github", marotte.MCPSource{}, "boom"))

	reportMCP(t, h, "sess_a", translate.MCPServerReport{Name: "github", State: translate.MCPReportConnecting},
		translate.MCPServerReport{Name: "fresh", State: translate.MCPReportConnecting})

	snap := h.mcpRegistry.snapshot()
	if len(snap) != 1 || snap[0].Name != "github" || snap[0].State != mcpStateFailed {
		t.Errorf("records = %+v, want github still failed and nothing for fresh", snap)
	}
}

// The organization's registry block is the union of the views', and stands when the last view ends.
func TestMCPRegistry_GovernanceIsTheUnionOfViews(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reg := func(server, filtered string) *marotte.GovernanceMCPRegistry {
		return &marotte.GovernanceMCPRegistry{
			Servers: []marotte.GovernanceRegistryServer{{Name: server}}, Filtered: []string{filtered}, Unresolved: []string{},
		}
	}
	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_a", translate.MCPSessionStatus{Registry: reg("docs", "local-a")})
	h.mcpRegistry.RecordSessionStatus(t.Context(), "sess_b", translate.MCPSessionStatus{Registry: reg("docs", "local-b")})
	want := &marotte.GovernanceMCPRegistry{
		Servers:    []marotte.GovernanceRegistryServer{{Name: "docs"}},
		Filtered:   []string{"local-a", "local-b"},
		Unresolved: []string{},
	}
	if got := h.config.governance.peek().MCPRegistry; !reflect.DeepEqual(got, want) {
		t.Errorf("governance registry = %+v, want %+v", got, want)
	}

	h.mcpRegistry.dropSessions(t.Context(), []marotte.SessionID{"sess_a", "sess_b"})

	if got := h.config.governance.peek().MCPRegistry; !reflect.DeepEqual(got, want) {
		t.Errorf("governance registry after every view ended = %+v, want the last union %+v", got, want)
	}
}

// The `#` menu reads the chat's own view once its session reported, and every view's union before that.
func TestMCPPool_ChatViewElseUnion(t *testing.T) {
	h, cs, br := newTestHub()
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("openBridge: %v", err)
	}
	reportMCP(t, h, "sess_other", resourceMCP("extra", "e://x"), resourceMCP("docs", "d://other"))
	union := `{"servers":[{"name":"docs","resources":[{"name":"d://other","uri":"d://other"}],"resource_templates":[]},` +
		`{"name":"extra","resources":[{"name":"e://x","uri":"e://x"}],"resource_templates":[]}]}`
	if got := poolBody(t, h, "?chat_id=c1"); got != union {
		t.Errorf("pool of c1 before its session reported = %s, want the union %s", got, union)
	}

	reportMCP(t, h, br.SessionID(), resourceMCP("docs", "d://mine"))

	own := `{"servers":[{"name":"docs","resources":[{"name":"d://mine","uri":"d://mine"}],"resource_templates":[]}]}`
	if got := poolBody(t, h, "?chat_id=c1"); got != own {
		t.Errorf("pool of c1 = %s, want its own view %s", got, own)
	}
	for _, query := range []string{"", "?chat_id=c-none"} {
		if got := poolBody(t, h, query); got != union {
			t.Errorf("pool%s = %s, want the union %s", query, got, union)
		}
	}
}

// A chat's view ends with its session's forward, unless a reload already serves the same session id.
func TestBridgeExit_DropsTheChatView(t *testing.T) {
	open := func(t *testing.T) (*Runtime, *fakeBridge) {
		t.Helper()
		h, cs, br := newTestHub()
		if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
			t.Fatalf("seed the chat: %v", err)
		}
		if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
			t.Fatalf("openBridge: %v", err)
		}
		br.deliver(mcpStatusFrame(t, br.SessionID(), map[string]string{"docs": "d://x"}))
		waitFor(t, func() bool { return slices.Equal(snapshotNames(h), []string{"docs"}) })
		return h, br
	}
	t.Run("closed", func(t *testing.T) {
		h, _ := open(t)

		h.CloseChatState(t.Context(), "c1", nil)
		joinInflight(t, h)

		if got := snapshotNames(h); len(got) != 0 {
			t.Errorf("records after the chat closed = %v, want none", got)
		}
	})
	t.Run("reloaded under the same id", func(t *testing.T) {
		h, br := open(t)
		next := newFakeBridge()
		next.sessionID = string(br.SessionID())
		h.bridge.mgr.mu.Lock()
		h.bridge.mgr.bridges["c1"] = testBridge(next, bridgeReady)
		h.bridge.mgr.mu.Unlock()

		br.endStream()
		joinInflight(t, h)

		if got := snapshotNames(h); !slices.Equal(got, []string{"docs"}) {
			t.Errorf("records after the old forward ended = %v, want the reloaded session's [docs]", got)
		}
	})
}

// A deleted run's step sessions take their views with them.
func TestRunEnd_DropsStepViews(t *testing.T) {
	h, _, rig := newDeleteRig(t, kasRuns(t,
		map[string]any{"workflowId": "wf_1", "name": "r", "status": "completed", "parentSessionId": "sess_cur"},
	))
	insertChatSession(t, h, rig, "c1", "sess_cur")
	h.router.seedSteps("wf_1", []string{"sess_step"})
	reportMCP(t, h, "sess_step", connectedMCP("step-only", marotte.MCPSource{}))
	reportMCP(t, h, "sess_chat", connectedMCP("shared", marotte.MCPSource{}))

	h.runs.deleteForSessions(t.Context(), "c1", []string{"sess_cur"}, runStop{})

	if got := snapshotNames(h); !slices.Equal(got, []string{"shared"}) {
		t.Errorf("records after the run was deleted = %v, want [shared]", got)
	}
}

// A delete whose answer was lost and whose run reads back gone takes the step views and step rows too.
func TestRunEnd_AnUnconfirmedDeleteOfAGoneRunDropsStepViews(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, br, g, _ := gatedHub(t, &lifecycleOps[0])
		h.router.seedSteps("wf_1", []string{"sess_step"})
		reportMCP(t, h, "sess_step", connectedMCP("step-only", marotte.MCPSource{}))
		g.outcome = deleteAnswer(nil, engineExited)
		br.setCallRPCErr(methodKiroWorkflowInspect, workflowNotFoundErr())
		close(g.release)

		if err := h.runs.delete(t.Context(), "wf_1"); err != nil {
			t.Fatalf("Delete of a run read back gone = %v, want nil", err)
		}
		if got := snapshotNames(h); len(got) != 0 {
			t.Errorf("records after the run was read back gone = %v, want none", got)
		}
		if wf, ok := h.router.StepRun("sess_step"); ok {
			t.Errorf("router still maps sess_step to %q, want it forgotten", wf)
		}
	})
}

// An engine end takes its MCP servers, and the pool with them.
func TestMCPRegistry_AnEngineEndClearsThePool(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	reportMCP(t, h, "sess_a", resourceMCP("docs", "d://x"))

	h.mcpRegistry.clearAll()

	if got, want := poolBody(t, h, ""), `{"servers":[]}`; got != want {
		t.Errorf("pool after clearAll = %s, want %s", got, want)
	}
}

// A pool replaced or cleared is announced, so a client holding pool rows re-reads
// them even when no server changed state.
func TestMCPRegistry_PoolChangesAreAnnounced(t *testing.T) {
	h := newHubWithMCPConfig(t, nil)
	before := h.bus.fanout.Position().Head

	reportMCP(t, h, "sess_a", resourceMCP("docs", "d://x"))
	reportMCP(t, h, "sess_a", resourceMCP("docs", "d://x"))
	h.mcpRegistry.clearAll()

	want := string(marotte.EventMCPPoolChanged)
	if got := extractTypes(t, bufferedSince(h, before)); len(filterTypes(got, want)) != 2 {
		t.Errorf("events after a pool set, an identical frame and a clear = %v, want two %s", got, want)
	}
}

func filterTypes(types []string, want string) []string {
	var out []string
	for _, ty := range types {
		if ty == want {
			out = append(out, ty)
		}
	}
	return out
}

// A session where the server failed takes the row from one where it connected: the row reads failed with its error and
// its failed row is announced, while "Connected integrations" still lists the server the connected session can use.
func TestMCPRegistry_AFailedSessionTakesTheRowFromAConnectedOne(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("linear"))
	reportMCP(t, h, "sess-a", connectedMCP("linear", marotte.MCPSource{}))
	before := h.bus.fanout.Position().Head
	reportMCP(t, h, "sess-b", failedMCP("linear", marotte.MCPSource{}, "spawn ENOENT"))

	row := rowOf(t, h, "linear")
	if row.State != mcpStateFailed || row.Error != "spawn ENOENT" {
		t.Errorf("row = {state %q, error %q}, want failed with the failed session's error", row.State, row.Error)
	}
	if frames := mcpStatusFrames(t, h, before, "linear"); len(frames) != 1 || frames[0].Payload.Status == nil ||
		frames[0].Payload.Status.State != mcpStateFailed {
		t.Errorf("mcp_status frames after sess-b failed = %+v, want one failed row", frames)
	}
	if got := h.MCPSnapshot(); len(got) != 1 || got[0].Name != "linear" {
		t.Errorf("MCPSnapshot = %+v, want linear, which sess-a has connected", got)
	}
}

// "Connected integrations" lists a server any session has connected, even while the row shows another session
// waiting for, or in, a sign-in.
func TestMCPRegistry_SnapshotListsAServerAnySessionConnected(t *testing.T) {
	h := newHubWithMCPConfig(t, enabledConfig("linear"))
	reportMCP(t, h, "sess-a", connectedMCP("linear", marotte.MCPSource{}))
	reportMCP(t, h, "sess-b", oauthMCP("linear", "https://p.example/passive"))
	if got := rowOf(t, h, "linear").State; got != mcpStateOAuth {
		t.Fatalf("row state = %q, want needs_auth from the waiting session", got)
	}

	if got := h.MCPSnapshot(); len(got) != 1 || got[0].Name != "linear" {
		t.Errorf("MCPSnapshot while sess-b waits = %+v, want linear", got)
	}
	if _, err := h.mcpRegistry.claimSignIn(t.Context(), "linear"); err != nil {
		t.Fatalf("claimSignIn = %v", err)
	}
	h.mcpRegistry.anchorConsentURL(t.Context(), consentRequest{URL: "https://p.example/a?state=s", Server: "linear"}, false)
	if got := rowOf(t, h, "linear").State; got != mcpStateSigningIn {
		t.Fatalf("row state = %q, want signing_in", got)
	}
	if got := h.MCPSnapshot(); len(got) != 1 || got[0].Name != "linear" {
		t.Errorf("MCPSnapshot while sess-b signs in = %+v, want linear", got)
	}
	h.mcpRegistry.dropSession(t.Context(), "sess-a")
	if got := h.MCPSnapshot(); len(got) != 0 {
		t.Errorf("MCPSnapshot once only the waiting session is left = %+v, want none", got)
	}
}

// resolveServers is the one answer every view reader takes: which session's report a name's row shows and its actions
// address, whether any session has it connected, and a refused sign-in's reason.
func TestResolveServers(t *testing.T) {
	view := func(sid marotte.SessionID, servers ...translate.MCPServerReport) mcpView {
		return mcpView{session: sid, servers: servers}
	}
	waiting := oauthMCP("linear", "https://p.example/passive")
	refusedB := view("sess-b", waiting)
	refusedB.failedSignIn = viewSignInFailure{server: "linear", reason: "refused"}
	staleFailure := view("sess-b", connectedMCP("linear", marotte.MCPSource{}))
	staleFailure.failedSignIn = viewSignInFailure{server: "linear", reason: "refused"}
	type want struct {
		session   marotte.SessionID
		state     translate.MCPReportState
		connected bool
		err       string
	}
	cases := map[string]struct {
		views []mcpView
		want  *want
	}{
		"A connected, B waiting": {
			views: []mcpView{view("sess-a", connectedMCP("linear", marotte.MCPSource{})), view("sess-b", waiting)},
			want:  &want{session: "sess-b", state: translate.MCPReportNeedsAuth, connected: true},
		},
		"A connected, B refused a sign-in": {
			views: []mcpView{view("sess-a", connectedMCP("linear", marotte.MCPSource{})), refusedB},
			want:  &want{session: "sess-b", state: translate.MCPReportNeedsAuth, connected: true, err: "refused"},
		},
		"A connected, B failed": {
			views: []mcpView{
				view("sess-a", connectedMCP("linear", marotte.MCPSource{})),
				view("sess-b", failedMCP("linear", marotte.MCPSource{}, "boom")),
			},
			want: &want{session: "sess-b", state: translate.MCPReportFailed, connected: true},
		},
		"A failed, B waiting": {
			views: []mcpView{view("sess-a", failedMCP("linear", marotte.MCPSource{}, "boom")), view("sess-b", waiting)},
			want:  &want{session: "sess-b", state: translate.MCPReportNeedsAuth},
		},
		"A disabled, B connected": {
			views: []mcpView{view("sess-a", disabledMCP("linear", marotte.MCPSource{})), view("sess-b", connectedMCP("linear", marotte.MCPSource{}))},
			want:  &want{session: "sess-b", state: translate.MCPReportConnected, connected: true},
		},
		"a refusal whose session then connected": {
			views: []mcpView{staleFailure},
			want:  &want{session: "sess-b", state: translate.MCPReportConnected, connected: true},
		},
		"both connected": {
			views: []mcpView{
				view("sess-a", connectedMCP("linear", marotte.MCPSource{})),
				view("sess-b", connectedMCP("linear", marotte.MCPSource{})),
			},
			want: &want{session: "sess-a", state: translate.MCPReportConnected, connected: true},
		},
		"only B lists it": {
			views: []mcpView{view("sess-a"), view("sess-b", failedMCP("linear", marotte.MCPSource{}, "boom"))},
			want:  &want{session: "sess-b", state: translate.MCPReportFailed},
		},
		"both waiting": {
			views: []mcpView{view("sess-a", waiting), view("sess-b", waiting)},
			want:  &want{session: "sess-a", state: translate.MCPReportNeedsAuth},
		},
		"no view lists it": {
			views: []mcpView{view("sess-a", connectedMCP("github", marotte.MCPSource{}))},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg := &mcpRegistry{views: tc.views}
			got, ok := reg.resolveLocked("linear")
			if tc.want == nil {
				if ok {
					t.Errorf("resolution = %+v, want none", got)
				}
				return
			}
			if !ok {
				t.Fatal("no resolution for linear")
			}
			if got.session != tc.want.session || got.report.State != tc.want.state || got.connected != tc.want.connected ||
				got.signInError != tc.want.err {
				t.Errorf("resolution = {session %s, state %v, connected %t, error %q}, want %+v",
					got.session, got.report.State, got.connected, got.signInError, *tc.want)
			}
			if owner := tc.views[got.view].session; owner != got.session {
				t.Errorf("view %d is %s's, want the report's session %s", got.view, owner, got.session)
			}
		})
	}
}
