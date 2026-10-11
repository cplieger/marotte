package command

// KAS's `autopilot` option is a select over "on" and "off"; a JSON boolean is refused, so these
// tests pin the string.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func supervisedReq(t *testing.T, chatID marotte.ChatID, enabled bool) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetSupervisedModeCommand{Enabled: enabled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdSetSupervisedMode,
		ChatID:  chatID,
		Payload: payload,
	}
}

func TestCmdSetSupervisedMode_SendsAutopilotAsAString(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		wantValue string
	}{
		{name: "enabling supervised turns autopilot off", enabled: true, wantValue: marotte.ConfigValueAutopilotOff},
		{name: "disabling supervised turns autopilot on", enabled: false, wantValue: marotte.ConfigValueAutopilotOn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{result: map[string]any{}, sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			_, err := cmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", tc.enabled))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
			}
			if b.callCount != 1 {
				t.Fatalf("bridge calls = %d, want 1", b.callCount)
			}
			if b.gotMethod != marotte.MethodSetConfigOption {
				t.Errorf("method = %q, want %q", b.gotMethod, marotte.MethodSetConfigOption)
			}
			if b.gotParams["configId"] != marotte.ConfigOptionAutopilot {
				t.Errorf("configId = %v, want %q", b.gotParams["configId"], marotte.ConfigOptionAutopilot)
			}
			got, ok := b.gotParams["value"].(string)
			if !ok {
				t.Fatalf("value = %#v (%T), want the string %q; a bare boolean is refused with -32602 and autopilot stays on",
					b.gotParams["value"], b.gotParams["value"], tc.wantValue)
			}
			if got != tc.wantValue {
				t.Errorf("value = %q, want %q", got, tc.wantValue)
			}
		})
	}
}

// The record still carries the user's choice, whatever the wire value maps to.
func TestCmdSetSupervisedMode_PersistsTheChoiceOnTheChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})

	if _, err := cmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", true)); err != nil {
		t.Fatalf("CmdSetSupervisedMode: %v", err)
	}

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if !c.SupervisedMode {
		t.Error("SupervisedMode = false, want true; the choice has to survive to reach StartOpts.Supervised")
	}
}
