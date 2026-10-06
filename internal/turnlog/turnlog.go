// Package turnlog is the in-memory accumulator for one open turn: per lane, the text or
// thinking entry still coalescing deltas and the steer carry withheld beside it, frozen
// into a marotte.Entry the moment anything else appends.
//
// It never touches disk or assigns a position: a seal hands the entry to the injected
// Sink, which fills Seq and Ts. Every append seals, so within one lane plus the lane-less
// entries, seq order is arrival order.
package turnlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// SteerAckPrefix is the shortest span committing a `[` to a steering acknowledgement
// marker; a carry starting with it is an unclosed marker and a seal drops it. Shorter
// spans are released as prose (`[STEERING ` alone can open real prose).
const SteerAckPrefix = "[STEERING steer-"

// ErrClosed reports an append to a turn whose turn_close is already on disk. The
// between-turns rule sends such an entry to the store, not here.
var ErrClosed = errors.New("turnlog: turn is closed")

// Sink persists a sealed entry: it assigns Seq and Ts, writes the line and
// reports a write failure.
type Sink interface {
	Append(ctx context.Context, e *marotte.Entry) error
}

// Sealed is one entry a step froze, with the steer carry that seal released into it (Delta,
// broadcast as entry_delta before entry_sealed). N is the delta count at the seal; 0 marks
// an entry born sealed, which travels as one entry_appended frame.
type Sealed struct {
	Entry *marotte.Entry
	Delta string
	N     uint64
}

// OpenCall is a tool call between its tool_call entry and its tool_result: the
// value the progress frames fold into and the result seals. StartedTs is the
// tool_call entry's own stamp, so a duration needs no clock in here.
type OpenCall struct {
	Lane      string
	Call      marotte.ToolCall
	StartedTs int64
}

// lane is one agent's state inside a turn: at most one open entry, and the bytes
// withheld from it because they might still grow into a steer marker.
type lane struct {
	open *marotte.OpenEntry
	// say is the say id the lane last opened an entry for and seg its segment count, so a
	// split say's later segments are `<say>#2`, `<say>#3`.
	say string
	// carry is the withheld text and carrySay the say id it came from, so a
	// released carry with no open entry opens an entry under its own say.
	carry    string
	carrySay string
	seg      int
}

// aggregate is the footer the turn_close carries, accumulated as the turn runs.
type aggregate struct {
	changed map[string]*marotte.FileChange
	refusal *marotte.RefusalInfo
	engine  *marotte.EngineError
	model   string
	refs    []marotte.CodeReference
	facts   Facts
	credits float64
	elapsed float64
}

// Turn accumulates one open turn. Safe for concurrent use: the dispatch loop writes, the
// registries read from other goroutines, and mu is held across the Sink append so a
// reader never sees a lane between its seal and the record.
type Turn struct {
	sink  Sink
	lanes map[string]*lane
	// calls holds every unsettled tool call by id with its lane fixed at the create, and
	// callOrder keeps arrival order for a close's aborted results.
	calls     map[string]*OpenCall
	callOrder []string
	// plan is the newest plan entry's payload bytes, for the byte-equal dedupe.
	plan []byte
	id   string
	// order is the lane keys in first-seen order, so a lane-less append seals
	// every lane deterministically.
	order []string
	// issuer is the lane the newest text or thinking delta arrived in, where a subagent
	// invocation is filed: the frame's own stamp names the delegate.
	issuer string
	agg    aggregate
	mu     sync.Mutex
	minted int
	// emitted is whether any content reached a lane. Read after Close, so a carry released
	// by the closing seal counts.
	emitted bool
	closed  bool
}

// Open starts the accumulator for a turn. The turn_open entry (source, n) is the store's,
// appended before this call.
func Open(id string, sink Sink) *Turn {
	return &Turn{
		sink:  sink,
		lanes: make(map[string]*lane),
		calls: make(map[string]*OpenCall),
		id:    id,
	}
}

// ID is the turn id every entry of this turn carries.
func (t *Turn) ID() string { return t.id }

// Closed reports whether the turn_close is on disk.
func (t *Turn) Closed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// Emitted reports whether the turn produced content: a text or thinking entry, or a laned
// entry. Lane-less entries are not content; read it after Close.
func (t *Turn) Emitted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emitted
}

// Open is lane key's still-coalescing entry, a copy, so a delta's caller can tell
// the delta that opened the entry (N is 1) from one that extended it.
func (t *Turn) Open(key string) (marotte.OpenEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if l := t.lanes[key]; l != nil && l.open != nil {
		return *l.open, true
	}
	return marotte.OpenEntry{}, false
}

// CodeReferences is the turn's deduped attribution list so far, a copy.
func (t *Turn) CodeReferences() []marotte.CodeReference {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.agg.refs)
}

// OpenCallFor is the accumulated in-flight value of an unsettled tool call, a
// copy the progress fold writes back through SetOpenCall.
func (t *Turn) OpenCallFor(callID string) (OpenCall, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[callID]
	if !ok {
		return OpenCall{}, false
	}
	return *c, true
}

// HasCallSince reports an unsettled tool call whose tool_call entry was stamped at
// or after since (unix milliseconds), the compaction reap's extension test.
func (t *Turn) HasCallSince(since int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range t.calls {
		if c.StartedTs >= since {
			return true
		}
	}
	return false
}

// SetOpenCall replaces an unsettled call's accumulated value. Lane and start
// stamp are the record's own and never move; a settled or unknown id is ignored.
func (t *Turn) SetOpenCall(callID string, call *marotte.ToolCall) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.calls[callID]; ok {
		c.Call = *call
	}
}

// OpenEntries are copies of the turn's still-coalescing entries, one per lane holding one,
// in lane first-seen order.
func (t *Turn) OpenEntries() []marotte.OpenEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]marotte.OpenEntry, 0, len(t.order))
	for _, key := range t.order {
		if l := t.lanes[key]; l != nil && l.open != nil {
			out = append(out, *l.open)
		}
	}
	return out
}

// IssuerLane is the lane the newest text or thinking delta arrived in ("" before any):
// where a subagent invocation is filed, matching the replay projection's rule.
func (t *Turn) IssuerLane() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.issuer
}

// Carry is the text lane key is currently withholding, which the chunk handler
// passes back into the marker filter with the next delta.
func (t *Turn) Carry(key string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if l := t.lanes[key]; l != nil {
		return l.carry
	}
	return ""
}

// SetSteerCarry replaces what lane key withholds. sayID attributes it, so a carry released
// with nothing open opens an entry under its own say.
func (t *Turn) SetSteerCarry(key, sayID, carry string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.laneOf(key)
	l.carry = carry
	if carry == "" {
		sayID = ""
	}
	l.carrySay = sayID
}

// Meter accumulates the turn's metered footer. Credits and elapsed SUM, because a
// turn can receive several metering frames and each reports its own share.
func (t *Turn) Meter(credits, elapsedMs float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.credits += credits
	t.agg.elapsed += elapsedMs
}

// SetModel latches which model answered the turn; first non-empty write wins, so
// a mid-turn switch does not relabel work already done.
func (t *Turn) SetModel(model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if model != "" && t.agg.model == "" {
		t.agg.model = model
	}
}

// SetRefusal latches the turn's refusal metadata; first write wins.
func (t *Turn) SetRefusal(r *marotte.RefusalInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r != nil && t.agg.refusal == nil {
		t.agg.refusal = r
	}
}

// SetEngineError latches the engine's account of a failed execution; last write
// wins, because a retried attempt and a sub-agent fault write earlier copies.
func (t *Turn) SetEngineError(e marotte.EngineError) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.engine = &e
}

// EngineError is the latched engine account, nil when none arrived.
func (t *Turn) EngineError() *marotte.EngineError {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.agg.engine
}

// SealLane freezes lane key's open entry and settles its steer carry, for a caller that
// must END a lane's prose with nothing to append (a refusal explanation is metadata, not
// assistant text). A no-op on a lane with nothing open or withheld.
func (t *Turn) SealLane(ctx context.Context, key string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	return t.sealLane(ctx, key)
}

// ChangedFile accumulates one path's line delta. Two edits to one file in one
// turn sum, which is the turn's own churn.
func (t *Turn) ChangedFile(path string, added, removed int, isNew bool) {
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.agg.changed == nil {
		t.agg.changed = make(map[string]*marotte.FileChange)
	}
	fc, ok := t.agg.changed[path]
	if !ok {
		fc = &marotte.FileChange{IsNewFile: isNew}
		t.agg.changed[path] = fc
	}
	fc.LinesAdded += added
	fc.LinesRemoved += removed
}

// AddCodeReferences merges licensed-code attributions, deduped whole, because the
// client replaces its own list rather than appending.
func (t *Turn) AddCodeReferences(refs ...marotte.CodeReference) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range refs {
		if !slices.Contains(t.agg.refs, r) {
			t.agg.refs = append(t.agg.refs, r)
		}
	}
}

// TextDelta folds a text delta into lane key: it extends an open text entry under the same
// say, otherwise seals the lane and opens a new one, so a new say id starts a new entry
// (the replay projection's rule).
func (t *Turn) TextDelta(ctx context.Context, key, sayID, delta string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delta(ctx, key, sayID, delta, marotte.EntryKindText)
}

// ThinkingDelta is TextDelta for a reasoning stream. A text entry open in the
// same lane seals: a same-lane entry of the wrong kind is a barrier.
func (t *Turn) ThinkingDelta(ctx context.Context, key, sayID, delta string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delta(ctx, key, sayID, delta, marotte.EntryKindThinking)
}

func (t *Turn) delta(ctx context.Context, key, sayID, delta string, kind marotte.EntryKind) ([]Sealed, error) {
	if t.closed {
		return nil, ErrClosed
	}
	l := t.laneOf(key)
	t.issuer = key
	// An absent say id extends: sealing on every id-less delta would fragment one prose run.
	if l.open != nil && l.open.Kind == kind && (sayID == "" || marotte.SayIDOf(l.open.ID) == sayID) {
		l.open.Text += delta
		l.open.N++
		return nil, nil
	}
	sealed, err := t.sealLane(ctx, key)
	if err != nil {
		return sealed, err
	}
	t.openEntry(l, key, kind, sayID, delta)
	return sealed, nil
}

// ToolCall appends a tool_call in lane key, the lane fixed for the call's whole
// life, and records the call as unsettled so a close can abort it.
func (t *Turn) ToolCall(ctx context.Context, key string, call *marotte.EntryToolCall) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolCall(ctx, key, call)
}

func (t *Turn) toolCall(ctx context.Context, key string, call *marotte.EntryToolCall) ([]Sealed, error) {
	var appended *marotte.Entry
	sealed, err := t.laned(ctx, key, marotte.EntryKindToolCall, call.ID, call, func(e *marotte.Entry) {
		appended = e
	})
	if appended != nil {
		t.trackCall(call.ID, key, call, appended.Ts)
	}
	return sealed, err
}

// Invocation appends a subagent invocation's tool_call in the ISSUER's lane, sealing the
// issuer's open text like an ordinary card; delegateID, the lane it OPENS, goes into the
// payload as the merge's pairing key. issuerLane is the caller's: the frame names the delegate.
func (t *Turn) Invocation(ctx context.Context, issuerLane, delegateID string, call *marotte.EntryToolCall) ([]Sealed, error) {
	stamped := *call
	stamped.AgentSubtaskID = delegateID
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.toolCall(ctx, issuerLane, &stamped)
}

// ToolResult appends a call's settled value in the CALL's lane, whatever lane the
// update carried, and settles the call. A disagreement is logged with both values
// and folds where the call is; there is no late adoption.
func (t *Turn) ToolResult(ctx context.Context, updateLane, callID string, res *marotte.EntryToolResult) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := updateLane
	if c, known := t.calls[callID]; known {
		if updateLane != c.Lane {
			slog.Warn("turnlog: tool_call_update lane disagrees with its call's lane",
				"turn", t.id, "tool_call", callID, "call_lane", c.Lane, "update_lane", updateLane)
		}
		key = c.Lane
	}
	return t.laned(ctx, key, marotte.EntryKindToolResult, marotte.ToolResultID(callID), t.agg.facts.WithInteraction(callID, res), func(*marotte.Entry) {
		t.settleCall(callID)
	})
}

// SteerAck appends the acknowledgement the marker filter stripped out of a chunk,
// in that chunk's own lane.
func (t *Turn) SteerAck(ctx context.Context, key, steerID, text string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	payload := marotte.EntrySteerAck{SteerID: steerID, Text: text}
	return t.laned(ctx, key, marotte.EntryKindSteerAck, marotte.SteerAckID(steerID), payload, nil)
}

// TurnBind appends the id KAS holds the prompt under. Lane-less with no
// exception, so it seals every lane; on the ordinary path nothing is open.
func (t *Turn) TurnBind(ctx context.Context, bind marotte.EntryTurnBind) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laneless(ctx, marotte.EntryKindTurnBind, t.mintID(), bind)
}

// Steer appends a steer, read or dropped. Lane-less because it is the model's
// input for what follows in every lane.
func (t *Turn) Steer(ctx context.Context, steerID string, steer *marotte.EntrySteer) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laneless(ctx, marotte.EntryKindSteer, steerID, steer)
}

// SteerInLane appends a steer whose read one lane acknowledged, in that lane, sealing only
// it: the lane records WHICH agent read the steer.
func (t *Turn) SteerInLane(ctx context.Context, key, steerID string, steer *marotte.EntrySteer) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laned(ctx, key, marotte.EntryKindSteer, steerID, steer, nil)
}

// Plan appends a plan state, sealing nothing when its entries are byte-equal to the newest
// plan entry (the wire resends the whole array on every update).
func (t *Turn) Plan(ctx context.Context, plan marotte.EntryPlan) ([]Sealed, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("turnlog: marshal plan: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plan != nil && bytes.Equal(t.plan, raw) {
		return nil, nil
	}
	sealed, err := t.laneless(ctx, marotte.EntryKindPlan, t.mintID(), plan)
	if err != nil {
		return sealed, err
	}
	t.plan = raw
	return sealed, nil
}

// Compaction appends a completed compaction. The id is derived from the summary
// bytes, so the live entry and its replayed twin carry one id; emptyOrdinal counts
// the log's empty-summary compactions from 1.
func (t *Turn) Compaction(ctx context.Context, summary string, emptyOrdinal int) ([]Sealed, error) {
	id := marotte.CompactionEntryID([]byte(summary), emptyOrdinal)
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laneless(ctx, marotte.EntryKindCompaction, id, marotte.EntryCompaction{Summary: summary})
}

// CompactionFailed appends a compaction that did not complete.
func (t *Turn) CompactionFailed(ctx context.Context, reason string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	payload := marotte.EntryCompactionFailed{Reason: reason}
	return t.laneless(ctx, marotte.EntryKindCompactionFailed, t.mintID(), payload)
}

// SafetyBlocked appends the safety properties an enforce-mode block violated.
func (t *Turn) SafetyBlocked(ctx context.Context, properties []string) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	payload := marotte.EntrySafetyBlocked{Properties: properties}
	return t.laneless(ctx, marotte.EntryKindSafetyBlocked, t.mintID(), payload)
}

// ModelSwitched appends an applied model switch. The switch applies while idle, so it lands
// by the between-turns rule or on a turn with nothing open. Takes the payload so the three
// adjacent strings cannot be transposed.
func (t *Turn) ModelSwitched(ctx context.Context, p marotte.EntryModelSwitched) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laneless(ctx, marotte.EntryKindModelSwitched, t.mintID(), p)
}

// ModeSwitched appends an applied mode switch, with ModelSwitched's position rule. Takes the
// payload so From and To cannot be transposed.
func (t *Turn) ModeSwitched(ctx context.Context, p marotte.EntryModeSwitched) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.laneless(ctx, marotte.EntryKindModeSwitched, t.mintID(), p)
}

// Close ends the turn: every lane's open entry seals, every tool_call with no
// result gets a tool_result{status: aborted} in its own lane, then the turn_close
// carries the aggregate. Idempotent by refusal — a second closer answers
// ErrClosed and writes nothing.
func (t *Turn) Close(ctx context.Context, c marotte.TurnConclusion) ([]Sealed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	sealed, err := t.sealAll(ctx)
	if err != nil {
		return sealed, err
	}
	for _, id := range slices.Clone(t.callOrder) {
		key := t.calls[id].Lane
		res := t.agg.facts.WithInteraction(id, &marotte.EntryToolResult{Status: marotte.ToolAborted})
		s, aerr := t.append(ctx, key, marotte.EntryKindToolResult, marotte.ToolResultID(id), res, "")
		if aerr != nil {
			return sealed, aerr
		}
		t.settleCall(id)
		sealed = append(sealed, s)
	}
	c = c.WithContent(t.emitted)
	footer := marotte.EntryTurnClose{
		ChangedFiles:   t.agg.changed,
		Refusal:        t.agg.refusal,
		Outcome:        c.Outcome,
		StopReasonRaw:  string(c.RawStop),
		FailureReason:  c.Reason,
		FailureKind:    c.FailureKind,
		Model:          t.agg.model,
		CodeReferences: t.agg.refs,
		Credits:        t.agg.credits,
		ElapsedMs:      t.agg.elapsed,
		Truncated:      c.Truncated,
	}
	t.agg.facts.Stamp(&footer)
	if e := t.agg.engine; e != nil {
		footer.EngineErrorClass = EngineClass(e.ErrorType, c.Outcome)
	}
	s, err := t.append(ctx, "", marotte.EntryKindTurnClose, t.mintID(), footer, "")
	if err != nil {
		return sealed, err
	}
	t.closed = true
	return append(sealed, s), nil
}

// laned appends an entry that belongs to one lane, sealing that lane's open entry
// first. after runs with the appended entry once the write landed, so a failed
// write records nothing.
func (t *Turn) laned(ctx context.Context, key string, kind marotte.EntryKind, id string, payload any, after func(*marotte.Entry)) ([]Sealed, error) {
	if t.closed {
		return nil, ErrClosed
	}
	sealed, err := t.sealLane(ctx, key)
	if err != nil {
		return sealed, err
	}
	s, err := t.append(ctx, key, kind, id, payload, "")
	if err != nil {
		return sealed, err
	}
	t.emitted = true
	if after != nil {
		after(s.Entry)
	}
	return append(sealed, s), nil
}

// laneless appends an entry belonging to no lane, sealing EVERY lane first: at the
// moment it takes a seq, no open entry at all is still open.
func (t *Turn) laneless(ctx context.Context, kind marotte.EntryKind, id string, payload any) ([]Sealed, error) {
	if t.closed {
		return nil, ErrClosed
	}
	sealed, err := t.sealAll(ctx)
	if err != nil {
		return sealed, err
	}
	s, err := t.append(ctx, "", kind, id, payload, "")
	if err != nil {
		return sealed, err
	}
	return append(sealed, s), nil
}

// sealAll seals every lane in first-seen order.
func (t *Turn) sealAll(ctx context.Context) ([]Sealed, error) {
	var sealed []Sealed
	for _, key := range slices.Clone(t.order) {
		s, err := t.sealLane(ctx, key)
		sealed = append(sealed, s...)
		if err != nil {
			return sealed, err
		}
	}
	return sealed, nil
}

// sealLane settles lane key's steer carry, freezes its open entry and hands it to
// the sink. A carry released with nothing open opens a text entry and seals it in
// the same step; a carry that is an unclosed marker is dropped.
func (t *Turn) sealLane(ctx context.Context, key string) ([]Sealed, error) {
	l := t.lanes[key]
	if l == nil {
		return nil, nil
	}
	released, releasedSay := l.settleCarry()
	var n uint64
	switch {
	case l.open != nil:
		l.open.Text += released
		if released != "" {
			l.open.N++
		}
		n = l.open.N
	case released != "":
		// Opened and sealed in one step, never announced: N == 0 makes it one entry_appended.
		t.openEntry(l, key, marotte.EntryKindText, releasedSay, released)
		released = ""
	default:
		return nil, nil
	}
	open := l.open
	l.open = nil
	var payload any = marotte.EntryText{Text: open.Text}
	if open.Kind == marotte.EntryKindThinking {
		payload = marotte.EntryThinking{Text: open.Text}
	}
	s, err := t.append(ctx, key, open.Kind, open.ID, payload, released)
	if err != nil {
		return nil, err
	}
	s.N = n
	return []Sealed{s}, nil
}

// settleCarry applies the seal's rule to what the lane withheld and clears it: a
// carry bearing the committing prefix is an unclosed marker and is dropped,
// anything else is prose and becomes the sealed entry's last delta.
func (l *lane) settleCarry() (released, say string) {
	carry, carrySay := l.carry, l.carrySay
	l.carry, l.carrySay = "", ""
	if carry == "" || strings.HasPrefix(carry, SteerAckPrefix) {
		return "", ""
	}
	return carry, carrySay
}

// openEntry starts a lane's open entry. A say id this lane already opened an
// entry for is one a seal split, so its later segments take `<say>#k`; an absent
// id takes a server-minted one, as the projection's own fallback does.
func (t *Turn) openEntry(l *lane, key string, kind marotte.EntryKind, sayID, delta string) {
	id := sayID
	switch sayID {
	case "":
		id = t.mintID()
	case l.say:
		l.seg++
		id = marotte.SaySegmentID(sayID, l.seg)
	default:
		l.say, l.seg = sayID, 1
	}
	l.open = &marotte.OpenEntry{Turn: t.id, ID: id, Lane: key, Kind: kind, Text: delta, N: 1}
	t.emitted = true
}

// append freezes one entry and hands it to the sink, which assigns Seq and Ts.
func (t *Turn) append(ctx context.Context, key string, kind marotte.EntryKind, id string, payload any, delta string) (Sealed, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Sealed{}, fmt.Errorf("turnlog: marshal %s: %w", kind, err)
	}
	e := &marotte.Entry{ID: id, Turn: t.id, Lane: key, Kind: kind, Payload: raw}
	if err := t.sink.Append(ctx, e); err != nil {
		return Sealed{}, err
	}
	return Sealed{Entry: e, Delta: delta}, nil
}

// laneOf returns lane key, recording its first-seen position so a lane-less seal
// has a deterministic order.
func (t *Turn) laneOf(key string) *lane {
	l, ok := t.lanes[key]
	if ok {
		return l
	}
	l = &lane{}
	t.lanes[key] = l
	t.order = append(t.order, key)
	return l
}

func (t *Turn) trackCall(id, key string, call *marotte.EntryToolCall, ts int64) {
	if _, dup := t.calls[id]; dup {
		return
	}
	t.calls[id] = &OpenCall{Call: marotte.ToolCallOfEntry(call), Lane: key, StartedTs: ts}
	t.callOrder = append(t.callOrder, id)
}

func (t *Turn) settleCall(id string) {
	delete(t.calls, id)
	t.callOrder = slices.DeleteFunc(t.callOrder, func(s string) bool { return s == id })
}

// mintID is the id for an entry no side of the merge pairs by id. Derived from the turn id
// and a counter, so it is unique per log and stable for a fixture.
func (t *Turn) mintID() string {
	t.minted++
	return t.id + ":e" + strconv.Itoa(t.minted)
}
