package command

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/keylock"
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

	_, err := cmdSetMode(t.Context(), host, host, host, host, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", "spec"))

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

		if _, err := cmdSetMode(t.Context(), spy, spy, spy, spy, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", "spec")); err != nil {
			t.Fatalf("first pick: %v", err)
		}
		before := len(spy.events)
		if _, err := cmdSetMode(t.Context(), spy, spy, spy, spy, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", "spec")); err != nil {
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

		if _, err := cmdSetMode(t.Context(), host, host, host, host, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", "spec")); err != nil {
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

// TestSessionConfig_ColdOpenPersistsAndASessionRefusalDoesNot pins applySessionConfig's
// distinction from both sides: a bridge not yet started is a chat with no session and persists, a
// session refusal does not.
func TestSessionConfig_ColdOpenPersistsAndASessionRefusalDoesNot(t *testing.T) {
	tests := map[string]struct {
		callErr     error
		wantStatus  int
		wantApplied bool
	}{
		"a bridge opening its session persists": {
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

				_, err := cmdSetMode(t.Context(), host, host, host, host, keylock.New(cmp.Compare[marotte.ChatID]), setModeReq(t, "c1", "spec"))

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

				_, err = cmdSetEffort(t.Context(), host, host, host, Workspace{}, host, keylock.New(cmp.Compare[marotte.ChatID]), cmd)

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

				_, err := cmdSetSupervisedMode(t.Context(), host, host, keylock.New(cmp.Compare[marotte.ChatID]), supervisedReq(t, "c1", true))

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
			t.Fatalf("a bridge opening its session answered %v; the pick is lost and the pill rolls back", err)
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

// gatedFirstCall parks the first session-config call until release closes, as a stalled kiro-cli does, and
// accepts every call. sent is the wire order, the order kiro-cli applies the changes in.
type gatedFirstCall struct {
	*recordingBridge
	release chan struct{}
	mu      sync.Mutex
	sent    []string
}

func (b *gatedFirstCall) Call(_ context.Context, _ string, params any) (*marotte.RPCResponse, error) {
	m, _ := params.(map[string]any)
	value := m["modeId"]
	if value == nil {
		value = m["value"]
	}
	b.mu.Lock()
	first := len(b.sent) == 0
	b.sent = append(b.sent, fmt.Sprint(value))
	b.mu.Unlock()
	if first {
		<-b.release
	}
	return &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, nil
}

func (b *gatedFirstCall) PromptAt(ctx context.Context, params any, commit func() error) (*marotte.RPCResponse, uint64, error) {
	return promptViaCallAt(ctx, b, params, commit)
}

func (b *gatedFirstCall) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

// lateChangeThenAnother parks change(first) inside its engine call, runs change(second) on the same chat while it
// is parked, as a client does once the first answered still_applying, then lets the engine answer. It returns the
// calls in wire order and the chat record both changes left.
func lateChangeThenAnother(
	t *testing.T,
	change func(ctx context.Context, host hostDouble, locks *keylock.Set[marotte.ChatID], first bool) error,
) (sent []string, rec *marotte.Chat) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedEmptyChat(t, store, "c1")
		b := &gatedFirstCall{recordingBridge: &recordingBridge{sessionID: "s"}, release: make(chan struct{})}
		host := newBridgeHost(store, b)
		locks := keylock.New(cmp.Compare[marotte.ChatID])

		var wg sync.WaitGroup
		for _, first := range []bool{true, false} {
			wg.Go(func() {
				if err := change(t.Context(), host, locks, first); err != nil {
					t.Errorf("change (first %v): %v", first, err)
				}
			})
			synctest.Wait()
		}
		close(b.release)
		wg.Wait()

		sent = b.sent
		rec, _ = store.Get(t.Context(), "c1")
	})
	return sent, rec
}

func TestCmdSetMode_ALateModeNeverOverwritesTheNextPick(t *testing.T) {
	sent, rec := lateChangeThenAnother(t, func(ctx context.Context, host hostDouble, locks *keylock.Set[marotte.ChatID], first bool) error {
		mode := "vibe"
		if first {
			mode = "spec"
		}
		_, err := cmdSetMode(ctx, host, host, host, host, locks, setModeReq(t, "c1", mode))
		return err
	})

	if want := []string{"spec", "vibe"}; !slices.Equal(sent, want) {
		t.Errorf("set_mode calls = %v, want %v", sent, want)
	}
	if rec.CurrentModeID != "vibe" {
		t.Errorf("CurrentModeID = %q, want vibe, the mode kiro-cli applied last", rec.CurrentModeID)
	}
}

func TestCmdSetSupervisedMode_ALateToggleNeverOverwritesTheNextOne(t *testing.T) {
	sent, rec := lateChangeThenAnother(t, func(ctx context.Context, host hostDouble, locks *keylock.Set[marotte.ChatID], first bool) error {
		_, err := cmdSetSupervisedMode(ctx, host, host, locks, supervisedReq(t, "c1", first))
		return err
	})

	want := []string{marotte.ConfigValueAutopilotOff, marotte.ConfigValueAutopilotOn}
	if !slices.Equal(sent, want) {
		t.Errorf("autopilot calls = %v, want %v", sent, want)
	}
	if rec.SupervisedMode {
		t.Error("SupervisedMode = true, want false, the toggle kiro-cli applied last")
	}
}
