package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

func writeAgentSettings(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(body), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestAgentFeatures_TakesValidValuesAndDefaultsTheRest(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{
		"spec_planning": "quick",
		"spec_planning_ask_first": true,
		"inline_agents_enabled": true,
		"work_validation": "off",
		"cloudformation_safety_check": "enforce",
		"terminal_command_timeout_ms": 500
	}`)

	got := agentFeatures(t.Context(), dir, nil)

	want := marotte.AgentFeatures{
		SpecPlan:             settings.SpecPlanningQuick,
		SpecAskClarification: true,
		InlineAgents:         true,
		WorkValidation:       settings.FeatureOff,
		// Absent: workflows default on.
		Workflows: true,
		// Invalid values fall back, so KAS's own experiment and 120 s decide.
	}
	if got != want {
		t.Errorf("agentFeatures() = %+v, want %+v", got, want)
	}
}

func TestAgentFeatures_WorkflowsFollowsTheToggle(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{}`, true},
		{`{"workflows_enabled": false}`, false},
		{`{"workflows_enabled": "off"}`, true},
	} {
		dir := t.TempDir()
		writeAgentSettings(t, dir, tc.body)
		if got := agentFeatures(t.Context(), dir, nil).Workflows; got != tc.want {
			t.Errorf("agentFeatures(%s).Workflows = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestAgentFeatures_SpecPlanningOffSendsNoWorkflow(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"spec_planning": "off"}`)

	if got := agentFeatures(t.Context(), dir, nil).SpecPlan; got != "" {
		t.Errorf("agentFeatures().SpecPlan = %q, want empty for off", got)
	}
}

// timeoutSpawnProbe parks in Start like initialize, then reads StartOpts.TerminalTimeout where the
// post-initialize re-read would; Notify refuses until Start returns.
type timeoutSpawnProbe struct {
	*fakeBridge
	arrival chan struct{}
	once    sync.Once
	release chan struct{}

	mu       sync.Mutex
	started  bool
	resolved int
	refused  int
}

func (b *timeoutSpawnProbe) Start(ctx context.Context, opts *marotte.StartOpts) error {
	b.once.Do(func() { close(b.arrival) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	first := !b.started
	b.started = true
	b.mu.Unlock()
	if first && opts.TerminalTimeout != nil {
		ms := opts.TerminalTimeout(ctx)
		b.mu.Lock()
		b.resolved = ms
		b.mu.Unlock()
	}
	return b.fakeBridge.Start(ctx, opts)
}

func (b *timeoutSpawnProbe) Notify(_ context.Context, _ string, _ any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.started {
		b.refused++
		return errProbeNotStarted
	}
	return nil
}

// A mid-spawn timeout save reaches the bridge through its own resolver: the live push is refused.
func TestSpawnTerminalTimeout_ConcurrentSaveReachesASpawningBridge(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 30000}`)

	probe := &timeoutSpawnProbe{fakeBridge: newFakeBridge(), arrival: make(chan struct{}), release: make(chan struct{})}
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return probe }, cs, WithConfigDir(dir))
	cs.wire(h)
	released := sync.OnceFunc(func() { close(probe.release) })
	t.Cleanup(func() { released(); shutdownHub(t, h) })

	ctx := t.Context()
	if _, err := cs.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true }); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	opened := make(chan error, 1)
	go func() {
		_, err := h.coord.OpenBridge(ctx, "c1", "")
		opened <- err
	}()

	<-probe.arrival
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 300000}`)
	h.PushTerminalSettings(ctx)

	released()
	if err := <-opened; err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.refused != 1 {
		t.Errorf("pushes refused mid-spawn = %d, want 1", probe.refused)
	}
	if probe.resolved != 300000 {
		t.Errorf("timeout the spawn resolved = %d, want 300000: StartOpts.TerminalTimeout must read at send time", probe.resolved)
	}
}

// terminalProbeBridge records the terminal-settings frames one bridge received.
type terminalProbeBridge struct {
	*fakeBridge

	mu     sync.Mutex
	frames []string
}

func (b *terminalProbeBridge) Notify(ctx context.Context, method string, params any) error {
	if method != marotte.MethodTerminalSettingsChanged {
		return b.fakeBridge.Notify(ctx, method, params)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, string(raw))
	return nil
}

func (b *terminalProbeBridge) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.frames...)
}

func TestPushTerminalSettings_ReachesEveryLiveBridge(t *testing.T) {
	dir := t.TempDir()
	writeAgentSettings(t, dir, `{"terminal_command_timeout_ms": 30000}`)
	var mu sync.Mutex
	var made []*terminalProbeBridge
	factory := func() ACPBridge {
		p := &terminalProbeBridge{fakeBridge: newFakeBridge()}
		mu.Lock()
		made = append(made, p)
		mu.Unlock()
		return p
	}
	cs := newTestChatStore()
	h := New(context.Background(), t.TempDir(), factory, cs, WithConfigDir(dir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	for _, id := range []marotte.ChatID{"c1", "c2"} {
		if _, err := cs.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool { c.Name = string(id); return true }); err != nil {
			t.Fatalf("seed chat %s: %v", id, err)
		}
		if _, err := h.coord.OpenBridge(t.Context(), id, ""); err != nil {
			t.Fatalf("OpenBridge %s: %v", id, err)
		}
	}

	h.PushTerminalSettings(t.Context())

	mu.Lock()
	defer mu.Unlock()
	if len(made) < 2 {
		t.Fatalf("bridges made = %d, want at least 2", len(made))
	}
	want := `{"terminal":{"commandTimeoutMs":30000,"enabled":true}}`
	reached := 0
	for _, p := range made {
		frames := p.sent()
		if len(frames) == 0 {
			continue
		}
		reached++
		if len(frames) != 1 || frames[0] != want {
			t.Errorf("bridge frames = %v, want exactly [%s]", frames, want)
		}
	}
	if reached != 2 {
		t.Errorf("bridges reached = %d, want both live chat bridges", reached)
	}
}
