package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// kasLine is one corpus line as the capture wrote it: "in" from KAS, "out" from marotte, and "report", the session
// door's decoded answer (KAS's door results stop at the door and never reach the lane).
type kasLine struct {
	Dir   string `json:"dir"`
	Frame struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	} `json:"frame"`
	raw json.RawMessage
}

func loadKASLines(t *testing.T, name string) []kasLine {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "kas", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 4<<20)
	sc.Scan()
	var out []kasLine
	for sc.Scan() {
		var l kasLine
		var whole struct {
			Frame json.RawMessage `json:"frame"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil || json.Unmarshal(sc.Bytes(), &whole) != nil {
			t.Fatalf("%s: %v", name, err)
		}
		l.raw = whole.Frame
		out = append(out, l)
	}
	return out
}

type doorReport struct {
	Method         string                `json:"method"`
	CurrentModeID  string                `json:"currentModeId"`
	AvailableModes []marotte.SessionMode `json:"availableModes"`
}

func (l *kasLine) report(t *testing.T) doorReport {
	t.Helper()
	var r doorReport
	if err := json.Unmarshal(l.raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (l *kasLine) kiroMeta(t *testing.T) map[string]any {
	t.Helper()
	var p struct {
		Meta struct {
			Kiro map[string]any `json:"kiro"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(l.Frame.Params, &p); err != nil {
		t.Fatal(err)
	}
	return p.Meta.Kiro
}

func listsMode(modes []marotte.SessionMode, id string) bool {
	return slices.ContainsFunc(modes, func(m marotte.SessionMode) bool { return m.ID == id })
}

// TestKASCorpus_BotRoleHoldsOnEveryDoor pins KAS's recorded answer: every door that sends the role reports the role,
// with no session/set_mode needed; a regular chat's modes never list it; and a session/set_mode to a role the session
// does not hold is answered {} after a not_found for it, which is the refusal the door reads.
func TestKASCorpus_BotRoleHoldsOnEveryDoor(t *testing.T) {
	lines := loadKASLines(t, "bot_role")
	var withRole, withoutRole, sets int
	var door *kasLine
	for i := range lines {
		l := &lines[i]
		switch {
		case l.Dir == "out" && (l.Frame.Method == marotte.MethodSessionNew || l.Frame.Method == marotte.MethodSessionLoad):
			door = l
		case l.Dir == "report":
			r := l.report(t)
			_, sent := door.kiroMeta(t)["customAgents"]
			switch {
			case sent && (r.CurrentModeID != marotte.BotRoleID || !listsMode(r.AvailableModes, marotte.BotRoleID)):
				t.Errorf("%s with the role reported %q among %+v", r.Method, r.CurrentModeID, r.AvailableModes)
			case sent:
				withRole++
			case r.CurrentModeID == marotte.BotRoleID || listsMode(r.AvailableModes, marotte.BotRoleID):
				t.Errorf("%s with no role reported %q among %+v", r.Method, r.CurrentModeID, r.AvailableModes)
			default:
				withoutRole++
			}
		case l.Dir == "out" && l.Frame.Method == marotte.MethodSetMode:
			sets++
			if withoutRole != 2 {
				t.Errorf("session/set_mode sent before the reopen with no role")
			}
		}
	}
	if withRole != 4 || withoutRole != 2 || sets != 1 {
		t.Errorf("%d doors with the role, %d without, %d set_mode; want 4 (new, reopen, edited, memory off), 2 (the regular chat, the reopen with no role) and 1", withRole, withoutRole, sets)
	}

	set := slices.IndexFunc(lines, func(l kasLine) bool { return l.Dir == "out" && l.Frame.Method == marotte.MethodSetMode })
	if set < 0 {
		t.Fatal("the capture holds no session/set_mode")
	}
	reply := slices.IndexFunc(lines[set:], func(l kasLine) bool {
		return l.Dir == "in" && l.Frame.ID != nil && l.Frame.Method == "" && *l.Frame.ID == *lines[set].Frame.ID
	})
	if reply < 0 {
		t.Fatal("session/set_mode has no reply")
	}
	reply += set
	if string(lines[reply].Frame.Result) != "{}" || lines[reply].Frame.Error != nil {
		t.Errorf("session/set_mode to an absent role answered result %s error %s, want {}", lines[reply].Frame.Result, lines[reply].Frame.Error)
	}
	notFound := slices.IndexFunc(lines[set:reply], func(l kasLine) bool {
		return l.Dir == "in" && l.Frame.Method == "_kiro/customAgent/not_found"
	})
	if notFound < 0 {
		t.Fatal("no _kiro/customAgent/not_found between session/set_mode and its reply")
	}
	var p struct {
		Requested string `json:"requestedAgent"`
		Fallback  string `json:"fallbackAgent"`
	}
	if err := json.Unmarshal(lines[set+notFound].Frame.Params, &p); err != nil || p.Requested != marotte.BotRoleID || p.Fallback != marotte.DefaultModeID {
		t.Errorf("not_found params = %+v (%v), want the role falling back to %s", p, err, marotte.DefaultModeID)
	}
}

// TestKASCorpus_BotAsksWhatADefaultChatAsks pins KAS's recorded answer: under every Permission profile, each probe
// asks a Bot exactly as often as a Default chat, the Bot's memory write never asks, and under Trusted with sub-agents
// on the Default chat names a sub-agent tool while the Bot names none of the tools its role excludes.
func TestKASCorpus_BotAsksWhatADefaultChatAsks(t *testing.T) {
	lines := loadKASLines(t, "bot_policy")
	type probe struct{ presets, capability, target string }
	mode := map[string]string{}
	presets := map[string]string{}
	asks := map[probe]map[string]int{}
	pending := map[int64]probe{}
	var current probe
	var currentSide string
	var memoryWrites int
	prose := map[string]*strings.Builder{}
	for _, l := range lines {
		switch {
		case l.Dir == "out" && l.Frame.Method == marotte.MethodSessionNew:
			k := l.kiroMeta(t)
			id, _ := k["overrideSessionId"].(string)
			m, _ := k["modeId"].(string)
			mode[id] = m
			pp, _ := json.Marshal(k["policyPreset"])
			presets[id] = string(pp)
		case l.Dir == "out" && l.Frame.Method == "_kiro/policy/check":
			var p struct {
				SessionID  string   `json:"sessionId"`
				Capability string   `json:"capability"`
				Command    string   `json:"command"`
				Paths      []string `json:"paths"`
			}
			if err := json.Unmarshal(l.Frame.Params, &p); err != nil {
				t.Fatal(err)
			}
			current = probe{presets[p.SessionID], p.Capability, p.Command + strings.Join(p.Paths, ",")}
			currentSide = mode[p.SessionID]
			if strings.HasSuffix(current.target, "/memory.md") {
				currentSide = "memory"
			}
			if asks[current] == nil {
				asks[current] = map[string]int{}
			}
			asks[current][currentSide] += 0
			pending[*l.Frame.ID] = current
		case l.Dir == "in" && l.Frame.Method == marotte.MethodRequestPermission:
			if currentSide == "memory" {
				memoryWrites++
			}
			asks[current][currentSide]++
		case l.Dir == "in" && l.Frame.Method == marotte.MethodSessionUpdate:
			var p struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					SessionUpdate string `json:"sessionUpdate"`
					Content       struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if json.Unmarshal(l.Frame.Params, &p) == nil && p.Update.SessionUpdate == "agent_message_chunk" {
				m := mode[p.SessionID]
				if prose[m] == nil {
					prose[m] = &strings.Builder{}
				}
				prose[m].WriteString(p.Update.Content.Text)
			}
		}
	}
	var compared int
	for pr, sides := range asks {
		if _, mem := sides["memory"]; mem {
			continue
		}
		if len(sides) != 2 || sides[""] != sides[marotte.BotRoleID] {
			t.Errorf("%+v asked %v, want the Default chat (\"\") and the Bot alike", pr, sides)
		}
		compared++
	}
	if compared != 6*10 {
		t.Errorf("%d probes compared, want 10 under each of 6 profiles", compared)
	}
	if memoryWrites != 0 {
		t.Errorf("the Bot's memory write asked %d times", memoryWrites)
	}
	if prose[""] == nil || !strings.Contains(prose[""].String(), "invoke_sub_agent") {
		t.Errorf("the Default chat's tool list names no invoke_sub_agent: %v", prose[""])
	}
	if prose[marotte.BotRoleID] == nil {
		t.Fatal("the Bot listed no tools")
	}
	for tool := range strings.FieldsSeq(prose[marotte.BotRoleID].String()) {
		if slices.Contains([]string{"use_subagent", "subagent", "invoke_sub_agent", "orchestrate_subagent", "delegate", "switch_to_execution"}, tool) {
			t.Errorf("the Bot can call %s", tool)
		}
	}
}

// TestKASCorpus_BotMemoryIsAContextFileOfItsOwnSession pins KAS's recorded answer: KAS takes a forced add of a file
// not written yet, each turn sees the file as it stands (none, then each edit, then the recreated file), a deleted
// file stays listed unmatched, and neither a regular session nor a fork of the Bot lists any context file.
func TestKASCorpus_BotMemoryIsAContextFileOfItsOwnSession(t *testing.T) {
	lines := loadKASLines(t, "bot_memory")
	var adds, shows []json.RawMessage
	var replies []string
	byID := map[int64]string{}
	var turn *strings.Builder
	var replay bool
	for _, l := range lines {
		switch {
		case l.Dir == "out" && l.Frame.Method == marotte.MethodSessionContext:
			var p marotte.SessionContextParams
			_ = json.Unmarshal(l.Frame.Params, &p)
			byID[*l.Frame.ID] = p.Subcommand
		case l.Dir == "in" && l.Frame.ID != nil && l.Frame.Method == "" && byID[*l.Frame.ID] == marotte.SessionContextAdd:
			adds = append(adds, l.Frame.Result)
		case l.Dir == "in" && l.Frame.ID != nil && l.Frame.Method == "" && byID[*l.Frame.ID] == "show":
			shows = append(shows, l.Frame.Result)
		case l.Dir == "out" && l.Frame.Method == marotte.MethodSessionLoad:
			replay = true
		case l.Dir == "out" && l.Frame.Method == marotte.MethodPrompt:
			turn = &strings.Builder{}
			replies = append(replies, "")
			replay = false
		case l.Dir == "in" && l.Frame.Method == marotte.MethodSessionUpdate && turn != nil && !replay:
			var p struct {
				Update struct {
					SessionUpdate string `json:"sessionUpdate"`
					Content       struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if json.Unmarshal(l.Frame.Params, &p) == nil && p.Update.SessionUpdate == "agent_message_chunk" {
				turn.WriteString(p.Update.Content.Text)
				replies[len(replies)-1] = turn.String()
			}
		}
	}
	if len(adds) != 1 {
		t.Fatalf("%d context adds, want 1", len(adds))
	}
	var added struct {
		Success bool `json:"success"`
		Entry   struct {
			Matched bool `json:"matched"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(adds[0], &added); err != nil || !added.Success || added.Entry.Matched {
		t.Errorf("the forced add of a missing file answered %s, want success with the entry unmatched", adds[0])
	}
	want := []string{"none", "slate-4410", "cobalt-6602", "amber-3301"}
	if len(replies) != len(want)+1 {
		t.Fatalf("%d turns, want %d on the Bot and one on the regular session", len(replies), len(want)+1)
	}
	for i, w := range want {
		if !strings.Contains(strings.ToLower(replies[i]), w) {
			t.Errorf("turn %d answered %q, want %q", i, replies[i], w)
		}
	}
	if strings.Contains(replies[len(want)], "amber-3301") {
		t.Errorf("the regular session answered %q, the Bot's memory", replies[len(want)])
	}
	type entries struct {
		Entries []struct {
			Path    string `json:"path"`
			Matched bool   `json:"matched"`
		} `json:"entries"`
	}
	if len(shows) != 3 {
		t.Fatalf("%d context shows, want the Bot's, the regular session's and the fork's", len(shows))
	}
	var deleted, regular, fork entries
	for i, e := range []*entries{&deleted, &regular, &fork} {
		if err := json.Unmarshal(shows[i], e); err != nil {
			t.Fatal(err)
		}
	}
	if len(deleted.Entries) != 1 || deleted.Entries[0].Matched || !strings.HasSuffix(deleted.Entries[0].Path, "/memory.md") {
		t.Errorf("after the file was deleted the Bot lists %+v, want the file unmatched", deleted.Entries)
	}
	if len(regular.Entries) != 0 || len(fork.Entries) != 0 {
		t.Errorf("the regular session lists %+v and the fork %+v, want none", regular.Entries, fork.Entries)
	}
}
