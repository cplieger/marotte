package agent

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// capturedBotDir is the Bot's directory as the capture harness scrubbed it.
var capturedBotDir = regexp.MustCompile(`~/\.kiro/bots/c-[0-9a-f]+`)

type capturedFSRequest struct {
	Method string
	Params map[string]any
}

// capturedFSRequests are the fs requests of name's capture for the Bot's memory file, in order, from the session
// whose door report is the index'th.
func capturedFSRequests(t *testing.T, name string, report int) []capturedFSRequest {
	t.Helper()
	var session string
	var out []capturedFSRequest
	seen := 0
	for _, l := range loadKASCorpusLines(t, name) {
		if l.Dir == "report" {
			if seen == report {
				var r struct {
					SessionID string `json:"sessionId"`
				}
				if err := json.Unmarshal(l.Frame, &r); err != nil {
					t.Fatal(err)
				}
				session = r.SessionID
			}
			seen++
			continue
		}
		f := l.frame(t)
		if l.Dir != "in" || f.ID == nil || (f.Method != marotte.MethodFSRead && f.Method != marotte.MethodFSWrite) {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p["sessionId"] == session && session != "" {
			out = append(out, capturedFSRequest{Method: f.Method, Params: p})
		}
	}
	if len(out) == 0 {
		t.Fatalf("the %s capture holds no fs request from door report %d's session", name, report)
	}
	return out
}

// replay sends req on chatID's routing as session, its path moved from the capture's Bot directory to f's.
func (f *botGrantFixture) replay(t *testing.T, chatID marotte.ChatID, session string, req capturedFSRequest) error {
	t.Helper()
	path, _ := req.Params["path"].(string)
	extra := map[string]any{}
	for k, v := range req.Params {
		if k != "path" && k != "sessionId" {
			extra[k] = v
		}
	}
	_, err := f.do(t, chatID, req.Method, session, capturedBotDir.ReplaceAllLiteralString(path, f.dir), extra)
	return err
}

// TestBotMemoryGrant_TheCapturedMemoryWriteIsAdmittedForTheBotAlone pins P4 (design §15) on the bot_memory_write
// capture: the reads and the fs/write_text_file KAS 3.2.0 sent for a Bot's memory write are admitted for the Bot's
// own session and land the written content, and the same requests from a regular chat's session are each refused.
func TestBotMemoryGrant_TheCapturedMemoryWriteIsAdmittedForTheBotAlone(t *testing.T) {
	reqs := capturedFSRequests(t, "bot_memory_write", 0)
	var written string
	for _, r := range reqs {
		if r.Method == marotte.MethodFSWrite {
			written, _ = r.Params["content"].(string)
		}
	}
	if !strings.Contains(written, "Marker: slate-4410") {
		t.Fatalf("the capture's memory write carries %q, want the marker", written)
	}

	t.Run("from the Bot's session", func(t *testing.T) {
		f := newBotGrantFixture(t)
		for i, r := range reqs {
			if err := f.replay(t, "c1", "sess_bot", r); err != nil {
				t.Errorf("request %d (%s) = %v, want admitted", i, r.Method, err)
			}
		}
		if got, err := os.ReadFile(f.file); err != nil || string(got) != written {
			t.Errorf("the memory file after the replay = %q, %v; want the captured write", got, err)
		}
	})
	t.Run("from a regular chat", func(t *testing.T) {
		f := newBotGrantFixture(t)
		for i, r := range reqs {
			if err := f.replay(t, "c3", "sess_chat", r); err == nil {
				t.Errorf("request %d (%s) from a regular chat was admitted", i, r.Method)
			}
		}
		if got, err := os.ReadFile(f.file); err != nil || string(got) != botNotes {
			t.Errorf("the memory file after the refused replay = %q, %v; want its notes", got, err)
		}
	})
}

// TestBotMemoryGrant_TheCapturedTangentReadIsRefused pins P5's last clause on the bot_tangent capture: the read KAS
// 3.2.0 sent from a tangent forked off a Bot is refused for the tangent's session, though it names the Bot's file.
func TestBotMemoryGrant_TheCapturedTangentReadIsRefused(t *testing.T) {
	reqs := capturedFSRequests(t, "bot_tangent", 1)
	f := newBotGrantFixture(t)
	for i, r := range reqs {
		if r.Method != marotte.MethodFSRead {
			t.Fatalf("request %d is %s; the tangent's capture holds only reads", i, r.Method)
		}
		if err := f.replay(t, "c4", "sess_tangent", r); err == nil {
			t.Errorf("request %d from the tangent was admitted", i)
		}
	}
}

// TestBotTangent_TheFirstOpenOnTheCapturedLoadRaisesNoError pins P5 on the door: a tangent of a Bot, its record on
// the default mode, opened on the session KAS 3.2.0 loaded for it (reporting vibe, no role among its modes), keeps the
// default mode, sends no session/set_mode and no context call, and broadcasts no error.
func TestBotTangent_TheFirstOpenOnTheCapturedLoadRaisesNoError(t *testing.T) {
	var load capturedReport
	for _, l := range loadKASCorpusLines(t, "bot_tangent") {
		if l.Dir == "report" {
			if err := json.Unmarshal(l.Frame, &load); err != nil {
				t.Fatal(err)
			}
		}
	}
	if load.Method != marotte.MethodSessionLoad || load.CurrentModeID != marotte.DefaultModeID {
		t.Fatalf("the bot_tangent capture's last door report is %+v, want the tangent's load on %s", load, marotte.DefaultModeID)
	}
	br := reportingCaptured(load)
	h, cs := botRuntime(t, br)
	seedModeChat(t, cs, "c1", false, true)
	if _, err := cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Tangent = true
		return true
	}); err != nil {
		t.Fatal(err)
	}
	since := h.bus.fanout.Position().Head

	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("open the tangent: %v", err)
	}

	if got := modeOf(t, cs, "c1"); got != marotte.DefaultModeID {
		t.Errorf("CurrentModeID = %q, want %q", got, marotte.DefaultModeID)
	}
	if got := setModeParams(br); len(got) != 0 {
		t.Errorf("session/set_mode modeIds = %v, want none", got)
	}
	if n, p := contextCalls(br); n != 0 {
		t.Errorf("%d %s calls (last %v), want none", n, marotte.MethodSessionContext, p)
	}
	if got := errorPayloadsSince(t, h, since); len(got) != 0 {
		t.Errorf("the tangent's first open broadcast errors %+v", got)
	}
}
