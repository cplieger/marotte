package agent

// A run read's revision: the ask cell's newest read, as the run read serves it, and the run log's
// step ends and starts, taken apart into slots and diffed on every read. A changed revision is
// published as run_rev; GET /api/runs/{id}?since=<rev> answers the slots that changed since, and
// ?view=header the run's head with no node tree, both from the held read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workflow"
)

// revRing is how many revisions a run keeps the changes of. A client further behind reads the
// run whole: at one revision per settled frame burst that is minutes of a busy run.
const revRing = 64

// headBulk are the state members a run's head leaves out: they grow with the run or are fixed at
// launch, and no surface that reads the head or applies a revision draws them. The whole read
// still carries them.
var headBulk = map[string]bool{
	"root": true, "capturedOutputs": true, "originalUserMessages": true, "inputs": true, "artifacts": true,
}

type slotKind uint8

const (
	slotHead slotKind = iota
	slotNode
	slotEnd
	slotStart
	slotPlan
)

// revSlot is one diffed unit: a head member by name, a node, step end or step start by PathKey,
// or the node plan.
type revSlot struct {
	key  string
	kind slotKind
}

// revChange is the slots one revision changed, from prev to rev.
type revChange struct {
	slots []revSlot
	prev  uint64
	rev   uint64
}

// runRevision is one cell's revision state. mu serializes revise and every read of it.
type runRevision struct {
	values map[revSlot][]byte
	nodes  map[string]workflow.IndexedNode
	ends   map[string]marotte.RunStepEnd
	starts map[string]marotte.RunStepStart
	turns  turnsRead
	served json.RawMessage
	head   []workflow.Member
	ring   []revChange
	rev    uint64
	mu     sync.Mutex
}

// revSnapshot is what a whole read serves of a revision: the read as served, and the extras
// diffed with it. ok is false when no revision describes the read.
type revSnapshot struct {
	ends   map[string]marotte.RunStepEnd
	starts map[string]marotte.RunStepStart
	served json.RawMessage
	rev    uint64
	ok     bool
}

// freshRead is one inspect read as refreshCell made it, with the run log's turn count taken
// before it (positionedRead's order).
type freshRead struct {
	raw   json.RawMessage
	turns turnsRead
	in    workflow.Inspect
}

type turnsRead struct {
	err error
	n   uint64
}

func (rs *Runs) turnsBefore(ctx context.Context, workflowID string) turnsRead {
	if rs.log == nil {
		return turnsRead{}
	}
	n, err := rs.log.turnHighWater(ctx, workflowID)
	return turnsRead{n: n, err: err}
}

// revMint mints run revisions, monotonic across runs.
type revMint struct {
	last atomic.Uint64
	once sync.Once
}

// mint is the next revision of any run. The first is the boot's wall clock in milliseconds times
// 1024, so a revision a client holds from an earlier process is never one this process mints, and
// every revision stays a safe JSON integer until the year 2248.
func (m *revMint) mint() uint64 {
	m.once.Do(func() { m.last.Store(uint64(time.Now().UnixMilli()) << 10) })
	return m.last.Add(1)
}

// revise moves c's revision to fresh (nil re-reads the run log's extras alone) and publishes
// run_rev when anything moved. A read it cannot index drops the revision, so every client reads
// the run whole until the next read indexes.
func (rs *Runs) revise(ctx context.Context, c *askCell, fresh *freshRead) revSnapshot {
	if c.transient {
		return revSnapshot{}
	}
	rv := &c.revision
	rv.mu.Lock()
	defer rv.mu.Unlock()
	next, ix, served, err := rv.readSlotsLocked(c.workflowID, fresh)
	switch {
	case err != nil:
		slog.Warn("run revision: the read could not be indexed; clients read it whole",
			"workflow_id", logsafe.Field(c.workflowID), "error", err)
		rv.resetLocked()
		return revSnapshot{served: served}
	case next == nil:
		return revSnapshot{}
	}
	nodes, head, in := rv.nodes, rv.head, c.heldInspect()
	if fresh != nil {
		rv.served, rv.turns, in = served, fresh.turns, fresh.in
		nodes, head = ix.Nodes, ix.Head
	}
	ends, starts := rs.revisedExtras(ctx, c.workflowID, in, rv, fresh != nil)
	extraSlots(next, ends, starts)
	first := rv.rev == 0
	changed := changedSlots(rv.values, next, rv.nodes, nodes)
	rv.values, rv.nodes, rv.head, rv.ends, rv.starts = next, nodes, head, ends, starts
	if !first && len(changed) == 0 {
		return rv.snapshotLocked()
	}
	prev := rv.rev
	rv.rev = rs.revs.mint()
	if !first {
		rv.ring = append(rv.ring, revChange{prev: prev, rev: rv.rev, slots: changed})
		if len(rv.ring) > revRing {
			rv.ring = slices.Delete(rv.ring, 0, len(rv.ring)-revRing)
		}
	}
	if !c.isEnded() {
		rs.broadcastAsk(ctx, marotte.NewEvent(marotte.EventRunRev, c.chatID, marotte.RunRevPayload{
			WorkflowID: c.workflowID, Rev: rv.rev, Status: rv.statusLocked(),
		}))
	}
	return rv.snapshotLocked()
}

// readSlotsLocked is the head, node and plan slots of fresh as the run read serves it (with the
// served bytes and their index), or of the held revision for a nil fresh; nil slots when nothing
// is held.
func (rv *runRevision) readSlotsLocked(workflowID string, fresh *freshRead) (
	slots map[revSlot][]byte, ix workflow.ReadIndex, served json.RawMessage, err error,
) {
	if fresh == nil {
		if rv.rev == 0 {
			return nil, ix, nil, nil
		}
		slots = map[revSlot][]byte{}
		for s, v := range rv.values {
			if s.kind == slotHead || s.kind == slotNode || s.kind == slotPlan {
				slots[s] = v
			}
		}
		return slots, ix, nil, nil
	}
	served = summarizedOrWhole(workflowID, fresh.raw)
	if ix, err = workflow.IndexRead(served); err != nil {
		return nil, ix, served, err
	}
	slots = map[revSlot][]byte{}
	readSlots(slots, &ix)
	return slots, ix, served, nil
}

// resetLocked drops every revision: an answer from the held read then reads whole.
func (rv *runRevision) resetLocked() {
	rv.values, rv.nodes, rv.ends, rv.starts, rv.served, rv.head, rv.ring = nil, nil, nil, nil, nil, nil, nil
	rv.turns, rv.rev = turnsRead{}, 0
}

func (c *askCell) heldInspect() workflow.Inspect {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reply
}

func (c *askCell) isEnded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ended
}

// revisedExtras reads the run log's step ends and starts against in, recording the grades due
// under it only when in is a fresh read (grade); a failed read keeps the revision's own.
func (rs *Runs) revisedExtras(ctx context.Context, workflowID string, in workflow.Inspect, rv *runRevision, grade bool) (
	ends map[string]marotte.RunStepEnd, starts map[string]marotte.RunStepStart,
) {
	if rs.log == nil {
		return nil, nil
	}
	ends, starts = rv.ends, rv.starts
	read := rs.log.recordedStepEnds
	if grade {
		read = rs.stepEnds
	}
	if e, err := read(ctx, workflowID, kasSteps(in)); err == nil {
		ends = e
	} else {
		slog.Warn("run revision: the run log's step ends could not be read", "workflow_id", logsafe.Field(workflowID),
			"error", err)
	}
	if s, err := rs.log.stepStarts(ctx, workflowID); err == nil {
		starts = s
	} else {
		slog.Warn("run revision: the run log's step starts could not be read", "workflow_id", logsafe.Field(workflowID),
			"error", err)
	}
	return ends, starts
}

func readSlots(into map[revSlot][]byte, ix *workflow.ReadIndex) {
	for _, m := range ix.Head {
		if !headBulk[m.Name] {
			into[revSlot{kind: slotHead, key: m.Name}] = m.Value
		}
	}
	for key, n := range ix.Nodes {
		into[revSlot{kind: slotNode, key: key}] = n.Own
	}
	if ix.Plan != nil {
		into[revSlot{kind: slotPlan}] = ix.Plan
	}
}

func extraSlots(into map[revSlot][]byte, ends map[string]marotte.RunStepEnd, starts map[string]marotte.RunStepStart) {
	for path, e := range ends {
		if b, err := json.Marshal(e); err == nil {
			into[revSlot{kind: slotEnd, key: path}] = b
		}
	}
	for path, s := range starts {
		if b, err := json.Marshal(s); err == nil {
			into[revSlot{kind: slotStart, key: path}] = b
		}
	}
}

// changedSlots is every slot that differs between was and now. A removed node also changes its
// parent, and a node whose start moved changes with it, so a client walking the changed nodes
// down from the root reaches both.
func changedSlots(was, now map[revSlot][]byte, wasNodes, nowNodes map[string]workflow.IndexedNode) []revSlot {
	set := map[revSlot]bool{}
	for s, v := range now {
		if old, ok := was[s]; !ok || !bytes.Equal(old, v) {
			set[s] = true
		}
	}
	for s := range was {
		if _, ok := now[s]; ok {
			continue
		}
		set[s] = true
		if n, ok := wasNodes[s.key]; s.kind == slotNode && ok && n.Parent != "" {
			set[revSlot{kind: slotNode, key: n.Parent}] = true
		}
	}
	var moved []string
	for s := range set {
		if _, ok := nowNodes[s.key]; s.kind == slotStart && ok {
			moved = append(moved, s.key)
		}
	}
	for _, key := range moved {
		set[revSlot{kind: slotNode, key: key}] = true
	}
	return slices.Collect(maps.Keys(set))
}

func (rv *runRevision) snapshotLocked() revSnapshot {
	return revSnapshot{ends: rv.ends, starts: rv.starts, served: rv.served, rev: rv.rev, ok: rv.rev != 0}
}

func (rv *runRevision) statusLocked() string {
	var status string
	if v, ok := rv.values[revSlot{kind: slotHead, key: "status"}]; ok {
		_ = json.Unmarshal(v, &status)
	}
	return status
}

// changesSinceLocked is the union of the slots changed after from; false when from is not a
// revision this run still holds the changes of.
func (rv *runRevision) changesSinceLocked(from uint64) (map[revSlot]bool, bool) {
	out := map[revSlot]bool{}
	if from == rv.rev {
		return out, true
	}
	i := slices.IndexFunc(rv.ring, func(ch revChange) bool { return ch.prev == from })
	if i < 0 {
		return nil, false
	}
	for _, ch := range rv.ring[i:] {
		for _, s := range ch.slots {
			out[s] = true
		}
	}
	return out, true
}

// headDocLocked is `{"workflowId":…,"state":{…}}` over the head's members, which headOf reads.
func (rv *runRevision) headDocLocked(workflowID string) json.RawMessage {
	var b bytes.Buffer
	b.WriteString(`{"workflowId":`)
	id, _ := json.Marshal(workflowID)
	b.Write(id)
	b.WriteString(`,"state":{`)
	sep := false
	for _, m := range rv.head {
		if headBulk[m.Name] {
			continue
		}
		if sep {
			b.WriteByte(',')
		}
		sep = true
		writeMember(&b, m.Name, m.Value)
	}
	b.WriteString(`}}`)
	return b.Bytes()
}

func writeMember(b *bytes.Buffer, name string, v json.RawMessage) {
	k, _ := json.Marshal(name)
	b.Write(k)
	b.WriteByte(':')
	b.Write(v)
}

// sinceNode is one changed node of a ?since answer: its own members and where it sits.
type sinceNode struct {
	Path   string          `json:"path"`
	Parent string          `json:"parent"`
	Node   json.RawMessage `json:"node"`
	Index  int             `json:"index"`
}

// sinceBody is a ?since answer before marotte's extras: `head` holds the changed head members,
// `nodes` every changed node with each of its ancestors, the `*removed` lists what is gone, and
// `nodePlan` is present only when it changed.
type sinceBody struct {
	Head          json.RawMessage `json:"head"`
	NodePlan      json.RawMessage `json:"nodePlan,omitempty"`
	WorkflowID    string          `json:"workflowId"`
	HeadRemoved   []string        `json:"head_removed"`
	Nodes         []sinceNode     `json:"nodes"`
	Removed       []string        `json:"removed"`
	EndsRemoved   []string        `json:"step_ends_removed"`
	StartsRemoved []string        `json:"step_starts_removed"`
}

// sinceRead answers GET /api/runs/{id}?since=<from> from the cell's held read, after re-reading
// the run log's extras; false when the run has no revision holding from, which a whole read
// answers.
func (rs *Runs) sinceRead(ctx context.Context, workflowID string, from uint64) (out json.RawMessage, ok bool, err error) {
	c := rs.asks.get(workflowID)
	if c == nil || c.transient {
		return nil, false, nil
	}
	rs.revise(ctx, c, nil)
	rv := &c.revision
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.rev == 0 {
		return nil, false, nil
	}
	changed, held := rv.changesSinceLocked(from)
	if !held {
		return nil, false, nil
	}
	a := newSinceAnswer(workflowID)
	for s := range changed {
		a.add(rv, s)
	}
	raw, err := json.Marshal(a.bodyOf(rv))
	if err != nil {
		return nil, false, err
	}
	x := rs.heldExtras(c, rv)
	x.ends, x.starts = a.ends, a.starts
	out, err = withRunExtras(raw, x)
	return out, err == nil, err
}

// sinceAnswer gathers a ?since answer slot by slot.
type sinceAnswer struct {
	ends   map[string]marotte.RunStepEnd
	starts map[string]marotte.RunStepStart
	nodes  map[string]bool
	body   sinceBody
	head   bytes.Buffer
}

func newSinceAnswer(workflowID string) *sinceAnswer {
	a := &sinceAnswer{
		ends: map[string]marotte.RunStepEnd{}, starts: map[string]marotte.RunStepStart{}, nodes: map[string]bool{},
		body: sinceBody{
			WorkflowID: workflowID, HeadRemoved: []string{}, Nodes: []sinceNode{}, Removed: []string{},
			EndsRemoved: []string{}, StartsRemoved: []string{},
		},
	}
	a.head.WriteByte('{')
	return a
}

// add answers changed slot s with its value in rv, or as removed; a node brings its ancestors.
func (a *sinceAnswer) add(rv *runRevision, s revSlot) {
	switch s.kind {
	case slotHead:
		v, ok := rv.values[s]
		if !ok {
			a.body.HeadRemoved = append(a.body.HeadRemoved, s.key)
			return
		}
		if a.head.Len() > 1 {
			a.head.WriteByte(',')
		}
		writeMember(&a.head, s.key, v)
	case slotNode:
		for key := s.key; key != "" && !a.nodes[key]; key = rv.nodes[key].Parent {
			a.nodes[key] = true
		}
	case slotEnd:
		if e, ok := rv.ends[s.key]; ok {
			a.ends[s.key] = e
		} else {
			a.body.EndsRemoved = append(a.body.EndsRemoved, s.key)
		}
	case slotStart:
		if st, ok := rv.starts[s.key]; ok {
			a.starts[s.key] = st
		} else {
			a.body.StartsRemoved = append(a.body.StartsRemoved, s.key)
		}
	case slotPlan:
		a.body.NodePlan = cmpOrNull(rv.values[s])
	}
}

// bodyOf closes the answer: each gathered node as its record, or removed.
func (a *sinceAnswer) bodyOf(rv *runRevision) *sinceBody {
	a.head.WriteByte('}')
	a.body.Head = a.head.Bytes()
	for _, key := range slices.Sorted(maps.Keys(a.nodes)) {
		if n, ok := rv.nodes[key]; ok {
			a.body.Nodes = append(a.body.Nodes, sinceNode{Path: key, Parent: n.Parent, Index: n.Index, Node: n.Own})
		} else {
			a.body.Removed = append(a.body.Removed, key)
		}
	}
	return &a.body
}

func cmpOrNull(v []byte) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	return v
}

// heldExtras is marotte's keys for an answer from the held read, every one but the step ends and
// starts. The caller holds rv.mu.
func (rs *Runs) heldExtras(c *askCell, rv *runRevision) *runExtras {
	id := c.workflowID
	rs.asks.mu.Lock()
	asks, reachable := c.last.Asks, c.last.Reachable
	rs.asks.mu.Unlock()
	pending := -1
	if in := c.heldInspect(); in.OK {
		pending = in.Pending
	}
	x := &runExtras{
		asks: asks, asksReachable: reachable, plan: rs.servedPlan(id, pending), displayName: rs.runName(id), rev: rv.rev,
	}
	if l, held := rs.lease(id); held {
		x.autoResumeStoppedAt = l.AutoResumeStoppedAt
	}
	head, err := headOf(rv.headDocLocked(id))
	if err == nil && head.Status != "" {
		ctl := rs.controls(id, &head)
		x.controls = &ctl
	}
	if err == nil && rv.turns.err == nil {
		x.readPosition = runReadPosition(marotte.RunStatus(head.Status), rv.turns.n)
	} else {
		slog.Warn("workflow inspect: the run's read position could not be computed", "workflow_id", logsafe.Field(id),
			"error", errors.Join(err, rv.turns.err))
	}
	return x
}

// headerRead answers GET /api/runs/{id}?view=header: the run's head, its asks, controls and read
// position, with no node tree, plan, step ends or step starts, from the held read (one engine
// read when the run's cell holds none). A run with no cell is read afresh.
func (rs *Runs) headerRead(ctx context.Context, workflowID string) (json.RawMessage, error) {
	c := rs.cellFor(workflowID)
	if c == nil {
		return rs.unheldHeader(ctx, workflowID)
	}
	c.revision.mu.Lock()
	cold := c.revision.rev == 0
	c.revision.mu.Unlock()
	if cold {
		if _, err := rs.readAsks(ctx, c); err != nil {
			return nil, err
		}
	}
	rv := &c.revision
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.rev == 0 {
		return rs.unheldHeader(ctx, workflowID)
	}
	x := rs.heldExtras(c, rv)
	x.noLog = true
	return withRunExtras(rv.headDocLocked(workflowID), x)
}

// unheldHeader is the header view of a run with no revision, from one engine read.
func (rs *Runs) unheldHeader(ctx context.Context, workflowID string) (json.RawMessage, error) {
	turns := rs.turnsBefore(ctx, workflowID)
	raw, err := rs.rawInspect(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	ix, err := workflow.IndexRead(raw)
	if err != nil {
		return nil, err
	}
	rv := runRevision{head: ix.Head, turns: turns}
	doc := rv.headDocLocked(workflowID)
	head, err := headOf(doc)
	if err != nil {
		return nil, err
	}
	x := &runExtras{
		asks: []marotte.RunAsk{}, asksReachable: marotte.RunStatus(head.Status).Terminal(),
		displayName: rs.runName(workflowID), noLog: true,
	}
	if head.Status != "" {
		ctl := rs.controls(workflowID, &head)
		x.controls = &ctl
	}
	if turns.err == nil {
		x.readPosition = runReadPosition(marotte.RunStatus(head.Status), turns.n)
	}
	return withRunExtras(doc, x)
}

// heldRaw is the run's newest inspect reply the ask cell holds, nil when it holds none it could read.
func (rs *Runs) heldRaw(workflowID string) json.RawMessage {
	c := rs.asks.get(workflowID)
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readFailed {
		return nil
	}
	return c.raw
}

// serveRunView answers the run read's ?since and ?view forms; false leaves the request to the
// whole read.
func (rr *runRoutes) serveRunView(w http.ResponseWriter, r *http.Request, id string) bool {
	q := r.URL.Query()
	if q.Get("view") == "header" {
		out, err := rr.runs.headerRead(r.Context(), id)
		if err != nil {
			writeInspectErr(w, id, err)
			return true
		}
		httpreply.WriteRawJSON(w, out)
		return true
	}
	if !q.Has("since") {
		return false
	}
	from, err := strconv.ParseUint(q.Get("since"), 10, 64)
	if err != nil {
		httpreply.BadRequest(w, "invalid since revision")
		return true
	}
	out, ok, err := rr.runs.sinceRead(r.Context(), id, from)
	if err != nil {
		slog.Warn("workflow inspect: a revision's changes could not be written; answering whole",
			"workflow_id", logsafe.Field(id), "error", err)
	}
	if !ok {
		return false
	}
	httpreply.WriteRawJSON(w, out)
	return true
}

// noteRunEvent is the bus's observer. A run's lifecycle frame moves KAS's state, so it schedules
// the run's cell a read; a step turn's open or close or a recorded grade moves only the run log's
// extras, which it revises with no engine read. Off the emitter's goroutine, since emitters may
// hold the cell's locks.
func (rs *Runs) noteRunEvent(evt *marotte.ServerEvent) {
	id, engine := runEventSubject(evt)
	if id == "" || rs.lifecycle == nil {
		return
	}
	rs.lifecycle.goUnlessDraining(func() {
		c := rs.asks.get(id)
		switch {
		case c == nil:
		case engine:
			rs.kickCell(c)
		default:
			ctx, cancel := rs.lifecycle.derivedContext()
			defer cancel()
			rs.revise(ctx, c, nil)
		}
	})
}

// runEventSubject is the run evt moves the run read of ("" for any other event), and whether it
// moved KAS's state rather than the run log's.
func runEventSubject(evt *marotte.ServerEvent) (string, bool) {
	switch p := evt.Payload.(type) {
	case marotte.RunProgressPayload:
		return p.WorkflowID, true
	case marotte.RunStartedPayload:
		return p.WorkflowID, true
	case marotte.RunFinishedPayload:
		return p.WorkflowID, true
	case marotte.TurnOpenedPayload:
		return p.WorkflowID, false
	case marotte.TurnClosedPayload:
		return p.WorkflowID, false
	case marotte.EntryAppendedPayload:
		if p.Entry.Kind == marotte.EntryKindStepGraded {
			return p.WorkflowID, false
		}
	}
	return "", false
}
