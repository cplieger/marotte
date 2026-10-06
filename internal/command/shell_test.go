package command

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// TestShellFence verifies the fence is sized one backtick longer than the
// longest backtick run in the body, so command output containing a ```
// run can never close the fence early.
func TestShellFence(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"no backticks", "plain output", 3},
		{"single backtick", "a`b", 3},
		{"double backtick", "a``b", 3},
		{"triple backtick run", "```", 4},
		{"code block in output", "text\n```\ncode\n```\nmore", 4},
		{"five backtick run", "`````", 6},
		{"runs split by newline stay separate", "``\n``", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fence := shellFence(tc.body)
			if len(fence) != tc.want {
				t.Errorf("shellFence(%q) len = %d, want %d", tc.body, len(fence), tc.want)
			}
			if strings.Contains(tc.body, fence) {
				t.Errorf("body %q contains fence %q — output could close it early", tc.body, fence)
			}
		})
	}
}

// TestRenderShellResult_CodeFenceInOutput pins that a command whose output
// itself contains a ``` fence (e.g. !cat README.md) is wrapped in a longer
// fence and preserved verbatim rather than breaking out into Markdown.
func TestRenderShellResult_CodeFenceInOutput(t *testing.T) {
	output := "# README\n```go\nfunc main() {}\n```\ndone"
	got := renderShellResult(output, nil, false)

	if !strings.HasPrefix(got, "````\n") {
		t.Errorf("result should open with a 4-backtick fence:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n````") {
		t.Errorf("result should close with a 4-backtick fence:\n%s", got)
	}
	if !strings.Contains(got, output) {
		t.Errorf("output not preserved verbatim:\n%s", got)
	}
	if !strings.Contains(got, "[exit 0]") {
		t.Errorf("missing exit status line:\n%s", got)
	}
}

// TestShellStatusLine covers the timeout message (LOW) and the exit-code
// line (LOW): a timed-out command shows a clear message instead of the
// opaque "signal: killed", and a normal exit shows its code.
func TestShellStatusLine(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		got := shellStatusLine(errors.New("signal: killed"), true)
		want := "[command timed out after 30s]"
		if got != want {
			t.Errorf("shellStatusLine(timeout) = %q, want %q", got, want)
		}
	})

	t.Run("success", func(t *testing.T) {
		if got := shellStatusLine(nil, false); got != "[exit 0]" {
			t.Errorf("shellStatusLine(nil) = %q, want %q", got, "[exit 0]")
		}
	})

	t.Run("nonzero exit code", func(t *testing.T) {
		runErr := exec.Command("sh", "-c", "exit 3").Run()
		if runErr == nil {
			t.Fatal("expected a non-nil error from 'exit 3'")
		}
		if got := shellStatusLine(runErr, false); got != "[exit 3]" {
			t.Errorf("shellStatusLine(exit 3) = %q, want %q", got, "[exit 3]")
		}
	})
}

// heldAdmissionDeps overrides the admission try on the bench stub to refuse, the
// state a chat is in while ANY holder — a prompt's blocked spawn included — owns
// the slot.
type heldAdmissionDeps struct {
	*benchDeps
	tried int
}

func (d *heldAdmissionDeps) TryReserveTurn(marotte.ChatID, marotte.TurnOpenSource) bool {
	d.tried++
	return false
}

// TestHandleShellInterception_HeldAdmissionReturns409Immediately asserts that the shell door is a TRY against
// the prompt's reservation, never a wait.
func TestHandleShellInterception_HeldAdmissionReturns409Immediately(t *testing.T) {
	deps := &heldAdmissionDeps{benchDeps: newBenchDeps()}
	cmd := &marotte.ClientCommand{Type: "prompt", ChatID: "c1"}
	p := &marotte.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	start := time.Now()
	_, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p)
	elapsed := time.Since(start)

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (busy)", statusOf(err))
	}
	if !strings.Contains(errText(err), "busy") {
		t.Errorf("body = %q, want it to mention busy", errText(err))
	}
	if deps.tried != 1 {
		t.Errorf("TryReserveTurn called %d times, want 1: the shell door admits through the reservation", deps.tried)
	}
	if elapsed > time.Second {
		t.Errorf("refusal took %v, want an immediate answer", elapsed)
	}
}

// shellStoreDeps is the benchDeps double with a chat store that actually invokes
// its mutate callback, which is what the shell interception needs to get past
// its "was the user message persisted" gate.
type shellStoreDeps struct {
	*benchDeps
	// finalized is every output handed to FinalizeLocalShellTurn: the shell's one
	// text entry, rendered.
	finalized []string
	mutations int
}

func (d *shellStoreDeps) Mutate(_ context.Context, _ marotte.ChatID, mutate func(*marotte.Chat, bool) bool) (string, error) {
	if !mutate(&marotte.Chat{}, false) {
		return "", nil
	}
	d.mutations++
	return strconv.Itoa(d.mutations), nil
}

func (d *shellStoreDeps) Get(context.Context, marotte.ChatID) (*marotte.Chat, bool) {
	return &marotte.Chat{}, true
}

func (d *shellStoreDeps) FinalizeLocalShellTurn(_ context.Context, _ marotte.ChatID, _, output string) {
	d.finalized = append(d.finalized, output)
}

// TestHandleShellInterception_TruncatedOutputIsStillASuccessfulCommand pins the capture contract at
// the site: output past ShellOutputCap still reports the command's real exit.
func TestHandleShellInterception_TruncatedOutputIsStillASuccessfulCommand(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	deps := &shellStoreDeps{benchDeps: newBenchDeps()}
	cmd := &marotte.ClientCommand{Type: "prompt", ChatID: "c1"}
	// Units of 1000, not 1024: 1 MiB is a multiple of 1024 and of io.Copy's 32 KiB buffer, so an
	// aligned producer would miss the short-write path.
	p := &marotte.PromptCommand{
		Text:      `!i=0; while [ $i -lt 1100 ]; do printf "%01000d" 0; i=$((i+1)); done`,
		MessageID: "m-1",
	}

	if _, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p); err != nil {
		t.Fatalf("HandleShellInterception: %v", err)
	}
	if len(deps.finalized) != 1 {
		t.Fatalf("the turn was finalized %d times, want once with the rendered output", len(deps.finalized))
	}
	body := deps.finalized[0]

	if !strings.Contains(body, "[exit 0]") {
		tail := body[max(len(body)-120, 0):]
		t.Errorf("a command that exited 0 was not reported as such; body tail = %q", tail)
	}
	if !strings.Contains(body, "[output truncated at 1 MiB]") {
		t.Error("output crossed the cap but was not labelled truncated")
	}
	if len(body) > ShellOutputCap+1024 {
		t.Errorf("assistant body is %d bytes, want at most the cap plus the trailer", len(body))
	}
}

// A `!cmd` on a still-default-named chat names it after the command, the way a
// prompt's first message names a chat; a chat that already has a name keeps it.
func TestNameDefaultChat_DerivesTheChatNameFromTheCommand(t *testing.T) {
	const eighty = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	cases := []struct {
		name     string
		named    bool
		text     string
		wantName string
	}{
		{name: "the command becomes the name", text: "!go test ./...", wantName: "!go test ./..."},
		{name: "eighty runes is the last length kept whole", text: eighty, wantName: eighty},
		{name: "longer text is cut and marked", text: eighty + " and then some more", wantName: eighty + "..."},
		{name: "a chat that already has a name keeps it", named: true, text: "!go test ./...", wantName: "a chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.named {
				seedEmptyChat(t, store, "c1")
			} else {
				seedDefaultNamedChat(t, store, "c1")
			}

			nameDefaultChat(t.Context(), store, "c1", tc.text)

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if c.Name != tc.wantName {
				t.Errorf("name after a %d-byte command = %q, want %q", len(tc.text), c.Name, tc.wantName)
			}
		})
	}
}

// The interception's summary line is what an operator reads after the fact, so
// the two facts it carries about a command that SUCCEEDED have to be true: the
// command did not exit with an error, and persisting its output did not fail.
// A summary that reports a failure the run did not have sends the next reader
// looking at the wrong thing.
func TestHandleShellInterception_SuccessLogsTheOutcomeHonestly(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	logs := captureLogs(t)
	deps := &shellStoreDeps{benchDeps: newBenchDeps()}
	cmd := &marotte.ClientCommand{Type: "prompt", ChatID: "c1"}
	p := &marotte.PromptCommand{Text: "!echo hi", MessageID: "m-1"}

	if _, err := HandleShellInterception(t.Context(), promptRolesOf(deps), cmd, p); err != nil {
		t.Fatalf("HandleShellInterception = %v, want it to succeed", err)
	}

	out := logs.String()
	if !strings.Contains(out, "exit_error=false") {
		t.Errorf("summary does not report exit_error=false: %s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a successful interception logged an error: %s", out)
	}
}
