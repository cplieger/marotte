package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/marotte"
)

type promptKey struct {
	chat marotte.ChatID
	id   string
}

type mergeHost struct {
	*resendHost
	busy map[marotte.ChatID]bool
	// unbound names the chats whose findings turn ends without KAS's turn_bind, with the reason
	// its turn_close carries.
	unbound    map[marotte.ChatID]string
	receipts   map[promptKey]marotte.PromptReceipt
	onMergeEvt func(marotte.ServerEvent)
	result     marotte.TurnResult
	events     []marotte.ServerEvent
	// loadErr is the parent load's answer; loadGate, when set, holds the load until it is closed,
	// and loading receives one value per load entered.
	loadErr  error
	loadGate chan struct{}
	loading  chan struct{}
	// receiptErr is every parent receipt read's error.
	receiptErr error
	// writeErr fails every write to the tangent's record once armed, which the summary turn's
	// await does when failWrites is set.
	writeErr   error
	failWrites error
	// reads orders the parent loads and log reads a resumed merge makes.
	reads    []string
	released int
	evMu     sync.Mutex
}

func (h *mergeHost) TryReserveTurn(chatID marotte.ChatID, _ marotte.TurnOpenSource) bool {
	return !h.busy[chatID]
}

func (h *mergeHost) AwaitTurn(_ context.Context, chatID marotte.ChatID, _ string) (marotte.TurnResult, error) {
	if chatID == "c-tangent" {
		h.evMu.Lock()
		h.writeErr = cmp.Or(h.writeErr, h.failWrites)
		h.evMu.Unlock()
	}
	if reason, ok := h.unbound[chatID]; ok {
		return marotte.TurnResult{Stop: marotte.StopReasonInterrupted, Reason: reason}, nil
	}
	return h.result, nil
}

func (h *mergeHost) AwaitTurnBound(_ context.Context, chatID marotte.ChatID, _ string) (bool, error) {
	_, unbound := h.unbound[chatID]
	return !unbound, nil
}

func (h *mergeHost) ReleaseTurn(marotte.ChatID, string) {
	h.evMu.Lock()
	h.released++
	h.evMu.Unlock()
}

func (h *mergeHost) LoadSession(ctx context.Context, chatID marotte.ChatID) error {
	h.evMu.Lock()
	h.reads = append(h.reads, "load "+string(chatID))
	gate, loading, err := h.loadGate, h.loading, h.loadErr
	h.evMu.Unlock()
	if loading != nil {
		loading <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (h *mergeHost) Mutate(ctx context.Context, id marotte.ChatID, fn func(c *marotte.Chat, exists bool) bool) (string, error) {
	h.evMu.Lock()
	err := h.writeErr
	h.evMu.Unlock()
	if err != nil && id == "c-tangent" {
		return "", err
	}
	return h.resendHost.Mutate(ctx, id, fn)
}

func (h *mergeHost) readsSoFar() []string {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	return slices.Clone(h.reads)
}

func (h *mergeHost) PromptReceipt(_ context.Context, chatID marotte.ChatID, promptID string) (marotte.PromptReceipt, error) {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	h.reads = append(h.reads, "receipt "+string(chatID))
	return h.receipts[promptKey{chat: chatID, id: promptID}], h.receiptErr
}

func (h *mergeHost) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	if isMergeFrame(evt) && h.onMergeEvt != nil {
		h.onMergeEvt(evt)
	}
	h.evMu.Lock()
	h.events = append(h.events, evt)
	h.evMu.Unlock()
}

func isMergeFrame(e marotte.ServerEvent) bool {
	if e.Type == marotte.EventTangentMerged {
		return true
	}
	p, ok := e.Payload.(marotte.ErrorPayload)
	return ok && p.Code == marotte.ErrCodeTangentMergeFailed
}

func (h *mergeHost) mergeFrames() []marotte.ServerEvent {
	h.evMu.Lock()
	defer h.evMu.Unlock()
	var out []marotte.ServerEvent
	for _, e := range h.events {
		if isMergeFrame(e) {
			out = append(out, e)
		}
	}
	return out
}

func (h *mergeHost) promptCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.opened)
}

type fakeTangents struct {
	err    error
	parent marotte.ChatID
	reply  string
}

func (f *fakeTangents) TangentParent(context.Context, marotte.ChatID) (marotte.ChatID, error) {
	return f.parent, f.err
}

func (f *fakeTangents) TurnReply(context.Context, marotte.ChatID, string) (string, error) {
	return f.reply, nil
}

type mergeFixture struct {
	host   *mergeHost
	roles  *promptRoles
	mem    *Membership
	merges *tangentMerges
	queue  *fakeQueue
	tabs   interface {
		List() ([]marotte.TabSubject, uint64)
	}
}

var tangentOp1 = mergeKey{tangent: "c-tangent", op: "op-1"}

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	h := &mergeHost{
		resendHost: newResendHost(newSteerBridge(), AdmissionAcquired, 0, false),
		busy:       map[marotte.ChatID]bool{},
		unbound:    map[marotte.ChatID]string{},
		receipts:   map[promptKey]marotte.PromptReceipt{},
		result:     marotte.TurnResult{Stop: marotte.StopReasonEndTurn, WireEnded: true},
	}
	for _, id := range []marotte.ChatID{"c-tangent", "c-parent"} {
		if _, err := h.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
			c.Name = map[marotte.ChatID]string{"c-tangent": "Try the cache", "c-parent": "Main"}[id]
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
	roles := promptRolesOf(h)
	q := &fakeQueue{chat: &marotte.Chat{ID: "c-parent"}, live: true}
	roles.followups = q
	mem, st, _ := newTabbedMembership(t, h)
	return &mergeFixture{host: h, roles: roles, mem: mem, merges: newTangentMerges(roles, mem, h), queue: q, tabs: st}
}

func (f *mergeFixture) merge(t *testing.T, tangents TangentAccess) (any, error) {
	t.Helper()
	return f.mergeWith(t, tangents, `{"op_id":"op-1"}`)
}

func (f *mergeFixture) mergeWith(t *testing.T, tangents TangentAccess, payload string) (any, error) {
	t.Helper()
	return cmdMergeTangent(t.Context(), tangents, f.merges,
		&marotte.ClientCommand{Type: marotte.CmdMergeTangent, ChatID: "c-tangent", Payload: json.RawMessage(payload)})
}

// restarted is the fixture as a replacement process sees it: the same chat records, no claims.
func (f *mergeFixture) restarted() *mergeFixture {
	again := *f
	again.merges = newTangentMerges(f.roles, f.mem, f.host)
	return &again
}

func TestMergeTangent_SummarizesThenPromptsTheIdleParent(t *testing.T) {
	f := newMergeFixture(t)
	reply, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "The cache halves the load."})
	if err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	if got, _ := reply.(map[string]any)["parent_chat_id"].(marotte.ChatID); got != "c-parent" {
		t.Errorf("reply parent_chat_id = %v, want c-parent", reply)
	}
	opened := f.host.opened
	if len(opened) != 2 {
		t.Fatalf("prompts opened = %d, want the summary then the findings", len(opened))
	}
	if opened[0].Label != tangentSummaryLabel || opened[0].Text != tangentSummaryPrompt {
		t.Errorf("summary prompt = %+v, want the labelled summary request", opened[0])
	}
	if !strings.Contains(opened[1].Text, "The cache halves the load.") ||
		opened[1].Label != `Merging findings from tangent "Try the cache"` {
		t.Errorf("findings prompt = %+v, want the summary under the tangent-named label", opened[1])
	}
	rec, ok := f.merges.record(t.Context(), tangentOp1)
	if !ok || rec.State != marotte.TangentMergeSucceeded || rec.DeliveryID != opened[1].ID {
		t.Errorf("merge record = %+v (found %v), want succeeded under the findings prompt's id %q", rec, ok, opened[1].ID)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Type != marotte.EventTangentMerged ||
		frames[0].Payload != (marotte.TangentMergedPayload{ParentChatID: "c-parent", OpID: "op-1"}) {
		t.Errorf("merge frames = %+v, want one tangent_merged naming the parent and the op", frames)
	}
	if want := 2 * len(opened); f.host.released != want {
		t.Errorf("ReleaseTurn calls = %d, want %d: each runner's hold and the merge's own on each turn", f.host.released, want)
	}
	tabs, _ := f.tabs.List()
	if len(tabs) != 1 || tabs[0].Ref != "c-parent" {
		t.Errorf("open tabs = %+v, want the parent's tab (re)opened", tabs)
	}
}

func TestMergeTangent_QueuesTheFindingsOnABusyParent(t *testing.T) {
	f := newMergeFixture(t)
	f.host.busy["c-parent"] = true
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	if len(f.host.opened) != 1 {
		t.Errorf("prompts opened = %d, want the summary only", len(f.host.opened))
	}
	rows := f.queue.chat.QueuedPrompts
	if len(rows) != 1 || !strings.Contains(rows[0].Text, "Found it.") || rows[0].Label == "" {
		t.Errorf("parent's queue = %+v, want one labelled row carrying the summary", rows)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Payload != (marotte.TangentMergedPayload{ParentChatID: "c-parent", OpID: "op-1"}) {
		t.Errorf("merge frames = %+v, want one tangent_merged naming the parent", frames)
	}
}

func TestMergeTangent_FailsWhenKASNeverBindsTheParentsFindingsPrompt(t *testing.T) {
	f := newMergeFixture(t)
	f.host.unbound["c-parent"] = "The agent could not start."
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	want := deliveryFailure("The agent could not start.")
	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != marotte.TangentMergeFailed || rec.Message != want {
		t.Errorf("merge record = %+v, want failed with %q", rec, want)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Payload != (marotte.ErrorPayload{Code: marotte.ErrCodeTangentMergeFailed, Message: want, OpID: "op-1"}) {
		t.Errorf("merge frames = %+v, want one tangent_merge_failed carrying the parent turn's reason", frames)
	}
	if tabs, _ := f.tabs.List(); len(tabs) != 0 {
		t.Errorf("open tabs = %+v, want none: a failed merge reopens nothing", tabs)
	}
}

func TestMergeTangent_AnUnfinishedSummaryMergesNothing(t *testing.T) {
	f := newMergeFixture(t)
	f.host.result = marotte.TurnResult{Stop: marotte.StopReasonCancelled}
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "partial"}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	if len(f.host.opened) != 1 || len(f.queue.chat.QueuedPrompts) != 0 {
		t.Errorf("opened %d prompts and queued %d rows, want the summary alone", len(f.host.opened), len(f.queue.chat.QueuedPrompts))
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Type != marotte.EventError || frames[0].Payload.(marotte.ErrorPayload).OpID != "op-1" {
		t.Errorf("merge frames = %+v, want one tangent_merge_failed error naming the op", frames)
	}
	if tabs, _ := f.tabs.List(); len(tabs) != 0 {
		t.Errorf("open tabs = %+v, want none: a failed merge reopens nothing", tabs)
	}
}

func TestMergeTangent_RecordsTheOutcomeBeforeItsFrame(t *testing.T) {
	f := newMergeFixture(t)
	var atFrame mergeStatus
	f.host.onMergeEvt = func(marotte.ServerEvent) { atFrame = f.merges.status(t.Context(), tangentOp1) }
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	if atFrame.State != string(marotte.TangentMergeSucceeded) {
		t.Errorf("status read as the frame went out = %+v, want succeeded", atFrame)
	}
}

func TestMergeTangent_RefusesBeforeSendingAnything(t *testing.T) {
	cases := map[string]struct {
		tangents   *fakeTangents
		payload    string
		busy       bool
		wantStatus int
	}{
		"missingOpID":    {tangents: &fakeTangents{parent: "c-parent"}, payload: `{}`, wantStatus: http.StatusBadRequest},
		"malformedOpID":  {tangents: &fakeTangents{parent: "c-parent"}, payload: `{"op_id":"../x"}`, wantStatus: http.StatusBadRequest},
		"malformedBody":  {tangents: &fakeTangents{parent: "c-parent"}, payload: `[`, wantStatus: http.StatusBadRequest},
		"busyTangent":    {tangents: &fakeTangents{parent: "c-parent"}, busy: true, wantStatus: http.StatusConflict},
		"notATangent":    {tangents: &fakeTangents{err: ErrNotATangent}, wantStatus: http.StatusConflict},
		"parentGone":     {tangents: &fakeTangents{err: ErrTangentParentGone}, wantStatus: http.StatusNotFound},
		"unreadableList": {tangents: &fakeTangents{err: errors.New("kas gone")}, wantStatus: http.StatusServiceUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMergeFixture(t)
			f.host.busy["c-tangent"] = tc.busy
			_, err := f.mergeWith(t, tc.tangents, cmp.Or(tc.payload, `{"op_id":"op-1"}`))
			if statusOf(err) != tc.wantStatus {
				t.Errorf("CmdMergeTangent status = %d (%v), want %d", statusOf(err), err, tc.wantStatus)
			}
			if len(f.host.opened) != 0 {
				t.Errorf("prompts opened = %d, want none", len(f.host.opened))
			}
			if got := f.merges.status(t.Context(), tangentOp1); got.State != mergeAbsent {
				t.Errorf("status after the refusal = %+v, want absent so a retry runs afresh", got)
			}
		})
	}
}

func TestMergeTangent_ARefusedSummaryTurnLeavesNoRecord(t *testing.T) {
	f := newMergeFixture(t)
	f.host.openErr = errors.New("log unwritable")
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent"}); err == nil {
		t.Fatal("CmdMergeTangent = nil, want the open's refusal")
	}
	if got := f.merges.status(t.Context(), tangentOp1); got.State != mergeAbsent {
		t.Errorf("status after the refused open = %+v, want absent", got)
	}
}

func TestMergeTangent_ARepeatAnswersTheMergesStateInsteadOfRunningIt(t *testing.T) {
	f := newMergeFixture(t)
	tangents := &fakeTangents{parent: "c-parent", reply: "Found it."}
	if _, err := f.merge(t, tangents); err != nil {
		t.Fatalf("first CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	reply, err := f.merge(t, tangents)
	if err != nil {
		t.Fatalf("repeated CmdMergeTangent = %v, want the merge's state", err)
	}
	if got := reply.(map[string]any); got["state"] != string(marotte.TangentMergeSucceeded) || got["parent_chat_id"] != marotte.ChatID("c-parent") {
		t.Errorf("repeated reply = %v, want state succeeded naming the parent", got)
	}
	if len(f.host.opened) != 2 {
		t.Errorf("prompts opened = %d, want the first merge's two and nothing more", len(f.host.opened))
	}
}

func TestMergeTangent_ARepeatOfAFailedMergeCarriesItsReason(t *testing.T) {
	f := newMergeFixture(t)
	f.host.result = marotte.TurnResult{Stop: marotte.StopReasonCancelled}
	tangents := &fakeTangents{parent: "c-parent"}
	if _, err := f.merge(t, tangents); err != nil {
		t.Fatalf("first CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	reply, err := f.merge(t, tangents)
	if err != nil {
		t.Fatalf("repeated CmdMergeTangent = %v, want the merge's state", err)
	}
	if got := reply.(map[string]any); got["state"] != string(marotte.TangentMergeFailed) ||
		got["message"] != "The tangent's summary did not finish, so nothing was merged." {
		t.Errorf("repeated reply = %v, want state failed with the frame's reason", got)
	}
}

func TestMergeTangent_ARepeatDuringAdmissionIsInProgress(t *testing.T) {
	f := newMergeFixture(t)
	f.merges.claim(tangentOp1)

	_, err := f.merge(t, &fakeTangents{parent: "c-parent"})
	var se *statusError
	if !errors.As(err, &se) || se.code != http.StatusConflict || se.reason != reasonInProgress {
		t.Errorf("CmdMergeTangent during admission = %v, want 409 in_progress", err)
	}
	if len(f.host.opened) != 0 {
		t.Errorf("prompts opened = %d, want none", len(f.host.opened))
	}
}

func TestMergeTangent_AReplacementProcessAnswersTheFinishedMerge(t *testing.T) {
	f := newMergeFixture(t)
	tangents := &fakeTangents{parent: "c-parent", reply: "Found it."}
	if _, err := f.merge(t, tangents); err != nil {
		t.Fatalf("first CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)
	next := f.restarted()

	if got := next.merges.status(t.Context(), tangentOp1); got.State != string(marotte.TangentMergeSucceeded) {
		t.Errorf("status after the restart = %+v, want succeeded", got)
	}
	reply, err := next.merge(t, tangents)
	if err != nil {
		t.Fatalf("CmdMergeTangent after the restart = %v, want the merge's state", err)
	}
	if got := reply.(map[string]any)["state"]; got != string(marotte.TangentMergeSucceeded) {
		t.Errorf("reply after the restart = %v, want succeeded", reply)
	}
	if n := f.host.promptCount(); n != 2 {
		t.Errorf("prompts opened = %d, want the first merge's two and nothing more", n)
	}
}

type orphanCase struct {
	seed func(t *testing.T, f *mergeFixture)
	// resent is the findings prompt the resumed merge sends again, nil for none.
	resent      *marotte.EntryPrompt
	wantState   marotte.TangentMergeState
	wantMessage string
}

const orphanLabel = `Merging findings from tangent "Try the cache"`

func seedReceipt(r marotte.PromptReceipt) func(*testing.T, *mergeFixture) {
	return func(_ *testing.T, f *mergeFixture) {
		f.host.receipts[promptKey{chat: "c-parent", id: "msg-delivery"}] = r
	}
}

// An ended process leaves a running record with no claim; the next reader loads the parent and
// settles it by what KAS holds under the record's delivery id.
func orphanCases() map[string]orphanCase {
	unbound := marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true}
	resent := &marotte.EntryPrompt{ID: "msg-delivery", Text: "findings", Label: orphanLabel}
	return map[string]orphanCase{
		"boundPrompt": {
			seed:      seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true, Bound: true}),
			wantState: marotte.TangentMergeSucceeded,
		},
		"queuedRow": {
			seed: func(t *testing.T, f *mergeFixture) {
				if _, err := f.host.Mutate(t.Context(), "c-parent", func(c *marotte.Chat, _ bool) bool {
					c.QueuedPrompts = append(c.QueuedPrompts, marotte.QueuedPrompt{ID: "msg-delivery", Text: "findings"})
					return true
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantState: marotte.TangentMergeSucceeded,
		},
		"unboundPromptResentAndBound": {
			seed:      seedReceipt(unbound),
			resent:    resent,
			wantState: marotte.TangentMergeSucceeded,
		},
		"unboundPromptResentAndStillUnbound": {
			seed: func(t *testing.T, f *mergeFixture) {
				seedReceipt(unbound)(t, f)
				f.host.unbound["c-parent"] = "The agent could not start."
			},
			resent:      resent,
			wantState:   marotte.TangentMergeFailed,
			wantMessage: deliveryFailure("The agent could not start."),
		},
		"neverDelivered": {
			seed:        func(*testing.T, *mergeFixture) {},
			wantState:   marotte.TangentMergeFailed,
			wantMessage: mergeInterrupted,
		},
	}
}

func orphanedMerge(t *testing.T, tc orphanCase) *mergeFixture {
	t.Helper()
	f := newMergeFixture(t)
	if err := f.merges.admit(t.Context(), tangentOp1, "c-parent", "msg-delivery"); err != nil {
		t.Fatal(err)
	}
	tc.seed(t, f)
	return f.restarted()
}

// checkResumed reads what a resumed orphan left once its background half ended.
func checkResumed(t *testing.T, f *mergeFixture, tc orphanCase) {
	t.Helper()
	f.host.waitInflight(t)
	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != tc.wantState || rec.Message != tc.wantMessage {
		t.Errorf("record after the resume = %+v, want state %q, message %q", rec, tc.wantState, tc.wantMessage)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 {
		t.Errorf("merge frames = %+v, want one terminal frame", frames)
	}
	if len(f.host.reads) == 0 || f.host.reads[0] != "load c-parent" {
		t.Errorf("parent reads = %v, want the parent loaded before anything is decided", f.host.reads)
	}
	var opened []*marotte.EntryPrompt
	if tc.resent != nil {
		opened = []*marotte.EntryPrompt{tc.resent}
	}
	if got := f.host.opened; !reflect.DeepEqual(got, opened) {
		t.Errorf("prompts opened = %+v, want %+v", got, opened)
	}
	if n := len(f.queue.chat.QueuedPrompts); n != 0 {
		t.Errorf("rows queued on the parent = %d, want none", n)
	}
}

func TestMergeStatus_ResumesAMergeAnEndedProcessLeftRunning(t *testing.T) {
	for name, tc := range orphanCases() {
		t.Run(name, func(t *testing.T) {
			f := orphanedMerge(t, tc)

			want := mergeStatus{State: string(marotte.TangentMergeRunning), ParentChatID: "c-parent"}
			if got := f.merges.status(t.Context(), tangentOp1); got != want {
				t.Errorf("status of the orphaned merge = %+v, want %+v until its resume settles", got, want)
			}
			checkResumed(t, f, tc)
		})
	}
}

func TestMergeTangent_ARetryResumesAMergeAnEndedProcessLeftRunning(t *testing.T) {
	for name, tc := range orphanCases() {
		t.Run(name, func(t *testing.T) {
			f := orphanedMerge(t, tc)

			reply, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."})
			if err != nil {
				t.Fatalf("CmdMergeTangent of the orphaned op = %v, want its state", err)
			}
			if got := reply.(map[string]any)["state"]; got != string(marotte.TangentMergeRunning) {
				t.Errorf("reply for the orphaned op = %v, want running", reply)
			}
			checkResumed(t, f, tc)
		})
	}
}

func TestMergeTangent_ARunningMergeThisProcessOwnsStaysRunning(t *testing.T) {
	f := newMergeFixture(t)
	if err := f.merges.admit(t.Context(), tangentOp1, "c-parent", "msg-delivery"); err != nil {
		t.Fatal(err)
	}
	f.merges.claim(tangentOp1)

	want := mergeStatus{State: string(marotte.TangentMergeRunning), ParentChatID: "c-parent"}
	if got := f.merges.status(t.Context(), tangentOp1); got != want {
		t.Errorf("status of a claimed running merge = %+v, want %+v", got, want)
	}
}

func TestTangentMerges_ForgetsATerminalRecordAfterItsTTL(t *testing.T) {
	f := newMergeFixture(t)
	now := time.Unix(1000, 0)
	f.merges.now = func() time.Time { return now }
	ctx := t.Context()
	if err := f.merges.admit(ctx, tangentOp1, "c-parent", "msg-1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	f.merges.claim(tangentOp1)
	if got := f.merges.status(ctx, tangentOp1).State; got != string(marotte.TangentMergeRunning) {
		t.Errorf("status of a long-running merge = %q, want running: only a settled merge expires", got)
	}
	if _, err := f.merges.settle(ctx, tangentOp1, marotte.TangentMergeSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f.merges.release(tangentOp1)
	now = now.Add(mergeTerminalTTL - time.Second)
	if got := f.merges.status(ctx, tangentOp1).State; got != string(marotte.TangentMergeSucceeded) {
		t.Errorf("status inside the TTL = %q, want succeeded", got)
	}
	now = now.Add(time.Second)
	if got := f.merges.status(ctx, tangentOp1).State; got != mergeAbsent {
		t.Errorf("status at the TTL = %q, want absent", got)
	}
	if err := f.merges.admit(ctx, mergeKey{tangent: "c-tangent", op: "op-2"}, "c-parent", "msg-2"); err != nil {
		t.Fatal(err)
	}
	c, _ := f.host.Get(ctx, "c-tangent")
	if len(c.TangentMerges) != 1 || c.TangentMerges[0].OpID != "op-2" {
		t.Errorf("records after the next write = %+v, want only op-2: the expired one is dropped", c.TangentMerges)
	}
}

func TestServeMergeStatus_AnswersTheRecordedState(t *testing.T) {
	f := newMergeFixture(t)
	if err := f.merges.admit(t.Context(), tangentOp1, "c-parent", "msg-1"); err != nil {
		t.Fatal(err)
	}
	f.merges.claim(tangentOp1)
	f.merges.claim(mergeKey{tangent: "c-tangent", op: "op-3"})
	d := &Dispatcher{merges: f.merges}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chats/{id}/merges/{op}", d.ServeMergeStatus)
	cases := map[string]struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		"admittedMerge":   {path: "/api/chats/c-tangent/merges/op-1", wantStatus: http.StatusOK, wantBody: `{"state":"running","parent_chat_id":"c-parent"}`},
		"admittingOp":     {path: "/api/chats/c-tangent/merges/op-3", wantStatus: http.StatusOK, wantBody: `{"state":"admitting"}`},
		"unknownOp":       {path: "/api/chats/c-tangent/merges/op-2", wantStatus: http.StatusOK, wantBody: `{"state":"absent"}`},
		"opOnAnotherChat": {path: "/api/chats/c-other/merges/op-1", wantStatus: http.StatusOK, wantBody: `{"state":"absent"}`},
		"malformedOpID":   {path: "/api/chats/c-tangent/merges/.x", wantStatus: http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s = %d (%s), want %d", tc.path, rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantBody != "" && strings.TrimSpace(rec.Body.String()) != tc.wantBody {
				t.Errorf("GET %s body = %s, want %s", tc.path, rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestMergeStatus_DecidesAnOrphanOnlyOnceTheParentsReplayMerged(t *testing.T) {
	f := orphanedMerge(t, orphanCase{seed: seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true})})
	f.host.loadGate, f.host.loading = make(chan struct{}), make(chan struct{}, 1)

	f.merges.status(t.Context(), tangentOp1)
	<-f.host.loading
	if got := f.host.readsSoFar(); !slices.Equal(got, []string{"load c-parent"}) {
		t.Errorf("parent reads while its replay is still merging = %v, want the load alone", got)
	}
	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != marotte.TangentMergeRunning {
		t.Errorf("record while the parent's replay is still merging = %+v, want running", rec)
	}
	if n := f.host.promptCount(); n != 0 {
		t.Errorf("prompts opened while the parent's replay is still merging = %d, want none", n)
	}

	// The replay's swap lands KAS's bind for the findings prompt, then the load answers.
	seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true, Bound: true})(t, f)
	close(f.host.loadGate)
	checkResumed(t, f, orphanCase{wantState: marotte.TangentMergeSucceeded})
}

func TestMergeStatus_LeavesAnOrphanRunningWhileTheParentCannotBeReconciled(t *testing.T) {
	f := orphanedMerge(t, orphanCase{seed: seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true, Bound: true})})
	f.host.loadErr = errors.New("replay swap failed")

	f.merges.status(t.Context(), tangentOp1)
	f.host.waitInflight(t)
	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != marotte.TangentMergeRunning {
		t.Errorf("record after an unreconciled load = %+v, want running", rec)
	}
	if got := f.host.readsSoFar(); !slices.Equal(got, []string{"load c-parent"}) {
		t.Errorf("parent reads after an unreconciled load = %v, want the load alone", got)
	}
	if frames := f.host.mergeFrames(); len(frames) != 0 {
		t.Errorf("merge frames after an unreconciled load = %+v, want none", frames)
	}

	f.host.loadErr, f.host.reads = nil, nil
	f.merges.status(t.Context(), tangentOp1)
	checkResumed(t, f, orphanCase{wantState: marotte.TangentMergeSucceeded})
}

func TestMergeStatus_FailsAnOrphanWhoseParentWasDeleted(t *testing.T) {
	f := orphanedMerge(t, orphanCase{seed: seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true})})
	f.host.loadErr = fmt.Errorf("%w: c-parent", ErrChatGone)

	f.merges.status(t.Context(), tangentOp1)
	checkResumed(t, f, orphanCase{wantState: marotte.TangentMergeFailed, wantMessage: mergeParentGone})

	// A second read finds the terminal record and resumes nothing.
	f.host.reads = nil
	if got := f.merges.status(t.Context(), tangentOp1).State; got != string(marotte.TangentMergeFailed) {
		t.Errorf("status after the parent's deletion settled it = %q, want failed", got)
	}
	if frames := f.host.mergeFrames(); len(frames) != 1 {
		t.Errorf("merge frames after a second read = %+v, want still the one terminal frame", frames)
	}
	if got := f.host.readsSoFar(); len(got) != 0 {
		t.Errorf("parent reads on the second read = %v, want none", got)
	}
}

// A parent deleted after its session load answered is read back as gone by the log, which is
// the same terminal answer as a load that found it deleted.
func TestMergeStatus_FailsAnOrphanWhoseParentWasDeletedAfterItsLoad(t *testing.T) {
	for name, err := range map[string]error{
		"tombstoned": chat.ErrTombstoned,
		"not found":  fmt.Errorf("%w: c-parent", chat.ErrChatNotFound),
	} {
		t.Run(name, func(t *testing.T) {
			f := orphanedMerge(t, orphanCase{seed: seedReceipt(marotte.PromptReceipt{Text: "findings", Label: orphanLabel, Opened: true})})
			f.host.receiptErr = err

			f.merges.status(t.Context(), tangentOp1)
			checkResumed(t, f, orphanCase{wantState: marotte.TangentMergeFailed, wantMessage: mergeParentGone})
		})
	}
}

func TestMergeTangent_PublishesNothingUntilItsOutcomeIsRecorded(t *testing.T) {
	f := newMergeFixture(t)
	f.host.failWrites = errors.New("disk full")
	if _, err := f.merge(t, &fakeTangents{parent: "c-parent", reply: "Found it."}); err != nil {
		t.Fatalf("CmdMergeTangent = %v, want accepted", err)
	}
	f.host.waitInflight(t)

	rec, _ := f.merges.record(t.Context(), tangentOp1)
	if rec.State != marotte.TangentMergeRunning {
		t.Errorf("record after its settlement failed to write = %+v, want running", rec)
	}
	if frames := f.host.mergeFrames(); len(frames) != 0 {
		t.Errorf("merge frames after the settlement failed to write = %+v, want none", frames)
	}
	if tabs, _ := f.tabs.List(); len(tabs) != 0 {
		t.Errorf("open tabs after the settlement failed to write = %+v, want none", tabs)
	}

	// Storage recovers; the next reader resumes the merge from the findings KAS bound.
	f.host.writeErr, f.host.failWrites = nil, nil
	f.host.receipts[promptKey{chat: "c-parent", id: rec.DeliveryID}] = marotte.PromptReceipt{Opened: true, Bound: true}
	f.merges.status(t.Context(), tangentOp1)
	f.host.waitInflight(t)
	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != marotte.TangentMergeSucceeded {
		t.Errorf("record after the recovered resume = %+v, want succeeded", rec)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Payload != (marotte.TangentMergedPayload{ParentChatID: "c-parent", OpID: "op-1"}) {
		t.Errorf("merge frames after the recovered resume = %+v, want one tangent_merged", frames)
	}
	if n := f.host.promptCount(); n != 2 {
		t.Errorf("prompts opened = %d, want the summary and the findings only", n)
	}
}

func TestTangentMerges_PublishesOnlyTheOutcomeItCommitted(t *testing.T) {
	f := newMergeFixture(t)
	if err := f.merges.admit(t.Context(), tangentOp1, "c-parent", "msg-delivery"); err != nil {
		t.Fatal(err)
	}
	f.merges.conclude(t.Context(), tangentOp1, "c-parent", "")
	f.merges.conclude(t.Context(), tangentOp1, "c-parent", mergeInterrupted)

	if rec, _ := f.merges.record(t.Context(), tangentOp1); rec.State != marotte.TangentMergeSucceeded {
		t.Errorf("record after a second conclude = %+v, want the first outcome, succeeded", rec)
	}
	frames := f.host.mergeFrames()
	if len(frames) != 1 || frames[0].Type != marotte.EventTangentMerged {
		t.Errorf("merge frames = %+v, want the committed tangent_merged alone", frames)
	}
}
