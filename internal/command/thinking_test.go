package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/chatlock"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// configLog records the configId and value of every config-option call, in order,
// so a test can say which option went out first.
type configLog struct {
	*recordingBridge
	sent []string
}

func (b *configLog) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if m, ok := params.(map[string]any); ok {
		b.sent = append(b.sent, fmt.Sprintf("%v=%v", m["configId"], m["value"]))
	}
	return b.recordingBridge.Call(ctx, method, params)
}

func (b *configLog) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

func thinkingReq(t *testing.T, chatID marotte.ChatID, enabled bool) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdSetThinking, ChatID: chatID, Payload: payload}
}

func seedThinking(t *testing.T, store chatStore, id marotte.ChatID, choice, active string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "a chat"
		c.Model = "opus-5"
		c.Thinking = choice
		c.ThinkingActive = active
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestCmdSetThinking_SendsTheStringValueAndPersistsTheChoice(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", "", marotte.ThinkingOn)
	b := &configLog{recordingBridge: &recordingBridge{result: map[string]any{}, sessionID: "s"}}
	host := newBridgeHost(store, b)

	_, err := cmdSetThinking(t.Context(), host, host, chatlock.New(), thinkingReq(t, "c1", false))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if want := []string{marotte.ConfigOptionThinking + "=off"}; !slices.Equal(b.sent, want) {
		t.Errorf("config calls = %v, want %v", b.sent, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Thinking != marotte.ThinkingOff {
		t.Errorf("Thinking = %q, want %q", c.Thinking, marotte.ThinkingOff)
	}
}

func TestCmdSetThinking_ARefusedLiveSwitchIsNotPersisted(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", "", "")
	host := newBridgeHost(store, &recordingBridge{callErr: errors.New("no such config option"), sessionID: "s"})

	_, err := cmdSetThinking(t.Context(), host, host, chatlock.New(), thinkingReq(t, "c1", false))

	if statusOf(err) != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", statusOf(err))
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Thinking != "" {
		t.Errorf("Thinking = %q, want it unset after a refused switch", c.Thinking)
	}
}

func TestCmdSetThinking_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newBridgeHost(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})
	cmd := &marotte.ClientCommand{Type: marotte.CmdSetThinking, ChatID: "c1", Payload: json.RawMessage(`{"enabled":"no"}`)}

	_, err := cmdSetThinking(t.Context(), host, host, chatlock.New(), cmd)

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// A tier pick is the slider's way back from its Off stop, so it turns thinking on,
// and BEFORE the effort: KAS caps a high tier while thinking is off.
func TestCmdSetEffort_TurnsThinkingBackOnBeforeTheEffort(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", marotte.ThinkingOff, marotte.ThinkingOff)
	b := &configLog{recordingBridge: &recordingBridge{result: map[string]any{}, sessionID: "s"}}
	host := newBridgeHost(store, b)

	err := setEffort(t, host, "", "c1", "max")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	want := []string{marotte.ConfigOptionThinking + "=on", marotte.ConfigOptionEffort + "=max"}
	if !slices.Equal(b.sent, want) {
		t.Errorf("config calls = %v, want %v", b.sent, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Thinking != marotte.ThinkingOn {
		t.Errorf("Thinking = %q, want %q after a tier pick", c.Thinking, marotte.ThinkingOn)
	}
}

// A chat that never chose, on a model whose own default is thinking off, is
// sitting on the Off stop, so a tier pick turns thinking on too.
func TestCmdSetEffort_TurnsThinkingOnForADefaultOffModel(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", "", "")
	b := &configLog{recordingBridge: &recordingBridge{result: map[string]any{}, sessionID: "s"}}
	host := idleHost(store, b, b)
	host.thinkingDefaultOff = map[string]bool{"opus-5": true}

	err := setEffort(t, host, "", "c1", "max")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	want := []string{marotte.ConfigOptionThinking + "=on", marotte.ConfigOptionEffort + "=max"}
	if !slices.Equal(b.sent, want) {
		t.Errorf("config calls = %v, want %v", b.sent, want)
	}
}

type refusingEffort struct{ *configLog }

func (b *refusingEffort) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	resp, err := b.configLog.Call(ctx, method, params)
	if m, ok := params.(map[string]any); ok && m["configId"] == marotte.ConfigOptionEffort {
		return nil, errors.New("no such config option")
	}
	return resp, err
}

func (b *refusingEffort) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

func TestCmdSetEffort_ARefusedTierPutsThinkingBackOff(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", marotte.ThinkingOff, marotte.ThinkingOff)
	b := &refusingEffort{&configLog{recordingBridge: &recordingBridge{result: map[string]any{}, sessionID: "s"}}}
	host := newBridgeHost(store, b)

	err := setEffort(t, host, "", "c1", "max")

	if statusOf(err) != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", statusOf(err))
	}
	want := []string{
		marotte.ConfigOptionThinking + "=on",
		marotte.ConfigOptionEffort + "=max",
		marotte.ConfigOptionThinking + "=off",
	}
	if !slices.Equal(b.sent, want) {
		t.Errorf("config calls = %v, want %v", b.sent, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Thinking != marotte.ThinkingOff || c.Effort != "" {
		t.Errorf("record = (Thinking %q, Effort %q), want (%q, unset)", c.Thinking, c.Effort, marotte.ThinkingOff)
	}
}

// gatedEffort parks a "max" effort call until release closes, then refuses it; every other
// config call succeeds. Safe for the two concurrent commands a test drives through it.
type gatedEffort struct {
	*recordingBridge
	release chan struct{}
	sent    []string
	mu      sync.Mutex
}

func (b *gatedEffort) Call(_ context.Context, _ string, params any) (*marotte.RPCResponse, error) {
	m, _ := params.(map[string]any)
	b.mu.Lock()
	b.sent = append(b.sent, fmt.Sprintf("%v=%v", m["configId"], m["value"]))
	b.mu.Unlock()
	if m["configId"] == marotte.ConfigOptionEffort && m["value"] == "max" {
		<-b.release
		return nil, errors.New("no such config option")
	}
	return &marotte.RPCResponse{Result: json.RawMessage(`{}`)}, nil
}

func (b *gatedEffort) CallAt(ctx context.Context, method string, params any) (*marotte.RPCResponse, uint64, error) {
	resp, err := b.Call(ctx, method, params)
	return resp, 0, err
}

// refusedEffortRacing parks a refused "max" pick on a thinking-Off chat inside its effort call,
// starts second on the same chat while it is parked, then lets the refusal through. It returns the
// config calls in wire order and the chat record both commands left.
func refusedEffortRacing(
	t *testing.T, second func(ctx context.Context, host hostDouble, locks *chatlock.Set) (any, error),
) (sent []string, rec *marotte.Chat) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		seedThinking(t, store, "c1", marotte.ThinkingOff, marotte.ThinkingOff)
		b := &gatedEffort{
			recordingBridge: &recordingBridge{sessionID: "s"},
			release:         make(chan struct{}),
		}
		host := newBridgeHost(store, b)
		locks := chatlock.New()

		refused := effortReq(t, "c1", "max")
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := cmdSetEffort(t.Context(), host, host, host, Workspace{}, host, locks,
				refused); statusOf(err) != http.StatusBadGateway {
				t.Errorf("set_effort max: status = %d, want 502", statusOf(err))
			}
		})
		synctest.Wait()
		wg.Go(func() {
			if _, err := second(t.Context(), host, locks); err != nil {
				t.Errorf("second command: %v", err)
			}
		})
		synctest.Wait()
		close(b.release)
		wg.Wait()

		sent = b.sent
		rec, _ = store.Get(t.Context(), "c1")
	})
	return sent, rec
}

func TestCmdSetEffort_ARefusedTierNeverUndoesAConcurrentAcceptedTier(t *testing.T) {
	accepted := effortReq(t, "c1", "high")
	sent, rec := refusedEffortRacing(t, func(ctx context.Context, host hostDouble, locks *chatlock.Set) (any, error) {
		return cmdSetEffort(ctx, host, host, host, Workspace{}, host, locks, accepted)
	})

	want := []string{
		marotte.ConfigOptionThinking + "=on",
		marotte.ConfigOptionEffort + "=max",
		marotte.ConfigOptionThinking + "=off",
		marotte.ConfigOptionThinking + "=on",
		marotte.ConfigOptionEffort + "=high",
	}
	if !slices.Equal(sent, want) {
		t.Errorf("config calls = %v, want %v", sent, want)
	}
	if rec.Thinking != marotte.ThinkingOn || rec.Effort != "high" {
		t.Errorf("record = (Thinking %q, Effort %q), want (%q, high)", rec.Thinking, rec.Effort, marotte.ThinkingOn)
	}
}

func TestCmdSetEffort_ARefusedTierNeverUndoesAConcurrentThinkingOn(t *testing.T) {
	on := thinkingReq(t, "c1", true)
	sent, rec := refusedEffortRacing(t, func(ctx context.Context, host hostDouble, locks *chatlock.Set) (any, error) {
		return cmdSetThinking(ctx, host, host, locks, on)
	})

	want := []string{
		marotte.ConfigOptionThinking + "=on",
		marotte.ConfigOptionEffort + "=max",
		marotte.ConfigOptionThinking + "=off",
		marotte.ConfigOptionThinking + "=on",
	}
	if !slices.Equal(sent, want) {
		t.Errorf("config calls = %v, want %v", sent, want)
	}
	if rec.Thinking != marotte.ThinkingOn {
		t.Errorf("record Thinking = %q, want %q", rec.Thinking, marotte.ThinkingOn)
	}
}

func TestCmdSetEffort_LeavesThinkingAloneWhenItIsOn(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedThinking(t, store, "c1", "", marotte.ThinkingOn)
	b := &configLog{recordingBridge: &recordingBridge{result: map[string]any{}, sessionID: "s"}}
	host := newBridgeHost(store, b)

	err := setEffort(t, host, "", "c1", "high")

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if want := []string{marotte.ConfigOptionEffort + "=high"}; !slices.Equal(b.sent, want) {
		t.Errorf("config calls = %v, want %v", b.sent, want)
	}
	if c, _ := store.Get(t.Context(), "c1"); c.Thinking != "" {
		t.Errorf("Thinking = %q, want the untouched empty choice", c.Thinking)
	}
}
