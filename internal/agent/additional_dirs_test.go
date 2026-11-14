package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/settings"
)

// agentRootsFixture is a canonical workspace, config directory and additional directory, with
// the additional directory granted in config.json.
type agentRootsFixture struct {
	work, config, extra string
}

func newAgentRootsFixture(t *testing.T) agentRootsFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := agentRootsFixture{
		work:   filepath.Join(base, "workspace"),
		config: filepath.Join(base, "config"),
		extra:  filepath.Join(base, "data"),
	}
	for _, d := range []string{f.work, f.config, f.extra} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeSetting(t, f.config, settings.KeyAdditionalDirectories, []string{f.extra})
	return f
}

func TestSessionDoors_CarryTheGrantedAdditionalDirectories(t *testing.T) {
	for _, tc := range []struct{ name, session string }{
		{"a new chat", ""},
		{"a resumed chat", "sess_resume_probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentRootsFixture(t)
			cs := newTestChatStore()
			h := newRuntimeOn(context.Background(), f.work,
				fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, WithConfigDir(f.config))
			cs.wire(h)
			t.Cleanup(func() { shutdownHub(t, h) })
			_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
				c.Name = "A"
				c.ACPSessionID = tc.session
				return true
			})
			sb, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			opts := fakeOf(sb).lastSessionOpts()
			if opts == nil || opts.SessionID != tc.session {
				t.Fatalf("the door was %+v, want one for session %q", opts, tc.session)
			}
			if !slices.Equal(opts.AdditionalDirectories, []string{f.extra}) {
				t.Errorf("the chat door carried %v, want [%s]", opts.AdditionalDirectories, f.extra)
			}
		})
	}

	t.Run("a run's host session", func(t *testing.T) {
		f := newAgentRootsFixture(t)
		h, _, br := newTestHubIn(f.work, WithConfigDir(f.config))
		t.Cleanup(func() { shutdownHub(t, h) })
		leaseParentless(t, h, "wf_1")
		// A person's resume writes only once its read finds the run paused (runTransitions).
		br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", ""))
		if err := h.runs.resume(t.Context(), "wf_1", callerPage); err != nil {
			t.Fatalf("resume: %v", err)
		}
		opts := br.lastSessionOpts()
		if opts == nil || opts.Role != marotte.SessionRoleHost {
			t.Fatalf("the door was %+v, want a host session's", opts)
		}
		if !slices.Equal(opts.AdditionalDirectories, []string{f.extra}) {
			t.Errorf("the host door carried %v, want [%s]", opts.AdditionalDirectories, f.extra)
		}
	})

	t.Run("an observer pass", func(t *testing.T) {
		f := newAgentRootsFixture(t)
		observer := newFakeBridge()
		src := fakeEngineOf(func() engineSession { return newFakeBridge() }, nil)
		src.observer = func() engineSession { return observer }
		cs := newTestChatStore()
		h := newRuntimeOn(context.Background(), f.work, src, cs, WithConfigDir(f.config))
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		if err := h.observe(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
			t.Fatalf("observer pass: %v", err)
		}
		opts := observer.lastSessionOpts()
		if opts == nil || !slices.Equal(opts.AdditionalDirectories, []string{f.extra}) {
			t.Errorf("the observer door was %+v, want additional directories [%s]", opts, f.extra)
		}
	})
}

// Removing the setting changes the chat door's fingerprint, so a resident chat reloads without
// the directory at its next open, and every door after the removal carries none.
func TestRemovingTheSetting_RevokesAccessForNewSessions_AtTheDoor(t *testing.T) {
	f := newAgentRootsFixture(t)
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), f.work,
		fakeEngineOf(func() engineSession { return newFakeBridge() }, nil), cs, WithConfigDir(f.config))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })

	granted, ok := h.sessionFingerprint(t.Context())
	if !ok {
		t.Fatal("no fingerprint with the directory granted")
	}
	writeSetting(t, f.config, settings.KeyAdditionalDirectories, []string{})
	revoked, ok := h.sessionFingerprint(t.Context())
	if !ok || revoked == granted {
		t.Error("removing the directory left the session fingerprint unchanged, so a resident chat never reloads without it")
	}
	if opts := h.chatSessionOpts(t.Context(), &marotte.Chat{}, &sessionAsk{}, nil); len(opts.AdditionalDirectories) != 0 {
		t.Errorf("a door after the removal carried %v, want none", opts.AdditionalDirectories)
	}
}

// Every door opened while config.json cannot be read carries the last readable profile and directories.
func TestUnreadableConfig_EverySessionDoorOpensOnTheLastReadableSettings(t *testing.T) {
	want, _ := policyfile.ProfileFor(policyfile.ProfileUnrestricted)
	// boot builds the runtime over a granted directory and the unrestricted profile, then breaks config.json.
	boot := func(t *testing.T, src *fakeEngineSource) (*Runtime, agentRootsFixture) {
		t.Helper()
		f := newAgentRootsFixture(t)
		writeSetting(t, f.config, settings.KeySecurityProfile, policyfile.ProfileUnrestricted)
		cs := newTestChatStore()
		h := newRuntimeOn(context.Background(), f.work, src, cs, WithConfigDir(f.config))
		cs.wire(h)
		t.Cleanup(func() { shutdownHub(t, h) })
		settleBoot(t, h)
		_, _ = cs.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.Name = "A"; return true })
		rewriteConfigByHand(t, f.config, `{"security_profile":`)
		return h, f
	}
	checkDoor := func(t *testing.T, so *marotte.SessionOpts, f agentRootsFixture, door string) {
		t.Helper()
		if so == nil {
			t.Fatalf("no %s door was opened", door)
		}
		if !slices.Equal(so.Presets, want.Presets) {
			t.Errorf("the %s door carried presets %v, want the last readable profile's %v", door, so.Presets, want.Presets)
		}
		if !slices.Equal(so.AdditionalDirectories, []string{f.extra}) {
			t.Errorf("the %s door carried directories %v, want [%s]", door, so.AdditionalDirectories, f.extra)
		}
	}
	checkLease := func(t *testing.T, h *Runtime, wf string) {
		t.Helper()
		l, held := h.runs.lease(wf)
		if !held {
			t.Fatalf("no lease for %s", wf)
		}
		if presets, err := l.HostPresets(); err != nil || !slices.Equal(presets, want.Presets) {
			t.Errorf("the lease's host presets = %v, %v, want the last readable profile's %v", presets, err, want.Presets)
		}
	}
	shared := func() (*fakeEngineSource, *fakeBridge) {
		br := newFakeBridge()
		return fakeEngineOf(func() engineSession { return br }, br), br
	}

	for _, tc := range []struct{ name, session string }{{"a_new_chat", ""}, {"a_resumed_chat", "sess_resume_probe"}} {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := shared()
			h, f := boot(t, src)
			_, _ = h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool { c.ACPSessionID = tc.session; return true })
			sb, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			checkDoor(t, fakeOf(sb).lastSessionOpts(), f, "chat")
		})
	}

	t.Run("a_parentless_launch", func(t *testing.T) {
		src, br := shared()
		h, f := boot(t, src)
		br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`))
		br.setCallResult(methodKiroWorkflowList, json.RawMessage(`{"runs":[]}`))
		br.setCallResult(methodKiroWorkflowNew, json.RawMessage(`{"workflowId":"wf_9"}`))
		br.setCallResult(methodKiroWorkflowInvoke, json.RawMessage(`{}`))
		if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
			t.Fatalf("Launch: %v", err)
		}
		checkDoor(t, br.lastSessionOpts(), f, "host")
		checkLease(t, h, "wf_9")
	})

	t.Run("a_host_reload", func(t *testing.T) {
		src, br := shared()
		h, f := boot(t, src)
		leaseParentless(t, h, "wf_1", want.Presets...)
		br.setCallResult(methodKiroWorkflowInspect, inspectPaused(t, "wf_1", ""))
		if err := h.runs.resume(t.Context(), "wf_1", callerPage); err != nil {
			t.Fatalf("resume: %v", err)
		}
		so := br.lastSessionOpts()
		if so == nil || !slices.Equal(so.AdditionalDirectories, []string{f.extra}) {
			t.Errorf("the host reload's door = %+v, want directories [%s]", so, f.extra)
		}
	})

	t.Run("a_retry_lease", func(t *testing.T) {
		src, _ := shared()
		h, _ := boot(t, src)
		h.runs.grantRetryLease(t.Context(), "wf_1", &runOrigin{recipe: "nightly"}, nil)
		checkLease(t, h, "wf_1")
	})

	t.Run("an_observer_pass", func(t *testing.T) {
		src, _ := shared()
		observer := newFakeBridge()
		src.observer = func() engineSession { return observer }
		h, f := boot(t, src)
		if err := h.observe(t.Context(), func(context.Context, engineSession) error { return nil }); err != nil {
			t.Fatalf("observer pass: %v", err)
		}
		checkDoor(t, observer.lastSessionOpts(), f, "observer")
	})
}
