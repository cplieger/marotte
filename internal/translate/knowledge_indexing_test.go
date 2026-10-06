package translate

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func knowledgeIndexingPayloads(t *testing.T, events []marotte.ServerEvent) []marotte.KnowledgeIndexingPayload {
	t.Helper()
	var got []marotte.KnowledgeIndexingPayload
	for _, e := range events {
		if e.Type != marotte.EventKnowledgeIndexing {
			continue
		}
		if e.ChatID != "c1" {
			t.Errorf("knowledge_indexing chat = %q, want c1", e.ChatID)
		}
		p, ok := e.Payload.(marotte.KnowledgeIndexingPayload)
		if !ok {
			t.Fatalf("knowledge_indexing payload type = %T", e.Payload)
		}
		got = append(got, p)
	}
	return got
}

func TestHandleKnowledgeIndexing(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   *marotte.KnowledgeIndexingPayload
		name   string
		phase  marotte.KnowledgeIndexingPhase
	}{
		{
			name:   "started_on_the_chats_own_session",
			phase:  marotte.KnowledgeIndexingStarted,
			params: map[string]any{"sessionId": "sess-parent", "name": "docs", "fileCount": 12},
			want:   &marotte.KnowledgeIndexingPayload{Name: "docs", Phase: marotte.KnowledgeIndexingStarted, FileCount: 12},
		},
		{
			name:   "completed_success_carries_the_item_count",
			phase:  marotte.KnowledgeIndexingCompleted,
			params: map[string]any{"sessionId": "sess-parent", "name": "docs", "status": "success", "itemCount": 40},
			want: &marotte.KnowledgeIndexingPayload{
				Name: "docs", Phase: marotte.KnowledgeIndexingCompleted,
				Status: marotte.KnowledgeIndexingSuccess, ItemCount: 40,
			},
		},
		{
			name:   "completed_failed_has_no_item_count",
			phase:  marotte.KnowledgeIndexingCompleted,
			params: map[string]any{"sessionId": "sess-parent", "name": "docs", "status": "failed"},
			want: &marotte.KnowledgeIndexingPayload{
				Name: "docs", Phase: marotte.KnowledgeIndexingCompleted, Status: marotte.KnowledgeIndexingFailed,
			},
		},
		{
			name:   "a_control_character_in_the_name_is_cleaned",
			phase:  marotte.KnowledgeIndexingStarted,
			params: map[string]any{"sessionId": "sess-parent", "name": "do\x1bcs", "fileCount": 1},
			want:   &marotte.KnowledgeIndexingPayload{Name: "do cs", Phase: marotte.KnowledgeIndexingStarted, FileCount: 1},
		},
		{
			name:   "a_subagent_session_is_not_the_chats_index",
			phase:  marotte.KnowledgeIndexingStarted,
			params: map[string]any{"sessionId": "sess-child", "name": "docs", "fileCount": 3},
		},
		{
			name:   "a_frame_with_no_session_is_dropped",
			phase:  marotte.KnowledgeIndexingStarted,
			params: map[string]any{"name": "docs", "fileCount": 3},
		},
		{
			name:   "an_empty_name_is_dropped",
			phase:  marotte.KnowledgeIndexingStarted,
			params: map[string]any{"sessionId": "sess-parent", "name": "  ", "fileCount": 3},
		},
		{
			name:   "an_unknown_completion_status_is_dropped",
			phase:  marotte.KnowledgeIndexingCompleted,
			params: map[string]any{"sessionId": "sess-parent", "name": "docs", "status": "partial"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, events := newEventCaptureDeps()
			deps.parent = "sess-parent"
			tr := New(rolesOf(deps))

			tr.HandleKnowledgeIndexing(tc.phase)(t.Context(), "c1", &marotte.RPCResponse{Params: mustJSON(t, tc.params)})

			got := knowledgeIndexingPayloads(t, *events)
			if tc.want == nil {
				if len(got) != 0 {
					t.Errorf("HandleKnowledgeIndexing(%v) broadcast %+v, want nothing", tc.params, got)
				}
				return
			}
			if len(got) != 1 || got[0] != *tc.want {
				t.Errorf("HandleKnowledgeIndexing(%v) = %+v, want [%+v]", tc.params, got, *tc.want)
			}
		})
	}
}

func TestHandleKnowledgeIndexing_MalformedParamsAreDropped(t *testing.T) {
	deps, events := newEventCaptureDeps()
	deps.parent = "sess-parent"
	tr := New(rolesOf(deps))

	tr.HandleKnowledgeIndexing(marotte.KnowledgeIndexingStarted)(t.Context(), "c1", &marotte.RPCResponse{Params: []byte(`"nope"`)})

	if got := knowledgeIndexingPayloads(t, *events); len(got) != 0 {
		t.Errorf("malformed params broadcast %+v, want nothing", got)
	}
}
