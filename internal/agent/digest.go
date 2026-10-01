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
	// digestConcurrency bounds resolutions in flight. The client is single-flight
	// per tab, so four slots serve four devices waking at once without queueing
	// and a flood degrades to waiting rather than to lock contention on the
	// streaming writers.
	digestConcurrency = 4
	// digestTimeout is the RouteTimeout on POST /api/sync, so a request parked on
	// a lock cannot hold its slot for as long as the peer stays connected.
	digestTimeout = 10 * time.Second
)

// resolveDigest is the hub's Resolver: one State per Held, in any order, from the
// registry the writers mint into. It takes NO per-chat store mutex — Mutate holds
// that across an fsynced file rewrite — and NO lock a writer holds across I/O; each
// answer is a few uncontended mutex reads.
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

// resolveOne answers one subject. A kind this server does not serve is gone, so
// the client forgets it rather than asking again on every digest.
func (rt *Runtime) resolveOne(ctx context.Context, h *sse.Held) sse.State {
	st := sse.State{Subject: h.Subject}
	switch subject.Kind(h.Kind) {
	case subject.KindChat:
		if !rt.chatStore.Exists(marotte.ChatID(h.Ref)) {
			st.Status = sse.StatusGone
			return st
		}
		st.Version, _ = rt.versions.Current(subject.KindChat, h.Ref)
	case subject.KindChats, subject.KindPending, subject.KindRuns, subject.KindCatalog, subject.KindStatus:
		st.Version, _ = rt.versions.Current(subject.Kind(h.Kind), h.Ref)
	case subject.KindLiveTurn:
		// The ref is a turn id; the registry says which chat still holds it open and
		// the store says the newest seq it sealed.
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

// digestSemaphore is a context-aware counting semaphore: a slot is acquired with
// the request context, so a waiter whose request expires fails with ctx.Err()
// instead of queueing behind the resolutions that filled the slots.
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

// turnVersion is a turn stamp's version: the turn id and its newest sealed seq.
func turnVersion(turn string, seq uint64) string {
	return turn + ":" + strconv.FormatUint(seq, 10)
}
