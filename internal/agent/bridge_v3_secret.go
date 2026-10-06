// `_kiro/secret/{get,store,delete}`, answered from internal/secretstore. KAS keeps MCP OAuth
// results only in memory and asks the client to hold them, gated on
// `_meta.kiro.secretStorage`, declared only when a store exists: KAS rethrows a store failure
// into MCP connect. Keys and values are never logged; one store serves every bridge
// because KAS's key namespace is global.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/secretstore"
)

// secretKeyParams is the shape of a get/delete request: `{key}`.
type secretKeyParams struct {
	Key string `json:"key"`
}

// secretStoreParams is the shape of a store request: `{key, value}`.
type secretStoreParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// secretGetBody is the reply to a get. A pointer, so a miss marshals as `null`: KAS reads that as no credential yet.
type secretGetBody struct {
	Value *string `json:"value"`
}

// handleKiroSecretRequest answers the three `_kiro/secret/*` requests, reporting whether msg
// was one. Synchronous: KAS can store and immediately get on the connect path.
func (in *inbound) handleKiroSecretRequest(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) bool {
	switch msg.Method {
	case methodKiroSecretGet:
		in.respondBridge(ctx, chatID, msg, secretGetResult(in.secrets, msg.Params), nil)
		return true
	case methodKiroSecretStore:
		result, err := secretStoreResult(ctx, in.secrets, msg.Params)
		in.respondBridge(ctx, chatID, msg, result, err)
		return true
	case methodKiroSecretDelete:
		result, err := secretDeleteResult(ctx, in.secrets, msg.Params)
		in.respondBridge(ctx, chatID, msg, result, err)
		return true
	default:
		return false
	}
}

// secretGetResult answers `_kiro/secret/get` with `{value}`; a miss and a nil store both
// answer null, never an error.
func secretGetResult(store *secretstore.Store, params json.RawMessage) secretGetBody {
	p := decodeSecretKey(params)
	if store == nil || p.Key == "" {
		return secretGetBody{}
	}
	v, ok := store.Get(p.Key)
	if !ok {
		return secretGetBody{}
	}
	return secretGetBody{Value: &v}
}

// secretStoreResult answers `_kiro/secret/store` with `{}`, or an error: KAS rethrows it into
// MCP connect, so a full disk surfaces instead of an empty read-back.
func secretStoreResult(ctx context.Context, store *secretstore.Store, params json.RawMessage) (map[string]any, error) {
	var p secretStoreParams
	if params != nil {
		if err := json.Unmarshal(params, &p); err != nil {
			slog.Warn("v3 secret store: undecodable params", "error", err)
			return nil, &marotte.RPCError{Code: -32602, Message: "secret/store: params must be {key, value}"}
		}
	}
	if p.Key == "" {
		return nil, &marotte.RPCError{Code: -32602, Message: "secret/store: key is required"}
	}
	if store == nil {
		// Unreachable unless the peer calls a method it was not offered: answered as a protocol error.
		slog.Warn("v3 secret store: no store configured, credential not persisted", "key", p.Key)
		return nil, &marotte.RPCError{Code: -32603, Message: "secret/store: no credential store configured"}
	}
	if err := store.Set(ctx, p.Key, p.Value); err != nil {
		// Key only: the value is a secret.
		slog.Error("v3 secret store: persist failed", "key", p.Key, "error", err)
		return nil, &marotte.RPCError{Code: -32603, Message: "secret/store: " + err.Error()}
	}
	slog.Debug("v3 secret store: persisted", "key", p.Key)
	return map[string]any{}, nil
}

// secretDeleteResult answers `_kiro/secret/delete` with `{}`, or an error KAS rethrows. An absent key succeeds.
func secretDeleteResult(ctx context.Context, store *secretstore.Store, params json.RawMessage) (map[string]any, error) {
	p := decodeSecretKey(params)
	if p.Key == "" {
		return nil, &marotte.RPCError{Code: -32602, Message: "secret/delete: key is required"}
	}
	if store == nil {
		// Nothing was stored, so the key is already absent.
		return map[string]any{}, nil
	}
	if err := store.Delete(ctx, p.Key); err != nil {
		slog.Error("v3 secret delete: persist failed", "key", p.Key, "error", err)
		return nil, &marotte.RPCError{Code: -32603, Message: "secret/delete: " + err.Error()}
	}
	return map[string]any{}, nil
}

// decodeSecretKey pulls `{key}` from params, yielding "" on absent or undecodable params.
func decodeSecretKey(params json.RawMessage) secretKeyParams {
	var p secretKeyParams
	if params != nil {
		if err := json.Unmarshal(params, &p); err != nil {
			slog.Warn("v3 secret: undecodable params", "error", err)
		}
	}
	return p
}
