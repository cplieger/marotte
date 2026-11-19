package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/launchrule"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/ruleengine"
	"github.com/cplieger/marotte/internal/runlease"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/marotte/internal/testsupport"
	"github.com/cplieger/marotte/internal/translate"
)

const (
	floorPolicyAsk = `{"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"always","kind":"allow_always"},` +
		`{"optionId":"reject","kind":"reject_once"},{"optionId":"never","kind":"reject_always"}],` +
		`"_meta":{"kiro":{"toolId":"fs_write","consent":{"askType":"implicit","capability":"fs_write"}}}}`
	floorAdminAsk = `{"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}],` +
		`"_meta":{"kiro":{"toolId":"fs_write","consent":{"scope":"administration"}}}}`
	floorClassifierAsk = `{"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}],` +
		`"_meta":{"kiro":{"toolId":"fs_write","trustClassifier":{"reason":"breaker"}}}}`
	floorNoAllowAsk = `{"options":[{"optionId":"reject","kind":"reject_once"}],` +
		`"_meta":{"kiro":{"toolId":"fs_write","consent":{"askType":"implicit","capability":"fs_write"}}}}`
)

// floorHub is c1 over a responding bridge with a rule engine wired, the ask budget shortened and Settings holding
// keys.
func floorHub(t *testing.T, keys map[string]any) (*Runtime, *respondingBridge) {
	t.Helper()
	body, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	return floorHubConfig(t, body)
}

// floorHubConfig is floorHub with config.json holding body, or absent when body is nil.
func floorHubConfig(t *testing.T, body []byte) (*Runtime, *respondingBridge) {
	t.Helper()
	shortAskBudget(t, 20*time.Millisecond)
	dir := t.TempDir()
	if body != nil {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, br := hubForFSTest(t, t.TempDir(), WithConfigDir(dir))
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
	h.SetRuleEngine(ruleengine.New(&ruleengine.Deps{WakeHold: wakeHoldBound}))
	return h, br
}

func shortAskBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := unattendedAskBudget
	unattendedAskBudget = d
	t.Cleanup(func() { unattendedAskBudget = prev })
}

// openedWith records the presets c1's session opened with, as its door does.
func openedWith(t *testing.T, h *Runtime, profile string) {
	t.Helper()
	p, ok := policyfile.ProfileFor(profile)
	if !ok {
		t.Fatalf("no %s profile", profile)
	}
	h.bridge.mgr.get("c1").recordOpenedPresets(p.Presets)
}

// forgetOpenedPresets leaves c1's session with no presets recorded, as before its door finished.
func forgetOpenedPresets(h *Runtime) {
	sb := h.bridge.mgr.get("c1")
	sb.mu.Lock()
	sb.openedPresets, sb.presetsKnown = nil, false
	sb.mu.Unlock()
}

func sendFloorAsk(t *testing.T, h *Runtime, id int64, params string) {
	t.Helper()
	h.routeInboundRequest(t.Context(), "c1", h.originOf("c1"), &marotte.RPCResponse{
		Method: marotte.MethodRequestPermission, ID: &id, Params: json.RawMessage(params),
	})
}

// floorAnswer is the outcome the floor sent, failing when it sent none within two seconds.
func floorAnswer(t *testing.T, br *respondingBridge) *marotte.PermissionOutcome {
	t.Helper()
	select {
	case <-br.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the floor answered nothing, so the ask waits for a person who is not there")
	}
	br.respMu.Lock()
	got := br.response.result
	br.respMu.Unlock()
	outcome, ok := got.(*marotte.PermissionOutcome)
	if !ok {
		t.Fatalf("answered with %T (%v), want a permission outcome", got, got)
	}
	return outcome
}

// noFloorAnswer fails when anything answers the ask within many budgets.
func noFloorAnswer(t *testing.T, br *respondingBridge) {
	t.Helper()
	select {
	case <-br.done:
		t.Fatal("the floor answered an ask a person is there to answer")
	case <-time.After(15 * unattendedAskBudget):
	}
}

func rejectionReasonOf(o *marotte.PermissionOutcome) string {
	if o.Meta == nil {
		return ""
	}
	return o.Meta.Kiro.RejectionReason
}

func botAnswers(t *testing.T, answer string) float64 {
	t.Helper()
	return testsupport.MetricValue(t, `marotte_bot_unattended_answers_total{answer="`+answer+`"}`)
}

// awaitBotAnswers waits for answer's count to pass base: the floor counts once the answer is delivered.
func awaitBotAnswers(t *testing.T, answer string, base float64) {
	t.Helper()
	waitFor(t, func() bool { return botAnswers(t, answer) > base })
}

func TestBotFloor_ARuleTurnsAskIsRefusedByDefault(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	refused, approved := botAnswers(t, "refused"), botAnswers(t, "approved")
	sendFloorAsk(t, h, 7001, floorPolicyAsk)
	o := floorAnswer(t, br)
	awaitBotAnswers(t, "refused", refused)
	if got := botAnswers(t, "refused") - refused; got != 1 {
		t.Errorf("refused answers counted %v, want 1", got)
	}
	if got := botAnswers(t, "approved") - approved; got != 0 {
		t.Errorf("approved answers counted %v, want 0", got)
	}
	if o.Outcome.OptionID != "reject" {
		t.Errorf("optionId = %q, want the one-shot reject", o.Outcome.OptionID)
	}
	// The note is the Bot turn's, so the agent reads why: a turn nobody watches, not a run.
	reason := rejectionReasonOf(o)
	if !strings.Contains(reason, "no one watching") || strings.Contains(reason, "run") {
		t.Errorf("rejectionReason = %q, want the Bot turn's own note", reason)
	}
}

// A config.json that is absent or not one JSON object reads the switch as off.
func TestBotFloor_AnAbsentOrUnreadableSettingRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "absent"},
		{name: "unreadable", body: []byte(`{"bot_auto_approve":true`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := floorHubConfig(t, tc.body)
			openedWith(t, h, policyfile.ProfileTrusted)
			openRuleTurn(t, h, 1)
			sendFloorAsk(t, h, 7010, floorPolicyAsk)
			if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
				t.Errorf("optionId = %q, want reject", got)
			}
		})
	}
}

func TestBotFloor_TheSwitchApprovesOnceNeverAlways(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	approved := botAnswers(t, "approved")
	sendFloorAsk(t, h, 7002, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "once" {
		t.Errorf("optionId = %q, want allow_once's", got)
	}
	awaitBotAnswers(t, "approved", approved)
	if got := botAnswers(t, "approved") - approved; got != 1 {
		t.Errorf("approved answers counted %v, want 1", got)
	}
}

// The run floor's switch approves no Bot turn.
func TestBotFloor_TheScheduledSwitchApprovesNoBotTurn(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyScheduledAutoApprove: true})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	sendFloorAsk(t, h, 7003, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
		t.Errorf("optionId = %q, want reject", got)
	}
}

func TestBotFloor_TheSwitchStillRefusesWhatNeedsAPerson(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		params  string
	}{
		{name: "administrator", profile: policyfile.ProfileTrusted, params: floorAdminAsk},
		{name: "safety_check_delegated", profile: policyfile.ProfileDelegated, params: floorPolicyAsk},
		{name: "safety_check_stamped_not_delegated", profile: policyfile.ProfileTrusted, params: floorClassifierAsk},
		{name: "no_recorded_presets", params: floorPolicyAsk},
		{name: "no_allow_option", profile: policyfile.ProfileTrusted, params: floorNoAllowAsk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
			if tc.profile != "" {
				openedWith(t, h, tc.profile)
			} else {
				forgetOpenedPresets(h)
			}
			openRuleTurn(t, h, 1)
			sendFloorAsk(t, h, 7004, tc.params)
			if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
				t.Errorf("optionId = %q, want reject", got)
			}
		})
	}
}

func TestBotFloor_APersonsAnswerFirstWins(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	sendFloorAsk(t, h, 7005, floorPolicyAsk)
	ids := pendingAskIDs(t, h)
	if len(ids) != 1 {
		t.Fatalf("asks pending = %v, want the one ask", ids)
	}
	if _, ok := h.bus.TakePendingPerm("c1", ids[0], marotte.DecisionKindPermission, marotte.SettledByUser); !ok {
		t.Fatal("the ask was not pending for the person to claim")
	}
	noFloorAnswer(t, br)
}

func TestBotFloor_AUserSteerClearsTheMark(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	if code := postSteer(t, h, "m-steer", "look at the tests too"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	sendFloorAsk(t, h, 7006, floorPolicyAsk)
	noFloorAnswer(t, br)
}

// A steer kiro-cli refuses never reached the turn, so nobody watches it still and its row waits on the list.
func TestBotFloor_ARefusedUserSteerKeepsTheMark(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	h.coord.sendEntrySteer = func(context.Context, marotte.ChatID, *command.EntrySteer) (string, bool) {
		return command.SteerRefuseFull, false
	}
	if code := postSteer(t, h, "m-steer", "look at the tests too"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	waitFor(t, func() bool {
		for _, e := range listOf(h, "c1") {
			if e.Row != nil && e.Row.Text == "look at the tests too" && e.State == marotte.RunPending {
				return true
			}
		}
		return false
	})
	sendFloorAsk(t, h, 7016, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
		t.Errorf("optionId = %q, want the floor's reject", got)
	}
}

// A person's steer kiro-cli did not queue reached nobody: one it dropped, and one whose call failed.
func TestBotFloor_AnUnqueuedUserSteerKeepsTheMark(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(br *respondingBridge)
	}{
		{name: "dropped", arm: func(br *respondingBridge) {
			br.setCallResult(marotte.MethodSessionSteer, json.RawMessage(`{"queued":false,"dropped":"buffer_full"}`))
		}},
		{name: "bridge_error", arm: func(br *respondingBridge) {
			br.setCallErr(marotte.MethodSessionSteer, errors.New("kas gone"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := floorHub(t, map[string]any{})
			tc.arm(br)
			openedWith(t, h, policyfile.ProfileTrusted)
			openRuleTurn(t, h, 1)
			send, sent := h.coord.sendEntrySteer, make(chan bool, 1)
			h.coord.sendEntrySteer = func(ctx context.Context, chatID marotte.ChatID, s *command.EntrySteer) (string, bool) {
				refuse, accepted := send(ctx, chatID, s)
				sent <- accepted
				return refuse, accepted
			}
			if code := postSteer(t, h, "m-steer", "look at the tests too"); code != 200 {
				t.Fatalf("Setup: steer = %d, want 200", code)
			}
			if <-sent {
				t.Fatal("Setup: kiro-cli's reply was read as queued")
			}
			sendFloorAsk(t, h, 7018, floorPolicyAsk)
			if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
				t.Errorf("optionId = %q, want the floor's reject", got)
			}
		})
	}
}

// A person's steer accepted after its turn ended reached that turn, not the rule's next one.
func TestBotFloor_ASteerAcceptedAfterItsTurnEndedKeepsTheNextTurnsMark(t *testing.T) {
	// The stubbed send records no hold in kiro-cli, so the POST answers queued only once the admission wait passed.
	shrinkAdmissionWait(t, 50*time.Millisecond)
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	first := openRuleTurn(t, h, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	h.coord.sendEntrySteer = func(context.Context, marotte.ChatID, *command.EntrySteer) (string, bool) {
		close(entered)
		<-release
		return "", true
	}
	if code := postSteer(t, h, "m-steer", "look at the tests too"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	<-entered
	closeDepthTurn(t, h, first)
	openRuleTurn(t, h, 1)
	close(release)
	sendFloorAsk(t, h, 7017, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
		t.Errorf("optionId = %q, want the floor's reject", got)
	}
}

// The mark is read when the floor answers, so a person who steers in after the ask keeps it.
func TestBotFloor_AUserSteerAfterTheAskKeepsItForThem(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	shortAskBudget(t, time.Hour)
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	sendFloorAsk(t, h, 7014, floorPolicyAsk)
	turnID, marked := h.coord.turns.unattendedTurn("c1")
	if !marked {
		t.Fatal("Setup: the rule's turn is not marked")
	}
	if code := postSteer(t, h, "m-steer", "I'm here, wait for me"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	waitFor(t, func() bool { _, still := h.coord.turns.unattendedTurn("c1"); return !still })
	h.answerBotTurn("c1", h.originOf("c1"), 7014, turnID, json.RawMessage(floorPolicyAsk))
	select {
	case <-br.done:
		t.Fatal("the floor answered an ask the person who steered in is there to answer")
	default:
	}
	if ids := pendingAskIDs(t, h); len(ids) != 1 {
		t.Errorf("asks pending = %v, want the one ask still waiting", ids)
	}
}

// A person who steers in while the floor reads its settings keeps the ask: the mark is read right before the claim.
func TestBotFloor_AUserSteerDuringTheSettingsReadKeepsItForThem(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	shortAskBudget(t, time.Hour)
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	sendFloorAsk(t, h, 7019, floorPolicyAsk)
	turnID, marked := h.coord.turns.unattendedTurn("c1")
	if !marked {
		t.Fatal("Setup: the rule's turn is not marked")
	}
	read := readBotAutoApprove
	t.Cleanup(func() { readBotAutoApprove = read })
	readBotAutoApprove = func(ctx context.Context, configDir string) bool {
		if code := postSteer(t, h, "m-steer", "I'm here, wait for me"); code != 200 {
			t.Errorf("Setup: steer = %d, want 200", code)
		}
		waitFor(t, func() bool { _, still := h.coord.turns.unattendedTurn("c1"); return !still })
		return read(ctx, configDir)
	}
	h.answerBotTurn("c1", h.originOf("c1"), 7019, turnID, json.RawMessage(floorPolicyAsk))
	select {
	case <-br.done:
		t.Fatal("the floor approved an ask the person who steered in is there to answer")
	default:
	}
	if ids := pendingAskIDs(t, h); len(ids) != 1 {
		t.Errorf("asks pending = %v, want the one ask still waiting", ids)
	}
}

// A program's prompt is not a rule's: the person who reads its chat answers its asks.
func TestBotFloor_AProgramsPromptOpensNoFlooredTurn(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	openedWith(t, h, policyfile.ProfileTrusted)
	id, _ := h.stagePromptTurnFrom(t, "c1", &marotte.Caller{Label: "grelot"}, nil)
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	sendFloorAsk(t, h, 7015, floorPolicyAsk)
	noFloorAnswer(t, br)
}

// A program's message into the turn is not a person watching it.
func TestBotFloor_AProgramsSteerKeepsTheMark(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	openRuleTurn(t, h, 1)
	if _, err := h.dispatcher.Notify(t.Context(), "c1", "1 new; run grelot inbox", &marotte.Caller{Label: "grelot"},
		command.NotifyOptions{}); err != nil {
		t.Fatalf("Setup: notify: %v", err)
	}
	sendFloorAsk(t, h, 7013, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
		t.Errorf("optionId = %q, want reject", got)
	}
}

func TestBotFloor_AUserTurnInABotIsNeverFloored(t *testing.T) {
	h, br := floorHub(t, map[string]any{settings.KeyBotAutoApprove: true})
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Bot = &marotte.Bot{Goal: "keep the docs tidy"}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	openedWith(t, h, policyfile.ProfileTrusted)
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	sendFloorAsk(t, h, 7007, floorPolicyAsk)
	noFloorAnswer(t, br)
}

// endRunAndWake ends wf on c1 and opens the prompt-less turn KAS wakes the chat with.
func endRunAndWake(t *testing.T, h *Runtime, workflowID string) {
	t.Helper()
	h.runs.observeComplete(t.Context(), "c1", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": workflowID, "status": "completed",
	}))
	h.translateACPEvent("c1", h.originOf("c1"), newTurnStartMsg())
	waitFor(t, func() bool { return h.TurnLive("c1") })
}

func TestBotFloor_TheWakeTurnAfterAnAutomaticRunIsFloored(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	endRunAndWake(t, h, "wf_auto")
	sendFloorAsk(t, h, 7008, floorPolicyAsk)
	if got := floorAnswer(t, br).Outcome.OptionID; got != "reject" {
		t.Errorf("optionId = %q, want reject", got)
	}
}

func TestBotFloor_TheWakeTurnAfterAUsersRunIsNotFloored(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	closeDepthTurn(t, h, id)
	endRunAndWake(t, h, "wf_user")
	sendFloorAsk(t, h, 7009, floorPolicyAsk)
	noFloorAnswer(t, br)
}

func TestBotFloor_AUserSteerIntoAWakeTurnClearsTheMark(t *testing.T) {
	h, br := floorHub(t, map[string]any{})
	openedWith(t, h, policyfile.ProfileTrusted)
	closeDepthTurn(t, h, openRuleTurn(t, h, 1))
	startDepthRun(t, h, "wf_auto")
	endRunAndWake(t, h, "wf_auto")
	if code := postSteer(t, h, "m-steer", "stop after this step"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	sendFloorAsk(t, h, 7010, floorPolicyAsk)
	noFloorAnswer(t, br)
}

// Until E5 deletes the run floor, both floors give one answer to one ask under their own switch.
func TestUnattendedFloors_AnswerOneAskAlike(t *testing.T) {
	keys := map[string]any{settings.KeyBotAutoApprove: true, settings.KeyScheduledAutoApprove: true}
	trusted, _ := policyfile.ProfileFor(policyfile.ProfileTrusted)
	for _, tc := range []struct {
		name   string
		params string
		want   string
	}{
		{name: "policy", params: floorPolicyAsk, want: "once"},
		{name: "administrator", params: floorAdminAsk, want: "reject"},
		{name: "classifier", params: floorClassifierAsk, want: "reject"},
		{name: "no_allow_option", params: floorNoAllowAsk, want: "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, br := floorHub(t, keys)
			openedWith(t, h, policyfile.ProfileTrusted)
			openRuleTurn(t, h, 1)
			sendFloorAsk(t, h, 7011, tc.params)
			if got := floorAnswer(t, br).Outcome.OptionID; got != tc.want {
				t.Errorf("chat floor optionId = %q, want %q", got, tc.want)
			}

			runChat := runChatID("wf_alike")
			h.insertBridgeNow(runChat, testBridge(br, bridgeReady))
			h.runs.grantLease(t.Context(), "wf_alike", "nightly",
				scheduledLaunch(launchrule.Row{ID: "sched-1"}, time.Time{}), &runlease.HostSecurity{Presets: trusted.Presets})
			id := int64(7012)
			h.bus.pendingPerms.add(id, marotte.NewEvent(marotte.EventPermissionNeeded, runChat,
				marotte.PermissionNeededPayload{RequestID: id}), br)
			h.runs.permissionWithUnattendedFloor(func(context.Context, marotte.ChatID, translate.AskOrigin, *marotte.RPCResponse) {})(
				t.Context(), runChat, br, &marotte.RPCResponse{Method: marotte.MethodRequestPermission, ID: &id, Params: json.RawMessage(tc.params)},
			)
			if got := floorAnswer(t, br).Outcome.OptionID; got != tc.want {
				t.Errorf("run floor optionId = %q, want %q", got, tc.want)
			}
		})
	}
}
