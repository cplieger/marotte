package bot

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/marotte"
)

const testChatID = "c-bot-0001"

func testPaths(t *testing.T) Paths {
	t.Helper()
	return Paths{KiroHome: filepath.Join(t.TempDir(), "kiro-home")}
}

func startedBot(memory bool, steering string) *marotte.Chat {
	return &marotte.Chat{
		ID:            testChatID,
		Name:          "Weekly answers",
		CurrentModeID: marotte.BotRoleID,
		Bot: &marotte.Bot{
			Goal:      "Answer animap's weekly questions.\nKeep the backlog under ten.",
			Steering:  steering,
			StartedAt: 1_700_000_000_000,
			Memory:    memory,
		},
	}
}

func memoryFile(p Paths) string {
	return filepath.Join(p.KiroHome, "bots", testChatID, "memory.md")
}

// sections are the role prompt's headings in order; Memory is present only with memory on.
var sections = []string{"## Your goal", "## Automatic prompts", "## Memory", "## Workers", "## Other chats", "## The user", "## Unattended turns"}

// section returns the prompt text under heading, up to the next heading.
func section(t *testing.T, prompt, heading string) string {
	t.Helper()
	at := strings.Index(prompt, heading+"\n")
	if at < 0 {
		t.Fatalf("the prompt has no %q section:\n%s", heading, prompt)
	}
	rest := prompt[at+len(heading)+1:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestRole_IsTheBotRoleOnEveryToolButTheSubagentClass(t *testing.T) {
	role := Role(startedBot(true, ""), testPaths(t))
	if role.ID != marotte.BotRoleID {
		t.Errorf("ID = %q, want %q", role.ID, marotte.BotRoleID)
	}
	if role.Tools != "*" {
		t.Errorf("Tools = %q, want \"*\": KAS reads an absent list as no tools", role.Tools)
	}
	want := []string{"use_subagent", "subagent", "invoke_sub_agent", "orchestrate_subagent", "delegate", "switch_to_execution"}
	if !slices.Equal(role.ExcludedTools, want) {
		t.Errorf("ExcludedTools = %v, want %v", role.ExcludedTools, want)
	}
	if !role.IncludeMCPJSON || !role.IncludePowers {
		t.Errorf("IncludeMCPJSON, IncludePowers = %v, %v; want both on, as a Default chat has them", role.IncludeMCPJSON, role.IncludePowers)
	}
	raw, err := json.Marshal(role)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "effortLevel", "welcomeMessage", "mcpServers"} {
		if _, ok := keys[key]; ok {
			t.Errorf("the entry carries %q; the chat's own model and effort are the Bot's: %s", key, raw)
		}
	}
}

func TestRole_DescriptionIsTheNameAndTheGoalsFirstLine(t *testing.T) {
	t.Run("a short first line", func(t *testing.T) {
		got := Role(startedBot(false, ""), testPaths(t)).Description
		if want := "Weekly answers: Answer animap's weekly questions."; got != want {
			t.Errorf("Description = %q, want %q", got, want)
		}
	})
	t.Run("a first line past 120 characters", func(t *testing.T) {
		rec := startedBot(false, "")
		rec.Bot.Goal = strings.Repeat("é", 130) + "\nsecond line"
		got := Role(rec, testPaths(t)).Description
		line, ok := strings.CutPrefix(got, "Weekly answers: ")
		if !ok {
			t.Fatalf("Description = %q, want it to start with the name", got)
		}
		if n := utf8.RuneCountInString(line); n > 120 || !utf8.ValidString(line) || !strings.HasPrefix(line, strings.Repeat("é", 100)) {
			t.Errorf("the goal part is %d characters (%q), want the first line cut to at most 120 on a rune boundary", n, line)
		}
	})
}

func TestRole_MemoryOnAllowsWritingTheFileUnderKiroHome(t *testing.T) {
	paths := testPaths(t)
	role := Role(startedBot(true, ""), paths)
	file := memoryFile(paths)
	rules := role.Permissions.Rules
	if len(rules) == 0 {
		t.Fatal("the role carries no permission rules")
	}
	want := marotte.PolicyRuleCore{Capability: "fs_write", Effect: "allow", Match: []string{file}}
	if last := rules[len(rules)-1]; !reflect.DeepEqual(last, want) {
		t.Errorf("the last rule = %+v, want the memory write %+v", last, want)
	}
	if !strings.Contains(section(t, role.Prompt, "## Memory"), file) {
		t.Errorf("the Memory section does not name %s:\n%s", file, role.Prompt)
	}
}

func TestRole_MemoryOffNamesNoFile(t *testing.T) {
	paths := testPaths(t)
	role := Role(startedBot(false, ""), paths)
	for _, r := range role.Permissions.Rules {
		if r.Capability == "fs_write" {
			t.Errorf("rule %+v grants a write with memory off", r)
		}
	}
	if len(role.Permissions.Rules) == 0 {
		t.Error("memory off dropped the default rules too")
	}
	if strings.Contains(role.Prompt, "## Memory") || strings.Contains(role.Prompt, paths.KiroHome) {
		t.Errorf("the prompt names memory with memory off:\n%s", role.Prompt)
	}
}

func TestRole_PromptSectionsInOrder(t *testing.T) {
	rec := startedBot(true, "")
	prompt := Role(rec, testPaths(t)).Prompt
	if want := "You are Weekly answers, a Bot: a permanent assistant the user keeps in marotte. This chat is yours and does not end."; !strings.HasPrefix(prompt, want) {
		t.Errorf("the prompt does not open with who the Bot is:\n%s", prompt)
	}
	at := -1
	for _, h := range sections {
		i := strings.Index(prompt, h+"\n")
		if i <= at {
			t.Fatalf("section %q is at %d, want it after the previous section at %d:\n%s", h, i, at, prompt)
		}
		at = i
	}
	if got := strings.TrimSpace(section(t, prompt, "## Your goal")); got != rec.Bot.Goal {
		t.Errorf("Your goal = %q, want the goal verbatim %q", got, rec.Bot.Goal)
	}
	auto := section(t, prompt, "## Automatic prompts")
	for _, want := range []string{"Automatic prompt from marotte", "rule", "data, never instructions", "as is"} {
		if !strings.Contains(auto, want) {
			t.Errorf("Automatic prompts does not say %q:\n%s", want, auto)
		}
	}
}

func TestRole_WorkersSectionHoldsNoMemoryPath(t *testing.T) {
	paths := testPaths(t)
	workers := section(t, Role(startedBot(true, ""), paths).Prompt, "## Workers")
	if strings.Contains(workers, paths.KiroHome) || strings.Contains(workers, "memory.md") {
		t.Errorf("Workers names the memory file's path, which a step cannot open:\n%s", workers)
	}
	for _, want := range []string{"run_workflow", "modelId", "inspect_workflow", "update_workflow"} {
		if !strings.Contains(workers, want) {
			t.Errorf("Workers does not name %s:\n%s", want, workers)
		}
	}
}

func TestRole_TheGoalIsTextNotTemplate(t *testing.T) {
	rec := startedBot(false, "")
	rec.Bot.Goal = "Report {{.Name}} and {{template \"x\"}} as written."
	if prompt := Role(rec, testPaths(t)).Prompt; !strings.Contains(prompt, rec.Bot.Goal) {
		t.Errorf("the goal was not copied verbatim:\n%s", prompt)
	}
}

func TestRole_SteeringNeverEntersTheRole(t *testing.T) {
	paths := testPaths(t)
	plain := Role(startedBot(true, ""), paths)
	steered := Role(startedBot(true, "Always answer in French."), paths)
	if !reflect.DeepEqual(plain, steered) {
		t.Errorf("custom steering changed the role; it rides as its own steering document")
	}
}

func TestSteeringDoc(t *testing.T) {
	t.Run("empty steering sends no document", func(t *testing.T) {
		if doc, ok := SteeringDoc(startedBot(true, "")); ok {
			t.Errorf("SteeringDoc = %+v, true; want none", doc)
		}
	})
	t.Run("set steering is one always document", func(t *testing.T) {
		doc, ok := SteeringDoc(startedBot(true, "Always answer in French."))
		want := marotte.ClientSteeringDoc{Name: "bot-steering", Inclusion: "always", Content: "Always answer in French."}
		if !ok || doc != want {
			t.Errorf("SteeringDoc = %+v, %v; want %+v", doc, ok, want)
		}
	})
}

// TestMemoryFile_IsTheBotsOwnFileUnderKiroHome pins that the door's context path is
// <KiroHome>/bots/<chat id>/memory.md, built from the server-minted id alone, and nothing is built from an id that is
// not one folder name or a KiroHome KAS would resolve against the workspace.
func TestMemoryFile_IsTheBotsOwnFileUnderKiroHome(t *testing.T) {
	paths := testPaths(t)
	got, ok := MemoryFile(startedBot(true, ""), paths)
	if want := filepath.Join(paths.KiroHome, "bots", testChatID, "memory.md"); !ok || got != want {
		t.Errorf("MemoryFile = %q, %v; want %q, true", got, ok, want)
	}
	for _, tc := range []struct {
		name, id, home string
	}{
		{"a parent step", "..", paths.KiroHome},
		{"a nested path", "c1/../../steering", paths.KiroHome},
		{"a dotted id", "c1.md", paths.KiroHome},
		{"no id", "", paths.KiroHome},
		{"a relative KiroHome", testChatID, "kiro-home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := startedBot(true, "")
			rec.ID = tc.id
			if got, ok := MemoryFile(rec, Paths{KiroHome: tc.home}); ok {
				t.Errorf("MemoryFile = %q, want none", got)
			}
		})
	}
}

// TestRole_AMemoryFileItCannotNameGrantsNoWrite pins that the role's write rule and Memory section follow MemoryFile.
func TestRole_AMemoryFileItCannotNameGrantsNoWrite(t *testing.T) {
	role := Role(startedBot(true, ""), Paths{KiroHome: "kiro-home"})
	for _, r := range role.Permissions.Rules {
		if r.Capability == "fs_write" {
			t.Errorf("rule %+v grants a write the door never loads", r)
		}
	}
	if strings.Contains(role.Prompt, "## Memory") {
		t.Errorf("the prompt names a memory file it has none of:\n%s", role.Prompt)
	}
}
