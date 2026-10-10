package agent

import (
	"errors"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func TestOpenBridge_ALivePushABridgeMissedIsResentAtTheNextOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		method string
	}{
		{name: "ignore_files", body: `{"agent_ignore_files":[".gitignore"]}`, method: marotte.MethodPolicyIgnoreFilesChanged},
		{name: "terminal_timeout", body: `{"terminal_command_timeout_ms":300000}`, method: marotte.MethodTerminalSettingsChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, configDir := reopenFixture(t)
			first, err := h.coord.openBridge(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			br := fakeOf(first)
			br.setNotifyErr(tc.method, errors.New("broken pipe"))
			rewriteConfigByHand(t, configDir, tc.body)
			if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
				t.Fatalf("OpenBridge after the hand edit: %v", err)
			}
			br.setNotifyErr(tc.method, nil)

			for i := range 2 {
				if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
					t.Fatalf("OpenBridge %d after the bridge healed: %v", i+1, err)
				}
				if got := len(br.notified(tc.method)); got != 1 {
					t.Errorf("after open %d since the bridge healed it holds %d %s frames, want 1: the first resends the "+
						"missed push before it returns, a later one does not repeat it", i+1, got, tc.method)
				}
			}
		})
	}
}

func TestOpenBridge_ALiveSettingItsSpawnFailedToSendIsSentAtTheNextOpen(t *testing.T) {
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
			h := reopenFixtureIn(t, configDir, func(br *fakeBridge) {
				br.setNotifyErr(tc.method, errors.New("broken pipe"))
				if tc.midway != "" {
					br.afterInitialize = func() { rewriteConfigByHand(t, configDir, tc.midway) }
				}
			})
			br := fakeOf(openChat(t, h, "c1"))
			br.setNotifyErr(tc.method, nil)

			for i := range 2 {
				openChat(t, h, "c1")
				if got := br.notified(tc.method); !slices.Equal(got, []string{tc.want}) {
					t.Errorf("after open %d since the spawn's %s send failed the chat holds frames %v, want [%s]: "+
						"a failed startup send is no record of delivery", i+1, tc.method, got, tc.want)
				}
			}
		})
	}
}

func TestOpenBridge_AFailedContentCollectionAssertIsResentAtTheNextOpen(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br := fakeOf(first)
	br.setCallErr(marotte.MethodSetConfigOption, errors.New("broken pipe"))
	rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge after the hand edit: %v", err)
	}
	br.mu.Lock()
	delete(br.callErrs, marotte.MethodSetConfigOption)
	br.mu.Unlock()

	for i := range 2 {
		if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
			t.Fatalf("OpenBridge %d after the bridge healed: %v", i+1, err)
		}
		if got := contentCollectionWrites(br); len(got) != 2 {
			t.Errorf("after open %d since the bridge healed its content-collection asserts are %v, want the failed one "+
				"and one resend", i+1, got)
		}
	}
}

func TestOpenBridge_AMissedPushIsHeldOverAnUnreadableDocumentAndResentOnceItParses(t *testing.T) {
	h, configDir := reopenFixture(t)
	first, err := h.coord.openBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	br := fakeOf(first)
	br.setNotifyErr(marotte.MethodTerminalSettingsChanged, errors.New("broken pipe"))
	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge after the hand edit: %v", err)
	}
	br.setNotifyErr(marotte.MethodTerminalSettingsChanged, nil)

	rewriteConfigByHand(t, configDir, "{half typed")
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge over the half-typed document: %v", err)
	}
	if frames := br.notified(marotte.MethodTerminalSettingsChanged); len(frames) != 0 {
		t.Errorf("the resend over an unparseable config.json sent %v; it answers the default timeout, not the user's", frames)
	}

	rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)
	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge once the document parses: %v", err)
	}
	want := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
	if got := br.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, want) {
		t.Errorf("once config.json parsed again with the same timeout the bridge was sent %v, want %v", got, want)
	}
}

func TestOpenBridge_ASpawnWhoseContentCollectionAssertFailedAssertsItAtTheNextOpen(t *testing.T) {
	h, _ := reopenFixtureWith(t, func(br *fakeBridge) {
		br.callErrs = map[string]error{marotte.MethodSetConfigOption: errors.New("broken pipe")}
	})
	br := fakeOf(openChat(t, h, "c1"))
	br.mu.Lock()
	delete(br.callErrs, marotte.MethodSetConfigOption)
	br.mu.Unlock()

	openChat(t, h, "c1")

	if got := contentCollectionWrites(br); len(got) != 2 || got[1] != marotte.ConfigValueContentCollectionDisabled {
		t.Errorf("content-collection asserts = %v, want the first open's failed one and the next open's %q: "+
			"a spawn whose door assert failed leaves KAS on its opted-in default", got, marotte.ConfigValueContentCollectionDisabled)
	}
}
