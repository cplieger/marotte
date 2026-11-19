package ruleengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
)

const testPrompt = "Check the queue."

// placement is where the fake run order puts a prompt the engine sends.
type placement uint8

const (
	// opensTurn: the chat is free, so the prompt's turn opens at once.
	opensTurn placement = iota
	// waitsInRow: a turn opened between the free check and the send, so the row waits pending on Chat.Run.
	waitsInRow
	// launchFails: the prompt's turn failed to open, so the row waits paused on Chat.Run, marked FailedLaunch.
	launchFails
)

// sent is one Notify call the fake received.
type sent struct {
	opts  command.NotifyOptions
	from  *marotte.Caller
	chat  marotte.ChatID
	text  string
	at    time.Time
	place placement
}

// errProcessGone answers every call a crashed process would never have made.
var errProcessGone = errors.New("the process is gone")

// fakeNotifier stands in for the Dispatcher's Notify and writes what the real run order writes: the prompt goes on
// Chat.Run as a notify-door row, onto its sender's waiting row when one is there (marotte.Caller.SameSender), and
// answers that row's id; a row that opens its turn leaves the list, opens the chat's turn in the log with the rule's
// caller on its prompt, tells the engine's turn-open tap and leaves the Bot busy until the test closes it. place,
// when set, scripts each call (opensTurn by default); after runs once the call is answered, afterUnqueue once an
// Unqueue took its row.
type fakeNotifier struct {
	place        func(n int) (placement, error)
	after        func()
	afterUnqueue func()
	gone         func() bool
	store        *chat.Store
	eng          *Engine
	turns        *turnState
	calls        []sent
	unqueued     []string
	mu           sync.Mutex
}

func (f *fakeNotifier) Notify(ctx context.Context, chatID marotte.ChatID, text string, from *marotte.Caller,
	opts command.NotifyOptions,
) (string, error) {
	if f.gone() {
		return "", errProcessGone
	}
	f.mu.Lock()
	place, err := opensTurn, error(nil)
	if f.place != nil {
		place, err = f.place(len(f.calls))
	}
	f.calls = append(f.calls, sent{chat: chatID, text: text, from: from, opts: opts, at: time.Now(), place: place})
	msg := "m-" + strconv.Itoa(len(f.calls))
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	if _, err := f.store.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool {
		i := slices.IndexFunc(c.Run, func(e marotte.RunEntry) bool {
			return e.Origin == marotte.RunFromNotify && !e.State.Live() && e.Row.Caller.SameSender(from)
		})
		if i < 0 && place == opensTurn {
			return false
		}
		if i < 0 {
			row := &marotte.QueuedPrompt{ID: msg, Text: text, Label: opts.DisplayText, Caller: from}
			c.Run = append(c.Run, marotte.RunEntry{ID: msg, Kind: marotte.RunKindRow, Origin: marotte.RunFromNotify, Row: row})
			i = len(c.Run) - 1
		}
		msg = c.Run[i].ID
		c.Run[i].Row.Text = text
		c.Run[i].State, c.Run[i].FailedLaunch = marotte.RunPending, false
		switch place {
		case opensTurn:
			c.Run = slices.Delete(c.Run, i, i+1)
		case launchFails:
			c.Run[i].State, c.Run[i].FailedLaunch = marotte.RunPaused, true
		case waitsInRow:
		}
		return true
	}); err != nil {
		return "", err
	}
	if place == opensTurn {
		if err := f.openTurn(ctx, chatID, &marotte.EntryPrompt{ID: msg, Text: text, Caller: from}); err != nil {
			return "", err
		}
	}
	if f.after != nil {
		f.after()
	}
	return msg, nil
}

// openTurn writes prompt's turn_open and tells the engine, as openTurnLocked does before the door answers.
func (f *fakeNotifier) openTurn(ctx context.Context, chatID marotte.ChatID, prompt *marotte.EntryPrompt) error {
	opened, err := f.store.OpenTurn(ctx, chatID, &chat.TurnSpec{Prompt: prompt, Source: marotte.TurnOpenNamePrompt}, nil)
	if err != nil {
		return err
	}
	f.turns.setLive(chatID, true)
	if f.eng != nil {
		f.eng.TurnOpened(chatID, opened.ID, marotte.TurnSourcePrompt, prompt.Caller, false)
	}
	return nil
}

// Unqueue takes the row messageID off chatID's run list, as unqueue_prompt does, and counts the call.
func (f *fakeNotifier) Unqueue(ctx context.Context, chatID marotte.ChatID, messageID string) error {
	if f.gone() {
		return errProcessGone
	}
	f.mu.Lock()
	f.unqueued = append(f.unqueued, messageID)
	f.mu.Unlock()
	_, err := f.store.Mutate(ctx, chatID, func(c *marotte.Chat, _ bool) bool {
		n := len(c.Run)
		c.Run = slices.DeleteFunc(c.Run, func(e marotte.RunEntry) bool { return e.ID == messageID })
		return len(c.Run) != n
	})
	if err == nil && f.afterUnqueue != nil {
		f.afterUnqueue()
	}
	return err
}

func (f *fakeNotifier) unqueuedRows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.unqueued)
}

func (f *fakeNotifier) sent() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.calls...)
}

// turnState is the runtime's live and waiting facts per chat, which the test moves.
type turnState struct {
	live    map[marotte.ChatID]bool
	waiting map[marotte.ChatID]bool
	mu      sync.Mutex
}

func newTurnState() *turnState {
	return &turnState{live: map[marotte.ChatID]bool{}, waiting: map[marotte.ChatID]bool{}}
}

func (s *turnState) setLive(id marotte.ChatID, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live[id] = v
}

func (s *turnState) setWaiting(id marotte.ChatID, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting[id] = v
}

func (s *turnState) isLive(id marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

func (s *turnState) isWaiting(id marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waiting[id]
}

// errWriteFault is the record write a full or broken disk refuses.
var errWriteFault = errors.New("no space left on device")

// faultStore is the real store with its writes made to fail on demand: failing refuses every write until cleared,
// failsAfterApply does too but runs each write's closure first, as a disk refusing the save does, and dieAfter(n)
// lets n more writes through and then refuses the rest of the process's life, as a crash does. gets counts the
// record reads; afterGet, when set, runs once a read has answered.
type faultStore struct {
	afterGet     func(chatID marotte.ChatID)
	beforeMutate func()
	*chat.Store
	failing         atomic.Bool
	failsAfterApply atomic.Bool
	gone            atomic.Bool
	left            atomic.Int64
	gets            atomic.Int64
}

func (s *faultStore) Get(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, bool) {
	s.gets.Add(1)
	c, ok := s.Store.Get(ctx, chatID)
	if s.afterGet != nil {
		s.afterGet(chatID)
	}
	return c, ok
}

func (s *faultStore) Mutate(ctx context.Context, chatID marotte.ChatID, f func(c *marotte.Chat, exists bool) bool) (string, error) {
	if s.beforeMutate != nil {
		s.beforeMutate()
	}
	if s.gone.Load() || s.failing.Load() {
		return "", errWriteFault
	}
	if s.failsAfterApply.Load() {
		if _, err := s.Store.Mutate(ctx, chatID, func(c *marotte.Chat, exists bool) bool { f(c, exists); return false }); err != nil {
			return "", err
		}
		return "", errWriteFault
	}
	v, err := s.Store.Mutate(ctx, chatID, f)
	if err == nil && s.left.Add(-1) == 0 {
		s.gone.Store(true)
	}
	return v, err
}

// dieAfter ends the process once n more of the engine's writes have landed; dieAfter(0) ends it now.
func (s *faultStore) dieAfter(n int64) {
	if n == 0 {
		s.gone.Store(true)
		return
	}
	s.left.Store(n)
}

// syncBuffer is the engine's log, written by its goroutine and read by the test.
type syncBuffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// lines counts the log lines at level carrying msg.
func (w *syncBuffer) lines(level slog.Level, msg string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for line := range strings.Lines(w.b.String()) {
		if strings.Contains(line, "level="+level.String()) && strings.Contains(line, strconv.Quote(msg)) {
			n++
		}
	}
	return n
}

// undelivered is one push the engine asked for about a prompt it gave up on.
type undelivered struct {
	err  error
	chat marotte.ChatID
	rule string
}

// harness is one engine over a real chat store, run inside the test's synctest bubble.
type harness struct {
	t       *testing.T
	failed  []undelivered
	failMu  sync.Mutex
	store   *chat.Store
	fs      *faultStore
	notes   *fakeNotifier
	turns   *turnState
	eng     *Engine
	logs    *syncBuffer
	done    chan error
	cancel  context.CancelFunc
	t0      time.Time
	dir     string
	ready   atomic.Bool
	refused atomic.Bool
}

// newHarness opens a store in a fresh directory; seed Bots, then call start. The store's first complete scan has not
// run yet, so BotIDs answers incomplete until scan.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), t0: time.Now()}
	h.open()
	h.ready.Store(true)
	return h
}

// open opens the store over the harness's directory as a fresh process does, with nothing live in it.
func (h *harness) open() {
	h.t.Helper()
	s, err := chat.NewStore(h.dir)
	if err != nil {
		h.t.Fatalf("NewStore: %v", err)
	}
	h.store, h.fs, h.turns, h.logs = s, &faultStore{Store: s}, newTurnState(), &syncBuffer{}
	h.notes = &fakeNotifier{turns: h.turns, store: s, gone: h.fs.gone.Load}
}

func (h *harness) scan() { h.store.List(h.t.Context()) }

// start builds the engine and runs it until the test ends.
func (h *harness) start() {
	h.t.Helper()
	h.eng = New(&Deps{
		Store:    h.fs,
		Notifier: h.notes,
		Enter: func() (func(), bool) {
			if h.refused.Load() {
				return nil, false
			}
			return func() {}, true
		},
		Ready:    h.ready.Load,
		TurnLive: h.turns.isLive,
		Waiting:  h.turns.isWaiting,
		NotDelivered: func(_ context.Context, chatID marotte.ChatID, ruleName string, err error) {
			h.failMu.Lock()
			defer h.failMu.Unlock()
			h.failed = append(h.failed, undelivered{chat: chatID, rule: ruleName, err: err})
		},
		Logger: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	h.notes.eng = h.eng
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.eng.Run(ctx) }()
	h.t.Cleanup(h.stop)
	synctest.Wait()
}

// stop drains the engine and waits for Run to return.
func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	if err := <-h.done; err != nil {
		h.t.Errorf("Run = %v, want nil on drain", err)
	}
}

// restart ends the process and starts the next one over the same directory: a new store, whose first open of each
// chat's log settles the run list the last process left, no turn live, a new notifier and a new engine. prep runs
// before the engine starts.
func (h *harness) restart(prep ...func()) {
	h.t.Helper()
	h.stop()
	h.open()
	h.scan()
	for _, f := range prep {
		f()
	}
	h.start()
}

// advance moves the bubble's clock by d and lets every goroutine settle.
func (*harness) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// at is the unix ms of the bubble's start plus d.
func (h *harness) at(d time.Duration) int64 { return h.t0.Add(d).UnixMilli() }

// endTurn closes chatID's turn the way closeTurn does: the record's last turn end first, then the tap.
func (h *harness) endTurn(chatID marotte.ChatID) {
	h.t.Helper()
	h.mutate(chatID, func(c *marotte.Chat) { c.LastTurnEndedAt = time.Now().UnixMilli() })
	h.turns.setLive(chatID, false)
	h.eng.TurnClosed(chatID, "turn-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
	synctest.Wait()
}

// takeQueuedRow opens the turn of the notify door's row waiting on chatID's run list, as the run order does when the
// chat frees: the row leaves Chat.Run and its prompt opens the turn, with its caller. tap says whether the engine's
// turn-open tap hears of it.
func (h *harness) takeQueuedRow(chatID marotte.ChatID, tap bool) {
	h.t.Helper()
	row := h.dropQueuedRow(chatID)
	prompt := marotte.EntryPrompt{ID: row.ID, Text: row.Text, Caller: row.Caller}
	opened, err := h.store.OpenTurn(h.t.Context(), chatID, &chat.TurnSpec{Prompt: &prompt, Source: marotte.TurnOpenNamePrompt}, nil)
	if err != nil {
		h.t.Fatalf("OpenTurn: %v", err)
	}
	h.turns.setLive(chatID, true)
	if tap {
		h.eng.TurnOpened(chatID, opened.ID, marotte.TurnSourcePrompt, row.Caller, false)
	}
	synctest.Wait()
}

// dropQueuedRow takes the notify door's row off chatID's run list, as the user's discard does, and answers it.
func (h *harness) dropQueuedRow(chatID marotte.ChatID) marotte.QueuedPrompt {
	h.t.Helper()
	var row *marotte.QueuedPrompt
	h.mutate(chatID, func(c *marotte.Chat) {
		i := slices.IndexFunc(c.Run, func(e marotte.RunEntry) bool { return e.Origin == marotte.RunFromNotify })
		if i < 0 {
			return
		}
		row = c.Run[i].Row
		c.Run = slices.Delete(c.Run, i, i+1)
	})
	if row == nil {
		h.t.Fatalf("no notify row on %s's run list", chatID)
	}
	return *row
}

// runRows answers the rows on chatID's run list that carry a rule's caller.
func (h *harness) runRows(chatID marotte.ChatID) []marotte.RunEntry {
	h.t.Helper()
	c, ok := h.store.Get(h.t.Context(), chatID)
	if !ok {
		h.t.Fatalf("no chat %s", chatID)
	}
	var out []marotte.RunEntry
	for _, e := range c.Run {
		if e.Row != nil && e.Row.Caller != nil && e.Row.Caller.Kind == marotte.CallerKindRule {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) mutate(chatID marotte.ChatID, f func(c *marotte.Chat)) {
	h.t.Helper()
	if _, err := h.store.Mutate(h.t.Context(), chatID, func(c *marotte.Chat, _ bool) bool { f(c); return true }); err != nil {
		h.t.Fatalf("mutate %s: %v", chatID, err)
	}
}

// seedBot writes a started, open Bot holding rules.
func (h *harness) seedBot(chatID marotte.ChatID, rules ...rule.Rule) {
	h.t.Helper()
	h.mutate(chatID, func(c *marotte.Chat) {
		c.Name = string(chatID)
		c.Bot = &marotte.Bot{Goal: "Keep the queue short.", StartedAt: h.t0.UnixMilli(), Rules: rules}
	})
}

// rule reads chatID's stored rule id, failing the test when it is gone.
func (h *harness) rule(chatID marotte.ChatID, id string) rule.Rule {
	h.t.Helper()
	c, ok := h.store.Get(h.t.Context(), chatID)
	if !ok || c.Bot == nil {
		h.t.Fatalf("no Bot %s", chatID)
	}
	for i := range c.Bot.Rules {
		if c.Bot.Rules[i].ID == id {
			return c.Bot.Rules[i]
		}
	}
	h.t.Fatalf("Bot %s has no rule %s", chatID, id)
	return rule.Rule{}
}

func everyT(id string, minutes int, anchor rule.Anchor) rule.Trigger {
	return rule.Trigger{ID: id, Kind: rule.KindEvery, Every: &rule.EveryTrigger{Minutes: minutes, Anchor: anchor}}
}

func specOf(name string, cond *rule.Cond, triggers ...rule.Trigger) rule.Spec {
	return rule.Spec{Name: name, Prompt: testPrompt, Enabled: true, Triggers: triggers, Condition: cond}
}

// armedRule is rule id over spec with every trigger armed at armedAt and nothing else in its status.
func armedRule(id string, spec rule.Spec, armedAt int64) rule.Rule {
	st := rule.Status{Triggers: map[string]rule.TriggerState{}}
	for _, tr := range spec.Triggers {
		st.Triggers[tr.ID] = rule.TriggerState{ArmedAt: armedAt}
	}
	return rule.Rule{ID: id, Spec: spec, Status: st}
}

func leafCond(l rule.Leaf) *rule.Cond { return &rule.Cond{Leaf: &l} }

// wantSent fails unless the fake received exactly n deliveries.
func (h *harness) wantSent(label string, n int) []sent {
	h.t.Helper()
	got := h.notes.sent()
	if len(got) != n {
		h.t.Fatalf("%s: %d deliveries, want %d: %+v", label, len(got), n, got)
	}
	return got
}

func eventIDOf(s sent) string {
	if s.from == nil {
		return ""
	}
	return s.from.Event
}

func dueID(trigger string, dueMs int64) string {
	return "every:" + trigger + ":" + strconv.FormatInt(dueMs, 10)
}

// ruleCaller is the caller a delivery of rule r1 "Tidy" for eventID carries.
func ruleCaller(eventID string) *marotte.Caller {
	return &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Tidy", Rule: "r1", Event: eventID, Depth: 1}
}

// eventBlock decodes the fenced event block of a delivered prompt.
func eventBlock(t *testing.T, text string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(text, "~~~json marotte-event\n")
	if !ok {
		t.Fatalf("no event block in %q", text)
	}
	body, _, ok := strings.Cut(rest, "\n~~~")
	if !ok {
		t.Fatalf("an unclosed event block in %q", text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("event block %q: %v", body, err)
	}
	return out
}

func (h *harness) undelivered() []undelivered {
	h.failMu.Lock()
	defer h.failMu.Unlock()
	return slices.Clone(h.failed)
}
