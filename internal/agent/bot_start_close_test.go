package agent

// A Bot's start and a close of its tab, from two devices, end as one of the two serial orders would: a close that
// commits before the start's first message sends none and starts no turn, and one that commits after ends that turn.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/tabs"
)

// startCloseHub is a runtime with a tab store and c1 a Bot draft in an open tab, its sessions on br.
func startCloseHub(t *testing.T, br *fakeBridge) (*Runtime, *testChatStore, *tabs.Store, marotte.TabSubject) {
	t.Helper()
	st, err := tabs.NewStore(t.TempDir(), settings.DefaultHistoryWindow)
	if err != nil {
		t.Fatalf("Setup: tabs.NewStore: %v", err)
	}
	h, cs := botRuntimeWith(t, []Option{WithTabs(st)}, br)
	seedBotDraft(t, cs, "c1")
	return h, cs, st, reopenTab(t, h, "op-open")
}

func startSave(t *testing.T) marotte.ClientCommand {
	t.Helper()
	return botSaveCmd(t, "c1", marotte.BotSaveCommand{Name: "CI watcher", Goal: "Keep CI green.", Start: true})
}

// assertClosedStartedBot checks c1 is a started Bot marked closed and listed as a closed Bot, that the start's
// answer names the close as its kickoff error, and that no turn opened.
func assertClosedStartedBot(t *testing.T, cs *testChatStore, st *tabs.Store, body []byte) {
	t.Helper()
	var answer struct {
		KickoffError string `json:"kickoff_error"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode the start's answer %s: %v", body, err)
	}
	if !strings.Contains(answer.KickoffError, "closed") {
		t.Errorf("kickoff_error = %q, want the refusal naming the close", answer.KickoffError)
	}
	c, ok := cs.Get(t.Context(), "c1")
	if !ok || c.Bot == nil {
		t.Fatal("the Bot's record is gone, want it kept")
	}
	if c.Bot.StartedAt == 0 || c.Bot.ClosedAt == 0 {
		t.Errorf("started_at %d closed_at %d, want a started Bot marked closed", c.Bot.StartedAt, c.Bot.ClosedAt)
	}
	recent := st.Recent()
	if i := slices.IndexFunc(recent, func(e marotte.RecentClose) bool { return e.Ref == "c1" }); i < 0 || !recent[i].Bot {
		t.Errorf("Recent() = %+v, want the Bot listed as a closed Bot", recent)
	}
	if opens := opensOf(t, logOf(t, cs, "c1")); len(opens) != 0 {
		t.Errorf("turn opens = %+v, want none: the close committed before the kickoff", opens)
	}
}

func TestBotSave_ACloseCommittingAfterTheStartsWriteSendsNoKickoff(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs, st, tab := startCloseHub(t, br)
	var fired atomic.Bool
	var closeCode atomic.Int32
	hook := func(id marotte.ChatID) {
		if c, ok := cs.Get(t.Context(), id); id != "c1" || !ok || c.Bot == nil || c.Bot.StartedAt == 0 {
			return
		}
		// A flag, not a sync.Once: the close's own record writes come back through this hook.
		if fired.CompareAndSwap(false, true) {
			closeCode.Store(int32(postCloseTabOp(t, h, tab, "op-close")))
		}
	}
	cs.mutated.Store(&hook)

	rec := postCmd(t, h, startSave(t))

	cs.mutated.Store(nil)
	if closeCode.Load() != http.StatusOK {
		t.Fatalf("the close inside the start = %d, want 200", closeCode.Load())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("bot_save start = %d %s, want 200: the record was a draft when it ran", rec.Code, rec.Body.String())
	}
	assertClosedStartedBot(t, cs, st, rec.Body.Bytes())
	if wire := br.wireLog(); slices.Contains(wire, marotte.MethodPrompt) {
		t.Errorf("wire = %v, want no session/prompt in the closed Bot", wire)
	}
}

func TestBotSave_AStartLandingInTheDraftClosesDeleteWindowSendsNoKickoff(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	h, cs, st, tab := startCloseHub(t, br)
	started := make(chan struct{})
	var startedOnce sync.Once
	mutated := func(id marotte.ChatID) {
		if c, ok := cs.Get(t.Context(), id); id == "c1" && ok && c.Bot != nil && c.Bot.StartedAt != 0 {
			startedOnce.Do(func() { close(started) })
		}
	}
	cs.mutated.Store(&mutated)
	saved := make(chan []byte, 1)
	saveCode := atomic.Int32{}
	var fired sync.Once
	beforeDelete := func(id marotte.ChatID) {
		if id != "c1" {
			return
		}
		fired.Do(func() {
			go func() {
				rec := postCmd(t, h, startSave(t))
				saveCode.Store(int32(rec.Code))
				saved <- rec.Body.Bytes()
			}()
			<-started
		})
	}
	cs.beforeDeleteIf.Store(&beforeDelete)

	if code := postCloseTabOp(t, h, tab, "op-close"); code != http.StatusOK {
		t.Fatalf("close_tab(draft) = %d, want 200", code)
	}
	body := <-saved

	cs.beforeDeleteIf.Store(nil)
	cs.mutated.Store(nil)
	if saveCode.Load() != http.StatusOK {
		t.Fatalf("bot_save start = %d %s, want 200: the draft existed when it ran", saveCode.Load(), body)
	}
	assertClosedStartedBot(t, cs, st, body)
}

func TestBotSave_AKickoffBegunBeforeTheCloseIsEndedByIt(t *testing.T) {
	br := reporting(marotte.BotRoleID, marotte.DefaultModeID, marotte.BotRoleID)
	release := holdCall(t, br, marotte.MethodPrompt)
	var cancelled atomic.Bool
	br.mu.Lock()
	br.onNotify = func(method string) {
		if method == marotte.MethodCancel {
			cancelled.Store(true)
		}
	}
	br.mu.Unlock()
	h, cs, _, tab := startCloseHub(t, br)

	rec := postCmdWithin(t, h, startSave(t))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "kickoff_error") {
		t.Fatalf("bot_save start = %d %s, want 200 with the kickoff sent", rec.Code, rec.Body.String())
	}
	awaitWire(t, br, marotte.MethodPrompt)

	if code := postCloseTabOp(t, h, tab, "op-close"); code != http.StatusOK {
		t.Fatalf("close_tab = %d, want 200", code)
	}
	release()
	awaitTeardown(t, h, "c1")
	joinInflight(h)

	if !cancelled.Load() {
		t.Errorf("wire = %v, want the kickoff's turn cancelled by the close", br.wireLog())
	}
	if c, ok := cs.Get(t.Context(), "c1"); !ok || c.Bot == nil || c.Bot.ClosedAt == 0 {
		t.Errorf("record found %v, want the started Bot kept and marked closed", ok)
	}
}
