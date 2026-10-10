package command

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

type fixedSession marotte.SessionID

func (s fixedSession) SessionID() marotte.SessionID { return marotte.SessionID(s) }

func (fixedSession) Call(context.Context, string, any) (*marotte.RPCResponse, error) {
	return nil, errors.New("no RPC on a fixed session")
}

func (fixedSession) CallAt(context.Context, string, any) (*marotte.RPCResponse, uint64, error) {
	return nil, 0, errors.New("no RPC on a fixed session")
}

func TestBuildPromptParams_CarriesAChosenOutputStyleOnly(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "concise rides the prompt", config: `{"output_style":"concise"}`, want: "concise"},
		{name: "default is never sent", config: `{"output_style":"default"}`},
		{name: "an unknown style is never sent", config: `{"output_style":"verbose"}`},
		{name: "no setting is never sent", config: `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tc.config)
			p := &marotte.PromptCommand{Text: "hi", MessageID: "m-1"}

			params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, 0)

			meta, _ := params["_meta"].(map[string]any)
			kiro, _ := meta["kiro"].(map[string]any)
			got, sent := kiro["outputStyle"]
			if tc.want == "" {
				if sent {
					t.Errorf("params[_meta].kiro.outputStyle = %v, want the key absent", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("params[_meta].kiro.outputStyle = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildPromptParams_RequestsTheContextBreakdownOnEveryPrompt(t *testing.T) {
	for _, config := range []string{`{}`, `{"output_style":"concise"}`} {
		dir := t.TempDir()
		writeConfig(t, dir, config)
		p := &marotte.PromptCommand{Text: "hi", MessageID: "m-1"}

		params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, 0)

		meta, _ := params["_meta"].(map[string]any)
		kiro, _ := meta["kiro"].(map[string]any)
		if got := kiro["contextBreakdown"]; got != "detailed" {
			t.Errorf("config %s: params[_meta].kiro.contextBreakdown = %v, want \"detailed\"", config, got)
		}
	}
}

func TestBuildPromptParams_CarriesTheDisplayLabelOfALabelledPromptOnly(t *testing.T) {
	for _, tc := range []struct{ name, label string }{
		{name: "labelled", label: "Run task 2"},
		{name: "typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, `{}`)
			p := &marotte.PromptCommand{Text: "the full instruction", MessageID: "m-1", DisplayText: tc.label}

			params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, 0)

			meta, _ := params["_meta"].(map[string]any)
			kiro, _ := meta["kiro"].(map[string]any)
			got, sent := kiro["displayText"]
			switch {
			case tc.label == "" && sent:
				t.Errorf("params[_meta].kiro.displayText = %v, want the key absent on a typed prompt", got)
			case tc.label != "" && got != tc.label:
				t.Errorf("params[_meta].kiro.displayText = %v, want %q", got, tc.label)
			}
		})
	}
}

// The label a client sends is drawn as one line, so control runes go and the length is bounded.
func TestValidatePromptPayload_TreatsTheDisplayLabel(t *testing.T) {
	cmd := &marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: []byte(`{"text":"x","message_id":"m-1","display_text":" Run\u0007 task\n2 "}`),
	}
	p, _, err := validatePromptPayload(cmd)
	if err != nil {
		t.Fatalf("validatePromptPayload = %v, want nil", err)
	}
	if strings.ContainsAny(p.DisplayText, "\a\n") || !strings.HasPrefix(p.DisplayText, "Run") {
		t.Errorf("DisplayText = %q, want one trimmed line with the bell removed", p.DisplayText)
	}
	long := &marotte.ClientCommand{
		Type: marotte.CmdPrompt, ChatID: "c1",
		Payload: []byte(`{"text":"x","message_id":"m-1","display_text":"` + strings.Repeat("a", 1000) + `"}`),
	}
	if p, _, _ := validatePromptPayload(long); len(p.DisplayText) > maxDisplayLabelBytes+len("...") {
		t.Errorf("len(DisplayText) = %d, want at most %d", len(p.DisplayText), maxDisplayLabelBytes+3)
	}
}
