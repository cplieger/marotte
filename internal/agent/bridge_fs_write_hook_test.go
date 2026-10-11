package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
)

type recordingHook struct {
	refuse       string
	target       string
	checked      []string
	savedContent []string
}

func (r *recordingHook) hook(t *testing.T) WriteHook {
	t.Helper()
	return WriteHook{
		Check: func(content []byte) error {
			r.checked = append(r.checked, string(content))
			if string(content) == r.refuse {
				return errors.New("tools.json was not saved: manifest version 1, want 2")
			}
			return nil
		},
		Saved: func() {
			data, err := os.ReadFile(r.target)
			if err != nil {
				t.Errorf("Saved ran with no file at %s: %v", r.target, err)
			}
			r.savedContent = append(r.savedContent, string(data))
		},
	}
}

func agentWrite(t *testing.T, h *Runtime, br *respondingBridge, path, content string) error {
	t.Helper()
	return sessionWrite(t, h, br, "s-main", path, content)
}

func sessionWrite(t *testing.T, h *Runtime, br *respondingBridge, session, path, content string) error {
	t.Helper()
	id := int64(7)
	msg := &marotte.RPCResponse{
		ID:     &id,
		Method: marotte.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"sessionId": session, "path": path, "content": content}),
	}
	h.inbound.respondFSWrite(t.Context(), "c1", h.originOf("c1"), msg)
	<-br.done
	return br.response.err
}

// hookedWorkspace is a workspace whose config dir sits inside it, the layout where an agent's
// file tools reach tools.json at all.
func hookedWorkspace(t *testing.T) (work, manifest string, rec *recordingHook) {
	t.Helper()
	work = t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "cfg"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest = filepath.Join(work, "cfg", "tools.json")
	if err := os.WriteFile(manifest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	return work, manifest, &recordingHook{refuse: "bad", target: manifest}
}

func TestRespondFSWrite_RefusesWhatTheWriteHookRefusesAndLeavesTheFile(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))

	err := agentWrite(t, h, br, manifest, "bad")

	if err == nil || err.Error() != "tools.json was not saved: manifest version 1, want 2" {
		t.Errorf("agent write refused by the hook answered %v, want the hook's reason", err)
	}
	if got, _ := os.ReadFile(manifest); string(got) != "old" {
		t.Errorf("tools.json after a refused agent write = %q, want %q unchanged", got, "old")
	}
	if len(rec.savedContent) != 0 {
		t.Errorf("Saved ran %d times for a refused write, want 0", len(rec.savedContent))
	}
}

func TestRespondFSWrite_RunsSavedOnceTheAcceptedContentIsOnDisk(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))

	if err := agentWrite(t, h, br, "cfg/tools.json", "good"); err != nil {
		t.Fatalf("agent write the hook accepts answered %v, want success", err)
	}

	if len(rec.savedContent) != 1 || rec.savedContent[0] != "good" {
		t.Errorf("tools.json at each Saved call = %q, want exactly one call seeing %q", rec.savedContent, "good")
	}
}

func TestRespondFSWrite_LeavesEveryOtherPathToTheOrdinaryWrite(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	if err := os.MkdirAll(filepath.Join(work, "elsewhere"), 0o750); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))

	if err := agentWrite(t, h, br, "cfg/other.json", "bad"); err != nil {
		t.Errorf("agent write of a sibling of tools.json answered %v, want success", err)
	}
	if err := agentWrite(t, h, br, "elsewhere/tools.json", "bad"); err != nil {
		t.Errorf("agent write of another directory's tools.json answered %v, want success", err)
	}

	if got, _ := os.ReadFile(filepath.Join(work, "cfg", "other.json")); string(got) != "bad" {
		t.Errorf("cfg/other.json after an agent write = %q, want %q", got, "bad")
	}
	if got, _ := os.ReadFile(filepath.Join(work, "elsewhere", "tools.json")); string(got) != "bad" {
		t.Errorf("elsewhere/tools.json after an agent write = %q, want %q", got, "bad")
	}
	if len(rec.checked) != 0 || len(rec.savedContent) != 0 {
		t.Errorf("hook saw checks %q and saves %q for unhooked paths, want neither", rec.checked, rec.savedContent)
	}
}

func TestRespondFSWrite_RefusesWithoutRecreatingADeletedConfigDir(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))
	if err := os.RemoveAll(filepath.Dir(manifest)); err != nil {
		t.Fatal(err)
	}

	err := agentWrite(t, h, br, "cfg/tools.json", "bad")

	if err == nil {
		t.Error("an agent write of tools.json into a deleted config dir skipped its hook")
	}
	if _, statErr := os.Stat(filepath.Dir(manifest)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("config dir after a refused write: stat error %v, want it still absent", statErr)
	}
}

func TestRespondFSWrite_HooksAnAcceptedWriteIntoADeletedConfigDir(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))
	if err := os.RemoveAll(filepath.Dir(manifest)); err != nil {
		t.Fatal(err)
	}

	if err := agentWrite(t, h, br, manifest, "good"); err != nil {
		t.Fatalf("agent write the hook accepts answered %v, want success", err)
	}

	if len(rec.savedContent) != 1 || rec.savedContent[0] != "good" {
		t.Errorf("tools.json at each Saved call = %q, want exactly one call seeing %q", rec.savedContent, "good")
	}
}

func TestRespondFSWrite_LeavesAnotherNewDirsToolsJSONWhileTheConfigDirIsDeleted(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))
	if err := os.RemoveAll(filepath.Dir(manifest)); err != nil {
		t.Fatal(err)
	}

	if err := agentWrite(t, h, br, "other/tools.json", "bad"); err != nil {
		t.Errorf("agent write of a new directory's tools.json answered %v, want success", err)
	}

	if len(rec.checked) != 0 {
		t.Errorf("hook checked %q for an unhooked path, want no check", rec.checked)
	}
}

func TestRespondFSWrite_HooksAWriteThroughASymlinkedAncestor(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	if err := os.Symlink("cfg", filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))

	err := agentWrite(t, h, br, "alias/tools.json", "bad")

	if err == nil {
		t.Error("an agent write reaching tools.json through a symlinked directory skipped its hook")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "old" {
		t.Errorf("tools.json after a refused write through an alias = %q, want %q unchanged", got, "old")
	}
}

func TestRespondFSWrite_HooksTheFileAHookPathNamesThroughASymlink(t *testing.T) {
	work, manifest, rec := hookedWorkspace(t)
	if err := os.Symlink("cfg", filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	h.SetWriteHook(filepath.Join(work, "alias", "tools.json"), rec.hook(t))

	err := agentWrite(t, h, br, manifest, "bad")

	if err == nil {
		t.Error("an agent write to tools.json skipped the hook registered through a symlinked directory")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "old" {
		t.Errorf("tools.json after a refused write = %q, want %q unchanged", got, "old")
	}
}

func raiseTurnApproval(t *testing.T, h *Runtime, chatID marotte.ChatID, session string) {
	t.Helper()
	id := int64(41)
	h.askHandlers[marotte.MethodRequestPermission](t.Context(), chatID, h.originOf(chatID), &marotte.RPCResponse{
		ID:     &id,
		Method: marotte.MethodRequestPermission,
		Params: mustJSON(t, map[string]any{
			"sessionId": session,
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Review changes"},
			"options":   []map[string]any{{"optionId": "accept", "name": "Accept", "kind": "allow_once"}},
			"_meta":     map[string]any{"kiro": map[string]any{"type": "turn_approval"}},
		}),
	})
}

func agentRewroteBadManifest(t *testing.T) (h *Runtime, br *respondingBridge, manifest string, rec *recordingHook) {
	t.Helper()
	work, manifest, rec := hookedWorkspace(t)
	if err := os.WriteFile(manifest, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, br = hubForFSTest(t, work)
	h.SetWriteHook(manifest, rec.hook(t))
	if err := agentWrite(t, h, br, manifest, "good"); err != nil {
		t.Fatalf("Setup: agent write the hook accepts answered %v, want success", err)
	}
	return h, br, manifest, rec
}

func TestRespondFSWrite_RefusesAnAgentWriteOfContentTheFileHeldBefore(t *testing.T) {
	h, br, manifest, rec := agentRewroteBadManifest(t)

	err := agentWrite(t, h, br, manifest, "bad")

	if err == nil {
		t.Error("an ordinary agent write of the bytes an earlier write replaced skipped the hook's refusal")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "good" {
		t.Errorf("tools.json after the refused write = %q, want %q unchanged", got, "good")
	}
	if len(rec.savedContent) != 1 {
		t.Errorf("Saved ran %d times, want 1 (the accepted write only)", len(rec.savedContent))
	}
}

func TestRespondFSWrite_LetsATurnApprovalRestorePutBackContentTheHookRefuses(t *testing.T) {
	h, br, manifest, rec := agentRewroteBadManifest(t)
	raiseTurnApproval(t, h, "c1", "s-main")

	if err := agentWrite(t, h, br, manifest, "bad"); err != nil {
		t.Errorf("the restore after a turn approval answered %v, want success", err)
	}

	if got, _ := os.ReadFile(manifest); string(got) != "bad" {
		t.Errorf("tools.json after the restore = %q, want %q put back", got, "bad")
	}
	if len(rec.savedContent) != 2 || rec.savedContent[1] != "bad" {
		t.Errorf("tools.json at each Saved call = %q, want a second call seeing the restored %q", rec.savedContent, "bad")
	}
}

func TestRespondFSWrite_RefusesAnotherSessionsWriteWhileATurnApprovalRestores(t *testing.T) {
	h, br, manifest, _ := agentRewroteBadManifest(t)
	raiseTurnApproval(t, h, "c1", "s-main")

	err := sessionWrite(t, h, br, "s-step", manifest, "bad")

	if err == nil {
		t.Error("a write from a session the turn approval does not cover skipped the hook's refusal")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "good" {
		t.Errorf("tools.json after the refused write = %q, want %q unchanged", got, "good")
	}
}

func TestRespondFSWrite_RefusesAgainOnceTheChatsNextTurnOpens(t *testing.T) {
	h, br, manifest, _ := agentRewroteBadManifest(t)
	raiseTurnApproval(t, h, "c1", "s-main")
	if _, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-2", Text: "next"},
	}); err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}

	err := agentWrite(t, h, br, manifest, "bad")

	if err == nil {
		t.Error("an agent write in the turn after a turn approval skipped the hook's refusal")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "good" {
		t.Errorf("tools.json after the refused write = %q, want %q unchanged", got, "good")
	}
}

func TestRespondFSWrite_LetsARewindRestorePutBackContentTheHookRefuses(t *testing.T) {
	h, br, manifest, rec := agentRewroteBadManifest(t)
	sb := h.bridge.mgr.get("c1")
	if _, err := sb.Call(t.Context(), marotte.MethodCheckpointRevertMultiple, map[string]any{"messageId": "m-1"}); err != nil {
		t.Fatalf("Setup: revert call: %v", err)
	}

	if err := sessionWrite(t, h, br, string(sb.SessionID()), manifest, "bad"); err != nil {
		t.Errorf("the restore a rewind makes answered %v, want success", err)
	}

	if got, _ := os.ReadFile(manifest); string(got) != "bad" {
		t.Errorf("tools.json after the rewind restore = %q, want %q put back", got, "bad")
	}
	if len(rec.savedContent) != 2 {
		t.Errorf("Saved ran %d times, want 2 (the agent write and the restore)", len(rec.savedContent))
	}
}

func TestRespondFSWrite_RefusesAfterARewindSentOverAnOpenTurn(t *testing.T) {
	h, br, manifest, _ := agentRewroteBadManifest(t)
	if _, err := h.coord.OpenTurn(t.Context(), "c1", command.TurnOpen{
		Source: marotte.TurnSourcePrompt, Prompt: &marotte.EntryPrompt{ID: "m-2", Text: "next"},
	}); err != nil {
		t.Fatalf("Setup: OpenTurn: %v", err)
	}
	sb := h.bridge.mgr.get("c1")
	if _, err := sb.Call(t.Context(), marotte.MethodCheckpointRevertMultiple, map[string]any{"messageId": "m-1"}); err != nil {
		t.Fatalf("Setup: revert call: %v", err)
	}

	err := sessionWrite(t, h, br, string(sb.SessionID()), manifest, "bad")

	if err == nil {
		t.Error("a write in the open turn a rewind raced skipped the hook's refusal")
	}
	if got, _ := os.ReadFile(manifest); string(got) != "good" {
		t.Errorf("tools.json after the refused write = %q, want %q unchanged", got, "good")
	}
}

func TestTurnApproval_OnARunChatMarksNoRestores(t *testing.T) {
	work, _, _ := hookedWorkspace(t)
	h, _ := hubForFSTest(t, work)
	run := runChatID("wf-1")

	raiseTurnApproval(t, h, run, "s-step")

	if h.coord.turns.restoring(run, "s-step") {
		t.Error("a turn approval on a run chat opened a restore window no turn of that chat can close")
	}
}
