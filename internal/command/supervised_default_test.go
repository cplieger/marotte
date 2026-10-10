package command

// The global Supervised default must reach every seed site, not only prompt.go's auto-create
// branch: every explicit create mints the record first.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/testsupport"
)

// An EMPTY doc writes no file at all, which is the fail-closed case.
func supervisedConfigDir(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	if doc == "" {
		return dir
	}
	if err := os.WriteFile(filepath.Join(dir, settings.Filename), []byte(doc), 0o600); err != nil {
		t.Fatalf("write %s: %v", settings.Filename, err)
	}
	return dir
}

// newSupervisedMembership builds a coordinator whose SupervisedDefault is the
// same reader prompt.go uses, over configDir. Mirrors what RegisterDefaults
// wires, so the two seed sites cannot disagree.
func newSupervisedMembership(t *testing.T, chats chatStore, configDir string) *Membership {
	t.Helper()
	return newMembership(&membershipDeps{
		Chats:             chats,
		SupervisedDefault: func(ctx context.Context) bool { return supervisedDefaultSetting(ctx, configDir) },
	})
}

// supervisedDefaultCases is the settings half every command shares: on, off, and
// no settings file at all.
var supervisedDefaultCases = []struct {
	name string
	doc  string
	want bool
}{
	{name: "the default is on", doc: `{"supervised_default":true}`, want: true},
	{name: "the default is off", doc: `{"supervised_default":false}`, want: false},
	{name: "no settings file", doc: "", want: false},
}

func TestCmdCreateChat_SeedsTheSupervisedDefault(t *testing.T) {
	for _, tc := range supervisedDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			mem := newSupervisedMembership(t, store, supervisedConfigDir(t, tc.doc))

			body, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{}))
			if err != nil {
				t.Fatalf("CmdCreateChat = %v", err)
			}

			id := chatIDOfResponse(t, body)
			c, ok := store.Get(t.Context(), id)
			if !ok {
				t.Fatalf("the returned chat %q is not in the store", id)
			}
			if c.SupervisedMode != tc.want {
				t.Errorf("create_chat with supervised_default %v: SupervisedMode = %v, want %v; "+
					"the New chat button mints the record before the first prompt, so the prompt path's "+
					"seed never runs for it", tc.doc, c.SupervisedMode, tc.want)
			}
		})
	}
}

func TestCmdResumeSession_SeedsTheSupervisedDefault(t *testing.T) {
	for _, tc := range supervisedDefaultCases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			mem := newSupervisedMembership(t, store, supervisedConfigDir(t, tc.doc))

			if _, err := cmdResumeSession(t.Context(), mem,
				resumeReq(t, "c1", "sess_abc-123", "Earlier work")); err != nil {
				t.Fatalf("CmdResumeSession = %v", err)
			}

			c, ok := store.Get(t.Context(), "c1")
			if !ok {
				t.Fatal("the chat was not created")
			}
			if c.SupervisedMode != tc.want {
				t.Errorf("resume_session with supervised_default %v: SupervisedMode = %v, want %v; "+
					"an adopted session has no parent posture, so it takes the global default",
					tc.doc, c.SupervisedMode, tc.want)
			}
		})
	}
}

// TestCmdForkChat_InheritsTheParentsSupervisedMode is driven in BOTH directions:
// a supervised parent under an OFF default proves the inheritance happens, and an
// unsupervised parent under an ON default proves the first case is not passing
// because the global default happened to agree.
func TestCmdForkChat_InheritsTheParentsSupervisedMode(t *testing.T) {
	cases := []struct {
		name             string
		doc              string
		parentSupervised bool
		want             bool
	}{
		{
			name:             "a supervised parent's tangent is supervised even with the default off",
			doc:              `{"supervised_default":false}`,
			parentSupervised: true,
			want:             true,
		},
		{
			name:             "an unsupervised parent's tangent is not supervised even with the default on",
			doc:              `{"supervised_default":true}`,
			parentSupervised: false,
			want:             false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			setSupervised(t, store, "c-parent", tc.parentSupervised)
			br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
			host := newForkHost(store, br, "c-parent")
			mem := newSupervisedMembership(t, host, supervisedConfigDir(t, tc.doc))

			if _, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), mem,
				forkReq(t, "c-tangent", "c-parent", "Reaper detour")); err != nil {
				t.Fatalf("CmdForkChat = %v", err)
			}

			c, ok := store.Get(t.Context(), "c-tangent")
			if !ok {
				t.Fatal("the tangent chat was not created")
			}
			if c.SupervisedMode != tc.want {
				t.Errorf("fork of a parent with SupervisedMode %v under supervised_default %v: "+
					"tangent SupervisedMode = %v, want %v; a tangent continues the same conversation, so "+
					"inheriting model, mode and effort while dropping the review gate would silently "+
					"downgrade safety", tc.parentSupervised, tc.doc, c.SupervisedMode, tc.want)
			}
		})
	}
}

// setSupervised flips one chat's posture without touching seedParent, which every
// other fork test shares.
func setSupervised(t *testing.T, store chatStore, id marotte.ChatID, supervised bool) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.SupervisedMode = supervised
		return true
	}); err != nil {
		t.Fatalf("set supervised on %s: %v", id, err)
	}
}

// TestMembership_SupervisedDefaultUnwiredIsFalse pins the nil guard: a build that
// wires no reader mints unsupervised chats rather than panicking, which is what
// every test helper in this package relies on.
func TestMembership_SupervisedDefaultUnwiredIsFalse(t *testing.T) {
	mem := newMembership(&membershipDeps{Chats: testsupport.NewInMemoryChatStore()})

	if mem.supervisedDefaultValue(t.Context()) {
		t.Error("an unwired SupervisedDefault read as true; it must fail closed")
	}
}
