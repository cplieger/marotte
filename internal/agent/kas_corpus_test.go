package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

// The corpus is internal/translate/testdata/kas, KAS's live answer to marotte's own bridge
// (internal/bridge TestCaptureKASCorpus). Each file is the session lane in order: inbound frames
// verbatim, outbound the capture's own calls and answers, the live half before session/load.
const kasCorpusDir = "../translate/testdata/kas"

// captureWorkDir is the capture's scratch workspace as the harness scrubbed it.
const captureWorkDir = "/work"

type kasLine struct {
	Dir   string          `json:"dir"`
	Frame json.RawMessage `json:"frame"`
}

type kasFrame struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

func (l kasLine) frame(t *testing.T) kasFrame {
	t.Helper()
	var f kasFrame
	if err := json.Unmarshal(l.Frame, &f); err != nil {
		t.Fatalf("decode frame %s: %v", l.Frame, err)
	}
	return f
}

// kasCorpusFile is one capture split at its outbound session/load: Replay is empty when the
// scenario reopens nothing.
type kasCorpusFile struct {
	Live, Replay []kasLine
}

// loadKASCorpusLines is every line of a capture after its provenance header, in order.
func loadKASCorpusLines(t *testing.T, name string) []kasLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(kasCorpusDir, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 4<<20)
	sc.Scan()
	var out []kasLine
	for sc.Scan() {
		var l kasLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, l)
	}
	return out
}

func loadKASCorpusFile(t *testing.T, name string) kasCorpusFile {
	t.Helper()
	path := filepath.Join(kasCorpusDir, name+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 4<<20)
	sc.Scan() // the provenance header, which internal/translate's TestKASCorpus_Provenance checks
	var c kasCorpusFile
	replay := false
	for sc.Scan() {
		var l kasLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if l.Dir == "out" && l.frame(t).Method == marotte.MethodSessionLoad {
			replay = true
			continue
		}
		if replay {
			c.Replay = append(c.Replay, l)
		} else {
			c.Live = append(c.Live, l)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

// projectReplay projects the capture's replayed session/update frames as the load path does.
func projectReplay(t *testing.T, lines []kasLine, workDir string) []translate.ProjectedTurn {
	t.Helper()
	n := 0
	p := translate.NewEntryProjection(func() string {
		n++
		return fmt.Sprintf("P%d", n)
	}, workDir)
	for _, l := range lines {
		f := l.frame(t)
		if l.Dir != "in" || f.Method != marotte.MethodSessionUpdate {
			continue
		}
		var params struct {
			Update json.RawMessage `json:"update"`
		}
		var kind struct {
			SessionUpdate marotte.ACPUpdateKind `json:"sessionUpdate"`
		}
		if json.Unmarshal(f.Params, &params) != nil || json.Unmarshal(params.Update, &kind) != nil {
			t.Fatalf("decode replayed update %s", l.Frame)
		}
		p.Ingest(kind.SessionUpdate, rewriteWorkDir(params.Update, workDir))
	}
	return p.Turns()
}

func rewriteWorkDir(raw []byte, workDir string) []byte {
	raw = bytes.ReplaceAll(raw, []byte(`"`+captureWorkDir+`"`), []byte(`"`+workDir+`"`))
	return bytes.ReplaceAll(raw, []byte(captureWorkDir+"/"), []byte(workDir+"/"))
}

// kasDrive replays a capture's live half into a runtime the way a chat produced it: each outbound
// prompt, steer, compact and ask answer goes through the hub's own command, and the fake engine
// answers the resulting call with the captured frames and reply.
type kasDrive struct {
	t       *testing.T
	h       *Runtime
	cs      *testChatStore
	br      *fakeBridge
	workDir string
	sid     string
	// answers are the capture's replies to marotte's calls and its answers to KAS's requests, by id.
	replies map[int64]json.RawMessage
	answers map[int64]kasFrame
	lines   []kasLine
	// subst rewrites an inbound frame: the capture's prompt tags become the ones this runtime sent.
	subst [][2]string
	// early are the frames before the first prompt, held until the prompt opens the chat's session.
	early   []kasLine
	mu      sync.Mutex
	pos     int
	prompts int
}

const kasChat marotte.ChatID = "c1"

func newKASDrive(t *testing.T, lines []kasLine) *kasDrive {
	t.Helper()
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "notes.txt"),
		[]byte("The release train leaves on Thursday.\nThe build cache lives in /var/cache/build.\nOwners rotate weekly.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, cs, br := newTestHubIn(workDir)
	t.Cleanup(func() { shutdownHub(t, h) })
	seedChat(t, cs, kasChat)
	d := &kasDrive{
		t: t, h: h, cs: cs, br: br, workDir: workDir, lines: lines,
		replies: map[int64]json.RawMessage{}, answers: map[int64]kasFrame{},
	}
	for _, l := range lines {
		f := l.frame(t)
		if f.ID == nil || f.Method != "" {
			continue
		}
		if l.Dir == "in" {
			d.replies[*f.ID] = f.Result
		} else {
			d.answers[*f.ID] = f
		}
	}
	for _, l := range lines {
		if sid := sessionIDOf(l.frame(t).Params); sid != "" {
			d.sid = sid
			break
		}
	}
	br.mu.Lock()
	br.sessionID = d.sid
	br.onCall = d.onCall
	br.mu.Unlock()
	return d
}

func sessionIDOf(params json.RawMessage) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.SessionID
}

func (d *kasDrive) run() {
	t := d.t
	for {
		d.mu.Lock()
		if d.pos >= len(d.lines) {
			d.mu.Unlock()
			break
		}
		l := d.lines[d.pos]
		d.mu.Unlock()
		f := l.frame(t)
		if l.Dir == "in" && f.Method == "" {
			d.mu.Lock()
			d.pos++
			d.mu.Unlock()
			continue
		}
		if l.Dir == "in" {
			d.mu.Lock()
			if d.prompts == 0 {
				d.early = append(d.early, l)
			} else {
				d.deliverLocked(l)
			}
			d.pos++
			d.mu.Unlock()
			continue
		}
		switch f.Method {
		case marotte.MethodPrompt:
			d.prompt(f)
		case marotte.MethodSessionCompact:
			if rec := postCmd(t, d.h, marotte.ClientCommand{Type: marotte.CmdCompact, ChatID: kasChat}); rec.Code != http.StatusOK {
				t.Fatalf("compact = %d %s", rec.Code, rec.Body.String())
			}
		case marotte.MethodSessionClose:
			d.settle()
			return
		default:
			d.mu.Lock()
			d.pos++
			d.mu.Unlock()
		}
	}
	d.settle()
}

// settle ends the session, as the capture's close did, and waits for the forward to fold every frame.
func (d *kasDrive) settle() {
	d.br.end()
	joinInflight(d.t, d.h)
}

func (d *kasDrive) prompt(f kasFrame) {
	t := d.t
	var p struct {
		Prompt []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if json.Unmarshal(f.Params, &p) != nil || len(p.Prompt) == 0 {
		t.Fatalf("captured prompt carries no text: %s", f.Params)
	}
	d.prompts++
	payload, _ := json.Marshal(map[string]any{"text": p.Prompt[0].Text, "message_id": fmt.Sprintf("m-%d", d.prompts)})
	// The previous turn's close is logged before its holder lets go, so the next prompt may meet it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := postCmd(t, d.h, marotte.ClientCommand{Type: marotte.CmdPrompt, ChatID: kasChat, Payload: payload})
		if rec.Code == http.StatusOK {
			break
		}
		if rec.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("prompt = %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitForWithin(t, 30*time.Second, func() bool { return len(closesOf(t, logOf(t, d.cs, kasChat))) >= d.prompts })
	// The close is logged before the run list ends its entry; the capture's next prompt was sent on an idle chat.
	waitForWithin(t, 30*time.Second, func() bool { return !listLive(d.h, kasChat) })
}

// onCall answers marotte's call with the capture: a prompt or compact delivers every captured
// frame up to its reply first, as KAS streams a turn before answering.
func (d *kasDrive) onCall(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool) {
	t := d.t
	switch method {
	case marotte.MethodPrompt, marotte.MethodSessionCompact, marotte.MethodSessionSteer:
	default:
		return nil, nil, false
	}
	d.mu.Lock()
	if d.pos >= len(d.lines) {
		d.mu.Unlock()
		return nil, nil, false
	}
	out := d.lines[d.pos].frame(t)
	if d.lines[d.pos].Dir != "out" || out.Method != method {
		d.mu.Unlock()
		if method == marotte.MethodSessionSteer {
			return d.steerReply(params), nil, true
		}
		return nil, nil, false
	}
	for _, l := range d.early {
		d.deliverLocked(l)
	}
	d.early = nil
	if method == marotte.MethodPrompt {
		if captured := promptTagOfRaw(out.Params); captured != "" {
			d.subst = append(d.subst, [2]string{captured, promptTagOf(params)})
		}
	}
	d.pos++
	d.mu.Unlock()
	return d.streamUntilReply(*out.ID), nil, true
}

func (d *kasDrive) steerReply(params map[string]any) json.RawMessage {
	wire, _ := params["messageId"].(string)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, l := range d.lines {
		f := l.frame(d.t)
		if l.Dir == "out" && f.Method == marotte.MethodSessionSteer && strings.Contains(string(f.Params), `"messageId":"`+wire+`"`) {
			return d.replies[*f.ID]
		}
	}
	d.t.Errorf("marotte steered with message id %q, which the capture never sent", wire)
	return json.RawMessage(`{"queued":false}`)
}

func promptTagOfRaw(params json.RawMessage) string {
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	return promptTagOf(p)
}

func (d *kasDrive) streamUntilReply(id int64) json.RawMessage {
	t := d.t
	for {
		d.mu.Lock()
		if d.pos >= len(d.lines) {
			d.mu.Unlock()
			t.Fatalf("the capture ends before the reply to request %d", id)
		}
		l := d.lines[d.pos]
		d.pos++
		f := l.frame(t)
		switch {
		case l.Dir == "in" && f.Method == "" && f.ID != nil && *f.ID == id:
			d.mu.Unlock()
			return f.Result
		case l.Dir == "in" && f.Method == "":
			d.mu.Unlock()
		case l.Dir == "in":
			d.deliverLocked(l)
			d.mu.Unlock()
			if f.ID != nil {
				d.answerAsk(f)
			}
		case f.Method == marotte.MethodSessionSteer:
			d.mu.Unlock()
			d.steer(f)
		default:
			d.mu.Unlock()
		}
	}
}

func (d *kasDrive) deliverLocked(l kasLine) {
	raw := rewriteWorkDir(l.Frame, d.workDir)
	for _, s := range d.subst {
		raw = bytes.ReplaceAll(raw, []byte(s[0]), []byte(s[1]))
	}
	var msg marotte.RPCResponse
	if err := json.Unmarshal(raw, &msg); err != nil {
		d.t.Fatalf("decode %s: %v", raw, err)
	}
	d.br.deliver(&msg)
}

// steer sends the captured steer through the chat's own steer command, under the captured id.
func (d *kasDrive) steer(f kasFrame) {
	t := d.t
	var p struct {
		Message   string `json:"message"`
		MessageID string `json:"messageId"`
	}
	_ = json.Unmarshal(f.Params, &p)
	before := countCalls(d.br, marotte.MethodSessionSteer)
	payload, _ := json.Marshal(map[string]any{"text": p.Message, "message_id": p.MessageID})
	if rec := postCmd(t, d.h, marotte.ClientCommand{Type: marotte.CmdSteer, ChatID: kasChat, Payload: payload}); rec.Code != http.StatusOK {
		t.Fatalf("steer = %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return countCalls(d.br, marotte.MethodSessionSteer) > before })
}

// answerAsk answers a captured permission or elicitation request the way the capture did, through
// the chat's answer command; every other request the runtime answers itself.
func (d *kasDrive) answerAsk(req kasFrame) {
	t := d.t
	ans, ok := d.answers[*req.ID]
	if !ok {
		return
	}
	var cmd marotte.ClientCommand
	switch req.Method {
	case marotte.MethodRequestPermission:
		var r struct {
			Outcome struct {
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(ans.Result, &r)
		payload, _ := json.Marshal(map[string]any{"request_id": *req.ID, "option_id": r.Outcome.OptionID})
		cmd = marotte.ClientCommand{Type: marotte.CmdPermissionResponse, ChatID: kasChat, Payload: payload}
	case marotte.MethodElicitationCreate:
		var r struct {
			Content map[string]json.RawMessage `json:"content"`
			Action  string                     `json:"action"`
		}
		_ = json.Unmarshal(ans.Result, &r)
		payload, _ := json.Marshal(marotte.ElicitationResponseCommand{RequestID: *req.ID, Action: r.Action, Content: r.Content})
		cmd = marotte.ClientCommand{Type: marotte.CmdElicitationResponse, ChatID: kasChat, Payload: payload}
	default:
		return
	}
	// The runtime registers the ask on its own goroutine, so the answer is retried until it lands. A
	// permission the chat's policy settled itself is already answered.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := postCmd(t, d.h, cmd)
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "already_answered") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("answer %s %d = %d %s", req.Method, *req.ID, rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWithin(t *testing.T, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", budget)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestKASCorpus_LiveAndReplayAgree pins that marotte's record of a live session and its own
// projection of KAS's replay of that session agree: a replay of a completely recorded session
// adds nothing, and an entry the record lost comes back looking as it did live.
func TestKASCorpus_LiveAndReplayAgree(t *testing.T) {
	for _, name := range []string{"steer", "compact", "delegate"} {
		t.Run(name, func(t *testing.T) {
			c := loadKASCorpusFile(t, name)
			d := newKASDrive(t, c.Live)
			d.run()
			log := logOf(t, d.cs, kasChat)
			projected := projectReplay(t, c.Replay, d.workDir)

			merged, changed := mergeEntries(recordTurnsOf(log, nil), projected, d.sid)
			if changed {
				t.Errorf("the replay of a completely recorded session changed the record:\nrecord:\n%s\nmerged:\n%s",
					dumpEntries(log), dumpMerged(merged))
			}
			for i, lost := range log {
				if !restorable(lost.Kind) {
					continue
				}
				rest := slices.Delete(slices.Clone(log), i, i+1)
				merged, _ := mergeEntries(recordTurnsOf(rest, nil), projected, d.sid)
				back, ok := restoredEntry(merged, lost)
				if !ok {
					t.Errorf("lost %s %q: the replay did not restore it:\n%s", lost.Kind, lost.ID, dumpMerged(merged))
					continue
				}
				if want, got := rendered(t, lost), rendered(t, back); want != got {
					t.Errorf("lost %s %q came back as\n  %s\nwant the live\n  %s", lost.Kind, lost.ID, got, want)
				}
			}
		})
	}
}

// restorable is an entry kind the replay carries; turn brackets are the merge's own spine.
func restorable(k marotte.EntryKind) bool {
	switch k {
	case marotte.EntryKindText, marotte.EntryKindToolCall, marotte.EntryKindToolResult,
		marotte.EntryKindCompaction, marotte.EntryKindSteer:
		return true
	}
	return false
}

func restoredEntry(merged []mergedTurn, lost marotte.Entry) (marotte.Entry, bool) {
	for _, turn := range merged {
		for _, e := range turn.Entries {
			if e.Kind == lost.Kind && e.ID == lost.ID {
				return e, true
			}
		}
	}
	return marotte.Entry{}, false
}

// rendered is what the chat shows of an entry. Left out: a result's title, since the card keeps its
// call's title unless the result settles one (static-src settledToolCall); a steer's state, since the
// replay cannot say whether the model read it.
func rendered(t *testing.T, e marotte.Entry) string {
	t.Helper()
	var v any
	switch e.Kind {
	case marotte.EntryKindText:
		var p marotte.EntryText
		decodePayload(t, e, &p)
		v = p.Text
	case marotte.EntryKindToolCall:
		var p marotte.EntryToolCall
		decodePayload(t, e, &p)
		v = p.Title
	case marotte.EntryKindToolResult:
		var p marotte.EntryToolResult
		decodePayload(t, e, &p)
		v = []any{p.Status, p.Output}
	case marotte.EntryKindCompaction:
		var p marotte.EntryCompaction
		decodePayload(t, e, &p)
		v = p.Summary
	case marotte.EntryKindSteer:
		var p marotte.EntrySteer
		decodePayload(t, e, &p)
		v = []any{p.Text, p.Origin}
	}
	raw, _ := json.Marshal(map[string]any{"lane": e.Lane, "fields": v})
	return string(raw)
}

func dumpEntries(entries []marotte.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s %s[%s] %s %s\n", e.Turn, e.Kind, e.Lane, e.ID, e.Payload)
	}
	return b.String()
}

// liveKiro is the _meta.kiro of every captured live session_info_update of kind, in order.
func liveKiro(t *testing.T, lines []kasLine, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range lines {
		f := l.frame(t)
		if l.Dir != "in" || f.Method != marotte.MethodSessionUpdate {
			continue
		}
		var p struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Meta          struct {
					Kiro map[string]any `json:"kiro"`
				} `json:"_meta"`
			} `json:"update"`
		}
		if json.Unmarshal(f.Params, &p) != nil {
			t.Fatalf("decode %s", l.Frame)
		}
		if p.Update.SessionUpdate == string(marotte.ACPUpdateSessionInfo) && p.Update.Meta.Kiro["kind"] == kind {
			out = append(out, p.Update.Meta.Kiro)
		}
	}
	return out
}

func entriesOfKind(t *testing.T, log []marotte.Entry, kind marotte.EntryKind) []marotte.Entry {
	t.Helper()
	var out []marotte.Entry
	for _, e := range log {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestKASCorpus_DecodesWithoutRefusalOrUnknownKind pins that marotte consumes every frame KAS sent
// it live, refuses none of KAS's requests, and reads each scenario's facts at the level KAS put them.
func TestKASCorpus_DecodesWithoutRefusalOrUnknownKind(t *testing.T) {
	for _, name := range []string{"steer", "compact", "delegate", "elicit"} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			c := loadKASCorpusFile(t, name)
			d := newKASDrive(t, c.Live)
			d.run()
			for _, bad := range []string{"UNKNOWN kind dropped", "refusing an unexpected peer request"} {
				if strings.Contains(logs.String(), bad) {
					t.Errorf("decoding the live half logged %q:\n%s", bad, logs.String())
				}
			}
			log := logOf(t, d.cs, kasChat)

			completions := liveKiro(t, c.Live, "turn_completion")
			closes := closesOf(t, log)
			if len(completions) == 0 || len(closes) != len(completions) {
				t.Fatalf("%d turn_close entries for %d captured turn_completion frames", len(closes), len(completions))
			}
			for i, k := range completions {
				want, _ := k["elapsedTime"].(float64)
				if want <= 0 || closes[i].ElapsedMs != want {
					t.Errorf("turn %d elapsed_ms = %v, want the captured elapsedTime %v", i, closes[i].ElapsedMs, want)
				}
			}
			switch name {
			case "steer":
				checkSteerFacts(t, c.Live, log)
			case "compact":
				checkCompactionFacts(t, c.Live, log, d)
			case "elicit":
				checkElicitationFacts(t, c.Live, d)
			}
		})
	}
}

func checkSteerFacts(t *testing.T, live []kasLine, log []marotte.Entry) {
	t.Helper()
	var sent struct {
		Message   string `json:"message"`
		MessageID string `json:"messageId"`
	}
	for _, l := range live {
		if f := l.frame(t); l.Dir == "out" && f.Method == marotte.MethodSessionSteer {
			_ = json.Unmarshal(f.Params, &sent)
		}
	}
	steers := entriesOfKind(t, log, marotte.EntryKindSteer)
	if len(steers) != 1 {
		t.Fatalf("%d steer entries, want the one sent:\n%s", len(steers), dumpEntries(log))
	}
	var p marotte.EntrySteer
	decodePayload(t, steers[0], &p)
	if steers[0].ID != marotte.SteerIDFor(sent.MessageID) || p.Text != sent.Message || p.Origin != marotte.SteerOriginUser || p.State != marotte.SteerStateRead {
		t.Errorf("steer %q = %+v, want id %q, text %q, origin user, state read",
			steers[0].ID, p, marotte.SteerIDFor(sent.MessageID), sent.Message)
	}
}

func checkCompactionFacts(t *testing.T, live []kasLine, log []marotte.Entry, d *kasDrive) {
	t.Helper()
	done := liveKiro(t, live, "summarization_completed")
	if len(done) != 1 {
		t.Fatalf("%d summarization_completed frames, want 1", len(done))
	}
	s, _ := done[0]["summarization"].(map[string]any)
	sum, _ := s["summary"].(map[string]any)
	want, _ := sum["conversationSummary"].(string)
	if want == "" {
		t.Fatal("the captured frame carries no summarization.summary.conversationSummary")
	}
	compactions := entriesOfKind(t, log, marotte.EntryKindCompaction)
	if len(compactions) != 1 {
		t.Fatalf("%d compaction entries, want 1:\n%s", len(compactions), dumpEntries(log))
	}
	var p marotte.EntryCompaction
	decodePayload(t, compactions[0], &p)
	if p.Summary != want {
		t.Errorf("compaction summary = %q, want the frame's conversationSummary %q", p.Summary, want)
	}
	if chat := chatRecord(t, d.cs, kasChat); chat.CompactionWatermark == "" {
		t.Errorf("the chat holds no compaction watermark after a compaction")
	}
}

func checkElicitationFacts(t *testing.T, live []kasLine, d *kasDrive) {
	t.Helper()
	var req struct {
		Elicitation struct {
			Mode            string                            `json:"mode"`
			Message         string                            `json:"message"`
			RequestedSchema *marotte.ElicitationRequestSchema `json:"requestedSchema"`
		} `json:"elicitation"`
	}
	found := false
	for _, l := range live {
		if f := l.frame(t); l.Dir == "in" && f.Method == marotte.MethodElicitationCreate {
			found = json.Unmarshal(f.Params, &req) == nil
		}
	}
	if !found || req.Elicitation.RequestedSchema == nil {
		t.Fatal("the capture holds no decodable _kiro/mcp/elicitation request with a requestedSchema")
	}
	asks := payloadsOfType[marotte.ElicitationNeededPayload](t, bufferedSince(d.h, 0), marotte.EventElicitationNeeded)
	if len(asks) != 1 {
		t.Fatalf("%d elicitation_needed events, want 1", len(asks))
	}
	got, want := asks[0], req.Elicitation
	gotSchema, _ := json.Marshal(got.RequestedSchema)
	wantSchema, _ := json.Marshal(want.RequestedSchema)
	if got.Mode != cmp.Or(want.Mode, "form") || got.Message != want.Message || string(gotSchema) != string(wantSchema) {
		t.Errorf("elicitation_needed = mode %q message %q schema %s, want mode %q message %q schema %s",
			got.Mode, got.Message, gotSchema, want.Mode, want.Message, wantSchema)
	}
}
