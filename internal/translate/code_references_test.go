package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/turnlog"
)

// codeRefsPayload extracts the CodeReferencesPayload from the single
// EventCodeReferences broadcast, or fails if there isn't exactly one.
func codeRefsPayload(t *testing.T, events *[]marotte.ServerEvent) marotte.CodeReferencesPayload {
	t.Helper()
	var got []marotte.CodeReferencesPayload
	for _, e := range *events {
		if e.Type != marotte.EventCodeReferences {
			continue
		}
		p, ok := e.Payload.(marotte.CodeReferencesPayload)
		if !ok {
			t.Fatalf("EventCodeReferences payload type = %T, want marotte.CodeReferencesPayload", e.Payload)
		}
		got = append(got, p)
	}
	if len(got) != 1 {
		t.Fatalf("EventCodeReferences broadcast count = %d, want 1", len(got))
	}
	return got[0]
}

func countCodeRefEvents(events *[]marotte.ServerEvent) int {
	n := 0
	for _, e := range *events {
		if e.Type == marotte.EventCodeReferences {
			n++
		}
	}
	return n
}

// startedTurn opens the chat's turn, the one OwnTurn answers with.
func startedTurn(deps *baseDeps, chatID marotte.ChatID) *turnlog.Turn {
	return deps.turns.chatTurn(chatID)
}

func codeRefMsg(t *testing.T, sessionID string, refs []map[string]any) *marotte.RPCResponse {
	t.Helper()
	return &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"sessionId":  sessionID,
		"references": refs,
	})}
}

// TestHandleCodeReferences_HappyPath pins that a well-formed notification on
// an open turn accumulates the references onto the turn and broadcasts exactly
// one code_references event carrying the turn's id and the full list.
func TestHandleCodeReferences_HappyPath(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	turn := startedTurn(deps, chatID)

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "MIT", "repository": "github.com/foo/bar", "url": "https://github.com/foo/bar"},
	}))

	p := codeRefsPayload(t, events)
	if p.Turn != turn.ID() {
		t.Errorf("payload Turn = %q, want %q", p.Turn, turn.ID())
	}
	if len(p.References) != 1 || p.References[0].LicenseName != "MIT" ||
		p.References[0].Repository != "github.com/foo/bar" ||
		p.References[0].URL != "https://github.com/foo/bar" {
		t.Errorf("payload References = %+v, want one MIT/foo/bar reference", p.References)
	}
	if refs := turn.CodeReferences(); len(refs) != 1 {
		t.Errorf("turn CodeReferences = %+v, want 1 accumulated", refs)
	}
}

// TestHandleCodeReferences_DropsEmptyLicense pins that references with no
// license name are filtered (matching KAS's own filter); a notification with
// only such entries produces no broadcast and no accumulation.
func TestHandleCodeReferences_DropsEmptyLicense(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	turn := startedTurn(deps, chatID)

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "", "repository": "github.com/x/y", "url": "https://github.com/x/y"},
	}))

	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (all references had empty license)", n)
	}
	if refs := turn.CodeReferences(); len(refs) != 0 {
		t.Errorf("turn CodeReferences = %+v, want none", refs)
	}
}

// TestHandleCodeReferences_NoTurnInFlight pins that a notification arriving
// with no open turn is dropped: no broadcast, and no turn opened for it, so it
// can't contaminate the next turn.
func TestHandleCodeReferences_NoTurnInFlight(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	// No startedTurn: the chat has no open turn.

	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", []map[string]any{
		{"licenseName": "Apache-2.0", "repository": "github.com/a/b", "url": "https://github.com/a/b"},
	}))

	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (no open turn)", n)
	}
	if deps.turns.chats[chatID] != nil {
		t.Error("a turn was opened for the references; want none (a frame with no turn to join is dropped)")
	}
}

// TestHandleCodeReferences_SkipsSubagentFanout pins the KAS fan-out dedup:
// KAS broadcasts the same references under every live session id, so a copy
// keyed to a subagent session (differing from the parent) is skipped; the
// parent-session copy is processed.
func TestHandleCodeReferences_SkipsSubagentFanout(t *testing.T) {
	t.Run("SubagentCopySkipped", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		startedTurn(deps, chatID)

		tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "sess-sub", []map[string]any{
			{"licenseName": "MIT", "repository": "r", "url": "https://example.com"},
		}))
		if n := countCodeRefEvents(events); n != 0 {
			t.Errorf("broadcast count = %d, want 0 (subagent-keyed copy must be skipped)", n)
		}
	})
	t.Run("ParentCopyProcessed", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		chatID := marotte.ChatID("c1")
		startedTurn(deps, chatID)

		tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "sess-parent", []map[string]any{
			{"licenseName": "MIT", "repository": "r", "url": "https://example.com"},
		}))
		if n := countCodeRefEvents(events); n != 1 {
			t.Errorf("broadcast count = %d, want 1 (parent-keyed copy must be processed)", n)
		}
	})
}

// TestHandleCodeReferences_DedupAcrossNotifications pins that the same
// reference delivered twice (e.g. a completion reproducing the same snippet
// again) accumulates once; the second broadcast still carries a single entry.
func TestHandleCodeReferences_DedupAcrossNotifications(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	turn := startedTurn(deps, chatID)

	ref := []map[string]any{{"licenseName": "MIT", "repository": "r", "url": "https://example.com"}}
	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", ref))
	tr.HandleCodeReferences(t.Context(), chatID, codeRefMsg(t, "", ref))

	if n := countCodeRefEvents(events); n != 2 {
		t.Fatalf("broadcast count = %d, want 2 (one per notification)", n)
	}
	last := (*events)[len(*events)-1]
	p, ok := last.Payload.(marotte.CodeReferencesPayload)
	if !ok {
		t.Fatalf("last payload type = %T", last.Payload)
	}
	if len(p.References) != 1 {
		t.Errorf("deduped References = %+v, want 1 (identical reference must not duplicate)", p.References)
	}
	if refs := turn.CodeReferences(); len(refs) != 1 {
		t.Errorf("turn CodeReferences = %+v, want 1 after dedup", refs)
	}
}

// TestHandleCodeReferences_MalformedParamsNoop pins that malformed params are
// dropped without a broadcast (defensive decode).
func TestHandleCodeReferences_MalformedParamsNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	chatID := marotte.ChatID("c1")
	startedTurn(deps, chatID)

	tr.HandleCodeReferences(t.Context(), chatID, &marotte.RPCResponse{Params: []byte("{")})
	if n := countCodeRefEvents(events); n != 0 {
		t.Errorf("broadcast count = %d, want 0 (malformed params)", n)
	}
}
