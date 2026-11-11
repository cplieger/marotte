package workflow

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// glanceFixture is a run whose lane repeat ran three passes; each pass holds one build step and,
// in the latest, an inner review repeat of two passes.
const glanceFixture = `{"workflowId":"wf","state":{"status":"running","root":{"nodeId":"wf","type":"sequence","status":"running","children":[
 {"nodeId":"lane","type":"repeat","status":"running","maxIterations":9,"children":[
  {"nodeId":"lane#0","type":"sequence","iteration":0,"status":"completed","children":[
   {"nodeId":"build","type":"step","status":"completed","startedAt":"2026-01-01T10:00:00Z","endedAt":"2026-01-01T10:05:00Z","credits":[{"usage":1.5,"unit":"credit"},{"usage":9,"unit":"token"}]}]},
  {"nodeId":"lane#1","type":"sequence","iteration":1,"status":"completed","children":[
   {"nodeId":"build","type":"step","status":"completed","startedAt":"2026-01-01T11:00:00Z","endedAt":null,"credits":[{"usage":2}]}]},
  {"nodeId":"lane#2","type":"sequence","iteration":2,"status":"running","children":[
   {"nodeId":"build","type":"step","status":"completed","startedAt":"2026-01-01T12:00:00Z","endedAt":"2026-01-01T12:01:00Z","credits":[{"usage":4}]},
   {"nodeId":"review","type":"repeat","status":"running","children":[
    {"nodeId":"review#0","type":"sequence","iteration":0,"status":"completed","children":[]},
    {"nodeId":"review#1","type":"sequence","iteration":1,"status":"running","children":[
     {"nodeId":"gate","type":"step","status":"running","startedAt":"2026-01-01T12:02:00Z"}]}]}]}]}]}},
 "nodePlan":{"nodeId":"wf"}}`

func ms(t *testing.T, stamp string) int64 {
	t.Helper()
	v, ok := unixMilli(stamp)
	if !ok {
		t.Fatalf("unparsable %q", stamp)
	}
	return v
}

func TestGlanceRead(t *testing.T) {
	lane0 := PathKey([]string{"wf", "lane", "iter-0", "build"})
	starts := map[string]marotte.RunStepStart{
		// The log's start is the attempt's, earlier than KAS's restamped one.
		lane0: {StartedAt: "2026-01-01T09:30:00Z", Ended: true},
	}
	out, paths, err := GlanceRead([]byte(glanceFixture), starts)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		State struct {
			Status string `json:"status"`
			Root   struct {
				Children []struct {
					MaxIterations int `json:"maxIterations"`
					Children      []struct {
						NodeID   string `json:"nodeId"`
						Children []struct {
							NodeID        string                    `json:"nodeId"`
							Children      []struct{ NodeID string } `json:"children"`
							EarlierPasses *EarlierPasses            `json:"earlierPasses"`
							Credits       json.RawMessage           `json:"credits"`
						} `json:"children"`
					} `json:"children"`
					EarlierPasses *EarlierPasses `json:"earlierPasses"`
				} `json:"children"`
			} `json:"root"`
		} `json:"state"`
		NodePlan json.RawMessage `json:"nodePlan"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("the glance is not JSON: %v\n%s", err, out)
	}
	if doc.State.Status != "running" || string(doc.NodePlan) != `{"nodeId":"wf"}` {
		t.Errorf("head or plan not copied: %s", out)
	}
	if len(doc.State.Root.Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(doc.State.Root.Children))
	}
	lane := doc.State.Root.Children[0]
	if lane.MaxIterations != 9 {
		t.Errorf("the repeat's own members were not kept: %s", out)
	}
	if len(lane.Children) != 1 || lane.Children[0].NodeID != "lane#2" {
		t.Fatalf("the lane's passes = %+v, want only lane#2", lane.Children)
	}
	want := EarlierPasses{
		Count:   2,
		Credits: 3.5,
		From:    ms(t, "2026-01-01T09:30:00Z"),
		To:      ms(t, "2026-01-01T11:00:00Z"),
	}
	if lane.EarlierPasses == nil || *lane.EarlierPasses != want {
		t.Errorf("earlierPasses = %+v, want %+v", lane.EarlierPasses, want)
	}
	inner := lane.Children[0].Children
	if len(inner) != 2 || inner[1].NodeID != "review" {
		t.Fatalf("the latest pass's members = %+v", inner)
	}
	if len(inner[1].Children) != 1 || inner[1].Children[0].NodeID != "review#1" {
		t.Errorf("the inner repeat kept %+v, want only review#1", inner[1].Children)
	}
	if p := inner[1].EarlierPasses; p == nil || *p != (EarlierPasses{Count: 1}) {
		t.Errorf("the inner repeat's earlierPasses = %+v, want one pass with no work", p)
	}
	if string(inner[0].Credits) != `[{"usage":4}]` {
		t.Errorf("a kept node's credits = %s, want copied", inner[0].Credits)
	}
	for _, key := range [][]string{
		{"wf"},
		{"wf", "lane"},
		{"wf", "lane", "iter-2"},
		{"wf", "lane", "iter-2", "build"},
		{"wf", "lane", "iter-2", "review", "iter-1", "gate"},
	} {
		if !paths[PathKey(key)] {
			t.Errorf("paths lacks %v", key)
		}
	}
	for _, key := range [][]string{{"wf", "lane", "iter-0"}, {"wf", "lane", "iter-1", "build"}, {"wf", "lane", "iter-2", "review", "iter-0"}} {
		if paths[PathKey(key)] {
			t.Errorf("paths names the folded %v", key)
		}
	}
}

func TestGlanceRead_AFoldedStepWithNoEndKeepsTheWindowOpen(t *testing.T) {
	raw := `{"state":{"root":{"nodeId":"wf","type":"repeat","status":"running","children":[
	 {"nodeId":"wf#0","type":"sequence","iteration":0,"status":"aborted","children":[
	  {"nodeId":"s","type":"step","status":"aborted","startedAt":"2026-01-01T10:00:00Z"}]},
	 {"nodeId":"wf#1","type":"sequence","iteration":1,"status":"running"}]}}}`
	out, _, err := GlanceRead([]byte(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		State struct {
			Root struct {
				EarlierPasses EarlierPasses `json:"earlierPasses"`
			} `json:"root"`
		} `json:"state"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.State.Root.EarlierPasses; !got.Open || got.From != ms(t, "2026-01-01T10:00:00Z") {
		t.Errorf("earlierPasses = %+v, want open from the step's start", got)
	}
}

func TestGlanceRead_CopiesARootThatIsNotAnObject(t *testing.T) {
	out, _, err := GlanceRead([]byte(`{"state":{"root":[1]}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"state":{"root":[1]}}` {
		t.Errorf("a non-object root = %s, want it copied", out)
	}
	if _, _, err := GlanceRead([]byte(`[1]`), nil); err == nil {
		t.Error("a reply that is not an object was accepted")
	}
}
