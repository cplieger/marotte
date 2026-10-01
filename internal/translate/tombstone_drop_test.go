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

// lateWrite is one handler in this package that persists something AFTER the frame
// that caused it, which is every write it makes.
type lateWrite func(*Translator, context.Context, marotte.ChatID)

// headerWrites are the sites that write the chat HEADER through ChatRecords.Mutate.
// Keyed by the log message the site emits on a real failure, so a case whose drop
// regresses names the exact slog line to look for.
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

// TestLateWrites_TombstonedRefusalIsNotAnError pins the drop for a chat that was
// deleted inside the tombstone window: the mutator never ran, nothing reached disk,
// nothing was broadcast. Every write in this package races a possible delete, so
// surfacing that as a logged error would put an ERROR line in the operator's log
// for the mechanism working as intended, on the most travelled paths in the app,
// several per turn. An entry append answers the same way for any refusal (the
// write-error rule reports at Warn), so the header sites are the ones this
// discriminates.
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

// TestLateWrites_ARefusedHeaderWriteLogsAnError is the other half, and it is what
// keeps the drop narrow: matching the sentinel must not swallow a real persist
// failure, a full disk, a permission fault, a corrupt chat file.
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

// TestLateWrites_ARefusedAppendIsReportedAtWarn pins the write-error rule for the
// entry sites: a refused append is reported once per frame at Warn, naming the
// entry kind, and never as an error. The store latches a refused write for the
// process's life, so an ERROR per frame would flood the log with one fault.
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

// TestHandleCompactionFailed_TombstonedChatGetsNoBanner is the one site whose
// drop is observable on the wire rather than only in the log: a refused append
// means the chat is gone, so no client holds it and the error banner has nobody
// to reach.
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

// TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite pins the return
// rather than the log level: a refused append means the chat is gone, so the
// watermark Mutate that follows it can only be refused too.
func TestHandleCompactionCompleted_TombstonedChatStopsAfterOneWrite(t *testing.T) {
	deps, _, store := refusingDeps(nil, chat.ErrTombstoned)
	tr := New(rolesOf(deps))

	summary := "rolled up"
	tr.handleCompactionCompleted(t.Context(), "c1", &summary)

	if store.mutateCalls != 0 {
		t.Errorf("mutateCalls = %d, want 0: the chat is gone, so there is nothing to watermark", store.mutateCalls)
	}
}

// TestHandleCompactionCompleted_ADeletedChatsCountReadIsNotAnError pins the one
// header-side READ on the compaction path: a deleted chat has no header, so its
// log read answers ErrChatNotFound rather than a tombstone, and that is the delete
// working as intended, not a fault.
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

// TestHandleFocusUpdate_ADeletedChatsPromptReadIsNotAnError is the same rule on the
// title path's READ: the derivation filter reads the log's prompts ahead of the
// header write, and a deleted chat answers that read with ErrChatNotFound. Any
// other read failure drops the title and says so at ERROR, because a title lost to
// a full disk or a corrupt log is the same fault a refused header write is.
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

// mustJSONCtx is mustJSON without a *testing.T, for the tables above: a case's
// payload is built once when the table is composed, outside any subtest.
func mustJSONCtx(v any) []byte {
	return mustJSONRapid(v)
}
