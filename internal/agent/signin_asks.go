package agent

import (
	"sync"

	"github.com/cplieger/marotte/internal/signin"
)

// signInAsks books the vault's answers to the engine's credential asks, so a turn that failed on its credential is
// graded by the asks it could have depended on and never by a later one. KAS's ask names no session, so a turn
// depended on the asks answered while it was open, else on the engine's newest ask before it opened.
type signInAsks struct {
	mu sync.Mutex
	// seq counts the answers; refusedAt is the seq of the newest refused one, zero for none.
	seq       uint64
	refusedAt uint64
	refusal   signin.Refusal
}

// mark is the position a turn opening now reads its asks from.
func (b *signInAsks) mark() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// answered books one answer: why is its refusal, zero for a served ask.
func (b *signInAsks) answered(why signin.Refusal) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	if why != 0 {
		b.refusedAt, b.refusal = b.seq, why
	}
}

// refusalFor is the refusal a turn opened at mark failed on: the newest one answered since it opened, else the
// engine's newest answer when that was a refusal; zero when the engine held a served credential, which Kiro then
// refused.
func (b *signInAsks) refusalFor(mark uint64) signin.Refusal {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refusedAt != 0 && (b.refusedAt > mark || b.refusedAt == b.seq) {
		return b.refusal
	}
	return 0
}
