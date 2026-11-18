package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
)

// The client's canned set must be what these handlers write: its read-budget tests replay it as the server.
func TestCannedAnswers_ReadAsTheHandlersWrite(t *testing.T) {
	type rulesAnswer struct {
		Rules []launchRuleView `json:"rules"`
	}
	testsupport.HoldCannedShape[hooksListResponse](t, "GET /api/hooks")
	testsupport.HoldCannedShape[rulesAnswer](t, "GET /api/launch-rules")
	// KAS's inspect reply is passed through; its own members are KAS's to change.
	testsupport.HoldCanned(t, "GET /api/runs/{run}?view=header", runViewCheck([]string{"workflowId", "state"}, true))
	testsupport.HoldCanned(t, "GET /api/runs/{run}?view=glance",
		runViewCheck([]string{"workflowId", "state", "nodePlan"}, false))
}

// runViewCheck holds a run view to withRunExtras: each member past the inspect reply's is one runExtras.keys can
// write, reading back as the type it writes there, and every member it always writes is present. workflowId and
// state are required because the client's RunInspectReply reads them.
func runViewCheck(inspect []string, noLog bool) func(a *testsupport.CannedAnswer) error {
	now := time.Now()
	every := &runExtras{
		autoResumeStoppedAt: now, silentSince: now, plan: &marotte.RunPlanUpdate{},
		controls: &marotte.RunControlsResponse{}, rev: 1,
	}
	written := map[string]reflect.Type{}
	for _, k := range every.keys() {
		written[k.name] = reflect.TypeOf(k.v)
	}
	required := []string{"workflowId", "state"}
	for _, k := range (&runExtras{noLog: noLog}).keys() {
		required = append(required, k.name)
	}
	return func(a *testsupport.CannedAnswer) error {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(a.Body, &members); err != nil {
			return fmt.Errorf("is no object: %w", err)
		}
		for _, name := range required {
			if _, ok := members[name]; !ok {
				return fmt.Errorf("carries no %s", name)
			}
		}
		for name, raw := range members {
			if slices.Contains(inspect, name) {
				continue
			}
			typ, ok := written[name]
			if !ok {
				return fmt.Errorf("carries %q, which withRunExtras does not write", name)
			}
			if noLog && (name == "step_ends" || name == "step_starts") {
				return fmt.Errorf("carries %q, which the header view leaves out", name)
			}
			if err := testsupport.ReadsBackAs(raw, reflect.New(typ).Interface()); err != nil {
				return fmt.Errorf("%s %w", name, err)
			}
		}
		return nil
	}
}
