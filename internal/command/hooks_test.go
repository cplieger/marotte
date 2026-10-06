package command

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// hookCmd builds an *marotte.ClientCommand carrying the marshaled hook payload p.
func hookCmd(t *testing.T, p hookCreatePayload) *marotte.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return &marotte.ClientCommand{Payload: json.RawMessage(raw)}
}

// TestValidateHookPayload exercises the decode/name/event gate, the
// per-field length caps (a field of exactly MaxHookField bytes is
// accepted; one byte over is 413), and the action-specific empty checks.
func TestValidateHookPayload(t *testing.T) {
	const max = MaxHookField
	atMax := strings.Repeat("a", max)
	overMax := strings.Repeat("a", max+1)
	const validName = "valid-hook"
	// A trigger KAS really loads: a fake one would assert that a hook destined to be discarded
	// validates.
	const validEvent = "PostFileSave"

	cases := []struct {
		name     string
		p        hookCreatePayload
		raw      []byte
		wantCode int
		wantErr  bool
	}{
		{name: "valid askAgent", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "do stuff"}, wantCode: 0, wantErr: false},
		{name: "valid runCommand", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: "make build"}, wantCode: 0, wantErr: false},
		{name: "invalid json", raw: []byte(`{not json`), wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty name", p: hookCreatePayload{Name: "", EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty event_type", p: hookCreatePayload{Name: validName, EventType: "", ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},

		{name: "askAgent blank prompt", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "   "}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "runCommand blank command", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: ""}, wantCode: http.StatusBadRequest, wantErr: true},

		{name: "description at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: atMax}, wantCode: 0, wantErr: false},
		{name: "description over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// The size gate runs before the trigger check, so a field at exactly MaxHookField is
		// refused as an unknown trigger (400, not 413).
		{name: "event_type at max", p: hookCreatePayload{Name: validName, EventType: atMax, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "event_type over max", p: hookCreatePayload{Name: validName, EventType: overMax, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		{name: "action_type at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: atMax, Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "action_type over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: overMax, Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		{name: "prompt at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: atMax}, wantCode: 0, wantErr: false},
		{name: "prompt over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		{name: "command at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: atMax}, wantCode: 0, wantErr: false},
		{name: "command over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		{name: "patterns at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: atMax}, wantCode: 0, wantErr: false},
		{name: "patterns over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		{name: "a matcher on a none-subject trigger is refused", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p", Patterns: `\.go$`}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "an alias spelling is refused too", p: hookCreatePayload{Name: validName, EventType: "userTriggered", ActionType: "askAgent", Prompt: "p", Patterns: "x"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "a none-subject trigger with no matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "whitespace is not a matcher", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p", Patterns: "   "}, wantCode: 0, wantErr: false},
		{name: "a tool trigger with NO matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "PreToolUse", ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "a session end command hook is accepted", p: hookCreatePayload{Name: validName, EventType: "SessionEnd", ActionType: "runCommand", Command: "log"}, wantCode: 0, wantErr: false},
		{name: "a session end agent hook is refused", p: hookCreatePayload{Name: validName, EventType: "sessionEnd", ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "a tool trigger with a matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "PreToolUse", ActionType: "askAgent", Prompt: "p", Patterns: "fsWrite"}, wantCode: 0, wantErr: false},

		{name: "name at max", p: hookCreatePayload{Name: atMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "name over max", p: hookCreatePayload{Name: overMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmd *marotte.ClientCommand
			if tc.raw != nil {
				cmd = &marotte.ClientCommand{Payload: json.RawMessage(tc.raw)}
			} else {
				cmd = hookCmd(t, tc.p)
			}
			_, _, code, err := validateHookPayload(cmd)
			if code != tc.wantCode {
				t.Errorf("validateHookPayload(%s) code = %d, want %d", tc.name, code, tc.wantCode)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("validateHookPayload(%s) err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func FuzzValidateHookPayload(f *testing.F) {
	f.Add([]byte(`{"name":"my-hook","event_type":"PostFileSave","action_type":"askAgent","prompt":"do stuff"}`))
	f.Add([]byte(`{"name":"my-hook","event_type":"file:save","action_type":"askAgent","prompt":"do stuff"}`))
	f.Add([]byte(`{"name":"build","event_type":"PostFileSave","action_type":"runCommand","command":"make build"}`))
	f.Add(make([]byte, 9000))
	f.Add([]byte(`{"name":"","event_type":"x","action_type":"askAgent","prompt":"p"}`))
	f.Add([]byte(`{"name":"h","event_type":"x","action_type":"bad","prompt":"p"}`))
	f.Add([]byte(`{"name":"../evil","event_type":"x","action_type":"askAgent","prompt":"p"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &marotte.ClientCommand{Payload: json.RawMessage(data)}
		_, _, code, err := validateHookPayload(cmd)

		if err == nil && code != 0 {
			t.Errorf("err==nil but code=%d", code)
		}
		if err != nil && code == 0 {
			t.Errorf("err=%v but code=0", err)
		}
		if code == 0 && err == nil {
			var p hookCreatePayload
			if json.Unmarshal(data, &p) != nil {
				t.Error("validation passed but JSON unmarshal fails")
			}
		}
	})
}

// TestBuildHookDoc pins the v1 envelope shape and the action/matcher/
// timeout mapping for both action branches.
func TestBuildHookDoc(t *testing.T) {
	t.Run("askAgent maps to an agent action", func(t *testing.T) {
		doc := buildHookDoc(&hookCreatePayload{
			Name: "Review", EventType: "fileEdited", ActionType: "askAgent",
			Prompt: "review", Patterns: `\.go$`, Description: "desc",
		})
		if doc.Version != "v1" {
			t.Errorf("version = %q, want v1", doc.Version)
		}
		if len(doc.Hooks) != 1 {
			t.Fatalf("hooks = %d, want 1", len(doc.Hooks))
		}
		h := doc.Hooks[0]
		if h.Name != "Review" || h.Trigger != "PostFileSave" ||
			h.Matcher != `\.go$` || h.Description != "desc" {
			t.Errorf("hook = %+v", h)
		}
		if h.Action.Type != "agent" || h.Action.Prompt != "review" || h.Action.Command != "" {
			t.Errorf("action = %+v", h.Action)
		}
		if h.Timeout != 0 {
			t.Errorf("timeout = %d, want 0 (omitted)", h.Timeout)
		}
	})

	t.Run("runCommand maps to a command action with timeout", func(t *testing.T) {
		doc := buildHookDoc(&hookCreatePayload{
			Name: "Lint", EventType: "PostFileSave", ActionType: "runCommand",
			Command: "make lint", Timeout: 30,
		})
		h := doc.Hooks[0]
		if h.Action.Type != "command" || h.Action.Command != "make lint" || h.Action.Prompt != "" {
			t.Errorf("action = %+v", h.Action)
		}
		if h.Timeout != 30 {
			t.Errorf("timeout = %d, want 30", h.Timeout)
		}
		if h.Matcher != "" {
			t.Errorf("matcher = %q, want empty", h.Matcher)
		}
	})
}

// TestBuildHookDoc_SessionEndIsNotStop pins the trigger KAS reads in the file:
// SessionEnd fires once at teardown, unlike the per-turn Stop.
func TestBuildHookDoc_SessionEndIsNotStop(t *testing.T) {
	for _, in := range []string{"SessionEnd", "sessionend"} {
		doc := buildHookDoc(&hookCreatePayload{Name: "se", EventType: in, ActionType: "runCommand", Command: "log"})
		if got := doc.Hooks[0].Trigger; got != "SessionEnd" {
			t.Errorf("buildHookDoc(event_type %q).Trigger = %q, want SessionEnd", in, got)
		}
	}
}

// TestValidateHookPayload_IneffectiveMatcherNamesTheTrigger guards the MESSAGE: a bare "invalid
// payload" on a seven-field form moves the guessing to the user.
func TestValidateHookPayload_IneffectiveMatcherNamesTheTrigger(t *testing.T) {
	_, _, code, err := validateHookPayload(hookCmd(t, hookCreatePayload{
		Name: "valid-hook", EventType: "sessionStart", ActionType: "askAgent",
		Prompt: "p", Patterns: `  \.go$  `,
	}))
	if code != http.StatusBadRequest || err == nil {
		t.Fatalf("validateHookPayload = (%d, %v), want 400 with an error", code, err)
	}
	for _, want := range []string{"SessionStart", `\.go$`, "patterns"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), `  \.go$  `) {
		t.Errorf("refusal %q quotes the untrimmed value; buildHookDoc stores the trimmed one", err.Error())
	}
}

func createHookPayload() hookCreatePayload {
	return hookCreatePayload{Name: "on-save", EventType: "PostFileSave", ActionType: "askAgent", Prompt: "review it"}
}

func TestCmdCreateHook_WritesIntoTheWorkspaceAndRefusesARepeat(t *testing.T) {
	ws := t.TempDir()
	cmd := hookCmd(t, createHookPayload())

	if _, err := CmdCreateHook(t.Context(), Workspace{Dir: ws}, cmd); err != nil {
		t.Fatalf("CmdCreateHook() = %v, want nil", err)
	}
	info, err := os.Stat(filepath.Join(ws, ".kiro", "hooks", "on-save.json"))
	if err != nil {
		t.Fatalf("hook file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("hook file mode = %v, want 0600", got)
	}

	_, err = CmdCreateHook(t.Context(), Workspace{Dir: ws}, cmd)
	var se *statusError
	if !errors.As(err, &se) || se.code != http.StatusConflict {
		t.Errorf("CmdCreateHook() repeat = %v, want a 409", err)
	}
}

// A cloned repo's .kiro/hooks may be a link out of the workspace, for instance to
// the global hooks dir; writing through it would turn a workspace hook global.
func TestCmdCreateHook_RefusesAHooksDirThatLeavesTheWorkspace(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".kiro"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".kiro", "hooks")); err != nil {
		t.Fatal(err)
	}

	if _, err := CmdCreateHook(t.Context(), Workspace{Dir: ws}, hookCmd(t, createHookPayload())); err == nil {
		t.Error("CmdCreateHook() through a hooks link out of the workspace = nil, want an error")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("CmdCreateHook() wrote %d entries outside the workspace", len(entries))
	}
}
