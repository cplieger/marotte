package marotte

import (
	"encoding/json"
	"testing"
)

func TestWorkflowLaunchReported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		raw         string
		wantValue   string
		wantOffered bool
	}{
		{
			name:        "offered beside other options",
			raw:         `{"configOptions":[{"id":"thinking","currentValue":true},{"id":"workflowLaunch","type":"select","currentValue":"large-tasks"}]}`,
			wantValue:   WorkflowLaunchLargeTasks,
			wantOffered: true,
		},
		{
			name: "absent from the list",
			raw:  `{"configOptions":[{"id":"model","currentValue":"auto"}]}`,
		},
		{
			name: "no list",
			raw:  `{"sessionId":"s1"}`,
		},
		{
			name: "nested under _meta",
			raw:  `{"_meta":{"configOptions":[{"id":"workflowLaunch","currentValue":"when-asked"}]}}`,
		},
		{
			name: "non-string value",
			raw:  `{"configOptions":[{"id":"workflowLaunch","currentValue":1}]}`,
		},
		{
			name: "unparseable",
			raw:  `{"configOptions":`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, offered := WorkflowLaunchReported(json.RawMessage(tc.raw))
			if value != tc.wantValue || offered != tc.wantOffered {
				t.Errorf("WorkflowLaunchReported(%s) = (%q, %v), want (%q, %v)", tc.raw, value, offered, tc.wantValue, tc.wantOffered)
			}
		})
	}
}
