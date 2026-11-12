package workflow

import "testing"

// A repeat's pass is addressed `iter-<n>` on the wire, so a step inside one is found by that path.
func TestCaptureAt_FindsAStepInsideARepeatsPass(t *testing.T) {
	raw := []byte(`{"state":{"root":{"nodeId":"wf","type":"sequence","children":[
		{"nodeId":"loop","type":"repeat","children":[
			{"nodeId":"loop#0","type":"sequence","iteration":0,"children":[{"nodeId":"build","type":"step","capturedOutput":"first"}]},
			{"nodeId":"loop#1","type":"sequence","iteration":1,"children":[{"nodeId":"build","type":"step","capturedOutput":"second"}]}
		]},
		{"nodeId":"quiet","type":"step"}
	]}}}`)

	cases := []struct {
		name  string
		path  []string
		want  string
		found bool
	}{
		{name: "the second pass", path: []string{"wf", "loop", "iter-1", "build"}, want: "second", found: true},
		{name: "the first pass", path: []string{"wf", "loop", "iter-0", "build"}, want: "first", found: true},
		{name: "a step that captured nothing", path: []string{"wf", "quiet"}, found: true},
		{name: "a pass by its node id", path: []string{"wf", "loop", "loop#1", "build"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found, err := CaptureAt(raw, PathKey(c.path))
			if err != nil {
				t.Fatal(err)
			}
			if found != c.found {
				t.Errorf("found = %v, want %v", found, c.found)
			}
			text := ""
			if got != nil {
				text = *got
			}
			if text != c.want {
				t.Errorf("capture = %q, want %q", text, c.want)
			}
		})
	}
}
