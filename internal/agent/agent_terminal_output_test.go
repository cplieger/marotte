package agent

import (
	"cmp"
	"encoding/json"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

// termCreateMsgArgs builds a terminal/create with explicit control over whether `args`
// is present, which termCreateMsg cannot express.
func termCreateMsgArgs(t *testing.T, id int64, command string, args *[]string) *marotte.RPCResponse {
	t.Helper()
	params := map[string]any{"command": command}
	if args != nil {
		params["args"] = *args
	}
	return &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, params)}
}

func waitForTermExit(t *testing.T, h *Runtime, msg *marotte.RPCResponse) string {
	t.Helper()
	h.translateACPEvent("c1", h.originOf("c1"), msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	return term.rawOutput()
}

// KAS leaves args unset and puts the whole line in command, so an absent args runs through a shell.
func TestTermCreate_AbsentArgsRunsTheCommandLineThroughAShell(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
		reason  string
	}{{
		name: "argument", command: `echo hello world`, want: "hello world\n",
		reason: "the single most common shape: a command with arguments",
	}, {
		name: "quoting", command: `echo "spaced arg"`, want: "spaced arg\n",
		reason: "quotes are the shell's, and exec.Command never removes them",
	}, {
		name: "pipeline", command: `printf 'a\nb\n' | grep b`, want: "b\n",
		reason: "a pipeline has no meaning without a shell",
	}, {
		name: "redirection", command: `echo out 1>&2`, want: "out\n",
		reason: "stderr redirection, which also proves both streams share one pipe",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
			got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, tc.command, nil))
			if got != tc.want {
				t.Errorf("output = %q, want %q (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// The gate is presence, not length: `"args":[]` execs prog with no arguments.
func TestTermCreate_PresentArgsExecsDirectly(t *testing.T) {
	t.Run("EmptyArgsIsStillDirectExec", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		// A shell would print an empty line; a direct exec finds no such program and prints nothing.
		empty := []string{}
		h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1, "echo hi", &empty))
		h.agentTerms.mu.Lock()
		n := len(h.agentTerms.terms)
		h.agentTerms.mu.Unlock()
		if n != 0 {
			t.Errorf("registered %d terminals, want 0: an empty args must exec"+
				" %q as a program name, which does not exist", n, "echo hi")
		}
	})
	t.Run("NonEmptyArgsAreNotReSplitByAShell", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		args := []string{"a b", "c"}
		got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, "echo", &args))
		if got != "a b c\n" {
			t.Errorf("output = %q, want %q: a pre-split argv is passed through"+
				" verbatim, not re-parsed", got, "a b c\n")
		}
	})
}

// A create failure that logs nothing leaves no server-side trace.
func TestTermCreate_EveryFailurePathLogsAndAnswers(t *testing.T) {
	cases := []struct {
		name   string
		msg    func(t *testing.T) *marotte.RPCResponse
		reason string
	}{{
		name: "InvalidParams",
		msg: func(t *testing.T) *marotte.RPCResponse {
			t.Helper()
			id := int64(1)
			return &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: []byte(`{"command":5}`)}
		},
		reason: "a malformed frame",
	}, {
		name: "EmptyCommand",
		msg: func(t *testing.T) *marotte.RPCResponse {
			t.Helper()
			return termCreateMsgArgs(t, 1, "", nil)
		},
		reason: "no command at all",
	}, {
		name: "CwdEscapesWorkspace",
		msg: func(t *testing.T) *marotte.RPCResponse {
			t.Helper()
			id := int64(1)
			return &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t,
				map[string]any{"command": "true", "cwd": "/etc"})}
		},
		reason: "a cwd outside the workspace",
	}, {
		name: "ExecFails",
		msg: func(t *testing.T) *marotte.RPCResponse {
			t.Helper()
			nope := []string{}
			return termCreateMsgArgs(t, 1, "definitely-not-a-real-binary-xyz", &nope)
		},
		reason: "a program that does not exist — BF1's own symptom",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t) // not parallel: swaps the slog default
			br := newRecordingTermBridge()
			h := hubWithBridge(t, t.TempDir(), br)

			h.translateACPEvent("c1", h.originOf("c1"), tc.msg(t))

			if !strings.Contains(logs.String(), "agent terminal create failed") {
				t.Errorf("no server-side line for %s\nlogs: %s", tc.reason, logs.String())
			}
			resp, ok := br.lastResponse()
			if !ok || resp.err == nil {
				t.Errorf("the agent got no error response for %s (resp=%+v ok=%v)", tc.reason, resp, ok)
			}
		})
	}
}

// terminal_exited must follow its output; Wait runs first and the drain is bounded from exit.
func TestTerminalExited_IsOrderedAfterEveryOutputEvent(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// A write immediately before exit is the shape that raced.
	got := waitForTermExit(t, h, termCreateMsgArgs(t, 1, `printf 'root\n'`, nil))
	if got != "root\n" {
		t.Fatalf("ring = %q, want %q", got, "root\n")
	}

	var evs []ringEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs = captureTerminalEvents(t, h)
		if hasType(evs, marotte.EventTerminalExited) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	exitedID, ok := firstEventID(evs, marotte.EventTerminalExited)
	if !ok {
		t.Fatal("terminal_exited was never broadcast")
	}
	sawOutput := false
	for _, e := range evs {
		if e.typ != string(marotte.EventTerminalOutput) {
			continue
		}
		sawOutput = true
		if e.eventID > exitedID {
			t.Errorf("terminal_output event %d came AFTER terminal_exited %d:"+
				" the client paints the exit footer above the line that produced it",
				e.eventID, exitedID)
		}
	}
	if !sawOutput {
		t.Error("no terminal_output event: the drain lost the command's only line")
	}
}

// The live stream carries plain text plus spans: sanitize.Output strips ANSI before the
// parser, so only SanitizeUnicode may run here.
func TestTerminalEmitter_ParsesStylingAndStillStripsHiddenUnicode(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	term := newAgentTerminal(nil, "c1", 4096)
	emit := h.agentTerms.emitter(t.Context(), term, "t1", "c1")

	// A real zerolog console line with a zero-width space smuggled in.
	emit("\x1b[90m1:47AM\x1b[0m \u200b\x1b[32mINF\x1b[0m ok\n")

	got := terminalOutputPayloads(t, h)
	if len(got) != 1 {
		t.Fatalf("got %d terminal_output events, want 1", len(got))
	}
	p := got[0]
	if p.Data != "1:47AM INF ok\n" {
		t.Errorf("data = %q, want %q: escapes parsed off, zero-width space stripped",
			p.Data, "1:47AM INF ok\n")
	}
	if strings.ContainsRune(p.Data, 0x1b) || strings.ContainsRune(p.Data, 0x200b) {
		t.Errorf("data still carries an escape or a hidden character: %q", p.Data)
	}
	if len(p.Spans) != 2 {
		t.Errorf("spans = %d, want 2 (the timestamp's grey and the level's green)."+
			" 0 means SanitizeOutput stripped the escapes before the parser saw them", len(p.Spans))
	}
	if p.Offset != 0 {
		t.Errorf("offset = %d, want 0 for the first chunk", p.Offset)
	}
}

func terminalOutputPayloads(t *testing.T, h *Runtime) []marotte.TerminalOutputPayload {
	t.Helper()
	type idPayload struct {
		p  marotte.TerminalOutputPayload
		id uint64
	}
	var found []idPayload
	for _, e := range h.bus.fanout.Snapshot() {
		var env struct {
			Type    string                        `json:"type"`
			Payload marotte.TerminalOutputPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		if env.Type == string(marotte.EventTerminalOutput) {
			found = append(found, idPayload{p: env.Payload, id: e.Offset})
		}
	}
	slices.SortFunc(found, func(a, b idPayload) int { return cmp.Compare(a.id, b.id) })
	out := make([]marotte.TerminalOutputPayload, len(found))
	for i, f := range found {
		out[i] = f.p
	}
	return out
}

// The wire Offset must be the parser's UTF-16 count, or non-ASCII output rebases spans onto the wrong character.
func TestTerminalEmitter_OffsetIsTheUTF16BaseOfEachChunk(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	term := newAgentTerminal(nil, "c1", 4096)
	emit := h.agentTerms.emitter(t.Context(), term, "t1", "c1")

	emit("\U0001F600ok")       // 4 UTF-16 units (2 for the pair, 2 for "ok"), 6 bytes
	emit("\x1b[31mred\x1b[0m") // styled, must start at unit 4

	got := terminalOutputPayloads(t, h)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Offset != 0 {
		t.Errorf("first chunk offset = %d, want 0", got[0].Offset)
	}
	if got[1].Offset != 4 {
		t.Errorf("second chunk offset = %d, want 4 UTF-16 units"+
			" (a byte count would say 6)", got[1].Offset)
	}
	if len(got[1].Spans) != 1 {
		t.Fatalf("second chunk spans = %+v, want one", got[1].Spans)
	}
	// The contract: subtracting the reported base indexes the chunk the client got.
	if s := got[1].Spans[0]; s.Start-got[1].Offset != 0 || s.End-got[1].Offset != 3 {
		t.Errorf("span %+v rebased by offset %d = [%d,%d), want [0,3) over %q",
			s, got[1].Offset, s.Start-got[1].Offset, s.End-got[1].Offset, got[1].Data)
	}
}

// KAS releases the terminal ~3ms after creating it and puts no output on the completion,
// so only the retired record, evicted at the turn boundary, makes adoption possible.
func TestTerminalOutput_SurvivesReleaseAndIsEvictedAtTheTurnBoundary(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1, `printf '\033[31mfail\033[0m\n'`, nil))
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	termID := onlyTermID(t, h)

	// KAS's release, which lands long before the completion that needs the bytes.
	if _, ok := h.agentTerms.release("c1", termID); !ok {
		t.Fatalf("release(%q) found no terminal", termID)
	}
	text, spans, ok := h.agentTerms.Output(termID)
	if !ok {
		t.Fatal("Output found nothing after release: the record was not retired")
	}
	if text != "fail\n" {
		t.Errorf("text = %q, want %q (escapes parsed off, same as the live path)", text, "fail\n")
	}
	if len(spans) != 1 || spans[0].FG != 1 {
		t.Errorf("spans = %+v, want one red span", spans)
	}

	// KAS can send several status frames per tool call, so a consuming read loses the second.
	if _, _, ok := h.agentTerms.Output(termID); !ok {
		t.Error("the second read found nothing: adoption is not idempotent," +
			" so a duplicate completed frame logs a false 'output missing'")
	}

	// The boundary is the closing turn's id; a record spawned with no turn open belongs to the next close.
	h.agentTerms.closeTurn("c1", "t-next")
	if _, _, ok := h.agentTerms.Output(termID); ok {
		t.Error("the record survived the turn boundary, so it grows with the session")
	}
}

// A silent command is known, not missing; the translate diagnostic keys on this boolean.
func TestTerminalOutput_KnownButSilentIsNotMissing(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1, "true", nil))
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	termID := onlyTermID(t, h)

	t.Run("Live", func(t *testing.T) {
		text, spans, ok := h.agentTerms.Output(termID)
		if !ok || text != "" || spans != nil {
			t.Errorf("Output = (%q, %+v, %v), want (\"\", nil, true):"+
				" a registered terminal that printed nothing is not missing", text, spans, ok)
		}
	})
	t.Run("Retired", func(t *testing.T) {
		if _, ok := h.agentTerms.release("c1", termID); !ok {
			t.Fatal("release found no terminal")
		}
		text, _, ok := h.agentTerms.Output(termID)
		if !ok || text != "" {
			t.Errorf("Output = (%q, _, %v), want (\"\", _, true):"+
				" a silent command's record must be retired like any other", text, ok)
		}
	})
	t.Run("Unknown", func(t *testing.T) {
		if _, _, ok := h.agentTerms.Output("no-such-terminal"); ok {
			t.Error("an unknown id reported as known, so a genuine miss cannot be diagnosed")
		}
	})
}

// Deleting a chat must not retire: nothing is left to adopt into, and no boundary would evict it.
func TestKillForChat_DropsRetiredOutput(t *testing.T) {
	at := bareTerminals()
	term := newAgentTerminal(&exec.Cmd{}, "c1", 64)
	term.output.write([]byte("secret\n"))
	at.terms["t1"] = term
	at.byChatID["c1"] = []string{"t1"}
	// A record from an earlier command in the same chat.
	at.retire("t0", term, term.turn)

	at.killForChat("c1")

	at.mu.Lock()
	n := len(at.retired)
	at.mu.Unlock()
	if n != 0 {
		t.Errorf("KillForChat left %d retired records for a deleted chat, want 0", n)
	}
}

func onlyTermID(t *testing.T, h *Runtime) string {
	t.Helper()
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	ids := make([]string, 0, len(h.agentTerms.terms))
	for id := range h.agentTerms.terms {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, cmp.Compare)
	if len(ids) != 1 {
		t.Fatalf("got %d registered terminals, want 1", len(ids))
	}
	return ids[0]
}

// Retire and adoption read the ring while the pump writes it; both must take the terminal's lock (-race).
func TestTerminalOutput_ReleaseAndAdoptWhileTheProcessIsStillWriting(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// A steady writer, so the pump is mid-flight when the release lands.
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1,
		`i=0; while [ $i -lt 400 ]; do printf 'line %s\n' "$i"; i=$((i+1)); done; sleep 0.5`, nil))
	term := singleTerm(t, h)
	termID := onlyTermID(t, h)

	// Wait for the first bytes so the pump is demonstrably running, then race it.
	deadline := time.Now().Add(5 * time.Second)
	for term.rawOutput() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the pump produced nothing in 5s")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := h.agentTerms.release("c1", termID); !ok {
		t.Fatalf("release(%q) found no terminal", termID)
	}
	if _, _, ok := h.agentTerms.Output(termID); !ok {
		t.Error("Output found nothing for a terminal released mid-write")
	}
	waitClosed(t, term.done, "terminal")
}

// Not parallel-safe: it writes a package var.
func withTerminalGroupGrace(t *testing.T, d time.Duration) {
	t.Helper()
	prev := terminalGroupGrace
	terminalGroupGrace = d
	t.Cleanup(func() { terminalGroupGrace = prev })
}

func TestTerminalGroupGrace_IsTwoSeconds(t *testing.T) {
	if terminalGroupGrace != 2*time.Second {
		t.Errorf("terminalGroupGrace = %v, want 2s (the same bound as terminalDrainGrace)", terminalGroupGrace)
	}
}

// A grandchild holding the write end keeps the pipe open past exit; closing the READ end
// is what releases the pump and lets the exit broadcast.
func TestAwaitTerminalExit_ForceClosesTheReaderWhenAGrandchildHoldsThePipe(t *testing.T) {
	logs := captureLogs(t) // not parallel: swaps the slog default
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// The head prints and exits; `sleep` inherits the write end past the grace.
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1,
		`sleep 30 & printf 'head done\n'`, nil))
	term := singleTerm(t, h)

	start := time.Now()
	waitClosed(t, term.done, "terminal exit")
	elapsed := time.Since(start)

	// Bounded by ONE grace: a regression to sequencing the group wait and the drain shows here (~2s vs ~4s).
	if elapsed > terminalDrainGrace+time.Second {
		t.Errorf("exit took %v, want it bounded near ONE %v grace: either the reader was "+
			"never force-closed, or the group wait and the drain were sequenced rather "+
			"than overlapped and a backgrounded daemon now pays both", elapsed, terminalDrainGrace)
	}
	if !strings.Contains(logs.String(), "output still open after exit") {
		t.Errorf("no line about the forced release\nlogs: %s", logs.String())
	}
	// The head's own line still reached the wire before the exit.
	if got := term.rawOutput(); !strings.Contains(got, "head done") {
		t.Errorf("ring = %q, want the head's line: the drain dropped it", got)
	}
}

// The process group must be observed empty before the exit is reported. The grandchild
// holds no descriptors, so only the group wait can delay the exit.
func TestAwaitTerminalExit_WaitsForTheCommandsProcessGroupToEmpty(t *testing.T) {
	logs := captureLogs(t) // not parallel: swaps the slog default
	withTerminalGroupGrace(t, 300*time.Millisecond)
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	// `sleep` joins the head's group and outlives it, with its pipe ends closed.
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1,
		`sleep 30 >/dev/null 2>&1 </dev/null & printf 'head done\n'`, nil))
	term := singleTerm(t, h)

	start := time.Now()
	waitClosed(t, term.done, "terminal exit")
	elapsed := time.Since(start)

	if elapsed < terminalGroupGrace {
		t.Errorf("exit took %v, want at least the %v group grace: awaitExit published "+
			"terminal_exited while the command's group still had a live member, so the "+
			"agent reads a file a grandchild is still writing", elapsed, terminalGroupGrace)
	}
	// One grace, not two.
	if elapsed > terminalGroupGrace+3*time.Second {
		t.Errorf("exit took %v, want it bounded near ONE %v grace: the group wait and the "+
			"drain were sequenced rather than overlapped", elapsed, terminalGroupGrace)
	}
	const msg = "the command exited but its process group did not empty"
	if !strings.Contains(logs.String(), msg) {
		t.Errorf("no line about the group still being alive\nlogs: %s", logs.String())
	}
	if strings.Contains(logs.String(), `"level":"WARN","msg":"agent terminal: `+msg) {
		t.Errorf("the group line is a WARN; a command that backgrounds a daemon on purpose "+
			"logs it on every exit and nobody acts on it\nlogs: %s", logs.String())
	}
}

func terminalExitedPayloads(t *testing.T, h *Runtime) []marotte.TerminalExitedPayload {
	t.Helper()
	type idPayload struct {
		p  marotte.TerminalExitedPayload
		id uint64
	}
	var found []idPayload
	for _, e := range h.bus.fanout.Snapshot() {
		var env struct {
			Type    string                        `json:"type"`
			Payload marotte.TerminalExitedPayload `json:"payload"`
		}
		if err := json.Unmarshal(e.Event.Data, &env); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		if env.Type == string(marotte.EventTerminalExited) {
			found = append(found, idPayload{p: env.Payload, id: e.Offset})
		}
	}
	slices.SortFunc(found, func(a, b idPayload) int { return cmp.Compare(a.id, b.id) })
	out := make([]marotte.TerminalExitedPayload, len(found))
	for i, f := range found {
		out[i] = f.p
	}
	return out
}

// TestTerminalExited_CleanExitCarriesTheExitCode pins exactly one of exit_code and signal; neither reads as still running.
func TestTerminalExited_CleanExitCarriesTheExitCode(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	h.translateACPEvent("c1", h.originOf("c1"), termCreateMsgArgs(t, 1, "true", nil))
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	var payloads []marotte.TerminalExitedPayload
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		payloads = terminalExitedPayloads(t, h)
		if len(payloads) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(payloads) != 1 {
		t.Fatalf("got %d terminal_exited payloads, want 1", len(payloads))
	}
	got := payloads[0]
	if got.Signal != "" {
		t.Errorf("terminal_exited Signal = %q for a clean exit, want empty", got.Signal)
	}
	if got.ExitCode == nil {
		t.Fatal("terminal_exited carried neither exit_code nor signal, so the tab never leaves 'running'")
	}
	if *got.ExitCode != 0 {
		t.Errorf("terminal_exited ExitCode = %d, want 0", *got.ExitCode)
	}
}

// TestTerminalOutput_AnAgentLimitCannotRaiseTheAppsCap pins that outputByteLimit may only shrink the ring.
func TestTerminalOutput_AnAgentLimitCannotRaiseTheAppsCap(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	id := int64(1)
	// Twice the app's cap, printing more than the cap.
	msg := &marotte.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, map[string]any{
		"command":         "yes a | head -n 40000",
		"outputByteLimit": 2 * outputBufferLimit,
	})}

	h.translateACPEvent("c1", h.originOf("c1"), msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
	termID := onlyTermID(t, h)

	text, _, ok := h.agentTerms.Output(termID)
	if !ok {
		t.Fatal("Output found nothing for a registered terminal")
	}
	if len(text) > outputBufferLimit {
		t.Errorf("retained %d bytes of output, want at most the app cap %d;"+
			" an agent-supplied limit must only shrink the ring", len(text), outputBufferLimit)
	}
}

// Eviction is by turn-id equality: one turn's close must not take another turn's records.
func TestCloseTurn_EvictsOnlyTheClosingTurnsRecords(t *testing.T) {
	t.Parallel()
	at := bareTerminals()
	for _, turn := range []string{"t2", "t3"} {
		term := newAgentTerminal(&exec.Cmd{}, "c1", 64)
		term.output.write([]byte(turn + "\n"))
		at.mu.Lock()
		at.retire("term-"+turn, term, turn)
		at.mu.Unlock()
	}

	at.closeTurn("c1", "t3")
	if raw, ok := at.peekRetired("term-t3"); ok {
		t.Errorf("peekRetired(term-t3) = (%q, true) after t3 closed, want it evicted", raw)
	}
	if _, ok := at.peekRetired("term-t2"); !ok {
		t.Error("t3's close evicted t2's record: eviction is by equality on the turn id")
	}

	at.closeTurn("c1", "t2")
	if raw, ok := at.peekRetired("term-t2"); ok {
		t.Errorf("peekRetired(term-t2) = (%q, true) after t2 closed, want it evicted", raw)
	}
}

// A background process stopped in a later turn leaves its record to that turn's close: its own
// turn already closed, so tagging it with the spawning turn would keep it until the chat is deleted.
func TestCloseTurn_EvictsARecordReleasedInALaterTurn(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	turns := &turnStub{cur: map[marotte.ChatID]string{"c1": "t1"}}
	h.agentTerms.currentTurn = turns.read
	termID, _ := spawnSleeper(t, h, "c1", br, 1)

	h.agentTerms.closeTurn("c1", "t1")
	turns.cur["c1"] = "t2"
	h.translateACPEvent("c1", br, termIDMsg(t, 2, methodTermRelease, termID))
	br.awaitResponseTo(t, 2)
	if _, ok := h.agentTerms.peekRetired(termID); !ok {
		t.Fatal("Setup: the released terminal left no record")
	}

	h.agentTerms.closeTurn("c1", "t2")
	if raw, ok := h.agentTerms.peekRetired(termID); ok {
		t.Errorf("peekRetired(%s) = (%q, true) after the releasing turn closed, want it evicted", termID, raw)
	}
}

// A turn read before the release takes at.mu can close in between; output tagged with that turn
// would wait on a close that already ran, so the next close must take it instead.
func TestTerminalRelease_ACloseBeforeTheRetireLeavesTheOutputToTheNextClose(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	turns := &closingTurnStub{turn: "t2"}
	h.agentTerms.currentTurn = turns.read
	termID, _ := spawnSleeper(t, h, "c1", br, 1)

	turns.closeOnNextRead(func() { h.agentTerms.closeTurn("c1", "t2") })
	h.translateACPEvent("c1", br, termIDMsg(t, 2, methodTermRelease, termID))
	br.awaitResponseTo(t, 2)
	if _, _, ok := h.agentTerms.Output(termID); !ok {
		t.Fatalf("Setup: Output(%s) found nothing right after terminal/release", termID)
	}

	h.agentTerms.closeTurn("c1", "t3")
	if _, _, ok := h.agentTerms.Output(termID); ok {
		t.Errorf("Output(%s) still answers after t3 closed: tagged with t2, whose close ran"+
			" during the release, it outlives every later close", termID)
	}
}

// The same race through a bridge's end, whose release reads the turn on the forward loop.
func TestBridgeEnd_ACloseBeforeTheRetireLeavesTheOutputToTheNextClose(t *testing.T) {
	ending := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), ending)
	turns := &closingTurnStub{turn: "t2"}
	h.agentTerms.currentTurn = turns.read
	ending.deliver(termCreateMsg(t, 1, "sleep", []string{"30"}, nil))
	ending.awaitResponseTo(t, 1)
	termID, term := onlyTermOf(t, h, ending)
	pid := term.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	turns.closeOnNextRead(func() { h.agentTerms.closeTurn("c1", "t2") })
	ending.endStream()
	// The bridge end kills only after it retired the output.
	waitClosed(t, term.done, "the ended bridge's terminal")
	if _, _, ok := h.agentTerms.Output(termID); !ok {
		t.Fatalf("Setup: Output(%s) found nothing right after the bridge ended", termID)
	}

	h.agentTerms.closeTurn("c1", "t3")
	if _, _, ok := h.agentTerms.Output(termID); ok {
		t.Errorf("Output(%s) still answers after t3 closed: tagged with t2, whose close ran"+
			" during the bridge end's release, it outlives every later close", termID)
	}
}

// A run chat has no turn whose close evicts its records, so its bridge's end takes them.
func TestReleaseForBridge_DropsARunChatsRecords(t *testing.T) {
	t.Parallel()
	at := bareTerminals()
	run := runChatID("wf_1")
	at.mu.Lock()
	at.retire("run-term", newAgentTerminal(&exec.Cmd{}, run, 64), "")
	at.retire("chat-term", newAgentTerminal(&exec.Cmd{}, "c1", 64), "")
	at.mu.Unlock()

	at.releaseForBridge(run, newRecordingTermBridge())

	if _, ok := at.peekRetired("run-term"); ok {
		t.Error("ReleaseForBridge(run chat) kept the run's record; no turn close would ever evict it")
	}
	if _, ok := at.peekRetired("chat-term"); !ok {
		t.Error("ReleaseForBridge(run chat) evicted another chat's record")
	}
}

// A chunk that renders to nothing (only bidi or zero-width controls) must not become an event.
func TestTerminalEmitter_AChunkThatRendersToNothingIsNotBroadcast(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	term := newAgentTerminal(nil, "c1", 4096)
	emit := h.agentTerms.emitter(t.Context(), term, "t1", "c1")

	emit("\u202c\u200b") // a bidi pop and a zero-width space, both deleted

	if got := terminalOutputPayloads(t, h); len(got) != 0 {
		t.Errorf("emitter broadcast %d terminal_output payloads for text the sanitizer deletes,"+
			" want 0: %+v", len(got), got)
	}
	// A renderable chunk still goes out.
	emit("visible")
	got := terminalOutputPayloads(t, h)
	if len(got) != 1 {
		t.Fatalf("got %d terminal_output payloads after a renderable chunk, want 1: %+v", len(got), got)
	}
	if got[0].Data != "visible" {
		t.Errorf("payload Data = %q, want %q", got[0].Data, "visible")
	}
}

// The teardown line is logged only when something was killed. No t.Parallel: captureLogs swaps the slog default.
func TestKillForTurn_ReportsOnlyARealTeardown(t *testing.T) {
	const wantLine = "interrupt: killed the turn's terminals"

	t.Run("nothing_to_kill", func(t *testing.T) {
		at := bareTerminals()
		logs := captureLogs(t)
		at.KillForTurn("c1")
		if got := logs.String(); strings.Contains(got, wantLine) {
			t.Errorf("KillForTurn(chat with no terminals) logged %q, want no teardown line", got)
		}
	})

	t.Run("one_terminal_killed", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		h.stagePromptTurn(t, "c1")
		_, term := spawnSleeper(t, h, "c1", h.originOf("c1"), 1)

		logs := captureLogs(t)
		h.agentTerms.KillForTurn("c1")
		if got := logs.String(); !strings.Contains(got, wantLine) {
			t.Errorf("KillForTurn(chat with a live terminal) logged %q, want a teardown line", got)
		}
		waitClosed(t, term.done, "the cancelled turn's terminal")

		// Already dead: a second cancel has nothing to kill.
		logs = captureLogs(t)
		h.agentTerms.KillForTurn("c1")
		if got := logs.String(); strings.Contains(got, wantLine) {
			t.Errorf("KillForTurn(turn whose terminal already exited) logged %q, want no teardown line", got)
		}
	})
}

// An undeliverable refusal wedges the turn (Bridge.Call has no deadline), so the log line
// is its only trace. No t.Parallel: captureLogs swaps the slog default.
func TestHandleTerminalRequest_ReportsAnUndeliverableRefusal(t *testing.T) {
	const wantLine = "terminal refusal could not be delivered"
	id := int64(77)
	msg := &marotte.RPCResponse{ID: &id, Method: "terminal/not_a_verb"}

	t.Run("refusal_refused", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), &droppingBridge{fakeBridge: newFakeBridge()})
		logs := captureLogs(t)
		h.handleTerminalRequest(t.Context(), "c1", h.originOf("c1"), "terminal/not_a_verb", msg)
		if got := logs.String(); !strings.Contains(got, wantLine) {
			t.Errorf("handleTerminalRequest(unimplemented verb, refusing bridge) logged %q,"+
				" want an undeliverable-refusal line", got)
		}
	})

	t.Run("refusal_delivered", func(t *testing.T) {
		h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
		logs := captureLogs(t)
		h.handleTerminalRequest(t.Context(), "c1", h.originOf("c1"), "terminal/not_a_verb", msg)
		if got := logs.String(); strings.Contains(got, wantLine) {
			t.Errorf("handleTerminalRequest(unimplemented verb, accepting bridge) logged %q,"+
				" want no undeliverable-refusal line", got)
		}
	})
}

// A chat absent from the map is idle.
type turnStub struct {
	cur map[marotte.ChatID]string
}

func (s *turnStub) read(chatID marotte.ChatID) (string, bool) {
	turn, ok := s.cur[chatID]
	return turn, ok
}

// closingTurnStub reports one turn for every chat; once armed, the next read runs a close
// before it returns, which lands that close between a reader's turn read and its next step.
type closingTurnStub struct {
	onRead func()
	turn   string
	mu     sync.Mutex
}

func (s *closingTurnStub) read(marotte.ChatID) (string, bool) {
	s.mu.Lock()
	onRead := s.onRead
	s.onRead = nil
	s.mu.Unlock()
	if onRead != nil {
		onRead()
	}
	return s.turn, true
}

func (s *closingTurnStub) closeOnNextRead(onRead func()) {
	s.mu.Lock()
	s.onRead = onRead
	s.mu.Unlock()
}

// spawnSleeper runs `sleep 30` through the real terminal/create dispatch; test cleanup kills it.
func spawnSleeper(t *testing.T, h *Runtime, chatID marotte.ChatID, origin acpResponder, reqID int64) (string, *agentTerminal) {
	t.Helper()
	h.agentTerms.mu.Lock()
	before := maps.Clone(h.agentTerms.terms)
	h.agentTerms.mu.Unlock()
	h.translateACPEvent(chatID, origin, termCreateMsg(t, reqID, "sleep", []string{"30"}, nil))
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	for id, term := range h.agentTerms.terms {
		if _, old := before[id]; old {
			continue
		}
		pid := term.cmd.Process.Pid
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		return id, term
	}
	t.Fatalf("Setup: terminal/create on %q registered no terminal", chatID)
	return "", nil
}

func awaitDead(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still alive 3s after it should have been killed", what, pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestKillForTurn_DoesNotKillAnAgentInitiatedTurnsTerminals pins that cancelling a prompt
// never kills a background process a wire-started turn spawned.
func TestKillForTurn_DoesNotKillAnAgentInitiatedTurnsTerminals(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	ctx := t.Context()

	// A turn marotte did not prompt: the first frame of the bracket opens it.
	h.stageWireTurn(t, "c1")
	_, agentBG := spawnSleeper(t, h, "c1", h.originOf("c1"), 1)

	// It ends on the wire's own bracket.
	h.coord.WireTurnEnd(ctx, "c1", marotte.StopReasonEndTurn)

	// The user's next turn, with a command of its own.
	h.stagePromptTurn(t, "c1")
	_, promptCmd := spawnSleeper(t, h, "c1", h.originOf("c1"), 2)

	h.agentTerms.KillForTurn("c1")

	awaitDead(t, promptCmd.cmd.Process.Pid, "the cancelled turn's own terminal")
	if !processAlive(agentBG.cmd.Process.Pid) {
		t.Error("cancelling the user's turn killed the AGENT-initiated turn's terminal: " +
			"that background command was not this cancel's to take")
	}
}

// TestKillForTurn_ScopedToTheOpenTurn pins that a cancel kills only the current turn's terminals.
func TestKillForTurn_ScopedToTheOpenTurn(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	turns := &turnStub{cur: map[marotte.ChatID]string{"c1": "t7", "c2": "t3"}}
	h.agentTerms.currentTurn = turns.read
	origin := h.originOf("c1")

	_, old := spawnSleeper(t, h, "c1", origin, 1) // turn t7's background command
	turns.cur["c1"] = "t8"                        // turn t7 closed and turn t8 opened
	_, cur := spawnSleeper(t, h, "c1", origin, 2) // turn t8, the open turn
	_, curB := spawnSleeper(t, h, "c1", origin, 3)
	_, other := spawnSleeper(t, h, "c2", newRecordingTermBridge(), 4)

	h.agentTerms.KillForTurn("c1")

	awaitDead(t, cur.cmd.Process.Pid, "the open turn's terminal")
	awaitDead(t, curB.cmd.Process.Pid, "the open turn's second terminal")
	if !processAlive(old.cmd.Process.Pid) {
		t.Error("an earlier turn's terminal was killed: that background command was not the cancel's to take")
	}
	if !processAlive(other.cmd.Process.Pid) {
		t.Error("another chat's terminal was killed")
	}
}

// TestKillForTurn_KeepsTheTerminalAnsweringUntilReleased pins that a cancelled turn's terminal
// stays registered: KAS reads its output and exit and stops it after the cancel.
func TestKillForTurn_KeepsTheTerminalAnsweringUntilReleased(t *testing.T) {
	br := newRecordingTermBridge()
	h := hubWithBridge(t, t.TempDir(), br)
	h.stagePromptTurn(t, "c1")
	termID, term := spawnSleeper(t, h, "c1", br, 1)

	h.agentTerms.KillForTurn("c1")
	waitClosed(t, term.done, "the cancelled turn's terminal")

	h.translateACPEvent("c1", br, termIDMsg(t, 2, methodTermOutput, termID))
	out := br.awaitResponseTo(t, 2)
	res, ok := out.result.(map[string]any)
	if out.err != nil || !ok {
		t.Fatalf("terminal/output after the cancel = (%v, %v), want a result", out.result, out.err)
	}
	if st, _ := res["exitStatus"].(map[string]any); st[keySignal] == nil {
		t.Errorf("terminal/output after the cancel: exitStatus = %v, want a signal", res["exitStatus"])
	}

	h.translateACPEvent("c1", br, termIDMsg(t, 3, methodTermKill, termID))
	if kill := br.awaitResponseTo(t, 3); kill.err != nil {
		t.Errorf("terminal/kill after the cancel answered %v, want {}", kill.err)
	}

	h.translateACPEvent("c1", br, termIDMsg(t, 4, methodTermRelease, termID))
	br.awaitResponseTo(t, 4)
	h.agentTerms.mu.Lock()
	_, still := h.agentTerms.terms[termID]
	h.agentTerms.mu.Unlock()
	if still {
		t.Error("terminal/release left the cancelled terminal registered")
	}
}

func TestKillForTurn_NothingOpenIsANoOp(t *testing.T) {
	h := hubWithBridge(t, t.TempDir(), newRecordingTermBridge())
	_, idle := spawnSleeper(t, h, "c1", h.originOf("c1"), 1) // created while the chat is idle

	h.agentTerms.KillForTurn("c1")

	if !processAlive(idle.cmd.Process.Pid) {
		t.Error("KillForTurn(idle chat) killed a terminal")
	}
}
