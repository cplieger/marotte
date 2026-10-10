package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cplieger/marotte/internal/chatlock"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

func setModeReq(t *testing.T, chatID marotte.ChatID, modeID string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.SetModeCommand{ModeID: modeID})
	if err != nil {
		t.Fatalf("marshal set_mode payload: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSetMode, ChatID: chatID, Payload: payload}
}

// TestCmdSetMode_TombstonedChatIs404: the 404 comes from the store's own refusal rather than being
// inferred from a no-op plus an absent record.
func TestCmdSetMode_TombstonedChatIs404(t *testing.T) {
	host := newTestHost(t, tombstonedChats{testsupport.NewInMemoryChatStore()})

	_, err := cmdSetMode(t.Context(), host, host, host, host, setModeReq(t, "c1", "spec"))

	if err == nil {
		t.Fatal("CmdSetMode on a tombstoned chat returned no error; the pill flips for a chat that does not exist")
	}
	if got := statusOf(err); got != http.StatusNotFound {
		t.Errorf("CmdSetMode on a tombstoned chat = %d, want %d", got, http.StatusNotFound)
	}
}

// TestCmdSetMode_NoOpAndAutoCreate pins the two ordinary outcomes, so the refusal above cannot be
// spelled as "anything that changed nothing is a 404".
func TestCmdSetMode_NoOpAndAutoCreate(t *testing.T) {
	t.Run("a repeat pick succeeds and says nothing", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		spy := &promptSpy{hostDouble: newTestHost(t, store)}

		if _, err := cmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("first pick: %v", err)
		}
		before := len(spy.events)
		if _, err := cmdSetMode(t.Context(), spy, spy, spy, spy, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("repeat pick: %v", err)
		}
		for _, evt := range spy.events[before:] {
			if evt.Type == marotte.EventModeChanged {
				t.Error("a repeat pick of the mode already in force broadcast mode_changed")
			}
		}
	})

	t.Run("a chat with no record yet is created", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		if _, err := cmdSetMode(t.Context(), host, host, host, host, setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("CmdSetMode on a fresh chat: %v", err)
		}

		c, ok := store.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("set_mode on a chat with no record did not create one; the pick cannot reach session/new")
		}
		if c.CurrentModeID != "spec" {
			t.Errorf("CurrentModeID = %q, want %q", c.CurrentModeID, "spec")
		}
	})
}

// TestSessionConfig_ColdSpawnPersistsAndASessionRefusalDoesNot pins applySessionConfig's
// distinction from both sides: a bridge not yet started is a chat with no session and persists, a
// session refusal does not.
func TestSessionConfig_ColdSpawnPersistsAndASessionRefusalDoesNot(t *testing.T) {
	tests := map[string]struct {
		callErr     error
		wantStatus  int
		wantApplied bool
	}{
		"a cold-spawning bridge persists": {
			callErr:     fmt.Errorf("write frame: %w", marotte.ErrBridgeNotStarted),
			wantApplied: true,
		},
		"a session refusal is reported and persists nothing": {
			callErr:    errors.New("-32602 effortLevel is not available for this model"),
			wantStatus: http.StatusBadGateway,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Run("set_mode", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})

				_, err := cmdSetMode(t.Context(), host, host, host, host, setModeReq(t, "c1", "spec"))

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.CurrentModeID == "spec"; got != test.wantApplied {
					t.Errorf("mode persisted = %v, want %v", got, test.wantApplied)
				}
			})

			t.Run("set_effort", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})
				payload, err := json.Marshal(marotte.SetEffortCommand{Level: marotte.EffortMax})
				if err != nil {
					t.Fatalf("marshal set_effort payload: %v", err)
				}
				cmd := &marotte.ClientCommand{Type: marotte.CmdSetEffort, ChatID: "c1", Payload: payload}

				_, err = cmdSetEffort(t.Context(), host, host, host, Workspace{}, host, chatlock.New(), cmd)

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.Effort == string(marotte.EffortMax); got != test.wantApplied {
					t.Errorf("effort persisted = %v, want %v", got, test.wantApplied)
				}
			})

			t.Run("set_supervised_mode", func(t *testing.T) {
				store := testsupport.NewInMemoryChatStore()
				seedEmptyChat(t, store, "c1")
				host := newBridgeHost(store, &recordingBridge{callErr: test.callErr})

				_, err := cmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", true))

				assertConfigOutcome(t, err, test.wantStatus)
				c, ok := store.Get(t.Context(), "c1")
				if got := ok && c.SupervisedMode; got != test.wantApplied {
					t.Errorf("supervised persisted = %v, want %v", got, test.wantApplied)
				}
			})
		})
	}
}

// assertConfigOutcome grades a config command's error against the status it owes:
// wantStatus 0 means the command must have succeeded.
func assertConfigOutcome(t *testing.T, err error, wantStatus int) {
	t.Helper()
	if wantStatus == 0 {
		if err != nil {
			t.Fatalf("a cold-spawning bridge answered %v; the pick is lost and the pill rolls back", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("a session refusal answered no error, so the record now claims a setting the session refused")
	}
	if got := statusOf(err); got != wantStatus {
		t.Errorf("status = %d, want %d", got, wantStatus)
	}
}
