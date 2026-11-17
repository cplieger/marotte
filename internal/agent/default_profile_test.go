package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/trigger"
)

// A chat on an instance that never saved a profile starts on Approve for me; a saved profile, Trusted included,
// is what it starts on instead.
func TestChatDoor_StartsOnApproveForMeUnlessAProfileIsSaved(t *testing.T) {
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
			if _, _, _, err := h.runs.launch(t.Context(), "bundled://publish", nil, marotte.RunDefaults{}); err != nil {
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

func parentlessFixture(t *testing.T, saved string) (*Runtime, *fakeBridge) {
	t.Helper()
	configDir := t.TempDir()
	writeProfileSetting(t, configDir, saved)
	br := newFakeBridge()
	cs := newTestChatStore()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return br }, br), cs, WithConfigDir(configDir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	return h, br
}
