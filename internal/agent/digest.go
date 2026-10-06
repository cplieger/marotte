package agent

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"github.com/cplieger/sse"
)

const (
	// digestConcurrency bounds resolutions in flight: four slots for four waking devices.
	digestConcurrency = 4
	// digestTimeout is POST /api/sync's RouteTimeout.
	digestTimeout = 10 * time.Second
)

// resolveDigest is the hub's Resolver: one State per Held, from the registry writers mint
// into. It takes no per-chat store mutex and no lock held across I/O.
func (rt *Runtime) resolveDigest(ctx context.Context, held []sse.Held) ([]sse.State, error) {
	if err := rt.digestSlots.acquire(ctx); err != nil {
		return nil, err
	}
	defer rt.digestSlots.release()
	if rt.digestHook != nil {
		rt.digestHook()
	}
	out := make([]sse.State, 0, len(held))
	for i := range held {
		out = append(out, rt.resolveOne(ctx, &held[i]))
	}
	return out, nil
}

// resolveOne answers one subject; an unserved kind is gone, so the client forgets it.
func (rt *Runtime) resolveOne(ctx context.Context, h *sse.Held) sse.State {
	st := sse.State{Subject: h.Subject}
	switch subject.Kind(h.Kind) {
	case subject.KindChat:
		if !rt.chatStore.Exists(marotte.ChatID(h.Ref)) {
			st.Status = sse.StatusGone
			return st
		}
		st.Version, _ = rt.versions.Current(subject.KindChat, h.Ref)
	case subject.KindChats, subject.KindPending, subject.KindRuns, subject.KindCatalog, subject.KindStatus,
		subject.KindForgeInventory:
		st.Version, _ = rt.versions.Current(subject.Kind(h.Kind), h.Ref)
	case subject.KindLiveTurn:
		// The ref is a turn id: the registry names the chat holding it, the store its newest sealed seq.
		chatID, held := rt.coord.turns.holdsTurn(h.Ref)
		if !held {
			st.Status = sse.StatusGone
			return st
		}
		seq, ok := rt.chatStore.TurnSeq(ctx, chatID, h.Ref)
		if !ok {
			st.Status = sse.StatusGone
			return st
		}
		st.Version = turnVersion(h.Ref, seq)
	case subject.KindRunTurn:
		wf, turn, _ := strings.Cut(h.Ref, "/")
		seq, open := rt.runs.openSeq(wf, turn)
		if !open {
			st.Status = sse.StatusGone
			return st
		}
		st.Version = turnVersion(turn, seq)
	case subject.KindTabs:
		var version uint64
		if rt.tabs != nil {
			_, version = rt.tabs.List()
		}
		st.Version = strconv.FormatUint(version, 10)
	default:
		st.Status = sse.StatusGone
	}
	return st
}

// digestSemaphore is a context-aware counting semaphore: an expired waiter fails with ctx.Err().
type digestSemaphore chan struct{}

func newDigestSemaphore(n int) digestSemaphore { return make(chan struct{}, n) }

func (s digestSemaphore) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s digestSemaphore) release() { <-s }

// turnVersion is a turn stamp's version: turn id and newest sealed seq.
func turnVersion(turn string, seq uint64) string {
	return turn + ":" + strconv.FormatUint(seq, 10)
}
