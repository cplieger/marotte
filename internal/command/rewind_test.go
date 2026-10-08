package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// recordingBridge records the one call made through it and replies with a scripted
// result. Shared by every command test that asserts what went onto the wire.
type recordingBridge struct {
	callErr error
	result  any
	// order, when set, records each Call in a slice the host double shares, so a test
	// can assert a host-side step ran BEFORE the wire call.
	order *[]string
	// duringCall, when set, runs while the wire call is IN FLIGHT, for state a notification would
	// observe mid-call.
	duringCall func()
	gotMethod  string
	gotParams  map[string]any
	callCount  int
	sessionID  marotte.SessionID
}

func (b *recordingBridge) Call(_ context.Context, method string, params any) (*marotte.RPCResponse, error) {
	b.callCount++
	if b.order != nil {
		*b.order = append(*b.order, "call")
	}
	b.gotMethod = method
	if m, ok := params.(map[string]any); ok {
		b.gotParams = m
	}
	if b.duringCall != nil {
		b.duringCall()
	}
	if b.callErr != nil {
		return nil, b.callErr
	}
	raw, err := json.Marshal(b.result)
	if err != nil {
		return nil, err
	}
	return &marotte.RPCResponse{Result: raw}, nil
}

// CallAt reports position zero, which is what "no ordering to wait for" means: this
// double is not on a prompt path.
func (b *recordingBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

func (b *recordingBridge) Notify(context.Context, string, any) error        { return nil }
func (b *recordingBridge) Respond(context.Context, int64, any, error) error { return nil }
func (b *recordingBridge) SessionID() marotte.SessionID                     { return b.sessionID }
func (b *recordingBridge) TryAcquireForPrompt() bool                        { return true }
func (b *recordingBridge) ReleaseAfterPrompt()                              {}
func (b *recordingBridge) BeginPromptCall(context.CancelCauseFunc) uint64   { return 0 }
func (b *recordingBridge) EndPromptCall()                                   {}
func (b *recordingBridge) PromptGeneration() uint64                         { return 0 }
func (b *recordingBridge) ArmCancelGrace(uint64, time.Duration) bool        { return false }

// bridgeDeps adds a bridge to storeDeps so the outgoing call can be observed.
type bridgeDeps struct {
	*storeDeps
	// bridge is the LIVE bridge Bridge() reports.
	bridge Bridge
	// opened is separate from bridge because the state rewind exists to serve is
	// exactly nil live plus a non-nil resume: a reopened chat nobody has prompted.
	opened Bridge
}

func (d *bridgeDeps) Bridge(marotte.ChatID) Bridge { return d.bridge }

// BridgeLive follows the live bridge: a spawning or absent one parks a steer.
func (d *bridgeDeps) BridgeLive(marotte.ChatID) bool { return d.bridge != nil }

func (d *bridgeDeps) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	return d.opened, nil
}

// newBridgeHost lets one bridge answer both lookups: a chat with a live bridge is what
// every caller but rewind's resume path sees.
func newBridgeHost(store ChatStore, bridge Bridge) hostDouble {
	return &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
		bridge:    bridge,
		opened:    bridge,
	}
}

// idleHost is newBridgeHost over a registry holding NO turn: the state a rewind is
// admitted in, and the one a steer is refused in. opened may differ from bridge for
// the bridgeless-but-resumable shape.
func idleHost(store ChatStore, bridge, opened Bridge) *bridgeDeps {
	return &bridgeDeps{
		storeDeps: &storeDeps{benchDeps: &benchDeps{}, store: store},
		bridge:    bridge,
		opened:    opened,
	}
}

func rewindReq(t *testing.T, chatID marotte.ChatID, messageID string) *marotte.ClientCommand {
	t.Helper()
	return rewindReqConfirmed(t, chatID, messageID, false)
}

// rewindReqConfirmed is rewindReq with the reader's answer to the runs-in-cut question.
func rewindReqConfirmed(t *testing.T, chatID marotte.ChatID, messageID string, confirmed bool) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.RewindChatCommand{MessageID: messageID, Confirmed: confirmed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdRewindChat,
		ChatID:  chatID,
		Payload: payload,
	}
}

// rewindStore is the in-memory store with a scripted log: RewindTarget answers from
// targets (a prompt id the log holds, and the turn it opened), Revert records the
// turns it was asked to revert to and answers the record plus the carrier it minted.
// The real store's resolution over turn_open and turn_bind entries is internal/chat's
// to pin; what this file pins is what the command does with the answer.
type rewindStore struct {
	*testsupport.InMemoryChatStore
	targets  map[string]marotte.RewindTarget
	reverted []string
	// minted, when set, is the carrier's turn_open and turn_close the store reports
	// as freshly written, which the command must announce ahead of the record.
	minted    []*marotte.Entry
	revertErr error
	// order, when set, interleaves the store's record with the bridge's call.
	order *[]string
}

func (s *rewindStore) RewindTarget(_ context.Context, _ marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error) {
	target, ok := s.targets[promptID]
	return target, ok, nil
}

func (s *rewindStore) Revert(_ context.Context, _ marotte.ChatID, turn, _ string) (*marotte.Entry, []*marotte.Entry, error) {
	if s.order != nil {
		*s.order = append(*s.order, "revert")
	}
	if s.revertErr != nil {
		return nil, nil, s.revertErr
	}
	s.reverted = append(s.reverted, turn)
	return &marotte.Entry{ID: turn + ":revert", Turn: "carrier", Kind: marotte.EntryKindTurnRevert}, s.minted, nil
}

// rewindRuns is the run registry as a rewind sees it: live holds the runs still
// leased, by id, with the label the reader knows them by; CancelRun records the
// cancel, releases the lease and joins the shared order so a test can place the
// cancel against the wire call and the truncate.
type rewindRuns struct {
	live      map[string]string
	order     *[]string
	cancelErr error
	cancelled []string
}

func (r *rewindRuns) LiveRuns(ids []string) []LiveRunRef {
	var out []LiveRunRef
	for _, id := range ids {
		if label, ok := r.live[id]; ok {
			out = append(out, LiveRunRef{ID: id, Label: label})
		}
	}
	return out
}

func (r *rewindRuns) CancelRun(_ context.Context, id string) error {
	if r.order != nil {
		*r.order = append(*r.order, "cancel:"+id)
	}
	r.cancelled = append(r.cancelled, id)
	if r.cancelErr != nil {
		return r.cancelErr
	}
	delete(r.live, id)
	return nil
}

// runsOf reads the live runs a refusal carries.
func runsOf(err error) []LiveRunRef {
	if se, ok := errors.AsType[*statusError](err); ok {
		return se.runs
	}
	return nil
}

// A cut holding a live run's launch un-says the instruction that started it, so the
// reader decides: unconfirmed, the command names the run and reverts nothing.
func TestCmdRewindChat_A409NamesTheLiveRunsTheCutLaunchedAndRecordsNothing(t *testing.T) {
	store := seedRewindChat(t, false)
	store.targets["u2"] = marotte.RewindTarget{Turn: "t2", LaunchedRuns: []string{"wf-1"}}
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)
	runs := &rewindRuns{live: map[string]string{"wf-1": "code-review"}}

	_, err := CmdRewindChat(t.Context(), host, host, host, runs, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 naming the run (body %s)", statusOf(err), errText(err))
	}
	if got := reasonOf(err); got != reasonRunsInCut {
		t.Errorf("reason = %q, want %q", got, reasonRunsInCut)
	}
	if want := []LiveRunRef{{ID: "wf-1", Label: "code-review"}}; !slices.Equal(runsOf(err), want) {
		t.Errorf("runs named = %+v, want %+v", runsOf(err), want)
	}
	if len(runs.cancelled) != 0 || b.callCount != 0 || len(store.reverted) != 0 {
		t.Errorf("unconfirmed rewind cancelled %v, called KAS %d times and truncated %v; want nothing touched",
			runs.cancelled, b.callCount, store.reverted)
	}
}

// Confirmed, the run is cancelled and stopped BEFORE the revert, so nothing a step
// does can land in the range being cut, and the log is cut exactly once.
func TestCmdRewindChat_ConfirmedStopsTheRunsBeforeTheRevertAndCutsOnce(t *testing.T) {
	var order []string
	store := seedRewindChat(t, false)
	store.order = &order
	store.targets["u2"] = marotte.RewindTarget{Turn: "t2", LaunchedRuns: []string{"wf-1", "wf-2"}}
	b := &recordingBridge{result: okResult(), sessionID: "sess-1", order: &order}
	host := idleHost(store, b, b)
	runs := &rewindRuns{live: map[string]string{"wf-1": "code-review", "wf-2": "docs-sweep"}, order: &order}

	_, err := CmdRewindChat(t.Context(), host, host, host, runs, host, rewindReqConfirmed(t, "c1", "u2", true))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if want := []string{"cancel:wf-1", "cancel:wf-2", "call", "revert"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v: every cancel before the revert, the revert before the cut", order, want)
	}
	if want := []string{"t2"}; !slices.Equal(store.reverted, want) {
		t.Errorf("recorded a revert at %v, want %v exactly once", store.reverted, want)
	}
}

// A run launched before the cut keeps running and is not the reader's to weigh; so
// does a run the cut launched whose lease has already gone.
func TestCmdRewindChat_ARunOutsideTheCutIsNeitherCancelledNorNamed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		launched []string
	}{
		{"launched before the cut", nil},
		{"launched in the cut but already over", []string{"wf-9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := seedRewindChat(t, false)
			store.targets["u2"] = marotte.RewindTarget{Turn: "t2", LaunchedRuns: tc.launched}
			b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
			host := idleHost(store, b, b)
			runs := &rewindRuns{live: map[string]string{"wf-0": "code-review"}}

			_, err := CmdRewindChat(t.Context(), host, host, host, runs, host, rewindReq(t, "c1", "u2"))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200 with no confirmation asked (body %s)", statusOf(err), errText(err))
			}
			if len(runs.cancelled) != 0 {
				t.Errorf("cancelled %v, want none", runs.cancelled)
			}
			if _, live := runs.live["wf-0"]; !live {
				t.Errorf("the run launched before the cut lost its lease")
			}
		})
	}
}

// seedRewindChat seeds a chat on sess-1 whose log holds two prompt turns: u1 opened
// t1 and u2 opened t2, the second bound to KAS's kas-2 when bound is set. The session
// id matters as much as the turns: rewind captures it before it resumes and refuses
// on a mismatch, so it has to match recordingBridge{sessionID: "sess-1"} or every
// rewind test refuses.
func seedRewindChat(t *testing.T, bound bool) *rewindStore {
	t.Helper()
	store := &rewindStore{
		InMemoryChatStore: testsupport.NewInMemoryChatStore(),
		targets: map[string]marotte.RewindTarget{
			"u1": {Turn: "t1"},
			"u2": {Turn: "t2"},
		},
	}
	if bound {
		store.targets["u2"] = marotte.RewindTarget{Turn: "t2", KASMessageID: "kas-2"}
	}
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("sess-1")
		c.TurnCount = 2
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return store
}

func okResult() map[string]any {
	return map[string]any{"success": true, "affectedFiles": []string{"a.go"}, "totalFiles": 2}
}

// The cut is AT the target's turn: KAS slices from the addressed prompt inclusive, so
// the record that kept t2 would disagree with the session about what the transcript is.
func TestCmdRewindChat_RecordsTheRevertAtTheTargetsTurn(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if len(store.reverted) != 1 || store.reverted[0] != "t2" {
		t.Errorf("recorded a revert at %v, want [t2]", store.reverted)
	}
}

func TestCmdRewindChat_CallsTheRevertVerbWithTheSessionAndMessage(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, _ = CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u1"))

	if b.gotMethod != marotte.MethodCheckpointRevertMultiple {
		t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodCheckpointRevertMultiple)
	}
	if b.gotParams["messageId"] != "u1" {
		t.Errorf("messageId = %v, want u1", b.gotParams["messageId"])
	}
	if b.gotParams["sessionId"] != marotte.SessionID("sess-1") {
		t.Errorf("sessionId = %v, want sess-1", b.gotParams["sessionId"])
	}
}

// marotte resolves the target first rather than spending a round trip to be told:
// only a prompt's id opens a turn, so an id no turn_open carries is refused before
// the bridge is reached and nothing is cut.
func TestCmdRewindChat_RefusesATargetNoPromptCarries(t *testing.T) {
	for _, id := range []string{"a1", "nope"} {
		t.Run(id, func(t *testing.T) {
			store := seedRewindChat(t, false)
			b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
			host := idleHost(store, b, b)

			_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", id))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", statusOf(err))
			}
			if b.callCount != 0 {
				t.Errorf("called the bridge %d times, want 0", b.callCount)
			}
			if len(store.reverted) != 0 {
				t.Errorf("recorded a revert at %v on a refused rewind, want nothing cut", store.reverted)
			}
		})
	}
}

func TestCmdRewindChat_RejectsAnEmptyMessageID(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", ""))

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
}

// A rewind is refused while the registry holds a turn or a reservation for the chat:
// KAS's own mid-turn rule, applied before the round trip, and before anything is cut.
func TestCmdRewindChat_RefusedWhileTheRegistryHoldsATurn(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)
	held := newBridgeHost(store, b)

	_, err := CmdRewindChat(t.Context(), host, host, held, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v under an open turn, want nothing cut", store.reverted)
	}
}

// A reopened chat has a session and no bridge: the rewind resumes it and reverts on
// the resumed bridge rather than refusing.
func TestCmdRewindChat_ResumesABridgelessChatAndReverts(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, nil, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if b.gotMethod != marotte.MethodCheckpointRevertMultiple {
		t.Errorf("method = %q, want the revert verb on the resumed bridge", b.gotMethod)
	}
	if len(store.reverted) != 1 || store.reverted[0] != "t2" {
		t.Errorf("recorded a revert at %v, want [t2]", store.reverted)
	}
}

func TestCmdRewindChat_AFailedResumeIsA502(t *testing.T) {
	store := seedRewindChat(t, false)
	host := idleHost(store, nil, nil)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v with no bridge to revert on, want nothing cut", store.reverted)
	}
}

func TestCmdRewindChat_RefusesAChatWithNoSession(t *testing.T) {
	store := seedRewindChat(t, false)
	if _, err := store.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.RecordSession("")
		return true
	}); err != nil {
		t.Fatalf("clear session: %v", err)
	}
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, nil, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v on a refused rewind, want nothing cut", store.reverted)
	}
}

// The session id is captured BEFORE the resume: a failed session/load falls through
// to session/new, which holds none of this transcript, so a resumed bridge on another
// session is refused rather than reverted.
func TestCmdRewindChat_RefusesWhenTheOriginalSessionWasNotResumed(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-fresh"}
	host := idleHost(store, nil, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 {
		t.Errorf("called the bridge %d times, want 0", b.callCount)
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v against an unrelated session, want nothing cut", store.reverted)
	}
}

// The revert lands BEFORE the cut: a refused revert then records nothing, and no replay
// barrier sits between the two, since the appended record is itself what refuses a swap
// built from the pre-revert replay.
func TestCmdRewindChat_RevertsBeforeItRecords(t *testing.T) {
	order := []string{}
	store := seedRewindChat(t, false)
	store.order = &order
	b := &recordingBridge{result: okResult(), sessionID: "sess-1", order: &order}
	host := idleHost(store, nil, b)

	if _, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2")); err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}
	if len(order) != 2 || order[0] != "call" || order[1] != "revert" {
		t.Fatalf("order = %v, want [call truncate]: the revert must land before the record is cut", order)
	}
}

// KAS refused in band (success:false), so the answer is 409 carrying KAS's own
// reason, and the record is untouched: a cut here would drop turns the session kept.
func TestCmdRewindChat_InBandRefusalLeavesTheRecordIntact(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{
		result: map[string]any{
			"success": false,
			"error":   "Cannot revert while the agent is still running. Stop the turn and try again.",
		},
		sessionID: "sess-1",
	}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if body := errText(err); !strings.Contains(body, "still running") {
		t.Errorf("response %s does not carry KAS's reason", body)
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v after a refused revert, want nothing cut", store.reverted)
	}
}

func TestCmdRewindChat_TransportFailureLeavesTheRecordIntact(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{callErr: errors.New("broken pipe"), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	if len(store.reverted) != 0 {
		t.Errorf("recorded a revert at %v after a failed call, want nothing cut", store.reverted)
	}
}

// A truncate the store refuses after KAS already reverted is a 500: the session and
// the record now disagree, which the next session/load's merge is what heals, and
// the response must not claim a rewind the record does not hold.
func TestCmdRewindChat_ARecordFailureIsA500(t *testing.T) {
	store := seedRewindChat(t, false)
	store.revertErr = errors.New("entry log refused the cut")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body %s)", statusOf(err), errText(err))
	}
}

// The log line names the turn the revert was recorded at and whether the log had to
// mint a carrier for the record, which is the one thing about the append a reader of
// the log line cannot derive from the turn.
func TestCmdRewindChat_LogsTheTurnAndWhetherACarrierWasMinted(t *testing.T) {
	logs := captureLogs(t)
	store := seedRewindChat(t, false)
	store.minted = mintedCarrier("t-carrier")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	if _, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2")); err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}
	if got := logs.String(); !strings.Contains(got, "turn=t2") || !strings.Contains(got, "carrier_minted=true") {
		t.Errorf("log does not report turn=t2 carrier_minted=true: %s", got)
	}
}

// A minted carrier is announced whole before the record: a client holding its turn_open
// without the turn_close reads a running turn, and the chat's tab dot pulses until a reload.
func TestCmdRewindChat_AnnouncesAMintedCarrierOpenedAndClosedBeforeTheRecord(t *testing.T) {
	store := seedRewindChat(t, false)
	store.minted = mintedCarrier("t-carrier")
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)
	bus := &recordingBus{}

	if _, err := CmdRewindChat(t.Context(), host, host, host, host, bus, rewindReq(t, "c1", "u2")); err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}
	var got []string
	for _, e := range bus.events {
		got = append(got, string(e.Type))
	}
	if want := "turn_opened,turn_closed,entry_appended"; strings.Join(got, ",") != want {
		t.Errorf("CmdRewindChat announced %v, want %s", got, want)
	}
}

// mintedCarrier is the pair the store writes when no turn survives a revert's window.
func mintedCarrier(turn string) []*marotte.Entry {
	return []*marotte.Entry{
		{ID: turn, Turn: turn, Kind: marotte.EntryKindTurnOpen},
		{ID: turn + ":close", Turn: turn, Seq: 1, Kind: marotte.EntryKindTurnClose},
	}
}

// marotte's own m- id names nothing in KAS's log; the id KAS holds the prompt under
// arrived on user_message_id_assigned and rides the turn_bind, so that is what the
// revert is addressed with.
func TestCmdRewindChat_AddressesKASByItsOwnRecordID(t *testing.T) {
	store := seedRewindChat(t, true)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if got := b.gotParams["messageId"]; got != "kas-2" {
		t.Errorf("messageId = %v, want kas-2: marotte's own id names nothing in KAS's log", got)
	}
}

// A turn whose bind never arrived is addressed by the prompt's own id, which KAS may
// or may not know.
func TestCmdRewindChat_FallsBackToTheRowsOwnIDWhenNoKASIDIsHeld(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if got := b.gotParams["messageId"]; got != "u2" {
		t.Errorf("messageId = %v, want u2", got)
	}
}

// A refusal on a turn marotte could only address by its own id says so beside KAS's
// reason: the reader can then tell an unaddressable turn from a mid-turn refusal.
func TestCmdRewindChat_ExplainsARefusalOnATurnItCannotAddress(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{
		result:    map[string]any{"success": false, "error": `Message "u2" not found`},
		sessionID: "sess-1",
	}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409", statusOf(err))
	}
	body := errText(err)
	if !strings.Contains(body, "no id for this turn in the agent's current session") {
		t.Errorf("response %s does not say why this turn is unaddressable", body)
	}
	if !strings.Contains(body, "not found") {
		t.Errorf("response %s dropped KAS's own reason", body)
	}
}

func TestCmdRewindChat_DoesNotBlameIDCaptureWhenTheKASIDWasSent(t *testing.T) {
	store := seedRewindChat(t, true)
	b := &recordingBridge{
		result: map[string]any{
			"success": false,
			"error":   "Cannot revert while the agent is still running. Stop the turn and try again.",
		},
		sessionID: "sess-1",
	}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, host, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409", statusOf(err))
	}
	if body := errText(err); strings.Contains(body, "no id for this turn in the agent's current session") {
		t.Errorf("response %s blames id capture for a mid-turn refusal", body)
	}
}

type slotAdmission struct {
	*benchDeps
	mu       sync.Mutex
	reserved bool
	source   marotte.TurnOpenSource
}

func newSlotAdmission() *slotAdmission { return &slotAdmission{benchDeps: &benchDeps{}} }

func (a *slotAdmission) TryReserveTurn(_ marotte.ChatID, source marotte.TurnOpenSource) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reserved {
		return false
	}
	a.reserved, a.source = true, source
	return true
}

func (a *slotAdmission) TryReserveIdleTurn(chatID marotte.ChatID, source marotte.TurnOpenSource) bool {
	return a.TryReserveTurn(chatID, source)
}

func (a *slotAdmission) ReleaseTurnReservation(marotte.ChatID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reserved = false
}

func (a *slotAdmission) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.source, a.reserved
}

// An admitted prompt opens a turn, and the turn ends the window in which KAS's restore writes are
// let through; so a prompt racing the revert must not be admitted until the rewind returns.
func TestCmdRewindChat_AdmitsNoPromptWhileTheRevertRuns(t *testing.T) {
	store := seedRewindChat(t, false)
	admission := newSlotAdmission()
	inRevert, release := make(chan struct{}), make(chan struct{})
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	b.duringCall = func() {
		close(inRevert)
		<-release
	}
	host := idleHost(store, b, b)
	req := rewindReq(t, "c1", "u2")
	done := make(chan error, 1)
	go func() {
		_, err := CmdRewindChat(t.Context(), host, host, admission, host, host, req)
		done <- err
	}()

	<-inRevert
	holder, held := admission.AdmissionHolderSource("c1")
	admittedDuringRevert := admission.TryReserveTurn("c1", marotte.TurnSourcePrompt)
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("CmdRewindChat = %v, want it to succeed", err)
	}
	if admittedDuringRevert {
		t.Fatal("a prompt reserved the chat's admission slot while _kiro/checkpoint/revertMultiple was in flight")
	}
	// ReserveTurnForPrompt answers a prompt-class holder Busy, which sends the prompt as a steer.
	if !held || holder.PromptClass() {
		t.Errorf("during the revert the slot holder was %v (held %t), want a non-prompt source a prompt parks behind", holder, held)
	}
	if !admission.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Error("after the rewind returned the admission slot was still held, so every later prompt is refused")
	}
}

// The slot is released on a refusal too, or the chat refuses every prompt after one failed rewind.
func TestCmdRewindChat_ReleasesTheAdmissionSlotWhenKASRefuses(t *testing.T) {
	store := seedRewindChat(t, false)
	admission := newSlotAdmission()
	b := &recordingBridge{result: map[string]any{"success": false, "error": "refused"}, sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, admission, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", statusOf(err), errText(err))
	}
	if !admission.TryReserveTurn("c1", marotte.TurnSourcePrompt) {
		t.Error("after a refused rewind the admission slot was still held")
	}
}

type lostSlot struct{ *benchDeps }

func (lostSlot) TryReserveIdleTurn(marotte.ChatID, marotte.TurnOpenSource) bool { return false }

func TestCmdRewindChat_RefusedWhenAPromptTakesTheSlotFirst(t *testing.T) {
	store := seedRewindChat(t, false)
	b := &recordingBridge{result: okResult(), sessionID: "sess-1"}
	host := idleHost(store, b, b)

	_, err := CmdRewindChat(t.Context(), host, host, lostSlot{&benchDeps{}}, host, host, rewindReq(t, "c1", "u2"))

	if statusOf(err) != http.StatusConflict {
		t.Errorf("status = %d, want 409", statusOf(err))
	}
	if b.callCount != 0 || len(store.reverted) != 0 {
		t.Errorf("called KAS %d times and recorded %v without the slot, want nothing", b.callCount, store.reverted)
	}
}
