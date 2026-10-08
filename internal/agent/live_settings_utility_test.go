package agent

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func TestUtilitySession_AnIgnoreListItsSpawnFailedToSendIsSentAtItsNextUse(t *testing.T) {
	configDir := t.TempDir()
	rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"]}`)
	h := reopenFixtureIn(t, configDir, func(br *fakeBridge) {
		br.setNotifyErr(marotte.MethodPolicyIgnoreFilesChanged, errors.New("broken pipe"))
	})
	utility := acquireUtility(t, h)
	utility.setNotifyErr(marotte.MethodPolicyIgnoreFilesChanged, nil)
	want := []string{jsonOf(t, map[string]any{marotte.ParamIgnoreFiles: []string{settings.AgentIgnoreFloor, ".gitignore"}})}

	for i := range 2 {
		acquireUtility(t, h)
		if got := utility.notified(marotte.MethodPolicyIgnoreFilesChanged); !slices.Equal(got, want) {
			t.Errorf("after use %d since the spawn's ignore send failed the utility process holds frames %v, want %v",
				i+1, got, want)
		}
	}
}

func TestUtilityUse_AHandEditReachesTheLiveChats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		chat := fakeOf(openChat(t, h, "c1"))
		synctest.Wait()
		rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)

		acquireUtility(t, h)
		synctest.Wait()

		want := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
		if got := chat.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, want) {
			t.Errorf("after a utility use the open chat holds timeout frames %v, want %v", got, want)
		}
	})
}

func TestUtilitySession_AHandEditOfTheIgnoreListReachesItAtItsNextUse(t *testing.T) {
	h, configDir := reopenFixture(t)
	utility := acquireUtility(t, h)
	rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"]}`)
	want := []string{jsonOf(t, map[string]any{marotte.ParamIgnoreFiles: []string{settings.AgentIgnoreFloor, ".gitignore"}})}

	for i := range 2 {
		if again := acquireUtility(t, h); again != utility {
			t.Fatalf("Setup: use %d after the edit respawned the utility session", i+1)
		}
		if got := utility.notified(marotte.MethodPolicyIgnoreFilesChanged); !slices.Equal(got, want) {
			t.Errorf("after use %d since the hand edit the utility process holds ignore frames %v, want %v once; "+
				"KAS enforces the list on that process's own reads", i+1, got, want)
		}
	}
}

func TestUtilitySession_AnIgnoreListItSpawnedWithIsNotResent(t *testing.T) {
	h, configDir := reopenFixture(t)
	rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"]}`)
	utility := acquireUtility(t, h)

	acquireUtility(t, h)

	if got := utility.notified(marotte.MethodPolicyIgnoreFilesChanged); len(got) != 0 {
		t.Errorf("a use after a spawn that sent the list pushed ignore frames %v, want none", got)
	}
}

func TestUtilitySession_AHandEditOfContentCollectionReachesItAtItsNextUse(t *testing.T) {
	h, configDir := reopenFixture(t)
	utility := acquireUtility(t, h)
	rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)

	for i := range 2 {
		if again := acquireUtility(t, h); again != utility {
			t.Fatalf("Setup: use %d after the edit respawned the utility session", i+1)
		}
		if got := contentCollectionWrites(utility); !slices.Equal(got, []string{marotte.ConfigValueContentCollectionEnabled}) {
			t.Errorf("after use %d since the hand edit the utility process was asserted %v, want [%s] once",
				i+1, got, marotte.ConfigValueContentCollectionEnabled)
		}
	}
}

func TestUtilitySession_APushThatLandsOnAProcessSinceReplacedIsNotRecordedForTheNewOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		old := acquireUtility(t, h)
		us := h.utility.peek().session
		gate := make(chan struct{})
		old.blockNotify(marotte.MethodPolicyIgnoreFilesChanged, gate)
		rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"]}`)
		synced := make(chan struct{})
		go func() {
			defer close(synced)
			us.syncLive(context.Background(), h.liveSettings)
		}()
		synctest.Wait()

		us.Stop()
		rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".dockerignore"]}`)
		lease, err := us.acquire(t.Context())
		if err != nil {
			t.Fatalf("Setup: restart the utility session: %v", err)
		}
		replacement, _ := lease.bridge.(*fakeBridge)
		close(gate)
		<-synced

		us.syncLive(t.Context(), h.liveSettings)

		if got := replacement.notified(marotte.MethodPolicyIgnoreFilesChanged); len(got) != 0 {
			t.Errorf("the restarted utility process, spawned with the current list, was pushed %v: the old process's "+
				"push was recorded as the new one's", got)
		}
	})
}

func TestUtilityUse_TheBackgroundPassLeavesTheUtilityToItsOwnUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		utility := acquireUtility(t, h)
		synctest.Wait()
		utility.setCallErr(marotte.MethodSetConfigOption, errors.New("broken pipe"))
		rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)

		acquireUtility(t, h)
		synctest.Wait()

		if got := contentCollectionWrites(utility); len(got) != 1 {
			t.Errorf("content-collection asserts on the utility process since the edit = %v, want its use's alone", got)
		}
	})
}
