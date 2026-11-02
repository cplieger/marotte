package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/marotte/internal/kascap"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/nodeopts"
	"github.com/cplieger/marotte/internal/procgroup"
	"golang.org/x/sys/unix"
)

const bridgeGroupGrace = 2 * time.Second

// bridgeTeardownGrace is how long a SIGTERMed tree gets before SIGKILL: KAS tears sessions down (SessionEnd hooks
// included) under its own 4 s cap, so one second more. A var for tests.
var bridgeTeardownGrace = 5 * time.Second

// waitStatus renders cmd.Wait's error for a log line. nil is a clean exit(0), itself the anomaly for a relay that
// should outlive its sessions.
func waitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// sidecarExitOf renders the reaped process's own status as "exit_code=N" or "signal=NAME", or "" when unknown.
func sidecarExitOf(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return "signal=" + ws.Signal().String()
	}
	return fmt.Sprintf("exit_code=%d", state.ExitCode())
}

// reapProcess reaps the sidecar's group, then the node engine's: on 2.18.0 a head-only kill left kiro-cli-chat and
// its node child alive at about 250 MB (procgroup.Kill), and the node engine leads its own group (sweepEngineGroups).
// stdin is already closed. endedItself is endedOnItsOwn's answer from before that close.
func (e *Engine) reapProcess(endedItself bool) {
	// Before the signal empties it (procgroup.GroupOf).
	pgid, owns := procgroup.GroupOf(e.cmd.Process)
	e.noteEngineGroups()
	termErr := procgroup.Kill(e.cmd.Process, syscall.SIGTERM)
	gone := procgroup.AlreadyGone(termErr)
	if termErr != nil && !gone {
		slog.Error("signal kiro-cli", "generation", e.generation, "error", termErr)
	}
	if !gone && (!owns || !procgroup.WaitGone(pgid, bridgeTeardownGrace)) {
		// Without a group there is nothing to wait on; kill the head now.
		if killErr := procgroup.Kill(e.cmd.Process, syscall.SIGKILL); killErr != nil && !procgroup.AlreadyGone(killErr) {
			slog.Error("kill kiro-cli", "generation", e.generation, "error", killErr)
		}
		if owns {
			slog.Warn("kiro-cli did not exit within the teardown grace; killed",
				"generation", e.generation, "grace_ms", bridgeTeardownGrace.Milliseconds())
		}
	}
	// Wait releases the process entry, so restarts leak no zombies.
	waitErr := e.cmd.Wait()
	e.mu.Lock()
	e.sidecarExit = sidecarExitOf(e.cmd.ProcessState)
	e.mu.Unlock()
	if endedItself {
		// The wait status is the only record of why.
		slog.Warn("kiro-cli ended on its own", "generation", e.generation, "wait", waitStatus(waitErr))
	} else {
		slog.Debug("kiro-cli reaped", "generation", e.generation, "wait", waitStatus(waitErr))
	}
	// A reaped head does not prove the tree went, and a surviving acp-server holds KAS's workflow lease against resumes.
	if owns && !procgroup.WaitGone(pgid, bridgeGroupGrace) {
		slog.Error("the engine's process group outlived it; it still holds any workflow lease it owned",
			"generation", e.generation, "pgid", pgid, "grace_ms", bridgeGroupGrace.Milliseconds())
	}
	e.sweepEngineGroups()
}

// exitSettle bounds the wait for a sidecar whose stdout reached EOF to become waitable: the kernel closes an exiting
// process's files before it marks the exit, so a probe right after the EOF can fall in between.
const exitSettle = 100 * time.Millisecond

// endedOnItsOwn reports whether the sidecar exited before marotte closed its stdin or signalled it. It must run
// before the stdin close, on which kiro-cli exits. A nil lifecycleCtx means no cmd.Cancel was installed to signal it.
func (e *Engine) endedOnItsOwn() bool {
	if e.lifecycleCtx != nil && e.lifecycleCtx.Err() != nil {
		return false
	}
	pid := e.cmd.Process.Pid
	e.mu.Lock()
	sawEOF := e.exitReason == ExitEnded
	e.mu.Unlock()
	if !sawEOF {
		return exitedUnreaped(pid)
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	settled := time.After(exitSettle)
	for !exitedUnreaped(pid) {
		select {
		case <-settled:
			return false
		case <-tick.C:
		}
	}
	return true
}

// exitedUnreaped reports whether pid has exited, without reaping it: cmd.Wait stays the one reap, after the reads end
// (os/exec StdoutPipe).
func exitedUnreaped(pid int) bool {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return err == nil && info.Signo == int32(unix.SIGCHLD)
}

// noteEngineGroups adds the groups below the sidecar to engineGroups. kiro-cli's node engine leads its own group,
// which its stdio MCP servers join, so the sidecar's group kill misses them. It runs after initialize and again at
// reap: once the relay dies they are init's, and no walk finds them.
func (e *Engine) noteEngineGroups() {
	if e.cmd == nil || e.cmd.Process == nil {
		return
	}
	found := procgroup.GroupsBelow(e.cmd.Process.Pid)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, g := range found {
		// The newer stamp wins: an id found again may now name a later group below the sidecar.
		if i := slices.IndexFunc(e.engineGroups, func(n procgroup.Group) bool { return n.ID == g.ID }); i >= 0 {
			e.engineGroups[i] = g
		} else {
			e.engineGroups = append(e.engineGroups, g)
		}
	}
}

// sweepEngineGroups gives each noted group bridgeGroupGrace to empty after the sidecar's, then kills what is left: an
// MCP server that ignores stdin EOF outlives an abrupt engine death, and tini reaps zombies but signals no live
// orphan.
func (e *Engine) sweepEngineGroups() {
	e.mu.Lock()
	groups := slices.Clone(e.engineGroups)
	e.mu.Unlock()
	for _, g := range groups {
		// A stale id's members are a later group's, so neither its wait nor its refusal is ours.
		if g.Stale() || procgroup.WaitGone(g.ID, bridgeGroupGrace) {
			continue
		}
		survivors := procgroup.LiveMembers(g.ID)
		if err := procgroup.KillGroup(g, syscall.SIGKILL); err != nil {
			// A group that emptied since the wait is refused or ESRCH, and that is the outcome wanted.
			if !g.Stale() && procgroup.LiveMembers(g.ID) > 0 {
				slog.Error("kill kiro-cli engine group", "generation", e.generation, "pgid", g.ID, "error", err)
			}
			continue
		}
		slog.Warn("killed MCP and engine survivors after the engine ended",
			"generation", e.generation, "pgid", g.ID, "count", survivors, "grace_ms", bridgeGroupGrace.Milliseconds())
		if !procgroup.WaitGone(g.ID, bridgeGroupGrace) {
			slog.Error("the engine's process group outlived it; it still holds any workflow lease it owned",
				"generation", e.generation, "pgid", g.ID, "grace_ms", bridgeGroupGrace.Milliseconds())
		}
	}
}

// Stop ends the engine: it closes stdin, reaps the process tree, fails every pending request and closes every lane.
// Safe to call repeatedly and concurrently with an exit the engine caused itself; it returns once Done is closed.
func (e *Engine) Stop() {
	e.recordExit(ExitStopped)
	e.teardown()
	<-e.done
}

// recordExit keeps the first engine-scoped evidence; a later one does not relabel the end.
func (e *Engine) recordExit(reason exitReason) {
	e.mu.Lock()
	if e.exitReason == "" {
		e.exitReason = reason
	}
	e.mu.Unlock()
}

// fail records reason and starts the teardown off the caller's goroutine: the caller may hold writeMu, which the exit
// path waits on.
func (e *Engine) fail(reason exitReason) {
	e.recordExit(reason)
	go e.teardown()
}

// teardown is the one end of the engine. With a read loop running, the reap closes stdout and the loop runs the exit
// path; without one, teardown runs it itself.
func (e *Engine) teardown() {
	e.stopOnce.Do(func() {
		close(e.stopping)
		started := e.cmd != nil && e.cmd.Process != nil
		endedItself := started && e.endedOnItsOwn()
		if p := e.stdin.Load(); p != nil {
			_ = p.w.Close()
		}
		if started {
			e.reapProcess(endedItself)
		}
		if !e.readLoopStarted.Load() {
			e.exitPath()
		}
		<-e.exited
		close(e.done)
	})
}

// localeEnvVar is the locale variable every child of the engine inherits. Twin of internal/agent's termLocaleEnvVar,
// which cannot import this package; change both.
const localeEnvVar = "LANG"

// localeEnv pins the text encoding of everything kiro-cli spawns. The image has no locales package, so an unset LANG
// is glibc's C locale and git octal-escapes non-ASCII paths. C.UTF-8 is built in.
func localeEnv() []string {
	return []string{localeEnvVar + "=C.UTF-8"}
}

// toolkitTelemetryEnvVar is read by kiro-cli's Rust binary, not KAS, so it has no kascap row (the environment census
// admits only variables the KAS bundle reads). Set to any value, it skips the legacy toolkit telemetry client.
const toolkitTelemetryEnvVar = "KIRO_DISABLE_TOOLKIT_TELEMETRY"

func isToolkitTelemetryEnv(kv string) bool { return strings.HasPrefix(kv, toolkitTelemetryEnvVar+"=") }

// toolkitTelemetryEnv follows the telemetryEnabled row: off sets the variable, on leaves it unset, kiro-cli's default.
func toolkitTelemetryEnv(disable bool) []string {
	if !disable {
		return nil
	}
	return []string{toolkitTelemetryEnvVar + "=1"}
}

const nodeOptionsVar = "NODE_OPTIONS"

// withHeapLimit rewrites env's NODE_OPTIONS to carry mib (nodeopts.WithHeapLimit). A malformed inherited value is
// left untouched and logged: Node then refuses it itself, so the start fails visibly instead of running on a
// corrupted option string. mib 0 returns env unchanged.
func withHeapLimit(env []string, mib int, generation uint64) []string {
	if mib == 0 {
		return env
	}
	inherited := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, nodeOptionsVar+"="); ok {
			inherited = v
		}
	}
	composed, err := nodeopts.WithHeapLimit(inherited, mib)
	if err != nil {
		slog.Error("engine: the memory limit was not applied because NODE_OPTIONS is malformed",
			"generation", generation, "reason", err)
		return env
	}
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, nodeOptionsVar+"=") })
	return append(out, nodeOptionsVar+"="+composed)
}

// buildACPArgs assembles the engine's argv: the v3 engine, then the sign-in backend's declared arguments, once. The
// operator's flags follow (startProcess), and refuseReason keeps an auth flag out of them.
func buildACPArgs(authArgs []string) []string {
	return append([]string{"acp", "--agent-engine", marotte.AgentEngineV3}, authArgs...)
}

func (e *Engine) startProcess() error {
	if e.cliPath == "" {
		// No active version: the first install is running or failed with no fallback. The client shows this message;
		// /api/health carries the same verdict.
		return errors.New("kiro-cli is not available yet: the pinned version is still installing or its install failed (see /api/health)")
	}
	// Operator flags follow the derived ones, so they are initial values: kiro-cli takes the last spelling. Filtered in
	// acp_args.go.
	args := append(buildACPArgs(e.authArgs), e.extraArgs...)
	// Stop owns teardown; lifecycleCtx (the runtime's shutdown context) is the backstop if Stop races or panics. Start
	// refuses a nil Lifetime.
	e.cmd = exec.CommandContext(e.lifecycleCtx, e.cliPath, args...) //nolint:gosec // G204: binary path from the install manager, never user input
	// Own process group, so teardown reclaims the whole tree; closing stdin alone does not reclaim it (procgroup.Kill).
	e.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	e.cmd.Env = e.processEnv()
	// Backstop when lifecycleCtx ends before Stop: close stdin and SIGTERM the sidecar's group, SIGKILL after
	// bridgeTeardownGrace. A second Close is expected after Stop, so its error is dropped.
	e.cmd.Cancel = func() error {
		if p := e.stdin.Load(); p != nil {
			_ = p.w.Close()
		}
		return procgroup.Kill(e.cmd.Process, syscall.SIGTERM)
	}
	e.cmd.WaitDelay = bridgeTeardownGrace
	stdin, err := e.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := e.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := e.cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	// A frameReader, because an oversize frame must be survivable (bridge_frame.go).
	e.stdout = newFrameReader(bufio.NewReaderSize(stdoutPipe, stdoutBufSize))
	if startErr := e.cmd.Start(); startErr != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return fmt.Errorf("start kiro-cli acp: %w", startErr)
	}
	// Published only once Start returned: a failed write starts a teardown that reads cmd.Process, which Start writes.
	e.stdin.Store(&stdinPipe{w: stdin})
	e.mu.Lock()
	e.startedAt = time.Now()
	e.mu.Unlock()
	go e.forwardStderr(stderrPipe)
	e.readLoopStarted.Store(true)
	go e.readLoop()
	return nil
}

// processEnv builds the engine's environment. Always set: a nil Env inherits the parent implicitly, which would
// bypass the credential screen.
func (e *Engine) processEnv() []string {
	env, dropped := screenBridgeEnv(os.Environ(), e.extraEnv, e.envAllow)
	// A setting decides these even when it withholds them (kascap.OwnsEnv), so no inherited value survives.
	env = slices.DeleteFunc(env, func(kv string) bool { return kascap.OwnsEnv(kv) || isToolkitTelemetryEnv(kv) })
	env = append(env, kascap.ChildEnv(&e.spawn)...)
	env = append(env, toolkitTelemetryEnv(e.spawn.DisableTelemetry)...)
	// After the screen and the door: os/exec keeps a key's last value.
	env = append(env, localeEnv()...)
	env = withHeapLimit(env, e.heapMiB, e.generation)
	if len(dropped) > 0 {
		// Names only: the values are the credentials.
		slog.Warn("dropped credential-shaped variables from the kiro-cli process; agent shell commands still inherit them",
			"variables", strings.Join(dropped, ","),
			"hint", "keep credentials out of the server environment, or allow the name via "+EnvAllowVar)
	}
	return env
}

// The stderr lines that name an engine end's cause; the node child writes to the sidecar's stderr pipe.
const (
	jsHeapOOMLine         = "JavaScript heap out of memory"
	relayWriteTimeoutLine = "timed out writing to the v3 engine's stdin"
)

// forwardStderr drains kiro-cli's stderr line by line into slog at the classified level with a source field, so crash
// traces keep their level and kiro-cli cannot forge slog-shaped JSON into marotte's stderr. It also flags the two lines
// the engine-exit record names. The goroutine ends when cmd.Wait closes the pipe.
func (e *Engine) forwardStderr(r io.Reader) {
	br := bufio.NewReaderSize(r, stderrLineCap+1)
	for {
		line, ok, err := readStderrLine(br)
		if err != nil || !ok {
			return
		}
		if strings.Contains(line, jsHeapOOMLine) {
			e.jsHeapOOM.Store(true)
		}
		if strings.Contains(line, relayWriteTimeoutLine) {
			e.relayWriteTimeout.Store(true)
		}
		slog.Log(e.lifecycleCtx, classifyStderrLevel(line), "kiro-cli stderr",
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

var jsonLevelMap = map[string]slog.Level{
	"ERROR":   slog.LevelError,
	"WARN":    slog.LevelWarn,
	"WARNING": slog.LevelWarn,
	"DEBUG":   slog.LevelDebug,
}

type stderrKeywordRule struct {
	Keyword string
	Level   slog.Level
}

// First match wins.
var stderrKeywordRules = []stderrKeywordRule{
	{"panic", slog.LevelError},
	{"fatal", slog.LevelError},
	{"error", slog.LevelError},
	{"warn", slog.LevelWarn},
}

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
