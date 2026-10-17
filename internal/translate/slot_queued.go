package translate

import (
	"context"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// infoKindQueued is KAS's report that the chat's prompt waits for its one autonomous-agent slot, which another
// session holds (AgentExecutionWaitingForActiveSlot, and getActiveSessionBlockingQueued on session/load). The slot
// is per engine, so any two marotte chats running agent turns take turns.
const infoKindQueued = "queued"

// slotWait is the wait a chat shows: the turn waiting and the session it waits for.
type slotWait struct {
	turn   string
	active string
}

// slotWaits holds each chat's shown wait, so a repeated frame tells the reader once per turn and blocker.
type slotWaits struct {
	by map[marotte.ChatID]slotWait
	mu sync.Mutex
}

// set records w as chatID's wait and reports whether it changed.
func (s *slotWaits) set(chatID marotte.ChatID, w slotWait) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = make(map[marotte.ChatID]slotWait)
	}
	if prev, ok := s.by[chatID]; ok && prev == w {
		return false
	}
	s.by[chatID] = w
	return true
}

func (s *slotWaits) clear(chatID marotte.ChatID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.by[chatID]
	delete(s.by, chatID)
	return ok
}

// handleSlotQueued tells the reader why the chat's turn shows no progress: a working label on the live turn and
// one notice naming the chat that holds the slot.
func (t *Translator) handleSlotQueued(ctx context.Context, chatID marotte.ChatID, active string) {
	w := slotWait{active: active}
	if turn, ok := t.turns.PromptTurn(chatID); ok {
		w.turn = turn.ID()
	} else if turn, ok := t.turns.OwnTurn(chatID); ok {
		w.turn = turn.ID()
	}
	if !t.slotWaits.set(chatID, w) {
		return
	}
	holder := "another kiro-cli session"
	if c, ok := t.sessions.ChatForACPSession(active); ok && c != chatID {
		if h, ok := t.chats.Get(ctx, c); ok && h.Name != "" {
			holder = `"` + displayText(h.Name) + `"`
		}
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{
		Label: "Waiting for " + holder,
	}))
	n := marotte.SystemNoticePayload{
		Level:   marotte.NoticeInfo,
		Message: "Waiting for " + holder + " to finish: kiro-cli runs one agent turn at a time, so this turn starts when that one ends.",
	}
	if c, ok := t.chats.Get(ctx, chatID); ok {
		n.ChatName = c.Name
	}
	t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventSystemNotice, chatID, n))
}

// endSlotWait forgets the chat's shown wait at its turn's bracket; a starting turn replaces the label.
func (t *Translator) endSlotWait(ctx context.Context, chatID marotte.ChatID, starting bool) {
	if t.slotWaits.clear(chatID) && starting {
		t.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventWorkingLabel, chatID, marotte.WorkingLabelPayload{
			Label: marotte.WorkingLabelThinking,
		}))
	}
}
