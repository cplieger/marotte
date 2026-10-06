package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procgroup"
)

// Start launches the kiro-cli subprocess and creates (acpSessionID == "") or loads an ACP session; one call per
// bridge. ctx bounds the handshake, which is also capped here by handshakeBudget or replayBudget; opts.Lifetime
// bounds the subprocess and is required.
func (b *Bridge) Start(ctx context.Context, opts *marotte.StartOpts) error {
	// The subprocess outlives this call; marotte.StartOpts.Lifetime explains why it is required.
	if opts.Lifetime == nil {
		return errors.New("bridge: StartOpts.Lifetime is required: it bounds the kiro-cli subprocess, and every default for it is a subprocess nothing can cancel")
	}
	b.lifecycleCtx = opts.Lifetime
	// The engine is not stored: only startProcess reads it, from opts. Immutable after Start; SetModel and initialize
	// read it lock-free.
	b.enableHooks = opts.EnableHooks
	b.secretStorage = opts.SecretStorage
	b.presets = opts.Presets
	b.toolSearch = opts.ToolSearch
	b.knowledge = opts.Knowledge
	b.memory = opts.Memory
	b.disableSessionTitles = opts.DisableSessionTitles
	b.disableAutoCompaction = opts.DisableAutoCompaction
	b.features = opts.Features
	b.extraArgs = opts.ExtraArgs
	b.contentCollection = opts.ContentCollection
	if opts.SessionID != "" && !ids.ValidSessionID(opts.SessionID) {
		return fmt.Errorf("invalid acp session id: %q", opts.SessionID)
	}
	if opts.Model != "" && !validIdent(opts.Model) {
		return fmt.Errorf("invalid model identifier: %q", opts.Model)
	}
	if err := b.startProcess(opts.AgentEngine); err != nil {
		return err
	}
	// A turn may run for hours, a handshake may not. Call has no deadline, so an unanswered initialize or session/new
	// blocks until the process dies, while the chat answers 409 and singleflight folds callers onto the wedge. Bounded per
	// phase: a resume replays the whole transcript before the load response.
	budget, phase := handshakeBudget, "session start"
	if opts.SessionID != "" {
		budget, phase = replayBudget, "session resume"
	}
	// Timed: the handshake is the slowest routine operation, and nothing measured it. One wall-clock number per spawn
	// for log aggregation, bracketing only the two phases so cold and warm containers compare.
	handshakeStart := time.Now()
	hctx, cancelHandshake := context.WithTimeout(ctx, budget)
	defer cancelHandshake()
	b.resolveTerminalTimeout(hctx, opts.TerminalTimeout)
	if err := b.initialize(hctx); err != nil {
		b.Stop()
		return handshakeTimeout(err, phase, budget)
	}
	// Between initialize and the first session verb: KAS scopes the ignore list to the connection and pushes it into
	// every session, so the first turn already enforces it.
	b.applyIgnoreFiles(hctx, opts.IgnoreFiles)
	b.reapplyTerminalTimeout(hctx, opts.TerminalTimeout)
	var err error
	if opts.SessionID != "" {
		err = b.loadSession(hctx, opts)
	} else {
		err = b.newSession(hctx, opts)
		// Fail closed when the budget expired inside newSession. The appliers are best-effort, and an expiry would silently
		// leave the session on the wrong mode or model, or in autopilot for a supervised chat, since applySupervised runs
		// last. A handshake finishing in its last microseconds fails anyway; the next Send recovers.
		if err == nil {
			err = hctx.Err()
		}
	}
	if err != nil {
		// initialize answered, so the log directory is known, exactly when an operator needs it.
		slog.Warn("bridge handshake failed",
			"phase", phase,
			"kas_log_dir", logsafe.Field(b.KASLogDir()),
			"error", err,
		)
		b.Stop()
		return handshakeTimeout(err, phase, budget)
	}
	// Lets a later Stop or error be tied to its bridge; session ids are bounded by chats, models by catalog.
	slog.Info("bridge started",
		"session_id", b.SessionID(),
		"model", b.ModelID(),
		"work_dir", b.workDir,
		"acp_session_id", opts.SessionID,
		"kas_log_dir", logsafe.Field(b.KASLogDir()),
		// phase keeps a resume's replay time apart from fresh sessions'.
		"phase", phase,
		"elapsed_ms", time.Since(handshakeStart).Milliseconds(),
	)
	return nil
}

// bridgeGroupGrace bounds Stop's confirmation that the killed group emptied.
const bridgeGroupGrace = 2 * time.Second

// bridgeTeardownGrace is how long a SIGTERMed tree gets before SIGKILL: KAS tears sessions down (SessionEnd hooks
// included) under its own 4 s cap, so one second more. A var for tests.
var bridgeTeardownGrace = 5 * time.Second

// waitStatus renders cmd.Wait's error for a log line. nil is a clean exit(0), itself the finding for a relay that
// should outlive the session.
func waitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// reapProcess SIGTERMs the tree, escalates to SIGKILL past bridgeTeardownGrace, reaps it and reports what it found;
// Stop's caller already closed stdin. The group, not the head: on 2.18.0 a head-only kill left kiro-cli-chat and its
// node child alive at about 250 MB (procgroup.Kill).
func (b *Bridge) reapProcess() {
	sessionID := b.SessionID()
	// Before the signal empties it (procgroup.GroupOf).
	pgid, owns := procgroup.GroupOf(b.cmd.Process)
	termErr := procgroup.Kill(b.cmd.Process, syscall.SIGTERM)
	// Already gone means kiro-cli ended on its own, so the wait status is the only record of why.
	endedItself := procgroup.AlreadyGone(termErr)
	if termErr != nil && !endedItself {
		slog.Error("signal kiro-cli", "session_id", sessionID, "error", termErr)
	}
	if !endedItself && (!owns || !procgroup.WaitGone(pgid, bridgeTeardownGrace)) {
		// Without a group there is nothing to wait on; kill the head now.
		if killErr := procgroup.Kill(b.cmd.Process, syscall.SIGKILL); killErr != nil && !procgroup.AlreadyGone(killErr) {
			slog.Error("kill kiro-cli", "session_id", sessionID, "error", killErr)
		}
		if owns {
			slog.Warn("kiro-cli did not exit within the teardown grace; killed",
				"session_id", sessionID, "grace_ms", bridgeTeardownGrace.Milliseconds())
		}
	}
	// Wait releases the process entry, so switch and cull cycles leak no zombies.
	waitErr := b.cmd.Wait()
	// The wait status is the diagnosis when the stream died unexplained; Debug when it is our own signal.
	if endedItself {
		slog.Warn("kiro-cli ended on its own", "session_id", sessionID, "wait", waitStatus(waitErr))
	} else {
		slog.Debug("kiro-cli reaped", "session_id", sessionID, "wait", waitStatus(waitErr))
	}
	// A reaped head does not prove the tree went, and a surviving acp-server holds KAS's workflow lease against resumes.
	if owns && !procgroup.WaitGone(pgid, bridgeGroupGrace) {
		slog.Error("kiro-cli process group outlived its bridge; it still holds any workflow lease it owned",
			"session_id", sessionID,
			"pgid", pgid,
			"grace_ms", bridgeGroupGrace.Milliseconds())
	}
}

// Stop shuts the subprocess down (reapProcess) and closes NotifCh. Safe to call repeatedly: shutdown, tab close,
// model switch and load recovery can race, and sync.Once prevents a double close of b.done. NotifCh also closes here
// when no read loop started, so a consumer of a failed Start still ends.
func (b *Bridge) Stop() {
	b.stopOnce.Do(func() {
		close(b.done)
		if p := b.stdin.Load(); p != nil {
			p.w.Close()
		}
		if b.cmd != nil && b.cmd.Process != nil {
			b.reapProcess()
		}
		b.readMu.Lock()
		if !b.notifClaimed {
			close(b.notifCh)
		}
		b.readMu.Unlock()
	})
}

// claimNotifClose hands closing notifCh to the read loop, or reports false when Stop already closed it.
func (b *Bridge) claimNotifClose() bool {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	select {
	case <-b.done:
		return false
	default:
	}
	b.notifClaimed = true
	return true
}

// handshakeBudget bounds a session start (initialize, session/new, the config appliers). A backstop, so not a
// setting; a var for tests. Sized for the first chat after a version change, when KAS unpacks its runtime. MCP
// initialization runs inside session/prompt. It stays under the client's command timeout.
var handshakeBudget = 120 * time.Second

// replayBudget bounds a resume: KAS streams the whole transcript before the load response, a length nothing here
// bounds. Kept separate so a fresh-start failure is not reported five minutes late. If a transcript outgrows it, add
// an activity-reset deadline in the frame reader, not a timer on the replay settle (load_projection.go).
var replayBudget = 300 * time.Second

// writeDeadline bounds one write to kiro-cli's stdin; a backstop, so not a setting, and a var for tests. writeMu
// serialises every outbound frame, so one wedged write silently blocks every later Respond, Call and Notify,
// session/cancel included. 30s: an 8 MiB reply drains in under a second through the 64 KiB pipe, and too short costs
// one resumable turn.
var writeDeadline = 30 * time.Second

// handshakeTimeout rewrites an expired handshake budget into an actionable error and returns others unchanged. The
// raw `session/new: context deadline exceeded` hides that pressing Send again is the whole recovery. errors.Is(err,
// context.DeadlineExceeded) still holds and the raw cause stays as the tail.
func handshakeTimeout(err error, phase string, budget time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s did not finish within %s. kiro-cli stopped answering during the handshake. Send again to retry, and check /api/health if it keeps happening: %w",
		phase, budget, err)
}

// localeEnvVar is the locale variable every child of this bridge inherits. Twin of internal/agent's
// termLocaleEnvVar, which cannot import this package; change both.
const localeEnvVar = "LANG"

// localeEnv pins the text encoding of everything kiro-cli spawns. The image has no locales package, so an unset LANG
// is glibc's C locale and git octal-escapes non-ASCII paths. C.UTF-8 is built in; this half cannot be overridden by an
// inherited value.
func localeEnv() []string {
	return []string{localeEnvVar + "=C.UTF-8"}
}

// buildACPArgs assembles the kiro-cli ACP invocation. The relay owns authentication, so KAS never asks marotte for
// the access token.
func buildACPArgs(engine string) []string {
	if engine == "" {
		engine = marotte.AgentEngineV3
	}
	return []string{"acp", "--agent-engine", engine, "--auth-method", "cli"}
}

func (b *Bridge) startProcess(engine string) error {
	if b.cliPath == "" {
		// No active version: the first install is running or failed with no fallback. The client shows this message;
		// /api/health carries the same verdict.
		return errors.New("kiro-cli is not available yet: the pinned version is still installing or its install failed (see /api/health)")
	}
	// Operator flags follow the derived ones, so they are initial values: kiro-cli takes the last spelling, and
	// switch_model / set_effort still win later. Filtered in acp_args.go.
	args := append(buildACPArgs(engine), b.extraArgs...)
	// Stop owns teardown; lifecycleCtx (the runtime's shutdown context) is the backstop if Stop races or panics. Start
	// refuses a nil Lifetime, so it is never nil and never a request or turn context.
	b.cmd = exec.CommandContext(b.lifecycleCtx, b.cliPath, args...) //nolint:gosec // G204: binary path from the install manager, never user input
	// Own process group, so teardown reclaims the whole tree; closing stdin alone does not reclaim it (procgroup.Kill).
	b.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Env is always set: a nil Env inherits the parent implicitly, which would bypass the credential screen.
	// b.extraEnv still lands last, so the active version wins PATH.
	env, dropped := screenBridgeEnv(os.Environ(), b.extraEnv, b.envAllow)
	// After the screen, so kascap's KIRO_FEATURE_* door wins over anything inherited or overlaid.
	env = append(env, kascap.ChildEnv(b.spawn())...)
	// After the screen and the door: os/exec keeps a key's last value.
	env = append(env, localeEnv()...)
	b.cmd.Env = env
	if len(dropped) > 0 {
		// Names only: the values are the credentials. One line per spawn, since a chat carries the environment down.
		slog.Warn("dropped credential-shaped variables from the kiro-cli process; agent shell commands still inherit them",
			"variables", strings.Join(dropped, ","),
			"hint", "keep credentials out of the server environment, or allow the name via "+EnvAllowVar)
	}
	// Backstop when lifecycleCtx ends before Stop: the reapProcess ladder (close stdin, SIGTERM the group, SIGKILL after
	// bridgeTeardownGrace). The tree shares marotte's pipe, so EOF reaches it all. A second Close is expected after Stop,
	// so its error is dropped.
	b.cmd.Cancel = func() error {
		if p := b.stdin.Load(); p != nil {
			_ = p.w.Close()
		}
		return procgroup.Kill(b.cmd.Process, syscall.SIGTERM)
	}
	b.cmd.WaitDelay = bridgeTeardownGrace
	stdin, err := b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := b.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := b.cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	b.stdin.Store(&stdinPipe{w: stdin})
	// A frameReader, because an oversize frame must be survivable (bridge_frame.go).
	b.stdout = newFrameReader(bufio.NewReaderSize(stdoutPipe, stdoutBufSize))
	if startErr := b.cmd.Start(); startErr != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return fmt.Errorf("start kiro-cli acp: %w", startErr)
	}
	go b.forwardStderr(stderrPipe)
	go b.readLoop()
	return nil
}

// forwardStderr drains kiro-cli's stderr line by line into slog at the classified level with a source field, so
// crash traces and rate-limit notices keep their level and kiro-cli cannot forge slog-shaped JSON into marotte's
// stderr. A line is classified by its JSON "level" field, else by keywords at word boundaries (needing a trailing
// colon or bracket, so "0 errors found" is not an error). Lines past stderrLineCap are marked and drained. The
// goroutine ends when cmd.Wait closes the pipe.
func (b *Bridge) forwardStderr(r io.Reader) {
	br := bufio.NewReaderSize(r, stderrLineCap+1)
	for {
		line, ok, err := readStderrLine(br)
		if err != nil || !ok {
			return
		}
		lvl := classifyStderrLevel(line)
		slog.Log(b.lifecycleCtx, lvl, "kiro-cli stderr",
			"source", "kiro_cli_stderr",
			"line", line)
	}
}

const stderrTruncationMarker = "... [truncated]"

func readStderrLine(r *bufio.Reader) (line string, ok bool, err error) {
	buf := make([]byte, 0, 4096)
	truncated := false
	for {
		fragment, readErr := r.ReadSlice('\n')
		if readErr != bufio.ErrBufferFull {
			fragment = bytes.TrimSuffix(fragment, []byte{'\n'})
			fragment = bytes.TrimSuffix(fragment, []byte{'\r'})
		}
		remaining := stderrLineCap - len(buf)
		if len(fragment) > remaining {
			fragment = fragment[:remaining]
			truncated = true
		}
		buf = append(buf, fragment...)
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", false, readErr
		}
		if errors.Is(readErr, io.EOF) && len(buf) == 0 && len(fragment) == 0 {
			return "", false, nil
		}
		if truncated {
			buf = buf[:stderrLineCap-len(stderrTruncationMarker)]
			buf = append(buf, stderrTruncationMarker...)
		}
		return string(buf), true, nil
	}
}

// jsonLevelMap maps structured JSON "level" field values to slog levels.
var jsonLevelMap = map[string]slog.Level{
	"ERROR":   slog.LevelError,
	"WARN":    slog.LevelWarn,
	"WARNING": slog.LevelWarn,
	"DEBUG":   slog.LevelDebug,
}

// stderrKeywordRule maps an unstructured keyword to a slog level.
type stderrKeywordRule struct {
	Keyword string
	Level   slog.Level
}

// stderrKeywordRules maps keywords to levels for unstructured stderr lines; first match wins.
var stderrKeywordRules = []stderrKeywordRule{
	{"panic", slog.LevelError},
	{"fatal", slog.LevelError},
	{"error", slog.LevelError},
	{"warn", slog.LevelWarn},
}

// classifyStderrLevel determines the slog level for a kiro-cli stderr line.
func classifyStderrLevel(line string) slog.Level {
	// A structured line with a "level" field.
	if line != "" && line[0] == '{' {
		var structured struct {
			Level string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &structured) == nil && structured.Level != "" {
			if lvl, ok := jsonLevelMap[strings.ToUpper(structured.Level)]; ok {
				return lvl
			}
			return slog.LevelInfo
		}
	}

	// Word-boundary matching for unstructured lines.
	low := strings.ToLower(line)
	for _, rule := range stderrKeywordRules {
		if matchesKeyword(low, rule.Keyword) {
			return rule.Level
		}
	}
	return slog.LevelInfo
}

// matchesKeyword reports whether keyword appears at a word boundary followed by a colon, bracket or separator, so
// substrings like "errors" do not match.
func matchesKeyword(low, keyword string) bool {
	idx := 0
	for {
		pos := strings.Index(low[idx:], keyword)
		if pos < 0 {
			return false
		}
		pos += idx
		if pos > 0 && low[pos-1] >= 'a' && low[pos-1] <= 'z' {
			idx = pos + len(keyword)
			continue
		}
		// Followed by ':', '[', ']', ' ', or the end.
		end := pos + len(keyword)
		if end >= len(low) {
			return true
		}
		ch := low[end]
		if ch == ':' || ch == '[' || ch == ']' || ch == ' ' || ch == '=' {
			return true
		}
		idx = end
	}
}
