package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// configurationStateMsg is a _kiro/configuration/state notification over a captured kiro-cli 2.28.0
// view (internal/translate/testdata/configuration_state.json).
func configurationStateMsg(t *testing.T, view string) *marotte.RPCResponse {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "translate", "testdata", "configuration_state.json"))
	if err != nil {
		t.Fatalf("read the configuration_state fixture: %v", err)
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse the configuration_state fixture: %v", err)
	}
	return &marotte.RPCResponse{Method: methodKiroConfigurationState, Params: fx[view]}
}

func settingsUpdatedCount(t *testing.T, h *Runtime) int {
	t.Helper()
	n := 0
	for _, typ := range extractTypes(t, bufferedSince(h, 0)) {
		if typ == string(marotte.EventSettingsUpdated) {
			n++
		}
	}
	return n
}

func TestConfigurationState_OnlyTheUtilityBridgeDeclaresIt(t *testing.T) {
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(t.Context(), "/tmp/work", func() ACPBridge { return br }, cs)
	cs.wire(h)
	_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })

	if _, err := h.coord.openBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if opts := br.lastStartOpts(); opts == nil || opts.ConfigurationState {
		t.Fatalf("chat StartOpts = %+v, want a chat spawn that withholds configurationState", opts)
	}
	u := h.utility.get()
	if _, err := u.session.acquire(t.Context()); err != nil {
		t.Fatalf("acquire utility session: %v", err)
	}
	defer u.session.Stop()
	if opts := br.lastStartOpts(); opts == nil || !opts.ConfigurationState {
		t.Errorf("utility StartOpts = %+v, want ConfigurationState true", opts)
	}
}

func TestConfigurationState_TheUtilityViewResolvesWorkValidationsDefault(t *testing.T) {
	h, _, _ := newTestHub()
	t.Cleanup(h.stopUtilityBridge)
	us := h.utility.get().session

	if got := h.config.KiroDefaults(); len(got) != 0 {
		t.Errorf("KiroDefaults() before any view = %v, want empty: nothing is known yet", got)
	}
	if !us.dispatchNotification(configurationStateMsg(t, "env_connection")) {
		t.Fatal("the utility session did not claim _kiro/configuration/state")
	}
	want := marotte.KiroDefault{Value: "on", Layer: "kiro-agent", LayerName: "Kiro Agent"}
	if got := h.config.KiroDefaults()[settings.KeyWorkValidation]; got != want {
		t.Errorf("work_validation default = %+v, want %+v", got, want)
	}
	if n := settingsUpdatedCount(t, h); n != 1 {
		t.Errorf("settings_updated broadcasts = %d, want 1 so every device re-reads the Default", n)
	}

	us.dispatchNotification(configurationStateMsg(t, "env_session"))
	if got := h.config.KiroDefaults()[settings.KeyWorkValidation]; got != want {
		t.Errorf("work_validation default after a session view = %+v, want the connection view's %+v", got, want)
	}
	us.dispatchNotification(configurationStateMsg(t, "env_connection"))
	if n := settingsUpdatedCount(t, h); n != 1 {
		t.Errorf("settings_updated broadcasts after an unchanged view = %d, want still 1", n)
	}

	us.dispatchNotification(configurationStateMsg(t, "unallocated_connection"))
	if got, want := h.config.KiroDefaults()[settings.KeyWorkValidation], (marotte.KiroDefault{Value: "off"}); got != want {
		t.Errorf("work_validation default with nothing stated = %+v, want KAS's built-in %+v", got, want)
	}
	if _, present := h.config.KiroDefaults()[settings.KeyCloudFormationSafety]; present {
		t.Error("cloudformation_safety_check carries a Default; KAS's infraSafety reader does not read the store")
	}
}
