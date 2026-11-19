package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ruleengine"
)

const botName = "Triage bot"

var weeklyRule = &marotte.Caller{Kind: marotte.CallerKindRule, Label: "Weekly questions", Rule: "r1", Event: "e1", Depth: 1}

// newBotPushHub is newWithholdHub with c1 a started Bot.
func newBotPushHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	h, fp := newWithholdHub(t)
	h.coord.chatStore.(*testChatStore).seed(t, "c1", func(c *marotte.Chat) {
		c.Name = botName
		c.Bot = &marotte.Bot{Goal: "Keep the queue short", StartedAt: 1}
	})
	return h, fp
}

func TestPushTurnOutcome_PicksTheKindFromTheTurnsAutomaticMark(t *testing.T) {
	for name, tc := range map[string]struct {
		caller    *marotte.Caller
		automatic bool
		stop      marotte.StopReason
		wantKind  marotte.PushKind
		wantBody  string
	}{
		"a rule's clean turn notifies under its own switch": {
			caller: weeklyRule, automatic: true, stop: marotte.StopReasonEndTurn,
			wantKind: marotte.PushKindBotRule, wantBody: "Response complete · Rule: Weekly questions · Read the queue",
		},
		"the wake after a rule's run notifies under its own switch": {
			automatic: true, stop: marotte.StopReasonEndTurn,
			wantKind: marotte.PushKindBotRule, wantBody: "Response complete · Read the queue",
		},
		"a rule's broken turn notifies under agent_finished": {
			caller: weeklyRule, automatic: true, stop: marotte.StopReasonError,
			wantKind: marotte.PushKindAgentFinished, wantBody: "Turn failed",
		},
		"the user's turn in a Bot notifies under agent_finished": {
			stop:     marotte.StopReasonEndTurn,
			wantKind: marotte.PushKindAgentFinished, wantBody: "Response complete · Read the queue",
		},
		"an API caller's turn in a Bot notifies under agent_finished": {
			caller: &marotte.Caller{Label: "mailer"}, stop: marotte.StopReasonEndTurn,
			wantKind: marotte.PushKindAgentFinished, wantBody: "Response complete · Read the queue",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, fp := newBotPushHub(t)

			h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(tc.stop), "Read the queue", tc.caller, tc.automatic)

			got := awaitRunPush(t, fp)
			if got.kind != tc.wantKind {
				t.Errorf("push kind = %q, want %q", got.kind, tc.wantKind)
			}
			if !strings.HasPrefix(got.body, tc.wantBody) {
				t.Errorf("push body = %q, want it to start %q", got.body, tc.wantBody)
			}
			if got.title != botName || got.subject != marotte.ChatSubject("c1") {
				t.Errorf("push title %q subject %+v, want the Bot's tab", got.title, got.subject)
			}
		})
	}
}

// A launched run's own push says nothing about a Bot's turn failing, so in a Bot the live-run withhold covers clean
// turns only.
func TestPushTurnOutcome_ABotsLiveRunWithholdsOnlyItsCleanTurns(t *testing.T) {
	for name, tc := range map[string]struct {
		caller    *marotte.Caller
		automatic bool
		stop      marotte.StopReason
		wantPush  bool
	}{
		"a rule's clean turn is withheld":     {caller: weeklyRule, automatic: true, stop: marotte.StopReasonEndTurn},
		"the user's clean turn is withheld":   {stop: marotte.StopReasonEndTurn},
		"a rule's broken turn still pushes":   {caller: weeklyRule, automatic: true, stop: marotte.StopReasonError, wantPush: true},
		"the user's broken turn still pushes": {stop: marotte.StopReasonError, wantPush: true},
	} {
		t.Run(name, func(t *testing.T) {
			h, fp := newBotPushHub(t)
			seedRunLease(t, h, "wf_1", "c1")

			h.coord.pushTurnOutcome(t.Context(), "c1", marotte.ConcludeStopReason(tc.stop), "", tc.caller, tc.automatic)

			if !tc.wantPush {
				awaitNoPush(t, h, fp, "the run the Bot launched pushes its own outcome")
				return
			}
			got := awaitRunPush(t, fp)
			if got.kind != marotte.PushKindAgentFinished || !strings.HasPrefix(got.body, "Turn failed") {
				t.Errorf("push %q kind %q, want Turn failed under agent_finished", got.body, got.kind)
			}
		})
	}
}

func TestRuleDeps_NotDeliveredPushesOnTheBotsTab(t *testing.T) {
	h, fp := newBotPushHub(t)
	h.RuleDeps(nil).NotDelivered(t.Context(), "c1", "Weekly questions", errors.New("the chat is starting"))

	got := awaitRunPush(t, fp)
	if got.kind != marotte.PushKindAgentFinished {
		t.Errorf("push kind = %q, want agent_finished: a broken automatic prompt reaches the user by default", got.kind)
	}
	if want := "Rule could not run · Weekly questions: the chat is starting"; got.body != want {
		t.Errorf("push body = %q, want %q", got.body, want)
	}
	if got.title != botName || got.subject != marotte.ChatSubject("c1") || got.chat != "c1" {
		t.Errorf("push title %q subject %+v chat %q, want the Bot's tab", got.title, got.subject, got.chat)
	}
}

// botTurnHub is a runtime over a ready bridge for c1, a started Bot, with a rule engine wired and a push service
// that records every push, so a turn's close reaches the push through the production path.
func botTurnHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	fp := newRunOutcomePush()
	h, br := hubForFSTest(t, t.TempDir(), WithPush(fp))
	br.callResults = map[string]json.RawMessage{marotte.MethodSessionSteer: json.RawMessage(`{"queued":true}`)}
	h.SetRuleEngine(ruleengine.New(&ruleengine.Deps{WakeHold: wakeHoldBound}))
	if _, err := h.chatStore.Mutate(t.Context(), "c1", func(c *marotte.Chat, _ bool) bool {
		c.Name = botName
		c.Bot = &marotte.Bot{Goal: "Keep the queue short", StartedAt: 1}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return h, fp
}

// awaitTurnPush is the next push that is not a run's own outcome.
func awaitTurnPush(t *testing.T, fp *runOutcomePush) runPushSent {
	t.Helper()
	for {
		if got := awaitRunPush(t, fp); got.kind != marotte.PushKindRunOutcome {
			return got
		}
	}
}

// sayInOwnTurn has c1's own turn say something: a silent end_turn grades empty and pushes nothing.
func sayInOwnTurn(t *testing.T, h *Runtime) {
	t.Helper()
	log, ok := h.coord.OwnTurn("c1")
	if !ok {
		t.Fatal("Setup: c1 has no own turn to speak in")
	}
	sayText(t, log)
}

func closeWakeTurn(t *testing.T, h *Runtime) {
	t.Helper()
	sayInOwnTurn(t, h)
	h.translateACPEvent("c1", h.originOf("c1"), newTurnEndMsg("end_turn"))
	waitFor(t, func() bool { return !h.TurnLive("c1") })
}

func wantTurnPush(t *testing.T, got runPushSent, kind marotte.PushKind, body string) {
	t.Helper()
	if got.kind != kind || got.body != body {
		t.Errorf("push %q kind %q, want %q under %s", got.body, got.kind, body, kind)
	}
}

func TestTurnClose_ARulesCleanTurnPushesUnderBotRule(t *testing.T) {
	h, fp := botTurnHub(t)
	id, _ := h.stagePromptTurnFrom(t, "c1", weeklyRule, nil)
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	sayInOwnTurn(t, h)
	closeDepthTurn(t, h, id)

	wantTurnPush(t, awaitTurnPush(t, fp), marotte.PushKindBotRule, "Response complete · Rule: Weekly questions")
}

// A rule that launches a workflow every 30 minutes wakes its Bot 48 times a day; with Automatic Bot turns off, those
// wakes must push nothing, so each one notifies under bot_rule.
func TestTurnClose_TheWakeAfterARulesRunPushesUnderBotRule(t *testing.T) {
	h, fp := botTurnHub(t)
	id := openRuleTurn(t, h, 1)
	sayInOwnTurn(t, h)
	closeDepthTurn(t, h, id)
	wantTurnPush(t, awaitTurnPush(t, fp), marotte.PushKindBotRule, "Response complete · Rule: Tidy")
	startDepthRun(t, h, "wf_auto")
	endRunAndWake(t, h, "wf_auto")
	closeWakeTurn(t, h)

	wantTurnPush(t, awaitTurnPush(t, fp), marotte.PushKindBotRule, "Response complete")
}

func TestTurnClose_TheWakeAfterAUsersRunPushesUnderAgentFinished(t *testing.T) {
	h, fp := botTurnHub(t)
	id, _ := h.stagePromptTurn(t, "c1")
	h.translateACPEvent("c1", h.originOf("c1"), newPromptTurnStartMsg(id))
	startDepthRun(t, h, "wf_user")
	sayInOwnTurn(t, h)
	closeDepthTurn(t, h, id)
	endRunAndWake(t, h, "wf_user")
	closeWakeTurn(t, h)

	wantTurnPush(t, awaitTurnPush(t, fp), marotte.PushKindAgentFinished, "Response complete")
}

// A person's steer hands the turn's asks to them, but the turn is still the one the rule started.
func TestTurnClose_ARuleTurnAPersonSteeredIntoStillPushesUnderBotRule(t *testing.T) {
	h, fp := botTurnHub(t)
	id := openRuleTurn(t, h, 1)
	if code := postSteer(t, h, "m-steer", "I'm here"); code != 200 {
		t.Fatalf("Setup: steer = %d, want 200", code)
	}
	waitFor(t, func() bool { return !h.coord.turns.stillUnattended("c1", id) })
	sayInOwnTurn(t, h)
	closeDepthTurn(t, h, id)

	wantTurnPush(t, awaitTurnPush(t, fp), marotte.PushKindBotRule, "Response complete · Rule: Tidy")
}
