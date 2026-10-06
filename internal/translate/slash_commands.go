package translate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// SlashCatalog holds the workspace's slash-menu entries. A chat's frame wins
// over the utility session's, because the utility session runs no MCP servers
// and so lists no MCP prompts. Each setter reports whether the list changed.
type SlashCatalog interface {
	SetFromChat(cmds []marotte.SlashCommand) bool
	SetFromUtility(cmds []marotte.SlashCommand) bool
}

// SteeringIssues holds KAS's per-document steering configuration issues. Set
// replaces the whole map and reports whether it changed.
type SteeringIssues interface {
	Set(issues map[string][]marotte.SteeringIssue) bool
}

// kasAvailableCommand is one availableCommands[] entry. KAS has already
// normalised name to the spelling its resolver matches.
type kasAvailableCommand struct {
	Input *struct {
		Hint string `json:"hint"`
	} `json:"input"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Meta        struct {
		Kiro struct {
			Type      string `json:"type"`
			CommandID string `json:"commandId"`
			Arguments []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Required    bool   `json:"required"`
			} `json:"arguments"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// ReadSlashCatalog decodes an available_commands_update into the menu's entries (steering
// docs, saved prompts, /goal). A prompt named like any other entry is dropped (KAS will not
// expand it). ok is false for an undecodable or empty frame; all-dropped is a catalog of none.
func ReadSlashCatalog(update json.RawMessage) (cmds []marotte.SlashCommand, ok bool) {
	var frame struct {
		AvailableCommands []kasAvailableCommand `json:"availableCommands"`
	}
	if json.Unmarshal(update, &frame) != nil || len(frame.AvailableCommands) == 0 {
		return nil, false
	}
	nonPrompt := make(map[string]bool, len(frame.AvailableCommands))
	for i := range frame.AvailableCommands {
		if c := &frame.AvailableCommands[i]; c.Meta.Kiro.Type != "prompt" {
			nonPrompt[c.Name] = true
		}
	}
	cmds = []marotte.SlashCommand{}
	for i := range frame.AvailableCommands {
		c := &frame.AvailableCommands[i]
		kind, keep := slashKind(c)
		if !keep || (kind == marotte.SlashKindPrompt && nonPrompt[c.Name]) {
			continue
		}
		if cmd, ok := slashCommandOf(c, kind); ok {
			cmds = append(cmds, cmd)
		}
	}
	return cmds, true
}

// slashCommandOf sanitizes one kept entry; ok is false when its name is empty
// once sanitized.
func slashCommandOf(c *kasAvailableCommand, kind marotte.SlashCommandKind) (marotte.SlashCommand, bool) {
	name := displayText(c.Name)
	if name == "" {
		return marotte.SlashCommand{}, false
	}
	cmd := marotte.SlashCommand{Name: name, Description: displayText(c.Description), Kind: kind}
	if c.Input != nil {
		cmd.Hint = displayText(c.Input.Hint)
	}
	for _, a := range c.Meta.Kiro.Arguments {
		cmd.Arguments = append(cmd.Arguments, marotte.SlashArgument{
			Name: displayText(a.Name), Description: displayText(a.Description), Required: a.Required,
		})
	}
	return cmd, true
}

// slashKind maps KAS's entry type onto a menu kind; keep is false for a kind
// that reaches the model as prose (skill, custom agent, the other workflow verbs).
func slashKind(c *kasAvailableCommand) (marotte.SlashCommandKind, bool) {
	switch c.Meta.Kiro.Type {
	case "steering":
		return marotte.SlashKindSteering, true
	case "prompt":
		return marotte.SlashKindPrompt, true
	case "workflow":
		return marotte.SlashKindGoal, c.Meta.Kiro.CommandID == "goal"
	default:
		return "", false
	}
}

// HandleAvailableCommandsUpdate feeds a chat's catalog frame into the workspace
// slash catalog. A step's or a subagent's frame is not the workspace's.
func (t *Translator) HandleAvailableCommandsUpdate(ctx context.Context, _ marotte.ChatID, raw json.RawMessage, attr FrameAttribution) {
	if !attr.ChatOwned() || t.slash == nil {
		return
	}
	cmds, ok := ReadSlashCatalog(raw)
	if !ok {
		return
	}
	if t.slash.SetFromChat(cmds) {
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSlashCommandsChanged, "", marotte.SlashCommandsChangedPayload{}))
	}
}

// kasSteeringDocuments is _kiro/steering/documents_changed's params. A document
// carries its content too; it is not decoded.
type kasSteeringDocuments struct {
	Status    string `json:"status"`
	Documents []struct {
		URI  string `json:"uri"`
		Meta struct {
			Kiro struct {
				ConfigIssues []marotte.SteeringIssue `json:"configIssues"`
			} `json:"kiro"`
		} `json:"_meta"`
	} `json:"documents"`
}

// ReadSteeringIssues decodes a documents_changed frame into issues keyed by
// KiroDoc.Path. ok is false for a failed or undecodable frame, which carries no
// document list and must not clear the map.
func ReadSteeringIssues(params json.RawMessage) (issues map[string][]marotte.SteeringIssue, ok bool) {
	var p kasSteeringDocuments
	if json.Unmarshal(params, &p) != nil || p.Status != "success" {
		return nil, false
	}
	issues = map[string][]marotte.SteeringIssue{}
	for i := range p.Documents {
		d := &p.Documents[i]
		if len(d.Meta.Kiro.ConfigIssues) == 0 {
			continue
		}
		u, err := url.Parse(d.URI)
		if err != nil || u.Scheme != "file" || u.Path == "" {
			continue
		}
		key := strings.TrimPrefix(u.Path, "/")
		for j := range d.Meta.Kiro.ConfigIssues {
			issues[key] = append(issues[key], sanitizeSteeringIssue(&d.Meta.Kiro.ConfigIssues[j]))
		}
	}
	return issues, true
}

func sanitizeSteeringIssue(is *marotte.SteeringIssue) marotte.SteeringIssue {
	out := marotte.SteeringIssue{
		Code: displayText(is.Code), Reference: displayText(is.Reference),
		Reason: displayText(is.Reason), Remediation: displayText(is.Remediation),
	}
	for _, p := range is.Patterns {
		out.Patterns = append(out.Patterns, displayText(p))
	}
	return out
}

// HandleSteeringDocuments consumes _kiro/steering/documents_changed for the
// configuration issues KAS found in each steering document.
func (t *Translator) HandleSteeringDocuments(ctx context.Context, _ marotte.ChatID, msg *marotte.RPCResponse) {
	t.ApplySteeringDocuments(ctx, msg.Params)
}

// ApplySteeringDocuments is HandleSteeringDocuments's body, shared with the
// utility session's forward.
func (t *Translator) ApplySteeringDocuments(ctx context.Context, params json.RawMessage) {
	if t.steeringIssues == nil {
		return
	}
	issues, ok := ReadSteeringIssues(params)
	if !ok {
		slog.Debug("steering documents_changed: not a success frame")
		return
	}
	if t.steeringIssues.Set(issues) {
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSteeringIssuesChanged, "", marotte.SteeringIssuesChangedPayload{}))
	}
}
