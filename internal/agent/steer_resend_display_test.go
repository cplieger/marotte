package agent

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

func kasReplayFrame(t *testing.T, kind marotte.ACPUpdateKind, kiro map[string]any, text string) (marotte.ACPUpdateKind, json.RawMessage) {
	t.Helper()
	k := map[string]any{"replay": true}
	maps.Copy(k, kiro)
	u := map[string]any{"sessionUpdate": string(kind), "_meta": map[string]any{"kiro": k}}
	if text != "" {
		u["content"] = map[string]any{"type": "text", "text": text}
	}
	return kind, mustJSON(t, u)
}

// kasReplayedUserText is the user text KAS replays for a session/prompt it was sent: the
// displayText when the prompt carried one, else its first text block (acp-server.js:
// displayText ?? content).
func kasReplayedUserText(t *testing.T, params map[string]any) string {
	t.Helper()
	meta, _ := params["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	if label, _ := kiro["displayText"].(string); label != "" {
		return label
	}
	blocks, _ := params[marotte.KeyPrompt].([]map[string]any)
	if len(blocks) == 0 {
		t.Fatalf("session/prompt params %v carry no prompt blocks", params)
	}
	text, _ := blocks[0]["text"].(string)
	return text
}

func resendTurn(t *testing.T, cs *testChatStore) string {
	t.Helper()
	for _, e := range logOf(t, cs, "c1") {
		if e.Kind != marotte.EntryKindTurnOpen {
			continue
		}
		var open marotte.EntryTurnOpen
		decodePayload(t, e, &open)
		if open.Prompt != nil && len(open.Prompt.Resends) > 0 {
			return e.Turn
		}
	}
	t.Fatal("no turn_open carries the unread steers")
	return ""
}

func turnClosed(t *testing.T, cs *testChatStore, turn string) bool {
	t.Helper()
	for _, e := range logOf(t, cs, "c1") {
		if e.Turn == turn && e.Kind == marotte.EntryKindTurnClose {
			return true
		}
	}
	return false
}

func replayOfResend(t *testing.T, workDir, userText string) []translate.ProjectedTurn {
	t.Helper()
	proj := translate.NewEntryProjection(newMessageID, workDir)
	proj.Ingest(kasReplayFrame(t, "user_message_chunk", map[string]any{
		"messageId": "kas-resend", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}, userText))
	proj.Ingest(kasReplayFrame(t, marotte.ACPUpdateSessionInfo, map[string]any{"kind": "turn_start", "turnStart": true}, ""))
	proj.Ingest(kasReplayFrame(t, marotte.ACPUpdateAgentChunk, map[string]any{
		"messageId": "kas-reply", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}, "replayed reply"))
	proj.Ingest(kasReplayFrame(t, marotte.ACPUpdateSessionInfo, map[string]any{
		"kind": "turn_end", "turnEnd": map[string]any{"stopReason": "end_turn"},
	}, ""))
	return proj.Turns()
}

func mergedResendPrompts(t *testing.T, merged []mergedTurn) (shown []marotte.EntryPrompt, replied bool) {
	t.Helper()
	for _, mt := range merged {
		open, ok := turnOpenOf(mt.Entries)
		if !ok || open.Prompt == nil || open.Prompt.Text == "prompt" {
			continue
		}
		shown = append(shown, *open.Prompt)
		replied = replied || slices.Contains(textsOf(t, mt.Entries), "replayed reply")
	}
	return shown, replied
}

func TestUnreadSteerResend_ShowsTheUsersWordsLiveAndAfterReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		lead string
		stop string
		want string
	}{
		{name: "the send-now arrow", lead: "steer-b", stop: "cancelled", want: "second\n\nfirst"},
		{name: "a turn end with unread steers", stop: "end_turn", want: "first\n\nsecond"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cs, br := steerOpsHub(t)
			at := s.h.originOf("c1")
			id, _ := s.h.stagePromptTurn(t, "c1")
			s.h.translateACPEvent("c1", at, newTurnStartMsg())
			s.steer("steer-a", "first")
			s.steer("steer-b", "second")
			if tc.lead != "" {
				payload := json.RawMessage(`{"lead":"` + tc.lead + `"}`)
				if rec := postCmd(t, s.h, marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: payload}); rec.Code != http.StatusOK {
					t.Fatalf("cancel = %d %s, want 200", rec.Code, rec.Body.String())
				}
			}
			// KAS assigns the prompt's record id while the prompt is in flight, live only.
			br.onCall = func(method string, _ map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
				if method == marotte.MethodPrompt {
					s.h.translateACPEvent("c1", at, newSessionInfoMsg(map[string]any{
						"kind": "user_message_id_assigned", "userMessageId": "kas-resend",
					}))
				}
				return nil, nil, false
			}
			s.h.translateACPEvent("c1", at, clearedMsg("steer-a", "steer-b"))
			s.h.translateACPEvent("c1", at, newTurnEndMsg(tc.stop))
			s.h.coord.ReleaseTurn("c1", id)

			live := resendPrompt(t, cs)
			if live == nil {
				t.Fatal("no turn opened carrying the unread steers")
			}
			if live.Text != tc.want || live.Label != "" {
				t.Errorf("live prompt text %q label %q, want %q with no label", live.Text, live.Label, tc.want)
			}
			waitForCall(t, br, marotte.MethodPrompt)
			sent := br.paramsFor(marotte.MethodPrompt)
			meta, _ := sent["_meta"].(map[string]any)
			kiro, _ := meta["kiro"].(map[string]any)
			if got, ok := kiro["displayText"]; ok {
				t.Errorf("session/prompt _meta.kiro.displayText = %v, want none: KAS replays it as the user's text", got)
			}

			turn := resendTurn(t, cs)
			waitFor(t, func() bool { return turnClosed(t, cs, turn) })
			sid, bound := "", false
			for _, b := range bindsOf(t, logOf(t, cs, "c1")) {
				if b.KASMessageID == "kas-resend" {
					sid, bound = b.SessionID, true
				}
			}
			if !bound {
				t.Fatalf("the resend turn holds no turn_bind for kas-resend, so the replay cannot pair it")
			}

			replayed := replayOfResend(t, s.h.replay.workDir, kasReplayedUserText(t, sent))
			if len(replayed) != 1 {
				t.Fatalf("projected %d turns from the replay, want 1", len(replayed))
			}
			if open, _ := turnOpenOf(replayed[0].Entries); open.Prompt == nil || open.Prompt.Text != tc.want {
				t.Errorf("replayed prompt = %+v, want KAS to replay %q", open.Prompt, tc.want)
			}

			// The merge a session/load runs once the log shows loss (a restart mid-turn), over the
			// recorded log. A replayed text the record never held shows the pairing rides the bound id.
			for _, rc := range []struct {
				name  string
				turns []translate.ProjectedTurn
			}{
				{name: "KAS's replay", turns: replayed},
				{name: "a replay with other text", turns: replayOfResend(t, s.h.replay.workDir, "words the record never held")},
			} {
				merged, _ := mergeEntries(recordTurnsOf(logOf(t, cs, "c1"), nil), rc.turns, sid)
				shown, replied := mergedResendPrompts(t, merged)
				if len(shown) != 1 || shown[0].Text != tc.want || shown[0].Label != "" {
					t.Errorf("%s: merged resend prompts = %+v, want one showing %q with no label", rc.name, shown, tc.want)
				}
				if !replied {
					t.Errorf("%s: the merged resend turn lacks the replayed reply, so the replay did not pair with it:\n%s", rc.name, dumpMerged(merged))
				}
			}
		})
	}
}
