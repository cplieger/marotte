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
// of static-src/hook-fold.ts (hookRenders, foldGroupHooks) and tool-group.ts placeStrays: a quiet hook hides, except
// one gating a tool call in a run of tool cards that holds a failed, denied or declined card, or one a refused box
// keeps (toolRunScan.kept).
func hiddenHookIDs(entries []marotte.Entry) map[string]struct{} {
	results := settledResults(entries)
	hidden := make(map[string]struct{})
	sc := scanToolRuns(entries, results)
	for _, run := range sc.done {
		hideQuietHooks(run, results, hidden)
	}
	for id := range sc.kept {
		delete(hidden, id)
	}
	return hidden
}

// scanToolRuns walks the log once for its tool-card runs, as the transcript groups them in log order of each run's
// first card, and the hooks a refused box keeps; internal/chat/testdata/tool_runs.json pins both against
// static-src/messages-blocks.ts indexGroups.
func scanToolRuns(entries []marotte.Entry, results map[string]*marotte.EntryToolResult) *toolRunScan {
	sc := &toolRunScan{
		results: results,
		silent:  silentSteers(entries),
		reverts: newestReverts(entries),
		open:    make(map[toolRunKey][]toolRunMember),
		plans:   make(map[toolRunKey]bool),
		owners:  make(map[string]bool),
		boxes:   make(map[boxKey]bool),
		after:   make(map[toolRunKey]bool),
		kept:    make(map[string]struct{}),
	}
	for i := range entries {
		sc.observe(i, &entries[i])
	}
	for k := range sc.open {
		sc.flush(k)
	}
	slices.SortFunc(sc.done, func(a, b []toolRunMember) int { return a[0].at - b[0].at })
	return sc
}

// toolRunScan walks a log one open run per lane of a turn.
type toolRunScan struct {
	results map[string]*marotte.EntryToolResult
	silent  map[int]struct{}
	reverts map[toolRunKey]int
	open    map[toolRunKey][]toolRunMember
	plans   map[toolRunKey]bool
	owners  map[string]bool
	boxes   map[boxKey]bool
	// after holds a lane whose last drawn entry opened a box, and whether that box's call was refused.
	after map[toolRunKey]bool
	// kept are the hooks a refused box keeps as rows, the ones endRun gives it.
	kept map[string]struct{}
	done [][]toolRunMember
}

// observe adds a tool card to its lane's run, or ends the run at anything else the transcript draws in that lane.
func (sc *toolRunScan) observe(i int, e *marotte.Entry) {
	k := toolRunKey{turn: e.Turn, lane: e.Lane}
	switch e.Kind {
	case marotte.EntryKindToolCall:
		sc.observeCall(i, e, k)
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
		marotte.EntryKindReconciled, marotte.EntryKindSteerDelivered, marotte.EntryKindSteerCleared,
		marotte.EntryKindStepGraded:
	default:
		sc.flush(k)
	}
}

// observeCall adds a tool call to its lane's run as seatOf seats it; an internal call draws nothing.
func (sc *toolRunScan) observeCall(i int, e *marotte.Entry, k toolRunKey) {
	var call marotte.EntryToolCall
	if json.Unmarshal(e.Payload, &call) != nil || call.Title == internalToolTitle {
		return
	}
	switch sc.seatOf(&call, e) {
	case seatBox:
		refused := toolRefused(&call, sc.results[e.ID])
		sc.endRun(k, true, refused)
		sc.after[k] = refused
	case seatNone:
	case seatMember:
		sc.open[k] = append(sc.open[k], toolRunMember{id: e.ID, call: &call, at: i})
	}
}

// flush ends lane k's run at an entry that opens no box.
func (sc *toolRunScan) flush(k toolRunKey) {
	sc.endRun(k, false, false)
}

// endRun ends lane k's run, at a box when atBox, whose call was refused when nextRefused. A box owns the PostToolUse
// hooks right after it and the PreToolUse hooks right before it, each set a run of its own (messages-blocks.ts
// indexGroups), and a refused box keeps them.
func (sc *toolRunScan) endRun(k toolRunKey, atBox, nextRefused bool) {
	members := sc.open[k]
	prevRefused, afterBox := sc.after[k]
	delete(sc.after, k)
	delete(sc.open, k)
	from := 0
	for afterBox && from < len(members) && hookOn(members[from], "PostToolUse") {
		from++
	}
	to := len(members)
	for atBox && to > from && hookOn(members[to-1], "PreToolUse") {
		to--
	}
	for _, run := range [][]toolRunMember{members[:from], members[from:to], members[to:]} {
		if len(run) > 0 {
			sc.done = append(sc.done, run)
		}
	}
	for i, m := range members {
		if (i < from && prevRefused) || (i >= to && nextRefused) {
			sc.kept[m.id] = struct{}{}
		}
	}
}

// hookOn is a hook card fired by trigger.
func hookOn(m toolRunMember, trigger string) bool {
	return m.call.Kind == marotte.ToolKindHook && m.call.HookTrigger == trigger
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

// runSeat is how a tool call takes part in its lane's tool-card run.
type runSeat int

const (
	seatMember runSeat = iota // a tool card in the run
	seatBox                   // opens a box of its own, which ends the run
	seatNone                  // a repeat of a box its turn's lane already opened, drawing nothing where it sits
)

// boxKey names a box a turn's lane opens once: a delegate's card, or a run's card on a delegate's page.
type boxKey struct{ turn, lane, box string }

// seatOf is where a call sits. A delegate's invocation, filed in its issuer's lane and stamped with the delegate's,
// opens that delegate's card; the live path stamps a delegate's own calls with their lane, which makes them tool
// cards. In the chat's lane only a run's first mention owns its card (block-window.ts runCardOwners) and a later one
// is a tool card; a delegate's page is its own surface, where every mention seats on the card.
func (sc *toolRunScan) seatOf(call *marotte.EntryToolCall, e *marotte.Entry) runSeat {
	if call.AgentSubtaskID != "" {
		if call.AgentSubtaskID == e.Lane {
			return seatMember
		}
		return sc.openBox(boxKey{turn: e.Turn, lane: e.Lane, box: "sub:" + call.AgentSubtaskID})
	}
	run := call.WorkflowID
	if res := sc.results[e.ID]; run == "" && res != nil {
		run = res.WorkflowID
	}
	switch {
	case run == "":
		return seatMember
	case e.Lane != "":
		return sc.openBox(boxKey{turn: e.Turn, lane: e.Lane, box: "run:" + run})
	case sc.owners[run]:
		return seatMember
	}
	sc.owners[run] = true
	return seatBox
}

// openBox seats a call on box k, opened by the first call naming it in its turn's lane.
func (sc *toolRunScan) openBox(k boxKey) runSeat {
	if sc.boxes[k] {
		return seatNone
	}
	sc.boxes[k] = true
	return seatBox
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

// toolRefused is a card whose outcome mark reads failed, denied or declined, twin of static-src/hook-fold.ts
// callRefused.
func toolRefused(call *marotte.EntryToolCall, res *marotte.EntryToolResult) bool {
	if call.Denial != nil || call.Declined {
		return true
	}
	if res == nil {
		return call.Status == marotte.ToolFailed
	}
	return res.Status == marotte.ToolFailed || res.Denial != nil || res.Declined ||
		(res.Status == marotte.ToolAborted && rejectedAsk(res.Interaction))
}

// rejectedAsk is an approval the reader rejected, static-src/interaction.ts askBucket's "rejected".
func rejectedAsk(i *marotte.ToolInteraction) bool {
	return i != nil && i.Type != "user_input" && (i.Choice == "reject_once" || i.Choice == "reject_always")
}
