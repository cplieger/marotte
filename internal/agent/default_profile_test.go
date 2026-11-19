package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/securityprofile"
	"github.com/cplieger/marotte/internal/trigger"
)

// A chat on an instance that never saved a profile starts on Approve for me; a saved profile, Trusted included,
// is what it starts on instead.
func TestChatDoor_StartsOnApproveForMeUnlessAProfileIsSaved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, saved, want string }{
		{"a_fresh_config", "", policyfile.ProfileDelegated},
		{"a_saved_trusted", policyfile.ProfileTrusted, policyfile.ProfileTrusted},
		{"an_unknown_saved_value", "no-such-profile", policyfile.ProfileDelegated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			writeProfileSetting(t, configDir, tc.saved)
			h := reopenFixtureIn(t, configDir, nil)

			sb, err := h.coord.openReleased(t.Context(), "c1", "")
			if err != nil {
				t.Fatalf("OpenBridge: %v", err)
			}
			if got, want := fakeOf(sb).lastSessionOpts().Presets, profilePresets(t, tc.want); !slices.Equal(got, want) {
				t.Errorf("the chat door sent %v, want %s's %v", got, tc.want, want)
			}
			if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.SecurityProfile != tc.want {
				t.Errorf("the chat was pinned to %q, want %q", rec.SecurityProfile, tc.want)
			}
		})
	}
}

// A parentless run's host opens on the saved profile, and on Trusted while none is saved (policyfile.DefaultRunProfile).
func TestParentlessRun_OpensOnTrustedUnlessAProfileIsSaved(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, saved, want string }{
		{"a_fresh_config", "", policyfile.ProfileTrusted},
		{"an_unknown_saved_value", "no-such-profile", policyfile.ProfileTrusted},
		{"a_saved_approve_for_me", policyfile.ProfileDelegated, policyfile.ProfileDelegated},
		{"a_saved_guarded", policyfile.ProfileGuarded, policyfile.ProfileGuarded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := profilePresets(t, tc.want)
			h, br := parentlessFixture(t, tc.saved)
			br.setCallResult(methodKiroWorkflowListRecipes, json.RawMessage(`{"recipes":[{"name":"publish","source":"bundled://publish","builtIn":true}]}`))
			br.setCallResult(methodKiroWorkflowList, json.RawMessage(`{"runs":[]}`))
			br.setCallResult(methodKiroWorkflowNew, json.RawMessage(`{"workflowId":"wf_9"}`))
			br.setCallResult(methodKiroWorkflowInvoke, json.RawMessage(`{}`))
			if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}, nil); err != nil {
				t.Fatalf("Launch: %v", err)
			}
			if so := br.lastSessionOpts(); so == nil || !slices.Equal(so.Presets, want) {
				t.Errorf("the host door was %+v, want %s's presets %v", so, tc.want, want)
			}
			h.runs.grantRetryLease(t.Context(), "wf_retry", &runOrigin{recipe: "nightly"}, nil)
			h.runs.grantEventRetryLease(t.Context(), "wf_event", &runOrigin{recipe: "nightly"}, nil,
				&trigger.RetriedEvent{RuleID: "rule-1", EventKey: "ev-1"})
			for _, wf := range []string{"wf_9", "wf_retry", "wf_event"} {
				l, held := h.runs.lease(wf)
				if !held {
					t.Fatalf("no lease for %s", wf)
				}
				if presets, err := l.HostPresets(); err != nil || !slices.Equal(presets, want) {
					t.Errorf("%s's lease recorded host presets %v, %v, want %s's %v", wf, presets, err, tc.want, want)
				}
			}
		})
	}
}

// Saving the default profile it already shows moves parentless runs onto it, since an unset value runs on Trusted.
func TestParentlessRun_FollowsASavedApproveForMe(t *testing.T) {
	t.Parallel()
	h, _ := parentlessFixture(t, "")
	store := &securityprofile.Store{ConfigDir: h.lifecycle.configDir}
	if err := store.Select(t.Context(), policyfile.ProfileDelegated); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got, want := h.readSessionSettings(t.Context()).RunPresets, profilePresets(t, policyfile.ProfileDelegated); !slices.Equal(got, want) {
		t.Errorf("run presets after saving delegated = %v, want %v", got, want)
	}
}

func parentlessFixture(t *testing.T, saved string) (*Runtime, *fakeBridge) {
	t.Helper()
	configDir := t.TempDir()
	writeProfileSetting(t, configDir, saved)
	br := newFakeBridge()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return br }, br), cs, WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	awaitBoot(h)
	return h, br
}

// The choice is saved only while config.json names a profile, so a picker can tell the default it shows from a saved
// pick of the same profile.
func TestSecurityProfileChoice_IsSavedOnlyWhileConfigNamesAProfile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stored, chat, run string
		saved                   bool
	}{
		{"a_fresh_config", "", policyfile.ProfileDelegated, policyfile.ProfileTrusted, false},
		{"an_unknown_value", "no-such-profile", policyfile.ProfileDelegated, policyfile.ProfileTrusted, false},
		{"a_saved_approve_for_me", policyfile.ProfileDelegated, policyfile.ProfileDelegated, policyfile.ProfileDelegated, true},
		{"a_saved_trusted", policyfile.ProfileTrusted, policyfile.ProfileTrusted, policyfile.ProfileTrusted, true},
		{"a_saved_guarded", policyfile.ProfileGuarded, policyfile.ProfileGuarded, policyfile.ProfileGuarded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := parentlessFixture(t, tc.stored)
			if chat, run, saved := h.SecurityProfileChoice(t.Context()); chat != tc.chat || run != tc.run || saved != tc.saved {
				t.Errorf("SecurityProfileChoice = (%q, %q, %v), want (%q, %q, %v)", chat, run, saved, tc.chat, tc.run, tc.saved)
			}
		})
	}
}

// The run profile Settings names is the one a parentless run's host opens on, whatever is stored.
func TestSecurityProfileChoice_NamesTheProfileARunHostOpensOn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, stored string }{
		{"a_fresh_config", ""},
		{"an_unknown_value", "no-such-profile"},
		{"a_saved_approve_for_me", policyfile.ProfileDelegated},
		{"a_saved_trusted", policyfile.ProfileTrusted},
		{"a_saved_guarded", policyfile.ProfileGuarded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := parentlessFixture(t, tc.stored)
			_, run, _ := h.SecurityProfileChoice(t.Context())
			if got, want := h.readSessionSettings(t.Context()).RunPresets, profilePresets(t, run); !slices.Equal(got, want) {
				t.Errorf("run hosts open with %v, but Settings names %q (%v)", got, run, want)
			}
		})
	}
}

// While config.json cannot be read, the choice Settings shows is the profile a new chat is pinned to: the last
// readable one, still saved.
func TestSecurityProfileChoice_IsTheChatDoorsProfileWhileConfigIsUnreadable(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	writeProfileSetting(t, configDir, policyfile.ProfileTrusted)
	h := reopenFixtureIn(t, configDir, nil)
	if id, _, saved := h.SecurityProfileChoice(t.Context()); id != policyfile.ProfileTrusted || !saved {
		t.Fatalf("Setup: SecurityProfileChoice = (%q, %v), want saved trusted", id, saved)
	}
	rewriteConfigByHand(t, configDir, `{"security_profile":`)

	if id, _, saved := h.SecurityProfileChoice(t.Context()); id != policyfile.ProfileTrusted || !saved {
		t.Errorf("SecurityProfileChoice on an unreadable config = (%q, %v), want the last readable (trusted, true)", id, saved)
	}
	if _, err := h.coord.openReleased(t.Context(), "c1", ""); err != nil {
		t.Fatalf("OpenBridge: %v", err)
	}
	if rec, _ := h.chatStore.Get(t.Context(), "c1"); rec.SecurityProfile != policyfile.ProfileTrusted {
		t.Errorf("the new chat was pinned to %q, want trusted", rec.SecurityProfile)
	}
}
