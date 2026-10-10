package command

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// commandTypeFloor stops a broken scan passing vacuously: the vocabulary is 24
// constants today, so a floor of 20 leaves room for four removals while sitting well
// above any count a failed parse would produce.
const commandTypeFloor = 20

// declaredCommandTypes reads the Cmd* constant names out of the vocabulary's own
// declaration, so the completeness check cannot drift from a second hand-written list.
func declaredCommandTypes(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../marotte/commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse the command vocabulary: %v", err)
	}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "CommandType" {
			return true
		}
		for _, name := range spec.Names {
			if strings.HasPrefix(name.Name, "Cmd") {
				names = append(names, name.Name)
			}
		}
		return true
	})
	return names
}

// TestCommandDischarges_ClassifiesEveryCommand: the table IS the discharge decision,
// and dischargeNo is its zero value, so a command nobody classified is silently
// treated as answering nothing. Only this test catches that, which is why it reads
// the constant block rather than a second list of names.
func TestCommandDischarges_ClassifiesEveryCommand(t *testing.T) {
	names := declaredCommandTypes(t)
	if len(names) < commandTypeFloor {
		t.Fatalf("the scan found %d Cmd* constants, want at least %d: the scan is broken, not the table",
			len(names), commandTypeFloor)
	}
	if len(commandDischarges) != len(names) {
		t.Errorf("the table has %d rows for %d commands", len(commandDischarges), len(names))
	}
	byValue := map[marotte.CommandType]string{
		marotte.CmdCreateChat:          "CmdCreateChat",
		marotte.CmdResumeSession:       "CmdResumeSession",
		marotte.CmdForkChat:            "CmdForkChat",
		marotte.CmdPrompt:              "CmdPrompt",
		marotte.CmdCancel:              "CmdCancel",
		marotte.CmdDeleteChat:          "CmdDeleteChat",
		marotte.CmdSwitchModel:         "CmdSwitchModel",
		marotte.CmdPermissionResponse:  "CmdPermissionResponse",
		marotte.CmdElicitationResponse: "CmdElicitationResponse",
		marotte.CmdUserInputResponse:   "CmdUserInputResponse",
		marotte.CmdRewindChat:          "CmdRewindChat",
		marotte.CmdCompact:             "CmdCompact",
		marotte.CmdSetEffort:           "CmdSetEffort",
		marotte.CmdSetThinking:         "CmdSetThinking",
		marotte.CmdSetDraft:            "CmdSetDraft",
		marotte.CmdSetAttachments:      "CmdSetAttachments",
		marotte.CmdSetMode:             "CmdSetMode",
		marotte.CmdSetSupervisedMode:   "CmdSetSupervisedMode",
		marotte.CmdSteer:               "CmdSteer",
		marotte.CmdSteerClear:          "CmdSteerClear",
		marotte.CmdQueuePrompt:         "CmdQueuePrompt",
		marotte.CmdUnqueuePrompt:       "CmdUnqueuePrompt",
		marotte.CmdSetInterruptMode:    "CmdSetInterruptMode",
		marotte.CmdRenameChat:          "CmdRenameChat",
		marotte.CmdSteerRemove:         "CmdSteerRemove",
		marotte.CmdOpenTab:             "CmdOpenTab",
		marotte.CmdCloseTab:            "CmdCloseTab",
		marotte.CmdReorderTabs:         "CmdReorderTabs",
		marotte.CmdPinTab:              "CmdPinTab",
		marotte.CmdReparentTab:         "CmdReparentTab",
		marotte.CmdApproveSpecPhase:    "CmdApproveSpecPhase",
		marotte.CmdMergeTangent:        "CmdMergeTangent",
	}
	classified := make(map[string]bool, len(commandDischarges))
	for value := range commandDischarges {
		name, known := byValue[value]
		if !known {
			t.Errorf("the table holds %q, which no Cmd* constant in this map names", value)
			continue
		}
		classified[name] = true
	}
	for _, name := range names {
		if !classified[name] {
			t.Errorf("%s has no discharge verdict: decide whether it is the user answering the agent", name)
		}
	}
}

type fakeChatStatus struct{ discharged []marotte.ChatID }

func (f *fakeChatStatus) DischargeWaiting(_ context.Context, chatID marotte.ChatID) {
	f.discharged = append(f.discharged, chatID)
}

// TestDispatch_DischargesOnlyAnAnswer drives the dispatcher per command type, because the hook is
// the dispatcher's. Each structured channel needs both halves: the affirmative payload clears the
// claim, and the walk-away, unknown action and absent payload keep it.
func TestDispatch_DischargesOnlyAnAnswer(t *testing.T) {
	cases := []struct {
		name    string
		cmdType marotte.CommandType
		payload string
		handler Handler
		want    bool
	}{
		{
			name:    "a steer is the user's own words to this agent",
			cmdType: marotte.CmdSteer,
			want:    true,
		},
		{
			// The prompt path discharges at StartTurn, where the turn's source tells a prompt from
			// a `!cmd`; a second discharge here would fire for both.
			name:    "a prompt does not, because its turn source decides",
			cmdType: marotte.CmdPrompt,
		},
		{
			name:    "an answer to the agent's own menu clears the claim",
			cmdType: marotte.CmdUserInputResponse,
			payload: `{"request_id":7,"action":"answered","answer":"blue"}`,
			want:    true,
		},
		{
			name:    "a dismissed question does not",
			cmdType: marotte.CmdUserInputResponse,
			payload: `{"request_id":7,"action":"dismissed"}`,
		},
		{
			name:    "an answered action with no answer text does not",
			cmdType: marotte.CmdUserInputResponse,
			payload: `{"request_id":7,"action":"answered"}`,
		},
		{
			name:    "a permission selection clears the claim",
			cmdType: marotte.CmdPermissionResponse,
			payload: `{"request_id":7,"option_id":"allow_once"}`,
			want:    true,
		},
		{
			name:    "a permission reject clears it as well, because deciding is answering",
			cmdType: marotte.CmdPermissionResponse,
			payload: `{"request_id":7,"option_id":"reject_once"}`,
			want:    true,
		},
		{
			name:    "a permission reply naming no option does not",
			cmdType: marotte.CmdPermissionResponse,
			payload: `{"request_id":7}`,
		},
		{
			name:    "an accepted MCP elicitation clears the claim",
			cmdType: marotte.CmdElicitationResponse,
			payload: `{"request_id":7,"action":"accept","content":{"colour":"blue"}}`,
			want:    true,
		},
		{
			name:    "a declined elicitation does not",
			cmdType: marotte.CmdElicitationResponse,
			payload: `{"request_id":7,"action":"decline"}`,
		},
		{
			name:    "a cancelled elicitation does not",
			cmdType: marotte.CmdElicitationResponse,
			payload: `{"request_id":7,"action":"cancel"}`,
		},
		{
			name:    "an elicitation action nobody declared does not",
			cmdType: marotte.CmdElicitationResponse,
			payload: `{"request_id":7,"action":"maybe"}`,
		},
		{
			name:    "a structured answer with no payload at all does not",
			cmdType: marotte.CmdUserInputResponse,
		},
		{
			name:    "a structured answer whose payload does not parse does not",
			cmdType: marotte.CmdPermissionResponse,
			payload: `"not an object"`,
		},
		{
			name:    "a steer whose handler failed answered nothing",
			cmdType: marotte.CmdSteer,
			handler: func(context.Context, *marotte.ClientCommand) (any, error) {
				return nil, StatusError(http.StatusConflict, ErrMissingChatID)
			},
		},
		{
			name:    "an answer whose handler failed does not",
			cmdType: marotte.CmdUserInputResponse,
			payload: `{"request_id":7,"action":"answered","answer":"blue"}`,
			handler: func(context.Context, *marotte.ClientCommand) (any, error) {
				return nil, StatusError(http.StatusConflict, errAlreadyAnswered)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := &fakeChatStatus{}
			d := New()
			d.status = status
			handler := tc.handler
			if handler == nil {
				handler = func(context.Context, *marotte.ClientCommand) (any, error) { return nil, nil }
			}
			d.Register(tc.cmdType, handler)

			body := `{"type":"` + string(tc.cmdType) + `","chat_id":"c-abc123"`
			if tc.payload != "" {
				body += `,"payload":` + tc.payload
			}
			body += `}`
			req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)

			if tc.want {
				if len(status.discharged) != 1 || status.discharged[0] != "c-abc123" {
					t.Errorf("%s payload %s discharged %v, want exactly [c-abc123]",
						tc.cmdType, tc.payload, status.discharged)
				}
				return
			}
			if len(status.discharged) != 0 {
				t.Errorf("%s payload %s discharged %v, want nothing",
					tc.cmdType, tc.payload, status.discharged)
			}
		})
	}
}

// A dispatcher built without roles is what internal/command's own tests construct, so
// the nil guard is load-bearing rather than defensive.
func TestDispatch_NoStatusRoleIsSilent(t *testing.T) {
	d := New()
	d.Register(marotte.CmdSteer, func(context.Context, *marotte.ClientCommand) (any, error) { return nil, nil })
	req := httptest.NewRequest(http.MethodPost, "/api/command",
		strings.NewReader(`{"type":"steer","chat_id":"c-abc123"}`))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
