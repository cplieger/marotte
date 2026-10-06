package agent

// The pending run-ask registry and the two doors a step's question arrives through. Not
// pendingPermsTracker: a run ask has no request id, blocks nothing upstream, and survives a bridge
// death and a restart, so it has its own identity and clearing rule plus a reconcile against KAS.
// Bounded by the run; every clear is idempotent.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// scrubLog strips CR and LF from a wire value before logging (CWE-117).
func scrubLog(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}

// runAskKey is the run plus the ask within it, never the ask id alone: a synthesised id derives
// from a node path two runs of one recipe share.
type runAskKey struct {
	workflowID string
	askID      string
}

// runAsk is one unanswered ask plus its queue key (the chat id, known only to the arrival door).
// Immutable after Add, and every method returning one has deleted it, so pointers are never shared.
type runAsk struct {
	chatID  marotte.ChatID
	payload marotte.RunInputNeededPayload
}

// event renders an ask as the client frame, shared by live broadcast and connect replay.
func (a *runAsk) event() marotte.ServerEvent {
	return marotte.NewEvent(marotte.EventRunInputNeeded, a.chatID, a.payload)
}

// pendingRunAsks holds the unanswered asks under its own mutex: it is read on every SSE connect and
// written from every forward goroutine. Zero value usable; travels by pointer. `answering` counts
// in-flight answers per run (beginAnswer).
type pendingRunAsks struct {
	asks      map[runAskKey]*runAsk
	answering map[string]int
	// versions holds the shared `pending` counter every add and settle bumps under mu (mintPending).
	versions *subject.Versions
	mu       sync.Mutex
}

// ensure creates the map on first write. Callers hold the lock.
func (r *pendingRunAsks) ensure() {
	if r.asks == nil {
		r.asks = make(map[runAskKey]*runAsk)
	}
}

// Add records an ask, reporting whether it is new; a redelivered frame keeps the original `asked_at`.
func (r *pendingRunAsks) Add(a *runAsk) bool {
	if a.payload.WorkflowID == "" || a.payload.AskID == "" {
		return false
	}
	k := runAskKey{workflowID: a.payload.WorkflowID, askID: a.payload.AskID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if _, dup := r.asks[k]; dup {
		return false
	}
	r.asks[k] = a
	mintPending(&r.versions)
	return true
}

// TakeIfPresent claims one ask, deleting and returning it, false when beaten. One lock spans both:
// KAS accepts one answer, and a loser's `session/prompt` would become an ordinary prompt on the step.
func (r *pendingRunAsks) TakeIfPresent(workflowID, askID string) (*runAsk, bool) {
	k := runAskKey{workflowID: workflowID, askID: askID}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.asks[k]
	if !ok {
		return nil, false
	}
	delete(r.asks, k)
	mintPending(&r.versions)
	return a, true
}

// Restore puts a claimed ask back, reporting whether it went in: the answer path claims before it
// sends. Go through (*Runs).restoreAsk, which re-broadcasts.
func (r *pendingRunAsks) Restore(a *runAsk) bool {
	return r.Add(a)
}

// HasRun reports whether a run's question is accounted for: an unanswered ask OR an answer in
// flight. Both, or a refetch in the claim-to-send gap mints a text-less twin.
func (r *pendingRunAsks) HasRun(workflowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering[workflowID] > 0 {
		return true
	}
	for k := range r.asks {
		if k.workflowID == workflowID {
			return true
		}
	}
	return false
}

// beginAnswer opens a run's answer window; the caller opens it before claiming and defers
// endAnswer. A count: two parked steps of one run can be answered at once.
func (r *pendingRunAsks) beginAnswer(workflowID string) {
	if workflowID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering == nil {
		r.answering = make(map[string]int)
	}
	r.answering[workflowID]++
}

// endAnswer closes one answer window, dropping the key at zero.
func (r *pendingRunAsks) endAnswer(workflowID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering[workflowID] > 1 {
		r.answering[workflowID]--
		return
	}
	delete(r.answering, workflowID)
}

// TakeRun claims and returns every ask of a run whose wait is over, so the caller can retire the
// cards: a stale head card hides every card queued behind it.
func (r *pendingRunAsks) TakeRun(workflowID string) []*runAsk {
	if workflowID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*runAsk
	for k, a := range r.asks {
		if k.workflowID == workflowID {
			out = append(out, a)
			delete(r.asks, k)
		}
	}
	if len(out) > 0 {
		mintPending(&r.versions)
	}
	return out
}

// TakeNode claims and returns every ask naming one node, safe on `node_complete` while a sibling
// branch is parked. An empty node id is left for the terminal clear.
func (r *pendingRunAsks) TakeNode(workflowID, nodeID string) []*runAsk {
	if workflowID == "" || nodeID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*runAsk
	for k, a := range r.asks {
		if k.workflowID == workflowID && a.payload.NodeID == nodeID {
			out = append(out, a)
			delete(r.asks, k)
		}
	}
	if len(out) > 0 {
		mintPending(&r.versions)
	}
	return out
}

// ClearChat drops every ask keyed to a chat that went away.
func (r *pendingRunAsks) ClearChat(chatID marotte.ChatID) {
	if chatID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := false
	for k, a := range r.asks {
		if a.chatID == chatID {
			delete(r.asks, k)
			removed = true
		}
	}
	if removed {
		mintPending(&r.versions)
	}
}

// SnapshotRun returns copies of a run's unanswered asks, sorted by ask id, for the read endpoint.
// Copies keep the never-shared invariant.
func (r *pendingRunAsks) SnapshotRun(workflowID string) []marotte.RunOpenAsk {
	if workflowID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []marotte.RunOpenAsk
	for k, a := range r.asks {
		if k.workflowID != workflowID {
			continue
		}
		out = append(out, marotte.RunOpenAsk{
			AskID:     a.payload.AskID,
			Question:  a.payload.Question,
			NodeID:    a.payload.NodeID,
			AgentName: a.payload.AgentName,
			AskedAt:   a.payload.AskedAt,
		})
	}
	slices.SortFunc(out, func(x, y marotte.RunOpenAsk) int {
		return strings.Compare(x.AskID, y.AskID)
	})
	return out
}

// List snapshots every unanswered ask, however old, optionally for one surface: a parked run has
// no deadline. A `run:<id>` key is not a chat.
func (r *pendingRunAsks) List(chatFilter marotte.ChatID) []marotte.ServerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]marotte.ServerEvent, 0, len(r.asks))
	for _, a := range r.asks {
		if chatFilter != "" && a.chatID != "" && a.chatID != chatFilter {
			continue
		}
		out = append(out, a.event())
	}
	return out
}

// handleSessionNotify records a step's question, then broadcasts it, so a stream opening between
// the two gets it from the replay. chatID comes from the door, never the payload's KAS `sessionId`.
func (rs *Runs) handleSessionNotify(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	p, ok := rs.translate.SessionNotifyAsk(msg)
	if !ok {
		return
	}
	a := &runAsk{chatID: chatID, payload: p}
	if !rs.asks.Add(a) {
		slog.Debug("run ask: already recorded, not re-broadcast",
			"workflow_id", scrubLog(p.WorkflowID), "ask_id", scrubLog(p.AskID))
		return
	}
	slog.Info("a workflow step is waiting for an answer",
		"workflow_id", scrubLog(p.WorkflowID), "node_id", scrubLog(p.NodeID),
		"agent", scrubLog(p.AgentName), "chat_id", chatID)
	rs.bus.Broadcast(ctx, a.event())
}

// settleAskForNode retires a node's asks and announces `run_input_settled`. `by` is the caller's:
// continue-without-answering is SettledByUser, a node completing SettledByMoot.
func (rs *Runs) settleAskForNode(
	ctx context.Context, workflowID, nodeID string, by marotte.SettledBy,
) {
	for _, a := range rs.asks.TakeNode(workflowID, nodeID) {
		slog.Info("a parked step moved on, so its question is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(nodeID),
			"ask_id", scrubLog(a.payload.AskID), "settled_by", string(by))
		rs.announceSettled(ctx, a, by)
	}
}

// settleAsksForRun retires every ask a run still held, as SettledByMoot.
func (rs *Runs) settleAsksForRun(ctx context.Context, workflowID string) {
	for _, a := range rs.asks.TakeRun(workflowID) {
		slog.Info("a run ended still holding a question, so the card is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(a.payload.NodeID),
			"ask_id", scrubLog(a.payload.AskID))
		rs.announceSettled(ctx, a, marotte.SettledByMoot)
	}
}

// restoreAsk puts a claimed ask back and re-broadcasts it: the click already spliced the dock entry.
// The dock de-duplicates by (kind, askID).
func (rs *Runs) restoreAsk(ctx context.Context, a *runAsk) {
	if !rs.asks.Restore(a) {
		return
	}
	slog.Info("an answer did not reach the step, so its question is offered again",
		"workflow_id", scrubLog(a.payload.WorkflowID), "ask_id", scrubLog(a.payload.AskID))
	rs.bus.Broadcast(ctx, a.event())
}

// announceSettled publishes a settlement on the ask's own chat id, so a chat-filtered stream receives it.
func (rs *Runs) announceSettled(ctx context.Context, a *runAsk, by marotte.SettledBy) {
	rs.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventRunInputSettled, a.chatID,
		marotte.RunInputSettledPayload{
			WorkflowID: a.payload.WorkflowID,
			AskID:      a.payload.AskID,
			SettledBy:  by,
		}))
}

// The two pauseReason literals meaning a person owes an answer: `send_message`'s park, and the
// re-park a plain Resume produces (resume keeps the node's completionSignal).
const (
	needInputPauseReason  = "Step requested user input via send_message."
	reparkPausePrefix     = "Step '"
	reparkPauseSuffix     = "' is waiting for user input."
	waitingForNextMessage = "' is waiting for the next user message."
)

// needInputSignal is KAS's node completionSignal for a step waiting on a person, the only mark a parallel branch's park keeps.
const needInputSignal = "need_input"

// needInputPause reports whether a pause reason means a step waits on a person; the re-park sentence
// is matched by its two ends (KAS interpolates the node id). needInputParked covers parallel branches.
func needInputPause(reason string) bool {
	if reason == needInputPauseReason {
		return true
	}
	if !strings.HasPrefix(reason, reparkPausePrefix) {
		return false
	}
	return strings.HasSuffix(reason, reparkPauseSuffix) ||
		strings.HasSuffix(reason, waitingForNextMessage)
}

// askInspect is the reconcile's own minimal `inspect` decode, deliberately not internal/workflow's
// State, which the read endpoint passes through whole.
type askInspect struct {
	State *struct {
		PauseDetail *askPauseDetail   `json:"pauseDetail"`
		Root        *askNode          `json:"root"`
		Status      marotte.RunStatus `json:"status"`
		PauseReason string            `json:"pauseReason"`
	} `json:"state"`
}

// askPauseDetail reads only `occurredAt`. Kept apart from the resume and cancel predicates'
// `pauseDetail`, so a wire change cannot reach the heal through this.
type askPauseDetail struct {
	OccurredAt string `json:"occurredAt"`
}

// askNode is one state-tree node. CompletionSignal survives a parallel branch where pauseReason does not;
// Type's one reader is statusUpdateTarget (KAS considers `step` nodes only).
type askNode struct {
	NodeID           string                `json:"nodeId"`
	Type             string                `json:"type"`
	Status           marotte.RunNodeStatus `json:"status"`
	SessionID        string                `json:"sessionId"`
	AgentName        string                `json:"agentName"`
	CompletionSignal string                `json:"completionSignal"`
	Children         []askNode             `json:"children"`
}

// reconcileNeedInput mints an ask for a run parked on a person with nothing in the in-memory
// registry, or a restart leaves it parked forever. Idempotent: the id derives from the paused leaf's
// path, and HasRun skips the pass. Run from the read path, so nobody gets a card for an unopened run.
func (rs *Runs) reconcileNeedInput(ctx context.Context, workflowID string, raw json.RawMessage) {
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	if res.State.Status != marotte.RunStatusPaused {
		return
	}
	// The signal arm leads: it reaches a parallel-branch park and names the step.
	leaf, path := needInputParked(res.State.Root, nil)
	if leaf == nil {
		if !needInputPause(res.State.PauseReason) {
			return
		}
		leaf, path = pausedLeaf(res.State.Root, nil)
	}
	if leaf == nil {
		return
	}
	if rs.asks.HasRun(workflowID) {
		return
	}
	askedAt := ""
	if res.State.PauseDetail != nil {
		askedAt = res.State.PauseDetail.OccurredAt
	}
	a := &runAsk{
		chatID: rs.askChatID(ctx, workflowID),
		payload: marotte.RunInputNeededPayload{
			WorkflowID:    workflowID,
			AskID:         keyenc.Join("reconciled", keyenc.Join(path...)),
			NodeID:        leaf.NodeID,
			StepSessionID: leaf.SessionID,
			AgentName:     leaf.AgentName,
			// Empty rather than invented.
			Question: "",
			AskedAt:  askedAt,
		},
	}
	if !rs.asks.Add(a) {
		return
	}
	slog.Info("a parked run had no ask on this server, so one was reconstructed from its state",
		"workflow_id", scrubLog(workflowID), "node_id", scrubLog(leaf.NodeID), "chat_id", a.chatID,
		"pause_reason", scrubLog(res.State.PauseReason))
	rs.bus.Broadcast(ctx, a.event())
}

// askChatID keys a synthesised ask to the launching chat while a live bridge hosts the run (both
// docks match it), else `run:<workflowId>`, also right for a parentless run.
func (rs *Runs) askChatID(ctx context.Context, workflowID string) marotte.ChatID {
	if chatID, sb := rs.hostBridgeChat(ctx, workflowID); sb != nil && chatID != "" {
		return chatID
	}
	return runChatID(workflowID)
}

// needInputParked finds a paused node whose completion signal waits on a person, with its path.
// It reaches a parallel-branch park (executeParallel copies state, not node records).
func needInputParked(n *askNode, trail []string) (leaf *askNode, path []string) {
	if n == nil {
		return nil, nil
	}
	here := append(append([]string{}, trail...), n.NodeID)
	if n.Status == marotte.RunNodeStatusPaused && n.CompletionSignal == needInputSignal {
		return n, here
	}
	for i := range n.Children {
		if leaf, path := needInputParked(&n.Children[i], here); leaf != nil {
			return leaf, path
		}
	}
	return nil, nil
}

// pausedLeaf finds the paused leaf a run waits at, with its path; the leaf's session is the answer address. Depth-first, first match.
func pausedLeaf(n *askNode, trail []string) (leaf *askNode, path []string) {
	if n == nil {
		return nil, nil
	}
	here := append(append([]string{}, trail...), n.NodeID)
	if len(n.Children) == 0 {
		if n.Status == marotte.RunNodeStatusPaused {
			return n, here
		}
		return nil, nil
	}
	for i := range n.Children {
		if leaf, path := pausedLeaf(&n.Children[i], here); leaf != nil {
			return leaf, path
		}
	}
	return nil, nil
}
