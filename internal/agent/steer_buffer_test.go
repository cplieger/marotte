package agent

// The steer record: what KAS's buffer holds and what the user's dock rows are, for
// every chat. Nothing reads KAS's buffer back, and its read cursor does not rewind
// on a clear (kirodotdev/Kiro#11449), so these tests drive the record through the
// same calls the frames and the commands make and assert what it decided.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

// steerSpy captures what the record owes once its lock is released.
type steerSpy struct {
	notes  map[string]*marotte.EntrySteer
	frames []marotte.SteerQueuedPayload
	jobs   []command.SteerJob
	mu     sync.Mutex
}

func (s *steerSpy) note(id string) *marotte.EntrySteer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notes[id]
}

func (s *steerSpy) takeJobs() []command.SteerJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.jobs
	s.jobs = nil
	return out
}

func (s *steerSpy) framesFor(key string) []marotte.SteerQueuedPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []marotte.SteerQueuedPayload
	for _, f := range s.frames {
		if f.SteerID == key {
			out = append(out, f)
		}
	}
	return out
}

// steerHarness drives one chat's record the way the frames and commands do.
type steerHarness struct {
	fatalf func(string, ...any)
	h      *Runtime
	recs   *steerRecords
	spy    *steerSpy
	chat   marotte.ChatID
	q      steerQueue
	turn   int
}

func newSteerHarness(t *testing.T) *steerHarness {
	t.Helper()
	h, _, _ := newTestHub()
	return harnessOn(h, "c1", t.Fatalf)
}

// harnessOn rewires the hub's record to a spy, so jobs run only when a test says.
func harnessOn(h *Runtime, chat marotte.ChatID, fatalf func(string, ...any)) *steerHarness {
	spy := &steerSpy{notes: map[string]*marotte.EntrySteer{}}
	recs := h.bus.steers
	recs.note = func(_ context.Context, _ marotte.ChatID, id string, steer *marotte.EntrySteer) {
		spy.mu.Lock()
		spy.notes[id] = steer
		spy.mu.Unlock()
	}
	recs.broadcast = func(e marotte.ServerEvent) {
		if p, ok := e.Payload.(marotte.SteerQueuedPayload); ok {
			spy.mu.Lock()
			spy.frames = append(spy.frames, p)
			spy.mu.Unlock()
		}
	}
	recs.runJob = func(j command.SteerJob) {
		spy.mu.Lock()
		spy.jobs = append(spy.jobs, j)
		spy.mu.Unlock()
	}
	recs.promptHeld = func(marotte.ChatID) bool { return false }
	return &steerHarness{fatalf: fatalf, h: h, recs: recs, spy: spy, chat: chat, q: h.steerQueue}
}

var promptHolder = command.SteerHolder{Held: true, PromptClass: true, Live: true}

func (s *steerHarness) bind() string {
	s.turn++
	id := "t-" + string(rune('0'+s.turn))
	s.recs.TurnBound(s.chat, id)
	return id
}

func (s *steerHarness) endTurn() {
	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{TurnID: s.turnID(), Source: marotte.TurnSourcePrompt})
}

// kas folds each send the way KAS answers it: steering_queued, then the reply.
func (s *steerHarness) kas(sends []command.SteerSend) {
	for _, snd := range sends {
		s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: snd.ID, Text: snd.Text, Origin: marotte.SteerOriginUser})
		s.q.SteerSent(s.chat, snd, true, nil)
	}
}

func (s *steerHarness) steer(key, text string) []command.SteerSend {
	sends, refuse := s.q.RouteSteer(s.chat, key, text, promptHolder)
	if refuse != "" {
		s.fatalf("RouteSteer(%s) refused %q", key, refuse)
	}
	s.kas(sends)
	return sends
}

// kasHolds is every id KAS's buffer holds for the chat, the reply a clear gives.
func (s *steerHarness) kasHolds() []string {
	s.recs.mu.Lock()
	defer s.recs.mu.Unlock()
	rec := s.recs.chats[s.chat]
	if rec == nil {
		return nil
	}
	var ids []string
	rec.live(func(w *dockRow) {
		if w.state.inKAS() && !slices.Contains(ids, w.kasID) {
			ids = append(ids, w.kasID)
		}
	})
	for _, o := range rec.others {
		ids = append(ids, o.SteerID)
	}
	return ids
}

// clearKAS is a landed clear: KAS's cleared frame, then its reply's ids.
func (s *steerHarness) clearKAS() []string {
	ids := s.kasHolds()
	if len(ids) > 0 {
		s.recs.SteerCleared(s.chat, ids)
	}
	return ids
}

// remove runs a delete through its clear and answers the record's verdict.
func (s *steerHarness) remove(key string) (res command.SteerOpResult, needsClear bool, refuse string) {
	op := "op-" + key
	needsClear, refuse = s.q.BeginRemove(s.chat, key, op)
	if refuse != "" || !needsClear {
		return res, needsClear, refuse
	}
	return s.q.RemoveCleared(s.chat, op, s.clearKAS(), true), true, ""
}

func (s *steerHarness) resubmit(key string, res command.SteerOpResult) {
	if res.Resend != nil {
		s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: res.Resend.ID, Text: res.Resend.Text, Origin: marotte.SteerOriginUser})
		s.q.OpSent(s.chat, "op-"+key, *res.Resend, true, nil)
	}
	s.q.EndOp(s.chat, "op-"+key)
}

func (s *steerHarness) look(fn func(rec *steerRecord)) {
	s.recs.mu.Lock()
	defer s.recs.mu.Unlock()
	if rec := s.recs.chats[s.chat]; rec != nil {
		fn(rec)
	}
}

func (s *steerHarness) channel() (c channelState) {
	s.look(func(rec *steerRecord) { c = rec.channel })
	return c
}

func (s *steerHarness) turnID() (id string) {
	s.look(func(rec *steerRecord) { id = rec.turnID })
	return id
}

func (s *steerHarness) probe() (id string) {
	s.look(func(rec *steerRecord) { id = rec.probe })
	return id
}

// state answers a row's state, or rowRead with ok false once it is gone.
func (s *steerHarness) state(key string) (st rowState, ok bool) {
	s.look(func(rec *steerRecord) {
		if w := rec.row(key); w != nil {
			st, ok = w.state, true
		}
	})
	return st, ok
}

func (s *steerHarness) kasID(key string) (id string) {
	s.look(func(rec *steerRecord) {
		if w := rec.row(key); w != nil {
			id = w.kasID
		}
	})
	return id
}

func (s *steerHarness) mustState(key string, want rowState) {
	if got, ok := s.state(key); !ok || got != want {
		s.fatalf("row %s state = %v (live %v), want %v", key, got, ok, want)
	}
}

// --- the tables ---

// The tables, cell by cell: each line is one event, columns in state order. A cell
// that changes here is a change to what the record does.
var wantChannelRules = [nSteerEvents][nChannelStates]chanRule{
	evBind:          {cOpen, cImpossible, cImpossible, cImpossible},
	evRevise:        {cImpossible, cKeep, cKeep, cKeep},
	evSteer:         {cKeep, cKeep, cProbing, cKeep},
	evDeleteQueued:  {cKeep, cResubmit, cResubmit, cResubmit},
	evDeleteWaiting: {cImpossible, cKeep, cImpossible, cOwed},
	evDeleteIdle:    {cKeep, cKeep, cKeep, cKeep},
	evDiscard:       {cKeep, cUnconfirmed, cUnconfirmed, cUnconfirmed},
	evInjectedProbe: {cKeep, cImpossible, cImpossible, cOpen},
	evInjectedOther: {cKeep, cKeep, cKeep, cKeep},
	evAckProbe:      {cKeep, cImpossible, cImpossible, cOwed},
	evTurnEnd:       {cNone, cNone, cNone, cNone},
	evTurnDeath:     {cNone, cNone, cNone, cNone},
	evForeignClear:  {cKeep, cOwed, cKeep, cOwed},
	evTeardown:      {cNone, cNone, cNone, cNone},
	evRefused:       {cKeep, cUnconfirmed, cKeep, cUnconfirmed},
	evPostLoadClear: {cKeep, cImpossible, cImpossible, cImpossible},
	evJobWake:       {cKeep, cFlush, cKeep, cFlush},
}

// Columns: parked, queued, outstanding, waiting, cleared, unsent. The terminal
// states keep every event.
var wantRowRules = [nSteerEvents][rowRead]rowRule{
	evBind:          {rKeep, rKeep, rKeep, rKeep, rKeep, rKeep},
	evRevise:        {rKeep, rKeep, rKeep, rKeep, rKeep, rKeep},
	evSteer:         {rRoute, rKeep, rKeep, rRoute, rRoute, rKeep},
	evDeleteQueued:  {rKeep, rResubmit, rResubmit, rJoinProbe, rJoinProbe, rKeep},
	evDeleteWaiting: {rKeep, rKeep, rKeep, rDeleted, rKeep, rKeep},
	evDeleteIdle:    {rDeleted, rKeep, rKeep, rKeep, rDeleted, rDeleted},
	evDiscard:       {rDiscarded, rDiscarded, rDiscarded, rDiscarded, rDiscarded, rDiscarded},
	evInjectedProbe: {rKeep, rKeep, rRead, rKeep, rKeep, rKeep},
	evInjectedOther: {rKeep, rRead, rImpossible, rKeep, rKeep, rRead},
	evAckProbe:      {rKeep, rKeep, rRead, rJoinProbe, rKeep, rKeep},
	evTurnEnd:       {rCollect, rCollect, rCollect, rCollect, rCollect, rKeep},
	evTurnDeath:     {rCollect, rCollect, rCollect, rCollect, rCollect, rKeep},
	evForeignClear:  {rKeep, rCleared, rCleared, rKeep, rKeep, rKeep},
	evTeardown:      {rDrop, rDrop, rDrop, rDrop, rDrop, rDrop},
	evRefused:       {rImpossible, rCleared, rCleared, rCleared, rKeep, rKeep},
	evPostLoadClear: {rKeep, rPark, rImpossible, rImpossible, rKeep, rKeep},
	evJobWake:       {rKeep, rKeep, rKeep, rRoute, rKeep, rKeep},
}

func TestSteerTables_EveryCellIsTheRatifiedOne(t *testing.T) {
	for ev := range nSteerEvents {
		for c := range nChannelStates {
			if got, want := channelRule(c, ev), wantChannelRules[ev][c]; got != want || want == cUnhandled {
				t.Errorf("channelRule(%d, E%d) = %d, want %d", c, ev, got, want)
			}
		}
		for s := range nRowStates {
			want := rKeep
			if !s.terminal() {
				want = wantRowRules[ev][s]
			}
			if got := rowRuleFor(s, ev); got != want || want == rUnhandled {
				t.Errorf("rowRuleFor(%v, E%d) = %d, want %d", s, ev, got, want)
			}
		}
	}
}

// probeSent is a chat in PROBING: steer-a deleted, steer-b the outstanding probe.
func probeSent(s *steerHarness) command.SteerOpResult {
	s.bind()
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	res, _, _ := s.remove("steer-a")
	s.resubmit("steer-a", res)
	return res
}

// Each transition the tables name, driven through the production fold or command
// that applies it, with the row and channel it must leave. rowDone means the row
// left the dock.
func TestSteerTables_TheFoldsApplyTheirCells(t *testing.T) {
	cases := []struct {
		run     func(s *steerHarness)
		name    string
		key     string
		row     rowState
		channel channelState
	}{
		{name: "E0 NONE binds OPEN", key: "steer-a", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			s.q.RouteSteer(s.chat, "steer-a", "a", promptHolder)
			s.bind()
		}},
		{name: "E0 in OPEN ends the old turn first", key: "steer-a", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.bind()
			if jobs := s.spy.takeJobs(); len(jobs) != 1 || jobs[0].End == nil {
				s.fatalf("jobs = %+v, want the discarded turn's end", jobs)
			}
		}},
		{name: "E0′ keeps the channel", key: "steer-a", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.TurnRevised(s.chat, "t-other")
			if s.turnID() != "t-other" {
				s.fatalf("turn = %q, want t-other", s.turnID())
			}
		}},
		{name: "E1 OPEN sends as itself", key: "steer-a", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
		}},
		{name: "E1 UNCONFIRMED becomes the probe", key: "steer-c", row: rowOutstanding, channel: chanProbing, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			res, _, _ := s.remove("steer-a")
			s.resubmit("steer-a", res)
			s.steer("steer-c", "c")
		}},
		{name: "E1 PROBING waits", key: "steer-c", row: rowWaiting, channel: chanProbing, run: func(s *steerHarness) {
			probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
		}},
		{name: "E1 routes a cleared row again", key: "steer-a", row: rowOutstanding, channel: chanProbing, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.SteerCleared(s.chat, []string{"steer-a"})
			s.q.RouteSteer(s.chat, "steer-a", "a", promptHolder)
		}},
		{name: "E2 OPEN resubmits the kept row", key: "steer-b", row: rowOutstanding, channel: chanProbing, run: func(s *steerHarness) {
			probeSent(s)
		}},
		{name: "E2 OPEN with nothing kept", key: "steer-a", row: rowDone, channel: chanUnconfirmed, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			res, _, _ := s.remove("steer-a")
			s.resubmit("steer-a", res)
		}},
		{name: "E2 NONE resends unbound", key: "steer-b", row: rowQueued, channel: chanNone, run: func(s *steerHarness) {
			s.steer("steer-a", "a")
			s.steer("steer-b", "b")
			res, _, _ := s.remove("steer-a")
			s.resubmit("steer-a", res)
		}},
		{name: "E3 deletes the last owed waiter", key: "steer-c", row: rowDone, channel: chanUnconfirmed, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerForgotten(s.chat, []string{res.Resend.ID})
			s.spy.takeJobs()
			s.remove("steer-c")
		}},
		{name: "E4 resubmits waiting rows as a new probe", key: "steer-c", row: rowOutstanding, channel: chanProbing, run: func(s *steerHarness) {
			old := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			res, _, _ := s.remove("steer-b")
			s.resubmit("steer-b", res)
			if res.Resend == nil || res.Resend.ID == old.Resend.ID {
				s.fatalf("resend = %+v, want a fresh probe", res.Resend)
			}
		}},
		{name: "E5 deletes an unsent row", key: "steer-a", row: rowDone, channel: chanNone, run: func(s *steerHarness) {
			s.q.RouteSteer(s.chat, "steer-a", "a", command.SteerHolder{Held: true, PromptClass: true})
			s.q.RouteSteer(s.chat, "steer-a", "a", command.SteerHolder{})
			s.remove("steer-a")
		}},
		{name: "E6 discards and leaves UNCONFIRMED", key: "steer-a", row: rowDone, channel: chanUnconfirmed, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.q.BeginDiscard(s.chat, "op-d")
			s.clearKAS()
			s.q.DiscardCleared(s.chat, "op-d", true)
		}},
		{name: "E7 reads the probe and opens", key: "steer-b", row: rowDone, channel: chanOpen, run: func(s *steerHarness) {
			res := probeSent(s)
			s.recs.SteerRead(s.chat, res.Resend.ID)
		}},
		{name: "E7 leaves a waiter for the flush", key: "steer-c", row: rowWaiting, channel: chanOpen, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerRead(s.chat, res.Resend.ID)
		}},
		{name: "E8 reads a queued row", key: "steer-a", row: rowDone, channel: chanOpen, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.SteerRead(s.chat, "steer-a")
		}},
		{name: "E8 reads a re-injected copy", key: "steer-a", row: rowDone, channel: chanNone, run: func(s *steerHarness) {
			s.steer("steer-a", "a")
			s.recs.BridgeGone(s.chat)
			s.recs.SteerRead(s.chat, "steer-a")
		}},
		{name: "E9 owes the waiters a probe", key: "steer-c", row: rowWaiting, channel: chanProbing, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerForgotten(s.chat, []string{res.Resend.ID})
			if s.probe() != "" || len(s.spy.takeJobs()) != 1 {
				s.fatalf("probe = %q, want an owed probe and its job", s.probe())
			}
		}},
		{name: "E9 with no waiter", key: "steer-b", row: rowDone, channel: chanUnconfirmed, run: func(s *steerHarness) {
			res := probeSent(s)
			s.recs.SteerForgotten(s.chat, []string{res.Resend.ID})
		}},
		{name: "E10 collects and closes", key: "steer-a", row: rowQueued, channel: chanNone, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.endTurn()
			if jobs := s.spy.takeJobs(); len(jobs) != 1 || !slices.Equal(rowKeys(s, jobs[0].Owner), []string{"steer-a"}) {
				s.fatalf("jobs = %+v, want steer-a collected", jobs)
			}
		}},
		{name: "E10b collects and owes the post-load clear", key: "steer-a", row: rowQueued, channel: chanNone, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.TurnEnded(s.chat, command.SteerTurnEnd{TurnID: s.turnID(), BridgeDeath: true, Source: marotte.TurnSourcePrompt})
			if !s.recs.NeedsPostLoadClear(s.chat) {
				s.fatalf("a dead buffer's row does not owe the post-load clear")
			}
		}},
		{name: "E11 OPEN clears and confirms nothing", key: "steer-a", row: rowCleared, channel: chanUnconfirmed, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.SteerCleared(s.chat, []string{"steer-a"})
		}},
		{name: "E11 PROBING owes the waiters a probe", key: "steer-c", row: rowWaiting, channel: chanProbing, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerCleared(s.chat, []string{res.Resend.ID})
			if s.probe() != "" {
				s.fatalf("probe = %q, want owed", s.probe())
			}
		}},
		{name: "E11 NONE with nothing held unsends", key: "steer-a", row: rowUnsent, channel: chanNone, run: func(s *steerHarness) {
			s.steer("steer-a", "a")
			s.recs.SteerCleared(s.chat, []string{"steer-a"})
		}},
		{name: "E14 drops every row", key: "steer-a", row: rowDone, channel: chanNone, run: func(s *steerHarness) {
			s.bind()
			s.steer("steer-a", "a")
			s.recs.BeginTeardown(s.chat, nil)
		}},
		{name: "E15 OPEN clears the refused row", key: "steer-a", row: rowCleared, channel: chanUnconfirmed, run: func(s *steerHarness) {
			s.bind()
			sends, _ := s.q.RouteSteer(s.chat, "steer-a", "a", promptHolder)
			s.q.SteerSent(s.chat, sends[0], false, nil)
		}},
		{name: "E15 PROBING clears the probe and its waiters", key: "steer-c", row: rowCleared, channel: chanUnconfirmed, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.q.SteerSent(s.chat, *res.Resend, false, nil)
		}},
		{name: "E16 leaves the row where KAS may hold it", key: "steer-a", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			s.bind()
			sends, _ := s.q.RouteSteer(s.chat, "steer-a", "a", promptHolder)
			s.q.SteerSent(s.chat, sends[0], false, errors.New("pipe closed"))
		}},
		{name: "E17 parks a row sent during the wait", key: "steer-b", row: rowParked, channel: chanNone, run: func(s *steerHarness) {
			s.steer("steer-a", "a")
			s.recs.BridgeGone(s.chat)
			s.steer("steer-b", "b")
			s.q.PostLoadCleared(s.chat, []string{"steer-a", "steer-b"}, true)
		}},
		{name: "E18 OPEN flushes a waiter as itself", key: "steer-c", row: rowQueued, channel: chanOpen, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerRead(s.chat, res.Resend.ID)
			s.kas(s.q.PlanFlush(s.chat))
		}},
		{name: "E18 PROBING sends the owed probe", key: "steer-c", row: rowOutstanding, channel: chanProbing, run: func(s *steerHarness) {
			res := probeSent(s)
			s.q.RouteSteer(s.chat, "steer-c", "c", promptHolder)
			s.recs.SteerForgotten(s.chat, []string{res.Resend.ID})
			s.kas(s.q.PlanFlush(s.chat))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSteerHarness(t)
			tc.run(s)

			got, live := s.state(tc.key)
			switch {
			case tc.row == rowDone && live:
				t.Errorf("row %s = %v, want it gone", tc.key, got)
			case tc.row != rowDone && (!live || got != tc.row):
				t.Errorf("row %s = %v (live %v), want %v", tc.key, got, live, tc.row)
			}
			if s.channel() != tc.channel {
				t.Errorf("channel = %d, want %d", s.channel(), tc.channel)
			}
		})
	}
}

func rowKeys(s *steerHarness, owner string) []string {
	rows, _ := s.q.JobRows(s.chat, owner, "")
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	return keys
}

// --- the probe after a clear ---

// After a clear KAS's cursor sits past every id it ever held, so the first steer
// is ONE probe whose read is observed; the steers behind it wait for that read and
// then go out as themselves.
func TestSteerRecords_AfterAClearTheFirstSendIsOneProbe(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	res, needsClear, refuse := s.remove("steer-a")
	if refuse != "" || !needsClear {
		t.Fatalf("remove = %q %v, want a clear", refuse, needsClear)
	}
	if res.Resend == nil || !slices.Equal(res.Resend.Keys, []string{"steer-b"}) {
		t.Fatalf("resend = %+v, want steer-b alone as the probe", res.Resend)
	}
	s.resubmit("steer-a", res)
	probe := res.Resend.ID
	if s.probe() != probe || s.channel() != chanProbing {
		t.Fatalf("channel = %v probe %q, want PROBING on %q", s.channel(), s.probe(), probe)
	}

	sends, _ := s.q.RouteSteer(s.chat, "steer-c", "third", promptHolder)
	if len(sends) != 0 {
		t.Errorf("a steer behind an outstanding probe went out: %+v", sends)
	}
	s.mustState("steer-c", rowWaiting)

	s.recs.SteerRead(s.chat, probe)
	if s.channel() != chanOpen {
		t.Errorf("channel after the probe's read = %v, want OPEN", s.channel())
	}
	jobs := s.spy.takeJobs()
	if len(jobs) != 1 || jobs[0].End != nil {
		t.Fatalf("jobs after the read = %+v, want one flush", jobs)
	}
	flush := s.q.PlanFlush(s.chat)
	if len(flush) != 1 || flush[0].ID != "steer-c" {
		t.Errorf("flush = %+v, want steer-c as itself", flush)
	}
}

// A probe never read before its turn ended leaves its rows for the turn end, and a
// read that arrives after that is a fact the turn end's job reads, not a reason to
// reopen a channel that no longer exists.
func TestSteerRecords_ALateProbeReadInNoneIsAFactOnly(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	res, _, _ := s.remove("steer-a")
	s.resubmit("steer-a", res)
	s.endTurn()
	if s.channel() != chanNone {
		t.Fatalf("channel after the turn end = %v, want NONE", s.channel())
	}

	s.recs.SteerRead(s.chat, res.Resend.ID)

	if s.channel() != chanNone || s.turnID() != "" {
		t.Errorf("channel = %v turn %q, want NONE with no turn", s.channel(), s.turnID())
	}
	jobs := s.spy.takeJobs()
	if len(jobs) != 1 || jobs[0].End == nil {
		t.Fatalf("jobs = %+v, want the turn end's", jobs)
	}
	if rows, _ := s.q.JobRows(s.chat, jobs[0].Owner, ""); len(rows) != 0 {
		t.Errorf("job rows after the late read = %+v, want none: the agent read them", rows)
	}
}

// A clear somebody else issued (KAS's own boundary, another client) moves the rows
// it named to cleared, and a kept row in that state still joins the probe.
func TestSteerRecords_AKeptRowARacingClearMovedJoinsTheProbe(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	op := "op-steer-a"
	if needsClear, refuse := s.q.BeginRemove(s.chat, "steer-a", op); !needsClear || refuse != "" {
		t.Fatalf("BeginRemove = %v %q", needsClear, refuse)
	}
	s.recs.SteerCleared(s.chat, []string{"steer-b"})
	s.mustState("steer-b", rowCleared)

	res := s.q.RemoveCleared(s.chat, op, s.clearKAS(), true)

	if res.Resend == nil || !slices.Equal(res.Resend.Keys, []string{"steer-b"}) {
		t.Errorf("resend = %+v, want the cleared steer-b in the probe", res.Resend)
	}
}

// --- deletes ---

// The target the agent read before the clear landed was delivered, so the delete
// answers consumed and writes no deleted entry; the rows the reader kept still go.
func TestSteerRecords_ATargetReadBeforeTheClearIsConsumed(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	op := "op-steer-a"
	s.q.BeginRemove(s.chat, "steer-a", op)
	s.recs.SteerRead(s.chat, s.kasID("steer-a"))

	res := s.q.RemoveCleared(s.chat, op, s.clearKAS(), true)

	if res.Reason != command.SteerRefuseConsumed {
		t.Errorf("reason = %q, want consumed", res.Reason)
	}
	if n := s.spy.note("steer-a"); n != nil {
		t.Errorf("the read target got entry %+v, want none from the record", n)
	}
	if res.Resend == nil || !slices.Equal(res.Resend.Keys, []string{"steer-b"}) {
		t.Errorf("resend = %+v, want steer-b", res.Resend)
	}
}

// The deleted row's entry says the reader deleted it.
func TestSteerRecords_TheDeletedRowIsNotedDeleted(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	res, _, _ := s.remove("steer-a")
	s.resubmit("steer-a", res)

	n := s.spy.note("steer-a")
	if n == nil || n.State != marotte.SteerStateDropped || n.Reason != marotte.SteerReasonDeleted || n.Text != "first" {
		t.Errorf("entry = %+v, want dropped/deleted carrying the text", n)
	}
	if _, live := s.state("steer-a"); live {
		t.Error("the deleted row is still in the dock")
	}
	if s.channel() != chanUnconfirmed {
		t.Errorf("channel = %v, want UNCONFIRMED: nothing is outstanding after the clear", s.channel())
	}
}

// A row KAS does not hold goes at once with no clear: parked, cleared, unsent.
func TestSteerRecords_ARowKASDoesNotHoldIsDeletedWithoutAClear(t *testing.T) {
	s := newSteerHarness(t)
	s.q.RouteSteer(s.chat, "steer-a", "first", command.SteerHolder{Held: true, PromptClass: true})
	s.mustState("steer-a", rowParked)

	_, needsClear, refuse := s.remove("steer-a")

	if needsClear || refuse != "" {
		t.Errorf("remove = %v %q, want an immediate delete", needsClear, refuse)
	}
	if n := s.spy.note("steer-a"); n == nil || n.Reason != marotte.SteerReasonDeleted {
		t.Errorf("entry = %+v, want deleted", n)
	}
}

// A delete before the turn that will read the rows has bound would clear what that
// turn's cursor is about to read, so it waits; with no started turn it goes ahead.
func TestSteerRecords_ADeleteInNoneIsRefusedOnlyWhileATurnIsStarting(t *testing.T) {
	s := newSteerHarness(t)
	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{Source: marotte.TurnSourcePrompt})
	sends, _ := s.q.RouteSteer(s.chat, "steer-a", "first", promptHolder)
	s.kas(sends)
	s.mustState("steer-a", rowQueued)

	if _, refuse := s.q.BeginRemove(s.chat, "steer-a", "op-1"); refuse != "" {
		t.Fatalf("with no started turn BeginRemove refused %q", refuse)
	}
	s.q.EndOp(s.chat, "op-1")

	s.h.stagePromptTurn(t, s.chat)
	if _, refuse := s.q.BeginRemove(s.chat, "steer-a", "op-2"); refuse != command.SteerRefuseStarting {
		t.Errorf("with a started, unbound turn BeginRemove = %q, want starting", refuse)
	}
}

// A row a turn-end job holds is the delete's when KAS cannot hold it, and refused
// while it may, because the job is about to decide whether KAS still has it.
func TestSteerRecords_AJobsRowIsTakenOverOrRefusedSettling(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "in kas")
	s.endTurn()
	s.spy.takeJobs()

	if _, refuse := s.q.BeginRemove(s.chat, "steer-a", "op-1"); refuse != command.SteerRefuseSettling {
		t.Fatalf("delete of a job's row KAS may hold = %q, want settling", refuse)
	}
	s.recs.SteerCleared(s.chat, []string{s.kasID("steer-a")})
	s.mustState("steer-a", rowCleared)
	if needsClear, refuse := s.q.BeginRemove(s.chat, "steer-a", "op-2"); needsClear || refuse != "" {
		t.Errorf("delete of a job's row KAS cleared = %v %q, want taken over at once", needsClear, refuse)
	}
}

// --- turn ends, bridge death, lead ---

// The turn closing under a delete hands the op the end, with the lead the cancel
// recorded, so the rows it kept go back by the turn end's routine in that order.
func TestSteerRecords_ATurnEndingUnderTheOpRecordsTheEndWithItsLead(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	op := "op-steer-a"
	s.q.BeginRemove(s.chat, "steer-a", op)
	undo := s.q.SetSteerLead(s.chat, "steer-b")
	defer undo()
	s.endTurn()

	res := s.q.RemoveCleared(s.chat, op, s.clearKAS(), true)
	if res.Resend != nil {
		t.Errorf("resend = %+v, want none into a closed turn", res.Resend)
	}
	end := s.q.EndOp(s.chat, op)

	if end == nil || end.Lead != "steer-b" {
		t.Fatalf("end = %+v, want the turn end carrying lead steer-b", end)
	}
	if jobs := s.spy.takeJobs(); len(jobs) != 0 {
		t.Errorf("the turn end also queued a job for the op's rows: %+v", jobs)
	}
	rows, _ := s.q.JobRows(s.chat, op, end.Lead)
	if len(rows) != 1 || rows[0].Key != "steer-b" {
		t.Errorf("op rows = %+v, want steer-b", rows)
	}
}

// A turn ending after the resubmit's reply and before the op lets go is still the
// op's to resolve: EndOp answers that end with the kept row still the op's, never a
// row left unowned with no channel and no job.
func TestSteerRecords_ATurnEndingAfterTheResubmitIsAnsweredByEndOp(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	const op = "op-steer-a"
	if needs, refuse := s.q.BeginRemove(s.chat, "steer-a", op); !needs || refuse != "" {
		t.Fatalf("BeginRemove = %v %q, want a clear", needs, refuse)
	}
	res := s.q.RemoveCleared(s.chat, op, s.clearKAS(), true)
	if res.Resend == nil {
		t.Fatal("RemoveCleared resubmitted nothing")
	}
	s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: res.Resend.ID, Text: res.Resend.Text})
	s.q.OpSent(s.chat, op, *res.Resend, true, nil)
	s.endTurn()

	end := s.q.EndOp(s.chat, op)

	if end == nil {
		t.Fatal("EndOp answered no turn end for a turn that closed under the op")
	}
	if jobs := s.spy.takeJobs(); len(jobs) != 0 {
		t.Errorf("jobs = %+v, want the op alone to resolve its rows", jobs)
	}
	rows, gone := s.q.JobRows(s.chat, op, end.Lead)
	if gone || len(rows) != 1 || rows[0].Key != "steer-b" || !rows[0].InKAS {
		t.Errorf("op rows = %+v (gone %v), want steer-b still in KAS for the turn end", rows, gone)
	}
}

// A turn ending under a delete hands every row it leaves to the op, the parked ones
// the op never marked included, so one boundary goes back as one send.
func TestSteerRecords_ATurnEndingUnderADeleteHandsItTheParkedRowsToo(t *testing.T) {
	s := newSteerHarness(t)
	s.steer("steer-a", "a")
	s.steer("steer-b", "b")
	if _, refuse := s.q.RouteSteer(s.chat, "steer-c", "c",
		command.SteerHolder{Held: true, PromptClass: true}); refuse != "" {
		t.Fatalf("RouteSteer(steer-c) refused %q", refuse)
	}
	s.mustState("steer-c", rowParked)
	const op = "op-steer-a"
	if needs, refuse := s.q.BeginRemove(s.chat, "steer-a", op); !needs || refuse != "" {
		t.Fatalf("BeginRemove = %v %q, want a clear", needs, refuse)
	}
	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{Source: marotte.TurnSourcePrompt})

	if jobs := s.spy.takeJobs(); len(jobs) != 0 {
		t.Errorf("jobs = %+v, want the op alone to resolve the turn's rows", jobs)
	}
	s.q.RemoveCleared(s.chat, op, s.clearKAS(), true)
	end := s.q.EndOp(s.chat, op)
	if end == nil {
		t.Fatal("EndOp answered no turn end for a turn that closed under the op")
	}
	rows, _ := s.q.JobRows(s.chat, op, end.Lead)
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if want := []string{"steer-b", "steer-c"}; !slices.Equal(keys, want) {
		t.Errorf("op rows = %v, want %v", keys, want)
	}
}

// A discard takes no row a turn end leaves: a row sent after Discard all began is the
// turn end's own job, and the discard does not drop it.
func TestSteerRecords_ATurnEndingUnderADiscardLeavesALaterRowToItsJob(t *testing.T) {
	s := newSteerHarness(t)
	s.steer("steer-a", "a")
	const op = "op-discard"
	if needs, refuse := s.q.BeginDiscard(s.chat, op); !needs || refuse != "" {
		t.Fatalf("BeginDiscard = %v %q, want a clear", needs, refuse)
	}
	if _, refuse := s.q.RouteSteer(s.chat, "steer-b", "b",
		command.SteerHolder{Held: true, PromptClass: true}); refuse != "" {
		t.Fatalf("RouteSteer(steer-b) refused %q", refuse)
	}
	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{Source: marotte.TurnSourcePrompt})
	s.q.DiscardCleared(s.chat, op, true)
	s.q.EndOp(s.chat, op)

	jobs := s.spy.takeJobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want the turn end's own job for steer-b", jobs)
	}
	if rows, _ := s.q.JobRows(s.chat, jobs[0].Owner, ""); len(rows) != 1 || rows[0].Key != "steer-b" {
		t.Errorf("job rows = %+v, want steer-b", rows)
	}
}

// A dead bridge's buffer may come back in KAS's log on the next load, so rows it
// held are marked for the post-load clear; the turn end's job carries the death.
func TestSteerRecords_ABridgeDeathCollectsTheRowsAndArmsThePostLoadClear(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")

	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{TurnID: s.turnID(), BridgeDeath: true, Source: marotte.TurnSourcePrompt})

	jobs := s.spy.takeJobs()
	if len(jobs) != 1 || jobs[0].End == nil || !jobs[0].End.BridgeDeath {
		t.Fatalf("jobs = %+v, want the death's turn end", jobs)
	}
	if !s.recs.NeedsPostLoadClear(s.chat) {
		t.Error("a row the dead buffer held does not arm the post-load clear")
	}
	rows, _ := s.q.JobRows(s.chat, jobs[0].Owner, "")
	if len(rows) != 1 || !rows[0].InKAS {
		t.Errorf("job rows = %+v, want steer-a still marked in KAS", rows)
	}
}

// A bridge that dies with no turn bound leaves the rows its buffer held unsent for
// the next prompt, and owes the post-load clear.
func TestSteerRecords_ABridgeGoneWithNoTurnLeavesTheRowsUnsent(t *testing.T) {
	s := newSteerHarness(t)
	s.recs.TurnEnded(s.chat, command.SteerTurnEnd{Source: marotte.TurnSourcePrompt})
	sends, _ := s.q.RouteSteer(s.chat, "steer-a", "first", promptHolder)
	s.kas(sends)

	s.recs.BridgeGone(s.chat)

	s.mustState("steer-a", rowUnsent)
	if f := s.spy.framesFor("steer-a"); len(f) == 0 || f[len(f)-1].State != marotte.SteerRowUnsent {
		t.Errorf("frames = %+v, want an unsent frame last", f)
	}
	if !s.recs.NeedsPostLoadClear(s.chat) {
		t.Error("the dead buffer's row does not arm the post-load clear")
	}
}

// The turn end's resend notes each row's boundary entry and names the keys in
// order, and keeps the rows until their prompt has opened: only then do they leave.
func TestSteerRecords_ResentNotesEachRowAndKeepsItUntilDelivered(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	s.endTurn()
	jobs := s.spy.takeJobs()
	s.q.StraysCleared(s.chat, jobs[0].Owner, nil, true)

	keys, text := s.q.Resent(s.chat, jobs[0].Owner, "steer-b")

	if !slices.Equal(keys, []string{"steer-b", "steer-a"}) || text != "second\n\nfirst" {
		t.Errorf("resent = %v %q, want the lead first, then arrival order", keys, text)
	}
	for _, k := range keys {
		if n := s.spy.note(k); n == nil || n.Reason != marotte.SteerReasonBoundary {
			t.Errorf("entry %s = %+v, want dropped/boundary", k, n)
		}
		if _, live := s.state(k); !live {
			t.Errorf("row %s left before its prompt opened", k)
		}
	}

	s.q.Delivered(s.chat, jobs[0].Owner)

	for _, k := range keys {
		if _, live := s.state(k); live {
			t.Errorf("row %s is still held after its prompt opened", k)
		}
	}
}

// A resend whose prompt could not open keeps the rows, unsent, with the entries they
// already carry, so the next send of one goes under a fresh id.
func TestSteerRecords_AResendThatCannotOpenLeavesTheRowsUnsent(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.endTurn()
	jobs := s.spy.takeJobs()
	s.q.StraysCleared(s.chat, jobs[0].Owner, nil, true)
	s.q.Resent(s.chat, jobs[0].Owner, "")

	s.q.Unsent(s.chat, jobs[0].Owner)

	s.mustState("steer-a", rowUnsent)
	key, _, ok := s.q.NextParked(s.chat)
	if !ok || key != "steer-a" {
		t.Fatalf("NextParked = %q %v, want steer-a for the next prompt", key, ok)
	}
	sends, _ := s.q.RouteSteer(s.chat, "steer-a", "first", command.SteerHolder{Held: true, PromptClass: true, Live: true, Delivering: true})
	if len(sends) != 1 || sends[0].ID == "steer-a" || !slices.Equal(sends[0].Keys, []string{"steer-a"}) {
		t.Errorf("sends = %+v, want one under a fresh id carrying steer-a", sends)
	}
}

// A refused re-route (nothing holds the chat) leaves the row unsent, and the next
// prompt's drain parks every unsent row so that prompt carries it.
func TestSteerRecords_UnsentRowsAreParkedForTheNextPrompt(t *testing.T) {
	s := newSteerHarness(t)
	s.q.RouteSteer(s.chat, "steer-a", "first", command.SteerHolder{Held: true, PromptClass: true})
	if _, refuse := s.q.RouteSteer(s.chat, "steer-a", "first", command.SteerHolder{}); refuse != command.SteerRefuseNoTurn {
		t.Fatalf("re-route with no holder = %q, want no_turn", refuse)
	}
	s.mustState("steer-a", rowUnsent)

	key, text, ok := s.q.NextParked(s.chat)

	if !ok || key != "steer-a" || text != "first" {
		t.Errorf("NextParked = %q %q %v, want steer-a", key, text, ok)
	}
	s.mustState("steer-a", rowParked)
}

// A steer sent while the prompt waited for MCP is in KAS when the post-load clear
// lands; the clear's ids put it back to parked so the drain resends it in order.
func TestSteerRecords_ThePostLoadClearParksARowSentDuringTheWait(t *testing.T) {
	s := newSteerHarness(t)
	s.steer("steer-a", "first")
	s.recs.BridgeGone(s.chat)
	if !s.recs.NeedsPostLoadClear(s.chat) {
		t.Fatal("a dead buffer's row does not arm the post-load clear")
	}
	sends, _ := s.q.RouteSteer(s.chat, "steer-b", "second", promptHolder)
	s.kas(sends)

	s.q.PostLoadCleared(s.chat, []string{s.kasID("steer-b")}, true)

	s.mustState("steer-b", rowParked)
	if s.recs.NeedsPostLoadClear(s.chat) {
		t.Error("the post-load clear is still owed after it landed")
	}
}

// --- discard, teardown, reload ---

// Discard all: every row goes deleted, except one the agent read before the clear,
// which keeps its read and gets no second entry.
func TestSteerRecords_DiscardLeavesARowReadBeforeTheClearRead(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	needsClear, _ := s.q.BeginDiscard(s.chat, "op-d")
	if !needsClear {
		t.Fatal("discard of rows KAS holds needs no clear")
	}
	s.recs.SteerRead(s.chat, s.kasID("steer-a"))
	s.clearKAS()

	s.q.DiscardCleared(s.chat, "op-d", true)

	if n := s.spy.note("steer-a"); n != nil {
		t.Errorf("the read row got entry %+v, want none", n)
	}
	if n := s.spy.note("steer-b"); n == nil || n.Reason != marotte.SteerReasonDeleted {
		t.Errorf("steer-b entry = %+v, want deleted", n)
	}
}

// The teardown marks the record gone before its cancel: the turn end that cancel
// causes owes no job, and the rows nothing read come back for the close's notes.
func TestSteerRecords_ATornDownChatOwesNothing(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	exit := make(chan struct{})

	unread := s.recs.BeginTeardown(s.chat, exit)
	s.endTurn()

	if len(unread) != 1 || unread[0].SteerID != "steer-a" {
		t.Errorf("unread = %+v, want steer-a", unread)
	}
	if jobs := s.spy.takeJobs(); len(jobs) != 0 {
		t.Errorf("a torn-down chat queued %+v", jobs)
	}
	done := make(chan struct{})
	go func() { s.recs.EndTeardown(s.chat); close(done) }()
	select {
	case <-done:
		t.Fatal("EndTeardown returned before the forward goroutine drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(exit)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EndTeardown never returned after the drain")
	}
	if s.recs.gone(s.chat) {
		t.Error("the record is still marked gone after EndTeardown, so a reopened chat starts dead")
	}
}

// A steer routed after the teardown began is refused: no row.
func TestSteerRecords_ATornDownChatTakesNoNewRow(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.recs.BeginTeardown(s.chat, nil)

	sends, refuse := s.q.RouteSteer(s.chat, "steer-a", "late", promptHolder)

	if refuse != command.SteerRefuseNoTurn || len(sends) != 0 {
		t.Errorf("RouteSteer after teardown = %+v %q, want refused no_turn, nothing sent", sends, refuse)
	}
	if _, live := s.state("steer-a"); live {
		t.Error("the torn-down chat took a new row")
	}
}

// The teardown waits behind a publication already under way, and that step's frame,
// owed to a chat now gone, is not broadcast after the teardown began.
func TestSteerRecords_BeginTeardownWaitsForAnActivePublication(t *testing.T) {
	s := newSteerHarness(t)
	var mu sync.Mutex
	var order []string
	log := func(ev string) {
		mu.Lock()
		order = append(order, ev)
		mu.Unlock()
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.recs.note = func(_ context.Context, _ marotte.ChatID, id string, _ *marotte.EntrySteer) {
		close(entered)
		<-release
		log("note " + id)
	}
	s.recs.broadcast = func(e marotte.ServerEvent) {
		log("frame " + e.Payload.(marotte.SteerQueuedPayload).SteerID)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		s.recs.step(s.chat, true, func(_ *steerRecord, fx *steerFx) {
			fx.notes = append(fx.notes, steerNote{id: "steer-a", steer: &marotte.EntrySteer{}})
			fx.frames = append(fx.frames, marotte.SteerQueuedPayload{SteerID: "steer-b"})
		})
	})
	<-entered
	returned := make(chan struct{})
	wg.Go(func() {
		s.recs.BeginTeardown(s.chat, nil)
		log("teardown returned")
		close(returned)
	})
	select {
	case <-returned:
		t.Fatal("BeginTeardown returned while an earlier step's note was still being written")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	wg.Wait()

	if want := []string{"note steer-a", "teardown returned"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v: the gone chat's frame must not be broadcast", order, want)
	}
}

// A tab close leaves each unread row its boundary note; a delete writes none.
func TestBeginChatTeardown_ACloseNotesEveryUnreadRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep bool
	}{{"close", true}, {"delete", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h, cs, _ := newTestHub()
			seedChat(t, cs, "c1")
			s := harnessOn(h, "c1", t.Fatalf)
			s.recs.note = func(ctx context.Context, chatID marotte.ChatID, id string, steer *marotte.EntrySteer) {
				h.coord.recordSteer(ctx, chatID, id, steer)
			}
			s.bind()
			s.steer("steer-a", "first")

			h.BeginChatTeardown("c1", tc.keep)

			var got []marotte.EntrySteer
			for _, e := range logOf(t, cs, "c1") {
				if e.Kind == marotte.EntryKindSteer && e.ID == "steer-a" {
					var steer marotte.EntrySteer
					decodePayload(t, e, &steer)
					got = append(got, steer)
				}
			}
			if tc.keep && (len(got) != 1 || got[0].Reason != marotte.SteerReasonBoundary || got[0].Text != "first") {
				t.Errorf("close notes = %+v, want one boundary note carrying the text", got)
			}
			if !tc.keep && len(got) != 0 {
				t.Errorf("delete notes = %+v, want none", got)
			}
		})
	}
}

// The connect replay lists every held row with its wire state, then a batch frame
// for every id a row is held under that is not its key, so a reconnecting dock
// rebuilds its batch map.
func TestSteerRecords_TheReplayListsEachRowWithItsBatch(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	res, _, _ := s.remove("steer-a")
	if res.Resend == nil {
		t.Fatal("the delete resubmitted nothing")
	}

	var rows, batches []marotte.SteerQueuedPayload
	for _, e := range s.recs.List(s.chat) {
		p := e.Payload.(marotte.SteerQueuedPayload)
		if len(p.Replaces) > 0 {
			batches = append(batches, p)
			continue
		}
		rows = append(rows, p)
	}
	if len(rows) != 1 || rows[0].SteerID != "steer-b" || rows[0].State != marotte.SteerRowQueued {
		t.Errorf("rows = %+v, want steer-b queued", rows)
	}
	if len(batches) != 1 || batches[0].SteerID != res.Resend.ID || !slices.Equal(batches[0].Replaces, []string{"steer-b"}) {
		t.Errorf("batches = %+v, want %s replacing steer-b", batches, res.Resend.ID)
	}
}

// While KAS may still hold copies of a dead buffer's rows, the next prompt's drain does not
// send them: a clear that never answered and a clear an agent row blocked both
// leave them unsent, and a clear that lands releases them.
func TestSteerRecords_AnUnsettledPostLoadClearHoldsTheReloadedRows(t *testing.T) {
	for _, tc := range []struct {
		settle   func(s *steerHarness)
		name     string
		wantSent bool
	}{
		{name: "the clear had no reply", settle: func(s *steerHarness) {
			s.q.PostLoadCleared(s.chat, nil, false)
		}},
		{name: "an agent row blocked the clear", settle: func(s *steerHarness) {
			s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: "notify-1", Origin: marotte.SteerOriginAgent})
			if s.recs.NeedsPostLoadClear(s.chat) {
				s.fatalf("a clear that would drop an agent row is still owed")
			}
		}},
		{name: "the clear landed", wantSent: true, settle: func(s *steerHarness) {
			s.q.PostLoadCleared(s.chat, nil, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSteerHarness(t)
			s.steer("steer-a", "first")
			s.recs.BridgeGone(s.chat)
			tc.settle(s)

			_, _, ok := s.q.NextParked(s.chat)

			if ok != tc.wantSent {
				t.Errorf("NextParked answered a row = %v, want %v", ok, tc.wantSent)
			}
			if !tc.wantSent {
				s.mustState("steer-a", rowUnsent)
			}
		})
	}
}

// --- frames ---

// Steps publish in the order they took the lock: a later step's frame waits behind
// an earlier step's note still being written, so a client never meets them reversed.
func TestSteerRecords_StepsPublishInTheOrderTheyRan(t *testing.T) {
	s := newSteerHarness(t)
	parked := command.SteerHolder{Held: true, PromptClass: true}
	s.q.RouteSteer(s.chat, "steer-a", "a", parked)
	var mu sync.Mutex
	var order []string
	entered, release := make(chan struct{}), make(chan struct{})
	s.recs.note = func(_ context.Context, _ marotte.ChatID, id string, _ *marotte.EntrySteer) {
		close(entered)
		<-release
		mu.Lock()
		order = append(order, "note "+id)
		mu.Unlock()
	}
	s.recs.broadcast = func(e marotte.ServerEvent) {
		mu.Lock()
		order = append(order, "frame "+e.Payload.(marotte.SteerQueuedPayload).SteerID)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Go(func() { s.q.BeginRemove(s.chat, "steer-a", "op-a") })
	<-entered
	wg.Go(func() { s.q.RouteSteer(s.chat, "steer-b", "b", parked) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := s.state("steer-b"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second step never ran")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	early := slices.Clone(order)
	mu.Unlock()
	close(release)
	wg.Wait()

	if len(early) != 0 {
		t.Errorf("published %v while the first step's note was still being written", early)
	}
	if want := []string{"note steer-a", "frame steer-b"}; !slices.Equal(order, want) {
		t.Errorf("publication order = %v, want %v", order, want)
	}
}

// A clear answers the agent rows alone: a user row's entry is written at its own
// terminal transition, so a row resent after the clear is never noted unread.
func TestSteerRecords_AClearAnswersOnlyAgentRows(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: "notify-1", Text: "a run finished", Origin: marotte.SteerOriginAgent})

	got := s.recs.SteerCleared(s.chat, []string{s.kasID("steer-a"), "notify-1"})

	if len(got) != 1 || got[0].SteerID != "notify-1" {
		t.Errorf("cleared = %+v, want the agent row alone", got)
	}
	s.mustState("steer-a", rowCleared)
}

// A user row sent under its key comes back as its own row frame, and a combined
// send comes back as one batch frame naming the rows it carries.
func TestSteerRecords_AQueuedFrameIsTheRowsOwnOrABatch(t *testing.T) {
	s := newSteerHarness(t)
	s.bind()
	s.steer("steer-a", "first")
	s.steer("steer-b", "second")
	s.steer("steer-c", "third")
	res, _, _ := s.remove("steer-a")

	single, ok := s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: "steer-x", Text: "x"})
	if _, own := s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: res.Resend.ID, Text: res.Resend.Text}); own {
		t.Error("a frame naming the record's rows was handed back for the caller to broadcast")
	}

	if !ok || single.SteerID != "steer-x" || len(single.Replaces) != 0 {
		t.Errorf("unknown id frame = %+v, %v, want itself for broadcast", single, ok)
	}
	batch := s.spy.framesFor(res.Resend.ID)
	if len(batch) != 1 || !slices.Equal(batch[0].Replaces, []string{"steer-b", "steer-c"}) || batch[0].Text != "second\n\nthird" {
		t.Errorf("published probe frames = %+v, want one batch of steer-b and steer-c", batch)
	}
}

// A chat whose teardown began answers no frame.
func TestSteerRecords_AGoneChatAnswersNoFrame(t *testing.T) {
	s := newSteerHarness(t)
	s.recs.BeginTeardown(s.chat, nil)

	if _, ok := s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: "notify-1"}); ok {
		t.Error("a torn-down chat answered a frame")
	}
}

// An agent row waits until read, and a 65th evicts the oldest rather than growing.
func TestSteerRecords_AgentRowsWaitAndAreBounded(t *testing.T) {
	s := newSteerHarness(t)
	for i := range maxSteerRows + 1 {
		s.recs.SteerWaiting(s.chat, &marotte.SteerQueuedPayload{SteerID: "notify-" + strings.Repeat("x", i+1), Origin: marotte.SteerOriginAgent})
	}
	if n := len(s.recs.List(s.chat)); n != maxSteerRows {
		t.Errorf("listed %d agent rows, want %d", n, maxSteerRows)
	}
	s.recs.SteerRead(s.chat, "notify-"+strings.Repeat("x", maxSteerRows+1))
	if n := len(s.recs.List(s.chat)); n != maxSteerRows-1 {
		t.Errorf("listed %d after a read, want %d", n, maxSteerRows-1)
	}
	if n := len(s.recs.List("other")); n != 0 {
		t.Errorf("another chat lists %d rows", n)
	}
}

// --- the connect replay, end to end over the SSE handler ---

func TestHandleSSE_ReplaysTheSteerRows(t *testing.T) {
	h, _, _ := newTestHub()
	s := harnessOn(h, "c1", t.Fatalf)
	s.bind()
	s.steer("steer-1", "actually use tabs")

	ctx := hookOnlyContext(t)
	rec := httptest.NewRecorder()
	h.handleSSE(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx))

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"steer_queued"`) || !strings.Contains(body, "actually use tabs") {
		t.Errorf("the connect replay does not carry the row: %q", body)
	}
}

// The chat teardown forgets the record, and a reopened chat starts a fresh one.
func TestCleanupChatState_ForgetsTheSteerRecord(t *testing.T) {
	h, _, _ := newTestHub()
	s := harnessOn(h, "c1", t.Fatalf)
	s.bind()
	s.steer("steer-1", "one")
	other := harnessOn(h, "c2", t.Fatalf)
	other.bind()
	other.steer("steer-2", "two")

	h.cleanupChatState(t.Context(), "c1", false)

	if n := len(h.bus.steers.List("c1")); n != 0 {
		t.Errorf("c1 still lists %d rows", n)
	}
	if n := len(h.bus.steers.List("c2")); n != 1 {
		t.Errorf("c2 lists %d rows, want its own", n)
	}
	if _, refuse := h.steerQueue.RouteSteer("c1", "steer-3", "three", promptHolder); refuse != "" {
		t.Errorf("a reopened chat refused a steer: %q", refuse)
	}
}

func TestChatLocks_SerializeOneChatAndNotOthers(t *testing.T) {
	locks := newChatLocks()
	unlock, err := locks.lock(t.Context(), "c1")
	if err != nil {
		t.Fatalf("lock c1: %v", err)
	}
	other, err := locks.lock(t.Context(), "c2")
	if err != nil {
		t.Fatalf("lock c2 while c1 is held: %v", err)
	}
	other()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := locks.lock(ctx, "c1"); err == nil {
		t.Fatal("second lock of c1 succeeded while the first was held")
	}
	acquired := make(chan func())
	go func() {
		u, err := locks.lock(t.Context(), "c1")
		if err != nil {
			t.Errorf("waiting lock: %v", err)
		}
		acquired <- u
	}()
	unlock()
	select {
	case u := <-acquired:
		u()
	case <-time.After(5 * time.Second):
		t.Fatal("a waiter was not woken by the unlock")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.m) != 0 {
		t.Errorf("map holds %d chats after every holder left, want 0", len(locks.m))
	}
}

func equalStrings(a, b []string) bool { return slices.Equal(a, b) }
