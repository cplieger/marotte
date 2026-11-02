package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// resendFixture builds a runtime over configDir whose engine lane is seeded before the engine starts.
func resendFixture(t *testing.T, configDir string, seedLane func(lane *fakeBridge), seedSession func(*fakeBridge)) (*Runtime, *fakeEngineSource) {
	t.Helper()
	lane := newFakeBridge()
	if seedLane != nil {
		seedLane(lane)
	}
	src := fakeEngineOf(func() engineSession {
		br := newFakeBridge()
		if seedSession != nil {
			seedSession(br)
		}
		return br
	}, lane)
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), src, cs, WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, src
}

func TestEngineLive_APushTheEngineMissedIsResentAtTheNextOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		method string
	}{
		{name: "ignore_files", body: `{"agent_ignore_files":[".gitignore"]}`, method: marotte.MethodPolicyIgnoreFilesChanged},
		{name: "terminal_timeout", body: `{"terminal_command_timeout_ms":300000}`, method: marotte.MethodTerminalSettingsChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			h, src := resendFixture(t, configDir, nil, nil)
			openChat(t, h, "c1")
			lane := src.lane
			lane.setNotifyErr(tc.method, errors.New("broken pipe"))
			rewriteConfigByHand(t, configDir, tc.body)
			openChat(t, h, "c1")
			lane.setNotifyErr(tc.method, nil)

			for i := range 2 {
				openChat(t, h, "c1")
				if got := len(lane.notified(tc.method)); got != 1 {
					t.Errorf("after open %d since the engine healed it holds %d %s frames, want 1: the first resends the "+
						"missed push before it returns, a later one does not repeat it", i+1, got, tc.method)
				}
			}
		})
	}
}

func TestEngineLive_ASettingTheEngineStartFailedToSendIsSentAtTheNextOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		boot   string
		midway string
		method string
		want   string
	}{
		{
			name: "ignore_files", boot: `{"agent_ignore_files":[".gitignore"]}`, method: marotte.MethodPolicyIgnoreFilesChanged,
			want: `{"files":[".kiroignore",".gitignore"]}`,
		},
		{
			name: "terminal_timeout", boot: `{"terminal_command_timeout_ms":1000}`, midway: `{"terminal_command_timeout_ms":300000}`,
			method: marotte.MethodTerminalSettingsChanged, want: `{"terminal":{"commandTimeoutMs":300000,"enabled":true}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			rewriteConfigByHand(t, configDir, tc.boot)
			lane := newFakeBridge()
			lane.setNotifyErr(tc.method, errors.New("broken pipe"))
			src := fakeEngineOf(func() engineSession { return newFakeBridge() }, lane)
			if tc.midway != "" {
				src.afterInitialize = func() { rewriteConfigByHand(t, configDir, tc.midway) }
			}
			cs := newTestChatStore()
			h := newRuntimeOn(context.Background(), t.TempDir(), src, cs, WithConfigDir(configDir))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			if !awaitEngine(h) {
				t.Fatal("the engine never became ready")
			}
			lane.setNotifyErr(tc.method, nil)

			for i := range 2 {
				openChat(t, h, "c1")
				if got := lane.notified(tc.method); !slices.Equal(got, []string{tc.want}) {
					t.Errorf("after open %d since the engine start's %s send failed the engine holds frames %v, want [%s]: "+
						"a failed startup send is no record of delivery", i+1, tc.method, got, tc.want)
				}
			}
		})
	}
}

func TestOpenBridge_AFailedContentCollectionAssertIsResentAtTheNextOpen(t *testing.T) {
	configDir := t.TempDir()
	h, src := resendFixture(t, configDir, nil, nil)
	openChat(t, h, "c1")
	lane := src.lane
	lane.setCallErr(marotte.MethodSetConfigOption, errors.New("broken pipe"))
	rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)
	openChat(t, h, "c1")
	lane.mu.Lock()
	delete(lane.callErrs, marotte.MethodSetConfigOption)
	lane.mu.Unlock()

	for i := range 2 {
		openChat(t, h, "c1")
		if got := contentCollectionWrites(lane); len(got) != 2 {
			t.Errorf("after open %d since the engine healed its content-collection asserts are %v, want the failed one "+
				"and one resend", i+1, got)
		}
	}
}

func TestOpenBridge_AMissedPushIsHeldOverAnUnreadableDocumentAndResentOnceItParses(t *testing.T) {
	configDir := t.TempDir()
	h, src := resendFixture(t, configDir, nil, nil)
	openChat(t, h, "c1")
	lane := src.lane
	lane.setNotifyErr(marotte.MethodTerminalSettingsChanged, errors.New("broken pipe"))
	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)
	openChat(t, h, "c1")
	lane.setNotifyErr(marotte.MethodTerminalSettingsChanged, nil)

	rewriteConfigByHand(t, configDir, "{half typed")
	openChat(t, h, "c1")
	if frames := lane.notified(marotte.MethodTerminalSettingsChanged); len(frames) != 0 {
		t.Errorf("the resend over an unparseable config.json sent %v; it answers the default timeout, not the user's", frames)
	}

	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)
	openChat(t, h, "c1")
	want := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
	if got := lane.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, want) {
		t.Errorf("once config.json parsed again with the same timeout the engine was sent %v, want %v", got, want)
	}
}

// A door's failed assert leaves the engine's record unconfirmed, so the open's engine sync asserts it before the open
// returns, once.
func TestOpenBridge_ADoorWhoseContentCollectionAssertFailedIsAssertedBeforeTheOpenReturns(t *testing.T) {
	h, src := resendFixture(t, t.TempDir(), nil, func(br *fakeBridge) {
		br.callErrs = map[string]error{marotte.MethodSetConfigOption: errors.New("broken pipe")}
	})
	for i := range 2 {
		openChat(t, h, "c1")
		if got := contentCollectionWrites(src.lane); len(got) != 1 || got[0] != marotte.ConfigValueContentCollectionDisabled {
			t.Errorf("after open %d the engine's content-collection asserts are %v, want one %q: "+
				"a door whose assert failed leaves KAS on its opted-in default", i+1, got, marotte.ConfigValueContentCollectionDisabled)
		}
	}
}
