package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

type fakeBridge struct {
	notifCh chan marotte.Notification
	// deliveredSeq drives the sequence a parked settle waits for.
	deliveredSeq uint64
	// loadSeq is the position `session/load` answered at, recorded after the replay frames, as on the wire.
	loadSeq     uint64
	callResults map[string]json.RawMessage
	callErrs    map[string]error
	// callRPCErrs is KAS refusing in band; callErrs is the transport failing.
	callRPCErrs  map[string]*marotte.RPCError
	notifyErrs   map[string]error
	lastParams   map[string]map[string]any
	callDeadline map[string]bool
	chunksOnCall map[string][]string
	// notifsOnCall are whole frames delivered after a named Call, unstamped (a replay); chunksOnCall stamps its
	// own id, which the own-session screen drops.
	notifsOnCall map[string][]*marotte.RPCResponse
	// blockOn parks Call, after recording it, until the method's channel is closed; blockNotifyOn parks Notify.
	blockOn       map[string]chan struct{}
	blockNotifyOn map[string]chan struct{}
	// onCall answers a Call instead of the scripted result, delivering its frames before returning, as KAS does.
	onCall func(method string, params map[string]any) (json.RawMessage, []*marotte.RPCResponse, bool)
	// refuseAfterCall is KAS refusing in band after onCall ran, so frames can fold before the refusal.
	refuseAfterCall map[string]*marotte.RPCError
	// failAfterCall is the transport failing after onCall ran: the words were written, then the reply was lost.
	failAfterCall map[string]error
	sessionID     string
	modelID       string
	effort        string
	// observedEffort is the level the SESSION reported, not the one asked for.
	observedEffort   string
	thinking         string
	observedThinking string
	currentMode      string
	// summarizationPct is 0 by default, a fresh bridge's answer for an omitted threshold.
	summarizationPct      float64
	catalog               []marotte.SessionModel
	modes                 []marotte.SessionMode
	models                []marotte.SessionModel
	sessionTitle          string
	sessionTitleSetByUser bool
	calls                 []string
	notifies              []string
	// notifyParams holds each Notify's params as JSON, index-aligned with notifies.
	notifyParams            []string
	contentCollectionWrites []string
	// sessionOpts records what the most recent door was handed, engineOpts the engine it opened on.
	sessionOpts *marotte.SessionOpts
	engineOpts  *marotte.EngineOpts
	// startGate holds a door open, so a session-ready test is not saved by an instantaneous door.
	startGate chan struct{}
	// stopGate holds a Close open, as a slow session teardown does.
	stopGate chan struct{}
	// closeErr refuses every Close, leaving the session open.
	closeErr error
	// onStart runs at the top of the door, so a test can change the world while a session opens.
	onStart func()
	// startErr fails every door.
	startErr error
	// loadErr fails every door that names a session, while session/new still opens.
	loadErr error
	// starts counts doors; one factory serves chat and host sessions, so read it as a delta.
	starts int
	// notifsOnStart is the transcript a session/load replays, pushed before the door returns.
	notifsOnStart []*marotte.RPCResponse
	// withholdReplay holds back that many trailing replay frames, which the load's position still
	// counts, until releaseWithheld delivers them: a load that answered before its replay was read.
	withholdReplay int
	withheld       []*marotte.RPCResponse
	mu             sync.Mutex
	// sendMu orders a send against the close: a real bridge reads no frame once stopped.
	sendMu   sync.RWMutex
	responds int
	// respondedIDs are the ACP request ids answered, in order; responses holds each answer.
	respondedIDs []int64
	responses    map[int64]fakeResponse
	// setModelFailures fails the next N SetModel calls.
	setModelFailures int
	// supervisedApplied is whether the session took `autopilot: off`, set by Start from opts.Supervised as the real bridge does.
	supervisedApplied bool
	// supervisedAssertFails makes Start refuse the assert, the fail-open a test observes.
	supervisedAssertFails    bool
	stopped                  bool
	contentCollectionApplied bool
	contentCollectionKnown   bool
	// streamClosed guards the channel close apart from stopped, so endStream ends the stream without claiming a close.
	streamClosed bool
	started      bool
}

// newFakeBridge is an open session, as a door hands one out.
func newFakeBridge() *fakeBridge {
	return &fakeBridge{
		sessionID: "fake-sess-" + time.Now().Format("150405.000"),
		modelID:   "fake-model",
		notifCh:   make(chan marotte.Notification, 16),
		started:   true,
	}
}

// door is the fake's session/new (load false) or session/load: it records the options and, on a load, replays the
// transcript before returning, as KAS does.
func (b *fakeBridge) door(ctx context.Context, so *marotte.SessionOpts, eo *marotte.EngineOpts, load bool) error {
	if b.onStart != nil {
		b.onStart()
	}
	b.mu.Lock()
	gate := b.startGate
	startErr := b.startErr
	if load && b.loadErr != nil {
		startErr = b.loadErr
	}
	b.mu.Unlock()
	if startErr != nil {
		return startErr
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	b.started = true
	if b.stopped {
		// A door on a closed fake is a new session handle: a fresh stream, as the engine gives each door.
		b.stopped, b.streamClosed = false, false
		b.notifCh = make(chan marotte.Notification, 16)
	}
	b.starts++
	b.sessionOpts, b.engineOpts = so, eo
	if so.SessionID != "" {
		b.sessionID = so.SessionID
	}
	// Mirrors the door recording the accepted assert; false would make every supervised chat look refused.
	b.supervisedApplied = so.Supervised && !b.supervisedAssertFails
	notifs := b.notifsOnStart
	keep := len(notifs) - min(b.withholdReplay, len(notifs))
	b.mu.Unlock()
	if !load {
		return nil
	}
	for _, n := range notifs[:keep] {
		b.deliver(n)
	}
	// After the replay: CallAt's position counts the frames already delivered.
	b.mu.Lock()
	b.withheld = notifs[keep:]
	b.loadSeq = b.deliveredSeq + uint64(len(b.withheld))
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) releaseWithheld() {
	b.mu.Lock()
	frames := b.withheld
	b.withheld = nil
	b.mu.Unlock()
	for _, n := range frames {
		b.deliver(n)
	}
}

// Zero until a Start names a session.
func (b *fakeBridge) SessionLoadSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadSeq
}

func (b *fakeBridge) lastSessionOpts() *marotte.SessionOpts {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionOpts
}

// Read it as a delta.
// fake is the plain fake under a wrapper that embeds it.
func (b *fakeBridge) fake() *fakeBridge { return b }

// doorAssertFails reports whether this session's door write of content collection fails.
func (b *fakeBridge) doorAssertFails() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, fails := b.callErrs[marotte.MethodSetConfigOption]
	return fails
}

func (b *fakeBridge) startCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.starts
}

// isClosed reports whether a Close landed, which also ends the frame stream.
func (b *fakeBridge) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// Close is session/close: the frame stream ends and the session is no longer open. closeErr refuses it.
func (b *fakeBridge) Close(ctx context.Context) error {
	b.mu.Lock()
	gate, refusal := b.stopGate, b.closeErr
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if refusal != nil {
		return refusal
	}
	b.end()
	return nil
}

// end closes the session's stream as an engine end does: no session/close is written.
func (b *fakeBridge) end() {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	b.stopped = true
	if !b.streamClosed {
		b.streamClosed = true
		close(b.notifCh)
	}
	b.mu.Unlock()
}

func (b *fakeBridge) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.started && !b.stopped
}

// endStream ends the frame stream without marking the bridge stopped.
func (b *fakeBridge) endStream() {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	if !b.streamClosed {
		b.streamClosed = true
		close(b.notifCh)
	}
	b.mu.Unlock()
}

// forgetCalls drops the call record, so a test reads only the calls its own act made.
func (b *fakeBridge) forgetCalls() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls, b.lastParams, b.callDeadline = nil, nil, nil
}

func (b *fakeBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	return b.callScript(ctx, method, params, b.deliver)
}

// callScript answers one call from the script, pushing the frames it carries through deliver before returning.
func (b *fakeBridge) callScript(ctx context.Context, method string, params any, deliver func(*marotte.RPCResponse)) (*marotte.RPCResponse, error) {
	b.mu.Lock()
	b.calls = append(b.calls, method)
	if p, ok := params.(map[string]any); ok {
		if b.lastParams == nil {
			b.lastParams = map[string]map[string]any{}
		}
		b.lastParams[method] = p
	}
	if b.callDeadline == nil {
		b.callDeadline = map[string]bool{}
	}
	_, hasDeadline := ctx.Deadline()
	b.callDeadline[method] = hasDeadline
	if err, ok := b.callErrs[method]; ok {
		b.mu.Unlock()
		return nil, err
	}
	if rpcErr, ok := b.callRPCErrs[method]; ok {
		b.mu.Unlock()
		return &marotte.RPCResponse{Error: rpcErr}, nil
	}
	res := json.RawMessage(`{"stopReason":"end_turn"}`)
	if r, ok := b.callResults[method]; ok {
		res = r
	}
	chunks := b.chunksOnCall[method]
	frames := b.notifsOnCall[method]
	blocker := b.blockOn[method]
	sessionID := b.sessionID
	hook := b.onCall
	refusal := b.refuseAfterCall[method]
	failure := b.failAfterCall[method]
	b.mu.Unlock()
	if hook != nil {
		p, _ := params.(map[string]any)
		if r, hooked, ok := hook(method, p); ok {
			res, frames = r, hooked
		}
	}
	if refusal != nil {
		return &marotte.RPCResponse{Error: refusal}, nil
	}
	if failure != nil {
		return nil, failure
	}
	// Blocked outside the mutex, so other methods' Calls proceed.
	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// After unlocking, once the Call has begun; notifCh is buffered. Stamped with this bridge's session id, or the own-session screen drops them.
	for _, text := range chunks {
		deliver(newSessionChunkMsg(sessionID, text))
	}
	// Before the return, which the settle barrier relies on.
	for _, f := range frames {
		deliver(f)
	}
	return &marotte.RPCResponse{Result: res}, nil
}

// deliver stamps the next sequence and pushes, as the real read loop does.
func (b *fakeBridge) deliver(msg *marotte.RPCResponse) {
	b.sendMu.RLock()
	defer b.sendMu.RUnlock()
	b.mu.Lock()
	if b.streamClosed {
		b.mu.Unlock()
		return
	}
	b.deliveredSeq++
	seq := b.deliveredSeq
	ch := b.notifCh
	b.mu.Unlock()
	ch <- marotte.Notification{Msg: msg, Seq: seq}
}

// CallAt is Call plus the read loop position at which the response arrived.
func (b *fakeBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	b.mu.Lock()
	seq := b.deliveredSeq
	b.mu.Unlock()
	return resp, seq, err
}

// paramsFor returns the params captured for the most recent Call to method.
func (b *fakeBridge) paramsFor(method string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastParams[method]
}

func (b *fakeBridge) Notify(ctx context.Context, method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	b.mu.Lock()
	blocker := b.blockNotifyOn[method]
	b.mu.Unlock()
	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	if err := b.notifyErrs[method]; err != nil {
		b.mu.Unlock()
		return err
	}
	b.notifies = append(b.notifies, method)
	b.notifyParams = append(b.notifyParams, string(raw))
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) notified(method string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for i, m := range b.notifies {
		if m == method {
			out = append(out, b.notifyParams[i])
		}
	}
	return out
}

// fakeResponse is one answer a fake session wrote.
type fakeResponse struct {
	result any
	err    error
}

func (b *fakeBridge) Respond(_ context.Context, id int64, result any, err error) error {
	b.mu.Lock()
	b.responds++
	b.respondedIDs = append(b.respondedIDs, id)
	if b.responses == nil {
		b.responses = map[int64]fakeResponse{}
	}
	b.responses[id] = fakeResponse{result: result, err: err}
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) responseTo(id int64) (fakeResponse, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.responses[id]
	return r, ok
}

func (b *fakeBridge) answeredIDs() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.respondedIDs)
}

func (b *fakeBridge) respondCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.responds
}

func (b *fakeBridge) callLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.calls))
	copy(out, b.calls)
	return out
}

// setCallResult re-arms one method's reply under the fake's mutex, for use after the bridge runs (openBridge's
// tryLoadSession calls Call concurrently). Assigning the map before openBridge is fine.
func (b *fakeBridge) setCallResult(method string, res json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callResults == nil {
		b.callResults = map[string]json.RawMessage{}
	}
	b.callResults[method] = res
}

// setCallErr re-arms a transport failure and setCallRPCErr an in-band refusal, under the mutex.
func (b *fakeBridge) setCallErr(method string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callErrs == nil {
		b.callErrs = map[string]error{}
	}
	b.callErrs[method] = err
}

func (b *fakeBridge) setNotifyErr(method string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notifyErrs == nil {
		b.notifyErrs = map[string]error{}
	}
	b.notifyErrs[method] = err
}

func (b *fakeBridge) setCallRPCErr(method string, err *marotte.RPCError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.callRPCErrs == nil {
		b.callRPCErrs = map[string]*marotte.RPCError{}
	}
	b.callRPCErrs[method] = err
}

// setStartGate parks every later Start until the returned channel closes.
func (b *fakeBridge) setStartGate(gate chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.startGate = gate
}

func (b *fakeBridge) lastCall() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return ""
	}
	return b.calls[len(b.calls)-1]
}

func (b *fakeBridge) SessionID() marotte.SessionID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return marotte.SessionID(b.sessionID)
}

// SupervisedApplied reports whether the session accepted `autopilot: off`. False by default, the honest zero:
// a refusal-report test sets it itself.
func (b *fakeBridge) SupervisedApplied() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.supervisedApplied
}

// AutoCompactionDisabled mirrors the real session: the value the last door SENT.
func (b *fakeBridge) AutoCompactionDisabled() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionOpts != nil && b.sessionOpts.DisableAutoCompaction
}

func (b *fakeBridge) ModelID() marotte.ModelID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return marotte.ModelID(b.modelID)
}

// CurrentMode is the mode the session ended in, settable to simulate a divergence.
func (b *fakeBridge) CurrentMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentMode
}

// SessionTitle returns whatever a test set, so the adoption guard can be exercised.
func (b *fakeBridge) SessionTitle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitle
}

func (b *fakeBridge) SessionTitleSetByUser() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionTitleSetByUser
}

// SummarizationThreshold returns whatever a test set; zero is a fresh bridge's answer.
func (b *fakeBridge) SummarizationThreshold() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.summarizationPct
}

// Modes and Models are nil by default, a fresh bridge's answer for omitted fields.
func (b *fakeBridge) Modes() []marotte.SessionMode {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modes
}

func (b *fakeBridge) Catalog() []marotte.SessionModel {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.catalog != nil {
		return b.catalog
	}
	return b.models
}

func (b *fakeBridge) Models() []marotte.SessionModel {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.models
}

func (b *fakeBridge) SetModel(_ context.Context, modelID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, "session/set_config_option")
	// Fail the next N swaps.
	if b.setModelFailures > 0 {
		b.setModelFailures--
		return errors.New("fake: model swap refused")
	}
	b.modelID = modelID
	return nil
}

func (b *fakeBridge) EnsureEffort(_ context.Context, level string) error {
	b.mu.Lock()
	b.effort = level
	b.calls = append(b.calls, "session/set_config_option")
	b.mu.Unlock()
	return nil
}

// ObserveEffort records the level without a second copy of EnsureEffort's cache rule; the healEffort tests
// assert what the heal decided to call.
func (b *fakeBridge) ObserveEffort(level string) {
	b.mu.Lock()
	b.observedEffort = level
	b.mu.Unlock()
}

func (b *fakeBridge) EnsureThinking(_ context.Context, choice string) error {
	b.mu.Lock()
	b.thinking = choice
	b.calls = append(b.calls, "session/set_config_option thinking")
	b.mu.Unlock()
	return nil
}

func (b *fakeBridge) ObserveThinking(value string) {
	b.mu.Lock()
	b.observedThinking = value
	b.mu.Unlock()
}

// Empty if never told.
func (b *fakeBridge) lastObservedEffort() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observedEffort
}

// Empty if never called.
func (b *fakeBridge) lastEffort() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.effort
}

func (b *fakeBridge) NotifCh() <-chan marotte.Notification {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.notifCh
}

type testChatStore struct {
	*chat.Store
	// Gets counts Get calls, for a test about how often the store is read.
	Gets atomic.Int64
}

// testChatRoot holds every test hub's chat directory, created and removed by TestMain.
var (
	testChatRoot string
	testChatSeq  atomic.Int64
)

func newTestChatStore() *testChatStore {
	dir := filepath.Join(testChatRoot, strconv.FormatInt(testChatSeq.Add(1), 10))
	s, err := chat.NewStore(dir)
	if err != nil {
		panic("test chat store: " + err.Error())
	}
	return &testChatStore{Store: s}
}

// wire hands the store the runtime seams the composition root wires after New.
func (s *testChatStore) wire(h *Runtime) {
	chat.WithBroadcaster(h)(s.Store)
	chat.WithLiveTurn(h.TurnLive)(s.Store)
	chat.WithOpenTurns(h.OpenTurns)(s.Store)
}

func (s *testChatStore) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	s.Gets.Add(1)
	return s.Store.Get(ctx, id)
}

// seed writes a header for id through Mutate, staging a chat before the runtime touches it.
func (s *testChatStore) seed(tb testing.TB, id marotte.ChatID, fill func(c *marotte.Chat)) {
	tb.Helper()
	if _, err := s.Mutate(tb.Context(), id, func(c *marotte.Chat, _ bool) bool {
		if fill != nil {
			fill(c)
		}
		return true
	}); err != nil {
		tb.Fatalf("seed chat %s: %v", id, err)
	}
}

// Forward runs one session's forward loop in the caller, taking the chat's forward attachment first.
func (bc *bridgeCoordinator) Forward(chatID marotte.ChatID, bridge engineSession) {
	bc.forwardAt(chatID, bridge, bc.turns.attachForward(chatID))
}
