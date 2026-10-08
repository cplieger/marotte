package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
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
			_, err := h.coord.OpenBridge(t.Context(), "c1", "")
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

func TestOpenBridge_AHandEditReachesEveryOtherLiveProcessFromOneOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		a, b := fakeOf(openChat(t, h, "c1")), fakeOf(openChat(t, h, "c2"))
		utility := acquireUtility(t, h)
		synctest.Wait()
		rewriteConfigByHand(t, configDir, `{"agent_ignore_files":[".gitignore"],"terminal_command_timeout_ms":300000}`)
		wantTerminal := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
		wantIgnore := []string{jsonOf(t, map[string]any{marotte.ParamIgnoreFiles: []string{settings.AgentIgnoreFloor, ".gitignore"}})}

		openChat(t, h, "c1")
		if got := a.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, wantTerminal) {
			t.Errorf("the opened chat holds timeout frames %v when its open returned, want %v", got, wantTerminal)
		}
		synctest.Wait()

		if got := b.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, wantTerminal) {
			t.Errorf("after c1's open c2 holds timeout frames %v, want %v: a hand edit reaches every chat from one open", got, wantTerminal)
		}
		if got := utility.notified(marotte.MethodPolicyIgnoreFilesChanged); !slices.Equal(got, wantIgnore) {
			t.Errorf("after c1's open the utility process holds ignore frames %v, want %v", got, wantIgnore)
		}
		if got := a.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, wantTerminal) {
			t.Errorf("the background pass sent the opened chat timeout frames %v, want only its open's %v", got, wantTerminal)
		}
	})
}

func TestOpenBridge_TwoConcurrentOpensOfOneChatPushOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		br := fakeOf(openChat(t, h, "c1"))
		gate := make(chan struct{})
		br.blockNotify(marotte.MethodTerminalSettingsChanged, gate)
		rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)

		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				if _, err := h.coord.OpenBridge(context.Background(), "c1", ""); err != nil {
					t.Errorf("OpenBridge: %v", err)
				}
			})
		}
		synctest.Wait()
		close(gate)
		wg.Wait()

		if got := br.notified(marotte.MethodTerminalSettingsChanged); len(got) != 1 {
			t.Errorf("two concurrent opens after one hand edit pushed %d timeout frames, want 1", len(got))
		}
	})
}

func TestOpenBridge_AWedgedPushToOneChatDoesNotDelayOpeningAnother(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		wedged := fakeOf(openChat(t, h, "c1"))
		other := fakeOf(openChat(t, h, "c2"))
		gate := make(chan struct{})
		wedged.blockNotify(marotte.MethodTerminalSettingsChanged, gate)
		rewriteConfigByHand(t, configDir, `{"terminal_command_timeout_ms":300000}`)
		wedgedDone := make(chan struct{})
		go func() {
			defer close(wedgedDone)
			_, _ = h.coord.OpenBridge(context.Background(), "c1", "")
		}()
		synctest.Wait()

		otherDone := make(chan struct{})
		go func() {
			defer close(otherDone)
			_, _ = h.coord.OpenBridge(context.Background(), "c2", "")
		}()
		synctest.Wait()
		select {
		case <-otherDone:
		default:
			t.Error("opening c2 waited on the push wedged in c1's process")
		}
		close(gate)
		<-wedgedDone
		<-otherDone
		if got := other.notified(marotte.MethodTerminalSettingsChanged); len(got) != 1 {
			t.Errorf("c2's open pushed %d timeout frames to its own process, want 1", len(got))
		}
	})
}

type editAfterSpawn struct {
	*fakeBridge
	edit func()
	once sync.Once
}

func (b *editAfterSpawn) Start(ctx context.Context, opts *marotte.StartOpts) error {
	err := b.fakeBridge.Start(ctx, opts)
	if err == nil && opts.DisableSessionTitles {
		b.once.Do(b.edit)
	}
	return err
}

func TestRunBridge_AnEditLandingDuringItsSpawnReachesItOnceStarted(t *testing.T) {
	for _, tc := range []struct {
		replies map[string]json.RawMessage
		start   func(t *testing.T, h *Runtime)
		name    string
	}{
		{
			name: "launch",
			replies: map[string]json.RawMessage{
				methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
				methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
				methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
				methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
			},
			start: func(t *testing.T, h *Runtime) {
				if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
					t.Fatalf("Launch: %v", err)
				}
			},
		},
		{
			name: "rehost",
			start: func(t *testing.T, h *Runtime) {
				host, err := h.runs.hostRun(t.Context(), "wf_1")
				if err != nil {
					t.Fatalf("hostRun: %v", err)
				}
				host.release(nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			br := &editAfterSpawn{fakeBridge: newFakeBridge()}
			h, _ := newContentCollectionHub(t, func() ACPBridge { return br })
			br.edit = func() { writeSetting(t, h.lifecycle.configDir, settings.KeyTerminalCommandTimeoutMs, 300000) }
			br.callResults = tc.replies
			if br.callResults == nil {
				br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t, map[string]any{
					"workflowId": "wf_1", "name": "publish", "status": "paused", "parentSessionId": "sess_old_run_bridge",
				})}
			}

			tc.start(t, h)

			want := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
			if got := br.notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, want) {
				t.Errorf("the run bridge holds %v once started, want %v: a write during its spawn found no bridge to push to", got, want)
			}
		})
	}
}

func TestOpenBridge_AHandEditReachesALiveRunBridgeFromAChatOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replies := map[string]json.RawMessage{
			methodKiroWorkflowListRecipes: json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`),
			methodKiroWorkflowNew:         json.RawMessage(`{"workflowId":"wf_9"}`),
			methodKiroWorkflowInvoke:      json.RawMessage(`{}`),
			methodKiroWorkflowList:        json.RawMessage(`{"runs":[]}`),
		}
		h, _ := newContentCollectionHub(t, func() ACPBridge {
			br := newFakeBridge()
			br.callResults = replies
			return br
		})
		if _, _, err := h.runs.Launch(t.Context(), "bundled://publish", nil); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		run := h.bridge.mgr.get(runChatID("wf_9"))
		if run == nil {
			t.Fatal("Setup: the launch left no run bridge")
		}
		openChat(t, h, "c1")
		synctest.Wait()
		writeSetting(t, h.lifecycle.configDir, settings.KeyTerminalCommandTimeoutMs, 300000)

		openChat(t, h, "c1")
		synctest.Wait()

		want := []string{jsonOf(t, marotte.TerminalSettingsParams(300000))}
		if got := fakeOf(run).notified(marotte.MethodTerminalSettingsChanged); !slices.Equal(got, want) {
			t.Errorf("after a chat open the run bridge holds timeout frames %v, want %v: a run bridge receives no message of its own", got, want)
		}
	})
}

func TestOpenBridge_TheBackgroundPassLeavesTheOpenedChatToItsOwnOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, configDir := reopenFixture(t)
		br := fakeOf(openChat(t, h, "c1"))
		synctest.Wait()
		br.setCallErr(marotte.MethodSetConfigOption, errors.New("broken pipe"))
		rewriteConfigByHand(t, configDir, `{"content_collection_enabled":true}`)

		openChat(t, h, "c1")
		synctest.Wait()

		if got := contentCollectionWrites(br); len(got) != 1 {
			t.Errorf("content-collection asserts on c1 since the edit = %v, want its open's alone: the background pass "+
				"retried the push the open had just made, waiting out a second timeout on a wedged pipe", got)
		}
	})
}

func TestLiveFanout_CoalescedRequestsSkipOnlyWhatEveryOneSynced(t *testing.T) {
	c1, c2 := liveSurface{chat: "c1"}, liveSurface{chat: "c2"}
	for _, tc := range []struct {
		name     string
		requests []liveSurface
		want     liveSurface
	}{
		{name: "one_chat_twice", requests: []liveSurface{c1, c1}, want: c1},
		{name: "two_chats", requests: []liveSurface{c1, c2}, want: liveSurface{}},
		{name: "a_chat_then_the_utility", requests: []liveSurface{c1, {utility: true}}, want: liveSurface{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f liveFanout
			if !f.request(liveSurface{}) {
				t.Fatal("the first request on an idle fan-out did not start the runner")
			}
			if _, ok := f.takeOrStop(); !ok {
				t.Fatal("Setup: the runner found no pass queued")
			}
			for _, r := range tc.requests {
				if f.request(r) {
					t.Fatalf("request(%+v) during a pass asked for a second runner", r)
				}
			}
			if got, ok := f.takeOrStop(); !ok || got != tc.want {
				t.Errorf("after requests %+v the coalesced pass skips %+v (queued %t), want %+v", tc.requests, got, ok, tc.want)
			}
			if _, ok := f.takeOrStop(); ok {
				t.Error("a second pass ran for requests one pass covered")
			}
			if !f.request(c1) {
				t.Error("a request after the runner stopped did not start a new one")
			}
		})
	}
}
