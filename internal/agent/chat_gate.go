package agent

import (
	"context"
	"sync"

	"github.com/cplieger/marotte/internal/marotte"
)

// gateVerdict is chatGates.admit's answer for one stamp.
type gateVerdict uint8

const (
	gateAdmitted    gateVerdict = iota
	gateClosedSince             // a close of the chat committed after the stamp was taken
	gateWait                    // a close of the chat is being decided or torn down
)

// chatGates is the per-chat life gate. Every commit that ends a chat's life (a tab close, a chat delete, a
// retention close) holds the chat's gate from before its check until its teardown returned, and commits the hold
// once the close is decided. A caller that would create life state for a chat (a session door, a lifecycle, a
// forward, a reservation) presents the stamp it took where its intent began, and is admitted only when no close
// committed since that stamp and none is held. Every check runs inside the checking site's own lock and waits
// outside it, so a hold cannot land between a check and the write it admits.
//
// An entry is never dropped: a stamp's verdict must not depend on when it is read, and an entry that vanished
// would admit any stamp from before its close. The cost is one small entry per chat ever closed.
type chatGates struct {
	chats map[marotte.ChatID]*gateState
	// seq is raised by every committed hold, registry-wide, so a stamp taken before a chat's first close compares
	// below that close.
	seq uint64
	mu  sync.Mutex
}

type gateState struct {
	// idle is made when holds leaves 0 and closed when it returns there; nil while idle.
	idle chan struct{}
	// last is seq at the chat's newest committed hold.
	last  uint64
	holds int
}

func newChatGates() *chatGates {
	return &chatGates{chats: make(map[marotte.ChatID]*gateState)}
}

// stamp is the gate's position now; a caller takes it where its intent toward a chat begins.
func (g *chatGates) stamp() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seq
}

// gateHold is one close's hold on a chat's gate: it blocks every admission until released, and once committed it
// ends every stamp taken before the commit.
type gateHold struct {
	g       *chatGates
	chatID  marotte.ChatID
	once    sync.Once
	settled bool // committed or released; guarded by g.mu
}

// hold blocks every admission for chatID until the returned hold is released.
func (g *chatGates) hold(chatID marotte.ChatID) *gateHold {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.chats[chatID]
	if st == nil {
		st = &gateState{}
		g.chats[chatID] = st
	}
	if st.holds == 0 {
		st.idle = make(chan struct{})
	}
	st.holds++
	return &gateHold{g: g, chatID: chatID}
}

// commit records that the close this hold guards committed: every stamp taken before it is ended for the chat. A
// commit after the hold's release does nothing and answers false.
func (h *gateHold) commit() bool {
	h.g.mu.Lock()
	defer h.g.mu.Unlock()
	if h.settled {
		return false
	}
	h.g.seq++
	h.g.chats[h.chatID].last = h.g.seq
	return true
}

// release drops the hold, waking the admissions it parked; idempotent. A hold released uncommitted ends nobody.
func (h *gateHold) release() {
	h.once.Do(func() {
		h.g.mu.Lock()
		defer h.g.mu.Unlock()
		h.settled = true
		st := h.g.chats[h.chatID]
		st.holds--
		if st.holds == 0 {
			close(st.idle)
			st.idle = nil
		}
	})
}

// admit is the gate's verdict for stamp, never waiting: on gateWait the caller waits on idle outside its own lock
// and checks again.
func (g *chatGates) admit(chatID marotte.ChatID, stamp uint64) (verdict gateVerdict, idle <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.chats[chatID]
	switch {
	case st == nil:
		return gateAdmitted, nil
	case st.last > stamp:
		return gateClosedSince, nil
	case st.holds > 0:
		return gateWait, st.idle
	}
	return gateAdmitted, nil
}

// held reports whether a close of chatID is being decided or torn down.
func (g *chatGates) held(chatID marotte.ChatID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.chats[chatID]
	return st != nil && st.holds > 0
}

// awaitNoClose waits until no close of chatID is held, reporting whether it waited; a ctx end answers the ctx's
// error. It admits nothing: the caller takes its stamp after it and reserves, which meets a later hold itself.
func (g *chatGates) awaitNoClose(ctx context.Context, chatID marotte.ChatID) (bool, error) {
	waited := false
	for {
		g.mu.Lock()
		var idle chan struct{}
		if st := g.chats[chatID]; st != nil {
			idle = st.idle
		}
		g.mu.Unlock()
		if idle == nil {
			return waited, nil
		}
		waited = true
		select {
		case <-idle:
		case <-ctx.Done():
			return waited, ctx.Err()
		}
	}
}
