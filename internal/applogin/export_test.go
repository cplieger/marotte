package applogin

import (
	"context"
	"time"

	"github.com/cplieger/access"
)

// holdPublish arms the writer's hold on its next publishing group; pending counts every job
// submitted and not yet answered, Updates and single-call writes alike.
func (s *Store) holdPublish() (pending func() int, release func()) {
	_, pending, release = s.holdGroup()
	return pending, release
}

// holdGroup arms the hold like holdPublish and also returns a channel closed once a group is held.
func (s *Store) holdGroup() (entered <-chan struct{}, pending func() int, release func()) {
	h := &publishHold{entered: make(chan struct{}), release: make(chan struct{})}
	s.w.hold.Store(h)
	var released bool
	return h.entered, func() int { return int(s.w.pending.Load()) },
		func() {
			if !released {
				released = true
				close(h.release)
			}
		}
}

func (s *Store) diskIsOwned() bool { return s.diskOwned.Load() }

// setKeysForTest replaces the device keys and the failure key on the next publication.
func (s *Store) setKeysForTest(device, failure []byte) error {
	return s.write(context.Background(), func(tx *txView) error {
		snap := tx.s
		oldD, oldF := snap.deviceKeys, snap.failureKey
		snap.deviceKeys = []access.Secret{access.NewSecret(string(device))}
		snap.failureKey = access.NewSecret(string(failure))
		tx.record(func() { snap.deviceKeys, snap.failureKey = oldD, oldF })
		return nil
	})
}

// putSessionForTest stores r as is, bypassing the SPI's rules.
func (s *Store) putSessionForTest(r storeSession) error {
	return s.write(context.Background(), func(tx *txView) error {
		tx.putSession(&r)
		return nil
	})
}

// setFenceTick replaces the fence loop's ticker for the next fence.
func (s *Store) setFenceTick(tick <-chan time.Time) { s.fence.tick = tick }

func (s *Store) pending() int { return int(s.w.pending.Load()) }
