package translate

import (
	"bytes"
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

// lateWrite is one handler that persists something AFTER the frame that caused it.
type lateWrite func(*Translator, context.Context, marotte.ChatID)

// headerWrites are the sites that write the chat HEADER through chatRecords.Mutate, keyed
// by the log message the site emits on a real failure.
func headerWrites() map[string]lateWrite {
	permID := int64(1)
	return map[string]lateWrite{
		"mode update persist": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandleModeUpdate(ctx, id, mustJSONCtx(map[string]any{"currentModeId": "spec"}))
		},
		"focus title: persist": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.handleFocusUpdate(ctx, id, &focusUpdate{Title: "Agent picked this"})
		},
		"agent_not_found: persist fallback": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandleAgentNotFound(ctx, id, &marotte.RPCResponse{
				ID:     &permID,
				Params: mustJSONCtx(map[string]any{"requestedAgent": "nope", "fallbackAgent": "vibe"}),
			})
		},
		"persist v3 usage": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandleUsageUpdate(ctx, id, mustJSONCtx(map[string]any{"size": 100, "used": 50}))
		},
		"persist v3 config catalog": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandleConfigOptionUpdate(ctx, id, mustJSONCtx(map[string]any{
				"configOptions": []map[string]any{{
					"id":      "model",
					"type":    "select",
					"options": []map[string]any{{"name": "Opus", "value": "claude-opus-5"}},
				}},
			}), FrameAttribution{})
		},
	}
}

// entryWrites are the sites that append an ENTRY to the chat's log, in the open turn
// or between turns. Keyed by the entry kind appendFailed names on a refusal.
func entryWrites() map[string]lateWrite {
	return map[string]lateWrite{
		"plan": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandlePlan(ctx, id, mustJSONCtx(map[string]any{
				"entries": []map[string]any{{"content": "step", "status": "pending"}},
			}), FrameAttribution{})
		},
		"compaction": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			summary := "rolled up"
			tr.handleCompactionCompleted(ctx, id, &summary)
		},
		"compaction_failed": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.handleCompactionFailed(ctx, id, "out of context")
		},
		"safety_blocked": func(tr *Translator, ctx context.Context, id marotte.ChatID) {
			tr.HandleSafetyStatusChanged(ctx, id, &marotte.RPCResponse{
				Params: mustJSONCtx(map[string]any{"status": "blocked", "detail": "refused"}),
			})
		},
	}
}

// refusingDeps is a double whose store refuses every header write and read with
// storeErr and whose log refuses every append with appendErr.
func refusingDeps(storeErr, appendErr error) (*baseDeps, *[]marotte.ServerEvent, *recStore) {
	deps, events := newEventCaptureDeps()
	store := &recStore{err: storeErr}
	deps.store = store
	deps.turns.appendErr = appendErr
	return deps, events, store
}

// TestLateWrites_TombstonedRefusalIsNotAnError pins the drop for a chat deleted inside the
// tombstone window: every write races a possible delete, so logging it as an ERROR would
// flag the mechanism working as intended. Entry appends answer the same way for any
// refusal, so the header sites are the ones this discriminates.
func TestLateWrites_TombstonedRefusalIsNotAnError(t *testing.T) {
	writes := headerWrites()
	maps.Copy(writes, entryWrites())
	for name, drive := range writes {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			defer captureSlog(&logs)()
			deps, _, _ := refusingDeps(chat.ErrTombstoned, chat.ErrTombstoned)
			tr := New(rolesOf(deps))

			drive(tr, t.Context(), "c1")

			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a tombstoned write logged an error:\n%s", logs.String())
			}
		})
	}
}

// TestLateWrites_ARefusedHeaderWriteLogsAnError keeps the drop narrow: a real persist
// failure (full disk, permission, corrupt file) still logs an error.
func TestLateWrites_ARefusedHeaderWriteLogsAnError(t *testing.T) {
	for name, drive := range headerWrites() {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			defer captureSlog(&logs)()
			deps, _, _ := refusingDeps(errBoom, nil)
			tr := New(rolesOf(deps))

			drive(tr, t.Context(), "c1")

			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("a real persist failure logged no error:\n%s", logs.String())
			}
		})
	}
}

// TestLateWrites_ARefusedAppendIsReportedAtWarn pins that a refused append is reported once
// per frame at Warn, naming the entry kind: the store latches a refused write for the
// process's life, so an ERROR per frame would flood the log.
func TestLateWrites_ARefusedAppendIsReportedAtWarn(t *testing.T) {
	for name, drive := range entryWrites() {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			defer captureSlog(&logs)()
			deps, _, _ := refusingDeps(nil, errBoom)
			tr := New(rolesOf(deps))

			drive(tr, t.Context(), "c1")

			got := logs.String()
			if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "append refused") {
				t.Errorf("a refused append was not reported at Warn:\n%s", got)
			}
			if !strings.Contains(got, "entry="+name) {
				t.Errorf("the refusal did not name the entry kind %q:\n%s", name, got)
			}
			if strings.Contains(got, "level=ERROR") {
				t.Errorf("a refused append logged an error:\n%s", got)
			}
		})
	}
}

// TestHandleCompactionFailed_TombstonedChatGetsNoBanner pins the wire-observable drop: a
// refused append means no client holds the chat, so no banner is sent.
func TestHandleCompactionFailed_TombstonedChatGetsNoBanner(t *testing.T) {
	deps, events, _ := refusingDeps(chat.ErrTombstoned, chat.ErrTombstoned)
	tr := New(rolesOf(deps))

	tr.handleCompactionFailed(t.Context(), "c1", "out of context")

	for _, e := range *events {
		if e.Type == marotte.EventError {
			t.Errorf("broadcast %s for a deleted chat", e.Type)
		}
	}
}

// TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite pins the return: the
// watermark Mutate after a refused append could only be refused too.
func TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite(t *testing.T) {
	deps, _, store := refusingDeps(nil, chat.ErrTombstoned)
	tr := New(rolesOf(deps))

	summary := "rolled up"
	tr.handleCompactionCompleted(t.Context(), "c1", &summary)

	if store.mutateCalls != 0 {
		t.Errorf("mutateCalls = %d, want 0: the chat is gone, so there is nothing to watermark", store.mutateCalls)
	}
}

// TestHandleCompactionCompleted_ADeletedChatsCountReadIsNotAnError pins that the log read
// of a deleted chat answering ErrChatNotFound is not a fault.
func TestHandleCompactionCompleted_ADeletedChatsCountReadIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	defer captureSlog(&logs)()
	deps, _, store := refusingDeps(chat.ErrChatNotFound, nil)
	tr := New(rolesOf(deps))

	summary := "rolled up"
	tr.handleCompactionCompleted(t.Context(), "c1", &summary)

	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a deleted chat's count read logged an error:\n%s", logs.String())
	}
	if store.mutateCalls != 0 {
		t.Errorf("mutateCalls = %d, want 0: the chat is gone, so there is nothing to watermark", store.mutateCalls)
	}
	if got := deps.between["c1"]; len(got) != 0 {
		t.Errorf("appended %d entries to a deleted chat, want 0", len(got))
	}
}

// TestHandleFocusUpdate_ADeletedChatsPromptReadIsNotAnError is the same rule on the title
// path's prompt read; any other read failure drops the title at ERROR.
func TestHandleFocusUpdate_ADeletedChatsPromptReadIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	defer captureSlog(&logs)()
	deps, _, store := refusingDeps(chat.ErrChatNotFound, nil)
	tr := New(rolesOf(deps))

	tr.handleFocusUpdate(t.Context(), "c1", &focusUpdate{Title: "Agent picked this"})

	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a deleted chat's prompt read logged an error:\n%s", logs.String())
	}
	if store.mutateCalls != 0 {
		t.Errorf("mutateCalls = %d, want 0: the chat is gone, so there is no header to title", store.mutateCalls)
	}
}

// mustJSONCtx is mustJSON without a *testing.T: a case's payload is built when the table is.
func mustJSONCtx(v any) []byte {
	return mustJSONRapid(v)
}
