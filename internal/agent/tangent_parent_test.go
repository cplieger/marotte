package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

func sessionRowsJSON(t *testing.T, parents map[string]string) json.RawMessage {
	t.Helper()
	type kiro struct {
		ParentSessionID string `json:"parentSessionId,omitempty"`
	}
	type rowJSON struct {
		SessionID string `json:"sessionId"`
		Meta      struct {
			Kiro kiro `json:"kiro"`
		} `json:"_meta"`
	}
	rows := make([]rowJSON, 0, len(parents))
	for sid, p := range parents {
		var r rowJSON
		r.SessionID = sid
		r.Meta.Kiro.ParentSessionID = p
		rows = append(rows, r)
	}
	raw, err := json.Marshal(map[string]any{"sessions": rows})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func seedChain(t *testing.T, cs *testChatStore, chatID marotte.ChatID, sessions ...string) {
	t.Helper()
	if _, err := cs.Mutate(t.Context(), chatID, func(c *marotte.Chat, _ bool) bool {
		c.Name = string(chatID)
		for _, sid := range sessions {
			c.RecordSession(sid)
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", chatID, err)
	}
}

func TestTangentParent_FollowsKASsParentLinkOverTheChain(t *testing.T) {
	cases := map[string]struct {
		parents map[string]string
		seed    func(*testing.T, *testChatStore)
		want    marotte.ChatID
		wantErr error
	}{
		"a successor session: the link is on the forked one": {
			parents: map[string]string{"s-fork": "s-parent", "s-next": "", "s-parent": ""},
			seed: func(t *testing.T, cs *testChatStore) {
				seedChain(t, cs, "c-parent", "s-parent")
				seedChain(t, cs, "c-tangent", "s-fork", "s-next")
			},
			want: "c-parent",
		},
		"the forked session revisited: the link is on the chain's last": {
			parents: map[string]string{"s-fork": "s-parent", "s-next": ""},
			seed: func(t *testing.T, cs *testChatStore) {
				seedChain(t, cs, "c-parent", "s-parent")
				seedChain(t, cs, "c-tangent", "s-fork", "s-next", "s-fork")
			},
			want: "c-parent",
		},
		"the parent chat was deleted": {
			parents: map[string]string{"s-fork": "s-parent"},
			seed: func(t *testing.T, cs *testChatStore) {
				seedChain(t, cs, "c-tangent", "s-fork")
			},
			wantErr: command.ErrTangentParentGone,
		},
		"an ordinary chat": {
			parents: map[string]string{"s-own": ""},
			seed: func(t *testing.T, cs *testChatStore) {
				seedChain(t, cs, "c-tangent", "s-own")
			},
			wantErr: command.ErrNotATangent,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, cs, br := newTestHub()
			tc.seed(t, cs)
			br.callResults = map[string]json.RawMessage{marotte.MethodSessionList: sessionRowsJSON(t, tc.parents)}

			got, err := h.TangentParent(t.Context(), "c-tangent")
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("TangentParent = (%q, %v), want (%q, %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestTangentParent_AnUnreadableListIsNeitherAnswer(t *testing.T) {
	h, cs, br := newTestHub()
	seedChain(t, cs, "c-tangent", "s-fork")
	br.callErrs = map[string]error{marotte.MethodSessionList: errors.New("kas gone")}

	_, err := h.TangentParent(t.Context(), "c-tangent")
	if err == nil || errors.Is(err, command.ErrNotATangent) || errors.Is(err, command.ErrTangentParentGone) {
		t.Errorf("TangentParent = %v, want the read failure, not a verdict", err)
	}
}

func TestTurnReply_JoinsTheMainLaneSays(t *testing.T) {
	h, cs, _ := newTestHub()
	ctx := t.Context()
	opened, err := cs.OpenTurn(ctx, "c1", &chat.TurnSpec{
		Source: marotte.TurnSourcePrompt.Name(),
		Prompt: &marotte.EntryPrompt{ID: "m-1", Text: "summarize"},
	}, func(c *marotte.Chat) { c.Name = "c1" })
	if err != nil {
		t.Fatalf("OpenTurn: %v", err)
	}
	turn := opened.Turn
	appendEntry := func(id, lane string, kind marotte.EntryKind, payload any) {
		raw, mErr := json.Marshal(payload)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if aErr := cs.Append(ctx, "c1", &marotte.Entry{ID: id, Turn: turn, Kind: kind, Lane: lane, Payload: raw}); aErr != nil {
			t.Fatalf("Append %s: %v", id, aErr)
		}
	}
	appendEntry("s1", "", marotte.EntryKindText, marotte.EntryText{Text: "First part, "})
	appendEntry("s2", "", marotte.EntryKindText, marotte.EntryText{Text: "still first."})
	appendEntry("sub", "lane-1", marotte.EntryKindText, marotte.EntryText{Text: "a subagent's words"})
	appendEntry("tc", "", marotte.EntryKindToolCall, map[string]any{"tool_call_id": "t1"})
	appendEntry("s3", "", marotte.EntryKindText, marotte.EntryText{Text: "Second part."})

	got, err := h.TurnReply(ctx, "c1", turn)
	if want := "First part, still first.\n\nSecond part."; err != nil || got != want {
		t.Errorf("TurnReply = (%q, %v), want (%q, nil)", got, err, want)
	}
}
