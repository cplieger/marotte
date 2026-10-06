package agent

// A run tab's acceptance test: nothing faked below the runtime, asserting the persisted tabs.json every device projects.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

// persistedTabs is the collection as it sits on disk, the set every device projects.
type persistedTabs struct {
	Tabs    []marotte.TabSubject `json:"tabs"`
	Version uint64               `json:"version"`
}

func readTabsFile(t *testing.T, dir string) persistedTabs {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tabs.json"))
	if err != nil {
		t.Fatalf("read tabs.json: %v", err)
	}
	var doc persistedTabs
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse tabs.json: %v", err)
	}
	return doc
}

// subjectFor finds the persisted tab for one (kind, ref), which is what names a subject.
func subjectFor(doc persistedTabs, kind marotte.TabKind, ref string) (marotte.TabSubject, bool) {
	for _, tab := range doc.Tabs {
		if tab.Kind == kind && tab.Ref == ref {
			return tab, true
		}
	}
	return marotte.TabSubject{}, false
}

// newTabbedRuntime returns the temp config dir too, so a test can read the document.
func newTabbedRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore: %v", err)
	}
	cs := newTestChatStore()
	br := newFakeBridge()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return br }, cs,
		WithTabs(st), WithConfigDir(dir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, dir
}

// openChatTab creates a chat through the coordinator; the subject's Ref is the chat id, its ID what a run tab nests under.
func openChatTab(t *testing.T, h *Runtime, opID string) marotte.TabSubject {
	t.Helper()
	opened, err := h.Membership().CreateChatAndOpen(t.Context(), command.ChatCreate{
		OpID: opID,
		Init: func(c *marotte.Chat) { c.Name = marotte.DefaultChatName },
	})
	if err != nil {
		t.Fatalf("CreateChatAndOpen: %v", err)
	}
	return opened.Subject
}

// TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat drives an `open_tab` carrying only a workflow id
// through the command boundary.
func TestAcceptance_ADeepLinkOpensTheRunAsAChildOfItsChat(t *testing.T) {
	h, dir := newTabbedRuntime(t)
	chatTab := openChatTab(t, h, "op-chat")
	// The lease names the launching chat the deep link cannot carry.
	h.translateACPEvent(marotte.ChatID(chatTab.Ref), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_deeplink", "workflowName": "publish-pr",
	}))

	rec := postCmd(t, h, marotte.ClientCommand{
		Type:    marotte.CmdOpenTab,
		Payload: json.RawMessage(`{"kind":"run","ref":"wf_deeplink","op_id":"opdeeplink"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("open_tab = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	reopened, ok := subjectFor(readTabsFile(t, dir), marotte.TabKindRun, "wf_deeplink")
	if !ok {
		t.Fatal("the deep link opened no run tab")
	}
	if reopened.Parent != chatTab.ID {
		t.Errorf("parent = %q, want the launching chat's tab %q — a deep link carries no chat id, "+
			"so the coordinator is the only thing that can answer this",
			reopened.Parent, chatTab.ID)
	}
}

// TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat pins that a manual or scheduled run's lease exists and names no chat.
func TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat(t *testing.T) {
	h, _ := newTabbedRuntime(t)
	openChatTab(t, h, "op-chat")

	// The workspace-global lifecycle frame of a launched run.
	h.translateACPEvent("", runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_scheduled", "workflowName": "nightly",
	}))
	// And one on the run's own synthetic-id bridge.
	h.translateACPEvent(runChatID("wf_scheduled"), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_scheduled", "nodeId": "coder",
	}))

	// The orphan sweep and deadline read it; fillRunParent answers off its chat id.
	if l, held := h.runs.lease("wf_scheduled"); !held {
		t.Error("the parentless run lost its lease, so nothing bounds or sweeps it")
	} else if l.ChatID != "" {
		t.Errorf("lease chat id = %q, want empty for a parentless run", l.ChatID)
	}
}
