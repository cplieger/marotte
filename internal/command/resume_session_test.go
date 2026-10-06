package command

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// storeDeps is benchDeps with a real chat store, so a handler that mutates the
// store can be asserted on. benchDeps returns nil for ChatStore().
type storeDeps struct {
	*benchDeps
	store ChatStore
}

// The store methods are promoted from the embedded store, not handed back
// through a ChatStore() getter: Roles holds the interface directly now, so a
// double that only overrode the getter left benchDeps' no-op methods winning and
// silently stored nothing.
func (d *storeDeps) Get(ctx context.Context, id marotte.ChatID) (*marotte.Chat, bool) {
	return d.store.Get(ctx, id)
}

func (d *storeDeps) Mutate(ctx context.Context, id marotte.ChatID, fn func(*marotte.Chat, bool) bool) (string, error) {
	return d.store.Mutate(ctx, id, fn)
}

func (d *storeDeps) Revert(ctx context.Context, id marotte.ChatID, turn, kasMessageID string) (*marotte.Entry, *marotte.Entry, error) {
	return d.store.Revert(ctx, id, turn, kasMessageID)
}

func (d *storeDeps) RewindTarget(ctx context.Context, id marotte.ChatID, promptID string) (marotte.RewindTarget, bool, error) {
	return d.store.RewindTarget(ctx, id, promptID)
}

func (d *storeDeps) PromptAttachmentPaths(ctx context.Context, id marotte.ChatID, watermark string) ([]string, error) {
	return d.store.PromptAttachmentPaths(ctx, id, watermark)
}

func (d *storeDeps) TurnCount(ctx context.Context, id marotte.ChatID) (uint64, bool) {
	return d.store.TurnCount(ctx, id)
}

func (d *storeDeps) SetDraft(ctx context.Context, id marotte.ChatID, text string) (*marotte.ComposerState, error) {
	return d.store.SetDraft(ctx, id, text)
}

func (d *storeDeps) SetAttachments(ctx context.Context, id marotte.ChatID, paths []string) (*marotte.ComposerState, error) {
	return d.store.SetAttachments(ctx, id, paths)
}

func (d *storeDeps) Delete(ctx context.Context, id marotte.ChatID) error {
	return d.store.Delete(ctx, id)
}

func newTestHost(t *testing.T, store ChatStore) hostDouble {
	t.Helper()
	return &storeDeps{benchDeps: newBenchDeps(), store: store}
}

// resumeReq builds a resume_session command envelope.
func resumeReq(t *testing.T, chatID marotte.ChatID, sessionID, name string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.ResumeSessionCommand{SessionID: sessionID, Name: name})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdResumeSession,
		ChatID:  chatID,
		Payload: payload,
	}
}

// TestCmdResumeSession_BindsTheSession is the point of the command: the chat is
// created ALREADY bound to the KAS session, so the next bridge takes the
// session/load path and the replay projection supplies the transcript. marotte
// copies no messages.
func TestCmdResumeSession_BindsTheSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newTestHost(t, store)
	ctx := t.Context()

	_, err := CmdResumeSession(ctx, newTestMembership(t, host), resumeReq(t, "c1", "sess_abc-123", "Earlier work"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, ok := store.Get(ctx, "c1")
	if !ok {
		t.Fatal("chat was not created")
	}
	if c.ACPSessionID != "sess_abc-123" {
		t.Errorf("acp_session_id = %q, want sess_abc-123", c.ACPSessionID)
	}
	if c.Name != "Earlier work" {
		t.Errorf("name = %q, want the session title", c.Name)
	}
	chain := c.SessionChain()
	if len(chain) != 1 || chain[0] != "sess_abc-123" {
		t.Errorf("session chain = %v, want [sess_abc-123]", chain)
	}
	if c.TurnCount != 0 {
		t.Errorf("chat carries %d turns, want 0: the replay supplies the transcript", c.TurnCount)
	}
}

// TestCmdResumeSession_RefusesToRebindAnExistingChat pins the guard. Pointing a
// live chat at another session would strand its own session — transcript still
// on disk, no longer referenced, so the reaper sweeps it — and hand the user a
// chat whose history silently changed.
func TestCmdResumeSession_RefusesToRebindAnExistingChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if _, err := store.Mutate(ctx, "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Existing"
		c.RecordSession("sess_original")
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	host := newTestHost(t, store)

	_, _ = CmdResumeSession(ctx, newTestMembership(t, host), resumeReq(t, "c1", "sess_other", "Hijack"))

	c, _ := store.Get(ctx, "c1")
	if c.ACPSessionID != "sess_original" {
		t.Errorf("acp_session_id = %q, want sess_original — an existing chat was rebound",
			c.ACPSessionID)
	}
	if c.Name != "Existing" {
		t.Errorf("name = %q, want Existing", c.Name)
	}
}

// TestCmdResumeSession_RejectsPathUnsafeIDs asserts that ids.ValidSessionID is a PATH-SAFETY guard, and the
// case list documents what it does and does not promise.
func TestCmdResumeSession_RejectsPathUnsafeIDs(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"traversal":      "sess_../../etc/passwd",
		"path separator": "sess_a/b",
		"backslash":      "sess_a\\b",
		"nul byte":       "sess_a\x00b",
		"dot dot":        "..",
	}
	for name, sid := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			host := newTestHost(t, store)

			_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", sid, ""))

			if statusOf(err) != http.StatusBadRequest {
				t.Errorf("status = %d for session id %q, want 400", statusOf(err), sid)
			}
			if _, ok := store.Get(t.Context(), "c1"); ok {
				t.Errorf("a chat was created for invalid session id %q", sid)
			}
		})
	}
}

// A name of exactly MaxChatNameBytes is a legal name and must be stored as
// given; one byte more is refused. The cap is on the RECORD's name field, so
// the boundary decides whether a chat is created at all.
func TestCmdResumeSession_NameLengthCap(t *testing.T) {
	atCap := strings.Repeat("n", marotte.MaxChatNameBytes)
	overCap := strings.Repeat("n", marotte.MaxChatNameBytes+1)

	t.Run("a name at the cap is accepted", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", "sess_abc", atCap))
		if err != nil {
			t.Fatalf("CmdResumeSession with a %d-byte name = %v, want it accepted", len(atCap), err)
		}
		c, ok := store.Get(t.Context(), "c1")
		if !ok {
			t.Fatal("no chat was created for an accepted name")
		}
		if c.Name != atCap {
			t.Errorf("stored name is %d bytes, want the %d-byte name as given", len(c.Name), len(atCap))
		}
	})

	t.Run("a name past the cap is refused", func(t *testing.T) {
		store := testsupport.NewInMemoryChatStore()
		host := newTestHost(t, store)

		_, err := CmdResumeSession(t.Context(), newTestMembership(t, host), resumeReq(t, "c1", "sess_abc", overCap))

		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("status = %d for a %d-byte name, want 400", statusOf(err), len(overCap))
		}
		if _, ok := store.Get(t.Context(), "c1"); ok {
			t.Error("a chat was created for an over-long name")
		}
	})
}
