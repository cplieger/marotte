package composition

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/runlease"
)

// seamCell is one scenario over one or more boots of the composed stores. Each cell names the chats it builds;
// a chat's session is "sess_" plus its id.
type seamCell struct {
	run func(t *testing.T, root string, eng *seamEngine, logs *seamLogs)
}

func sess(id string) string { return "sess_" + id }

// TestRetention_KeepState drives the composed retention wiring: every keep fact set and unset through its writer,
// the races between them and the purge, and the regressions the purge has had.
func TestRetention_KeepState(t *testing.T) {
	cells := map[string]seamCell{
		// The incident: a chat idle past retention with its tab open, across a restart.
		"original_regression": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.create(t, "c-open", sess("c-open"))
			b1.shutdown(t)
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.age(t, "c-open", seamIdle)
			b.pass(t)
			b.kept(t, "c-open", sess("c-open"))
			if b.tabOf(t, "c-open") == "" {
				t.Error("the open tab was lost")
			}
		}},
		"tab_set": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"tab_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		"turn_set": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			if _, ok := b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt, b.h.LifeStamp()); !ok {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"turn_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			res, ok := b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt, b.h.LifeStamp())
			if !ok {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.h.ReleaseTurnReservation(res)
			b.waitKey(t, "c-1", marotte.KeyTurn, false)
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		// A turn reserved and released with no Mutate still counts as use.
		"turn_no_mutate_tabless": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			res, ok := b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt, b.h.LifeStamp())
			if !ok {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.h.ReleaseTurnReservation(res)
			b.waitKey(t, "c-1", marotte.KeyTurn, false)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		// A turn fact the previous process wrote dies with it.
		"turn_other_boot": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-1", sess("c-1"))
			b1.shutdown(t)
			// What a process that died mid-turn leaves: its turn fact, under its boot id.
			editRecord(t, root, "c-1", func(doc map[string]any) {
				doc["keep"] = map[string]any{marotte.KeyTurn: map[string]any{
					"boot": "a-dead-process", "since": time.Now().UnixMilli(), "process": true,
				}}
			})
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		"run_set": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			eng.list(t, seamRow("wf_1", "running", sess("c-1")))
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
			if eng.called(t, kasDelete, "wf_1") {
				t.Error("the live run was deleted")
			}
		}},
		// A run's end releases nothing: its mark keeps the chat until one retention period after its last sign of
		// life, then stops keeping.
		"run_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			b.runComplete(t, "wf_1")
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
			b.ageMark(t, "c-1", runKey("wf_1"), seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		// A mark one retention period old is still inside the hour a throttled progress refresh may lag.
		"run_mark_inside_refresh_slack": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			b.age(t, "c-1", seamIdle)
			b.ageMark(t, "c-1", runKey("wf_1"), seamRetentionDays*24*time.Hour+chat.RunMarkRefresh/2)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"runtab_set": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "completed", sess("c-1")))
			b.loneRunTab(t, "c-1", "wf_1")
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"runtab_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "completed", sess("c-1")))
			b.loneRunTab(t, "c-1", "wf_1")
			b.closeTabID(t, b.runTabOf("wf_1"))
			b.waitKey(t, "c-1", runTabKey("wf_1"), false)
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		"draft_set": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			if code, body := b.cmd(t, marotte.CmdSetDraft, "c-1", map[string]any{"text": "half a thought"}); code != http.StatusOK {
				t.Fatalf("Setup: set_draft = %d %v", code, body)
			}
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"recent_use": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle-48*time.Hour)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		// A record from before this boot is kept until the reconcile has passed over it.
		"unreconciled": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-1", sess("c-1"))
			b1.shutdown(t)
			b := bootSeam(t, root, eng, logs, seamOpts{noReconcile: true})
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"r1_purge_then_open_tab": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
			if code, _ := b.openTab(t, marotte.TabKindChat, "c-1", ""); code != http.StatusNotFound {
				t.Errorf("open_tab of a purged chat = %d, want 404", code)
			}
			if tab := b.tabOf(t, "c-1"); tab != "" {
				t.Errorf("a tab %s shows the purged chat", tab)
			}
		}},
		"r1_concurrent": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			for i := range 200 {
				id := fmt.Sprintf("c-%03d", i)
				b.seed(t, id, "")
				b.age(t, id, seamIdle)
				var wg sync.WaitGroup
				wg.Go(func() {
					_, _ = b.h.Membership().OpenTab(t.Context(), marotte.OpenTab{Kind: marotte.TabKindChat, Ref: id}, "")
				})
				wg.Go(func() {
					_, _ = b.chats.PurgeIdle(t.Context(), marotte.ChatID(id), time.Now().Add(-seamRetentionDays*24*time.Hour))
				})
				wg.Wait()
				b.chats.EndTeardown(marotte.ChatID(id), nil)
				tab := b.tabOf(t, id)
				if !b.present(t, id) && tab != "" {
					t.Fatalf("iteration %d: a tab shows a chat the purge removed", i)
				}
				if tab != "" {
					b.closeTabID(t, tab)
				}
			}
		}},
		"r5_prompt_after_purge": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
			if got, _ := b.h.ReserveTurnForPrompt(t.Context(), "c-1", 0, b.h.LifeStamp(), ""); got != command.AdmissionChatGone {
				t.Errorf("a prompt on the purged chat = %v, want AdmissionChatGone", got)
			}
			if _, ok := b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt, b.h.LifeStamp()); ok {
				t.Error("a turn was reserved on the purged chat")
			}
			if b.present(t, "c-1") {
				t.Error("the refused turn recreated the purged record")
			}
		}},
		// A prompt admitted (its reservation taken) before its turn opens holds the chat through a pass.
		"r5_prompt_parked_after_reservation": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.age(t, "c-1", seamIdle)
			if got, _ := b.h.ReserveTurnForPrompt(t.Context(), "c-1", time.Second, b.h.LifeStamp(), ""); got != command.AdmissionAcquired {
				t.Fatalf("Setup: the prompt admission = %v", got)
			}
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"r9_engine_down": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-held", sess("c-held"))
			b.seed(t, "c-idle", sess("c-idle"))
			b.age(t, "c-held", seamIdle)
			b.age(t, "c-idle", seamIdle)
			eng.down(t)
			b.pass(t)
			b.kept(t, "c-held", sess("c-held"))
			b.purged(t, "c-idle", sess("c-idle"))
			if eng.called(t, kasDelete, "") {
				t.Error("a run was deleted while the engine was down")
			}
		}},
		"r9_engine_down_at_boot": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-1", sess("c-1"))
			b1.shutdown(t)
			b := bootSeam(t, root, eng, logs, seamOpts{engine: installingEngine{}, noWait: true})
			b.age(t, "c-1", seamIdle)
			b.chatPass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		// A live run whose launcher no source names stops the reconcile, so nothing from before the boot is purged.
		"r10_run_unknown_launcher": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-1", sess("c-1"))
			b1.shutdown(t)
			eng.list(t, seamRow("wf_orphan", "running", ""))
			b := bootSeam(t, root, eng, logs, seamOpts{noWait: true})
			waitFor(t, "the reconcile's stall", func() bool { return logs.has("a live run names no launching chat") })
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"r11_unreadable_record": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			path := filepath.Join(root, "chats", "c-garbage", "chat.json")
			writeFixture(t, path, "{not json")
			old := time.Now().Add(-seamIdle)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.pass(t)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("an unreadable record was purged: %v", err)
			}
		}},
		// A fact written for chat B while the pass is parked in chat A's teardown keeps B.
		"r12_fact_lands_mid_pass": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			// The pass decides eight chats at a time, in directory order: eight parked teardowns leave c-b undecided.
			ahead := make([]string, 8)
			for i := range ahead {
				ahead[i] = fmt.Sprintf("c-a%d", i)
				b.seed(t, ahead[i], sess(ahead[i]))
				b.age(t, ahead[i], seamIdle)
			}
			b.seed(t, "c-b", sess("c-b"))
			b.age(t, "c-b", seamIdle)
			_, release := eng.block(t, kasSessDel)
			n := logs.count(seamPassLine)
			b.start()
			waitFor(t, "eight parked teardowns", func() bool { return eng.count(t, kasSessDel) == len(ahead) })
			if code, body := b.openTab(t, marotte.TabKindChat, "c-b", ""); code != http.StatusOK {
				t.Fatalf("open_tab c-b mid-pass = %d %v", code, body)
			}
			release()
			waitFor(t, "the parked pass", func() bool { return logs.count(seamPassLine) > n })
			for _, id := range ahead {
				b.purged(t, id, sess(id))
			}
			b.kept(t, "c-b", sess("c-b"))
		}},
		// Deleting a chat from History closes the tabs nested under it and releases their facts.
		"close_tabs_for_releases_descendants": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			parent := b.create(t, "c-p", sess("c-p"))
			b.seed(t, "c-f", sess("c-f"))
			if code, body := b.openTab(t, marotte.TabKindChat, "c-f", parent); code != http.StatusOK {
				t.Fatalf("Setup: nest c-f = %d %v", code, body)
			}
			b.seed(t, "c-r", sess("c-r"))
			eng.list(t, seamRow("wf_r", "completed", sess("c-r")))
			if code, body := b.openTab(t, marotte.TabKindRun, "wf_r", parent); code != http.StatusOK {
				t.Fatalf("Setup: nest the run tab = %d %v", code, body)
			}
			if code, body := b.cmd(t, marotte.CmdDeleteChat, "c-p", nil); code != http.StatusOK {
				t.Fatalf("delete_chat c-p = %d %v", code, body)
			}
			b.waitKey(t, "c-f", marotte.KeyTab, false)
			b.waitKey(t, "c-r", runTabKey("wf_r"), false)
			b.age(t, "c-f", seamIdle)
			b.age(t, "c-r", seamIdle)
			b.pass(t)
			b.purged(t, "c-f", sess("c-f"))
			b.purged(t, "c-r", sess("c-r"))
		}},
		// A create replayed after its tab closed holds the tab again; a supplied existing id likewise.
		"create_replay_after_close": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			code, body := b.cmd(t, marotte.CmdCreateChat, "", map[string]any{"op_id": "op-x"})
			if code != http.StatusOK {
				t.Fatalf("Setup: create = %d %v", code, body)
			}
			chatObj, _ := body["chat"].(map[string]any)
			id, _ := chatObj["id"].(string)
			b.closeTab(t, id)
			if code, body := b.cmd(t, marotte.CmdCreateChat, "", map[string]any{"op_id": "op-x"}); code != http.StatusOK {
				t.Fatalf("replay = %d %v", code, body)
			}
			b.age(t, id, seamIdle)
			b.pass(t)
			b.kept(t, id, "")
			if _, ok := b.keys(t, id)[marotte.KeyTab]; !ok {
				t.Errorf("the replayed create holds no tab fact: %v", b.keys(t, id))
			}
		}},
		"create_supplied_existing_id": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			if code, body := b.cmd(t, marotte.CmdCreateChat, "c-1", nil); code != http.StatusOK {
				t.Fatalf("create with the existing id = %d %v", code, body)
			}
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"resume_claimed_session": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-a", sess("c-a"))
			code, _ := b.cmd(t, marotte.CmdResumeSession, "", map[string]any{"session_id": sess("c-a")})
			if code != http.StatusConflict {
				t.Errorf("resume_session of a held session = %d, want 409", code)
			}
		}},
		// The purge teardown parked in its session delete: every claimant on the chain is refused.
		"g6_1_claimants_during_teardown": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "completed", sess("c-1")))
			b.age(t, "c-1", seamIdle)
			entered, release := eng.block(t, kasSessDel)
			n := logs.count(seamPassLine)
			b.start()
			entered()
			if b.present(t, "c-1") {
				t.Fatal("Setup: the record was not removed before the teardown")
			}
			if code, body := b.openTab(t, marotte.TabKindRun, "wf_1", ""); code != http.StatusConflict {
				t.Errorf("g6_1_run_tab: run tab during the teardown = %d %v, want 409", code, body)
			}
			if code, body := b.cmd(t, marotte.CmdCreateChat, "c-1", nil); code != http.StatusConflict {
				t.Errorf("create with the purged id = %d %v, want 409", code, body)
			}
			if code, body := b.cmd(t, marotte.CmdResumeSession, "", map[string]any{"session_id": sess("c-1")}); code != http.StatusConflict {
				t.Errorf("r15: resume of a chain session = %d %v, want 409", code, body)
			}
			if _, ok := b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt, b.h.LifeStamp()); ok {
				t.Error("a turn was reserved on the chat in teardown")
			}
			if b.runTabOf("wf_1") != "" || b.present(t, "c-1") {
				t.Error("a claimant wrote state for the chain in teardown")
			}
			release()
			waitFor(t, "the parked pass", func() bool { return logs.count(seamPassLine) > n })
			if !eng.called(t, kasDelete, "wf_1") {
				t.Error("r6: the teardown did not delete the chain's run")
			}
			if b.chats.SessionInTeardown(sess("c-1")) {
				t.Error("the chain is still in teardown after it ended")
			}
		}},
		// A run tab whose launcher cannot be read is refused, even under an open supplied parent.
		"c6_1_launcher_unread": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			parent := b.create(t, "c-p", sess("c-p"))
			eng.fail(t, kasList, "", "engine draining")
			code, body := b.openTab(t, marotte.TabKindRun, "wf_1", parent)
			if code != http.StatusServiceUnavailable {
				t.Errorf("run tab with an unreadable launcher = %d %v, want 503", code, body)
			}
			if tab := b.runTabOf("wf_1"); tab != "" {
				t.Errorf("a run tab %s was written for a run whose launcher is unknown", tab)
			}
		}},
		// An unreadable arrangement keeps a held chat and a pre-upgrade chat alike.
		"tabs_unreadable": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.create(t, "c-held", sess("c-held"))
			b1.seed(t, "c-old", sess("c-old"))
			b1.shutdown(t)
			makePreUpgrade(t, filepath.Join(root, "chats"), "c-old")
			writeFixture(t, filepath.Join(root, "tabs.json"), "{garbage")
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.age(t, "c-held", seamIdle)
			b.age(t, "c-old", seamIdle)
			b.pass(t)
			b.kept(t, "c-held", sess("c-held"))
			b.kept(t, "c-old", sess("c-old"))
		}},
		// The browser's route restore writes the arrangement after a degraded boot; the held chat stays
		// kept, and stays kept across the next restart.
		"orch6_first_tab_write": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.create(t, "c-held", sess("c-held"))
			b1.seed(t, "c-other", sess("c-other"))
			b1.shutdown(t)
			writeFixture(t, filepath.Join(root, "tabs.json"), "{garbage")
			b := bootSeam(t, root, eng, logs, seamOpts{})
			if code, body := b.openTab(t, marotte.TabKindChat, "c-other", ""); code != http.StatusOK {
				t.Fatalf("the route restore = %d %v", code, body)
			}
			b.age(t, "c-held", seamIdle)
			b.pass(t)
			b.kept(t, "c-held", sess("c-held"))
			b.shutdown(t)

			b3 := bootSeam(t, root, eng, logs, seamOpts{})
			b3.age(t, "c-held", seamIdle)
			b3.pass(t)
			b3.kept(t, "c-held", sess("c-held"))
		}},
		// A fact the previous boot wrote for a tab that is still open survives the reconcile; one whose tab closed
		// with its release lost is settled from the owed entry.
		"reconcile_tab_across_boots": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.create(t, "c-open", sess("c-open"))
			b1.seed(t, "c-closed", sess("c-closed"))
			b1.shutdown(t)
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.age(t, "c-open", seamIdle)
			b.age(t, "c-closed", seamIdle)
			b.pass(t)
			b.kept(t, "c-open", sess("c-open"))
			b.purged(t, "c-closed", sess("c-closed"))
		}},
		// A run whose lease the previous boot held keeps its launcher across boots by its mark's time, and a terminal
		// row seen at a later boot releases nothing.
		"reconcile_run_across_boots": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-1", sess("c-1"))
			b1.runStart(t, "wf_1", sess("c-1"))
			b1.waitKey(t, "c-1", runKey("wf_1"), true)
			b1.shutdown(t)

			eng.list(t, seamRow("wf_1", "running", sess("c-1")))
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
			b.shutdown(t)

			eng.list(t, seamRow("wf_1", "completed", sess("c-1")))
			b3 := bootSeam(t, root, eng, logs, seamOpts{})
			b3.age(t, "c-1", seamIdle)
			b3.pass(t)
			b3.kept(t, "c-1", sess("c-1"))
			b3.ageMark(t, "c-1", runKey("wf_1"), seamIdle)
			b3.pass(t)
			b3.purged(t, "c-1", sess("c-1"))
		}},
		// A person's close stops every run the chat launched, a paused one included: a refused cancel is retried, and
		// a stop read back terminal releases the run's mark.
		"close_by_person_stops_runs": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_run", "running", sess("c-1")), seamRow("wf_park", "paused", sess("c-1")))
			b.runStart(t, "wf_run", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_run"), true)
			eng.inspected(t, "wf_run", "cancelled", sess("c-1"))
			eng.inspected(t, "wf_park", "paused", sess("c-1"))
			eng.fail(t, kasCancel, "wf_park", "owner pid 979344 is live")

			b.personClose(t, "c-1")

			if !eng.called(t, kasCancel, "wf_run") || !eng.called(t, kasCancel, "wf_park") {
				t.Fatal("a person's close did not cancel both the running and the paused run")
			}
			eng.answer(t, kasCancel, "wf_park", map[string]any{})
			waitFor(t, "the paused run's refused cancel retried", func() bool { return eng.countFor(t, kasCancel, "wf_park") >= 2 })
			eng.inspected(t, "wf_park", "cancelled", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_run"), false)
			b.waitKey(t, "c-1", runKey("wf_park"), false)
			if eng.called(t, kasDelete, "") {
				t.Error("a run whose cancel was confirmed was deleted")
			}
		}},
		// Closes no person made stop nothing: an API caller's close, the purge's RetentionClose, the boot's pruneTabs
		// and a restart.
		"close_not_by_person_stops_nothing": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.create(t, "c-api", sess("c-api"))
			b1.create(t, "c-purged", sess("c-purged"))
			b1.create(t, "c-pruned", sess("c-pruned"))
			b1.create(t, "c-restart", sess("c-restart"))
			eng.list(t, seamRow("wf_api", "running", sess("c-api")), seamRow("wf_purged", "running", sess("c-purged")),
				seamRow("wf_pruned", "running", sess("c-pruned")), seamRow("wf_restart", "running", sess("c-restart")))
			b1.runStart(t, "wf_api", sess("c-api"))
			b1.waitKey(t, "c-api", runKey("wf_api"), true)
			b1.closeTab(t, "c-api")
			b1.closeTab(t, "c-purged")
			b1.age(t, "c-purged", seamIdle)
			b1.pass(t)
			b1.purged(t, "c-purged", sess("c-purged"))
			b1.shutdown(t)
			if err := os.RemoveAll(filepath.Join(root, "chats", "c-pruned")); err != nil {
				t.Fatalf("Setup: %v", err)
			}

			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.quiet(t)
			if eng.count(t, kasCancel) != 0 {
				t.Errorf("a close no person made cancelled %d runs, want 0", eng.count(t, kasCancel))
			}
			if _, ok := b.keys(t, "c-api")[runKey("wf_api")]; !ok {
				t.Error("an API caller's close released the run's mark")
			}
		}},
		// A run whose launching chat has no open tab is not retried; with the tab open, the closed-chat refusal does
		// not apply.
		"retry_closed_launcher_refused": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "failed", sess("c-1")))
			eng.inspected(t, "wf_1", "failed", sess("c-1"))
			if _, body := b.serve(t, http.MethodPost, "/api/runs/wf_1/retry", nil); fmt.Sprint(body) != "" &&
				strings.Contains(fmt.Sprint(body), "is closed") {
				t.Errorf("a retry with the launching chat's tab open was refused as closed: %v", body)
			}
			b.closeTab(t, "c-1")
			code, body := b.serve(t, http.MethodPost, "/api/runs/wf_1/retry", nil)
			if code != http.StatusConflict || !strings.Contains(fmt.Sprint(body), "is closed") {
				t.Errorf("retry with no tab on the launching chat = %d %v, want 409 naming the closed chat", code, body)
			}
		}},
		// The stop rewrites the run's mark before its first cancel, so a chat whose run went quiet is kept while the
		// stop is in flight, and purged once the stop is confirmed and the chat is idle.
		"stop_keeps_chat_until_confirmed": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "paused", sess("c-1")))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			b.age(t, "c-1", seamIdle)
			b.ageMark(t, "c-1", runKey("wf_1"), seamIdle)
			entered, release := eng.block(t, kasCancel)
			done := make(chan struct{})
			go func() {
				defer close(done)
				b.personClose(t, "c-1")
			}()
			entered()
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
			eng.inspected(t, "wf_1", "cancelled", sess("c-1"))
			release()
			<-done
			b.waitKey(t, "c-1", runKey("wf_1"), false)
			// The confirmed stop releases the run mark before it ends the stop hold.
			b.waitKey(t, "c-1", marotte.KeyStop, false)
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		// A purge pass while a person's close is still listing the runs to stop finds the close's stop hold where the
		// tab fact was: the chat is kept, and purged only once the stop has ended and the chat is idle.
		"close_purge_during_stop_list": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "running", sess("c-1")))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			b.age(t, "c-1", seamIdle)
			b.ageMark(t, "c-1", runKey("wf_1"), seamIdle)
			eng.inspected(t, "wf_1", "cancelled", sess("c-1"))
			entered, release := eng.block(t, kasList)
			done := make(chan struct{})
			go func() {
				defer close(done)
				b.personClose(t, "c-1")
			}()
			entered()
			b.chatPass(t)
			b.kept(t, "c-1", sess("c-1"))
			if keys := b.keys(t, "c-1"); keys[marotte.KeyStop].Since == 0 || keys[marotte.KeyTab].Since != 0 {
				t.Errorf("keep during the close's run list = %v, want the stop hold and no tab fact", keys)
			}
			release()
			<-done
			b.waitKey(t, "c-1", marotte.KeyStop, false)
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
		}},
		// A person's close of a chat whose record cannot be read still stops the run its lease names it as launching.
		"close_unreadable_record_stops_leased_runs": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-1", sess("c-1"))
			eng.list(t, seamRow("wf_1", "running", sess("c-1")))
			eng.inspected(t, "wf_1", "cancelled", sess("c-1"))
			// The lease marotte's own launch from c-1 grants.
			if err := b.leases.Put(t.Context(), &runlease.Lease{
				WorkflowID: "wf_1", ChatID: "c-1", ParentSessionID: sess("c-1"), Origin: runlease.OriginAgent,
				StartedAt: time.Now(),
			}); err != nil {
				t.Fatalf("Setup: lease: %v", err)
			}
			writeFixture(t, filepath.Join(root, "chats", "c-1", "chat.json"), "{not json")

			b.personClose(t, "c-1")

			if !eng.called(t, kasCancel, "wf_1") {
				t.Error("a person's close of an unreadable chat did not cancel the run its lease names")
			}
			if !logs.has("close: the chat could not be read; stopping only the runs its leases name") {
				t.Error("the unreadable record was not reported on the close path")
			}
		}},
		// A chat that holds a run's mark only because the run's owner scan could not read it is not the run's
		// launcher: its close cancels and deletes nothing.
		"close_of_doubt_holder_stops_nothing": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.create(t, "c-launch", sess("c-launch"))
			b.create(t, "c-by", sess("c-by"))
			eng.list(t, seamRow("wf_1", "running", sess("c-launch")))
			header := filepath.Join(root, "chats", "c-by", "chat.json")
			saved, err := os.ReadFile(header)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			writeFixture(t, header, "{not json")
			b.runStart(t, "wf_1", sess("c-launch"))
			b.waitKey(t, "c-launch", runKey("wf_1"), true)
			writeFixture(t, header, string(saved))
			if l, held := b.leases.Get("wf_1"); !held || !slices.Contains(l.KeepChats, "c-by") {
				t.Fatalf("Setup: the run's lease does not hold c-by in doubt: %+v", l)
			}

			b.personClose(t, "c-by")

			if eng.called(t, kasCancel, "wf_1") || eng.called(t, kasDelete, "wf_1") {
				t.Error("closing a chat that held the run's mark only in doubt stopped a run c-launch launched")
			}
		}},
		// A record from before keep facts existed ages one window from the fill-in.
		"fill_in_floor": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b1 := bootSeam(t, root, eng, logs, seamOpts{})
			b1.seed(t, "c-old", sess("c-old"))
			b1.shutdown(t)
			makePreUpgrade(t, filepath.Join(root, "chats"), "c-old")
			ageRecord(t, root, "c-old", 10*seamIdle)
			b2 := bootSeam(t, root, eng, logs, seamOpts{})
			b2.pass(t)
			b2.kept(t, "c-old", sess("c-old"))
			b2.age(t, "c-old", seamIdle)
			b2.pass(t)
			b2.purged(t, "c-old", sess("c-old"))
		}},
	}
	for name, cell := range cells {
		t.Run(name, func(t *testing.T) {
			root := seamRoot(t)
			eng := newSeamEngine(t)
			logs := captureSeamLogs(t)
			cell.run(t, root, eng, logs)
		})
	}
}
