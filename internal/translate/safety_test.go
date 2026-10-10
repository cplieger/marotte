package translate

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func safetyStatusPayloads(t *testing.T, events *[]marotte.ServerEvent) []marotte.SafetyStatusPayload {
	t.Helper()
	var got []marotte.SafetyStatusPayload
	for _, e := range *events {
		if e.Type != marotte.EventSafetyStatus {
			continue
		}
		p, ok := e.Payload.(marotte.SafetyStatusPayload)
		if !ok {
			t.Fatalf("EventSafetyStatus payload type = %T, want marotte.SafetyStatusPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

func safetyPropsPayloads(t *testing.T, events *[]marotte.ServerEvent) []marotte.SafetyPropertiesPayload {
	t.Helper()
	var got []marotte.SafetyPropertiesPayload
	for _, e := range *events {
		if e.Type != marotte.EventSafetyProperties {
			continue
		}
		p, ok := e.Payload.(marotte.SafetyPropertiesPayload)
		if !ok {
			t.Fatalf("EventSafetyProperties payload type = %T, want marotte.SafetyPropertiesPayload", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

// TestHandleSafetyStatusChanged_Blocked pins that a blocked status translates
// to one safety_status event carrying the status, detail, tool id, and the
// violated properties.
func TestHandleSafetyStatusChanged_Blocked(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"status":            "blocked",
		"detail":            "\U0001F6E1\uFE0F fs_write blocked",
		"toolId":            "fs_write",
		"blockedProperties": []string{"no public S3 buckets"},
	})})

	got := safetyStatusPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_status count = %d, want 1", len(got))
	}
	p := got[0]
	if p.Status != marotte.SafetyStatusBlocked {
		t.Errorf("Status = %q, want blocked", p.Status)
	}
	if p.ToolID != "fs_write" {
		t.Errorf("ToolID = %q, want fs_write", p.ToolID)
	}
	if len(p.BlockedProperties) != 1 || p.BlockedProperties[0] != "no public S3 buckets" {
		t.Errorf("BlockedProperties = %+v, want one entry", p.BlockedProperties)
	}
}

// TestHandleSafetyStatusChanged_IdleForwarded pins that idle is forwarded too
// (the client uses it to clear a stale banner).
func TestHandleSafetyStatusChanged_IdleForwarded(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "idle",
	})})

	got := safetyStatusPayloads(t, events)
	if len(got) != 1 || got[0].Status != marotte.SafetyStatusIdle {
		t.Fatalf("want one idle safety_status, got %+v", got)
	}
}

// TestHandleSafetyStatusChanged_UnknownDropped pins that an unrecognized
// status is dropped rather than surfaced as a mystery banner.
func TestHandleSafetyStatusChanged_UnknownDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "quantum-entangled",
	})})

	if got := safetyStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for unknown status, got %+v", got)
	}
}

// TestHandleSafetyStatusChanged_MalformedNoop pins defensive decode.
func TestHandleSafetyStatusChanged_MalformedNoop(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))
	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: []byte("{")})
	if got := safetyStatusPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for malformed params, got %+v", got)
	}
}

// TestHandleSafetyPropertiesChanged_ObjectForm pins that object-form properties
// ({index, description, enabled}) translate to one safety_properties event.
func TestHandleSafetyPropertiesChanged_ObjectForm(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"sessionId": "",
		"reason":    "formalized",
		"properties": []map[string]any{
			{"index": 0, "description": "no public S3 buckets", "enabled": true},
			{"index": 1, "description": "encrypt EBS volumes", "enabled": true},
		},
	})})

	got := safetyPropsPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_properties count = %d, want 1", len(got))
	}
	if got[0].Reason != "formalized" {
		t.Errorf("Reason = %q, want formalized", got[0].Reason)
	}
	if len(got[0].Properties) != 2 || got[0].Properties[0].Description != "no public S3 buckets" {
		t.Errorf("Properties = %+v, want 2 formalized entries", got[0].Properties)
	}
}

// TestHandleSafetyPropertiesChanged_StringForm pins the tolerant decode: bare
// string properties (the getProperties path) become descriptions with
// Enabled=true.
func TestHandleSafetyPropertiesChanged_StringForm(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"properties": []any{"no public S3 buckets", ""},
	})})

	got := safetyPropsPayloads(t, events)
	if len(got) != 1 {
		t.Fatalf("safety_properties count = %d, want 1", len(got))
	}
	if len(got[0].Properties) != 1 {
		t.Fatalf("Properties = %+v, want 1 (empty string dropped)", got[0].Properties)
	}
	if got[0].Properties[0].Description != "no public S3 buckets" || !got[0].Properties[0].Enabled {
		t.Errorf("Property = %+v, want description set + Enabled=true", got[0].Properties[0])
	}
}

// TestHandleSafetyPropertiesChanged_EmptyDropped pins that a notification with
// no usable properties produces no broadcast.
func TestHandleSafetyPropertiesChanged_EmptyDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(rolesOf(deps))

	tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"properties": []any{},
	})})

	if got := safetyPropsPayloads(t, events); len(got) != 0 {
		t.Fatalf("want no broadcast for empty properties, got %+v", got)
	}
}

// TestHandleSafetyPropertiesChanged_SkipsSubagent pins that a subagent-keyed
// copy is skipped (properties belong to the parent chat's surface).
func TestHandleSafetyPropertiesChanged_SkipsSubagent(t *testing.T) {
	t.Run("SubagentSkipped", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
			"sessionId":  "sess-sub",
			"properties": []any{"no public S3 buckets"},
		})})
		if got := safetyPropsPayloads(t, events); len(got) != 0 {
			t.Fatalf("want no broadcast for subagent-keyed copy, got %+v", got)
		}
	})
	t.Run("ParentProcessed", func(t *testing.T) {
		deps, events := newEventCaptureDeps()
		deps.parent = "sess-parent"
		tr := New(rolesOf(deps))
		tr.HandleSafetyPropertiesChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
			"sessionId":  "sess-parent",
			"properties": []any{"no public S3 buckets"},
		})})
		if got := safetyPropsPayloads(t, events); len(got) != 1 {
			t.Fatalf("want one broadcast for parent-keyed copy, got %+v", got)
		}
	})
}

// safetyBlocksOf decodes every safety_blocked entry the chat holds: sealed in its
// open turn or filed after its newest close.
func safetyBlocksOf(t *testing.T, deps *baseDeps, chatID marotte.ChatID) []marotte.EntrySafetyBlocked {
	t.Helper()
	var out []marotte.EntrySafetyBlocked
	for _, entries := range [][]marotte.Entry{deps.chatEntries(chatID), deps.between[chatID]} {
		for i := range entries {
			if entries[i].Kind == marotte.EntryKindSafetyBlocked {
				out = append(out, decodePayload[marotte.EntrySafetyBlocked](t, &entries[i]))
			}
		}
	}
	return out
}

// depsWithStore wires an InMemoryChatStore into event-capturing deps and seeds
// chatID, so the header reads the handlers make find a record.
func depsWithStore(t *testing.T, chatID marotte.ChatID) (*baseDeps, *[]marotte.ServerEvent, *testsupport.InMemoryChatStore) {
	t.Helper()
	deps, events := newEventCaptureDeps()
	store := testsupport.NewInMemoryChatStore()
	deps.store = store
	if _, err := store.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	return deps, events, store
}

// TestHandleSafetyStatusChanged_BlockedPersistsEvent pins a durable safety_blocked entry with
// the violated properties, in addition to the transient banner.
func TestHandleSafetyStatusChanged_BlockedPersistsEvent(t *testing.T) {
	deps, events, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"status":            "blocked",
		"detail":            "\U0001F6E1\uFE0F fs_write blocked",
		"toolId":            "fs_write",
		"blockedProperties": []string{"no public S3 buckets", "encrypt at rest"},
	})})

	// Transient banner SSE still fires.
	if got := safetyStatusPayloads(t, events); len(got) != 1 || got[0].Status != marotte.SafetyStatusBlocked {
		t.Fatalf("want one blocked safety_status broadcast, got %+v", got)
	}
	// Permanent record: exactly one safety_blocked entry carrying the WHY.
	blocks := safetyBlocksOf(t, deps, "c1")
	if len(blocks) != 1 {
		t.Fatalf("safety_blocked entry count = %d, want 1", len(blocks))
	}
	if want := []string{"no public S3 buckets", "encrypt at rest"}; !slices.Equal(blocks[0].Properties, want) {
		t.Errorf("Properties = %q, want the violated properties %q", blocks[0].Properties, want)
	}
	if !hasEntryAppended(events, marotte.EntryKindSafetyBlocked) {
		t.Error("no entry_appended{safety_blocked} frame: the record is born sealed and must be announced")
	}
}

// TestHandleSafetyStatusChanged_BlockedFallsBackToDetail pins that a block with
// no properties records the gate's detail rather than an empty event.
func TestHandleSafetyStatusChanged_BlockedFallsBackToDetail(t *testing.T) {
	deps, _, _ := depsWithStore(t, "c1")
	tr := New(rolesOf(deps))

	tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
		"status": "blocked",
		"detail": "policy violation",
	})})

	blocks := safetyBlocksOf(t, deps, "c1")
	if len(blocks) != 1 || !slices.Equal(blocks[0].Properties, []string{"policy violation"}) {
		t.Fatalf("want one safety_blocked entry with the detail as its one property, got %+v", blocks)
	}
}

// TestHandleSafetyStatusChanged_NonBlockedNoPersist pins that in-progress and
// all-clear statuses only broadcast the transient banner and never persist a
// permanent event (only enforce-mode blocks are durable facts).
func TestHandleSafetyStatusChanged_NonBlockedNoPersist(t *testing.T) {
	for _, status := range []string{"idle", "formalizing", "evaluating", "error"} {
		t.Run(status, func(t *testing.T) {
			deps, _, _ := depsWithStore(t, "c1")
			tr := New(rolesOf(deps))

			tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
				"status": status,
			})})

			if blocks := safetyBlocksOf(t, deps, "c1"); len(blocks) != 0 {
				t.Fatalf("status %q persisted %d safety_blocked entr(ies), want 0", status, len(blocks))
			}
		})
	}
}

// TestHandleSafetyStatusChanged_BlockPersistSpeaksOnlyOnFailure pins a log line only when the
// persist fails.
func TestHandleSafetyStatusChanged_BlockPersistSpeaksOnlyOnFailure(t *testing.T) {
	tests := []struct {
		appendErr  error
		name       string
		wantLogged bool
	}{
		{name: "a_successful_append_is_silent", appendErr: nil, wantLogged: false},
		{name: "a_failed_append_reports_itself", appendErr: errBoom, wantLogged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			t.Cleanup(captureSlog(&logs))
			deps, _ := newEventCaptureDeps()
			deps.turns.appendErr = tc.appendErr
			tr := New(rolesOf(deps))

			tr.HandleSafetyStatusChanged(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, map[string]any{
				"status":            "blocked",
				"detail":            "fs_write blocked",
				"toolId":            "fs_write",
				"blockedProperties": []string{"no public S3 buckets"},
			})})

			got := strings.Contains(logs.String(), `msg="entry log: append refused; the frame is dropped"`) &&
				strings.Contains(logs.String(), "entry=safety_blocked")
			if got != tc.wantLogged {
				t.Errorf("HandleSafetyStatusChanged(blocked, appendErr=%v) logged the append error = %t, want %t; logs = %q",
					tc.appendErr, got, tc.wantLogged, logs.String())
			}
		})
	}
}
