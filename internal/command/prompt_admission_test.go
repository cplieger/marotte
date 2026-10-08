package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// clientAPITimeout reads the shared fixture pinning @cplieger/fetch's
// API_TIMEOUT_MS — the timeout every client action dispatches under. The TS
// half (static-src/api-timeout.node.test.ts) asserts the fixture equals the
// INSTALLED library's constant, so a library bump fails that gate and forces a
// fixture update, which this test then re-checks against AdmissionWait.
func clientAPITimeout(t *testing.T) time.Duration {
	t.Helper()
	raw, err := os.ReadFile("testdata/client_api_timeout.json")
	if err != nil {
		t.Fatalf("read the client API timeout fixture (its contract is described in this file): %v", err)
	}
	var fixture struct {
		APITimeoutMS int `json:"api_timeout_ms"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode testdata/client_api_timeout.json: %v", err)
	}
	if fixture.APITimeoutMS <= 0 {
		t.Fatalf("client_api_timeout.json api_timeout_ms = %d, want a positive value", fixture.APITimeoutMS)
	}
	return time.Duration(fixture.APITimeoutMS) * time.Millisecond
}

// A contended admission wait longer than the client's own request timeout
// would make the client abort the POST before the server's refusal arrives —
// the user would see a network error instead of the busy answer.
func TestAdmissionWait_StaysUnderTheClientAPITimeout(t *testing.T) {
	timeout := clientAPITimeout(t)
	if AdmissionWait >= timeout {
		t.Fatalf("AdmissionWait = %v, want it below the client API timeout %v", AdmissionWait, timeout)
	}
}

// callRecorder is an ordered, mutex-guarded call log shared by the doubles, so
// a release-ordering assertion reads one sequence.
type callRecorder struct {
	mu  sync.Mutex
	log []string
}

func (r *callRecorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, s)
}

func (r *callRecorder) indexOf(s string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.log {
		if e == s {
			return i
		}
	}
	return -1
}

func (r *callRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// orderedBridge is a Bridge with a REAL single-holder prompt slot and the
// shared log, so "the goroutine released the bridge slot" is a fact rather
// than a stub's yes.
type orderedBridge struct {
	recordingBridge
	rec  *callRecorder
	mu   sync.Mutex
	held bool
}

func (b *orderedBridge) TryAcquireForPrompt() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held {
		return false
	}
	b.held = true
	b.rec.add("bridge.acquire")
	return true
}

func (b *orderedBridge) ReleaseAfterPrompt() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held = false
	b.rec.add("bridge.release")
}

func (b *orderedBridge) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	b.rec.add("call")
	return b.recordingBridge.CallAt(ctx, method, params)
}

// scriptedAdmission is a TurnOutcomeAccess with a REAL single-holder admission
// slot, the shared log, and a scripted captured result. afterReservationRelease
// runs synchronously after ReleaseTurnReservation returns — the injected pause
// between the goroutine's releases and the recovery's re-reserve.
type scriptedAdmission struct {
	rec    *callRecorder
	result marotte.TurnResult
	// startRefused makes StartTurn answer false: the dead-ctx / dropped-id arm.
	startRefused            bool
	afterReservationRelease func()
	mu                      sync.Mutex
	reserved                bool
	admitFirst              AdmissionOutcome
	admitted                int
}

func (a *scriptedAdmission) ReserveTurnForPrompt(context.Context, marotte.ChatID, time.Duration) AdmissionOutcome {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admitted++
	if a.admitted == 1 && a.admitFirst != AdmissionAcquired {
		a.rec.add("admission.refused")
		return a.admitFirst
	}
	if a.reserved {
		a.rec.add("admission.refused")
		return AdmissionBusy
	}
	a.reserved = true
	a.rec.add("admission.acquired")
	return AdmissionAcquired
}

func (a *scriptedAdmission) PromptHolder(marotte.ChatID) (string, bool) { return "", false }

func (a *scriptedAdmission) TryReserveIdleTurn(c marotte.ChatID, s marotte.TurnOpenSource) bool {
	return a.TryReserveTurn(c, s)
}

func (a *scriptedAdmission) TryReserveTurnFenced(c marotte.ChatID, s marotte.TurnOpenSource, _ TurnFence) bool {
	return a.TryReserveTurn(c, s)
}

func (a *scriptedAdmission) TryReserveTurn(_ marotte.ChatID, _ marotte.TurnOpenSource) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rec.add("tryReserve")
	if a.reserved {
		return false
	}
	a.reserved = true
	return true
}

func (a *scriptedAdmission) ReleaseTurnReservation(marotte.ChatID) {
	a.mu.Lock()
	a.reserved = false
	a.mu.Unlock()
	a.rec.add("reservation.release")
	if a.afterReservationRelease != nil {
		a.afterReservationRelease()
	}
}

func (a *scriptedAdmission) AdmissionHolderSource(marotte.ChatID) (marotte.TurnOpenSource, bool) {
	return 0, false
}

// admissionTurnID is the one turn the scripted admission opens.
const admissionTurnID = "t-1"

func (a *scriptedAdmission) OpenTurn(context.Context, marotte.ChatID, TurnOpen) (string, error) {
	a.rec.add("openTurn")
	return admissionTurnID, nil
}

func (a *scriptedAdmission) StartTurn(context.Context, marotte.ChatID, string) bool {
	a.rec.add("startTurn")
	return !a.startRefused
}

func (a *scriptedAdmission) AwaitTurn(context.Context, marotte.ChatID, string) (marotte.TurnResult, error) {
	a.rec.add("await")
	return a.result, nil
}

func (a *scriptedAdmission) ReleaseTurn(marotte.ChatID, string) { a.rec.add("releaseTurn") }

func (a *scriptedAdmission) SettleTurnOnResponse(context.Context, marotte.ChatID, string, uint64, *marotte.RPCResponse) {
	a.rec.add("settle")
}

func (a *scriptedAdmission) TurnOpenedAfter(marotte.ChatID, string) bool {
	a.rec.add("openedAfter")
	return false
}

func (a *scriptedAdmission) StopRequestedAfter(marotte.ChatID, string) bool { return false }

func (a *scriptedAdmission) FinalizeLocalShellTurn(context.Context, marotte.ChatID, string, string) {}

func (a *scriptedAdmission) AbandonInFlightTurn(context.Context, marotte.ChatID, string, marotte.StopReason, string, marotte.FailureKind, uint64) {
	a.rec.add("abandon")
}

// admissionHost wires the scripted admission and the ordered bridge over the
// store-backed host double.
type admissionHost struct {
	hostDouble
	admission *scriptedAdmission
	bridge    Bridge
	openErr   error
	rec       *callRecorder
	mu        sync.Mutex
	events    []marotte.ServerEvent
}

func (h *admissionHost) OpenBridge(context.Context, marotte.ChatID, string) (Bridge, error) {
	h.rec.add("openBridge")
	if h.openErr != nil {
		return nil, h.openErr
	}
	return h.bridge, nil
}

func (h *admissionHost) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, evt)
}

func (h *admissionHost) errorCodes() []marotte.ErrorCode {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []marotte.ErrorCode
	for _, evt := range h.events {
		if evt.Type != marotte.EventError {
			continue
		}
		if p, ok := evt.Payload.(marotte.ErrorPayload); ok {
			out = append(out, p.Code)
		}
	}
	return out
}

// newAdmissionFixture builds the wired prompt roles plus the join handle.
func newAdmissionFixture(t *testing.T, result marotte.TurnResult, startRefused bool) (*admissionHost, *promptRoles, *promptJoin) {
	t.Helper()
	rec := &callRecorder{}
	host := &admissionHost{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		admission:  &scriptedAdmission{rec: rec, result: result, startRefused: startRefused},
		bridge:     &orderedBridge{rec: rec},
		rec:        rec,
	}
	roles := promptRolesOf(host)
	roles.bridges = host
	roles.bus = host
	roles.admission = host.admission
	roles.turnOutcome = host.admission
	join := &promptJoin{}
	roles.lifecycle = join
	return host, roles, join
}

// The ack precedes the turn AND the bridge spawn: the POST answers while
// OpenBridge is still parked, and the turn's call happens only afterwards.
func TestCmdPrompt_AcksBeforeOpenBridgeAndTurnCompletion(t *testing.T) {
	gate := make(chan struct{})
	host, roles, join := newAdmissionFixture(t, marotte.TurnResult{}, false)
	blocked := &gatedBridgeAccess{inner: host, gate: gate}
	roles.bridges = blocked

	got, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))
	if err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	ack, ok := got.(promptAck)
	if !ok {
		t.Fatalf("CmdPrompt returned %T, want promptAck", got)
	}
	if !ack.Accepted || ack.MessageID != "m-1" {
		t.Errorf("ack = %+v, want accepted with the message id", ack)
	}
	if n := host.rec.indexOf("call"); n != -1 {
		t.Error("the ACP call ran before the ack was released to the client")
	}
	close(gate)
	join.join()
	if host.rec.indexOf("call") == -1 {
		t.Error("the turn never ran after the ack")
	}
}

// gatedBridgeAccess parks OpenBridge until the test releases it.
type gatedBridgeAccess struct {
	inner *admissionHost
	gate  chan struct{}
}

func (g *gatedBridgeAccess) Bridge(id marotte.ChatID) Bridge   { return g.inner.Bridge(id) }
func (g *gatedBridgeAccess) BridgeLive(id marotte.ChatID) bool { return g.inner.BridgeLive(id) }
func (g *gatedBridgeAccess) CloseBridge(ctx context.Context, id marotte.ChatID, outcome marotte.TurnOutcome) {
	g.inner.CloseBridge(ctx, id, outcome)
}

func (g *gatedBridgeAccess) OpenBridge(ctx context.Context, id marotte.ChatID, model string) (Bridge, error) {
	select {
	case <-g.gate:
	case <-ctx.Done():
	}
	return g.inner.OpenBridge(ctx, id, model)
}

// A refused admission WRITES NOTHING: no turn is opened, so the log never holds
// a turn no process owns, and the draft that held the text stays where the
// client's 409-to-steer conversion can still find it.
func TestCmdPrompt_A409WritesNothing(t *testing.T) {
	cases := map[string]struct {
		refusal    AdmissionOutcome
		wantReason string
	}{
		"plain busy":       {refusal: AdmissionBusy, wantReason: ""},
		"starting variant": {refusal: AdmissionStarting, wantReason: "starting"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			host := &admissionHost{
				hostDouble: newTestHost(t, store),
				admission:  &scriptedAdmission{rec: &callRecorder{}, admitFirst: tc.refusal, reserved: true},
				rec:        &callRecorder{},
			}
			roles := promptRolesOf(host)
			roles.bridges = host
			roles.bus = host
			roles.admission = host.admission
			roles.turnOutcome = host.admission
			seedEmptyChat(t, store, "c1")
			if _, err := store.SetDraft(t.Context(), "c1", "typed text"); err != nil {
				t.Fatalf("seed draft: %v", err)
			}

			_, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))

			if statusOf(err) != http.StatusConflict {
				t.Fatalf("status = %d, want 409", statusOf(err))
			}
			var se *statusError
			if !errors.As(err, &se) {
				t.Fatalf("error is %T, want *statusError", err)
			}
			if se.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", se.reason, tc.wantReason)
			}
			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("chat vanished")
			}
			if host.admission.rec.indexOf("openTurn") != -1 {
				t.Errorf("a refused prompt opened a turn; log = %v", host.admission.rec.snapshot())
			}
			if c.Draft != "typed text" {
				t.Errorf("draft = %q, want the refused prompt's text left in place", c.Draft)
			}
		})
	}
}

// The reason field is ADDITIVE on the wire: present on the starting refusal,
// absent everywhere else — existing clients that only read `error` see the
// envelope they always saw.
func TestWriteErr_EmitsTheReasonAdditively(t *testing.T) {
	t.Run("starting refusal carries it", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeErr(w, StatusErrorReason(http.StatusConflict, reasonStarting, errBusy))
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if body["error"] != "busy" || body["reason"] != "starting" {
			t.Errorf("body = %v, want error busy with reason starting", body)
		}
	})
	t.Run("a plain status error omits the key", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeErr(w, StatusError(http.StatusConflict, errBusy))
		if strings.Contains(w.Body.String(), "reason") {
			t.Errorf("body = %q, want no reason key on a plain refusal", w.Body.String())
		}
	})
}

// A StartTurn that answers false is a failed turn, never a silent one and never
// an ACP call: the turn CmdPrompt opened is closed by the turn end rule, the
// failure is broadcast, and BOTH holds release so the chat is not wedged.
func TestCmdPrompt_ARefusedStartTurnFailsLoudAndReleasesBothSlots(t *testing.T) {
	host, roles, join := newAdmissionFixture(t, marotte.TurnResult{}, true)

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	if host.rec.indexOf("call") != -1 {
		t.Error("an ACP call was made for a turn that never started")
	}
	if host.rec.indexOf("abandon") == -1 {
		t.Error("the opened turn was never closed by the turn end rule")
	}
	codes := host.errorCodes()
	if len(codes) != 1 || codes[0] != marotte.ErrCodePromptFailed {
		t.Errorf("error codes = %v, want exactly [%s]", codes, marotte.ErrCodePromptFailed)
	}
	if host.rec.indexOf("bridge.release") == -1 {
		t.Error("the bridge slot was not released")
	}
	if host.rec.indexOf("reservation.release") == -1 {
		t.Error("the admission reservation was not released")
	}
	if host.admission.reserved {
		t.Error("the reservation is still held after the failure")
	}
}

// A prompt Call failure past the ack is SSE-only, and it releases everything:
// the failure broadcast case.
func TestCmdPrompt_CallFailureBroadcastsAndReleases(t *testing.T) {
	rec := &callRecorder{}
	failing := recordingBridge{callErr: errors.New("the pipe died")}
	host := &admissionHost{
		hostDouble: newTestHost(t, testsupport.NewInMemoryChatStore()),
		admission:  &scriptedAdmission{rec: rec},
		bridge:     &orderedBridge{rec: rec, recordingBridge: failing},
		rec:        rec,
	}
	roles := promptRolesOf(host)
	roles.bridges = host
	roles.bus = host
	roles.admission = host.admission
	roles.turnOutcome = host.admission
	join := &promptJoin{}
	roles.lifecycle = join

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	codes := host.errorCodes()
	if len(codes) != 1 || codes[0] != marotte.ErrCodePromptFailed {
		t.Errorf("error codes = %v, want exactly [%s]", codes, marotte.ErrCodePromptFailed)
	}
	if host.rec.indexOf("abandon") == -1 {
		t.Error("the failed turn was never finalized")
	}
	for _, want := range []string{"bridge.release", "reservation.release", "releaseTurn"} {
		if host.rec.indexOf(want) == -1 {
			t.Errorf("%s missing from %v: a failed call must release everything", want, host.rec.snapshot())
		}
	}
}

// firingResult is the captured outcome that arms the empty-turn recovery.
func firingResult() marotte.TurnResult {
	return marotte.TurnResult{Stop: marotte.StopReasonEndTurn, EmittedNothing: true, WireEnded: true}
}

// The settled path's release order is the design's contract: the result is
// captured while the turn handle is held, the bridge slot and the reservation
// release BEFORE the recovery arbitrates, the recovery's predicates read a
// still-live record, and ReleaseTurn goes LAST. The test FAILS if ReleaseTurn
// precedes the recovery read.
func TestCmdPrompt_RecoveryReadsTheCapturedResultBeforeTheHandleGoes(t *testing.T) {
	host, roles, join := newAdmissionFixture(t, firingResult(), false)

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	order := []string{"settle", "await", "bridge.release", "reservation.release", "openedAfter", "tryReserve", "releaseTurn"}
	last := -1
	for _, step := range order {
		idx := host.rec.indexOf(step)
		if idx == -1 {
			t.Fatalf("step %q never ran; log = %v", step, host.rec.snapshot())
		}
		if idx < last {
			t.Fatalf("step %q ran out of order; log = %v, want relative order %v", step, host.rec.snapshot(), order)
		}
		last = idx
	}
	if await, rel := host.rec.indexOf("await"), host.rec.indexOf("releaseTurn"); rel < await {
		t.Error("ReleaseTurn preceded the capture: the registry drops the result at the last release")
	}
}

// A prompt arriving during recovery preempts it: the goroutine released both
// holds before the recovery re-reserves, the competitor (injected in the pause
// between the releases and the re-reserve) acquires BOTH, and the recovery's
// try-reserve refusal abandons the retry.
func TestCmdPrompt_APromptDuringRecoveryPreemptsTheRetry(t *testing.T) {
	host, roles, join := newAdmissionFixture(t, firingResult(), false)
	var competitor AdmissionOutcome
	competitorGotBridge := false
	host.admission.afterReservationRelease = func() {
		competitor = host.admission.ReserveTurnForPrompt(context.Background(), "c1", 0)
		competitorGotBridge = host.bridge.TryAcquireForPrompt()
	}

	if _, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing")); err != nil {
		t.Fatalf("CmdPrompt = %v, want the early ack", err)
	}
	join.join()

	if competitor != AdmissionAcquired {
		t.Fatalf("the competing prompt's admission = %v, want acquired: both holds must be free in the pause", competitor)
	}
	if !competitorGotBridge {
		t.Fatal("the competing prompt could not take the bridge slot in the pause")
	}
	if got := host.rec.snapshot(); host.rec.indexOf("tryReserve") == -1 {
		t.Fatalf("the recovery never tried to re-reserve; log = %v", got)
	}
	calls := 0
	for _, e := range host.rec.snapshot() {
		if e == "call" {
			calls++
		}
	}
	if calls != 1 {
		t.Errorf("ACP calls = %d, want 1: an abandoned retry must not re-prompt", calls)
	}
}

// A re-send of the same text after a 409-starting reuses the message id: the
// refused attempt opened nothing, so the fresh attempt opens the one turn and
// replays the ack.
func TestCmdPrompt_IdempotentRetryReplaysTheAck(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	rec := &callRecorder{}
	host := &admissionHost{
		hostDouble: newTestHost(t, store),
		admission:  &scriptedAdmission{rec: rec, admitFirst: AdmissionStarting},
		bridge:     &orderedBridge{rec: rec},
		rec:        rec,
	}
	roles := promptRolesOf(host)
	roles.bridges = host
	roles.bus = host
	roles.admission = host.admission
	roles.turnOutcome = host.admission
	join := &promptJoin{}
	roles.lifecycle = join

	_, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("first send status = %d, want 409 starting", statusOf(err))
	}

	got, err := CmdPrompt(t.Context(), roles, promptReq(t, "c1", "do the thing"))
	if err != nil {
		t.Fatalf("re-send = %v, want the ack", err)
	}
	join.join()
	ack, ok := got.(promptAck)
	if !ok || !ack.Accepted || ack.MessageID != "m-1" {
		t.Fatalf("re-send answered %+v, want the ack with the same message id", got)
	}
	opened := 0
	for _, e := range rec.snapshot() {
		if e == "openTurn" {
			opened++
		}
	}
	if opened != 1 {
		t.Errorf("turns opened = %d, want 1: the refused attempt wrote nothing and the retry opened the turn", opened)
	}
}

// promptJoin is a LifecycleAccess whose in-flight count a test can join on:
// CmdPrompt acks before its turn runs, so a test asserting on the turn's
// effects waits for the goroutine to deregister first. TurnContext matches the
// production derivation (detached from the request, so the goroutine survives
// the handler's return).
type promptJoin struct{ wg sync.WaitGroup }

func (l *promptJoin) TurnContext(reqCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(reqCtx))
}
func (l *promptJoin) InflightAdd(delta int) { l.wg.Add(delta) }
func (l *promptJoin) InflightDone()         { l.wg.Done() }
func (l *promptJoin) Draining() bool        { return false }

// join blocks until every in-flight turn has deregistered. Safe to call after
// CmdPrompt returned: the registration is synchronous, before the ack.
func (l *promptJoin) join() { l.wg.Wait() }
