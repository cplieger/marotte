package composition

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/marotte"
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
			if !b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt) {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.kept(t, "c-1", sess("c-1"))
		}},
		"turn_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			if !b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt) {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.h.ReleaseTurnReservation("c-1")
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
			if !b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt) {
				t.Fatal("Setup: the turn reservation was refused")
			}
			b.h.ReleaseTurnReservation("c-1")
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
		"run_unset": {func(t *testing.T, root string, eng *seamEngine, logs *seamLogs) {
			b := bootSeam(t, root, eng, logs, seamOpts{})
			b.seed(t, "c-1", sess("c-1"))
			b.runStart(t, "wf_1", sess("c-1"))
			b.waitKey(t, "c-1", runKey("wf_1"), true)
			b.runComplete(t, "wf_1")
			b.waitKey(t, "c-1", runKey("wf_1"), false)
			b.age(t, "c-1", seamIdle)
			b.pass(t)
			b.purged(t, "c-1", sess("c-1"))
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
			if got := b.h.ReserveTurnForPrompt(t.Context(), "c-1", 0); got != command.AdmissionChatGone {
				t.Errorf("a prompt on the purged chat = %v, want AdmissionChatGone", got)
			}
			if b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt) {
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
			if got := b.h.ReserveTurnForPrompt(t.Context(), "c-1", time.Second); got != command.AdmissionAcquired {
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
			if b.h.TryReserveTurn("c-1", marotte.TurnSourcePrompt) {
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
		// A run whose lease the previous boot held keeps its launcher until its terminal row is seen.
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
			b3.waitKey(t, "c-1", runKey("wf_1"), false)
			b3.age(t, "c-1", seamIdle)
			b3.pass(t)
			b3.purged(t, "c-1", sess("c-1"))
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
