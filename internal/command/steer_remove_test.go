package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// steerBridge answers each method from a script and records every call in order,
// with whether the call's context was still live when it went out.
type steerBridge struct {
	*recordingBridge
	results   map[string]any
	errs      map[string]error
	onCall    func(method string)
	notifyErr error
	calls     []steerCall
	mu        sync.Mutex
}

type steerCall struct {
	params map[string]any
	method string
	live   bool
}

func newSteerBridge() *steerBridge {
	return &steerBridge{
		recordingBridge: &recordingBridge{sessionID: "sess-1"},
		results: map[string]any{
			marotte.MethodSessionSteerClear: map[string]any{"cleared": true, "messageIds": []string{}},
			marotte.MethodSessionSteer:      map[string]any{"queued": true},
		},
		errs: map[string]error{},
	}
}

func (b *steerBridge) record(ctx context.Context, method string, params any) {
	m, _ := params.(map[string]any)
	b.mu.Lock()
	b.calls = append(b.calls, steerCall{method: method, params: m, live: ctx.Err() == nil})
	b.mu.Unlock()
	if b.onCall != nil {
		b.onCall(method)
	}
}

func (b *steerBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	b.record(ctx, method, params)
	if err := b.errs[method]; err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b.results[method])
	if err != nil {
		return nil, err
	}
	return &marotte.RPCResponse{Result: raw}, nil
}

func (b *steerBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

func (b *steerBridge) Notify(ctx context.Context, method string, params any) error {
	b.record(ctx, method, params)
	return b.notifyErr
}

func (b *steerBridge) methods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.calls))
	for _, c := range b.calls {
		out = append(out, c.method)
	}
	return out
}

func (b *steerBridge) callsOf(method string) []steerCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []steerCall
	for _, c := range b.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func removeReq(t *testing.T, chatID marotte.ChatID, key string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SteerRemoveCommand{SteerID: key})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSteerRemove, ChatID: chatID, Payload: payload}
}

func removeHost(b Bridge) hostDouble {
	return newBridgeHost(testsupport.NewInMemoryChatStore(), b)
}

// Every refusal the record can answer reaches the reader as its own class, and the
// one that means "already read" keeps the not_waiting class the client branches on.
func TestCmdSteerRemove_EachRefusalReachesTheReaderAsItsClass(t *testing.T) {
	cases := []struct {
		refuse     string
		wantReason string
		wantStatus int
	}{
		{refuse: SteerRefuseNotWaiting, wantStatus: http.StatusNotFound, wantReason: SteerRefuseNotWaiting},
		{refuse: SteerRefuseNotUser, wantStatus: http.StatusConflict, wantReason: SteerRefuseNotUser},
		{refuse: SteerRefuseAgentRows, wantStatus: http.StatusConflict, wantReason: SteerRefuseAgentRows},
		{refuse: SteerRefuseSettling, wantStatus: http.StatusConflict, wantReason: SteerRefuseSettling},
		{refuse: SteerRefuseStarting, wantStatus: http.StatusConflict, wantReason: SteerRefuseStarting},
		{refuse: SteerRefuseChatGone, wantStatus: http.StatusConflict, wantReason: SteerRefuseChatGone},
		{refuse: SteerRefuseConsumed, wantStatus: http.StatusConflict, wantReason: SteerRefuseNotWaiting},
		{refuse: SteerRefuseNoReply, wantStatus: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.refuse, func(t *testing.T) {
			b := newSteerBridge()
			q := clearingQueue()
			q.removeRef = tc.refuse

			_, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-a"))

			if statusOf(err) != tc.wantStatus || reasonOf(err) != tc.wantReason {
				t.Errorf("CmdSteerRemove(refused %q) = %d %q, want %d %q",
					tc.refuse, statusOf(err), reasonOf(err), tc.wantStatus, tc.wantReason)
			}
			if got := b.methods(); len(got) != 0 {
				t.Errorf("a refused delete reached the wire: %v", got)
			}
		})
	}
}

// A row KAS does not hold goes at once: no clear, nothing resent.
func TestCmdSteerRemove_ARowKASDoesNotHoldGoesWithoutAClear(t *testing.T) {
	b := newSteerBridge()

	body, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), newStubSteerQueue()), removeReq(t, "c1", "steer-a"))
	if err != nil {
		t.Fatalf("CmdSteerRemove = %v, want success", err)
	}
	if got := body.(map[string]any); got["deleted"] != "steer-a" {
		t.Errorf("body = %v, want deleted steer-a", got)
	}
	if got := b.methods(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}

// A row KAS may hold costs a clear, and the rows the reader kept go back as ONE
// steer under a fresh id, recorded in the ledger as the user's.
func TestCmdSteerRemove_ClearsThenResendsTheKeptRowsAsOneSteer(t *testing.T) {
	b := newSteerBridge()
	b.results[marotte.MethodSessionSteerClear] = map[string]any{"cleared": true, "messageIds": []string{"steer-a", "steer-b", "steer-c"}}
	q := clearingQueue()
	q.removeRes = SteerOpResult{Resend: &SteerSend{ID: "steer-p", Text: "first\n\nthird", Keys: []string{"steer-a", "steer-c"}}}
	ledger := NewSteerLedger()

	body, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), ledger, q), removeReq(t, "c1", "steer-b"))
	if err != nil {
		t.Fatalf("CmdSteerRemove = %v, want success", err)
	}
	if got := body.(map[string]any); got["deleted"] != "steer-b" {
		t.Errorf("body = %v, want deleted steer-b", got)
	}
	if got, want := b.methods(), []string{marotte.MethodSessionSteerClear, marotte.MethodSessionSteer}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	steer := b.callsOf(marotte.MethodSessionSteer)[0].params
	if steer["messageId"] != "p" || steer["message"] != "first\n\nthird" {
		t.Errorf("resend params = %v, want messageId p and the kept rows joined in order", steer)
	}
	if !slices.Equal(q.cleared, []string{"steer-a", "steer-b", "steer-c"}) {
		t.Errorf("record told cleared = %v, want the clear's own ids", q.cleared)
	}
	if got := ledger.SteerOrigin("c1", "steer-p"); got != marotte.SteerOriginUser {
		t.Errorf("ledger origin(steer-p) = %q, want %q", got, marotte.SteerOriginUser)
	}
	wantOrder := []string{"lock", "begin-remove steer-b", "await", "remove-cleared", "op-sent steer-p", "end-op", "unlock"}
	if got := q.callLog(); !slices.Equal(got, wantOrder) {
		t.Errorf("record calls = %v, want %v", got, wantOrder)
	}
}

// A clear with no reply may or may not have emptied the buffer, so the record is
// told so and nothing is resent: the reader is told nothing changed.
func TestCmdSteerRemove_AClearWithNoReplyIsAGatewayErrorAndSendsNothing(t *testing.T) {
	b := newSteerBridge()
	b.errs[marotte.MethodSessionSteerClear] = errors.New("pipe closed")
	q := clearingQueue()
	q.removeRes = SteerOpResult{Reason: SteerRefuseNoReply}

	_, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body %s)", statusOf(err), errText(err))
	}
	if !slices.Contains(q.callLog(), "remove-no-reply") {
		t.Errorf("record calls = %v, want the clear reported as unanswered", q.callLog())
	}
	if got := b.callsOf(marotte.MethodSessionSteer); len(got) != 0 {
		t.Errorf("a steer went out after an unanswered clear: %v", got)
	}
}

// A clear whose reply arrived before the frames KAS wrote ahead of it could fold
// cannot say which rows were read first, so nothing is classified from its ids.
func TestCmdSteerRemove_AClearWhoseFramesNeverFoldChangesNothing(t *testing.T) {
	b := newSteerBridge()
	b.results[marotte.MethodSessionSteerClear] = map[string]any{"cleared": true, "messageIds": []string{"steer-b"}}
	q := clearingQueue()
	q.unfolded = true
	q.removeRes = SteerOpResult{Reason: SteerRefuseNoReply}

	_, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body %s)", statusOf(err), errText(err))
	}
	if !slices.Contains(q.callLog(), "remove-no-reply") || q.cleared != nil {
		t.Errorf("record calls = %v cleared %v, want the clear reported unsettled with no ids", q.callLog(), q.cleared)
	}
	if got := b.callsOf(marotte.MethodSessionSteer); len(got) != 0 {
		t.Errorf("a steer went out after an unsettled clear: %v", got)
	}
}

// Once the target is deleted the delete has happened, whatever its resubmit does:
// a failed resend is never a failure that would make Edit restore text the dock no
// longer shows.
func TestCmdSteerRemove_AFailedResubmitStillAnswersDeleted(t *testing.T) {
	b := newSteerBridge()
	b.errs[marotte.MethodSessionSteer] = errors.New("pipe closed")
	q := clearingQueue()
	q.removeRes = SteerOpResult{Resend: &SteerSend{ID: "steer-p", Text: "first", Keys: []string{"steer-a"}}}

	body, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b"))
	if err != nil {
		t.Fatalf("CmdSteerRemove = %v, want success", err)
	}
	if got := body.(map[string]any); got["deleted"] != "steer-b" {
		t.Errorf("body = %v, want deleted steer-b", got)
	}
	if len(q.sent) != 1 || q.sent[0].err == nil {
		t.Errorf("record told %+v, want the resend's error", q.sent)
	}
}

// A target the agent read first is not deleted, but the rows the reader kept were
// cleared with it, so they still go back before the refusal is answered.
func TestCmdSteerRemove_AConsumedTargetStillResubmitsTheKeptRows(t *testing.T) {
	b := newSteerBridge()
	q := clearingQueue()
	q.removeRes = SteerOpResult{Reason: SteerRefuseConsumed, Resend: &SteerSend{ID: "steer-p", Text: "kept", Keys: []string{"steer-a"}}}

	_, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != SteerRefuseNotWaiting {
		t.Errorf("status = %d %q, want 409 %q", statusOf(err), reasonOf(err), SteerRefuseNotWaiting)
	}
	if got := b.callsOf(marotte.MethodSessionSteer); len(got) != 1 {
		t.Errorf("steer calls = %d, want the kept rows resent once", len(got))
	}
}

// TestCmdSteerRemove_ATurnThatClosedUnderTheOpEndsThroughEndOp: EndOp answers the end and the op
// resolves it under the lock it still holds, whether the end landed before the resend or after.
func TestCmdSteerRemove_ATurnThatClosedUnderTheOpEndsTheOpBeforeTheUnlock(t *testing.T) {
	b := newSteerBridge()
	q := clearingQueue()

	if _, err := CmdSteerRemove(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b")); err != nil {
		t.Fatalf("CmdSteerRemove = %v, want success", err)
	}
	log := q.callLog()
	end, unlock := slices.Index(log, "end-op"), slices.Index(log, "unlock")
	if end < 0 || unlock < end || slices.Contains(log, "job-rows") {
		t.Errorf("record calls = %v, want end-op before unlock and no end resolved by the op", log)
	}
}

// A stale bind that ended the op's channel settled the op's rows; the op routes
// them into the started prompt before it unlocks.
func TestCmdSteerClear_AStaleBindUnderTheClearRoutesBeforeTheUnlock(t *testing.T) {
	b := newSteerBridge()
	q := clearingQueue()
	q.discardRes = SteerOpResult{Reason: SteerRefuseNoReply}
	q.reroute = true
	q.parked = []string{"steer-w"}
	host := &promptHolderHost{hostDouble: removeHost(b), turn: "t-p"}

	_, err := CmdSteerClear(t.Context(), steerRolesOf(host, NewSteerLedger(), q), clearReq("c1"))
	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body %s)", statusOf(err), errText(err))
	}
	log := q.callLog()
	end, route, unlock := slices.Index(log, "end-op"), slices.Index(log, "route steer-w"), slices.Index(log, "unlock")
	if end < 0 || route < end || unlock < route {
		t.Errorf("record calls = %v, want end-op, then the reroute, then unlock", log)
	}
}

// promptHolderHost names a started prompt as the admission's prompt holder.
type promptHolderHost struct {
	hostDouble
	turn string
}

func (h *promptHolderHost) PromptHolder(marotte.ChatID) (string, bool) { return h.turn, h.turn != "" }

func (h *promptHolderHost) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return marotte.TurnSourcePrompt, true
}

// A reader who closes the tab mid-delete must not strand the rows it kept: the
// clear and the resubmit run on a context the request's cancel does not reach.
func TestCmdSteerRemove_ADisconnectMidOpStillResubmits(t *testing.T) {
	b := newSteerBridge()
	ctx, cancel := context.WithCancel(t.Context())
	b.onCall = func(method string) {
		if method == marotte.MethodSessionSteerClear {
			cancel()
		}
	}
	q := clearingQueue()
	q.removeRes = SteerOpResult{Resend: &SteerSend{ID: "steer-p", Text: "kept", Keys: []string{"steer-a"}}}

	if _, err := CmdSteerRemove(ctx, steerRolesOf(removeHost(b), NewSteerLedger(), q), removeReq(t, "c1", "steer-b")); err != nil {
		t.Fatalf("CmdSteerRemove = %v, want success", err)
	}
	steers := b.callsOf(marotte.MethodSessionSteer)
	if len(steers) != 1 || !steers[0].live {
		t.Errorf("resend calls = %+v, want one on a live context after the reader left", steers)
	}
}

func TestCmdSteerClear_ARefusalReachesTheReaderAsItsClass(t *testing.T) {
	b := newSteerBridge()
	q := clearingQueue()
	q.discardRef = SteerRefuseChatGone

	_, err := CmdSteerClear(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), clearReq("c1"))

	if statusOf(err) != http.StatusConflict || reasonOf(err) != SteerRefuseChatGone {
		t.Errorf("status = %d %q, want 409 %q", statusOf(err), reasonOf(err), SteerRefuseChatGone)
	}
	if got := b.methods(); len(got) != 0 {
		t.Errorf("a refused discard reached the wire: %v", got)
	}
}

func TestCmdSteerClear_AClearWithNoReplyIsAGatewayError(t *testing.T) {
	for _, tc := range []struct {
		setup func(b *steerBridge, q *stubSteerQueue)
		name  string
	}{
		{name: "no reply", setup: func(b *steerBridge, _ *stubSteerQueue) {
			b.errs[marotte.MethodSessionSteerClear] = errors.New("pipe closed")
		}},
		{name: "frames that never fold", setup: func(_ *steerBridge, q *stubSteerQueue) { q.unfolded = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSteerBridge()
			q := clearingQueue()
			tc.setup(b, q)
			q.discardRes = SteerOpResult{Reason: SteerRefuseNoReply}

			_, err := CmdSteerClear(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), q), clearReq("c1"))

			if statusOf(err) != http.StatusBadGateway {
				t.Errorf("status = %d, want 502 (body %s)", statusOf(err), errText(err))
			}
			if !slices.Contains(q.callLog(), "discard-no-reply") {
				t.Errorf("record calls = %v, want the clear reported as unsettled", q.callLog())
			}
		})
	}
}

// Nothing KAS may hold means nothing to clear: the discard costs no RPC.
func TestCmdSteerClear_NothingInKASCostsNoClear(t *testing.T) {
	b := newSteerBridge()

	body, err := CmdSteerClear(t.Context(), steerRolesOf(removeHost(b), NewSteerLedger(), newStubSteerQueue()), clearReq("c1"))
	if err != nil {
		t.Fatalf("CmdSteerClear = %v, want success", err)
	}
	if got := b.methods(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
	if cleared, _ := body.(map[string]any)["cleared"].([]string); cleared == nil || len(cleared) != 0 {
		t.Errorf("cleared = %#v, want an empty list", body.(map[string]any)["cleared"])
	}
}

// resendHost scripts admission for the turn-end resend and records the prompt
// entry the resend opens.
type resendHost struct {
	*bridgeDeps
	onReserve func()
	openErr   error
	admit     AdmissionOutcome
	opened    []*marotte.EntryPrompt
	mu        sync.Mutex
	inflight  int
	drained   chan struct{}
}

func newResendHost(b Bridge, admit AdmissionOutcome, holder marotte.TurnOpenSource, held bool) *resendHost {
	d := &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: &benchDeps{holder: holder, holderOpen: held}, store: testsupport.NewInMemoryChatStore()},
		bridge:    b,
	}
	return &resendHost{bridgeDeps: d, admit: admit, drained: make(chan struct{}, 1)}
}

func (h *resendHost) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	if h.onReserve != nil {
		h.onReserve()
	}
	return h.admit
}

func (h *resendHost) OpenTurn(_ context.Context, _ marotte.ChatID, open TurnOpen) (string, error) {
	p := open.Prompt
	if h.openErr != nil {
		return "", h.openErr
	}
	h.mu.Lock()
	h.opened = append(h.opened, p)
	h.mu.Unlock()
	return benchTurnID, nil
}

// OpenBridge refuses, so the prompt goroutine the resend starts ends at once.
func (h *resendHost) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return nil, errors.New("no bridge in this test")
}

func (h *resendHost) InflightAdd(delta int) {
	h.mu.Lock()
	h.inflight += delta
	h.mu.Unlock()
}

// InflightDone signals drained when the count returns to zero, not on each Done:
// a turn end resolved on its own goroutine registers before the prompt goroutine
// it launches, so the prompt can finish while the resolution has not yet
// delivered the rows.
func (h *resendHost) InflightDone() {
	h.mu.Lock()
	h.inflight--
	idle := h.inflight == 0
	h.mu.Unlock()
	if !idle {
		return
	}
	select {
	case h.drained <- struct{}{}:
	default:
	}
}

// waitInflight waits for every goroutine registered in flight to finish.
func (h *resendHost) waitInflight(t *testing.T) {
	t.Helper()
	select {
	case <-h.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the resend's in-flight goroutines never finished")
	}
}

// drainRoles wires a resend host and a scripted record into the drain's roles.
func drainRoles(h hostDouble, q *stubSteerQueue) *promptRoles {
	return steerRolesOf(h, NewSteerLedger(), q)
}

var cleanClose = CloseFacts{Outcome: marotte.TurnOutcomeCompleted, Fence: TurnFence{Epoch: 1, Seq: 1}}

// What a turn leaves unread goes back together, in order, as the next prompt, and
// the prompt names the rows it carries; the rows retire only once it opened.
func TestDrain_UnreadSteersGoAsOnePromptNamingTheirKeys(t *testing.T) {
	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	q := newStubSteerQueue()
	q.unsentRows = []SteerRow{{Key: "steer-a", Text: "first"}, {Key: "steer-b", Text: "second"}}

	drainAfterClose(t.Context(), drainRoles(h, q), "c1", cleanClose, EndFacts{})
	h.waitInflight(t)

	if len(h.opened) != 1 {
		t.Fatalf("prompts opened = %d, want 1", len(h.opened))
	}
	if p := h.opened[0]; p.Text != "first\n\nsecond" || !slices.Equal(p.Resends, []string{"steer-a", "steer-b"}) {
		t.Errorf("prompt = %+v, want the rows joined in order, naming both keys", p)
	}
	if !slices.Equal(q.delivered, []string{"steer-a", "steer-b"}) {
		t.Errorf("delivered = %v, want both keys retired after the open", q.delivered)
	}
}

// A drain whose prompt cannot open keeps custody of the rows: they stay unsent
// rather than vanishing with the refused open.
func TestDrain_APromptThatCannotOpenLeavesTheRowsUnsent(t *testing.T) {
	for _, tc := range []struct {
		err  error
		name string
	}{
		{name: "the open fails", err: errors.New("disk full")},
		{name: "a newer turn opened", err: ErrTurnSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
			h.openErr = tc.err
			q := newStubSteerQueue()
			q.unsentRows = []SteerRow{{Key: "steer-a", Text: "first"}}

			drainAfterClose(t.Context(), drainRoles(h, q), "c1", cleanClose, EndFacts{})

			if len(q.delivered) != 0 {
				t.Errorf("delivered = %v, want nothing retired by a refused open", q.delivered)
			}
		})
	}
}

// A pending end belongs to a newer close or to a shutdown, so this close's drain
// sends nothing while one is listed.
func TestDrain_APendingEndHoldsBackEverySend(t *testing.T) {
	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	q := newStubSteerQueue()
	q.ends = []SteerEnd{{Owner: "end-2", End: SteerTurnEnd{TurnSeq: 2}}}
	q.unsentRows = []SteerRow{{Key: "steer-a", Text: "first"}}

	drainAfterClose(t.Context(), drainRoles(h, q), "c1", cleanClose, EndFacts{})

	if slices.Contains(q.callLog(), "unsent-rows") || len(h.opened) != 0 {
		t.Errorf("record calls = %v, opened = %d, want no send while an end is pending", q.callLog(), len(h.opened))
	}
}

// A close resolves only the ends of turns that opened at or before it, so an older
// pipeline never consumes a newer close's end.
func TestResolveEnds_LeavesANewerTurnsEnd(t *testing.T) {
	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	q := newStubSteerQueue()
	q.ends = []SteerEnd{{Owner: "end-1", End: SteerTurnEnd{TurnSeq: 1}}, {Owner: "end-2", End: SteerTurnEnd{TurnSeq: 2}}}
	q.jobRows = []SteerRow{{Key: "steer-a", Text: "first"}}

	ResolveEnds(t.Context(), drainRoles(h, q), "c1", 1)

	if got := q.Ends("c1"); len(got) != 1 || got[0].Owner != "end-2" {
		t.Errorf("ends left = %+v, want only the newer turn's", got)
	}
}

// Rows KAS may still hold are settled before they go unsent, so none is delivered
// twice: a dead buffer proves them gone, a clear's reply proves it, and with an
// agent row waiting (a clear would drop it) or any admission holder they stay for
// the next cursor.
func TestResolveEnds_AStrayIsClearedOnlyWithNoHolder(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	cases := []struct {
		end       SteerTurnEnd
		name      string
		wantLog   string
		holder    marotte.TurnOpenSource
		held      bool
		agentRows bool
		wantClear bool
	}{
		{name: "a dead buffer", end: SteerTurnEnd{BridgeDeath: true, Exit: closed}, wantLog: "strays-all"},
		{name: "a clear's reply", wantLog: "strays-all", wantClear: true},
		{name: "an agent row waiting", agentRows: true, wantLog: "release-in-kas"},
		{name: "a prompt holding the chat", held: true, holder: marotte.TurnSourcePrompt, wantLog: "release-in-kas"},
		{name: "a bare reservation", held: true, holder: marotte.TurnSourceLocalShell, wantLog: "release-in-kas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newSteerBridge()
			h := newResendHost(b, AdmissionAcquired, tc.holder, tc.held)
			q := newStubSteerQueue()
			q.agentRows = tc.agentRows
			q.ends = []SteerEnd{{Owner: "end-1", End: tc.end}}
			q.jobRows = []SteerRow{{Key: "steer-a", Text: "first", InKAS: true}}

			ResolveEnds(t.Context(), drainRoles(h, q), "c1", 1)

			log := q.callLog()
			if !slices.Contains(log, tc.wantLog) || !slices.Contains(log, "end-unsent") {
				t.Errorf("record calls = %v, want %q and the end made unsent", log, tc.wantLog)
			}
			if got := len(b.callsOf(marotte.MethodSessionSteerClear)) == 1; got != tc.wantClear {
				t.Errorf("clear issued = %v, want %v (calls %v)", got, tc.wantClear, b.methods())
			}
			if got := b.callsOf(marotte.MethodSessionSteer); len(got) != 0 {
				t.Errorf("a row KAS may hold was sent again: %+v", got)
			}
		})
	}
}

// A dead bridge's frames must fold before its rows are sent as prompt text; a wait
// that ends without the drain is reported, so the close's drain withholds them.
func TestResolveEnds_ADeadBridgeThatHasNotDrainedIsReported(t *testing.T) {
	was := ResendBridgeWait
	ResendBridgeWait = time.Millisecond
	t.Cleanup(func() { ResendBridgeWait = was })
	h := newResendHost(newSteerBridge(), AdmissionAcquired, 0, false)
	q := newStubSteerQueue()
	q.ends = []SteerEnd{{Owner: "end-1", End: SteerTurnEnd{BridgeDeath: true, Exit: make(chan struct{})}}}
	q.jobRows = []SteerRow{{Key: "steer-a", Text: "first", InKAS: true}}

	facts := ResolveEnds(t.Context(), drainRoles(h, q), "c1", 1)

	if !facts.Death || !facts.Undrained {
		t.Errorf("facts = %+v, want an undrained death", facts)
	}
	if got := q.callLog(); !slices.Contains(got, "end-unsent") {
		t.Errorf("record calls = %v, want the rows made unsent", got)
	}
}

// The send-now arrow's row is recorded before the cancel goes out, so the turn
// end it causes resends that row first; a cancel that never reached KAS ends no
// turn, so the lead is undone.
func TestCmdCancel_TheLeadIsSetBeforeTheCancelAndUndoneIfItFails(t *testing.T) {
	for _, tc := range []struct {
		notifyErr error
		name      string
		want      []string
	}{
		{name: "the cancel lands", want: []string{"lead steer-a"}},
		{name: "the cancel fails", notifyErr: errors.New("pipe closed"), want: []string{"lead steer-a", "lead-undone steer-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSteerBridge()
			b.notifyErr = tc.notifyErr
			q := newStubSteerQueue()
			var order []string
			b.onCall = func(method string) { order = append(order, method+" after "+strings.Join(q.callLog(), ",")) }
			host := removeHost(b)
			cmd := &marotte.ClientCommand{Type: marotte.CmdCancel, ChatID: "c1", Payload: json.RawMessage(`{"lead":"steer-a"}`)}

			if _, err := CmdCancel(t.Context(), host, host, host, host, q, cmd); err != nil {
				t.Fatalf("CmdCancel = %v", err)
			}
			if got := q.callLog(); !slices.Equal(got, tc.want) {
				t.Errorf("record calls = %v, want %v", got, tc.want)
			}
			if len(order) != 1 || order[0] != marotte.MethodCancel+" after lead steer-a" {
				t.Errorf("wire = %v, want the cancel after the lead was set", order)
			}
		})
	}
}

// orderTeardown records the teardown's steps into a log the bridge shares.
type orderTeardown struct {
	log *[]string
}

func (o orderTeardown) BeginChatTeardown(_ marotte.ChatID, keep bool) {
	*o.log = append(*o.log, "begin keep="+strconv.FormatBool(keep))
}

func (o orderTeardown) CloseChatState(context.Context, marotte.ChatID) {
	*o.log = append(*o.log, "close")
}

func (o orderTeardown) DeleteChatState(context.Context, marotte.ChatID) {
	*o.log = append(*o.log, "delete")
}

func (o orderTeardown) DeleteChatStateByChain(_ context.Context, _ marotte.ChatID, _ []string, cause RunStopCause) {
	entry := "delete-by-chain"
	if cause == RunStopTabClosed {
		entry += " tab-closed"
	}
	*o.log = append(*o.log, entry)
}

// The steer record is torn down BEFORE the teardown's cancel: the cancel ends the
// turn, and a turn end that found a live record would resend rows into a chat
// that is closing.
func TestChatTeardown_TheSteerRecordGoesBeforeTheCancel(t *testing.T) {
	for _, tc := range []struct {
		run  func(ctx context.Context, h hostDouble, td ChatTeardown)
		name string
		want []string
	}{
		{name: "close", run: func(ctx context.Context, h hostDouble, td ChatTeardown) {
			closeChatTeardown(ctx, h, h, h, td, "c1")
		}, want: []string{"begin keep=true", marotte.MethodCancel, "close"}},
		{name: "delete", run: func(ctx context.Context, h hostDouble, td ChatTeardown) {
			deleteChatTeardown(ctx, h, h, h, td, "c1", []string{"sess-1"})
		}, want: []string{"begin keep=false", marotte.MethodCancel, "delete-by-chain tab-closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			b := newSteerBridge()
			b.onCall = func(method string) { log = append(log, method) }

			tc.run(t.Context(), removeHost(b), orderTeardown{log: &log})

			if !slices.Equal(log, tc.want) {
				t.Errorf("teardown order = %v, want %v", log, tc.want)
			}
		})
	}
}
