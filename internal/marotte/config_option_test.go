package marotte

import (
	"encoding/json"
	"testing"
)

func TestConfigOptionValue_ReadsTheNamedOption(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		raw         string
		id          string
		wantValue   string
		wantOffered bool
	}{
		{
			name:        "workflowLaunch beside other options",
			raw:         `{"configOptions":[{"id":"thinking","currentValue":true},{"id":"workflowLaunch","type":"select","currentValue":"large-tasks"}]}`,
			id:          ConfigOptionWorkflowLaunch,
			wantValue:   WorkflowLaunchLargeTasks,
			wantOffered: true,
		},
		{
			name:        "sessionToolsMessaging beside workflowLaunch",
			raw:         `{"configOptions":[{"id":"workflowLaunch","currentValue":"when-asked"},{"id":"sessionToolsMessaging","currentValue":"off"}]}`,
			id:          ConfigOptionSessionMessaging,
			wantValue:   SessionMessagingOff,
			wantOffered: true,
		},
		{
			name: "another option's value is not read",
			raw:  `{"configOptions":[{"id":"workflowLaunch","currentValue":"when-asked"}]}`,
			id:   ConfigOptionSessionMessaging,
		},
		{
			name: "absent from the list",
			raw:  `{"configOptions":[{"id":"model","currentValue":"auto"}]}`,
			id:   ConfigOptionWorkflowLaunch,
		},
		{
			name: "no list",
			raw:  `{"sessionId":"s1"}`,
			id:   ConfigOptionWorkflowLaunch,
		},
		{
			name: "nested under _meta",
			raw:  `{"_meta":{"configOptions":[{"id":"workflowLaunch","currentValue":"when-asked"}]}}`,
			id:   ConfigOptionWorkflowLaunch,
		},
		{
			name: "non-string value",
			raw:  `{"configOptions":[{"id":"sessionToolsMessaging","currentValue":true}]}`,
			id:   ConfigOptionSessionMessaging,
		},
		{
			name: "unparseable",
			raw:  `{"configOptions":`,
			id:   ConfigOptionWorkflowLaunch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, offered := ConfigOptionValue(json.RawMessage(tc.raw), tc.id)
			if value != tc.wantValue || offered != tc.wantOffered {
				t.Errorf("ConfigOptionValue(%s, %q) = (%q, %v), want (%q, %v)", tc.raw, tc.id, value, offered, tc.wantValue, tc.wantOffered)
			}
		})
	}
}
