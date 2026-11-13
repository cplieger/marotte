package workflow

import (
	"encoding/json"
	"testing"
)

func TestIndexRead(t *testing.T) {
	raw := []byte(`{"workflowId":"wf","nodePlan":{"x":1},"state":{"status":"running","root":` +
		`{"nodeId":"loop","children":[{"nodeId":"loop#0","children":[{"nodeId":"build","type":"step"}],"iteration":0}],` +
		`"type":"repeat"},"pause":null}}`)

	ix, err := IndexRead(raw)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a repeat pass is keyed by its frame spelling, whatever its member order", func(t *testing.T) {
		build, ok := ix.Nodes[PathKey([]string{"loop", "iter-0", "build"})]
		if !ok {
			t.Fatalf("nodes = %v, want loop/iter-0/build", ix.Nodes)
		}
		if build.Parent != PathKey([]string{"loop", "iter-0"}) || build.Index != 0 {
			t.Errorf("build sits at %q #%d", build.Parent, build.Index)
		}
	})
	t.Run("a node's own members leave its children out", func(t *testing.T) {
		pass := ix.Nodes[PathKey([]string{"loop", "iter-0"})]
		var own map[string]json.RawMessage
		if err := json.Unmarshal(pass.Own, &own); err != nil {
			t.Fatalf("own = %s: %v", pass.Own, err)
		}
		if _, ok := own["children"]; ok || string(own["iteration"]) != "0" {
			t.Errorf("own = %s", pass.Own)
		}
	})
	t.Run("the head is every state member but root, in order", func(t *testing.T) {
		if len(ix.Head) != 2 || ix.Head[0].Name != "status" || ix.Head[1].Name != "pause" {
			t.Errorf("head = %v", ix.Head)
		}
		if string(ix.Plan) != `{"x":1}` {
			t.Errorf("plan = %s", ix.Plan)
		}
	})
}

func TestIndexRead_RefusesANonObject(t *testing.T) {
	if _, err := IndexRead([]byte(`[1]`)); err == nil {
		t.Error("IndexRead([1]) answered no error")
	}
}
