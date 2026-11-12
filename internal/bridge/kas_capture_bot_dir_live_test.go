package bridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/marotte"
)

// fileRequest is one file request KAS sent on a lane, and whether the capture's answer refused it.
type fileRequest struct {
	method  string
	content string
	refused bool
}

// fileRequests are the file requests on lane naming path, in order, each with its answer.
func (c *capture) fileRequests(lane int, path string) []fileRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []fileRequest
	pending := map[string]int{}
	for _, l := range c.lines {
		if l.lane != lane {
			continue
		}
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"params"`
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(l.Frame, &f)
		isFile := strings.HasPrefix(f.Method, "fs/") || strings.HasPrefix(f.Method, "_kiro/fs/")
		switch {
		case l.Dir == "in" && l.isFrame && len(f.ID) > 0 && isFile && f.Params.Path == path:
			pending[string(f.ID)] = len(out)
			out = append(out, fileRequest{method: f.Method, content: f.Params.Content})
		case l.Dir == "out" && f.Method == "" && len(f.ID) > 0:
			if i, ok := pending[string(f.ID)]; ok {
				out[i].refused = len(f.Error) > 0
				delete(pending, string(f.ID))
			}
		}
	}
	return out
}

// replayed reports a user message on lane holding text, which a load's replay of the session's history carries.
func (c *capture) replayed(lane int, text string) bool {
	return slices.ContainsFunc(c.inFrames(lane), func(f frameView) bool {
		return f.Params.Update.SessionUpdate == "user_message_chunk" && strings.Contains(f.text(), text)
	})
}

// runBotMemoryWrite: KAS writes a Bot's memory file through fs/write_text_file with the Bot's directory among
// the session's additional directories, a load across a memory toggle finds the session, and a regular session's
// requests for the same file reach the client, which refuses them.
func runBotMemoryWrite(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	rec := captureBot(string(marotte.NewChatID()), "Keep this workspace's notes tidy.", true)
	if err := bot.EnsureMemory(rec, bot.Paths{KiroHome: c.home}); err != nil {
		return err
	}
	file := c.memoryFile(rec)
	s, lane, err := c.botDoor(ctx, e, rec, "", nil)
	if err != nil {
		return err
	}
	if err := onRole(s, "session/new"); err != nil {
		return err
	}
	if _, err := c.ask(ctx, s, lane, "Add the line \"Marker: "+botMarker+"\" at the end of your memory file with your file-writing tool, then reply with the single word done."); err != nil {
		return err
	}
	if !slices.ContainsFunc(c.fileRequests(lane, file), func(r fileRequest) bool {
		return r.method == marotte.MethodFSWrite && !r.refused && strings.Contains(r.content, botMarker)
	}) {
		return fmt.Errorf("no admitted fs/write_text_file of the memory file carries the marker: %+v", c.fileRequests(lane, file))
	}
	if body, err := os.ReadFile(file); err != nil || !strings.Contains(string(body), botMarker) {
		return fmt.Errorf("the memory file after the turn holds %q (%v)", body, err)
	}

	id := string(s.SessionID())
	for _, memory := range []bool{false, true} {
		if err := c.closeSession(ctx, s, lane); err != nil {
			return err
		}
		rec.Bot.Memory = memory
		if s, lane, err = c.botDoor(ctx, e, rec, id, nil); err != nil {
			return err
		}
		step := fmt.Sprintf("the reopen with memory %v", memory)
		if err := onRole(s, step); err != nil {
			return err
		}
		if !c.replayed(lane, botMarker) {
			return fmt.Errorf("%s replayed no turn naming the marker: KAS did not find the session", step)
		}
	}
	reply, err := c.ask(ctx, s, lane, "Reply with the marker your memory file holds, and nothing else. Call no tool.")
	if err != nil {
		return err
	}
	if !strings.Contains(reply, botMarker) {
		return fmt.Errorf("after the toggle the turn answered %q, want the written marker", reply)
	}

	reg, regLane, err := c.openSession(ctx, e, &marotte.SessionOpts{Role: marotte.SessionRoleChat})
	if err != nil {
		return err
	}
	if _, err := c.ask(ctx, reg, regLane, "Replace the whole content of the file "+file+" with the single line \"Marker: "+botMarkerNext+"\" using your file-writing tool, then reply with the single word done, or failed if the write fails."); err != nil {
		return err
	}
	reqs := c.fileRequests(regLane, file)
	if len(reqs) == 0 {
		return errors.New("the regular session sent no request for the memory file")
	}
	for _, r := range reqs {
		if !r.refused {
			return fmt.Errorf("the regular session's %s of the memory file was answered", r.method)
		}
	}
	if body, err := os.ReadFile(file); err != nil || strings.Contains(string(body), botMarkerNext) {
		return fmt.Errorf("the memory file after the regular session's turn holds %q (%v)", body, err)
	}
	return nil
}

// runBotTangent: a tangent forked from a Bot on the default mode loads as a regular chat on that mode, with no
// role among its modes and no not_found, and its read of the Bot's memory file reaches the client, which refuses it.
// The fork copies the Bot's conversation, which holds the file as it stood, so the file changes after the fork and
// only a read could learn the new marker.
func runBotTangent(ctx context.Context, c *capture) error {
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	rec := captureBot(string(marotte.NewChatID()), "Keep this workspace's notes tidy.", true)
	if err := c.writeMemory(rec, "Marker: "+botMarkerLast+"\n"); err != nil {
		return err
	}
	file := c.memoryFile(rec)
	s, lane, err := c.botDoor(ctx, e, rec, "", nil)
	if err != nil {
		return err
	}
	if err := onRole(s, "session/new"); err != nil {
		return err
	}
	if _, err := c.ask(ctx, s, lane, "Reply with the single word ready, and nothing else. Call no tool."); err != nil {
		return err
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
	if err := c.writeMemory(rec, "Marker: "+botMarkerNext+"\n"); err != nil {
		return err
	}

	ts, tLane, err := c.openSession(ctx, e, &marotte.SessionOpts{SessionID: forked.SessionID, Role: marotte.SessionRoleChat})
	if err != nil {
		return err
	}
	if got := ts.CurrentMode(); got != marotte.DefaultModeID {
		return fmt.Errorf("the tangent's first open reported mode %q, want %q", got, marotte.DefaultModeID)
	}
	if slices.ContainsFunc(ts.Modes(), func(m marotte.SessionMode) bool { return m.ID == marotte.BotRoleID }) {
		return fmt.Errorf("the tangent's first open lists the role among %+v", ts.Modes())
	}
	reply, err := c.ask(ctx, ts, tLane, "Call your file-reading tool on the file "+file+" now, even if you think you know what it holds, and reply with its first line, or the single word refused if the read fails.")
	if err != nil {
		return err
	}
	reqs := c.fileRequests(tLane, file)
	if len(reqs) == 0 {
		return errors.New("the tangent sent no request for the memory file")
	}
	for _, r := range reqs {
		if !r.refused {
			return fmt.Errorf("the tangent's %s of the memory file was answered", r.method)
		}
	}
	if strings.Contains(reply, botMarkerNext) {
		return fmt.Errorf("the tangent answered %q, the file's content", reply)
	}
	if n := c.notFounds(tLane); n != 0 {
		return fmt.Errorf("the tangent's lane holds %d not_found frames", n)
	}
	return nil
}
