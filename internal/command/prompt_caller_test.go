package command

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// promptCallerLabel reads params' _meta.kiro.callerLabel and whether the key is present.
func promptCallerLabel(params any) (string, bool) {
	m, _ := params.(map[string]any)
	meta, _ := m["_meta"].(map[string]any)
	kiro, _ := meta["kiro"].(map[string]any)
	v, ok := kiro["callerLabel"]
	label, _ := v.(string)
	return label, ok
}

func callerPromptParams(t *testing.T, from *marotte.Caller) map[string]any {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, `{}`)
	p := &marotte.PromptCommand{Text: notifyText, MessageID: "m-1", Caller: from}
	params, _ := buildPromptParams(t.Context(), Workspace{ConfigDir: dir}, fixedSession("s"), p, "t-abc", 0)
	return params
}

// A notify prompt's Caller.Label is copied to _meta.kiro.callerLabel.
func TestBuildPromptParams_NamesTheNotifyCallerToTheEngine(t *testing.T) {
	params := callerPromptParams(t, &marotte.Caller{Label: "grelot"})
	if got, ok := promptCallerLabel(params); !ok || got != "grelot" {
		t.Errorf("params[_meta].kiro.callerLabel = %q (present %v), want %q", got, ok, "grelot")
	}
}

// An /api/command caller's label reaches _meta.kiro.callerLabel as commandCaller cleaned it: one line,
// runs of blanks collapsed, ends trimmed.
func TestBuildPromptParams_NamesTheCommandCallerToTheEngine(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/command", nil)
	params := callerPromptParams(t, commandCaller(r, "  ci\tbot \n7 "))
	if got, ok := promptCallerLabel(params); !ok || got != "ci bot 7" {
		t.Errorf("params[_meta].kiro.callerLabel = %q (present %v), want %q", got, ok, "ci bot 7")
	}
}

// The user's own prompt, and an API caller that gave no name, carry no callerLabel.
func TestBuildPromptParams_NamesNoCallerWithoutALabel(t *testing.T) {
	for name, from := range map[string]*marotte.Caller{"user": nil, "unnamed caller": {}} {
		if got, ok := promptCallerLabel(callerPromptParams(t, from)); ok {
			t.Errorf("%s: params[_meta].kiro.callerLabel = %q, want the key absent", name, got)
		}
	}
}
