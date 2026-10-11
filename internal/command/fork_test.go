package command

// These tests hand the handler a bridge that answers the way KAS would, so the handler picks its
// own path from a real refusal.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// forkHost answers TWO opens, because a tangent has two bridges: the parent's,
// resumed to issue the fork, and the tangent's own, resumed to load the forked
// session. Which one a call is for is the chat id, so every call is recorded rather
// than only the last.
type forkHost struct {
	*bridgeDeps
	// tangentBridge answers an open for any chat that is not the parent. It reports the
	// session the tangent is bound to, which is what production's does.
	tangentBridge Bridge
	// onOpenTangent stands in for the replay swap: in production the transcript lands
	// in the chat record inside OpenBridge, through the projection.
	onOpenTangent func(marotte.ChatID)
	// parent is the chat whose open gets bridgeDeps' bridge; every other chat gets
	// tangentBridge. Named by the caller so a test forking a differently-named parent
	// cannot silently route the parent's own resume to the tangent's bridge.
	parent      marotte.ChatID
	openChatIDs []marotte.ChatID
	openModels  []string
}

func (d *forkHost) OpenBridge(ctx context.Context, chatID marotte.ChatID, model string) (Bridge, error) {
	d.openChatIDs = append(d.openChatIDs, chatID)
	d.openModels = append(d.openModels, model)
	if chatID != d.parent {
		if d.onOpenTangent != nil {
			d.onOpenTangent(chatID)
		}
		return d.tangentBridge, nil
	}
	return d.bridgeDeps.OpenBridge(ctx, chatID, model)
}

// opensFor counts the opens recorded for one chat, so a test asserting on the
// parent's resume is not counting the tangent's.
func (d *forkHost) opensFor(chatID marotte.ChatID) int {
	n := 0
	for _, got := range d.openChatIDs {
		if got == chatID {
			n++
		}
	}
	return n
}

func newForkHost(store chatStore, bridge Bridge, parent marotte.ChatID) *forkHost {
	return &forkHost{
		bridgeDeps: &bridgeDeps{
			storeDeps: &storeDeps{benchDeps: newBenchDeps(), store: store},
			bridge:    bridge,
			opened:    bridge,
		},
		parent:        parent,
		tangentBridge: &recordingBridge{sessionID: "sess_tangent"},
	}
}

func forkReq(t *testing.T, newChat, parent marotte.ChatID, title string) *marotte.ClientCommand {
	t.Helper()
	return forkReqWithOp(t, newChat, parent, title, "")
}

func forkReqWithOp(t *testing.T, newChat, parent marotte.ChatID, title, opID string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.ForkChatCommand{ParentChatID: parent, Title: title, OpID: opID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{
		Type:    marotte.CmdForkChat,
		ChatID:  newChat,
		Payload: payload,
	}
}

type deletingForkBridge struct {
	recordingBridge
	store  chatStore
	parent marotte.ChatID
}

func (b *deletingForkBridge) Call(ctx context.Context, method string, params any) (*marotte.RPCResponse, error) {
	if _, err := b.store.Delete(ctx, b.parent); err != nil {
		return nil, err
	}
	return b.recordingBridge.Call(ctx, method, params)
}

// seedParent writes a parent chat with a transcript, a model, a mode and a live
// session id — everything the tangent inherits.
func seedParent(t *testing.T, store chatStore, id marotte.ChatID) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), id, func(c *marotte.Chat, _ bool) bool {
		c.Name = "Parent conversation"
		c.Model = "parent-model"
		c.CurrentModeID = "plan"
		c.Effort = "high"
		c.RecordSession("sess_parent")
		c.TurnCount = 2
		return true
	}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
}

// TestCmdForkChat_BindsTheForkedSession is the primary path and the whole point:
// KAS returns a NEW session id carrying the parent's context, and the tangent is
// created already bound to it — so the transcript arrives from the session/load
// replay and marotte copies no messages.
func TestCmdForkChat_BindsTheForkedSession(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{
		sessionID: "sess_parent",
		result:    map[string]any{"sessionId": "sess_tangent"},
	}
	host := newForkHost(store, br, "c-parent")

	_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", "Reaper detour"))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if br.gotMethod != marotte.MethodSessionFork {
		t.Errorf("called %q, want %q", br.gotMethod, marotte.MethodSessionFork)
	}
	if got := br.gotParams["sessionId"]; got != marotte.SessionID("sess_parent") {
		t.Errorf("forked sessionId = %v, want the PARENT's sess_parent", got)
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	if c.ACPSessionID != "sess_tangent" {
		t.Errorf("acp_session_id = %q, want the forked sess_tangent", c.ACPSessionID)
	}
	if c.TurnCount != 0 {
		t.Errorf("tangent carries %d turns, want 0: the replay supplies them", c.TurnCount)
	}
	if chain := c.SessionChain(); len(chain) != 1 || chain[0] != "sess_tangent" {
		t.Errorf("session chain = %v, want [sess_tangent]", chain)
	}
}

// TestCmdForkChat_SendsTangentMeta pins the caller-supplied _meta.kiro block; `createdReason` is
// KAS's own spelling for a tangent (2.18.0 sidecar).
func TestCmdForkChat_SendsTangentMeta(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", "Reaper detour"))

	if _, ok := br.gotParams["messageId"]; ok {
		t.Error("session/fork carried a messageId; a tangent fork names no message")
	}
	if br.gotParams["cwd"] == nil {
		t.Error("session/fork carried no cwd")
	}
	meta, ok := br.gotParams["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %T, want a map", br.gotParams["_meta"])
	}
	kiro, ok := meta["kiro"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro = %T, want a map", meta["kiro"])
	}
	if kiro["createdReason"] != marotte.CreatedReasonTangent {
		t.Errorf("createdReason = %v, want %q", kiro["createdReason"], marotte.CreatedReasonTangent)
	}
	if kiro["title"] != "Reaper detour" {
		t.Errorf("title = %v, want the supplied one", kiro["title"])
	}
}

// TestCmdForkChat_OmitsAnEmptyTitle: an absent title must not become an empty
// one. KAS stores the block verbatim, so sending `title: ""` would name the
// session the empty string rather than leaving it unnamed.
func TestCmdForkChat_OmitsAnEmptyTitle(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	meta := br.gotParams["_meta"].(map[string]any) //nolint:forcetypeassert // shape pinned by TestCmdForkChat_SendsTangentMeta
	kiro := meta["kiro"].(map[string]any)          //nolint:forcetypeassert // shape pinned by TestCmdForkChat_SendsTangentMeta
	if _, ok := kiro["title"]; ok {
		t.Errorf("an empty title was sent as a key: %v", kiro["title"])
	}
}

// TestCmdForkChat_InheritsTheParentsAgent: the tangent's answers must come from
// the same agent that produced the conversation it inherited. Read off the
// parent's RECORD rather than sent by the client, because the record is the truth
// about all three and a tab's projection can be stale.
func TestCmdForkChat_InheritsTheParentsAgent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	c, _ := store.Get(t.Context(), "c-tangent")
	if c.Model != "parent-model" {
		t.Errorf("model = %q, want the parent's", c.Model)
	}
	if c.CurrentModeID != "plan" {
		t.Errorf("current_mode_id = %q, want the parent's", c.CurrentModeID)
	}
	if c.Effort != "high" {
		t.Errorf("effort = %q, want the parent's", c.Effort)
	}
	// The NAME is not inherited: two tabs would share a label.
	if c.Name != marotte.DefaultChatName {
		t.Errorf("name = %q, want the default; the parent's name is not inherited", c.Name)
	}
	if !c.Tangent || !c.Header().Tangent {
		t.Error("tangent = false, want the fork marked so its header offers a merge back")
	}
}

// TestCmdForkChat_StartsFreshOnForkRefusal drives the three refusal shapes.
// The tangent still opens, but without a bound session or inherited context.
func TestCmdForkChat_StartsFreshOnForkRefusal(t *testing.T) {
	cases := map[string]*recordingBridge{
		"call error":        {sessionID: "sess_parent", callErr: errors.New("-32601 method not found")},
		"no session id":     {sessionID: "sess_parent", result: map[string]any{"error": "cannot fork"}},
		"unsafe session id": {sessionID: "sess_parent", result: map[string]any{"sessionId": "../../etc/passwd"}},
	}
	for name, br := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			host := newForkHost(store, br, "c-parent")

			body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200: a refused fork still opens the tangent (err %v)",
					statusOf(err), err)
			}
			reply, ok := body.(map[string]any)
			if !ok {
				t.Fatalf("body = %T, want map[string]any", body)
			}
			if reply["outcome"] != marotte.ForkOutcomeFresh {
				t.Errorf("outcome = %v, want %q", reply["outcome"], marotte.ForkOutcomeFresh)
			}
			if reply["session_id"] != "" {
				t.Errorf("session_id = %v, want empty on the fresh path", reply["session_id"])
			}
			c, ok := store.Get(t.Context(), "c-tangent")
			if !ok {
				t.Fatal("the tangent chat was not created on the fresh path")
			}
			if c.ACPSessionID != "" {
				t.Errorf("acp_session_id = %q, want empty: no session was forked", c.ACPSessionID)
			}
			if c.Model != "parent-model" || c.CurrentModeID != "plan" {
				t.Errorf("fresh tangent lost the parent's settings: model=%q mode=%q",
					c.Model, c.CurrentModeID)
			}
			if header, _ := reply["chat"].(marotte.ChatHeader); c.Tangent || header.Tangent {
				t.Errorf("tangent = %v (returned header %v), want false: no KAS parent link exists for a merge to resolve",
					c.Tangent, header.Tangent)
			}
		})
	}
}

func TestCmdForkChat_RefusesWhenTheParentWasDeletedDuringTheFork(t *testing.T) {
	cases := map[string]error{
		"forked session": nil,
		"fresh fallback": errors.New("bridge closed during fork"),
	}
	for name, callErr := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			br := &deletingForkBridge{
				sessionID: "sess_parent",
				result:    map[string]any{"sessionId": "sess_tangent"},
				callErr:   callErr,
				store:     store,
				parent:    "c-parent",
			}
			host := newForkHost(store, br, "c-parent")
			mem, st, _ := newTabbedMembership(t, store)

			body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), mem, forkReq(t, "c-tangent", "c-parent", ""))

			if statusOf(err) != http.StatusNotFound {
				t.Errorf("CmdForkChat status = %d, want 404 (body %v, error %v)", statusOf(err), body, err)
			}
			if _, ok := store.Get(t.Context(), "c-tangent"); ok {
				t.Error("CmdForkChat created a tangent after its parent was deleted")
			}
			if got := tabIDsFor(st, "c-tangent"); len(got) != 0 {
				t.Errorf("CmdForkChat opened tangent tabs %v after its parent was deleted", got)
			}
		})
	}
}

func TestCmdForkChat_ReplayStillResolvesWithoutTheParent(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")
	mem, st, _ := newTabbedMembership(t, store)
	req := forkReqWithOp(t, "", "c-parent", "", "op-replay")

	first, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), mem, req)
	if err != nil {
		t.Fatalf("first CmdForkChat = %v, want success", err)
	}
	firstReply, ok := first.(map[string]any)
	if !ok {
		t.Fatalf("first CmdForkChat body = %T, want map[string]any", first)
	}
	firstChat, ok := firstReply["chat"].(marotte.ChatHeader)
	if !ok {
		t.Fatalf("first CmdForkChat chat = %T, want marotte.ChatHeader", firstReply["chat"])
	}
	if _, err := store.Delete(t.Context(), "c-parent"); err != nil {
		t.Fatalf("Delete(%q) = %v, want nil", "c-parent", err)
	}

	body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), mem, req)
	if err != nil {
		t.Fatalf("replayed CmdForkChat = %v, want success", err)
	}
	reply, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("replayed CmdForkChat body = %T, want map[string]any", body)
	}
	gotChat, ok := reply["chat"].(marotte.ChatHeader)
	if !ok {
		t.Fatalf("replayed CmdForkChat chat = %T, want marotte.ChatHeader", reply["chat"])
	}
	if gotChat.ID != firstChat.ID {
		t.Errorf("replayed CmdForkChat chat = %q, want first chat %q", gotChat.ID, firstChat.ID)
	}
	if br.callCount != 1 {
		t.Errorf("replayed CmdForkChat made %d session/fork calls, want 1 total", br.callCount)
	}
	if got := tabIDsFor(st, marotte.ChatID(firstChat.ID)); len(got) != 1 {
		t.Errorf("replayed CmdForkChat left tabs %v, want the existing tangent tab", got)
	}
}

// A tangent needs the parent's context even when no live bridge is present. The
// parent bridge is opened with no model override, then the ordinary session/fork
// path carries KAS's own context into the tangent.
func TestCmdForkChat_OpensTheParentBridgeBeforeForking(t *testing.T) {
	cases := map[string]Bridge{
		"no bridge":  nil,
		"no session": &recordingBridge{},
	}
	for name, live := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedParent(t, store, "c-parent")
			resumed := &recordingBridge{
				sessionID: "sess_parent",
				result:    map[string]any{"sessionId": "sess_tangent"},
			}
			host := newForkHost(store, live, "c-parent")
			host.opened = resumed

			_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
			}
			if got := host.opensFor("c-parent"); got != 1 {
				t.Errorf("OpenBridge calls for the parent = %d, want 1", got)
			}
			if len(host.openChatIDs) == 0 || host.openChatIDs[0] != "c-parent" {
				t.Errorf("first OpenBridge chat = %v, want c-parent", host.openChatIDs)
			}
			for i, model := range host.openModels {
				if model != "" {
					t.Errorf("OpenBridge %d model = %q, want empty so the chat keeps its own", i, model)
				}
			}
			if resumed.gotMethod != marotte.MethodSessionFork {
				t.Errorf("opened bridge called %q, want %q", resumed.gotMethod, marotte.MethodSessionFork)
			}
			c, ok := store.Get(t.Context(), "c-tangent")
			if !ok {
				t.Fatal("the tangent chat was not created")
			}
			if c.ACPSessionID != "sess_tangent" {
				t.Errorf("acp_session_id = %q, want sess_tangent", c.ACPSessionID)
			}
		})
	}
}

// replayInto writes the turn count a session/load replay's merge leaves on the
// header, standing in for the swap that lands the turns in the log inside OpenBridge.
func replayInto(t *testing.T, store chatStore, chatID marotte.ChatID) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.TurnCount = 2
		return true
	}); err != nil {
		t.Fatalf("replay into %q: %v", chatID, err)
	}
}

// TestCmdForkChat_LoadsTheForkedHistoryIntoTheNewChat is the whole defect: the forked
// session holds the parent's conversation, so marotte's own surface must show it rather
// than an empty transcript attached to a session that secretly knows everything.
func TestCmdForkChat_LoadsTheForkedHistoryIntoTheNewChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")
	host.onOpenTangent = func(id marotte.ChatID) { replayInto(t, store, id) }

	body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	if c.TurnCount != 2 {
		t.Errorf("the tangent's record holds %d turns, want the parent's 2", c.TurnCount)
	}
	// The header half is load-bearing: the client's isEmptyChat reads its count, so a header
	// captured before the swap skips the fetch.
	reply, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map[string]any", body)
	}
	header, ok := reply["chat"].(marotte.ChatHeader)
	if !ok {
		t.Fatalf("chat = %T, want marotte.ChatHeader", reply["chat"])
	}
	if header.TurnCount != 2 {
		t.Errorf("the response header reports turn_count = %d, want 2", header.TurnCount)
	}
}

// The load runs on the TANGENT's own bridge, not the parent's: the replay projection is
// keyed by chat, so a load on the parent's bridge would rewrite the PARENT's transcript
// and seat the live untagged frames a load also emits in the parent's turn. Nothing
// waits for the replay to be adopted: a late merge lands on its own and the swap's
// subject_changed tells the client.
func TestCmdForkChat_OpensTheTangentsOwnBridge(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")

	_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	if got := host.opensFor("c-tangent"); got != 1 {
		t.Errorf("OpenBridge calls for the tangent = %d, want 1 (all opens: %v)", got, host.openChatIDs)
	}
}

// rebindSession detaches the chat's session and records another over it, which is what
// tryLoadSession's failure branch plus persistNewSessionMetadata do when a load is
// refused and session/new mints a fresh session.
func rebindSession(t *testing.T, store chatStore, chatID marotte.ChatID, sessionID string) {
	t.Helper()
	if _, err := store.Mutate(t.Context(), chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.RecordSession("")
		c.RecordSession(sessionID)
		return true
	}); err != nil {
		t.Fatalf("rebind %q to %q: %v", chatID, sessionID, err)
	}
}

// A resume that fell through to session/new leaves the tangent bound to a session
// carrying none of the parent's conversation, so the outcome must report `fresh`. The
// record alone cannot say it: it holds a non-empty session id either way.
func TestCmdForkChat_AFellThroughResumeReportsFresh(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")
	host.tangentBridge = &recordingBridge{sessionID: "sess_fresh"}
	host.onOpenTangent = func(id marotte.ChatID) { rebindSession(t, store, id, "sess_fresh") }

	body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200: a lost context is not a refusal (body %s)", statusOf(err), errText(err))
	}
	reply, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map[string]any", body)
	}
	if reply["outcome"] != marotte.ForkOutcomeFresh {
		t.Errorf("outcome = %v, want %q: the resume fell through, so the tangent inherited nothing",
			reply["outcome"], marotte.ForkOutcomeFresh)
	}
	if reply["session_id"] != "sess_fresh" {
		t.Errorf("session_id = %v, want the sess_fresh the record now holds", reply["session_id"])
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	if c.TurnCount != 0 {
		t.Errorf("the tangent holds %d turns, want 0: a fresh session carries no transcript", c.TurnCount)
	}
	// The forked session stays in the retired chain, so its KAS parent link still resolves.
	if header, _ := reply["chat"].(marotte.ChatHeader); !header.Tangent {
		t.Error("returned header tangent = false, want true: the chain still holds the session KAS forked")
	}
}

// A repeat of the same op after a fell-through load must answer `fresh` and spend no
// bridge: the record already says the fork's session was retired, so there is nothing
// left to inherit. Two POSTs of one op_id with no Idempotency-Key both run the handler,
// which is what routes the second call through the ledger's replay branch.
func TestCmdForkChat_RepeatOpAfterAFellThroughLoadReportsFreshAndLoadsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_tangent"}}
	host := newForkHost(store, br, "c-parent")
	host.tangentBridge = &recordingBridge{sessionID: "sess_fresh"}
	host.onOpenTangent = func(id marotte.ChatID) { rebindSession(t, store, id, "sess_fresh") }
	ops := newTestMembership(t, host)
	req := forkReqOp(t, "c-parent", "", "op-same")

	first, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), ops, req)
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	tangent := chatIDOfResponse(t, first)
	opensAfterFirst := host.opensFor(tangent)

	second, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), ops, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	reply, ok := second.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map[string]any", second)
	}
	if reply["outcome"] != marotte.ForkOutcomeFresh {
		t.Errorf("outcome = %v, want %q: the first attempt's load fell through, so the tangent inherited nothing",
			reply["outcome"], marotte.ForkOutcomeFresh)
	}
	if reply["session_id"] != "sess_fresh" {
		t.Errorf("session_id = %v, want the sess_fresh the record now holds", reply["session_id"])
	}
	if got := host.opensFor(tangent); got != opensAfterFirst {
		t.Errorf("OpenBridge calls for the tangent = %d, want the %d the first attempt made: "+
			"a session KAS minted empty is not worth a process tree", got, opensAfterFirst)
	}
}

// A tangent whose fork was refused has no session, so there is nothing to load and no
// reason to spend a process tree finding that out.
func TestCmdForkChat_AFreshTangentLoadsNothing(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	host := newForkHost(store, nil, "c-parent")

	body, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	if statusOf(err) != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
	}
	reply, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map[string]any", body)
	}
	if reply["outcome"] != marotte.ForkOutcomeFresh {
		t.Errorf("outcome = %v, want %q", reply["outcome"], marotte.ForkOutcomeFresh)
	}
	if got := host.opensFor("c-tangent"); got != 0 {
		t.Errorf("OpenBridge calls for the tangent = %d, want 0: a fresh tangent has nothing to load", got)
	}
}

// TestCmdForkChat_RefusesToReshapeAnExistingChat pins the guard, for
// cmdResumeSession's reason: binding a live chat to another session strands its
// own (the transcript stays on disk unreferenced, so the reaper sweeps it) and
// silently changes the history under a conversation someone is reading.
func TestCmdForkChat_RefusesToReshapeAnExistingChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	if _, err := store.Mutate(t.Context(), "c-tangent", func(c *marotte.Chat, _ bool) bool {
		c.Name = "Existing work"
		c.RecordSession("sess_existing")
		return true
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_new"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	c, _ := store.Get(t.Context(), "c-tangent")
	if c.ACPSessionID != "sess_existing" {
		t.Errorf("acp_session_id = %q, want sess_existing — an existing chat was rebound",
			c.ACPSessionID)
	}
	if c.Name != "Existing work" {
		t.Errorf("name = %q, want Existing work", c.Name)
	}
}

// TestCmdForkChat_Rejects covers the refusals that are the CLIENT's mistake
// rather than KAS's, and which must not create a chat.
func TestCmdForkChat_Rejects(t *testing.T) {
	cases := map[string]struct {
		newChat marotte.ChatID
		parent  marotte.ChatID
		title   string
		want    int
		seed    bool
	}{
		"self fork":       {newChat: "c-parent", parent: "c-parent", want: http.StatusBadRequest, seed: true},
		"empty parent":    {newChat: "c-tangent", parent: "", want: http.StatusBadRequest, seed: true},
		"unsafe parent":   {newChat: "c-tangent", parent: "../etc", want: http.StatusBadRequest, seed: true},
		"unknown parent":  {newChat: "c-tangent", parent: "c-missing", want: http.StatusNotFound, seed: false},
		"oversized title": {newChat: "c-tangent", parent: "c-parent", title: strings.Repeat("🙂", 65), want: http.StatusBadRequest, seed: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			if tc.seed {
				seedParent(t, store, "c-parent")
			}
			br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
			host := newForkHost(store, br, "c-parent")

			_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, tc.newChat, tc.parent, tc.title))

			if statusOf(err) != tc.want {
				t.Errorf("status = %d, want %d (body %s)", statusOf(err), tc.want, errText(err))
			}
			if tc.newChat != "c-parent" {
				if _, ok := store.Get(t.Context(), tc.newChat); ok {
					t.Errorf("a chat was created for a refused fork")
				}
			}
			if br.callCount != 0 {
				t.Errorf("made %d calls, want 0: a refused request must not reach KAS", br.callCount)
			}
		})
	}
}

// TestCmdForkChat_RejectsAMalformedPayload: the envelope's own failure mode.
func TestCmdForkChat_RejectsAMalformedPayload(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	host := newForkHost(store, nil, "c-parent")

	_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), &marotte.ClientCommand{
		Type:    marotte.CmdForkChat,
		ChatID:  "c-tangent",
		Payload: json.RawMessage(`{"parent_chat_id":`),
	})

	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", statusOf(err))
	}
}

// TestCmdForkChat_TheRecordSurvivesAClose asserts that closing the tab kills the work, not the record, so a
// tangent is still there to reopen.
func TestCmdForkChat_TheRecordSurvivesAClose(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))
	closeChatTeardown(t.Context(), host, host, host, "c-tangent")

	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tab-close teardown deleted the tangent's record; History would not list it")
	}
	if chain := c.SessionChain(); len(chain) == 0 {
		t.Error("the tangent lost its session chain on close")
	}
}

func testWorkspace(t *testing.T) Workspace {
	t.Helper()
	return Workspace{Dir: t.TempDir(), ConfigDir: t.TempDir()}
}

// A title at the user-name cap is legal, and KAS and the record receive the same
// sanitized value: an unsanitized title would let the record and KAS disagree.
func TestCmdForkChat_AcceptsATitleAtTheCapAndSendsItSanitized(t *testing.T) {
	atCap := strings.Repeat("🙂", 64)
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, err := cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", "  "+atCap+"\u202e  "))
	if err != nil {
		t.Fatalf("CmdForkChat with a 128-unit title = %v, want it accepted", err)
	}
	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("no tangent was created for an accepted title")
	}
	meta, ok := br.gotParams["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %T, want a map", br.gotParams["_meta"])
	}
	kiro, ok := meta["kiro"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.kiro = %T, want a map", meta["kiro"])
	}
	if kiro["title"] != atCap || c.Name != atCap {
		t.Errorf("KAS title = %q, record name = %q; want both %q", kiro["title"], c.Name, atCap)
	}
}

// TestCmdForkChat_LatchesATitleAsTheUsersName: KAS stores a fork's supplied
// title as the user's, so the tangent's record names it the same way and no
// later focus or derived title replaces it.
func TestCmdForkChat_LatchesATitleAsTheUsersName(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", "Reaper detour"))

	c, ok := store.Get(t.Context(), "c-tangent")
	if !ok {
		t.Fatal("the tangent chat was not created")
	}
	if c.Name != "Reaper detour" || !c.NameSetByUser {
		t.Errorf("name = %q, set by user = %v; want %q, true", c.Name, c.NameSetByUser, "Reaper detour")
	}
}

// TestCmdForkChat_AnUntitledForkIsNotUserNamed: no title, no latch, so the
// tangent can still take a derived or focus title.
func TestCmdForkChat_AnUntitledForkIsNotUserNamed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedParent(t, store, "c-parent")
	br := &recordingBridge{sessionID: "sess_parent", result: map[string]any{"sessionId": "sess_t"}}
	host := newForkHost(store, br, "c-parent")

	_, _ = cmdForkChat(t.Context(), host, host, testWorkspace(t), newTestMembership(t, host), forkReq(t, "c-tangent", "c-parent", ""))

	c, _ := store.Get(t.Context(), "c-tangent")
	if c.NameSetByUser {
		t.Errorf("an untitled fork is marked user-named (name %q)", c.Name)
	}
}
