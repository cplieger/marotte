package agent

// The ask core: which steps of a run wait on a person. Never a registry: each snapshot, published
// whole as `run_asks`, holds workflow.OpenAsks of the newest `_kiro/workflow/inspect` reply decoded
// from the session hosting the run, with what marotte itself saw and did on the run; it holds none
// before the first read, once KAS no longer knows the run, or after the lease ends. One cell per
// leased run, living exactly as long as the lease; it also serializes marotte's own verbs on the
// run, so a stop never overtakes an answer marotte already wrote.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/marotte/internal/translate"
	"github.com/cplieger/marotte/internal/workflow"
)

// scrubLog strips CR and LF from a wire value before logging (CWE-117).
func scrubLog(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}

// settleQuiet is how long a live-looking run with nothing moving may read `settling` before it
// reads `unsettled`: five times the longest admitted-answer-to-running window measured on
// kiro-cli 2.29.1 (1,011 ms over 20 samples). A var for tests.
var settleQuiet = 5 * time.Second

// Vars for tests. askRequery re-reads a run that holds an ask or a pending send; askPendingCeiling
// is about ten times settleQuiet's measured window, after which an admitted answer whose turn never started
// stops blocking the card; askStopCeiling is the browser's request budget; askVerbCeiling bounds
// resume, retry and pause behind another verb's check.
var (
	askRequery        = 60 * time.Second
	askPendingCeiling = 10 * time.Second
	askStopCeiling    = clientRequestBudget
	askVerbCeiling    = 5 * time.Second
)

// runAsks holds one cell per leased run. mu guards cells, transients, seq and every cell's last; a
// cell's own mu is always taken before it.
type runAsks struct {
	cells map[string]*askCell
	// transients serialize the step messages on a run with no lease, counted by the requests holding each.
	transients map[string]*transientCell
	// versions holds the shared `pending` counter every publish bumps under mu (mintPending).
	versions *subject.Versions
	epoch    func() string
	// seq stamps every snapshot, monotonic across cells, so a cell rebuilt for the same run never
	// re-uses a number a client already holds.
	seq uint64
	mu  sync.Mutex
}

type pendingPhase int

const (
	pendingChecking pendingPhase = iota
	pendingSending
	pendingReplied
)

// pendingPrompt is marotte's one unsettled prompt on a run. done closes when it clears.
type pendingPrompt struct {
	done        chan struct{}
	ceiling     *time.Timer
	session     string
	writeMarker string
	phase       pendingPhase
}

// stepMarks is one step session's turn record this boot.
type stepMarks struct {
	marker  string
	asked   string
	warned  string
	endedAt string
	// said is the step's last words from the run's record, read once per marker.
	said       string
	saidMarker string
	open       bool
	saidRead   bool
}

type askCell struct {
	steps    map[string]*stepMarks
	pending  *pendingPrompt
	notified map[string]string // position -> the ask id last notified there
	requery  *time.Timer
	quiet    *time.Timer
	// serial is the per-run serializer, a cancellable mutex; query admits one inspect at a time.
	serial     chan struct{}
	query      chan struct{}
	workflowID string
	chatID     marotte.ChatID
	lastMotion time.Time
	reply      workflow.Inspect
	// basis is the turn record reply is paired with, taken before reply's inspect was sent.
	basis    askBasis
	last     marotte.RunAsksPayload
	turns    uint64
	mu       sync.Mutex
	dirty    bool
	stopping bool
	ended    bool
	// transient is the cell of a run with no lease: it serializes step messages and publishes nothing.
	transient   bool
	decodeWarns bool
}

func newAskCell(workflowID string, chatID marotte.ChatID) *askCell {
	return &askCell{
		workflowID: workflowID,
		chatID:     chatID,
		steps:      map[string]*stepMarks{},
		notified:   map[string]string{},
		serial:     make(chan struct{}, 1),
		query:      make(chan struct{}, 1),
		lastMotion: time.Now(),
	}
}

func (a *runAsks) epochNow() string {
	if a.epoch == nil {
		return ""
	}
	return a.epoch()
}

// stampLocked sets c's snapshot under a fresh seq. The caller holds a.mu.
func (a *runAsks) stampLocked(c *askCell, asks []marotte.RunAsk, reachable bool) {
	if asks == nil {
		asks = []marotte.RunAsk{}
	}
	a.seq++
	c.last = marotte.RunAsksPayload{
		WorkflowID: c.workflowID, ChatID: c.chatID, Epoch: a.epochNow(),
		Asks: asks, Seq: a.seq, Reachable: reachable,
	}
	mintPending(&a.versions)
}

func (a *runAsks) get(workflowID string) *askCell {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cells[workflowID]
}

func askChat(l *runlease.Lease) marotte.ChatID {
	if l.ChatID != "" {
		return marotte.ChatID(l.ChatID)
	}
	return runChatID(l.WorkflowID)
}

// seed gives every lease a cell that has not read its run: such a run is named, unreachable, so a
// reconnecting client keeps its banners until the run is read.
func (a *runAsks) seed(leases []runlease.Lease) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range leases {
		a.insertLocked(&leases[i])
	}
}

func (a *runAsks) insertLocked(l *runlease.Lease) *askCell {
	if a.cells == nil {
		a.cells = map[string]*askCell{}
	}
	c := newAskCell(l.WorkflowID, askChat(l))
	a.stampLocked(c, nil, false)
	a.cells[l.WorkflowID] = c
	return c
}

// cellFor answers the run's cell, creating it while the run is leased. The lease is checked under
// a.mu, which end takes after the lease store's release, so no cell outlives its lease.
func (rs *Runs) cellFor(workflowID string) *askCell {
	if workflowID == "" {
		return nil
	}
	a := &rs.asks
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.cells[workflowID]; c != nil {
		return c
	}
	l, held := rs.lease(workflowID)
	if !held {
		return nil
	}
	return a.insertLocked(&l)
}

type transientCell struct {
	c *askCell
	n int
}

// verbCell is the run's cell, else, for a run with no lease, a transient one the step messages to
// that run share until the last calls done. No other verb takes a transient, and it publishes nothing.
func (rs *Runs) verbCell(workflowID string) (c *askCell, done func()) {
	if c := rs.cellFor(workflowID); c != nil {
		return c, func() {}
	}
	a := &rs.asks
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transients == nil {
		a.transients = map[string]*transientCell{}
	}
	tc := a.transients[workflowID]
	if tc == nil {
		tc = &transientCell{c: newAskCell(workflowID, runChatID(workflowID))}
		tc.c.transient = true
		a.transients[workflowID] = tc
	}
	tc.n++
	return tc.c, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		tc.n--
		if tc.n == 0 && a.transients[workflowID] == tc {
			delete(a.transients, workflowID)
		}
	}
}

// list is one run_asks envelope per run marotte answers for: absence from a pending_snapshot means
// the run has ended or was never marotte's.
func (a *runAsks) list() []marotte.ServerEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := slices.Sorted(maps.Keys(a.cells))
	out := make([]marotte.ServerEvent, 0, len(ids))
	for _, id := range ids {
		c := a.cells[id]
		out = append(out, marotte.NewEvent(marotte.EventRunAsks, c.chatID, c.last))
	}
	return out
}

// endAsks retires the run's cell once its lease is released: the release itself says the run waits
// on nobody, so no read is needed. Every surface gets the empty snapshot, every banner's push is
// retracted, and a stop waiting on a pending answer is let go.
func (rs *Runs) endAsks(ctx context.Context, workflowID string) {
	a := &rs.asks
	c := a.get(workflowID)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ended = true
	c.stopping = false
	c.clearPendingLocked()
	stopTimer(c.requery)
	stopTimer(c.quiet)
	positions := slices.Collect(maps.Keys(c.notified))
	clear(c.notified)
	a.mu.Lock()
	if a.cells[workflowID] == c {
		delete(a.cells, workflowID)
	}
	a.stampLocked(c, nil, true)
	evt := marotte.NewEvent(marotte.EventRunAsks, c.chatID, c.last)
	a.mu.Unlock()
	rs.broadcastAsk(ctx, evt)
	c.mu.Unlock()
	rs.retractAsks(positions)
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

func (rs *Runs) broadcastAsk(ctx context.Context, evt marotte.ServerEvent) {
	if rs.bus != nil {
		rs.bus.Broadcast(ctx, evt)
	}
}

func (rs *Runs) retractAsks(positions []string) {
	if rs.coord == nil {
		return
	}
	for _, p := range positions {
		rs.coord.retractPush(marotte.RunAskSubject(p))
	}
}

// askBasis is the cell's turn record as it stood before an inspect was sent. A reply is paired
// with the basis taken before it, never a newer one: a turn_start landing while the inspect is in
// flight would otherwise publish the old wait under the next turn's marker.
type askBasis struct {
	steps  map[string]workflow.StepLog
	motion time.Time
}

// basisLocked snapshots the turn record. The caller holds c.mu.
func (c *askCell) basisLocked() askBasis {
	out := make(map[string]workflow.StepLog, len(c.steps))
	for id, m := range c.steps {
		out[id] = workflow.StepLog{Marker: m.marker, Asked: m.asked, Said: m.said, EndedAt: m.endedAt, Open: m.open}
	}
	return askBasis{steps: out, motion: c.lastMotion}
}

// runLogLocked is OpenAsks's second input: b, plus marotte's own requests as they stand now. The
// caller holds c.mu.
func (c *askCell) runLogLocked(b askBasis, epoch string) workflow.RunLog {
	return workflow.RunLog{
		Steps: b.steps, Epoch: epoch,
		Stopping: c.stopping, Pending: c.pending != nil,
		Quiet: time.Since(b.motion) >= settleQuiet,
	}
}

func wireAsks(asks []workflow.Ask) []marotte.RunAsk {
	out := make([]marotte.RunAsk, 0, len(asks))
	for i := range asks {
		a := &asks[i]
		out = append(out, marotte.RunAsk{
			AskID: a.ID, NodeID: a.NodeID, NodePath: a.NodePath, StepSessionID: a.StepSessionID,
			AgentName: a.AgentName, Text: a.Text, TextSource: a.TextSource, Reason: a.Reason,
			Blocked: a.Blocked(), AskedAt: a.AskedAt,
		})
	}
	return out
}

func sameAsks(x, y []marotte.RunAsk) bool {
	return slices.EqualFunc(x, y, func(p, q marotte.RunAsk) bool {
		return p.AskID == q.AskID && p.Text == q.Text && p.TextSource == q.TextSource && p.Reason == q.Reason &&
			p.Blocked == q.Blocked && p.AskedAt == q.AskedAt &&
			p.AgentName == q.AgentName && slices.Equal(p.NodePath, q.NodePath)
	})
}

// askNotice is one notification or retraction a publish owes, sent once the cell's lock is dropped.
type askNotice struct {
	raise   []marotte.RunAsk
	retract []string
}

// publishLocked stamps and broadcasts asks when they or reachable changed, and returns what the
// change owes the notification channels. The caller holds c.mu.
func (rs *Runs) publishLocked(ctx context.Context, c *askCell, asks []marotte.RunAsk, reachable bool) askNotice {
	if c.ended || c.transient {
		return askNotice{}
	}
	a := &rs.asks
	a.mu.Lock()
	if reachable == c.last.Reachable && sameAsks(asks, c.last.Asks) {
		a.mu.Unlock()
		return askNotice{}
	}
	a.stampLocked(c, asks, reachable)
	evt := marotte.NewEvent(marotte.EventRunAsks, c.chatID, c.last)
	a.mu.Unlock()
	rs.broadcastAsk(ctx, evt)
	return c.noticesLocked(asks)
}

// noticesLocked raises each ask id once per boot, keyed by its position so a re-park replaces its
// banner, and retracts the positions that stopped waiting.
func (c *askCell) noticesLocked(asks []marotte.RunAsk) askNotice {
	var out askNotice
	live := make(map[string]bool, len(asks))
	for i := range asks {
		pos := askPosition(c.workflowID, &asks[i])
		live[pos] = true
		if c.notified[pos] != asks[i].AskID {
			c.notified[pos] = asks[i].AskID
			out.raise = append(out.raise, asks[i])
		}
	}
	for pos := range c.notified {
		if !live[pos] {
			delete(c.notified, pos)
			out.retract = append(out.retract, pos)
		}
	}
	return out
}

func askPosition(workflowID string, a *marotte.RunAsk) string {
	return workflow.AskPosition(workflowID, workflow.StepSession{Path: a.NodePath, SessionID: a.StepSessionID})
}

// unseenAskText is a notification's body when marotte did not see what the step said.
const unseenAskText = "marotte did not see what this step said"

func (rs *Runs) sendNotices(ctx context.Context, c *askCell, n askNotice) {
	rs.retractAsks(n.retract)
	if rs.coord == nil {
		return
	}
	for i := range n.raise {
		a := &n.raise[i]
		p := notice.Question(rs.coord.NoticeTarget(ctx, c.chatID, c.workflowID),
			cmp.Or(a.AgentName, a.NodeID), cmp.Or(a.Text, unseenAskText))
		// One tag per wait: a re-park replaces its own banner, a sibling's is its own.
		p.PushSubject = marotte.RunAskSubject(askPosition(c.workflowID, a))
		slog.Info("a workflow step is waiting for an answer", "workflow_id", scrubLog(c.workflowID),
			"node_id", scrubLog(a.NodeID), "text_source", string(a.TextSource), "chat_id", c.chatID)
		rs.coord.Notify(ctx, c.chatID, &p)
	}
}

// republishLocked recomputes the asks from the newest reply with the cell's current request state,
// for a change marotte made itself (a pending send, a stop) with no new read.
func (rs *Runs) republishLocked(ctx context.Context, c *askCell) askNotice {
	if !c.reply.OK {
		return askNotice{}
	}
	asks := workflow.OpenAsks(c.reply, c.runLogLocked(c.basis, rs.asks.epochNow()))
	return rs.publishLocked(ctx, c, wireAsks(asks), c.last.Reachable)
}

// askRead is one refresh's result: the reply and the asks of it.
type askRead struct {
	raw  json.RawMessage
	in   workflow.Inspect
	asks []workflow.Ask
}

// readAsks is an explicit refresh, waiting for any query in flight: a verb's pre-send check or a run read. A failed
// or undecodable read keeps the last asks and reads unreachable.
func (rs *Runs) readAsks(ctx context.Context, c *askCell) (askRead, error) {
	select {
	case c.query <- struct{}{}:
	case <-ctx.Done():
		return askRead{}, ctx.Err()
	}
	// This read serves any trigger owed one.
	c.takeOwed()
	r, err := rs.refreshCell(ctx, c)
	if c.releaseQuery() {
		rs.kickCell(c)
	}
	return r, err
}

// kickCell schedules a background read of the run.
func (rs *Runs) kickCell(c *askCell) {
	if rs.lifecycle == nil || c.transient {
		return
	}
	c.mu.Lock()
	c.dirty = true
	c.mu.Unlock()
	select {
	case c.query <- struct{}{}:
	default:
		// The query in flight re-loops on dirty.
		return
	}
	if !rs.lifecycle.goUnlessDraining(func() { rs.refreshLoop(c) }) {
		<-c.query
	}
}

// takeOwed is the start of a read under the query token: it settles every trigger owed a read.
func (c *askCell) takeOwed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dirty = false
}

// refreshLoop owns the query token on entry and re-reads while a trigger landed during a read.
func (rs *Runs) refreshLoop(c *askCell) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	for {
		c.takeOwed()
		if _, err := rs.refreshCell(ctx, c); err != nil {
			slog.Debug("ask core: a background read of a run failed", "workflow_id", c.workflowID, "error", err)
		}
		if !c.releaseQuery() {
			return
		}
		select {
		case c.query <- struct{}{}:
		default:
			return
		}
	}
}

// releaseQuery frees the query token and reports whether a trigger is still owed a read.
func (c *askCell) releaseQuery() bool {
	<-c.query
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty && !c.ended
}

// refreshCell is one read, an engine call, and its publish.
func (rs *Runs) refreshCell(ctx context.Context, c *askCell) (askRead, error) {
	c.mu.Lock()
	basis := c.basisLocked()
	c.mu.Unlock()
	raw, err := rs.rawInspect(ctx, c.workflowID)
	if workflowNotFound(err) {
		rs.markGone(ctx, c)
		return askRead{}, err
	}
	if err != nil {
		rs.markUnreadable(ctx, c)
		return askRead{}, err
	}
	in, decodeErr := workflow.DecodeInspect(c.workflowID, raw)
	if decodeErr != nil {
		c.mu.Lock()
		warned := c.decodeWarns
		c.decodeWarns = true
		c.mu.Unlock()
		if !warned {
			slog.Warn("ask core: an inspect reply did not decode", "workflow_id", c.workflowID, "error", decodeErr)
		}
		rs.markUnreadable(ctx, c)
		return askRead{raw: raw}, decodeErr
	}
	rs.readSaid(ctx, c, in, basis)
	c.mu.Lock()
	c.reply, c.basis = in, basis
	asks := workflow.OpenAsks(in, c.runLogLocked(basis, rs.asks.epochNow()))
	c.settleOnReadLocked(in, asks)
	// Settling can clear marotte's own holds, which are part of this snapshot.
	asks = workflow.OpenAsks(in, c.runLogLocked(basis, rs.asks.epochNow()))
	n := rs.publishLocked(ctx, c, wireAsks(asks), true)
	rs.armTimersLocked(c, asks)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
	return askRead{raw: raw, in: in, asks: asks}, nil
}

// settleOnReadLocked applies a read to marotte's own request state: a terminal run waits on
// nothing, and an answer the step took leaves no ask on its session.
func (c *askCell) settleOnReadLocked(in workflow.Inspect, asks []workflow.Ask) {
	if marotte.RunStatus(in.State.Status).Terminal() {
		c.stopping = false
		c.clearPendingLocked()
		stopTimer(c.requery)
		stopTimer(c.quiet)
		return
	}
	p := c.pending
	if p == nil || p.phase != pendingReplied {
		return
	}
	if !slices.ContainsFunc(asks, func(a workflow.Ask) bool { return a.StepSessionID == p.session }) {
		c.clearPendingLocked()
	}
}

// markGone is a read KAS answered with "no such run": it waits on nobody.
func (rs *Runs) markGone(ctx context.Context, c *askCell) {
	c.mu.Lock()
	c.stopping = false
	c.clearPendingLocked()
	stopTimer(c.requery)
	stopTimer(c.quiet)
	c.reply = workflow.Inspect{}
	n := rs.publishLocked(ctx, c, nil, true)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
}

func (rs *Runs) markUnreadable(ctx context.Context, c *askCell) {
	c.mu.Lock()
	n := rs.publishLocked(ctx, c, c.last.Asks, false)
	rs.armRequeryLocked(c)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
}

func (rs *Runs) armTimersLocked(c *askCell, asks []workflow.Ask) {
	rs.armRequeryLocked(c)
	stopTimer(c.quiet)
	if !slices.ContainsFunc(asks, func(a workflow.Ask) bool { return a.Wait == marotte.RunAskSettling }) {
		return
	}
	wait := max(settleQuiet-time.Since(c.lastMotion), 0)
	c.quiet = time.AfterFunc(wait+time.Millisecond, func() { rs.kickCell(c) })
}

// armRequeryLocked re-reads a run holding an ask or a pending send every askRequery, so a move made outside marotte
// is seen.
func (rs *Runs) armRequeryLocked(c *askCell) {
	stopTimer(c.requery)
	if c.ended || (len(c.last.Asks) == 0 && c.pending == nil) {
		return
	}
	c.requery = time.AfterFunc(askRequery, func() { rs.kickCell(c) })
}

// readSaid fills b with, from the run's own record, the last words of each parked step whose
// question marotte did not see this boot: the cold path, after a restart or a park with no warning.
// Once per turn marker, since the record can be large.
func (rs *Runs) readSaid(ctx context.Context, c *askCell, in workflow.Inspect, b askBasis) {
	if rs.log == nil {
		return
	}
	for _, st := range workflow.ParkedSteps(in) {
		sl := b.steps[st.SessionID]
		c.mu.Lock()
		m := c.steps[st.SessionID]
		if m == nil {
			m = &stepMarks{}
			c.steps[st.SessionID] = m
		}
		cached := m.saidRead && m.saidMarker == sl.Marker
		said := m.said
		c.mu.Unlock()
		if sl.Asked != "" && !sl.Open {
			continue
		}
		if !cached {
			said = rs.log.lastStepMessage(ctx, c.workflowID, workflow.PathKey(st.Path))
			c.mu.Lock()
			m.said, m.saidMarker, m.saidRead = said, sl.Marker, true
			c.mu.Unlock()
		}
		sl.Said = said
		b.steps[st.SessionID] = sl
	}
}

// askTurnEdge records a step session's turn bracket: the marker an ask's id carries moves only on
// turn_start, and turn_end fixes the warning that turn sent as its question.
func (rs *Runs) askTurnEdge(workflowID, session string, edge translate.TurnEdge) {
	if edge == translate.TurnEdgeNone || session == "" {
		return
	}
	c := rs.cellFor(workflowID)
	if c == nil {
		return
	}
	c.mu.Lock()
	m := c.steps[session]
	if m == nil {
		m = &stepMarks{}
		c.steps[session] = m
	}
	switch edge {
	case translate.TurnEdgeStart:
		c.turns++
		m.marker = keyenc.Join(rs.asks.epochNow(), strconv.FormatUint(c.turns, 10))
		m.open, m.warned = true, ""
	case translate.TurnEdgeEnd:
		m.open = false
		m.asked, m.warned = m.warned, ""
		m.endedAt = time.Now().UTC().Format(time.RFC3339)
	case translate.TurnEdgeNone:
	}
	c.lastMotion = time.Now()
	if p := c.pending; p != nil && p.session == session {
		c.settleMovedLocked(p)
	}
	c.mu.Unlock()
	rs.kickCell(c)
}

// settleMovedLocked clears a replied send once its session started a turn after the write: for an
// admitted answer the answering turn, for a refused one the ordinary turn KAS ran instead.
func (c *askCell) settleMovedLocked(p *pendingPrompt) {
	if p.phase != pendingReplied {
		return
	}
	if m := c.steps[p.session]; m != nil && m.marker != p.writeMarker {
		c.clearPendingLocked()
	}
}

// onNotify is `_kiro/session/notify` at either door: the workflow message first, then a step's
// warning is kept for the turn it ends, and any notify naming a run is motion on it.
func (rs *Runs) onNotify(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	rs.translate.HandleWorkflowMessage(ctx, chatID, msg)
	sig, ok := rs.translate.SessionNotifySignal(msg)
	if !ok {
		return
	}
	c := rs.cellFor(sig.WorkflowID)
	if c == nil {
		return
	}
	c.mu.Lock()
	if sig.Asks() && sig.CallerSessionID != "" {
		m := c.steps[sig.CallerSessionID]
		if m == nil {
			m = &stepMarks{}
			c.steps[sig.CallerSessionID] = m
		}
		m.warned = sig.Message
	}
	c.lastMotion = time.Now()
	c.mu.Unlock()
	rs.kickCell(c)
}

// asksOnFrame makes a lifecycle frame a trigger, after the frame's own handling (which can mint the
// lease a new cell needs).
func (rs *Runs) asksOnFrame(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		next(ctx, chatID, msg)
		if c := rs.cellFor(workflowIDOfFrame(msg)); c != nil {
			c.mu.Lock()
			c.lastMotion = time.Now()
			c.mu.Unlock()
			rs.kickCell(c)
		}
	}
}

// asksOnMotion stamps motion without a read: `watch_poll` fires on every idle poll and moves no wait.
func (rs *Runs) asksOnMotion(next chatHandler) chatHandler {
	return func(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
		next(ctx, chatID, msg)
		if c := rs.asks.get(workflowIDOfFrame(msg)); c != nil {
			c.mu.Lock()
			c.lastMotion = time.Now()
			c.mu.Unlock()
		}
	}
}

// kickChat re-reads every run leased to a chat whose bridge just opened.
func (rs *Runs) kickChat(chatID marotte.ChatID) {
	for _, c := range rs.asks.snapshotCells() {
		if c.chatID == chatID {
			rs.kickCell(c)
		}
	}
}

// kickAllAsks re-reads every leased run, the SSE reconnect trigger.
func (rs *Runs) kickAllAsks() {
	for _, c := range rs.asks.snapshotCells() {
		rs.kickCell(c)
	}
}

func (a *runAsks) snapshotCells() []*askCell {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Collect(maps.Values(a.cells))
}

// The serializer.

func (c *askCell) acquire(ctx context.Context) error {
	select {
	case c.serial <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *askCell) release() { <-c.serial }

// The refusals a verb answers about an ask, each with its machine code.
var (
	errAskStopping      = askRefusal(marotte.RunAskRefusedStopping, "This run is stopping.")
	errAskPending       = askRefusal(marotte.RunAskRefusedPending, "Another answer to this run is still being delivered.")
	errAskStale         = askRefusal(marotte.RunAskRefusedStale, "The step has said more since you started typing.")
	errAskGone          = askRefusal(marotte.RunAskRefusedGone, "This step is no longer waiting for an answer.")
	errAskBranchRunning = askRefusal(marotte.RunAskRefusedBranchRunning, "Another step of this run is still running; answer once it stops.")
	errAskUnsettled     = askRefusal(marotte.RunAskRefusedUnsettled, "This run looks busy but nothing is moving; open the run to check it.")
	errAskSettling      = askRefusal(marotte.RunAskRefusedSettling, "This run is moving; try again in a moment.")
	errAskUnreadable    = command.StatusErrorReason(http.StatusServiceUnavailable, string(marotte.RunAskRefusedUnreadable),
		errors.New("marotte could not read this run just now; try again in a moment"))
	errAskUnnamed       = askRefusal(marotte.RunAskRefusedUnnamed, "More than one execution of this step is here; send from the question you mean to answer.")
	errRunAnswerPending = askRefusal(marotte.RunAnswerPending, "An answer to this run is still being delivered; try again once it lands.")
)

func askRefusal(code marotte.RunAskRefusal, text string) error {
	return command.StatusErrorReason(http.StatusConflict, string(code), errors.New(text))
}

var waitRefusal = map[marotte.RunAskBlocker]error{
	marotte.RunAskBranchRunning: errAskBranchRunning,
	marotte.RunAskUnsettled:     errAskUnsettled,
	marotte.RunAskSettling:      errAskSettling,
}

// holdRefusal refuses a verb on marotte's own requests. The caller holds the serializer.
func (c *askCell) holdRefusal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.stopping:
		return errAskStopping
	case c.pending != nil:
		return errAskPending
	}
	return nil
}

// askByID is the ask askID names in a fresh read, decided on Wait alone: the verb's own pending
// registration holds every ask of the run and must not refuse it.
func askByID(asks []workflow.Ask, askID string) (*workflow.Ask, error) {
	i, err := namedAsk(asks, askID)
	if err != nil {
		return nil, err
	}
	if wErr := waitRefusal[asks[i].Wait]; wErr != nil {
		return nil, wErr
	}
	return &asks[i], nil
}

// namedAsk is the index of the ask askID names: errAskStale when its position asks again under a
// newer id, errAskGone when nothing asks there.
func namedAsk(asks []workflow.Ask, askID string) (int, error) {
	if i := slices.IndexFunc(asks, func(a workflow.Ask) bool { return a.ID == askID }); i >= 0 {
		return i, nil
	}
	parts, err := keyenc.Split(askID)
	if err == nil && len(parts) == 2 && slices.ContainsFunc(asks, func(a workflow.Ask) bool { return a.Position == parts[0] }) {
		return -1, errAskStale
	}
	return -1, errAskGone
}

// registerPending records marotte's send on the run before its pre-send read, so every device
// disables Send at once. The caller holds the serializer.
func (rs *Runs) registerPending(ctx context.Context, c *askCell) *pendingPrompt {
	p := &pendingPrompt{done: make(chan struct{}), phase: pendingChecking}
	c.mu.Lock()
	c.pending = p
	n := rs.republishLocked(ctx, c)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
	return p
}

// clearPending ends p if it is still the run's pending send, releasing every stop waiting on it.
func (rs *Runs) clearPending(ctx context.Context, c *askCell, p *pendingPrompt) {
	c.mu.Lock()
	if c.pending != p {
		c.mu.Unlock()
		return
	}
	c.clearPendingLocked()
	n := rs.republishLocked(ctx, c)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
}

func (c *askCell) clearPendingLocked() {
	p := c.pending
	if p == nil {
		return
	}
	c.pending = nil
	stopTimer(p.ceiling)
	close(p.done)
}

// sendingPending names the session and the marker the write goes out against.
func (c *askCell) sendingPending(p *pendingPrompt, session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p.phase, p.session = pendingSending, session
	if m := c.steps[session]; m != nil {
		p.writeMarker = m.marker
	}
}

// repliedPending moves p to replied: it outlives the reply until the step's next turn_start, the
// first wire evidence the loop took the words, or askPendingCeiling.
func (rs *Runs) repliedPending(ctx context.Context, c *askCell, p *pendingPrompt) {
	c.mu.Lock()
	if c.pending != p {
		c.mu.Unlock()
		return
	}
	p.phase = pendingReplied
	if c.transient {
		// No turn edge or read reaches a transient cell, and no stop waits on it.
		c.clearPendingLocked()
	} else {
		c.settleMovedLocked(p)
	}
	if c.pending != p {
		n := rs.republishLocked(ctx, c)
		c.mu.Unlock()
		rs.sendNotices(ctx, c, n)
		return
	}
	if rs.lifecycle != nil {
		p.ceiling = rs.lifecycle.afterUnlessDraining(askPendingCeiling, func() { rs.expirePending(c, p) })
	}
	c.mu.Unlock()
}

// expirePending reads the run once more and clears a send whose turn never started.
func (rs *Runs) expirePending(c *askCell, p *pendingPrompt) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()
	_, _ = rs.readAsks(ctx, c)
	c.mu.Lock()
	still := c.pending == p
	c.mu.Unlock()
	if !still {
		return
	}
	slog.Warn("an answered step did not start within 10 s", "workflow_id", c.workflowID,
		"session", scrubLog(p.session))
	rs.clearPending(ctx, c, p)
	rs.kickCell(c)
}

// stopSerialized sends a cancel or delete in order behind marotte's own sends on a leased run: it marks
// the run stopping, waits for a pending answer to start its turn, and sends holding the serializer.
// Past askStopCeiling it sends anyway, so an unattended run's deadline is never extended. send
// reports whether the stop may have landed; one that did not makes the run answerable again. A run
// with no cell sends at once, unordered against a transient cell's step message.
func (rs *Runs) stopSerialized(ctx context.Context, workflowID string, send func(context.Context) bool) {
	c := rs.asks.get(workflowID)
	if c == nil {
		send(ctx)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, askStopCeiling)
	defer cancel()
	held := c.acquire(sctx) == nil
	rs.setStopping(ctx, c, true)
	if held {
		if p := c.pendingOf(); p != nil {
			c.release()
			select {
			case <-p.done:
			case <-sctx.Done():
			}
			held = c.acquire(sctx) == nil
		}
	}
	if !held {
		slog.Warn("a run was stopped with marotte's answer to it still in flight", "workflow_id", workflowID)
	}
	landed := send(ctx)
	if !landed {
		rs.setStopping(ctx, c, false)
	}
	if held {
		c.release()
	}
	rs.kickCell(c)
}

func (c *askCell) pendingOf() *pendingPrompt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

func (rs *Runs) setStopping(ctx context.Context, c *askCell, on bool) {
	c.mu.Lock()
	if c.stopping == on {
		c.mu.Unlock()
		return
	}
	c.stopping = on
	n := rs.republishLocked(ctx, c)
	c.mu.Unlock()
	rs.sendNotices(ctx, c, n)
}

// clearStopping is a kept delete: the run takes answers again.
func (rs *Runs) clearStopping(ctx context.Context, workflowID string) {
	if c := rs.asks.get(workflowID); c != nil {
		rs.setStopping(ctx, c, false)
	}
}

// controlSerialized sends a resume, retry or pause in order with marotte's own sends, refusing while
// one is pending or a stop is queued; a run with no cell is sent unserialized.
func (rs *Runs) controlSerialized(ctx context.Context, workflowID string, send func(context.Context) error) error {
	c := rs.asks.get(workflowID)
	if c == nil {
		return send(ctx)
	}
	vctx, cancel := context.WithTimeout(ctx, askVerbCeiling)
	err := c.acquire(vctx)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errRunAnswerPending
	}
	defer rs.kickCell(c)
	defer c.release()
	switch refusal := c.holdRefusal(); {
	case errors.Is(refusal, errAskPending):
		return errRunAnswerPending
	case refusal != nil:
		return refusal
	}
	return send(ctx)
}
