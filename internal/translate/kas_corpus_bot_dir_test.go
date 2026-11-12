package translate

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// fileExchange is one file request KAS sent and whether the client's answer refused it.
type fileExchange struct {
	method, session, path, content string
	refused                        bool
}

// fileExchanges are the fs/ and _kiro/fs/ requests of a capture with their answers, in order. KAS numbers its own
// requests on each lane, so an answer is matched to the open request of the same id.
func fileExchanges(t *testing.T, lines []kasLine) []fileExchange {
	t.Helper()
	var out []fileExchange
	open := map[int64]int{}
	for _, l := range lines {
		isFile := strings.HasPrefix(l.Frame.Method, "fs/") || strings.HasPrefix(l.Frame.Method, "_kiro/fs/")
		switch {
		case l.Dir == "in" && l.Frame.ID != nil && isFile:
			var p struct {
				SessionID string `json:"sessionId"`
				Path      string `json:"path"`
				Content   string `json:"content"`
			}
			if err := json.Unmarshal(l.Frame.Params, &p); err != nil {
				t.Fatal(err)
			}
			open[*l.Frame.ID] = len(out)
			out = append(out, fileExchange{method: l.Frame.Method, session: p.SessionID, path: p.Path, content: p.Content})
		case l.Dir == "out" && l.Frame.ID != nil && l.Frame.Method == "":
			if i, ok := open[*l.Frame.ID]; ok {
				out[i].refused = l.Frame.Error != nil
				delete(open, *l.Frame.ID)
			}
		}
	}
	return out
}

// capturedDoor is one session/new or session/load the capture sent, with the report the door answered it with.
type capturedDoor struct {
	method      string
	directories []string
	report      doorReport
	session     string
}

// doorsOf are a capture's doors in order.
func doorsOf(t *testing.T, lines []kasLine) []capturedDoor {
	t.Helper()
	var out []capturedDoor
	var pending *capturedDoor
	for i := range lines {
		l := &lines[i]
		switch {
		case l.Dir == "out" && (l.Frame.Method == marotte.MethodSessionNew || l.Frame.Method == marotte.MethodSessionLoad):
			var p struct {
				AdditionalDirectories []string `json:"additionalDirectories"`
			}
			if err := json.Unmarshal(l.Frame.Params, &p); err != nil {
				t.Fatal(err)
			}
			pending = &capturedDoor{method: l.Frame.Method, directories: p.AdditionalDirectories}
		case l.Dir == "report" && pending != nil:
			pending.report = l.report(t)
			var r struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(l.raw, &r); err != nil {
				t.Fatal(err)
			}
			pending.session = r.SessionID
			out = append(out, *pending)
			pending = nil
		}
	}
	return out
}

// TestKASCorpus_ABotWritesItsMemoryThroughTheClient pins P4 (design §15) on the live answer: a Bot whose directory is
// among its session's additional directories has its memory write sent to the client as fs/write_text_file for the
// file's path; a load with the directory dropped and one with it back each find the session on the role; and a
// regular session asked to write the same path still sends its requests to the client, which is all that refuses it.
func TestKASCorpus_ABotWritesItsMemoryThroughTheClient(t *testing.T) {
	lines := loadKASLines(t, "bot_memory_write")
	doors := doorsOf(t, lines)
	if len(doors) != 4 {
		t.Fatalf("%d doors, want the Bot's new, its loads with memory off and on, and a regular session's new", len(doors))
	}
	bot, regular := doors[0], doors[3]
	if len(bot.directories) != 1 || !strings.HasPrefix(bot.directories[0], "~/.kiro/bots/") {
		t.Fatalf("the Bot's session/new lists additional directories %v, want its own directory", bot.directories)
	}
	file := bot.directories[0] + "/memory.md"
	for i, want := range [][]string{nil, bot.directories} {
		d := doors[1+i]
		if d.method != marotte.MethodSessionLoad || d.session != bot.session || !slices.Equal(d.directories, want) {
			t.Errorf("door %d is %s of %s listing %v, want a load of the Bot's session listing %v", 1+i, d.method, d.session, d.directories, want)
		}
		if d.report.CurrentModeID != marotte.BotRoleID {
			t.Errorf("the load listing %v reported mode %q, want the role", d.directories, d.report.CurrentModeID)
		}
	}
	if len(regular.directories) != 0 {
		t.Errorf("the regular session lists additional directories %v", regular.directories)
	}

	var botWrites, regularAsks int
	for _, x := range fileExchanges(t, lines) {
		if x.path != file {
			continue
		}
		switch x.session {
		case bot.session:
			if x.method == marotte.MethodFSWrite && !x.refused && strings.Contains(x.content, "Marker: slate-4410") {
				botWrites++
			}
		case regular.session:
			regularAsks++
			if !x.refused {
				t.Errorf("the regular session's %s of the memory file was answered", x.method)
			}
		}
	}
	if botWrites != 1 {
		t.Errorf("%d answered fs/write_text_file of %s carry the marker, want 1", botWrites, file)
	}
	if regularAsks == 0 {
		t.Error("the regular session sent no request for the memory file: its write never reached the client")
	}
}

// TestKASCorpus_ATangentOfABotLoadsOnTheDefaultMode pins P5 (design §15, §9.4) on the live answer: the fork names
// the default mode beside createdReason; the tangent's first load reports that mode with no role among its modes,
// though the fork's own reply lists the role; no not_found reaches the tangent; and its read of the Bot's memory
// file goes to the client, which refuses it.
func TestKASCorpus_ATangentOfABotLoadsOnTheDefaultMode(t *testing.T) {
	lines := loadKASLines(t, "bot_tangent")
	fork := slices.IndexFunc(lines, func(l kasLine) bool { return l.Dir == "out" && l.Frame.Method == marotte.MethodSessionFork })
	if fork < 0 {
		t.Fatal("the capture holds no session/fork")
	}
	if k := lines[fork].kiroMeta(t); k["modeId"] != marotte.DefaultModeID || k["createdReason"] != marotte.CreatedReasonTangent {
		t.Errorf("session/fork's _meta.kiro = %v, want modeId %q beside createdReason %q", k, marotte.DefaultModeID, marotte.CreatedReasonTangent)
	}
	reply := slices.IndexFunc(lines[fork:], func(l kasLine) bool {
		return l.Dir == "in" && l.Frame.Method == "" && l.Frame.ID != nil && *l.Frame.ID == *lines[fork].Frame.ID
	})
	if reply < 0 {
		t.Fatal("session/fork has no reply")
	}
	var forked struct {
		SessionID string `json:"sessionId"`
		Modes     struct {
			AvailableModes []marotte.SessionMode `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(lines[fork+reply].Frame.Result, &forked); err != nil || forked.SessionID == "" {
		t.Fatalf("session/fork answered %s (%v)", lines[fork+reply].Frame.Result, err)
	}
	if !listsMode(forked.Modes.AvailableModes, marotte.BotRoleID) {
		t.Error("the fork's reply lists no role: KAS no longer builds it from the parent's registry, so §9.4's caveat is stale")
	}

	doors := doorsOf(t, lines)
	if len(doors) != 2 || doors[1].session != forked.SessionID || doors[1].method != marotte.MethodSessionLoad {
		t.Fatalf("doors = %+v, want the Bot's new and the tangent's load", doors)
	}
	tangent := doors[1]
	if tangent.report.CurrentModeID != marotte.DefaultModeID || listsMode(tangent.report.AvailableModes, marotte.BotRoleID) {
		t.Errorf("the tangent's load reported %q among %+v, want %q and no role", tangent.report.CurrentModeID, tangent.report.AvailableModes, marotte.DefaultModeID)
	}
	if len(tangent.directories) != 0 {
		t.Errorf("the tangent's load lists additional directories %v", tangent.directories)
	}
	for _, l := range lines[fork:] {
		if l.Dir == "in" && l.Frame.Method == "_kiro/customAgent/not_found" {
			t.Errorf("a not_found after the fork: %s", l.Frame.Params)
		}
	}
	var reads int
	for _, x := range fileExchanges(t, lines) {
		if x.session != tangent.session || !strings.HasSuffix(x.path, "/memory.md") {
			continue
		}
		reads++
		if !x.refused {
			t.Errorf("the tangent's %s of the Bot's memory file was answered", x.method)
		}
	}
	if reads == 0 {
		t.Error("the tangent sent no request for the Bot's memory file")
	}
}
