package translate

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func slashFrame(t *testing.T, cmds ...map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": cmds})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func kasCmd(name, typ string, kiro map[string]any) map[string]any {
	meta := map[string]any{"type": typ}
	maps.Copy(meta, kiro)
	return map[string]any{"name": name, "description": name + " desc", "_meta": map[string]any{"kiro": meta}}
}

func slashNames(cmds []marotte.SlashCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name + ":" + string(c.Kind)
	}
	return out
}

func TestReadSlashCatalog_KeepsOnlyServerResolvedKinds(t *testing.T) {
	raw := slashFrame(t,
		kasCmd("style", "steering", nil),
		kasCmd("review", "prompt", nil),
		kasCmd("some-skill", "skill", nil),
		kasCmd("planner", "custom-agent", nil),
		kasCmd("workflow-run", "workflow", map[string]any{"commandId": "run"}),
		kasCmd("goal", "workflow", map[string]any{"commandId": "goal"}),
	)
	cmds, ok := ReadSlashCatalog(raw)
	got := strings.Join(slashNames(cmds), ",")
	if !ok || got != "style:steering,review:prompt,goal:goal" {
		t.Errorf("ReadSlashCatalog = %q (ok=%v), want style:steering,review:prompt,goal:goal", got, ok)
	}
}

// KAS declines to expand a prompt that shares a name with any other command,
// including one the menu itself drops, so the check reads the whole frame.
func TestReadSlashCatalog_DropsPromptShadowedByNonPrompt(t *testing.T) {
	raw := slashFrame(t,
		kasCmd("context-gatherer", "prompt", nil),
		kasCmd("context-gatherer", "custom-agent", nil),
		kasCmd("review", "prompt", map[string]any{"arguments": []map[string]any{{"name": "file", "required": true}}}),
	)
	cmds, _ := ReadSlashCatalog(raw)
	if got := strings.Join(slashNames(cmds), ","); got != "review:prompt" {
		t.Fatalf("ReadSlashCatalog = %q, want review:prompt", got)
	}
	if args := cmds[0].Arguments; len(args) != 1 || args[0].Name != "file" || !args[0].Required {
		t.Errorf("review arguments = %+v, want one required file", args)
	}
}

func TestReadSlashCatalog_SanitisesNameAndBoundsFields(t *testing.T) {
	cmd := kasCmd("evil\u202ename", "prompt", nil)
	cmd["description"] = strings.Repeat("d", 2048)
	cmds, _ := ReadSlashCatalog(slashFrame(t, cmd))
	if len(cmds) != 1 {
		t.Fatalf("ReadSlashCatalog = %+v, want one entry", cmds)
	}
	if strings.ContainsRune(cmds[0].Name, '\u202e') {
		t.Errorf("name = %q, want the bidi override stripped", cmds[0].Name)
	}
	if len(cmds[0].Description) > maxDisplayTextBytes+len("...") {
		t.Errorf("description is %d bytes, want at most %d", len(cmds[0].Description), maxDisplayTextBytes+3)
	}
}

// An empty frame says nothing about the catalog; a frame whose entries are all
// dropped says the catalog holds none, which is what lets the client stop holding.
func TestReadSlashCatalog_EmptyFrameIsNotACatalog(t *testing.T) {
	if _, ok := ReadSlashCatalog(slashFrame(t)); ok {
		t.Error("ReadSlashCatalog(no availableCommands) ok = true, want false")
	}
	cmds, ok := ReadSlashCatalog(slashFrame(t, kasCmd("some-skill", "skill", nil)))
	if !ok || cmds == nil || len(cmds) != 0 {
		t.Errorf("ReadSlashCatalog(skills only) = (%+v, %v), want an empty non-nil list and ok", cmds, ok)
	}
}

type slashCatalogDouble struct{ chat int }

func (s *slashCatalogDouble) SetFromChat([]marotte.SlashCommand) bool { s.chat++; return true }

type countingBus struct{ events []marotte.EventType }

func (b *countingBus) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.events = append(b.events, evt.Type)
}

// Only a chat's own frame feeds the workspace catalog.
func TestAvailableCommandsUpdate_StepAndSubagentFramesAreIgnored(t *testing.T) {
	slash := &slashCatalogDouble{}
	bus := &countingBus{}
	tr := New(&Roles{Bus: bus, Slash: slash})
	raw := slashFrame(t, kasCmd("review", "prompt", nil))

	tr.HandleAvailableCommandsUpdate(t.Context(), "c1", raw, FrameAttribution{Step: true})
	tr.HandleAvailableCommandsUpdate(t.Context(), "c1", raw, FrameAttribution{Subagent: true, SessionID: "sub"})
	if slash.chat != 0 || len(bus.events) != 0 {
		t.Fatalf("after step and subagent frames: SetFromChat calls = %d, events = %v, want none", slash.chat, bus.events)
	}
	tr.HandleAvailableCommandsUpdate(t.Context(), "c1", raw, FrameAttribution{})
	if slash.chat != 1 || len(bus.events) != 1 || bus.events[0] != marotte.EventSlashCommandsChanged {
		t.Errorf("after a chat frame: SetFromChat calls = %d, events = %v, want 1 and one slash_commands_changed", slash.chat, bus.events)
	}
}

func TestReadSteeringIssues_FailedFrameIsNotAnUpdate(t *testing.T) {
	if _, ok := readSteeringIssues(json.RawMessage(`{"status":"failed","error":"x"}`)); ok {
		t.Error("ReadSteeringIssues(failed) ok = true, want false so the map is kept")
	}
	issues, ok := readSteeringIssues(json.RawMessage(`{"status":"success","documents":[
		{"uri":"file:///workspace/.kiro/steering/a%20b.md","_meta":{"kiro":{"configIssues":[{"code":"fileMatchPatternLiteralComma","patterns":["a,b"],"remediation":"Split it."}]}}},
		{"uri":"file:///workspace/.kiro/steering/a%20c.md","_meta":{"kiro":{}}}]}`))
	if !ok || len(issues) != 1 || len(issues["workspace/.kiro/steering/a b.md"]) != 1 {
		t.Errorf("ReadSteeringIssues = %+v (ok=%v), want one issue under the decoded path", issues, ok)
	}
}
