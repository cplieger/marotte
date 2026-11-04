package push

import (
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/liveness"
	"github.com/cplieger/marotte/internal/marotte"
)

// deferSuppressedSends is the one switch over what the presence filter does with a push it
// suppresses. On, the default: the delivery is HELD and lands when the profile flips to gone inside
// the kind's TTL, so an ask raised while a locked phone still reads present reaches it once the
// alive window closes on the lock. Off is the plain drop: not pushed to that profile, then or
// later, and the person learns of it from the pending snapshot at unlock. A var only so its tests
// can turn it off; production never reassigns it.
var deferSuppressedSends = true

// One beat: presence itself moves at that granularity. A var so a test can shrink the wait rather
// than the window.
var deferPoll = liveness.Keepalive

// heldKey bounds the queue at one entry per profile, kind and subject, so the profile gets
// one push per key when it goes, carrying the latest event.
type heldKey struct {
	tag     string
	subject string
	kind    marotte.PushKind
}

type heldPush struct {
	expiresAt time.Time
	sub       marotte.PushSubscription
	// events are the key's held events, oldest first, each naming the ask it announces ("" for none). The newest
	// lands; a retraction removes its own ask's, so an answered ask cannot take a later one's push with it.
	events []heldEvent
}

type heldEvent struct {
	ask     string
	payload []byte
}

// add makes payload the newest event, replacing an earlier one about the same ask.
func (h *heldPush) add(ask string, payload []byte) {
	h.events = append(slices.DeleteFunc(h.events, func(e heldEvent) bool { return e.ask == ask }),
		heldEvent{ask: ask, payload: payload})
}

// Its clock is the presence table's, so the verdict and the TTL move together.
type deferred struct {
	held  map[heldKey]heldPush
	timer *time.Timer
	mu    sync.Mutex
}

// With the switch off it is a no-op, which is the drop.
func (s *Service) holdForLater(
	sub marotte.PushSubscription, kind marotte.PushKind, subject marotte.PushSubject, ask string, payload []byte,
) {
	if !deferSuppressedSends || s.presence == nil {
		return
	}
	key := heldKey{tag: tagOf(sub.Endpoint), kind: kind, subject: debounceKey(kind, subject).subject}
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	if s.deferred.held == nil {
		s.deferred.held = make(map[heldKey]heldPush)
	}
	h := s.deferred.held[key]
	h.sub, h.expiresAt = sub, s.presence.now().Add(ttlDuration(kind))
	h.add(ask, payload)
	s.deferred.held[key] = h
	if s.deferred.timer == nil {
		s.deferred.timer = time.AfterFunc(deferPoll, s.releaseHeld)
	}
}

// joinHeld adds an ask the debounce kept from sending to every delivery already held about its kind and subject, so
// the push held for an earlier ask still lands for this one once that ask is answered.
func (s *Service) joinHeld(kind marotte.PushKind, subject marotte.PushSubject, ask string, payload []byte) {
	if ask == "" {
		return
	}
	want := debounceKey(kind, subject).subject
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	for key, h := range s.deferred.held {
		if key.kind == kind && key.subject == want {
			h.add(ask, payload)
			s.deferred.held[key] = h
		}
	}
}

// Retract drops the held ask push about subject that announces ask: the ask was answered on some
// surface, so a nudge about it has nothing left to say. A later ask's push and another kind's stay
// held. A no-op with the variant off, since nothing is ever held.
func (s *Service) Retract(subject marotte.PushSubject, ask string) {
	if !deferSuppressedSends {
		return
	}
	want := debounceKey("", subject).subject
	s.deferred.mu.Lock()
	defer s.deferred.mu.Unlock()
	for key, h := range s.deferred.held {
		if key.kind != marotte.PushKindPermission || key.subject != want {
			continue
		}
		h.events = slices.DeleteFunc(h.events, func(e heldEvent) bool { return e.ask == ask })
		if len(h.events) == 0 {
			delete(s.deferred.held, key)
		} else {
			s.deferred.held[key] = h
		}
	}
}

// releaseHeld re-judges every held delivery: past its TTL it is dropped, a profile
// now gone gets its push, a profile still present waits another poll. Re-arms
// itself while anything is held; the service lifetime ending stops the sends
// through the fan-out's context.
func (s *Service) releaseHeld() {
	if s.lifetime.Err() != nil {
		return
	}
	now := s.presence.now()
	var due []struct {
		key heldKey
		h   heldPush
	}
	s.deferred.mu.Lock()
	for key, h := range s.deferred.held {
		switch {
		case now.After(h.expiresAt):
			delete(s.deferred.held, key)
			slog.Debug("push: held delivery expired unsent", "kind", string(key.kind), "tag", key.tag)
		case s.presence.Gone(key.tag):
			delete(s.deferred.held, key)
			due = append(due, struct {
				key heldKey
				h   heldPush
			}{key, h})
		}
	}
	// Close cancels the lifetime before stop takes the lock, so a poll that raced Close never re-arms past it.
	if len(s.deferred.held) == 0 || s.lifetime.Err() != nil {
		s.deferred.timer = nil
	} else {
		s.deferred.timer = time.AfterFunc(deferPoll, s.releaseHeld)
	}
	s.deferred.mu.Unlock()
	for i := range due {
		d := &due[i]
		slog.Debug("push: held delivery released, profile gone", "kind", string(d.key.kind), "tag", d.key.tag)
		s.fanOut(s.lifetime, []marotte.PushSubscription{d.h.sub}, d.h.events[len(d.h.events)-1].payload, d.key.kind)
	}
}

// A poll already running sees the cancelled lifetime and sends nothing.
func (d *deferred) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
