package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/marotte"
)

// rawCall is one connection-level request on the engine, its in-band refusal an error: callers must tell an unknown
// workflow from a fault and a refused cancel from a landed one. label prefixes errors, wrapped so rpcerr.Details
// reaches the *marotte.RPCError.
func (o *engineOwner) rawCall(ctx context.Context, label, method string, params map[string]any) (json.RawMessage, error) {
	raw, _, err := o.callRawAt(ctx, label, method, params)
	return raw, err
}

// callRawAt is rawCall plus the engine-lane position the reply arrived at: KAS replays a step's session as
// notifications before the load result, so ordering a decision against them needs it.
func (o *engineOwner) callRawAt(ctx context.Context, label, method string, params map[string]any) (json.RawMessage, drainPoint, error) {
	resp, at, err := o.callAt(ctx, method, params)
	if err != nil {
		return nil, at, fmt.Errorf("%s: %w", label, err)
	}
	if resp == nil {
		return nil, at, errors.New(label + ": nil response")
	}
	if resp.Error != nil {
		return nil, at, fmt.Errorf("%s: %w", label, resp.Error)
	}
	return resp.Result, at, nil
}

// accountUsageRaw reads the account's usage; KAS's handler reads only the sign-in, never a session.
func (o *engineOwner) accountUsageRaw(ctx context.Context) (json.RawMessage, error) {
	return o.rawCall(ctx, "account usage call", methodKiroGetUsage, map[string]any{})
}

// No sessionId: that targets the workspace-global default store.
func (o *engineOwner) knowledgeRaw(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	return o.rawCall(ctx, "knowledge call", methodKiroKnowledge, params)
}

// memoryRaw issues a _kiro/memory/* request without a sessionId, so KAS resolves memory from the account entitlement.
func (o *engineOwner) memoryRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return o.rawCall(ctx, "memory call "+method, method, params)
}

func (o *engineOwner) hooksRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return o.rawCall(ctx, fmt.Sprintf("hooks call %s", method), method, params)
}

// configTemplateRaw issues the session-less _kiro/config/template request.
func (o *engineOwner) configTemplateRaw(ctx context.Context) (json.RawMessage, error) {
	return o.rawCall(ctx, "config template call", methodKiroConfigTemplate, map[string]any{})
}

// workspaceConfigurationRaw asks KAS for its composed configuration as a workspace observer of roots: the agents,
// steering and settings a session opened there would load.
func (o *engineOwner) workspaceConfigurationRaw(ctx context.Context, roots []string) (json.RawMessage, error) {
	return o.rawCall(ctx, "configuration getState call", methodKiroConfigurationGet,
		map[string]any{"observer": "workspace", "roots": roots})
}

// No triggerHook wrapper: it runs `sh -c` on a command a hook file names.

func (o *engineOwner) renameSessionRaw(ctx context.Context, sessionID, title string) (json.RawMessage, error) {
	return o.rawCall(ctx, "session rename", marotte.MethodSessionRename,
		map[string]any{marotte.KeySessionID: sessionID, "title": title})
}

// KAS loads it from disk.
func (o *engineOwner) exportSessionRaw(ctx context.Context, sessionID string) (json.RawMessage, error) {
	return o.rawCall(ctx, "session export", marotte.MethodSessionExport,
		map[string]any{marotte.KeySessionID: sessionID})
}

// observeBudget bounds one observer pass's door and calls; its close runs under the engine's own budget.
const observeBudget = 60 * time.Second

// observe runs fn on an observer pass: a session opened on the engine with presets, no mode and no prompt, which
// closePass tries to end when fn returns. KAS answers the policy, code-intelligence and governance reads only for a
// resident session, and a pass that opens with the current presets answers for the profile as it is now. attach
// consumes the pass's own frames (its workspace notifications and command list) from before the door writes; nil
// drains them.
func (o *engineOwner) observe(
	ctx context.Context, presets []string, attach func(engineSession), fn func(ctx context.Context, s engineSession) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, observeBudget)
	defer cancel()
	s, err := o.openSession(ctx, &marotte.SessionOpts{Role: marotte.SessionRoleObserver, Presets: presets},
		func(s engineSession) {
			if attach != nil {
				attach(s)
				return
			}
			go drainSession(s)
		})
	if err != nil {
		return fmt.Errorf("observer pass: %w", err)
	}
	defer o.closePass(durable.Context(ctx), s)
	return fn(ctx, s)
}

// closePass ends a pass's session, best effort. A refused close leaves it live in KAS and on its lane, so the session
// is deleted on the engine, which KAS refuses only for a session owning a live workflow (a pass never does), and
// closed again, which KAS answers {} for a session it no longer holds. A delete or second close that still fails is
// logged at WARN and leaves the session open until the engine ends.
func (o *engineOwner) closePass(ctx context.Context, s engineSession) {
	err := s.Close(ctx)
	if _, refused := errors.AsType[*marotte.RPCError](err); refused {
		dctx, cancel := context.WithTimeout(ctx, sessionDeleteTimeout)
		_, err = o.rawCall(dctx, "observer pass delete", marotte.MethodSessionDelete,
			map[string]any{marotte.KeySessionID: s.SessionID()})
		cancel()
		if err == nil {
			err = s.Close(ctx)
		}
	}
	if err != nil {
		slog.Warn("an observer pass did not close its session", "session_id", s.SessionID(), "error", err)
	}
}

// drainSession reads a session's frames to its close, answering none: an observer that consumes nothing.
func drainSession(s engineSession) {
	for n := range s.NotifCh() {
		if n.Msg != nil && n.Msg.ID != nil && n.Msg.Method != "" {
			_ = s.Respond(context.Background(), *n.Msg.ID, nil, &marotte.RPCError{
				Code: marotte.RPCCodeMethodNotFound, Message: "an observer session runs no tools: " + n.Msg.Method,
			})
		}
	}
}

// observerCall is one session-scoped call on a pass's session, its id added to params.
func observerCall(ctx context.Context, s engineSession, label, method string, extra map[string]any) (json.RawMessage, error) {
	params := map[string]any{marotte.KeySessionID: s.SessionID()}
	maps.Copy(params, extra)
	resp, err := s.Call(ctx, method, params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if resp == nil {
		return nil, errors.New(label + ": nil response")
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %w", label, resp.Error)
	}
	return resp.Result, nil
}
