package chat

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// internalToolTitle is the engine bookkeeping call the transcript never mounts, twin of static-src/tool-schema.ts
// INTERNAL_TOOL_TITLES: it neither joins nor breaks a tool-card run.
const internalToolTitle = "Fetching your cloud config"

// searchView is entries less what the transcript draws nothing for: every hidden hook's tool_call and tool_result
// while hooks.showStatus is off. entries itself is never modified.
func (s *Store) searchView(entries []marotte.Entry) []marotte.Entry {
	if s.hookStatusShown == nil || s.hookStatusShown() {
		return entries
	}
	hidden := hiddenHookIDs(entries)
	if len(hidden) == 0 {
		return entries
	}
	return slices.DeleteFunc(slices.Clone(entries), func(e marotte.Entry) bool {
		id := e.ID
		switch e.Kind {
		case marotte.EntryKindToolCall:
		case marotte.EntryKindToolResult:
			id = toolCallIDOfResult(e.ID)
		default:
			return false
		}
		_, gone := hidden[id]
		return gone
	})
}

// toolRunKey is one lane of one turn, the scope a tool-card run lives in.
type toolRunKey struct {
	turn string
	lane string
}

type toolRunMember struct {
	call *marotte.EntryToolCall
	id   string
	at   int
}

// hiddenHookIDs are the hook tool_call ids the transcript draws nothing for while hooks.showStatus is off, the twin
// of static-src/hook-fold.ts (hookRenders, foldGroupHooks): a quiet hook hides, except one gating a tool call in a
// run of tool cards that holds a failed, denied or declined card.
func hiddenHookIDs(entries []marotte.Entry) map[string]struct{} {
	results := settledResults(entries)
	hidden := make(map[string]struct{})
	for _, run := range toolCardRuns(entries, results) {
		hideQuietHooks(run, results, hidden)
	}
	return hidden
}

// toolCardRuns are the log's tool-card runs as the transcript groups them, in log order of each run's first card;
// internal/chat/testdata/tool_runs.json pins the boundaries against static-src/messages-blocks.ts indexGroups.
func toolCardRuns(entries []marotte.Entry, results map[string]*marotte.EntryToolResult) [][]toolRunMember {
	sc := toolRunScan{
		results: results,
		silent:  silentSteers(entries),
		reverts: newestReverts(entries),
		open:    make(map[toolRunKey][]toolRunMember),
		plans:   make(map[toolRunKey]bool),
		owners:  make(map[string]bool),
	}
	for i := range entries {
		sc.observe(i, &entries[i])
	}
	for k := range sc.open {
		sc.flush(k)
	}
	slices.SortFunc(sc.done, func(a, b []toolRunMember) int { return a[0].at - b[0].at })
	return sc.done
}

// toolRunScan walks a log one open run per lane of a turn.
type toolRunScan struct {
	results map[string]*marotte.EntryToolResult
	silent  map[int]struct{}
	reverts map[toolRunKey]int
	open    map[toolRunKey][]toolRunMember
	plans   map[toolRunKey]bool
	owners  map[string]bool
	done    [][]toolRunMember
}

// observe adds a tool card to its lane's run, or ends the run at anything else the transcript draws in that lane.
func (sc *toolRunScan) observe(i int, e *marotte.Entry) {
	k := toolRunKey{turn: e.Turn, lane: e.Lane}
	switch e.Kind {
	case marotte.EntryKindToolCall:
		var call marotte.EntryToolCall
		if json.Unmarshal(e.Payload, &call) != nil || call.Title == internalToolTitle {
			return
		}
		if breaksRun(&call, sc.results[e.ID], sc.owners) {
			sc.flush(k)
			return
		}
		sc.open[k] = append(sc.open[k], toolRunMember{id: e.ID, call: &call, at: i})
	case marotte.EntryKindSteer:
		// A steer draws in the chat's own flow whichever lane read it.
		if _, silent := sc.silent[i]; !silent {
			sc.flush(toolRunKey{turn: e.Turn})
		}
	case marotte.EntryKindTurnRevert:
		// Only a lane's newest rewind row draws; a later one replaces it.
		if sc.reverts[k] == i {
			sc.flush(k)
		}
	case marotte.EntryKindModelFallback:
		if !subagentFallback(e) {
			sc.flush(k)
		}
	case marotte.EntryKindPlan:
		// Only a lane's first plan draws; a later one updates that card in place.
		if !sc.plans[k] {
			sc.plans[k] = true
			sc.flush(k)
		}
	case marotte.EntryKindTurnOpen, marotte.EntryKindTurnClose, marotte.EntryKindTurnBind,
		marotte.EntryKindTurnWake, marotte.EntryKindRunNamed, marotte.EntryKindToolResult,
		marotte.EntryKindReconciled, marotte.EntryKindSteerDelivered, marotte.EntryKindSteerCleared:
	default:
		sc.flush(k)
	}
}

func (sc *toolRunScan) flush(k toolRunKey) {
	if members := sc.open[k]; len(members) > 0 {
		sc.done = append(sc.done, members)
	}
	delete(sc.open, k)
}

// silentSteers are the indexes of the steers that draw nothing where they were filed: a row a wake draws in its
// header (wakeFacts.drawn, chat-wide), and a dropped agent note its own turn read (readDrops).
func silentSteers(entries []marotte.Entry) map[int]struct{} {
	drawn := wakeFactsOf(entries).drawn
	readDrop := readDropsByTurn(entries)
	out := make(map[int]struct{})
	for i := range entries {
		e := &entries[i]
		if e.Kind != marotte.EntryKindSteer {
			continue
		}
		if _, woken := drawn[e.ID]; woken || readDropped(readDrop[e.Turn], e) {
			out[i] = struct{}{}
		}
	}
	return out
}

// readDropsByTurn is readDrops per turn, over the steer and steer_delivered entries it reads.
func readDropsByTurn(entries []marotte.Entry) map[string]map[string]struct{} {
	byTurn := make(map[string][]marotte.Entry)
	for i := range entries {
		if k := entries[i].Kind; k == marotte.EntryKindSteer || k == marotte.EntryKindSteerDelivered {
			byTurn[entries[i].Turn] = append(byTurn[entries[i].Turn], entries[i])
		}
	}
	out := make(map[string]map[string]struct{}, len(byTurn))
	for turn, es := range byTurn {
		out[turn] = readDrops(es)
	}
	return out
}

// newestReverts is each lane's newest turn_revert index per turn, the one row the transcript draws.
func newestReverts(entries []marotte.Entry) map[toolRunKey]int {
	out := make(map[toolRunKey]int)
	for i := range entries {
		if e := &entries[i]; e.Kind == marotte.EntryKindTurnRevert {
			out[toolRunKey{turn: e.Turn, lane: e.Lane}] = i
		}
	}
	return out
}

// hideQuietHooks hides a run's quiet hooks unless a refused card keeps the run's tool-trigger hooks as rows.
func hideQuietHooks(members []toolRunMember, results map[string]*marotte.EntryToolResult, hidden map[string]struct{}) {
	refused := slices.ContainsFunc(members, func(m toolRunMember) bool {
		return m.call.Kind != marotte.ToolKindHook && toolRefused(m.call, results[m.id])
	})
	for _, m := range members {
		if m.call.Kind != marotte.ToolKindHook || !hookQuiet(m.call, results[m.id]) {
			continue
		}
		if !refused || !toolTriggerHook(m.call) {
			hidden[m.id] = struct{}{}
		}
	}
}

// breaksRun is whether a call opens a box of its own instead of joining the run: a delegate's invocation, or the
// first call naming a workflow run, which owns its run card.
func breaksRun(call *marotte.EntryToolCall, res *marotte.EntryToolResult, owners map[string]bool) bool {
	if call.AgentSubtaskID != "" {
		return true
	}
	run := call.WorkflowID
	if run == "" && res != nil {
		run = res.WorkflowID
	}
	if run == "" || owners[run] {
		return false
	}
	owners[run] = true
	return true
}

// settledResults maps each tool_call id to its tool_result.
func settledResults(entries []marotte.Entry) map[string]*marotte.EntryToolResult {
	out := make(map[string]*marotte.EntryToolResult)
	for i := range entries {
		e := &entries[i]
		if e.Kind != marotte.EntryKindToolResult {
			continue
		}
		var res marotte.EntryToolResult
		if json.Unmarshal(e.Payload, &res) == nil {
			out[toolCallIDOfResult(e.ID)] = &res
		}
	}
	return out
}

// hookQuiet is a hook that settled completed and printed nothing.
func hookQuiet(call *marotte.EntryToolCall, res *marotte.EntryToolResult) bool {
	status, output := call.Status, call.Output
	if res != nil {
		status = res.Status
		if res.Output != "" {
			output = res.Output
		}
	}
	return status == marotte.ToolCompleted && strings.TrimSpace(output) == ""
}

// toolTriggerHook is a hook that gates one tool call; an unresolved trigger is not one.
func toolTriggerHook(call *marotte.EntryToolCall) bool {
	return call.HookTrigger == "PreToolUse" || call.HookTrigger == "PostToolUse"
}

// toolRefused is a card whose outcome mark reads failed, denied or declined.
func toolRefused(call *marotte.EntryToolCall, res *marotte.EntryToolResult) bool {
	if call.Denial != nil || call.Declined {
		return true
	}
	if res == nil {
		return call.Status == marotte.ToolFailed
	}
	return res.Status == marotte.ToolFailed || res.Denial != nil || res.Declined
}
