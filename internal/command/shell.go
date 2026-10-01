package command

// The `!cmd` shell interception.
//
// Output capture is procout's, not this package's: internal/auth and
// internal/server already share procout.Buffer, whose Write also reports
// the bytes it kept, which makes io.Copy return io.ErrShortWrite on a
// truncated capture — Cmd.Wait then hands that back as the command's error
// on a child that exited 0.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/procout"
	"github.com/cplieger/marotte/internal/sanitize"
)

// ShellOutputCap bounds the captured stdout+stderr of a `!cmd` shell interception.
const ShellOutputCap = 1 * 1024 * 1024

// ShellTimeout is the default timeout for user-initiated `!cmd` shell
// interceptions. Exposed as a package-level constant so tests and
// future settings overrides can reference the default.
const ShellTimeout = 30 * time.Second

// shellChatName is the header fallback for a `!cmd` on a chat no record exists
// for: the command text's first 80 runes, the shell's own naming rule.
func shellChatName(text string) string {
	name := TruncateRunes(text, 80)
	if name != text {
		name += ellipsis
	}
	return name
}

// HandleShellInterception runs a "!" prefixed prompt as a local shell command:
// one turn of three entries, the turn_open carrying the command as its prompt,
// one text entry carrying the output, and the turn_close.
func HandleShellInterception(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand, p *marotte.PromptCommand) (any, error) {
	shellCmd := strings.TrimPrefix(p.Text, "!")
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		return nil, StatusError(http.StatusBadRequest, errEmptyPrompt)
	}

	// Admission: the same per-chat reservation a prompt takes, as a try —
	// never a wait. One mechanism serializes prompts and shells, whatever
	// state the bridge is in.
	if !roles.admission.TryReserveTurn(cmd.ChatID, marotte.TurnSourceLocalShell) {
		return nil, StatusError(http.StatusConflict, errBusy)
	}
	defer roles.admission.ReleaseTurnReservation(cmd.ChatID)

	roles.lifecycle.InflightAdd(1)
	defer roles.lifecycle.InflightDone()

	turnID, err := roles.turnOutcome.OpenTurn(ctx, cmd.ChatID, marotte.TurnSourceLocalShell,
		&marotte.EntryPrompt{ID: p.MessageID, Text: p.Text},
		func(c *marotte.Chat) { c.Name = shellChatName(p.Text) })
	if err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusConflict, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	defer roles.turnOutcome.ReleaseTurn(cmd.ChatID, turnID)
	nameDefaultChat(ctx, roles.chats, cmd.ChatID, p.Text)
	if !roles.turnOutcome.StartTurn(ctx, cmd.ChatID, turnID) {
		roles.turnOutcome.AbandonInFlightTurn(ctx, cmd.ChatID, turnID, marotte.StopReasonCancelled, "")
		return nil, StatusError(http.StatusConflict, errBusy)
	}

	slog.Info("shell interception", "chat_id", cmd.ChatID, "cmd_len", len(shellCmd))
	start := time.Now()

	// Bound the command with a 30s timeout derived from the request
	// context, so a client disconnect also cancels the shell process.
	shellCtx, cancel := context.WithTimeout(ctx, ShellTimeout)
	defer cancel()

	shellProc := exec.CommandContext(shellCtx, "sh", "-c", shellCmd)
	shellProc.Dir = roles.workspace.Dir
	// One *procout.Buffer on both streams is the documented way to merge them:
	// os/exec compares the two writers and guarantees at most one goroutine
	// calls Write at a time, so the two copiers do not race.
	capped := procout.NewBuffer(ShellOutputCap)
	shellProc.Stdout = capped
	shellProc.Stderr = capped
	runErr := shellProc.Run()

	raw := capped.String()
	if capped.Truncated() {
		raw += "\n[output truncated at 1 MiB]"
	}
	output := sanitize.Output(raw)
	timedOut := errors.Is(shellCtx.Err(), context.DeadlineExceeded)

	slog.Info("shell interception complete",
		"chat_id", cmd.ChatID,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"exit_error", runErr != nil,
		"timed_out", timedOut,
		"truncated", capped.Truncated())

	roles.turnOutcome.FinalizeLocalShellTurn(ctx, cmd.ChatID, turnID, renderShellResult(output, runErr, timedOut))
	return responseOK, nil
}

// nameDefaultChat gives a still-default-named chat the command's own name, the
// header write settleComposerOnPrompt does for a prompt.
func nameDefaultChat(ctx context.Context, chats ChatStore, chatID marotte.ChatID, text string) {
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.Name != marotte.DefaultChatName {
			return false
		}
		c.Name = shellChatName(text)
		return true
	}); err != nil {
		slog.Warn("shell interception: name chat", "chat_id", chatID, keyError, err)
	}
}

// renderShellResult wraps sanitized command output in a Markdown code
// fence and appends a status line describing how the command ended.
//
// The fence length is dynamic — one backtick longer than the longest
// backtick run anywhere in the body — so output that itself contains a
// ``` run (e.g. `!cat README.md`, `!git show`) can never close the fence
// early and leak the remainder to the client as rendered Markdown.
func renderShellResult(output string, runErr error, timedOut bool) string {
	output = strings.TrimRight(output, "\n")
	body := shellStatusLine(runErr, timedOut)
	if output != "" {
		body = output + "\n" + body
	}
	fence := shellFence(body)
	return fence + "\n" + body + "\n" + fence
}

// shellStatusLine reports the command's outcome as a bracketed status
// line shown beneath the output. A timeout gets a clear message rather
// than the opaque "signal: killed" the OS reports for the SIGKILL; every
// other outcome shows the process exit code so a non-zero exit is visible.
func shellStatusLine(runErr error, timedOut bool) string {
	switch {
	case timedOut:
		return fmt.Sprintf("[command timed out after %s]", ShellTimeout)
	case runErr == nil:
		return "[exit 0]"
	default:
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			return fmt.Sprintf("[exit %d]", exitErr.ExitCode())
		}
		// The command could not be run at all (e.g. sh missing, or the
		// request context was cancelled before the process started).
		return "[error: " + runErr.Error() + "]"
	}
}

// shellFence returns a backtick code fence long enough to wrap body
// without body's own backtick runs closing it: at least three backticks,
// and always one more than the longest consecutive backtick run in body.
func shellFence(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(longest+1, 3))
}
