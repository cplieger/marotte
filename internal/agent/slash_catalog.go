package agent

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/webhttp/v4"
)

var (
	_ translate.SlashCatalog   = (*slashCatalog)(nil)
	_ translate.SteeringIssues = (*steeringIssues)(nil)
)

// slashCatalog is the workspace slash menu, held once: every session runs on one engine in one cwd, and the kinds do
// not depend on mode.
type slashCatalog struct {
	cmds []marotte.SlashCommand
	mu   sync.Mutex
	// ready is set by the first list KAS sent; that list may be empty.
	ready bool
}

// Set replaces the list with a session frame's and marks it ready.
func (c *slashCatalog) Set(cmds []marotte.SlashCommand) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready && slashCommandsEqual(c.cmds, cmds) {
		return false
	}
	c.ready = true
	c.cmds = slices.Clone(cmds)
	return true
}

func slashCommandsEqual(a, b []marotte.SlashCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := &a[i], &b[i]
		if x.Name != y.Name || x.Description != y.Description || x.Kind != y.Kind ||
			x.Hint != y.Hint || !slices.Equal(x.Arguments, y.Arguments) {
			return false
		}
	}
	return true
}

// snapshot returns a copy of the list, never nil, and whether KAS has sent one.
func (c *slashCatalog) snapshot() (cmds []marotte.SlashCommand, ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]marotte.SlashCommand, len(c.cmds))
	copy(out, c.cmds)
	return out, c.ready
}

type steeringIssues struct {
	issues map[string][]marotte.SteeringIssue
	mu     sync.Mutex
}

// Set replaces the map, reporting whether it changed.
func (s *steeringIssues) Set(issues map[string][]marotte.SteeringIssue) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maps.EqualFunc(s.issues, issues, steeringIssuesEqual) {
		return false
	}
	s.issues = maps.Clone(issues)
	return true
}

func steeringIssuesEqual(a, b []marotte.SteeringIssue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := &a[i], &b[i]
		if x.Code != y.Code || x.Reference != y.Reference || x.Reason != y.Reason ||
			x.Remediation != y.Remediation || !slices.Equal(x.Patterns, y.Patterns) {
			return false
		}
	}
	return true
}

// snapshot returns a copy of the map, never nil.
func (s *steeringIssues) snapshot() map[string][]marotte.SteeringIssue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]marotte.SteeringIssue, len(s.issues))
	for k, v := range s.issues {
		out[k] = slices.Clone(v)
	}
	return out
}

// handleSlashCommands: GET /api/slash-commands.
func (rt *Runtime) handleSlashCommands(w http.ResponseWriter, _ *http.Request) {
	cmds, ready := rt.slash.snapshot()
	webhttp.WriteJSON(w, marotte.SlashCommandsResponse{Commands: cmds, Ready: ready})
}

// handleSteeringIssues: GET /api/steering/issues.
func (rt *Runtime) handleSteeringIssues(w http.ResponseWriter, _ *http.Request) {
	webhttp.WriteJSON(w, marotte.SteeringIssuesResponse{Issues: rt.steeringIssues.snapshot()})
}

// applySlashCommands takes an observer pass's available_commands_update, the menu's seed before any chat opened.
func (rt *Runtime) applySlashCommands(update json.RawMessage) {
	cmds, ok := translate.ReadSlashCatalog(update)
	if !ok || !rt.slash.Set(cmds) {
		return
	}
	rt.Broadcast(rt.lifecycle.shutdownCtx, marotte.NewEvent(marotte.EventSlashCommandsChanged, "", marotte.SlashCommandsChangedPayload{}))
}
