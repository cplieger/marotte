package agent

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// windowWorld walks a chat from the failsafe's stop of m-a (compact-1 and m-resume listed, m-q1 paused) through the
// turns that can run before the resume starts, in a random order, with the reader's stops among the steps.
type windowWorld struct {
	*tableWorld
	r       *rand.Rand
	reached map[string]int
	// withdrawn names the resumes that waited when the reader stopped, which must never start.
	withdrawn map[string]bool
	tok       uint64
	ids       int
}

// window names what runs between the failsafe's stop and the resume's start, as the reader sees it.
func (w *windowWorld) window() string {
	live := w.l.entries[:w.l.liveLen()]
	switch {
	case len(live) == 2 && live[0].kind == marotte.RunKindCompaction:
		return "kiro-cli's turn under the compaction"
	case len(live) == 0:
		return "nothing live"
	}
	h := live[0]
	switch {
	case h.kind == marotte.RunKindCompaction:
		return "the compaction"
	case h.wire():
		return "kiro-cli's re-prompt"
	case h.kind == marotte.RunKindUnread:
		return "the stopped turn's unread steers"
	case h.state == marotte.RunClosing:
		return "a closing turn"
	case h.fsStopped:
		return "the turn the failsafe stops"
	}
	return "a " + string(h.kind) + " turn"
}

// readerStop is one stop the reader can make: Cancel drawn against the turn on the page, Cancel of what the dock
// names, Cancel drawn against a turn already gone, or a send-now press.
func (w *windowWorld) readerStop() {
	w.tok++
	kinds := []string{"Cancel drawn against the page", "Cancel of what the dock names", "Cancel of a gone turn", "send-now"}
	kind := kinds[w.r.IntN(len(kinds))]
	r, at := w.l.resume(), w.window()
	switch kind {
	case kinds[0]:
		target := "m-a"
		if live := w.l.entries[:w.l.liveLen()]; len(live) > 0 {
			target = live[len(live)-1].id
		}
		w.l.readerStop(target, w.tok)
	case kinds[1]:
		w.l.readerStop("", w.tok)
	case kinds[2]:
		w.l.readerStop("m-gone", w.tok)
	default:
		h, err := w.l.sendNow("m-q1", "", false)
		if err != nil {
			return
		}
		if h != nil {
			w.l.readerStop(h.id, w.tok)
		}
	}
	if r != nil {
		w.withdrawn[r.id] = true
		w.reached[kind+" during "+at]++
	}
}

// step runs one thing that can happen in the window: kiro-cli's re-prompt opens, a turn's close leaves steers unread,
// the head turn opens, closes or reaches 95% again, its close tail ends, the compaction is written or answered with
// the admission slot held a while (a close's hold), and the slot is given back.
func (w *windowWorld) step() {
	live := w.l.entries[:w.l.liveLen()]
	if w.r.IntN(5) == 0 {
		w.readerStop()
		return
	}
	if len(live) == 0 {
		w.held = false
		return
	}
	h := live[len(live)-1]
	switch {
	case h.state == marotte.RunClosing && !h.endsDone && w.r.IntN(2) == 0:
		w.l.endsChanged(1, 0, []string{"s-1"}, false, true, w.facts())
		w.l.closeEndsResolved(h.id, false, true, w.facts())
	case h.state == marotte.RunClosing && w.r.IntN(2) == 0 && !slices.ContainsFunc(live, (*runEntry).wire):
		w.wireOpen("t-w"+strconv.Itoa(w.ids), true)
		w.ids++
	case h.state == marotte.RunClosing:
		w.l.tailEnded(h.id, false)
		w.held = false
		w.l.reservationReleased(h.res.Token)
	case h.kind == marotte.RunKindCompaction && h.state == marotte.RunStarting:
		w.l.compactionStart(h.id, w.facts())
	case h.kind == marotte.RunKindCompaction && w.r.IntN(3) == 0 && len(live) == 1:
		w.wireOpen("t-w"+strconv.Itoa(w.ids), true)
		w.ids++
	case h.kind == marotte.RunKindCompaction:
		w.held = w.r.IntN(2) == 0
		w.l.compactionEnded(h.id, true, nil)
	case h.state == marotte.RunStarting && h.turnID == "":
		w.running(h.id)
	case h.state == marotte.RunRunning && w.r.IntN(4) == 0 && len(live) == 1:
		w.l.usageUnderThePoint()
		w.ids++
		w.tok++
		w.l.failsafe(failsafeFacts{turnID: h.turnID, resumeID: "m-resume-" + strconv.Itoa(w.ids), token: w.tok})
	default:
		w.l.turnClosed(listClose{turnID: h.turnID, seq: w.nextSeq}, w.facts())
	}
}

// Whatever runs between the failsafe's stop and the resume's start (the stopped turn, its close, kiro-cli's re-prompt,
// the compaction with or without kiro-cli's turn under it, the stopped turn's unread steers, or nothing), a stop the
// reader makes there means the resume never starts. A walk with no reader stop does start it, so the walks reach the
// resume's take.
func TestFailsafe_NoResumeStartsAfterAReaderStop(t *testing.T) {
	reached := map[string]int{}
	for seed := range uint64(400) {
		t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
			w := &windowWorld{
				tableWorld: firedOn(t), r: rand.New(rand.NewPCG(seed, seed^0x5bd1e995)), reached: reached, tok: 1,
				withdrawn: map[string]bool{},
			}
			for range 60 {
				w.step()
				w.advance()
				for _, e := range w.l.takeEffects().launch {
					if e.kind != marotte.RunKindResume {
						continue
					}
					if w.withdrawn[e.id] {
						t.Fatalf("resume %s started after a stop of the reader's while it waited; list %s", e.id, w.order())
					}
					reached["resume started with no reader stop"]++
				}
			}
		})
	}
	var want []string
	for _, kind := range []string{"Cancel drawn against the page", "Cancel of what the dock names", "Cancel of a gone turn", "send-now"} {
		for _, at := range []string{
			"the turn the failsafe stops", "a closing turn", "kiro-cli's re-prompt", "the compaction",
			"kiro-cli's turn under the compaction", "the stopped turn's unread steers", "nothing live",
		} {
			want = append(want, kind+" during "+at)
		}
	}
	for _, k := range append(want, "resume started with no reader stop") {
		if reached[k] == 0 {
			t.Errorf("the walks never reached %q", k)
		}
	}
}
