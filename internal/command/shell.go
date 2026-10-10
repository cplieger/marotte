package command

// The `!cmd` shell interception. procout.Buffer's Write reports the bytes it kept, so a truncated
// capture makes io.Copy return io.ErrShortWrite, which Cmd.Wait hands back as the error of a child
// that exited 0.

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

// shellOutputCap bounds the captured stdout+stderr of a `!cmd` shell interception.
const shellOutputCap = 1 * 1024 * 1024

// shellTimeout is the default timeout for user-initiated `!cmd` shell interceptions.
const shellTimeout = 30 * time.Second

// shellChatName is the header fallback for a `!cmd` on a chat no record exists
// for: the command text's first 80 runes, the shell's own naming rule.
func shellChatName(text string) string {
	name := truncateRunes(text, 80)
	if name != text {
		name += ellipsis
	}
	return name
}

// handleShellInterception runs a "!" prefixed prompt as a local shell command:
// one turn of three entries, the turn_open carrying the command as its prompt,
// one text entry carrying the output, and the turn_close.
func handleShellInterception(ctx context.Context, roles *promptRoles, cmd *marotte.ClientCommand, p *marotte.PromptCommand) (any, error) {
	shellCmd := strings.TrimPrefix(p.Text, "!")
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		return nil, StatusError(http.StatusBadRequest, errEmptyPrompt)
	}

	// The same per-chat reservation a prompt takes, as a try: one mechanism serializes prompts and
	// shells.
	if !roles.admission.TryReserveTurn(cmd.ChatID, marotte.TurnSourceLocalShell) {
		return nil, StatusError(http.StatusConflict, errBusy)
	}
	defer roles.admission.ReleaseTurnReservation(cmd.ChatID)

	roles.lifecycle.InflightAdd(1)
	defer roles.lifecycle.InflightDone()

	turnID, err := roles.turnOutcome.OpenTurn(ctx, cmd.ChatID, TurnOpen{
		Source: marotte.TurnSourceLocalShell,
		Prompt: &marotte.EntryPrompt{ID: p.MessageID, Text: p.Text},
		Init:   func(c *marotte.Chat) { c.Name = shellChatName(p.Text) },
	})
	if err != nil {
		if errors.Is(err, chat.ErrTombstoned) {
			return nil, StatusError(http.StatusConflict, ErrChatNotFound)
		}
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	defer roles.turnOutcome.ReleaseTurn(cmd.ChatID, turnID)
	nameDefaultChat(ctx, roles.chats, cmd.ChatID, p.Text)
	if !roles.turnOutcome.StartTurn(ctx, cmd.ChatID, turnID) {
		roles.turnOutcome.AbandonInFlightTurn(ctx, cmd.ChatID, turnID, marotte.StopReasonCancelled, "", "", 0)
		return nil, StatusError(http.StatusConflict, errBusy)
	}

	slog.Info("shell interception", "chat_id", cmd.ChatID, "cmd_len", len(shellCmd))
	start := time.Now()

	shellCtx, cancel := context.WithTimeout(ctx, shellTimeout)
	defer cancel()

	shellProc := exec.CommandContext(shellCtx, "sh", "-c", shellCmd)
	shellProc.Dir = roles.workspace.Dir
	// One *procout.Buffer on both streams: os/exec guarantees at most one goroutine writes when the
	// two writers are equal.
	capped := procout.NewBuffer(shellOutputCap)
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

func nameDefaultChat(ctx context.Context, chats chatStore, chatID marotte.ChatID, text string) {
	if _, err := chats.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists || c.Name != marotte.DefaultChatName || c.NameSetByUser {
			return false
		}
		c.Name = shellChatName(text)
		return true
	}); err != nil {
		slog.Warn("shell interception: name chat", "chat_id", chatID, keyError, err)
	}
}

// renderShellResult wraps sanitized output in a Markdown fence one backtick longer than the longest
// backtick run in it, so output containing ``` cannot close the fence early, and appends a status
// line.
func renderShellResult(output string, runErr error, timedOut bool) string {
	output = strings.TrimRight(output, "\n")
	body := shellStatusLine(runErr, timedOut)
	if output != "" {
		body = output + "\n" + body
	}
	fence := shellFence(body)
	return fence + "\n" + body + "\n" + fence
}

// shellStatusLine reports how the command ended: a timeout in words rather than the opaque "signal:
// killed", otherwise the exit code.
func shellStatusLine(runErr error, timedOut bool) string {
	switch {
	case timedOut:
		return fmt.Sprintf("[command timed out after %s]", shellTimeout)
	case runErr == nil:
		return "[exit 0]"
	default:
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			return fmt.Sprintf("[exit %d]", exitErr.ExitCode())
		}
		return "[error: " + runErr.Error() + "]"
	}
}

// shellFence returns a backtick fence of at least three, always one longer than the longest
// backtick run in body.
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
