package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/secretstore"
)

// probeKey is the key shape KAS derives (sha256("http://127.0.0.1:46877/mcp" + "|")).
const probeKey = "kiro.mcp.2a0a3d1d4672ffaff77fcbe95f21be210e2e444f1b152fb537773dd72a3ddf3a.client"

func newSecretStore(t *testing.T) *secretstore.Store {
	t.Helper()
	s, err := secretstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("secretstore.New() error = %v", err)
	}
	return s
}

func rawParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}

// TestSecretGetMissIsExplicitNull pins the wire contract: KAS reads null as no credential yet.
func TestSecretGetMissIsExplicitNull(t *testing.T) {
	store := newSecretStore(t)
	got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey}))
	if got.Value != nil {
		t.Errorf("Value = %q, want nil for a miss", *got.Value)
	}
	// An explicit null, not an omitted key.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(data) != `{"value":null}` {
		t.Errorf("wire form = %s, want {\"value\":null}", data)
	}
}

// TestSecretStoreThenGet is the round trip KAS relies on across a spawn.
func TestSecretStoreThenGet(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()
	blob := `{"client_id":"probe-client-1","client_secret":"s3cret"}`

	result, err := secretStoreResult(ctx, store, rawParams(t, map[string]string{"key": probeKey, "value": blob}))
	if err != nil {
		t.Fatalf("secretStoreResult() error = %v, want nil", err)
	}
	if len(result) != 0 {
		t.Errorf("store result = %v, want an empty object", result)
	}

	got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey}))
	if got.Value == nil || *got.Value != blob {
		t.Errorf("Value = %v, want %q", got.Value, blob)
	}
}

// TestSecretDelete covers delete, including the absent key KAS issues speculatively.
func TestSecretDelete(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()
	if err := store.Set(ctx, probeKey, "v"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := secretDeleteResult(ctx, store, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Fatalf("secretDeleteResult() error = %v, want nil", err)
	}
	if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
		t.Errorf("Value = %q after delete, want nil", *got.Value)
	}
	if _, err := secretDeleteResult(ctx, store, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Errorf("secretDeleteResult(absent) error = %v, want nil", err)
	}
}

// TestSecretStoreRejectsBadParams pins a JSON-RPC error rather than a store under an empty key.
func TestSecretStoreRejectsBadParams(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()

	cases := []struct {
		name     string
		params   json.RawMessage
		wantCode int
	}{
		{"no key", rawParams(t, map[string]string{"value": "v"}), -32602},
		{"empty key", rawParams(t, map[string]string{"key": "", "value": "v"}), -32602},
		{"not an object", json.RawMessage(`["key","value"]`), -32602},
		{"nil params", nil, -32602},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := secretStoreResult(ctx, store, tc.params)
			if err == nil {
				t.Error("secretStoreResult() error = nil, want an error")
			}
			// A bad request is invalid-params (-32602).
			rpcErr, isRPC := errors.AsType[*marotte.RPCError](err)
			if !isRPC {
				t.Errorf("secretStoreResult(%s) error = %T, want *marotte.RPCError", tc.name, err)
			} else if rpcErr.Code != tc.wantCode {
				t.Errorf("secretStoreResult(%s) error code = %d, want %d", tc.name, rpcErr.Code, tc.wantCode)
			}
			// Asserted through the read path: "not there" is what KAS sees.
			if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
				t.Errorf("a rejected store left a value: %q", *got.Value)
			}
		})
	}
}

// TestSecretNilStoreDegradesRatherThanFails pins that with no store a get reports absent so MCP OAuth re-registers per spawn.
func TestSecretNilStoreDegradesRatherThanFails(t *testing.T) {
	if got := secretGetResult(nil, rawParams(t, map[string]string{"key": probeKey})); got.Value != nil {
		t.Errorf("Value = %q, want nil", *got.Value)
	}
	// A delete against no store succeeds.
	if _, err := secretDeleteResult(t.Context(), nil, rawParams(t, map[string]string{"key": probeKey})); err != nil {
		t.Errorf("secretDeleteResult(nil store) error = %v, want nil", err)
	}
	// A store against no store must not claim success.
	_, err := secretStoreResult(t.Context(), nil, rawParams(t, map[string]string{"key": probeKey, "value": "v"}))
	if err == nil {
		t.Error("secretStoreResult(nil store) error = nil, want an error")
	}
	// Internal-error (-32603): the request was well formed.
	rpcErr, isRPC := errors.AsType[*marotte.RPCError](err)
	if !isRPC {
		t.Errorf("secretStoreResult(nil store) error = %T, want *marotte.RPCError", err)
	} else if rpcErr.Code != -32603 {
		t.Errorf("secretStoreResult(nil store) error code = %d, want -32603", rpcErr.Code)
	}
}

// TestSecretDeleteRejectsMissingKey pins that a keyless delete is invalid-params, not a no-op success.
func TestSecretDeleteRejectsMissingKey(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()

	cases := []struct {
		name     string
		params   json.RawMessage
		wantCode int
	}{
		{"no_key", rawParams(t, map[string]string{}), -32602},
		{"empty_key", rawParams(t, map[string]string{"key": ""}), -32602},
		{"not_an_object", json.RawMessage(`"probe"`), -32602},
		{"nil_params", nil, -32602},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := secretDeleteResult(ctx, store, tc.params)
			if err == nil {
				t.Error("secretDeleteResult() error = nil, want an error")
			}
			if result != nil {
				t.Errorf("secretDeleteResult(%s) result = %v, want nil alongside an error", tc.name, result)
			}
			rpcErr, isRPC := errors.AsType[*marotte.RPCError](err)
			if !isRPC {
				t.Errorf("secretDeleteResult(%s) error = %T, want *marotte.RPCError", tc.name, err)
			} else if rpcErr.Code != tc.wantCode {
				t.Errorf("secretDeleteResult(%s) error code = %d, want %d", tc.name, rpcErr.Code, tc.wantCode)
			}
		})
	}
}

// TestHandleKiroSecretRequestClaimsOnlyItsOwnMethods pins the dispatch hop: a wrong claim
// swallows a frame, a missed one wedges the turn.
func TestHandleKiroSecretRequestClaimsOnlyItsOwnMethods(t *testing.T) {
	h, _, _ := newTestHub()
	var id int64 = 1
	cases := []struct {
		method string
		want   bool
	}{
		{methodKiroSecretGet, true},
		{methodKiroSecretStore, true},
		{methodKiroSecretDelete, true},
		{"_kiro/auth/get" + "AccessToken", false},
		{marotte.MethodFSRead, false},
		{"_kiro/secret/unknown", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			msg := &marotte.RPCResponse{Method: tc.method, ID: &id}
			// No bridge is registered, so the return value is the whole contract.
			if got := h.inbound.handleKiroSecretRequest(t.Context(), "c1", msg); got != tc.want {
				t.Errorf("handleKiroSecretRequest(%q) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// TestSecretStoreIsSharedAcrossBridges pins that one bridge's DCR result is visible to the next.
func TestSecretStoreIsSharedAcrossBridges(t *testing.T) {
	store := newSecretStore(t)
	ctx := t.Context()

	if _, err := secretStoreResult(ctx, store, rawParams(t, map[string]string{"key": probeKey, "value": "reg"})); err != nil {
		t.Fatalf("bridge A store: %v", err)
	}
	// Same process, same store pointer.
	if got := secretGetResult(store, rawParams(t, map[string]string{"key": probeKey})); got.Value == nil || *got.Value != "reg" {
		t.Errorf("bridge B Value = %v, want %q", got.Value, "reg")
	}
}

// TestSecretRequestReportsOnlyUndecodableParams pins that only undecodable params earn a line; KAS gets
// on every MCP connect. No t.Parallel: captureLogs swaps the slog default.
func TestSecretRequestReportsOnlyUndecodableParams(t *testing.T) {
	const wantLine = "v3 secret: undecodable params"

	t.Run("undecodable_params", func(t *testing.T) {
		logs := captureLogs(t)
		secretGetResult(nil, json.RawMessage(`["probe"]`))
		if got := logs.String(); !strings.Contains(got, wantLine) {
			t.Errorf("secretGetResult(`[\"probe\"]`) logged %q, want a line containing %q", got, wantLine)
		}
	})

	t.Run("well_formed_params", func(t *testing.T) {
		logs := captureLogs(t)
		secretGetResult(nil, rawParams(t, map[string]string{"key": probeKey}))
		if got := logs.String(); strings.Contains(got, wantLine) {
			t.Errorf("secretGetResult(well-formed params) logged %q, want no %q line", got, wantLine)
		}
	})
}

// startedChatBridge spawns one chat bridge with opts and returns the fake it started.
func startedChatBridge(t *testing.T, opts ...Option) *fakeBridge {
	t.Helper()
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs, opts...)
	cs.wire(h)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed the chat: %v", err)
	}
	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if br.lastStartOpts() == nil {
		t.Fatal("the bridge was never started, so there is nothing to assert on")
	}
	return br
}

// TestChatSpawn_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore pins both directions:
// declared without a store loses every credential; withheld with one re-runs OAuth.
func TestChatSpawn_DeclaresSecretStorageOnlyWhenThisProcessHoldsAStore(t *testing.T) {
	t.Run("a runtime with a credential store declares the capability", func(t *testing.T) {
		br := startedChatBridge(t, WithConfigDir(t.TempDir()))
		if !br.lastStartOpts().SecretStorage {
			t.Error("a runtime holding a credential store did not declare secretStorage, " +
				"so KAS never asks it to persist one and MCP OAuth re-registers per spawn")
		}
	})

	t.Run("a runtime without one declares it off", func(t *testing.T) {
		br := startedChatBridge(t)
		if br.lastStartOpts().SecretStorage {
			t.Error("a runtime with no credential store declared secretStorage, so KAS " +
				"hands it credentials that go nowhere")
		}
	})
}
