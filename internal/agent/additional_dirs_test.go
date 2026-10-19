package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
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
			sb, err := h.coord.openBridge(t.Context(), "c1", "")
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
		if err := h.runs.resume(t.Context(), "wf_1"); err != nil {
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
