package command

import (
	"context"
	"errors"
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

			params, _ := BuildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, 0)

			meta, hasMeta := params["_meta"].(map[string]any)
			if tc.want == "" {
				if hasMeta {
					t.Errorf("params[_meta] = %v, want no _meta at all", meta)
				}
				return
			}
			kiro, _ := meta["kiro"].(map[string]any)
			if got := kiro["outputStyle"]; got != tc.want {
				t.Errorf("params[_meta].kiro.outputStyle = %v, want %q", got, tc.want)
			}
		})
	}
}
