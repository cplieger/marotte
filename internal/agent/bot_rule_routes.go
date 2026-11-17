package agent

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/command"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rule"
	"github.com/cplieger/marotte/internal/ruleengine"
)

// ruleTestsPerMinute is Test this rule's rate per Bot, one keyed bucket on the chat id: marotte has no HTTP session
// to key on.
const ruleTestsPerMinute = 30

const (
	msgRuleTestRefused  = "This rule can't be tested as it is."
	msgTooManyRuleTests = "Too many tests in a minute."
	msgRuleTestFailed   = "Couldn't test this rule"
	errSampleOrLast     = "Send either a sample event or the last real event, not both"
	errNoEvent          = "Pick the event to test with"
	errPickTrigger      = "Pick which trigger the event comes from"
	errNoSuchTrigger    = "This rule has no such trigger"
	errNoLastEvent      = "This trigger has no real event yet; test with a sample"
)

// handleRuleCatalog serves GET /api/bots/rule-catalog: what the editor offers, from the tables validation reads.
func handleRuleCatalog(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, rule.NewCatalog())
}

// ruleTestRequest is POST /api/bots/{id}/rules/test's body. Spec is raw so rule.Decode names a mistyped field by its
// path.
type ruleTestRequest struct {
	Sample    map[string]any  `json:"sample"`
	TriggerID string          `json:"trigger_id"`
	Spec      json.RawMessage `json:"spec"`
	UseLast   bool            `json:"use_last"`
}

// handleRuleTest serves POST /api/bots/{id}/rules/test: a dry run of the spec as the editor holds it, which sends,
// writes and moves nothing.
func (rt *Runtime) handleRuleTest(w http.ResponseWriter, r *http.Request) {
	c, ok := rt.botRecord(w, r)
	if !ok {
		return
	}
	if ok, wait := rt.ruleTests.Allow(c.ID); !ok {
		httpreply.TooManyRequests(w, msgTooManyRuleTests, wait)
		return
	}
	var body ruleTestRequest
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	d, bad := planRuleTest(c, &body)
	if len(bad) > 0 {
		httpreply.BadRequestFields(w, msgRuleTestRefused, bad)
		return
	}
	id := marotte.ChatID(c.ID)
	d.Now, d.Live, d.Waiting, d.Ready = time.Now(), rt.TurnLive(id), rt.bus.chatStatus.waiting(id), rt.engine.isReady()
	answer, err := ruleengine.Test(&d)
	if err != nil {
		httpreply.ServerError(w, msgRuleTestFailed, err)
		return
	}
	httpserver.WriteJSON(w, answer)
}

// planRuleTest reads body against c's record into a dry run, or answers every refused field at once.
func planRuleTest(c *marotte.Chat, body *ruleTestRequest) (ruleengine.DryRun, []httpreply.FieldError) {
	var bad []httpreply.FieldError
	refuse := func(field, msg string) { bad = append(bad, httpreply.FieldError{Field: field, Error: msg}) }
	switch {
	case body.Sample != nil && body.UseLast:
		refuse("sample", errSampleOrLast)
	case body.Sample == nil && !body.UseLast:
		refuse("sample", errNoEvent)
	}
	spec, errs := rule.Decode(body.Spec)
	for _, e := range errs {
		refuse(joinField("spec", e.Field), e.Error)
	}
	if len(errs) > 0 {
		return ruleengine.DryRun{}, bad
	}
	saved := savedRule(c.Bot.Rules, &spec)
	var ctx rule.ValidateContext
	if saved >= 0 {
		ctx.Kept = triggerIDs(&c.Bot.Rules[saved].Spec)
	}
	for _, e := range command.ValidateRuleSpec(spec, ctx) {
		refuse(joinField("spec", e.Field), e.Error)
	}
	t, ok := testedTrigger(&spec, body.TriggerID, refuse)
	if len(bad) > 0 || !ok {
		return ruleengine.DryRun{}, bad
	}
	d := ruleengine.DryRun{Chat: c, Spec: spec, Saved: saved, Trigger: t.ID}
	d.Data = testEvent(c, saved, t, body, refuse)
	return d, bad
}

// testEvent is the event a dry run evaluates: trigger t's stored last real event, or the body's sample.
func testEvent(c *marotte.Chat, saved int, t rule.Trigger, body *ruleTestRequest, refuse func(field, msg string)) json.RawMessage {
	if body.UseLast {
		var last json.RawMessage
		if saved >= 0 {
			last = c.Bot.Rules[saved].Status.Triggers[t.ID].LastEvent
		}
		if len(last) == 0 {
			refuse("use_last", errNoLastEvent)
		}
		return last
	}
	data, err := rule.SampleEvent(t, body.Sample)
	if err != nil {
		refuse("sample", err.Error())
	}
	return data
}

// testedTrigger is the spec's trigger the event comes from: id, or the only trigger when id is empty.
func testedTrigger(spec *rule.Spec, id string, refuse func(field, msg string)) (rule.Trigger, bool) {
	if id == "" {
		if len(spec.Triggers) == 1 {
			return spec.Triggers[0], true
		}
		refuse("trigger_id", errPickTrigger)
		return rule.Trigger{}, false
	}
	i := slices.IndexFunc(spec.Triggers, func(t rule.Trigger) bool { return t.ID == id })
	if i < 0 {
		refuse("trigger_id", errNoSuchTrigger)
		return rule.Trigger{}, false
	}
	return spec.Triggers[i], true
}

// savedRule is the index of the stored rule owning one of spec's minted trigger ids, -1 for a rule not yet saved.
// The request names no rule id: a minted trigger id is unique within the Bot.
func savedRule(rules []rule.Rule, spec *rule.Spec) int {
	for _, t := range spec.Triggers {
		for i := range rules {
			if triggerIDs(&rules[i].Spec)[t.ID] {
				return i
			}
		}
	}
	return -1
}

func triggerIDs(spec *rule.Spec) map[string]bool {
	set := make(map[string]bool, len(spec.Triggers))
	for _, t := range spec.Triggers {
		set[t.ID] = true
	}
	return set
}

func joinField(prefix, field string) string {
	if field == "" {
		return prefix
	}
	return prefix + "." + field
}
