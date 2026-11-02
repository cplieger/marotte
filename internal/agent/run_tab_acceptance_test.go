package agent

// A run tab's acceptance test: nothing faked below the runtime, asserting the persisted tabs.json every device projects.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/tabs"
)

type persistedTabs struct {
	Tabs []marotte.TabSubject `json:"tabs"`
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

func subjectFor(doc persistedTabs, kind marotte.TabKind, ref string) (marotte.TabSubject, bool) {
	for _, tab := range doc.Tabs {
		if tab.Kind == kind && tab.Ref == ref {
			return tab, true
		}
	}
	return marotte.TabSubject{}, false
}

// tabbedRig is a runtime over a real tab store, with the chat store and the engine's scripts a test
// arranges through.
type tabbedRig struct {
	h     *Runtime
	chats *testChatStore
	br    *fakeBridge
	// dir is the temp config dir, so a test can read the document.
	dir string
}

// newTabbedRig settles the engine's boot, so a test may edit br's scripts afterwards.
func newTabbedRig(t *testing.T) tabbedRig {
	t.Helper()
	dir := t.TempDir()
	st, err := tabs.NewStore(dir)
	if err != nil {
		t.Fatalf("tabs.NewStore: %v", err)
	}
	cs := newTestChatStore()
	br := newFakeBridge()
	h := newRuntimeOn(context.Background(), t.TempDir(), fakeEngineOf(func() engineSession { return br }, laneOf(br)), cs,
		WithTabs(st), WithConfigDir(dir))
	cs.wire(h)
	t.Cleanup(func() { shutdownHub(t, h) })
	settleBoot(t, h)
	return tabbedRig{h: h, chats: cs, br: br, dir: dir}
}

func newTabbedRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	r := newTabbedRig(t)
	return r.h, r.dir
}

// The subject's Ref is the chat id, its ID what a run tab nests under.
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
	h.translateACPEvent(marotte.ChatID(chatTab.Ref), h.originOf(marotte.ChatID(chatTab.Ref)), runNotif(methodWFRunStart, map[string]any{
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

// TestAcceptance_AnEndedRunOpensAsAChildOfItsChat drives the `open_tab` a run's outcome notification
// sends when clicked, which carries only the workflow id and arrives after the run ended and its lease
// was released: the run inventory still names the session that launched it.
func TestAcceptance_AnEndedRunOpensAsAChildOfItsChat(t *testing.T) {
	for name, tc := range map[string]struct {
		row        map[string]any
		wantNested bool
	}{
		"launched by the chat's session": {
			row:        map[string]any{"workflowId": "wf_ended", "name": "publish-pr", "status": "completed", "parentSessionId": "sess_launcher"},
			wantNested: true,
		},
		"launched by no session (scheduled, or from the Workflows page)": {
			row: map[string]any{"workflowId": "wf_ended", "name": "nightly", "status": "completed"},
		},
		"launched by a session no chat holds": {
			row: map[string]any{"workflowId": "wf_ended", "name": "publish-pr", "status": "failed", "parentSessionId": "sess_stranger"},
		},
		"listed nowhere": {
			row: map[string]any{"workflowId": "wf_other", "name": "publish-pr", "status": "completed", "parentSessionId": "sess_launcher"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newTabbedRig(t)
			chatTab := openChatTab(t, r.h, "op-chat")
			if _, err := r.chats.Mutate(t.Context(), marotte.ChatID(chatTab.Ref), func(c *marotte.Chat, _ bool) bool {
				c.RecordSession("sess_launcher")
				return true
			}); err != nil {
				t.Fatalf("record the chat's session: %v", err)
			}
			r.br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t, tc.row)}
			if _, held := r.h.runs.lease("wf_ended"); held {
				t.Fatal("the ended run holds a lease, so this does not exercise the inventory read")
			}

			rec := postCmd(t, r.h, marotte.ClientCommand{
				Type:    marotte.CmdOpenTab,
				Payload: json.RawMessage(`{"kind":"run","ref":"wf_ended","owns":false,"op_id":"opended"}`),
			})
			if rec.Code != 200 {
				t.Fatalf("open_tab = %d, want 200: %s", rec.Code, rec.Body.String())
			}

			opened, ok := subjectFor(readTabsFile(t, r.dir), marotte.TabKindRun, "wf_ended")
			if !ok {
				t.Fatal("the click opened no run tab")
			}
			want := ""
			if tc.wantNested {
				want = chatTab.ID
			}
			if opened.Parent != want {
				t.Errorf("parent = %q, want %q", opened.Parent, want)
			}
		})
	}
}

// TestAcceptance_AnEndedRunReopensItsClosedChatTab drives the same click when the launching chat's
// tab was closed and its record kept: the chat's tab comes back with the run nested under it.
func TestAcceptance_AnEndedRunReopensItsClosedChatTab(t *testing.T) {
	r := newTabbedRig(t)
	const launcher = marotte.ChatID("c-launcher")
	if _, err := r.chats.Mutate(t.Context(), launcher, func(c *marotte.Chat, _ bool) bool {
		c.Name = marotte.DefaultChatName
		c.RecordSession("sess_launcher")
		return true
	}); err != nil {
		t.Fatalf("seed the tabless chat: %v", err)
	}
	r.br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t, map[string]any{
		"workflowId": "wf_ended", "name": "publish-pr", "status": "completed", "parentSessionId": "sess_launcher",
	})}

	rec := postCmd(t, r.h, marotte.ClientCommand{
		Type:    marotte.CmdOpenTab,
		Payload: json.RawMessage(`{"kind":"run","ref":"wf_ended","owns":false,"op_id":"opended"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("open_tab = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	doc := readTabsFile(t, r.dir)
	chatTab, ok := subjectFor(doc, marotte.TabKindChat, string(launcher))
	if !ok {
		t.Fatal("the launching chat's tab was not reopened")
	}
	opened, ok := subjectFor(doc, marotte.TabKindRun, "wf_ended")
	if !ok {
		t.Fatal("the click opened no run tab")
	}
	if opened.Parent != chatTab.ID {
		t.Errorf("parent = %q, want the reopened chat tab %q", opened.Parent, chatTab.ID)
	}
}

// TestAcceptance_AnUnreadableLauncherOpensNoRunTab pins that a failed read refuses the click, because
// a run opened top level then could never nest once the read recovers.
func TestAcceptance_AnUnreadableLauncherOpensNoRunTab(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T, r tabbedRig){
		"the run inventory cannot be read": func(_ *testing.T, r tabbedRig) {
			r.br.callErrs = map[string]error{methodKiroWorkflowList: errors.New("the engine went away")}
		},
		"a chat file cannot be read": func(t *testing.T, r tabbedRig) {
			r.br.callResults = map[string]json.RawMessage{methodKiroWorkflowList: kasRuns(t, map[string]any{
				"workflowId": "wf_ended", "name": "publish-pr", "status": "completed", "parentSessionId": "sess_launcher",
			})}
			unreadable := filepath.Join(r.chats.Dir(), "c9")
			if err := os.MkdirAll(unreadable, 0o700); err != nil {
				t.Fatalf("Setup: %s", err)
			}
			if err := os.WriteFile(filepath.Join(unreadable, "chat.json"), []byte("{"), 0o600); err != nil {
				t.Fatalf("Setup: %s", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newTabbedRig(t)
			openChatTab(t, r.h, "op-chat")
			arrange(t, r)

			rec := postCmd(t, r.h, marotte.ClientCommand{
				Type:    marotte.CmdOpenTab,
				Payload: json.RawMessage(`{"kind":"run","ref":"wf_ended","owns":false,"op_id":"opended"}`),
			})

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("open_tab = %d, want 503: %s", rec.Code, rec.Body.String())
			}
			if tab, ok := subjectFor(readTabsFile(t, r.dir), marotte.TabKindRun, "wf_ended"); ok {
				t.Errorf("a run tab %+v was opened on an unread launcher", tab)
			}
		})
	}
}

// liveRunOfTablessChat seeds a chat with no tab and starts a run its lease names.
func liveRunOfTablessChat(t *testing.T, r tabbedRig, launcher marotte.ChatID, workflowID string) {
	t.Helper()
	if _, err := r.chats.Mutate(t.Context(), launcher, func(c *marotte.Chat, _ bool) bool {
		c.Name = marotte.DefaultChatName
		return true
	}); err != nil {
		t.Fatalf("seed the tabless chat: %v", err)
	}
	r.h.translateACPEvent(launcher, r.h.originOf(launcher), runNotif(methodWFRunStart, map[string]any{
		"workflowId": workflowID, "workflowName": "publish-pr",
	}))
	if l, held := r.h.runs.lease(workflowID); !held || l.ChatID != string(launcher) {
		t.Fatalf("lease = %+v held %v, want one naming %q", l, held, launcher)
	}
}

// TestAcceptance_ALiveRunWhoseLauncherCannotBeReadOpensNoRunTab — the lease names the chat, its tab
// is closed and its record unreadable, so it may still exist: the click is refused, not top level.
func TestAcceptance_ALiveRunWhoseLauncherCannotBeReadOpensNoRunTab(t *testing.T) {
	r := newTabbedRig(t)
	bystander := openChatTab(t, r.h, "op-chat")
	const launcher = marotte.ChatID("c-launcher")
	liveRunOfTablessChat(t, r, launcher, "wf_live")
	if err := os.WriteFile(filepath.Join(r.chats.Dir(), string(launcher), "chat.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("Setup: %s", err)
	}

	rec := postCmd(t, r.h, marotte.ClientCommand{
		Type:    marotte.CmdOpenTab,
		Payload: json.RawMessage(`{"kind":"run","ref":"wf_live","owns":false,"op_id":"oplive"}`),
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("open_tab = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	doc := readTabsFile(t, r.dir)
	if tab, ok := subjectFor(doc, marotte.TabKindRun, "wf_live"); ok {
		t.Errorf("a run tab %+v was opened on an unread launcher", tab)
	}
	if len(doc.Tabs) != 1 || doc.Tabs[0].ID != bystander.ID {
		t.Errorf("tabs.json holds %v, want only the bystander %q: nothing opened", doc.Tabs, bystander.ID)
	}
}

// TestAcceptance_AClosedSuppliedParentStillNestsTheRun — the page named the launching chat's tab and
// another device closed it first: the launcher's tab comes back with the run nested under it.
func TestAcceptance_AClosedSuppliedParentStillNestsTheRun(t *testing.T) {
	r := newTabbedRig(t)
	const launcher = marotte.ChatID("c-launcher")
	liveRunOfTablessChat(t, r, launcher, "wf_live")

	rec := postCmd(t, r.h, marotte.ClientCommand{
		Type:    marotte.CmdOpenTab,
		Payload: json.RawMessage(`{"kind":"run","ref":"wf_live","parent":"t-closed-elsewhere","owns":false,"op_id":"oplive"}`),
	})
	if rec.Code != 200 {
		t.Fatalf("open_tab = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	doc := readTabsFile(t, r.dir)
	chatTab, ok := subjectFor(doc, marotte.TabKindChat, string(launcher))
	if !ok {
		t.Fatal("the launching chat's tab was not reopened")
	}
	opened, ok := subjectFor(doc, marotte.TabKindRun, "wf_live")
	if !ok {
		t.Fatal("the click opened no run tab")
	}
	if opened.Parent != chatTab.ID {
		t.Errorf("parent = %q, want the reopened chat tab %q", opened.Parent, chatTab.ID)
	}
}

// TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat pins that a manual or scheduled run's lease exists and names no chat.
func TestAcceptance_AParentlessRunKeepsItsLeaseWithNoChat(t *testing.T) {
	h, _ := newTabbedRuntime(t)
	openChatTab(t, h, "op-chat")

	// The workspace-global lifecycle frame of a launched run.
	h.translateACPEvent("", h.originOf(""), runNotif(methodWFRunStart, map[string]any{
		"workflowId": "wf_scheduled", "workflowName": "nightly",
	}))
	// And one on the run's own synthetic-id bridge.
	h.translateACPEvent(runChatID("wf_scheduled"), h.originOf(runChatID("wf_scheduled")), runNotif(methodWFNodeStart, map[string]any{
		"workflowId": "wf_scheduled", "nodeId": "coder",
	}))

	// The orphan sweep and deadline read it; RunChat answers off its chat id.
	if l, held := h.runs.lease("wf_scheduled"); !held {
		t.Error("the parentless run lost its lease, so nothing bounds or sweeps it")
	} else if l.ChatID != "" {
		t.Errorf("lease chat id = %q, want empty for a parentless run", l.ChatID)
	}
}
