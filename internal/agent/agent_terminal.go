// Agent terminals for kiro-cli's terminal/* ACP requests: headless subprocesses, not PTYs,
// piped into a byte-limited UTF-8-aware ring; no resize.

package agent

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/ansitext"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
	"github.com/cplieger/marotte/internal/sanitize"
	"github.com/cplieger/marotte/internal/systembin"
)

// keySignal is the signal key of an ACP terminal exit status (KAS zTerminalExitStatus).
const keySignal = "signal"

// terminalDrainGrace bounds awaitExit's wait for output EOF after the process exits (a
// grandchild holding the pipe). Matches cmd.WaitDelay.
const terminalDrainGrace = 2 * time.Second

// terminalGroupGrace bounds a teardown's wait for the process group to empty. A var only for tests.
var terminalGroupGrace = 2 * time.Second

// killTerminalGroup SIGKILLs term through signalGroup and, for a group, waits up to
// terminalGroupGrace for it to empty. A return does not prove the tree is gone.
func killTerminalGroup(term *agentTerminal, termID string) error {
	pgid, err := term.signalGroup(syscall.SIGKILL)
	if pgid > 0 && !procgroup.WaitGone(pgid, terminalGroupGrace) {
		slog.Warn("agent terminal: process group still had a live member after the kill",
			"term_id", termID, "pgid", pgid, "grace", terminalGroupGrace)
	}
	return err
}

// agentTerminal is one headless subprocess spawned by kiro-cli.
type agentTerminal struct {
	exitErr error
	cmd     *exec.Cmd
	done    chan struct{}
	output  *byteRing
	// ansi carries SGR state and the running UTF-16 offset across reads. Pump goroutine only.
	ansi   *ansitext.Parser
	chatID marotte.ChatID
	signal string
	// turn is the spawning turn's id, so an interrupt kills only that turn's processes.
	turn string
	// session is the ACP session the create named, so run bounds can attribute a waiting
	// command to a run's own step. Empty answers false everywhere.
	session  string
	exitCode int
	// pgid is the group the command leads, read right after Start; 0 when none. Guarded by procMu.
	pgid int
	mu   sync.Mutex
	// procMu orders every signal against the reap; separate from mu, which the pump holds per chunk.
	procMu sync.Mutex
	reaped bool
}

// signalGroup signals the group captured at spawn and returns its id; failing that, the
// head alone, returning 0. Once the head is reaped it returns (0, os.ErrProcessDone): the
// group number may name a stranger's group.
func (t *agentTerminal) signalGroup(sig syscall.Signal) (int, error) {
	t.procMu.Lock()
	defer t.procMu.Unlock()
	if t.reaped {
		return 0, os.ErrProcessDone
	}
	if t.pgid > 1 {
		if err := syscall.Kill(-t.pgid, sig); err == nil {
			return t.pgid, nil
		}
	}
	return 0, t.cmd.Process.Signal(sig)
}

// newAgentTerminal builds a running terminal. A constructor because without `ansi` the
// pump nil-panics and without `output` terminal/output returns nothing.
func newAgentTerminal(cmd *exec.Cmd, chatID marotte.ChatID, limit int) *agentTerminal {
	return &agentTerminal{
		cmd:    cmd,
		done:   make(chan struct{}),
		output: newByteRing(limit),
		ansi:   ansitext.NewParser(),
		chatID: chatID,
	}
}

// rawOutput snapshots the raw ring under the pump's lock; it is read while the process still writes.
func (t *agentTerminal) rawOutput() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.String()
}

// wireSpans converts parser spans to the wire shape; internal/ansitext stays a stdlib-only leaf.
func wireSpans(in []ansitext.Span) []marotte.TextSpan {
	if len(in) == 0 {
		return nil
	}
	out := make([]marotte.TextSpan, len(in))
	for i, s := range in {
		out[i] = marotte.TextSpan{Start: s.Start, End: s.End, FG: s.FG, BG: s.BG, Attrs: s.Attrs}
	}
	return out
}

// termEnvVar is one ACP terminal/create `env` entry (KAS zEnvVariable).
type termEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// termLocaleEnvVar is the locale variable a terminal command inherits. Twin of
// internal/bridge's localeEnvVar: change both.
const termLocaleEnvVar = "LANG"

// termLocaleEnv pins C.UTF-8: the image ships no locales, so the C locale would octal-escape non-ASCII paths.
func termLocaleEnv() []string {
	return []string{termLocaleEnvVar + "=C.UTF-8"}
}

// termEnv layers the requested vars over the process environment, then the locale pin
// (last wins in os/exec). Nil when none are requested, so the child inherits os.Environ().
func termEnv(vars []termEnvVar) []string {
	if len(vars) == 0 {
		return nil
	}
	env := os.Environ()
	for _, v := range vars {
		env = append(env, v.Name+"="+v.Value)
	}
	return append(env, termLocaleEnv()...)
}

// exitStatusObject returns the ACP exit-status object; a signal kill omits exitCode
// (KAS requires exitCode>=0). Takes term.mu; call only after term.done is closed.
func (t *agentTerminal) exitStatusObject() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.signal != "" {
		return map[string]any{keySignal: t.signal}
	}
	return map[string]any{keyExitCode: t.exitCode}
}

// exitStatusFromState maps a finished process to (exitCode, signal). A signal death
// returns (0, "<signal>"): ExitCode() is -1 there, which KAS rejects.
func exitStatusFromState(st *os.ProcessState) (exitCode int, signal string) {
	if st == nil {
		return 0, ""
	}
	if code := st.ExitCode(); code >= 0 {
		return code, ""
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 0, ws.Signal().String()
	}
	return 0, "unknown"
}

// agentTerminals holds the active terminals by ID plus the output of terminals since gone.
type agentTerminals struct {
	terms    map[string]*agentTerminal
	byChatID map[marotte.ChatID][]string // chatID → []terminalID
	// retired holds the raw output of released terminals (KAS releases within milliseconds
	// of creating). Bounded by the turn; each record is at most one 64 KiB ring.
	retired map[string]retiredOutput
	// currentTurn reads which turn a chat's activity belongs to now.
	currentTurn func(marotte.ChatID) (string, bool)
	// broadcast publishes lifecycle and output frames.
	broadcast func(context.Context, marotte.ServerEvent)
	// bridges answers the request a terminal operation arrived on.
	bridges *bridgeManager
	// lifecycle supplies the process lifetime, the in-flight counter and the workspace dir.
	lifecycle *lifetime
	mu        sync.Mutex
}

// retiredOutput is what survives a terminal: raw bytes plus the identity to evict it.
// Kept even when empty, so a silent command stays distinguishable from a lost one.
type retiredOutput struct {
	raw    string
	chatID marotte.ChatID
	// turn is the spawning turn's id; empty is evicted by any turn close.
	turn string
}

func newAgentTerminals(bridges *bridgeManager, lc *lifetime,
	broadcast func(context.Context, marotte.ServerEvent),
	currentTurn func(marotte.ChatID) (string, bool),
) *agentTerminals {
	return &agentTerminals{
		terms:       make(map[string]*agentTerminal),
		byChatID:    make(map[marotte.ChatID][]string),
		retired:     make(map[string]retiredOutput),
		bridges:     bridges,
		lifecycle:   lc,
		broadcast:   broadcast,
		currentTurn: currentTurn,
	}
}

// retire records a departing terminal's output for later adoption. Callers hold at.mu.
// Every removal path goes through here: a forgotten retire is silent.
func (at *agentTerminals) retire(id string, term *agentTerminal) {
	if term == nil || term.output == nil {
		return
	}
	at.retired[id] = retiredOutput{raw: term.rawOutput(), chatID: term.chatID, turn: term.turn}
}

// peekRetired returns a retired terminal's raw output without consuming it: KAS can send
// several status frames per tool call and adoption runs on each.
func (at *agentTerminals) peekRetired(id string) (string, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	rec, ok := at.retired[id]
	if !ok {
		return "", false
	}
	return rec.raw, true
}

// CloseTurn evicts the output records of the closed turn, by equality on the turn id;
// a record with no turn belongs to the next close. Called by the winning closer.
func (at *agentTerminals) CloseTurn(chatID marotte.ChatID, turnID string) {
	at.mu.Lock()
	for id, rec := range at.retired {
		if rec.chatID == chatID && (rec.turn == turnID || rec.turn == "") {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
}

// turnOf reads which turn a chat's activity belongs to now, or empty when idle. Nil-safe.
func (at *agentTerminals) turnOf(chatID marotte.ChatID) string {
	if at.currentTurn == nil {
		return ""
	}
	turn, _ := at.currentTurn(chatID)
	return turn
}

// LiveTerminalForSession reports whether the chat holds a running terminal whose create
// named one of these sessions. Narrowed by session because a run's steps share the carrier
// chat; a session-less terminal answers false, so the bound applies.
func (at *agentTerminals) LiveTerminalForSession(chatID marotte.ChatID, sessions map[string]struct{}) bool {
	if len(sessions) == 0 {
		return false
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	for _, id := range at.byChatID[chatID] {
		term, ok := at.terms[id]
		if !ok {
			continue
		}
		if _, named := sessions[term.session]; !named {
			continue
		}
		select {
		case <-term.done:
		default:
			return true
		}
	}
	return false
}

// KillForTurn kills only the terminals the chat's current turn created; an idle chat kills nothing.
func (at *agentTerminals) KillForTurn(chatID marotte.ChatID) {
	cur := at.turnOf(chatID)
	if cur == "" {
		return
	}
	at.mu.Lock()
	ids := at.byChatID[chatID]
	var doomed []doomedTerminal
	kept := ids[:0]
	for _, id := range ids {
		term, ok := at.terms[id]
		if !ok {
			continue
		}
		if term.turn != cur {
			kept = append(kept, id)
			continue
		}
		at.retire(id, term)
		delete(at.terms, id)
		doomed = append(doomed, doomedTerminal{id: id, term: term})
	}
	at.byChatID[chatID] = kept
	at.mu.Unlock()
	for _, d := range doomed {
		if d.term.cmd.Process != nil {
			if err := killTerminalGroup(d.term, d.id); err != nil {
				slog.Debug("agent: turn terminal kill failed", "chat_id", chatID, "error", err)
			}
		}
	}
	if len(doomed) > 0 {
		slog.Info("interrupt: killed the turn's terminals", "chat_id", chatID, "count", len(doomed))
	}
}

// KillForChat kills and removes every terminal of chatID without retiring output: the chat is being deleted.
func (at *agentTerminals) KillForChat(chatID marotte.ChatID) {
	at.mu.Lock()
	ids := at.byChatID[chatID]
	delete(at.byChatID, chatID)
	var doomed []doomedTerminal
	for _, id := range ids {
		term, ok := at.terms[id]
		if ok {
			delete(at.terms, id)
			doomed = append(doomed, doomedTerminal{id: id, term: term})
		}
	}
	for id, rec := range at.retired {
		if rec.chatID == chatID {
			delete(at.retired, id)
		}
	}
	at.mu.Unlock()
	// Outside the lock: the kill waits for the group to empty.
	for _, d := range doomed {
		if d.term.cmd.Process != nil {
			if err := killTerminalGroup(d.term, d.id); err != nil {
				slog.Debug("agent: agent terminal kill failed", "id", d.id, "error", err)
			}
		}
	}
}

// doomedTerminal carries a removed terminal out of the locked section for its kill.
type doomedTerminal struct {
	term *agentTerminal
	id   string
}

// drainAll waits for every terminal to exit, then clears the maps. Contexts derive from
// shutdownCtx: cancel sends SIGTERM and WaitDelay escalates to SIGKILL after 2s.
func (at *agentTerminals) drainAll() {
	at.mu.Lock()
	terms := make(map[string]*agentTerminal, len(at.terms))
	maps.Copy(terms, at.terms)
	at.mu.Unlock()

	if len(terms) == 0 {
		return
	}

	for _, term := range terms {
		<-term.done
	}

	at.mu.Lock()
	for id, term := range terms {
		at.retire(id, term)
		delete(at.terms, id)
	}
	at.byChatID = make(map[marotte.ChatID][]string)
	at.mu.Unlock()
}

// release removes terminalID from both maps and returns it; callers kill outside the lock.
func (at *agentTerminals) release(terminalID string) (*agentTerminal, bool) {
	at.mu.Lock()
	defer at.mu.Unlock()
	term, ok := at.terms[terminalID]
	if !ok {
		return nil, false
	}
	at.retire(terminalID, term)
	delete(at.terms, terminalID)
	if term.chatID != "" {
		ids := at.byChatID[term.chatID]
		for i, id := range ids {
			if id == terminalID {
				at.byChatID[term.chatID] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
	}
	return term, true
}

// handleTerminalRequest dispatches terminal/* ACP requests.
func (rt *Runtime) handleTerminalRequest(ctx context.Context, chatID marotte.ChatID, method string, msg *marotte.RPCResponse) {
	switch method {
	case methodTermCreate:
		rt.agentTerms.respondCreate(ctx, chatID, msg)
	case methodTermOutput:
		rt.agentTerms.respondOutput(ctx, chatID, msg)
	case methodTermRelease:
		rt.agentTerms.respondRelease(ctx, chatID, msg)
	case methodTermWaitForExit:
		rt.agentTerms.respondWaitForExit(ctx, chatID, msg)
	case methodTermKill:
		rt.agentTerms.respondKill(ctx, chatID, msg)
	default:
		// Routed on a `terminal/` prefix, so an unknown verb must still be answered: Bridge.Call
		// has no client deadline, and silence wedges the turn.
		if msg.ID == nil {
			return
		}
		slog.Warn("chat bridge: refusing an unimplemented terminal verb",
			"method", method, "chat_id", chatID, "id", *msg.ID)
		if err := rt.BridgeRespond(ctx, chatID, *msg.ID, nil,
			&marotte.RPCError{
				Code:    marotte.RPCCodeMethodNotFound,
				Message: "unimplemented terminal method: " + method,
			}); err != nil {
			slog.Error("chat bridge: terminal refusal could not be delivered; the turn may be wedged",
				"method", method, "chat_id", chatID, "error", err)
		}
	}
}

// agentShell resolves the shell for an agent command line once per process: bash first,
// POSIX sh as fallback. Through internal/systembin, not exec.LookPath: PATH[0] is the
// toolbelt link dir, which an agent can write. Caching is safe only because the
// candidate set reads no environment.
var agentShell = sync.OnceValue(func() string {
	if p, ok := systembin.Resolve("bash"); ok {
		return p
	}
	return "/bin/sh"
})

// agentCommand builds the process for one terminal/create. KAS leaves `args` UNSET and
// puts the whole line in `command` (kiro-cli 2.18.0), so an absent `args` runs through a
// shell and a present one, empty included, is an argv. Presence, not length, decides.
func agentCommand(ctx context.Context, command string, args *[]string) *exec.Cmd {
	if args != nil {
		return exec.CommandContext(ctx, command, *args...) // #nosec G204 -- agent-controlled
	}
	return exec.CommandContext(ctx, agentShell(), "-c", command) // #nosec G204 -- agent-controlled
}

// derefArgs flattens the presence-preserving pointer for the wire.
func derefArgs(args *[]string) []string {
	if args == nil {
		return nil
	}
	return *args
}

// failCreate answers and logs a terminal/create that could not start; without it a failed exec leaves no server-side trace.
func (at *agentTerminals) failCreate(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse, command, reason string) {
	slog.Warn("agent terminal create failed", "chat_id", chatID, "cmd", command, "reason", reason)
	respondErr(ctx, at.bridges, chatID, msg, reason)
}

// failParse answers and logs a request whose params did not decode: KAS awaits these with
// no timeout. The request's own chatID is what respondErr resolves the bridge by.
func (at *agentTerminals) failParse(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse, method string, err error) {
	slog.Warn("agent terminal request had undecodable params",
		"method", method, "chat_id", chatID, "error", err)
	respondErr(ctx, at.bridges, chatID, msg, "invalid params")
}

func (at *agentTerminals) respondCreate(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var params struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
		// SessionID names which workflow step asked; absent decodes to "" so the terminal is
		// still created, bounded rather than immortal.
		SessionID string `json:"sessionId"`
		// Args is a pointer so an explicit `[]` stays distinct from an omitted field.
		Args *[]string `json:"args"`
		// Env is the ACP terminal/create env array (KAS zEnvVariable {name,value}).
		Env             []termEnvVar `json:"env"`
		OutputByteLimit int          `json:"outputByteLimit"`
	}
	if parseRequest(msg, &params) != nil {
		at.failCreate(ctx, chatID, msg, params.Command, "invalid params")
		return
	}
	if params.Command == "" {
		at.failCreate(ctx, chatID, msg, params.Command, "command is required")
		return
	}
	// Screened before anything is created, so a refusal needs no teardown (agent_terminal_env.go).
	if blocked := screenAgentEnv(params.Env, operatorAllowedEnv()); len(blocked) > 0 {
		slog.Warn("refused an agent terminal that redirects execution through the environment",
			"chat_id", chatID, "command", params.Command, "variables", strings.Join(blocked, ","))
		respondErr(ctx, at.bridges, chatID, msg, "refusing to set "+strings.Join(blocked, ", ")+
			": these variables change what a program executes, so they are not accepted from the agent."+
			" Pass the setting on the command itself, or have the operator allow the name via "+envAllowVar)
		return
	}

	limit := outputBufferLimit
	if params.OutputByteLimit > 0 && params.OutputByteLimit < limit {
		limit = params.OutputByteLimit
	}

	// The command must outlive the per-event ctx, which translateACPEvent cancels on return;
	// the AfterFunc re-attaches shutdown.
	cmdCtx, cmdCancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(at.lifecycle.shutdownCtx, cmdCancel)
	cmd := agentCommand(cmdCtx, params.Command, params.Args)
	// Own process group, so teardown can signal the whole tree (see procgroup.Kill).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if env := termEnv(params.Env); env != nil {
		cmd.Env = env
	}
	if params.Cwd != "" {
		abs, err := at.lifecycle.resolveInsideWorkDir(params.Cwd)
		if err != nil {
			stop()
			cmdCancel()
			at.failCreate(ctx, chatID, msg, params.Command, "cwd escapes workspace: "+err.Error())
			return
		}
		cmd.Dir = abs
	} else {
		cmd.Dir = at.lifecycle.workDir
	}

	term := newAgentTerminal(cmd, chatID, limit)
	cmd.Cancel = func() error {
		_, err := term.signalGroup(syscall.SIGTERM)
		return err
	}

	// One caller-owned pipe for both streams: os/exec closes the pipes it hands out inside
	// Wait, and one pipe preserves write order between stdout and stderr.
	pr, pw, err := os.Pipe()
	if err != nil {
		stop()
		cmdCancel()
		at.failCreate(ctx, chatID, msg, params.Command, "pipe: "+err.Error())
		return
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	// procMu spans Start so a cancel racing the spawn signals the group, not the head alone.
	term.procMu.Lock()
	err = cmd.Start()
	if err == nil {
		if g, ok := procgroup.GroupOf(cmd.Process); ok {
			term.pgid = g
		}
	}
	term.procMu.Unlock()
	if err != nil {
		_ = pw.Close()
		_ = pr.Close()
		stop()
		cmdCancel()
		at.failCreate(ctx, chatID, msg, params.Command, err.Error())
		return
	}
	// The child holds its own write end; closing ours lets the reader see EOF.
	_ = pw.Close()

	termID := newMessageID() // reuse the ID generator for unique terminal IDs

	// Read before at.mu: it takes the turn lifecycle's mutex, and under at.mu that is a second lock order.
	turn := at.turnOf(chatID)

	// Register and broadcast terminal_created BEFORE starting the pump/exit goroutines: event
	// ids follow call order, and a client drops output for an unknown terminal id.
	at.mu.Lock()
	term.turn = turn
	term.session = params.SessionID
	at.terms[termID] = term
	at.byChatID[chatID] = append(at.byChatID[chatID], termID)
	at.mu.Unlock()

	slog.Info("agent terminal created", "chat_id", chatID, "term_id", termID, "cmd", params.Command)
	at.broadcast(ctx, marotte.NewEvent(marotte.EventTerminalCreated, chatID, marotte.TerminalCreatedPayload{
		TerminalID: termID,
		Command:    params.Command,
		// A plain slice on the wire: the client only labels the tab with it.
		Args: derefArgs(params.Args),
	}))
	respondOK(ctx, at.bridges, chatID, msg, map[string]string{"terminalId": termID})

	drained := make(chan struct{})
	at.lifecycle.inflight.Go(func() {
		defer close(drained)
		defer func() { _ = pr.Close() }()
		at.pumpOutput(term, termID, chatID, pr)
	})

	at.lifecycle.inflight.Go(func() {
		at.awaitExit(term, termID, chatID, cmd, stop, cmdCancel, drained, pr)
	})
}

// pumpOutput streams the merged output into the ring and broadcasts each chunk until EOF or error.
func (at *agentTerminals) pumpOutput(term *agentTerminal, termID string, chatID marotte.ChatID, r io.Reader) {
	// This goroutine outlives the per-event ctx, so it takes a runtime-scoped one.
	ctx, cancel := at.lifecycle.derivedContext()
	defer cancel()
	buf := getPumpBuf()
	defer pumpBufPool.Put(buf) //nolint:staticcheck // returned after loop exits

	// pending carries an incomplete trailing rune (≤3 bytes) into the next chunk so the live
	// stream stays valid UTF-8; the ring still gets every raw byte once.
	var pending []byte
	emit := at.emitter(ctx, term, termID, chatID)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			term.mu.Lock()
			term.output.Write(buf[:n]) // ring gets every raw byte, unchanged
			term.mu.Unlock()

			chunk := buf[:n]
			if len(pending) > 0 {
				chunk = append(pending, chunk...)
			}
			hold := incompleteTailLen(chunk)
			if out := chunk[:len(chunk)-hold]; len(out) > 0 {
				emit(string(out))
			}
			// Copied: buf is reused next Read.
			pending = append(pending[:0:0], chunk[len(chunk)-hold:]...)
		}
		if readErr != nil {
			break
		}
	}
	// Leftover incomplete bytes at EOF render as U+FFFD, like the ring.
	if len(pending) > 0 {
		emit(string(pending))
	}
	// Release an escape sequence the stream ended mid-way through.
	base := term.ansi.Offset()
	if text, parsed := term.ansi.Flush(); text != "" {
		at.publishText(ctx, termID, chatID, text, wireSpans(parsed), base)
	}
}

// emitter turns one raw chunk into transcript text: hidden Unicode stripped, escapes
// parsed into style spans. SanitizeUnicode, not SanitizeOutput: the latter strips ANSI
// before the parser sees it, so spans come out empty.
func (at *agentTerminals) emitter(
	ctx context.Context, term *agentTerminal, termID string, chatID marotte.ChatID,
) func(string) {
	return func(raw string) {
		base := term.ansi.Offset()
		text, parsed := term.ansi.Write(sanitize.Unicode(raw))
		if text == "" && len(parsed) == 0 {
			return
		}
		at.publishText(ctx, termID, chatID, text, wireSpans(parsed), base)
	}
}

// publishText broadcasts one rendered chunk. base is the chunk's start in UTF-16 code
// units, read off the parser's own counter: spans are absolute in the same units.
func (at *agentTerminals) publishText(
	ctx context.Context, termID string, chatID marotte.ChatID,
	text string, spans []marotte.TextSpan, base int,
) {
	at.broadcast(ctx, marotte.NewEvent(marotte.EventTerminalOutput, chatID, marotte.TerminalOutputPayload{
		TerminalID: termID,
		Data:       text,
		Spans:      spans,
		Offset:     base,
	}))
}

// incompleteTailLen returns how many trailing bytes of b form an incomplete UTF-8
// sequence (0..3); a standalone invalid byte counts as complete.
func incompleteTailLen(b []byte) int {
	if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
		return 0
	}
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if lead := b[len(b)-i]; utf8.RuneStart(lead) {
			if utf8.FullRune(b[len(b)-i:]) {
				return 0 // a complete (though possibly invalid) rune — don't hold
			}
			return i
		}
	}
	return 0
}

// awaitExit waits for the process, records its exit status, closes term.done and
// broadcasts terminal_exited.
func (at *agentTerminals) awaitExit(
	term *agentTerminal, termID string, chatID marotte.ChatID, cmd *exec.Cmd,
	stop func() bool, cmdCancel context.CancelFunc,
	drained <-chan struct{}, pr *os.File,
) {
	ctx, cancel := at.lifecycle.derivedContext()
	defer cancel()

	term.procMu.Lock()
	pgid := term.pgid
	term.procMu.Unlock()

	// Wait first, then drain: safe only because the pipe is caller-owned.
	err := cmd.Wait()
	term.procMu.Lock()
	term.reaped = true
	term.procMu.Unlock()

	// The head is reaped but the group may not be empty, and the agent will read files a
	// backgrounded grandchild is still writing. Observe, not kill. Joined after the drain
	// so the two graces overlap.
	groupEmptied := make(chan bool, 1)
	go func() { groupEmptied <- pgid == 0 || procgroup.WaitGone(pgid, terminalGroupGrace) }()

	// Bound the drain from exit: a grandchild can hold the pipe open forever. terminal_exited
	// must not be observable before its output (measured: exit as event 365, output as 368).
	select {
	case <-drained:
	case <-time.After(terminalDrainGrace):
		slog.Warn("agent terminal: output still open after exit; releasing the reader",
			"chat_id", chatID, "term_id", termID, "grace", terminalDrainGrace)
		_ = pr.Close()
		<-drained
	}

	// Debug: a deliberate background process reaches this on every exit.
	if !<-groupEmptied {
		slog.Debug("agent terminal: the command exited but its process group did not empty",
			"chat_id", chatID, "term_id", termID, "pgid", pgid, "grace", terminalGroupGrace)
	}

	stop()      // unregister the AfterFunc
	cmdCancel() // release the command context
	term.mu.Lock()
	if err != nil {
		term.exitErr = err
		term.exitCode, term.signal = exitStatusFromState(cmd.ProcessState)
	}
	sig := term.signal
	code := term.exitCode
	term.mu.Unlock()
	close(term.done)
	payload := marotte.TerminalExitedPayload{TerminalID: termID}
	if sig != "" {
		payload.Signal = sig
	} else {
		payload.ExitCode = &code
	}
	at.broadcast(ctx, marotte.NewEvent(marotte.EventTerminalExited, chatID, payload))
}

func (at *agentTerminals) respondOutput(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermOutput, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// The real chatID, or respondErr's bridge lookup misses and the agent hangs.
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	term.mu.Lock()
	output := term.output.String()
	truncated := term.output.Truncated()
	term.mu.Unlock()

	result := map[string]any{"output": output, "truncated": truncated}
	// KAS zTerminalOutputResponse requires exitStatus to be an object or null.
	select {
	case <-term.done:
		result["exitStatus"] = term.exitStatusObject()
	default:
	}
	respondOK(ctx, at.bridges, chatID, msg, result)
}

func (at *agentTerminals) respondRelease(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermRelease, err)
		return
	}
	term, ok := at.release(params.TerminalID)
	if ok && term.cmd.Process != nil {
		// A finished command's group is left running, matching KAS's own terminal.
		if err := killTerminalGroup(term, params.TerminalID); err != nil {
			if procgroup.AlreadyGone(err) {
				slog.Debug("terminal release: kill was a no-op, the process was already reaped",
					"term_id", params.TerminalID, "error", err)
			} else {
				slog.Warn("terminal release: kill failed", "term_id", params.TerminalID, "error", err)
			}
		}
	}
	slog.Info("agent terminal released", "term_id", params.TerminalID)
	// The request's chatID so the ack resolves a bridge for an unknown id. KAS expects an empty object.
	respondOK(ctx, at.bridges, chatID, msg, map[string]any{})
}

func (at *agentTerminals) respondWaitForExit(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermWaitForExit, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge.
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	// A fresh runtime-scoped context: the per-event ctx is cancelled before this runs, and
	// Bridge.Respond drops a write on a cancelled ctx.
	at.lifecycle.inflight.Go(func() {
		fctx, cancel := at.lifecycle.derivedContext()
		defer cancel()
		select {
		case <-term.done:
			respondOK(fctx, at.bridges, chatID, msg, term.exitStatusObject())
		case <-at.lifecycle.done:
			return
		}
	})
}

func (at *agentTerminals) respondKill(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	var params struct {
		TerminalID string `json:"terminalId"`
	}
	if err := parseRequest(msg, &params); err != nil {
		at.failParse(ctx, chatID, msg, methodTermKill, err)
		return
	}
	at.mu.Lock()
	term, ok := at.terms[params.TerminalID]
	at.mu.Unlock()
	if !ok {
		// Real chatID so the not-found error resolves a bridge.
		respondErr(ctx, at.bridges, chatID, msg, "terminal not found")
		return
	}
	if term.cmd.Process != nil {
		// KAS sends terminal/kill on timeout and cancel, racing the command's own exit; losing that race is not a failure.
		if err := killTerminalGroup(term, params.TerminalID); err != nil {
			if procgroup.AlreadyGone(err) {
				slog.Debug("terminal kill was a no-op, the process was already reaped",
					"term_id", params.TerminalID, "error", err)
			} else {
				slog.Warn("terminal kill failed", "term_id", params.TerminalID, "error", err)
			}
		}
	}
	// KAS zKillTerminalResponse is an empty object.
	respondOK(ctx, at.bridges, chatID, msg, map[string]any{})
}

// Output returns a terminal's rendered output for translate.TerminalReader, rendered on
// demand from the raw ring (released terminals included, via retire). ok reports whether
// the terminal is known, so a silent command answers ("", nil, true).
func (at *agentTerminals) Output(terminalID string) (string, []marotte.TextSpan, bool) {
	at.mu.Lock()
	term, live := at.terms[terminalID]
	at.mu.Unlock()

	var raw string
	if live {
		raw = term.rawOutput()
	} else {
		var known bool
		raw, known = at.peekRetired(terminalID)
		if !known {
			return "", nil, false
		}
	}
	if raw == "" {
		return "", nil, true
	}
	text, spans := ansitext.Parse(sanitize.Unicode(raw))
	return text, wireSpans(spans), true
}
