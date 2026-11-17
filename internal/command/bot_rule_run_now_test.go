package command

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/testsupport"
)

func runNowReq(t *testing.T, id marotte.ChatID, ruleID, opID string) *marotte.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(marotte.BotRuleRunNowCommand{RuleID: ruleID, OpID: opID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &marotte.ClientCommand{Type: marotte.CmdBotRuleRunNow, ChatID: id, Payload: payload}
}

func TestCmdBotRuleRunNow_RefusesWhatCannotRunAndAsksTheEngineNothing(t *testing.T) {
	cases := []struct {
		seed   func(t *testing.T, store *testsupport.InMemoryChatStore)
		name   string
		rule   string
		op     string
		reason string
		status int
	}{
		{
			name: "a regular chat", status: http.StatusConflict, reason: reasonBotNotFound, rule: "r-watch", op: "op-1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) { seedNamed(t, store, "c-bot", "Plain") },
		},
		{
			name: "a draft", status: http.StatusConflict, reason: reasonBotDraft, rule: "r-watch", op: "op-1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				b := watchedBot(marotte.Bot{})
				b.StartedAt = 0
				seedBot(t, store, "c-bot", "Watcher", b)
			},
		},
		{
			name: "a closed Bot", status: http.StatusConflict, reason: reasonBotClosed, rule: "r-watch", op: "op-1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{ClosedAt: 2}))
			},
		},
		{
			name: "a rule the Bot does not hold", status: http.StatusNotFound, reason: reasonRuleNotFound, rule: "r-gone", op: "op-1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
			},
		},
		{
			name: "no op_id", status: http.StatusBadRequest, rule: "r-watch",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
			},
		},
		{
			name: "an op_id that is no identifier", status: http.StatusBadRequest, rule: "r-watch", op: "op 1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
			},
		},
		{
			name: "no rule_id", status: http.StatusBadRequest, op: "op-1",
			seed: func(t *testing.T, store *testsupport.InMemoryChatStore) {
				seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			tc.seed(t, store)
			roles, eng := newRuleRoles(t, store)

			_, err := cmdBotRuleRunNow(t.Context(), roles, runNowReq(t, "c-bot", tc.rule, tc.op))

			if statusOf(err) != tc.status || (tc.reason != "" && reasonOf(err) != tc.reason) {
				t.Errorf("bot_rule_run_now = %d %q, want %d %q (%v)", statusOf(err), reasonOf(err), tc.status, tc.reason, err)
			}
			if len(eng.runs) != 0 {
				t.Errorf("a refused Run now reached the engine: %+v", eng.runs)
			}
		})
	}
}

func TestCmdBotRuleRunNow_AnswersTheEnginesOutcome(t *testing.T) {
	cases := []struct {
		answer RunNowAnswer
		name   string
		paused bool
	}{
		{name: "fired", answer: RunNowAnswer{Outcome: RunNowFired}},
		{name: "queued behind the Bot's turn", answer: RunNowAnswer{Outcome: RunNowQueued}},
		{name: "skipped with its reason", answer: RunNowAnswer{Outcome: RunNowSkipped, Reason: "this Bot is working"}},
		{name: "on a paused Bot, which Run now still reaches", paused: true, answer: RunNowAnswer{Outcome: RunNowQueued}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{Paused: tc.paused}))
			roles, eng := newRuleRoles(t, store)
			eng.answer = tc.answer

			body, err := cmdBotRuleRunNow(t.Context(), roles, runNowReq(t, "c-bot", "r-watch", "op-press"))
			if err != nil {
				t.Fatalf("bot_rule_run_now = %d %v, want 200", statusOf(err), err)
			}

			m, ok := body.(map[string]any)
			if !ok {
				t.Fatalf("answer %T, want a map", body)
			}
			if m["outcome"] != tc.answer.Outcome || (tc.answer.Reason != "" && m["reason"] != tc.answer.Reason) {
				t.Errorf("answer %v, want outcome %q reason %q", m, tc.answer.Outcome, tc.answer.Reason)
			}
			if _, has := m["reason"]; tc.answer.Reason == "" && has {
				t.Errorf("answer %v carries a reason for %q", m, tc.answer.Outcome)
			}
			if want := []runNowCall{{chat: "c-bot", rule: "r-watch", op: "op-press"}}; !slices.Equal(eng.runs, want) {
				t.Errorf("engine runs %+v, want %+v", eng.runs, want)
			}
			if got := watchStatus(t, store); len(got.Queue) != len(queueOf("t-watch")) {
				t.Errorf("the command wrote the queue itself (%d events); only the engine writes rule status", len(got.Queue))
			}
		})
	}
}

func TestCmdBotRuleRunNow_AnEngineRefusalKeepsItsStatus(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
	roles, eng := newRuleRoles(t, store)
	eng.err = StatusError(http.StatusServiceUnavailable, errors.New("starting"))

	_, err := cmdBotRuleRunNow(t.Context(), roles, runNowReq(t, "c-bot", "r-watch", "op-press"))

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("bot_rule_run_now = %d %v, want the engine's 503", statusOf(err), err)
	}
}

func TestCmdBotRuleRunNow_WithNoEngineIsUnavailable(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedBot(t, store, "c-bot", "Watcher", watchedBot(marotte.Bot{}))
	roles, _ := newBotRoles(t, store)

	_, err := cmdBotRuleRunNow(t.Context(), roles, runNowReq(t, "c-bot", "r-watch", "op-press"))

	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("bot_rule_run_now with no engine = %d %v, want 503", statusOf(err), err)
	}
}

// The starter rule is design's check-in, preselected on a new draft: a goal and Create bot give a Bot that checks
// in every two hours after its last reply.
func TestCmdCreateChat_ABotStartsWithTheStarterRuleUnarmed(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	id := createBot(t, mem, "op-bot")

	c := botOf(t, store, id)
	if len(c.Bot.Rules) != 1 {
		t.Fatalf("rules = %+v, want the starter rule alone", c.Bot.Rules)
	}
	r := c.Bot.Rules[0]
	if !ruleIDShape.MatchString(r.ID) {
		t.Errorf("rule id %q, want a minted rule id", r.ID)
	}
	s := r.Spec
	const prompt = "Re-read your goal and your memory file, check the workflows you launched, then take the next step " +
		"toward the goal. If nothing is due, answer in one line and stop."
	if s.Name != "Check in on the goal" || !s.Enabled || s.Prompt != prompt || s.Condition != nil {
		t.Errorf("spec name %q enabled %v condition %v prompt %q, want the starter rule's", s.Name, s.Enabled, s.Condition, s.Prompt)
	}
	if len(s.Triggers) != 1 {
		t.Fatalf("triggers %+v, want one", s.Triggers)
	}
	tr := s.Triggers[0]
	if tr.Kind != rule.KindEvery || tr.Every == nil || tr.Every.Minutes != 120 || tr.Every.Anchor != rule.AnchorIdle ||
		tr.Limits != nil || tr.At != nil || !triggerIDShape.MatchString(tr.ID) {
		t.Errorf("trigger %+v every %+v, want every 120 minutes after the last reply with no limits and a minted id", tr, tr.Every)
	}
	if st := r.Status.Triggers[tr.ID]; st.ArmedAt != 0 {
		t.Errorf("armed_at = %d, want a draft's rule unarmed until Start", st.ArmedAt)
	}
	if errs := ValidateRuleSpec(s, rule.ValidateContext{Kept: map[string]bool{tr.ID: true}}); len(errs) != 0 {
		t.Errorf("the starter rule fails validation: %v", errs)
	}
}

func TestCmdCreateChat_ARegularChatHasNoRules(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	mem, _, _ := newTabbedMembership(t, store)

	body, err := cmdCreateChat(t.Context(), mem, createReq(t, "", marotte.CreateChatCommand{OpID: "op-chat", Model: "model-a-5"}))
	if err != nil {
		t.Fatalf("create_chat = %v", err)
	}

	if c, _ := store.Get(t.Context(), chatIDOfResponse(t, body)); c.Bot != nil {
		t.Errorf("a regular chat has a bot block %+v", c.Bot)
	}
}
