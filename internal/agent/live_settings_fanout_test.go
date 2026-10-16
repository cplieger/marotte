package agent

import (
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
)

type parkedPush struct {
	*liveRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *parkedPush) SetPreferences(prefs map[marotte.PushKind]bool) {
	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
	p.liveRecorder.SetPreferences(prefs)
}

func TestSyncProcessLive_TogglesReadBeforeAHandEditCannotLandAfterTheOpenThatReadIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		configDir := t.TempDir()
		push := &parkedPush{liveRecorder: newLiveRecorder(t), entered: make(chan struct{}), release: make(chan struct{})}
		h := reopenFixtureIn(t, configDir, nil, WithPush(push))
		rewriteConfigByHand(t, configDir, `{"notify_pr_status":true}`)

		go h.syncProcessLive(t.Context())
		<-push.entered
		rewriteConfigByHand(t, configDir, `{"notify_pr_status":false}`)
		opened := make(chan error, 1)
		go func() {
			_, err := h.coord.openBridge(t.Context(), "c1", "")
			opened <- err
		}()
		synctest.Wait()
		close(push.release)
		if err := <-opened; err != nil {
			t.Fatalf("OpenBridge: %v", err)
		}
		synctest.Wait()

		if push.Preferences()[marotte.PushKindPRStatus] {
			t.Errorf("pr_status notifications are on after an open that read the hand edit turning them off: a sync that " +
				"read the older document landed after it")
		}
	})
}
