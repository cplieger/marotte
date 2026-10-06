package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// MaxHookField caps the per-field size for CmdCreateHook payloads.
const MaxHookField = 8 * 1024

// validHookNameRe restricts hook filenames to a strict single-segment
// allowlist: lowercase ASCII, digits, underscore, hyphen, 1-64 chars,
// must start with an alphanumeric.
var validHookNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// The two action_type values create_hook accepts.
const (
	hookActionAskAgent   = "askAgent"
	hookActionRunCommand = "runCommand"
)

// hookCreatePayload is the decoded shape for CmdCreateHook.
type hookCreatePayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	EventType   string `json:"event_type"`
	ActionType  string `json:"action_type"` // "askAgent" or "runCommand"
	Prompt      string `json:"prompt,omitempty"`
	Command     string `json:"command,omitempty"`
	Patterns    string `json:"patterns,omitempty"`
	Timeout     int    `json:"timeout,omitempty"` // optional per-hook timeout (seconds)
}

// hookFieldsExceedLimit reports whether any CmdCreateHook string field
// exceeds the per-field MaxHookField cap.
func hookFieldsExceedLimit(p *hookCreatePayload) bool {
	return len(p.Name) > MaxHookField || len(p.Description) > MaxHookField ||
		len(p.EventType) > MaxHookField || len(p.ActionType) > MaxHookField ||
		len(p.Prompt) > MaxHookField || len(p.Command) > MaxHookField ||
		len(p.Patterns) > MaxHookField
}

// validateHookPayload decodes + validates a CmdCreateHook payload.
func validateHookPayload(cmd *marotte.ClientCommand) (p hookCreatePayload, safeName string, code int, err error) {
	if uErr := json.Unmarshal(cmd.Payload, &p); uErr != nil || p.Name == "" || p.EventType == "" {
		return p, "", http.StatusBadRequest, ErrInvalidPayload
	}
	if hookFieldsExceedLimit(&p) {
		return p, "", http.StatusRequestEntityTooLarge,
			errors.New("hook field too large")
	}
	switch p.ActionType {
	case hookActionAskAgent:
		if strings.TrimSpace(p.Prompt) == "" {
			return p, "", http.StatusBadRequest,
				errors.New("askAgent hook requires a non-empty prompt")
		}
	case hookActionRunCommand:
		if strings.TrimSpace(p.Command) == "" {
			return p, "", http.StatusBadRequest,
				errors.New("runCommand hook requires a non-empty command")
		}
	default:
		return p, "", http.StatusBadRequest,
			errors.New("action_type must be askAgent or runCommand")
	}
	trigger, known := marotte.NormalizeHookTrigger(p.EventType)
	if !known {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("event_type %q is not a trigger kiro-cli loads. Expected one of: %s",
				p.EventType, marotte.KnownHookTriggers())
	}
	if trigger.CommandOnly && p.ActionType == hookActionAskAgent {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("trigger %s runs only command hooks, so an askAgent hook would load and never fire; use action_type runCommand",
				trigger.Name)
	}
	// A matcher on a trigger with nothing to match is a typo KAS only logs, so the hook would
	// silently govern nothing. A tool hook with no matcher is legitimate (every call) and is not
	// refused.
	if marotte.ClassifyHookMatcher(trigger.Name, p.Patterns) == marotte.HookMatcherIneffective {
		return p, "", http.StatusBadRequest,
			fmt.Errorf("trigger %s has nothing to match against, so its matcher %q would be ignored. Leave patterns empty for this trigger",
				trigger.Name, strings.TrimSpace(p.Patterns))
	}
	safeName = strings.ReplaceAll(strings.ToLower(p.Name), " ", "-")
	if !validHookNameRe.MatchString(safeName) {
		return p, "", http.StatusBadRequest,
			errors.New("hook name must be 1-64 chars, start with a letter or digit, and contain only [a-z0-9_-]")
	}
	return p, safeName, 0, nil
}

// The standalone .kiro/hooks/<name>.json shape:
//
//	{ "version": "v1", "hooks": [ { name, trigger, matcher?, action, timeout? } ] }
//
// trigger is PascalCase (marotte.NormalizeHookTrigger); action.type is "command" or "agent".
// "version" is the literal "v1" KAS's schema requires, unrelated to the v3 agent engine or the v2
// hook engine: "v2" makes every hook unloadable.
type hookAction struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

type hookEntry struct {
	Name        string     `json:"name"`
	Trigger     string     `json:"trigger"`
	Description string     `json:"description,omitempty"`
	Matcher     string     `json:"matcher,omitempty"`
	Action      hookAction `json:"action"`
	Timeout     int        `json:"timeout,omitempty"`
}

type hookDoc struct {
	Version string      `json:"version"`
	Hooks   []hookEntry `json:"hooks"`
}

// mustTrigger resolves an event type validateHookPayload has already
// accepted.
func mustTrigger(eventType string) string {
	t, _ := marotte.NormalizeHookTrigger(eventType)
	return t.Name
}

// buildHookAction maps marotte's action_type payload ("askAgent" /
// "runCommand") to the v1 action shape. validateHookPayload guarantees
// one of the two values, so the default arm is defensive only.
func buildHookAction(p *hookCreatePayload) hookAction {
	switch p.ActionType {
	case hookActionRunCommand:
		return hookAction{Type: "command", Command: p.Command}
	case hookActionAskAgent:
		return hookAction{Type: "agent", Prompt: p.Prompt}
	default:
		return hookAction{Type: p.ActionType}
	}
}

// buildHookDoc renders the v1 hook document written to
// .kiro/hooks/<name>.json.
func buildHookDoc(p *hookCreatePayload) hookDoc {
	entry := hookEntry{
		Name:        p.Name,
		Trigger:     mustTrigger(p.EventType),
		Description: p.Description,
		Matcher:     strings.TrimSpace(p.Patterns),
		Action:      buildHookAction(p),
	}
	if p.Timeout > 0 {
		entry.Timeout = p.Timeout
	}
	return hookDoc{Version: "v1", Hooks: []hookEntry{entry}}
}

// CmdCreateHook creates a hook file from chat context.
func CmdCreateHook(ctx context.Context, ws Workspace, cmd *marotte.ClientCommand) (any, error) {
	p, safeName, code, vErr := validateHookPayload(cmd)
	if vErr != nil {
		return nil, StatusError(code, vErr)
	}
	hook := buildHookDoc(&p)

	// Through the workspace root, so a .kiro or hooks link leading out of the
	// workspace is refused rather than written through: a cloned repo's link
	// to ~/.kiro/hooks would silently make this hook global.
	root, err := os.OpenRoot(ws.Dir)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	defer root.Close()
	relPath := filepath.Join(".kiro", "hooks", safeName+".json")
	if _, lstatErr := root.Lstat(relPath); lstatErr == nil {
		return nil, StatusError(http.StatusConflict,
			errors.New("a hook with this name already exists"))
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		return nil, StatusError(http.StatusInternalServerError, lstatErr)
	}
	data, err := json.MarshalIndent(hook, "", "  ")
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if _, err := atomicfile.WriteFileInRoot(ctx, root, relPath, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("hook created from chat", keyName, p.Name, "path", relPath)
	return responseWith(map[string]any{"path": relPath}), nil
}
