package agent

// The pending run-ask registry and the two doors a step's question arrives through. Not
// pendingPermsTracker: a run ask has no request id, blocks nothing upstream, and survives a bridge
// death and a restart, so it has its own identity and clearing rule plus a reconcile against KAS.
// Bounded by the run; every clear is idempotent.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/notice"
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

func (a *runAsk) event() marotte.ServerEvent {
	return marotte.NewEvent(marotte.EventRunInputNeeded, a.chatID, a.payload)
}

// pendingRunAsks holds the unanswered asks under its own mutex: it is read on every SSE connect and
// written from every forward goroutine. Zero value usable; travels by pointer. `answering` counts
// in-flight answers per run (beginAnswer).
//
// It is also the one owner of a step message's ask decision (admitStep): an ask's arrival and a
// message's verb choice serialize on mu, which the carrier's read loop takes without waiting on a send.
type pendingRunAsks struct {
	asks      map[runAskKey]*runAsk
	answering map[string]int
	// holds are the paused-step prompts in flight, per run.
	holds map[string][]*askHold
	// versions holds the shared `pending` counter every add and settle bumps under mu (mintPending).
	versions *subject.Versions
	mu       sync.Mutex
}

// askHold is a paused-step prompt in flight. An ask for its execution arriving before the prompt
// settles is held on it, not offered: the words being sent are that ask's answer.
type askHold struct {
	n    runNow
	at   stepAddr
	held []heldAsk
}

// heldAsk is an ask a hold took, with the carrier's folded position when its frame folded (one less
// than the frame's own); gen 0 is an ask that is no frame.
type heldAsk struct {
	a      *runAsk
	folded drainPoint
}

// promptSend is a prompt's admission carried across the send to its settlement: the hold, the carrier
// whose read loop folds the step's frames, and what the send proved. Its zero delivery is a refusal.
type promptSend struct {
	hold    *askHold
	carrier marotte.ChatID
	sent    stepDelivery
}

func (h *askHold) owns(p *marotte.RunInputNeededPayload) bool {
	st, matches := h.n.askedStep(p)
	return matches == 1 && h.n.addrOf(st) == h.at
}

// ensure creates the map on first write. Callers hold the lock.
func (r *pendingRunAsks) ensure() {
	if r.asks == nil {
		r.asks = make(map[runAskKey]*runAsk)
	}
}

type askArrival int

const (
	askRecorded askArrival = iota
	// askIgnored: a redelivery, or a payload with no identity.
	askIgnored
	// askHeld: a prompt in flight to its execution answers it.
	askHeld
)

// add records an ask, reporting whether it is new; a redelivered frame keeps the original `asked_at`.
// An ask that is no frame (a restore, a reconcile) counts as older than any prompt in flight.
func (r *pendingRunAsks) add(a *runAsk) bool {
	return r.offer(a, drainPoint{}) == askRecorded
}

func (r *pendingRunAsks) offer(a *runAsk, folded drainPoint) askArrival {
	if a.payload.WorkflowID == "" || a.payload.AskID == "" {
		return askIgnored
	}
	k := runAskKey{workflowID: a.payload.WorkflowID, askID: a.payload.AskID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if _, dup := r.asks[k]; dup {
		return askIgnored
	}
	hs := r.holds[k.workflowID]
	for _, h := range hs {
		if slices.ContainsFunc(h.held, func(x heldAsk) bool { return x.a.payload.AskID == k.askID }) {
			return askIgnored
		}
	}
	for _, h := range hs {
		if h.owns(&a.payload) {
			h.held = append(h.held, heldAsk{a: a, folded: folded})
			return askHeld
		}
	}
	r.asks[k] = a
	mintPending(&r.versions)
	return askRecorded
}

// stepAdmission is what admitStep took for one step message: the claimed ask for an answer (close
// it with endAnswer), the held send for a prompt (close it with Runs.settleHold).
type stepAdmission struct {
	ask    *runAsk
	prompt *promptSend
	verb   marotte.RunStepMessageVerb
}

// askAtLocked answers the open ask that belongs to the execution at, the lowest ask id first.
func (r *pendingRunAsks) askAtLocked(n runNow, at stepAddr) (runAskKey, *runAsk) {
	var keys []runAskKey
	for k := range r.asks {
		if k.workflowID == at.workflowID {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(x, y runAskKey) int { return strings.Compare(x.askID, y.askID) })
	for _, k := range keys {
		a := r.asks[k]
		if st, matches := n.askedStep(&a.payload); matches == 1 && n.addrOf(st) == at {
			return k, a
		}
	}
	return runAskKey{}, nil
}

func (r *pendingRunAsks) askAt(n runNow, at stepAddr) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, a := r.askAtLocked(n, at)
	return a != nil
}

// admitStep picks a step message's verb from the step's state and its open ask, and takes what that
// verb needs in the same critical section, so no ask can arrive between the choice and the send.
func (r *pendingRunAsks) admitStep(n runNow, now *stepNow, carrier marotte.ChatID) (stepAdmission, marotte.RunStepMessageRefusal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, a := r.askAtLocked(n, now.at)
	verb, refuse := stepMessageVerb(now, a != nil)
	if refuse != "" {
		return stepAdmission{}, refuse
	}
	adm := stepAdmission{verb: verb}
	switch verb {
	case marotte.RunStepMessageAnswer:
		delete(r.asks, k)
		mintPending(&r.versions)
		r.beginAnswerLocked(k.workflowID)
		adm.ask = a
	case marotte.RunStepMessagePrompt:
		if r.holds == nil {
			r.holds = make(map[string][]*askHold)
		}
		h := &askHold{n: n, at: now.at}
		adm.prompt = &promptSend{hold: h, carrier: carrier}
		r.holds[now.at.workflowID] = append(r.holds[now.at.workflowID], h)
	case marotte.RunStepMessageSteer:
	}
	return adm, ""
}

// releaseHold ends a prompt's hold and splits its asks by what the send proved (stepDelivery.answers):
// the words answer an ask folded before their settlement, and every other ask is still owed an answer.
func (r *pendingRunAsks) releaseHold(s *promptSend) (answered, unanswered []*runAsk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := s.hold
	wf := h.at.workflowID
	r.holds[wf] = slices.DeleteFunc(r.holds[wf], func(x *askHold) bool { return x == h })
	if len(r.holds[wf]) == 0 {
		delete(r.holds, wf)
	}
	for _, x := range h.held {
		if s.sent.answers(x.folded) {
			answered = append(answered, x.a)
		} else {
			unanswered = append(unanswered, x.a)
		}
	}
	return answered, unanswered
}

// An empty workflowID matches every run.
func (r *pendingRunAsks) takeHeldLocked(workflowID string, match func(*runAsk) bool) []*runAsk {
	var out []*runAsk
	for wf, hs := range r.holds {
		if workflowID != "" && wf != workflowID {
			continue
		}
		for _, h := range hs {
			h.held = slices.DeleteFunc(h.held, func(x heldAsk) bool {
				if match(x.a) {
					out = append(out, x.a)
					return true
				}
				return false
			})
		}
	}
	return out
}

// takeIfPresent claims one ask, deleting and returning it, false when beaten. One lock spans both:
// KAS accepts one answer, and a loser's `session/prompt` would become an ordinary prompt on the step.
func (r *pendingRunAsks) takeIfPresent(workflowID, askID string) (*runAsk, bool) {
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

// restore puts a claimed ask back, reporting whether it went in: the answer path claims before it
// sends. Go through (*Runs).restoreAsk, which re-broadcasts.
func (r *pendingRunAsks) restore(a *runAsk) bool {
	return r.add(a)
}

// hasRun reports whether a run's question is accounted for: an unanswered ask OR an answer in
// flight. Both, or a refetch in the claim-to-send gap mints a text-less twin.
func (r *pendingRunAsks) hasRun(workflowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering[workflowID] > 0 {
		return true
	}
	for _, h := range r.holds[workflowID] {
		if len(h.held) > 0 {
			return true
		}
	}
	for k := range r.asks {
		if k.workflowID == workflowID {
			return true
		}
	}
	return false
}

// The caller opens it before claiming and defers endAnswer. A count: two parked steps of one run
// can be answered at once.
func (r *pendingRunAsks) beginAnswer(workflowID string) {
	if workflowID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginAnswerLocked(workflowID)
}

func (r *pendingRunAsks) beginAnswerLocked(workflowID string) {
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

// takeRun claims and returns every ask of a run whose wait is over, so the caller can retire the
// cards: a stale head card hides every card queued behind it.
func (r *pendingRunAsks) takeRun(workflowID string) []*runAsk {
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
	return append(out, r.takeHeldLocked(workflowID, func(*runAsk) bool { return true })...)
}

// takeNode claims and returns every ask naming one node, safe on `node_complete` while a sibling
// branch is parked. An empty node id is left for the terminal clear.
func (r *pendingRunAsks) takeNode(workflowID, nodeID string) []*runAsk {
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
	return append(out, r.takeHeldLocked(workflowID, func(a *runAsk) bool { return a.payload.NodeID == nodeID })...)
}

// clearChat drops every ask keyed to a chat that went away.
func (r *pendingRunAsks) clearChat(chatID marotte.ChatID) {
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
	r.takeHeldLocked("", func(a *runAsk) bool { return a.chatID == chatID })
}

// snapshotRun returns copies of a run's unanswered asks, sorted by ask id, for the read endpoint.
// Copies keep the never-shared invariant.
func (r *pendingRunAsks) snapshotRun(workflowID string) []marotte.RunOpenAsk {
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

// list snapshots every unanswered ask, however old, optionally for one surface: a parked run has
// no deadline. A `run:<id>` key is not a chat.
func (r *pendingRunAsks) list(chatFilter marotte.ChatID) []marotte.ServerEvent {
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

// handleSessionNotify records a workflow message, then a step's question before its broadcast, so a
// stream opening between the two gets it from the replay. chatID comes from the door, never the payload's KAS `sessionId`.
func (rs *Runs) handleSessionNotify(ctx context.Context, chatID marotte.ChatID, msg *marotte.RPCResponse) {
	rs.translate.HandleWorkflowMessage(ctx, chatID, msg)
	p, ok := rs.translate.SessionNotifyAsk(msg)
	if !ok {
		return
	}
	a := &runAsk{chatID: chatID, payload: p}
	switch rs.asks.offer(a, rs.coord.turns.folded(chatID)) {
	case askIgnored:
		slog.Debug("run ask: already recorded, not re-broadcast",
			"workflow_id", scrubLog(p.WorkflowID), "ask_id", scrubLog(p.AskID))
		return
	case askHeld:
		slog.Info("a workflow step asked while a message to it was being sent, so the message answers it",
			"workflow_id", scrubLog(p.WorkflowID), "node_id", scrubLog(p.NodeID), "ask_id", scrubLog(p.AskID))
		return
	case askRecorded:
	}
	slog.Info("a workflow step is waiting for an answer",
		"workflow_id", scrubLog(p.WorkflowID), "node_id", scrubLog(p.NodeID),
		"agent", scrubLog(p.AgentName), "chat_id", chatID)
	rs.bus.Broadcast(ctx, a.event())
	rs.notifyAsk(ctx, a)
}

// notifyAsk notifies a step's question: it parks the run until a person answers.
func (rs *Runs) notifyAsk(ctx context.Context, a *runAsk) {
	if rs.coord == nil {
		return
	}
	p := &a.payload
	t := rs.coord.NoticeTarget(ctx, a.chatID, p.WorkflowID)
	n := notice.Question(t, cmp.Or(p.AgentName, p.NodeID), p.Question)
	rs.coord.Notify(ctx, a.chatID, &n)
}

// `by` is the caller's: continue-without-answering is SettledByUser, a node completing
// SettledByMoot.
func (rs *Runs) settleAskForNode(
	ctx context.Context, workflowID, nodeID string, by marotte.SettledBy,
) {
	for _, a := range rs.asks.takeNode(workflowID, nodeID) {
		slog.Info("a parked step moved on, so its question is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(nodeID),
			"ask_id", scrubLog(a.payload.AskID), "settled_by", string(by))
		rs.announceSettled(ctx, a, by)
	}
}

func (rs *Runs) settleAsksForRun(ctx context.Context, workflowID string) {
	for _, a := range rs.asks.takeRun(workflowID) {
		slog.Info("a run ended still holding a question, so the card is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(a.payload.NodeID),
			"ask_id", scrubLog(a.payload.AskID))
		rs.announceSettled(ctx, a, marotte.SettledByMoot)
	}
}

// restoreAsk puts a claimed ask back and re-broadcasts it: the click already spliced the dock entry.
// The dock de-duplicates by (kind, askID).
func (rs *Runs) restoreAsk(ctx context.Context, a *runAsk) {
	if !rs.asks.restore(a) {
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
	if rs.coord != nil {
		rs.coord.retractPush(marotte.RunSubject(a.payload.WorkflowID))
	}
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

// The re-park sentence is matched by its two ends (KAS interpolates the node id). needInputParked
// covers parallel branches.
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

// CompletionSignal survives a parallel branch where pauseReason does not; Type's one reader is
// statusUpdateTarget (KAS considers `step` nodes only).
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
	if rs.asks.hasRun(workflowID) {
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
	if !rs.asks.add(a) {
		return
	}
	slog.Info("a parked run had no ask on this server, so one was reconstructed from its state",
		"workflow_id", scrubLog(workflowID), "node_id", scrubLog(leaf.NodeID), "chat_id", a.chatID,
		"pause_reason", scrubLog(res.State.PauseReason))
	rs.bus.Broadcast(ctx, a.event())
	rs.notifyAsk(ctx, a)
}

// askChatID keys a synthesised ask to the launching chat while a live bridge hosts the run (both
// docks match it), else `run:<workflowId>`, also right for a parentless run.
func (rs *Runs) askChatID(ctx context.Context, workflowID string) marotte.ChatID {
	if chatID, sb := rs.hostBridgeChat(ctx, workflowID); sb != nil && chatID != "" {
		return chatID
	}
	return runChatID(workflowID)
}

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

// The leaf's session is the answer address. Depth-first, first match.
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
