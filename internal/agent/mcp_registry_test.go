package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// fakeMCPConfig is the filter tests' name census. The sets are independent so a test can stage a
// configured-but-disabled name; enabledConfig builds the nested shape.
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

func newHubWithMCPConfig(cfg mcpNameSets) *Runtime {
	cs := newTestChatStore()
	factory := func() ACPBridge { return newFakeBridge() }
	var opts []Option
	if cfg != nil {
		opts = append(opts, WithMCPConfig(cfg))
	}
	h := New(context.Background(), "/tmp/work", factory, cs, opts...)
	cs.wire(h)
	return h
}

func TestMCPRegistry_RecordConnectedPopulatesSnapshot(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "github", marotte.MCPSource{}, nil, nil, nil, nil)
	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v, want 1 server", snap)
	}
	if snap[0].Name != "github" || snap[0].State != mcpStateConnected {
		t.Errorf("snapshot[0] = %+v", snap[0])
	}
}

func TestMCPRegistry_RecordOAuthOverridesState(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "linear", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordOAuth(t.Context(), "linear", marotte.MCPSource{}, "https://oauth.example/auth")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateOAuth {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateOAuth)
	}
	if snap[0].OAuthURL != "https://oauth.example/auth" {
		t.Errorf("oauth url = %q", snap[0].OAuthURL)
	}
}

func TestMCPRegistry_RecordInitFailureRecordsError(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	before := h.bus.fanout.Position().Head
	h.mcpRegistry.RecordInitFailure(t.Context(), "broken", marotte.MCPSource{}, "connection refused")

	snap := h.mcpRegistry.Snapshot()
	if len(snap) != 1 || snap[0].Name != "broken" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap[0].State != mcpStateFailed {
		t.Errorf("state = %q, want %q", snap[0].State, mcpStateFailed)
	}
	if snap[0].Error != "connection refused" {
		t.Errorf("error = %q", snap[0].Error)
	}
	types := extractTypes(t, bufferedSince(h, before))
	found := false
	for _, tp := range types {
		if tp == "mcp_failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("mcp_failed event not emitted, got %v", types)
	}
}

func TestMCPRegistry_ClearAllEmitsDisconnect(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "a", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "b", marotte.MCPSource{}, nil, nil, nil, nil)

	before := h.bus.fanout.Position().Head
	h.mcpRegistry.clearAll(t.Context())

	if len(h.mcpRegistry.Snapshot()) != 0 {
		t.Error("clearAll left entries in registry")
	}
	types := extractTypes(t, bufferedSince(h, before))
	got := 0
	for _, tp := range types {
		if tp == "mcp_disconnected" {
			got++
		}
	}
	if got != 2 {
		t.Errorf("got %d mcp_disconnected events, want 2 (types=%v)", got, types)
	}
}

func TestMCPRegistry_ClearAllOnEmptyNoEvents(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	before := h.bus.fanout.Position().Head
	h.mcpRegistry.clearAll(t.Context())
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
			h := newHubWithMCPConfig(enabledConfig("mine"))
			h.mcpRegistry.RecordConnected(t.Context(), "theirs", tc.src, []string{"do_thing"}, nil, nil, nil)

			snap := h.mcpRegistry.Snapshot()
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
	h := newHubWithMCPConfig(enabledConfig("github"))
	src := marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}
	h.mcpRegistry.RecordConnected(t.Context(), "github", src, nil, nil, nil, nil)

	snap := h.mcpRegistry.Snapshot()
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
	h := newHubWithMCPConfig(cfg)
	src := marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}
	h.mcpRegistry.RecordConnected(t.Context(), "github", src, nil, nil, nil, nil)

	snap := h.mcpRegistry.Snapshot()
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
			h := newHubWithMCPConfig(cfg)
			h.mcpRegistry.RecordConnected(t.Context(), "github", src, nil, nil, nil, nil)
			snap := h.mcpRegistry.Snapshot()
			if len(snap) != 1 || snap[0].Origin != marotte.OriginUser || snap[0].Shadows {
				t.Fatalf("after RecordConnected: snapshot = %+v, want one user row with no shadow mark", snap)
			}
			h.mcpRegistry.RecordInitFailure(t.Context(), "github", src, "x")
			snap = h.mcpRegistry.Snapshot()
			if len(snap) != 1 || snap[0].Origin != marotte.OriginUser || snap[0].State != mcpStateFailed {
				t.Errorf("after RecordInitFailure: snapshot = %+v, want one failed user row", snap)
			}
		})
	}
}

// TestMCPRegistry_NoMetaFallsBackToTheName pins the fallback for an unstamped entry.
func TestMCPRegistry_NoMetaFallsBackToTheName(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("github"))
	h.mcpRegistry.RecordConnected(t.Context(), "github", marotte.MCPSource{}, nil, nil, nil, nil)

	snap := h.mcpRegistry.Snapshot()
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
			h := newHubWithMCPConfig(enabledConfig("github"))
			h.mcpRegistry.RecordConnected(t.Context(), tc.server, marotte.MCPSource{Origin: "user"}, nil, nil, nil, nil)
			snap := h.mcpRegistry.Snapshot()
			if len(snap) != 1 {
				t.Fatalf("snapshot = %+v, want one row", snap)
			}
			if snap[0].Origin != tc.want {
				t.Errorf("RecordConnected(%q, user stamp): Origin = %q, want %q", tc.server, snap[0].Origin, tc.want)
			}
		})
	}
}

// TestMCPRegistry_StampsUserOriginOnConfiguredServers pins that the user's own server never claims a foreign origin.
func TestMCPRegistry_StampsUserOriginOnConfiguredServers(t *testing.T) {
	h := newHubWithMCPConfig(enabledConfig("github"))
	ctx := t.Context()
	h.mcpRegistry.RecordConnected(ctx, "github", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordOAuth(ctx, "github", marotte.MCPSource{}, "https://oauth.example/auth")
	if got := h.mcpRegistry.Snapshot()[0].Origin; got != marotte.OriginUser {
		t.Errorf("origin after recordOAuth = %q, want %q", got, marotte.OriginUser)
	}
	h.mcpRegistry.RecordInitFailure(ctx, "github", marotte.MCPSource{}, "boom")
	if got := h.mcpRegistry.Snapshot()[0].Origin; got != marotte.OriginUser {
		t.Errorf("origin after recordInitFailure = %q, want %q", got, marotte.OriginUser)
	}
}

// TestMCPRegistry_RecordDisabled pins a read-only row for a foreign disabled server and a discard for marotte's own.
func TestMCPRegistry_RecordDisabled(t *testing.T) {
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
			h := newHubWithMCPConfig(tc.cfg())
			h.mcpRegistry.RecordDisabled(t.Context(), "mine", tc.src)

			snap := h.mcpRegistry.Snapshot()
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
	h := newHubWithMCPConfig(enabledConfig("mine"))
	h.mcpRegistry.RecordConnected(t.Context(), "mine", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "theirs", marotte.MCPSource{Origin: "power", Power: "aws-docs"}, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))

	raw := rec.Body.String()
	if !strings.Contains(raw, `"origin":"user"`) || !strings.Contains(raw, `"origin":"power"`) {
		t.Fatalf("body = %s, want both origins on the wire", raw)
	}
	var body struct {
		Servers []statusServer `json:"servers"`
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
	h := newHubWithMCPConfig(enabledConfig("github"))
	h.mcpRegistry.RecordConnected(t.Context(), "github", marotte.MCPSource{Origin: "workspace", Root: "/workspace/app"}, nil, nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "docs", marotte.MCPSource{Origin: "power", Power: "aws-docs"}, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil))
	var body struct {
		Servers []statusServer `json:"servers"`
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
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "zulu", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "alpha", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordConnected(t.Context(), "mike", marotte.MCPSource{}, nil, nil, nil, nil)

	names := make([]string, 0)
	for _, s := range h.mcpRegistry.Snapshot() {
		names = append(names, s.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("snapshot not sorted: %v", names)
	}
}

func TestMCPRegistry_OnChangeFiresOutsideLock(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	var mu sync.Mutex
	count := 0
	done := make(chan struct{}, 10)
	h.mcpRegistry.SetOnChange(func() {
		mu.Lock()
		count++
		mu.Unlock()
		done <- struct{}{}
	})
	h.mcpRegistry.RecordConnected(t.Context(), "a", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordOAuth(t.Context(), "a", marotte.MCPSource{}, "url")
	h.mcpRegistry.RecordInitFailure(t.Context(), "a", marotte.MCPSource{}, "err")
	h.mcpRegistry.clearAll(t.Context())

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
		h := newHubWithMCPConfig(nil)
		// A defer, not t.Cleanup: Test waits for bubble goroutines before cleanups, so a cleanup close deadlocks.
		defer close(h.lifecycle.done)
		start := time.Now()
		var mu sync.Mutex
		var firedAt []time.Duration
		h.mcpRegistry.SetOnChange(func() {
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
		h := newHubWithMCPConfig(nil)
		defer close(h.lifecycle.done) // see the sibling test for why not t.Cleanup
		var mu sync.Mutex
		calls := 0
		h.mcpRegistry.SetOnChange(func() {
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
	h := newHubWithMCPConfig(nil)
	h.mcpRegistry.RecordConnected(t.Context(), "github", marotte.MCPSource{}, nil, nil, nil, nil)
	h.mcpRegistry.RecordInitFailure(t.Context(), "broken", marotte.MCPSource{}, "no auth")

	req := httptest.NewRequest(http.MethodGet, "/api/mcp/status", nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handleStatus(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Servers []statusServer `json:"servers"`
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

// BenchmarkMCPRegistrySnapshot measures Snapshot, called on every SSE reconnect and bridge spawn.
func BenchmarkMCPRegistrySnapshot(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("servers=%d", n), func(b *testing.B) {
			h := newHubWithMCPConfig(nil)
			for i := range n {
				h.mcpRegistry.RecordConnected(b.Context(), fmt.Sprintf("server-%02d", i), marotte.MCPSource{}, nil, nil, nil, nil)
			}
			b.ResetTimer()
			for b.Loop() {
				_ = h.mcpRegistry.Snapshot()
			}
		})
	}
}

// The real *mcp.Store's MCPConfigContractTest run lives in internal/mcp (TestStore_MCPConfigContract).

// The `#` menu lists the active chat's own pool: a mention resolves only there.
func TestMCPRegistry_PoolAnswersOnlyThatChatsResources(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	ba, bb := newFakeBridge(), newFakeBridge()
	h.bridge.mgr.insert("c-a", &sharedBridge{bridge: ba, state: bridgeIdle})
	h.bridge.mgr.insert("c-b", &sharedBridge{bridge: bb, state: bridgeIdle})
	h.mcpRegistry.setPool(ba, []translate.MCPPoolServer{{Name: "docs", Resources: []marotte.MCPResourceInfo{{Name: "a", URI: "a://x"}}}})
	h.mcpRegistry.setPool(bb, []translate.MCPPoolServer{
		{Name: "extra", Resources: []marotte.MCPResourceInfo{{Name: "e", URI: "e://x"}}},
		{
			Name: "docs", Resources: []marotte.MCPResourceInfo{{Name: "b", URI: "b://x"}},
			ResourceTemplates: []marotte.MCPResourceTemplateInfo{{Name: "t", URITemplate: "b://{id}"}},
		},
	})

	if got, want := poolBody(t, h, "c-a"), `{"servers":[{"name":"docs","resources":[{"name":"a","uri":"a://x"}],"resource_templates":[]}]}`; got != want {
		t.Errorf("chat A pool = %s, want %s", got, want)
	}
	wantB := `{"servers":[{"name":"docs","resources":[{"name":"b","uri":"b://x"}],"resource_templates":[{"name":"t","uri_template":"b://{id}"}]},` +
		`{"name":"extra","resources":[{"name":"e","uri":"e://x"}],"resource_templates":[]}]}`
	if got := poolBody(t, h, "c-b"); got != wantB {
		t.Errorf("chat B pool = %s, want %s", got, wantB)
	}
	if got, want := poolBody(t, h, "c-none"), `{"servers":[]}`; got != want {
		t.Errorf("pool of a chat with no bridge = %s, want %s", got, want)
	}
}

// A status frame lists every server, so a server a later frame omits leaves the pool.
func TestMCPRegistry_StatusFrameReplacesThePoolWhole(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	br := newFakeBridge()
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	h.coord.recordPool(br, mcpStatusFrame(t, map[string]string{"docs": "d://x", "extra": "e://x"}))
	h.coord.recordPool(br, mcpStatusFrame(t, map[string]string{"docs": "d://x"}))

	want := `{"servers":[{"name":"docs","resources":[{"name":"d://x","uri":"d://x"}],"resource_templates":[]}]}`
	if got := poolBody(t, h, "c1"); got != want {
		t.Errorf("pool after a frame without extra = %s, want %s", got, want)
	}
}

// The last-bridge wipe must leave every bridge's pool to that bridge's own exit.
func TestMCPRegistry_ClearAllLeavesALiveBridgesPool(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	br := newFakeBridge()
	h.bridge.mgr.insert("c1", &sharedBridge{bridge: br, state: bridgeIdle})
	h.mcpRegistry.setPool(br, []translate.MCPPoolServer{{Name: "docs", Resources: []marotte.MCPResourceInfo{{Name: "d", URI: "d://x"}}}})

	h.mcpRegistry.clearAll(t.Context())

	want := `{"servers":[{"name":"docs","resources":[{"name":"d","uri":"d://x"}],"resource_templates":[]}]}`
	if got := poolBody(t, h, "c1"); got != want {
		t.Errorf("pool after clearAll = %s, want %s", got, want)
	}
}

// A pool replaced or dropped is announced, so a client holding pool rows re-reads
// them even when no server changed state.
func TestMCPRegistry_PoolChangesAreAnnounced(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	br := newFakeBridge()
	before := h.bus.fanout.Position().Head

	h.coord.recordPool(br, mcpStatusFrame(t, map[string]string{"docs": "d://x"}))
	h.mcpRegistry.dropPool(br)

	want := string(marotte.EventMCPPoolChanged)
	if got := extractTypes(t, bufferedSince(h, before)); len(filterTypes(got, want)) != 2 {
		t.Errorf("events after a pool set and drop = %v, want two %s", got, want)
	}
}

// filterTypes keeps the entries of types equal to want.
func filterTypes(types []string, want string) []string {
	var out []string
	for _, ty := range types {
		if ty == want {
			out = append(out, ty)
		}
	}
	return out
}

// onPoolSelected runs fn between the pool endpoint's bridge selection and its
// pool read, for the rest of the test.
func onPoolSelected(t *testing.T, fn func()) {
	t.Helper()
	prev := poolSelected
	poolSelected = fn
	t.Cleanup(func() { poolSelected = prev })
}

// A read straddling a bridge swap answers the bridge serving the chat afterwards, never the departed one.
func TestMCPRegistry_PoolFollowsABridgeReplacedMidRead(t *testing.T) {
	poolOf := func(uri string) []translate.MCPPoolServer {
		return []translate.MCPPoolServer{{Name: "docs", Resources: []marotte.MCPResourceInfo{{Name: uri, URI: uri}}}}
	}
	newWant := `{"servers":[{"name":"docs","resources":[{"name":"new://r","uri":"new://r"}],"resource_templates":[]}]}`
	cases := []struct {
		name  string
		moves int
		swap  func(h *Runtime, sb *sharedBridge, next ACPBridge)
		want  string
	}{
		{
			name: "bridge swapped in the entry", moves: 1, want: newWant,
			swap: func(_ *Runtime, sb *sharedBridge, next ACPBridge) { sb.swapBridge(next) },
		},
		{
			name: "entry replaced", moves: 1, want: newWant,
			swap: func(h *Runtime, sb *sharedBridge, next ACPBridge) {
				h.bridge.mgr.removeIfSame("c1", sb)
				h.bridge.mgr.insert("c1", &sharedBridge{bridge: next, state: bridgeIdle})
			},
		},
		{
			name: "bridge keeps moving", moves: servingPoolAttempts, want: `{"servers":[]}`,
			swap: func(_ *Runtime, sb *sharedBridge, next ACPBridge) { sb.swapBridge(next) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHubWithMCPConfig(nil)
			old := newFakeBridge()
			sb := &sharedBridge{bridge: old, state: bridgeIdle}
			h.bridge.mgr.insert("c1", sb)
			h.mcpRegistry.setPool(old, poolOf("old://r"))
			moved := 0
			onPoolSelected(t, func() {
				if moved == tc.moves {
					return
				}
				moved++
				next := newFakeBridge()
				h.mcpRegistry.setPool(next, poolOf("new://r"))
				tc.swap(h, h.bridge.mgr.get("c1"), next)
			})

			if got := poolBody(t, h, "c1"); got != tc.want {
				t.Errorf("c1 pool after %d replacement(s) mid-read = %s, want %s", tc.moves, got, tc.want)
			}
		})
	}
}

// The endpoint and a failed load's swap run on different goroutines (-race).
func TestMCPRegistry_PoolReadRacesABridgeSwap(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	a, b := newFakeBridge(), newFakeBridge()
	sb := &sharedBridge{bridge: a, state: bridgeIdle}
	h.bridge.mgr.insert("c1", sb)
	pool := []translate.MCPPoolServer{{Name: "docs", Resources: []marotte.MCPResourceInfo{{Name: "d", URI: "d://x"}}}}
	h.mcpRegistry.setPool(a, pool)
	h.mcpRegistry.setPool(b, pool)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		next := ACPBridge(b)
		for {
			select {
			case <-done:
				return
			default:
				next = sb.swapBridge(next)
			}
		}
	})
	want := `{"servers":[{"name":"docs","resources":[{"name":"d","uri":"d://x"}],"resource_templates":[]}]}`
	for range 200 {
		if got := poolBody(t, h, "c1"); got != want && got != `{"servers":[]}` {
			t.Fatalf("c1 pool during swaps = %s, want %s or an empty pool", got, want)
		}
	}
	close(done)
	wg.Wait()
}

func TestMCPRegistry_PoolRequiresAChatID(t *testing.T) {
	h := newHubWithMCPConfig(nil)
	rec := httptest.NewRecorder()
	h.mcpRegistry.handlePool(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/pool", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /api/mcp/pool with no chat_id status = %d, want 400", rec.Code)
	}
}
