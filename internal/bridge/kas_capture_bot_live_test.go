package bridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/bridge"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
)

const (
	botCodeword     = "ochre-2291"
	botCodewordNext = "umber-5817"
	botMarker       = "slate-4410"
	botMarkerNext   = "cobalt-6602"
	botMarkerLast   = "amber-3301"
	// reportDir marks the door's decoded answer: KAS's session/new and session/load results are consumed by the door
	// and never reach the lane, so the capture records what the door reported from them.
	reportDir = "report"
	// methodPolicyCheck runs KAS's own tool approval for one capability and target, asking the client as a tool
	// call would.
	methodPolicyCheck  = "_kiro/policy/check"
	methodNotFound     = "_kiro/customAgent/not_found"
	captureBotGoalNext = "Answer questions about this workspace. Your codeword is " + botCodewordNext + "."
)

// botProbes are the approvals P2 compares between a Default chat and a Bot under each Permission profile: the
// default agent rules' reach (a workspace read, the read-only shell list, kiro.dev) and what lies past it. A web_fetch
// target is a host name, as KAS's approval reads the tool's; 3.1.0's policy check asks for every web_fetch.
var botProbes = []struct {
	name, capability, command string
	paths                     []string
}{
	{name: "read in the workspace", capability: "fs_read", paths: []string{"notes.txt"}},
	{name: "read outside the workspace", capability: "fs_read", paths: []string{"/etc/hostname"}},
	{name: "write in the workspace", capability: "fs_write", paths: []string{"out.txt"}},
	{name: "write an agent file", capability: "fs_write", paths: []string{".kiro/agents/probe.json"}},
	{name: "a read-only git command", capability: "shell", command: "git status"},
	{name: "a development command", capability: "shell", command: "go build ./..."},
	{name: "a destructive command", capability: "shell", command: "rm -rf build"},
	{name: "the excluded npm audit", capability: "shell", command: "npm audit fix"},
	{name: "fetch kiro.dev", capability: "web_fetch", paths: []string{"kiro.dev"}},
	{name: "fetch another site", capability: "web_fetch", paths: []string{"example.com"}},
}

func captureBot(id, goal string, memory bool) *marotte.Chat {
	return &marotte.Chat{ID: id, Name: "Capture Bot", Bot: &marotte.Bot{Goal: goal, StartedAt: 1, Memory: memory}}
}

func (c *capture) botRole(rec *marotte.Chat) marotte.ClientCustomAgent {
	return bot.Role(rec, bot.Paths{KiroHome: c.home})
}

func botOpts(role marotte.ClientCustomAgent, presets []string) *marotte.SessionOpts {
	return &marotte.SessionOpts{
		Role: marotte.SessionRoleChat, Mode: marotte.BotRoleID,
		CustomAgents: []marotte.ClientCustomAgent{role}, Presets: presets,
	}
}

func (c *capture) memoryFile(rec *marotte.Chat) string {
	file, ok := bot.MemoryFile(rec, bot.Paths{KiroHome: c.home})
	if !ok {
		c.t.Fatalf("no memory file for %s under %s", rec.ID, c.home)
	}
	return file
}

func (c *capture) writeMemory(rec *marotte.Chat, body string) error {
	file := c.memoryFile(rec)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(body), 0o600)
}

// botDoor opens rec's session as marotte's Bot door does: the role on the door, then the memory file added as a
// context file while memory is on, or removed on a load while it is off (internal/agent holdBotMemory).
func (c *capture) botDoor(ctx context.Context, e *bridge.Engine, rec *marotte.Chat, sessionID string, presets []string) (*bridge.Session, int, error) {
	o := botOpts(c.botRole(rec), presets)
	o.SessionID = sessionID
	s, lane, err := c.openSession(ctx, e, o)
	if err != nil {
		return nil, 0, err
	}
	sub := marotte.SessionContextAdd
	switch {
	case !rec.Bot.Memory && sessionID == "":
		return s, lane, nil
	case !rec.Bot.Memory:
		sub = marotte.SessionContextRemove
	}
	return s, lane, c.editContext(ctx, s, lane, sub, c.memoryFile(rec))
}

func (c *capture) editContext(ctx context.Context, s *bridge.Session, lane int, sub, file string) error {
	p := marotte.SessionContextParams{SessionID: s.SessionID(), Subcommand: sub, Path: file, Force: sub == marotte.SessionContextAdd}
	raw, _ := json.Marshal(p)
	var params map[string]any
	_ = json.Unmarshal(raw, &params)
	resp, err := c.call(ctx, s, lane, marotte.MethodSessionContext, params)
	if err != nil {
		return fmt.Errorf("context %s: %w", sub, err)
	}
	var r struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(resp.Result, &r) != nil || !r.Success {
		return fmt.Errorf("context %s answered %s", sub, resp.Result)
	}
	return nil
}

// openSession opens o's session through marotte's door: session/new when o names no session, else session/load. It
// records the door's _meta.kiro choices and its report, and keeps recording the lane after a load, unlike loadSession.
func (c *capture) openSession(ctx context.Context, e *bridge.Engine, o *marotte.SessionOpts) (*bridge.Session, int, error) {
	lane, attach := c.attach()
	method := marotte.MethodSessionLoad
	params := map[string]any{"cwd": c.work}
	kiro := map[string]any{}
	if o.SessionID == "" {
		method = marotte.MethodSessionNew
		if o.Mode != "" {
			kiro["modeId"] = o.Mode
		}
	} else {
		params[marotte.KeySessionID] = o.SessionID
	}
	if len(o.CustomAgents) > 0 {
		kiro["customAgents"] = o.CustomAgents
	}
	if len(o.Presets) > 0 {
		kiro["policyPreset"] = o.Presets
	}
	params["_meta"] = map[string]any{"kiro": kiro}
	req := c.out(lane, map[string]any{"method": method, "params": params})
	var (
		s   *bridge.Session
		err error
	)
	if method == marotte.MethodSessionNew {
		s, err = e.NewSession(ctx, o, attach)
	} else {
		s, err = e.LoadSession(ctx, o, attach)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", method, err)
	}
	if method == marotte.MethodSessionNew {
		kiro["overrideSessionId"] = s.SessionID()
		c.rewrite(req, map[string]any{"method": method, "params": params})
	} else if err := c.waitReached(lane, s.SessionLoadSeq()); err != nil {
		return nil, 0, err
	}
	c.report(lane, method, s)
	return s, lane, nil
}

func (c *capture) rewrite(l *capLine, frame map[string]any) {
	frame["jsonrpc"] = "2.0"
	raw, _ := json.Marshal(frame)
	c.mu.Lock()
	l.Frame = raw
	c.mu.Unlock()
}

func (c *capture) report(lane int, method string, s *bridge.Session) {
	raw, _ := json.Marshal(map[string]any{
		"method": method, marotte.KeySessionID: s.SessionID(),
		"currentModeId": s.CurrentMode(), "availableModes": s.Modes(),
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, &capLine{Dir: reportDir, Frame: raw, lane: lane, laneSeq: c.reached[lane]})
}

// ask prompts text and returns the turn's own prose.
func (c *capture) ask(ctx context.Context, s *bridge.Session, lane int, text string) (string, error) {
	from := len(c.inFrames(lane))
	if err := c.prompt(ctx, s, lane, text); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range c.inFrames(lane)[from:] {
		if f.Params.Update.SessionUpdate == "agent_message_chunk" && f.metaString("agentSubtaskId") == "" {
			b.WriteString(f.text())
		}
	}
	return b.String(), nil
}

type contextEntry struct {
	Path    string `json:"path"`
	Matched bool   `json:"matched"`
}

// contextFiles is the session's own context files as KAS lists them; matched is false for a path with no file.
func (c *capture) contextFiles(ctx context.Context, s *bridge.Session, lane int) ([]contextEntry, error) {
	resp, err := c.call(ctx, s, lane, marotte.MethodSessionContext, map[string]any{marotte.KeySessionID: s.SessionID(), "subcommand": "show"})
	if err != nil {
		return nil, fmt.Errorf("context show: %w", err)
	}
	var r struct {
		Entries []contextEntry `json:"entries"`
		Success bool           `json:"success"`
	}
	if json.Unmarshal(resp.Result, &r) != nil || !r.Success {
		return nil, fmt.Errorf("context show answered %s", resp.Result)
	}
	return r.Entries, nil
}

func lists(entries []contextEntry, file string) bool {
	return slices.ContainsFunc(entries, func(e contextEntry) bool { return e.Path == file })
}

func onRole(s *bridge.Session, step string) error {
	if got := s.CurrentMode(); got != marotte.BotRoleID {
		return fmt.Errorf("%s reported mode %q, want %q", step, got, marotte.BotRoleID)
	}
	return nil
}

// runBotRole is P1: the role on session/new, on a reopen, on a reopen after an edited role (goal changed, memory on
// then off), and a reopen that sends no role. A regular chat opens first, the catalog baseline P1b compares with.
func runBotRole(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	reg, regLane, err := c.openSession(ctx, e, &marotte.SessionOpts{Role: marotte.SessionRoleChat})
	if err != nil {
		return err
	}
	if err := c.closeSession(ctx, reg, regLane); err != nil {
		return err
	}
	rec := captureBot(string(marotte.NewChatID()), "Answer questions about this workspace. Your codeword is "+botCodeword+".", false)
	s, lane, err := c.botDoor(ctx, e, rec, "", nil)
	if err != nil {
		return err
	}
	if err := onRole(s, "session/new"); err != nil {
		return err
	}
	reply, err := c.ask(ctx, s, lane, "Reply with the codeword your instructions give you, and nothing else.")
	if err != nil {
		return err
	}
	if !strings.Contains(reply, botCodeword) {
		return fmt.Errorf("the role's prompt did not reach the turn: %q", reply)
	}
	id := string(s.SessionID())
	reopen := func(step string) error {
		if err := c.closeSession(ctx, s, lane); err != nil {
			return err
		}
		if s, lane, err = c.botDoor(ctx, e, rec, id, nil); err != nil {
			return err
		}
		return onRole(s, step)
	}
	if err := reopen("a reopen"); err != nil {
		return err
	}

	rec.Bot.Goal, rec.Bot.Memory = captureBotGoalNext, true
	if err := c.writeMemory(rec, "Marker: "+botMarker+"\n"); err != nil {
		return err
	}
	if err := reopen("the reopen after an edit"); err != nil {
		return err
	}
	reply, err = c.ask(ctx, s, lane, "Reply with your codeword and the marker your memory file holds, separated by one space, and nothing else.")
	if err != nil {
		return err
	}
	if !strings.Contains(reply, botCodewordNext) || !strings.Contains(reply, botMarker) {
		return fmt.Errorf("the edited role did not reach the turn: %q", reply)
	}

	file := c.memoryFile(rec)
	rec.Bot.Memory = false
	if err := reopen("the reopen with memory off"); err != nil {
		return err
	}
	entries, err := c.contextFiles(ctx, s, lane)
	if err != nil {
		return err
	}
	if lists(entries, file) {
		return fmt.Errorf("memory is off and the session still lists its file: %+v", entries)
	}

	if err := c.closeSession(ctx, s, lane); err != nil {
		return err
	}
	if s, lane, err = c.openSession(ctx, e, &marotte.SessionOpts{SessionID: id, Role: marotte.SessionRoleChat}); err != nil {
		return err
	}
	if s.CurrentMode() == marotte.BotRoleID {
		return errors.New("a reopen with no role reported the role")
	}
	before := c.notFounds(lane)
	_, setErr := c.call(ctx, s, lane, marotte.MethodSetMode, map[string]any{
		marotte.KeySessionID: s.SessionID(), "modeId": marotte.BotRoleID,
	})
	deadline := time.Now().Add(10 * time.Second)
	for c.notFounds(lane) == before && time.Now().Before(deadline) {
		<-time.After(200 * time.Millisecond)
	}
	if setErr == nil && c.notFounds(lane) == before {
		return fmt.Errorf("session/set_mode to a role the session does not hold was neither refused nor reported (%s now)", s.CurrentMode())
	}
	return nil
}

func (c *capture) notFounds(lane int) int {
	n := 0
	for _, f := range c.inFrames(lane) {
		if f.Method == methodNotFound {
			n++
		}
	}
	return n
}

// runBotPolicy is P2: under each Permission profile, the same approvals on a Default chat and on a Bot, then the
// memory write on the Bot; and each session's own list of its tools, sub-agents on.
func runBotPolicy(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{AutoDelegation: "on"})
	if err != nil {
		return err
	}
	rec := captureBot(string(marotte.NewChatID()), "Keep this workspace's notes tidy.", true)
	if err := c.writeMemory(rec, "Nothing yet.\n"); err != nil {
		return err
	}
	for _, p := range policyfile.Profiles() {
		def, defLane, err := c.openSession(ctx, e, &marotte.SessionOpts{Role: marotte.SessionRoleChat, Presets: p.Presets})
		if err != nil {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
		bs, botLane, err := c.botDoor(ctx, e, rec, "", p.Presets)
		if err != nil {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
		if err := onRole(bs, p.ID); err != nil {
			return err
		}
		for _, probe := range botProbes {
			for _, side := range []struct {
				s    *bridge.Session
				lane int
			}{{def, defLane}, {bs, botLane}} {
				targets := probe.paths
				if strings.HasPrefix(probe.capability, "fs_") {
					targets = c.inWork(targets)
				}
				if err := c.policyCheck(ctx, side.s, side.lane, probe.capability, probe.command, targets); err != nil {
					return fmt.Errorf("%s, %s: %w", p.ID, probe.name, err)
				}
			}
		}
		if err := c.policyCheck(ctx, bs, botLane, "fs_write", "", []string{c.memoryFile(rec)}); err != nil {
			return fmt.Errorf("%s, the memory write: %w", p.ID, err)
		}
		if p.ID != policyfile.ProfileTrusted {
			continue
		}
		const listTools = "List the exact name of every tool you can call, one per line, and nothing else. Call no tool."
		defTools, err := c.ask(ctx, def, defLane, listTools)
		if err != nil {
			return err
		}
		if !strings.Contains(defTools, "invoke_sub_agent") {
			return fmt.Errorf("the Default chat's own tool list names no invoke_sub_agent: %q", defTools)
		}
		if _, err := c.ask(ctx, bs, botLane, listTools); err != nil {
			return err
		}
	}
	return nil
}

func (c *capture) inWork(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if filepath.IsAbs(p) {
			out[i] = p
		} else {
			out[i] = filepath.Join(c.work, p)
		}
	}
	return out
}

func (c *capture) policyCheck(ctx context.Context, s *bridge.Session, lane int, capability, command string, paths []string) error {
	params := map[string]any{marotte.KeySessionID: s.SessionID(), "capability": capability}
	if command != "" {
		params["command"] = command
	}
	if len(paths) > 0 {
		params["paths"] = paths
	}
	resp, err := c.call(ctx, s, lane, methodPolicyCheck, params)
	if err != nil {
		return err
	}
	var r struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(resp.Result, &r) != nil || r.Outcome == "" {
		return fmt.Errorf("policy check answered %s", resp.Result)
	}
	return nil
}

// runBotMemory is P3 on the context-file path: a memory file added before it exists loads once written, an edit
// reaches the next turn, a deleted file loads nothing and a recreated one loads again. Neither a regular session on the
// same engine nor a fork of the Bot lists the file, and KAS writes the path only into the Bot session's own files.
func runBotMemory(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	rec := captureBot(string(marotte.NewChatID()), "Keep this workspace's notes tidy.", true)
	file := c.memoryFile(rec)
	s, lane, err := c.botDoor(ctx, e, rec, "", nil)
	if err != nil {
		return err
	}
	if err := onRole(s, "session/new"); err != nil {
		return err
	}
	const askMarker = "Reply with the marker your memory file holds now, or the single word none if you see no memory file content, and nothing else. Call no tool."
	expect := func(marker string) error {
		reply, err := c.ask(ctx, s, lane, askMarker)
		if err != nil {
			return err
		}
		if marker == "" && strings.Contains(reply, "Marker") || marker != "" && !strings.Contains(reply, marker) {
			return fmt.Errorf("want marker %q, the turn answered %q", marker, reply)
		}
		return nil
	}
	steps := []struct {
		body   string
		remove bool
		want   string
	}{
		{body: "", want: ""},
		{body: "Marker: " + botMarker + "\n", want: botMarker},
		{body: "Marker: " + botMarkerNext + "\n", want: botMarkerNext},
		{remove: true, want: ""},
		{body: "Marker: " + botMarkerLast + "\n", want: botMarkerLast},
	}
	for i, st := range steps {
		switch {
		case st.remove:
			if err := os.Remove(file); err != nil {
				return err
			}
		case st.body != "":
			if err := c.writeMemory(rec, st.body); err != nil {
				return err
			}
		}
		if st.remove {
			entries, err := c.contextFiles(ctx, s, lane)
			if err != nil {
				return err
			}
			if !lists(entries, file) || slices.ContainsFunc(entries, func(e contextEntry) bool { return e.Path == file && e.Matched }) {
				return fmt.Errorf("a deleted memory file: want it listed unmatched, got %+v", entries)
			}
			continue
		}
		if err := expect(st.want); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
	}
	return c.checkMemoryScope(ctx, e, s, lane, file)
}

// checkMemoryScope checks that nothing but the Bot's own session holds its context file.
func (c *capture) checkMemoryScope(ctx context.Context, e *bridge.Engine, s *bridge.Session, lane int, file string) error {
	reg, regLane, err := c.openSession(ctx, e, &marotte.SessionOpts{Role: marotte.SessionRoleChat})
	if err != nil {
		return err
	}
	if entries, err := c.contextFiles(ctx, reg, regLane); err != nil || len(entries) != 0 {
		return fmt.Errorf("a regular session lists context files %+v (%v)", entries, err)
	}
	reply, err := c.ask(ctx, reg, regLane, "Reply with any word that follows \"Marker:\" in your context, or the single word none, and nothing else. Call no tool.")
	if err != nil {
		return err
	}
	if strings.Contains(reply, botMarkerLast) {
		return fmt.Errorf("a regular session saw the Bot's memory: %q", reply)
	}
	resp, err := c.call(ctx, s, lane, marotte.MethodSessionFork, map[string]any{
		marotte.KeySessionID: s.SessionID(), "cwd": c.work,
		"_meta": map[string]any{"kiro": map[string]any{"createdReason": marotte.CreatedReasonTangent, "modeId": marotte.DefaultModeID}},
	})
	if err != nil {
		return fmt.Errorf("session/fork: %w", err)
	}
	var forked struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(resp.Result, &forked) != nil || forked.SessionID == "" {
		return fmt.Errorf("session/fork answered %s", resp.Result)
	}
	fs, forkLane, err := c.openSession(ctx, e, &marotte.SessionOpts{SessionID: forked.SessionID, Role: marotte.SessionRoleChat})
	if err != nil {
		return err
	}
	if entries, err := c.contextFiles(ctx, fs, forkLane); err != nil || lists(entries, file) {
		return fmt.Errorf("the fork lists the Bot's memory file: %+v (%v)", entries, err)
	}
	return c.persistedOnlyBy(string(s.SessionID()), forked.SessionID, file)
}

// persistedOnlyBy fails when a file under KIRO_HOME or the workspace names path and belongs to neither the Bot's
// session nor its fork, whose copied conversation carries the snapshots taken before the fork.
func (c *capture) persistedOnlyBy(botSession, fork, path string) error {
	var found []string
	for _, root := range []string{c.home, c.work} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || p == path {
				return err
			}
			raw, err := os.ReadFile(p)
			if err != nil || !strings.Contains(string(raw), path) {
				return err
			}
			if !strings.Contains(p, botSession) && !strings.Contains(p, fork) {
				found = append(found, p)
			}
			c.t.Logf("names the memory file: %s", strings.TrimPrefix(p, root))
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("files outside the Bot's session name its memory file: %v", found)
	}
	return nil
}
