package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// FuzzResolveSwitchModel asserts a request names its own model, auto included, or the chat's for none.
func FuzzResolveSwitchModel(f *testing.F) {
	f.Add("claude-sonnet", "claude-opus")
	f.Add("claude-sonnet", "")
	f.Add("claude-sonnet", "auto")
	f.Add("", "gpt-4")
	f.Add("model-a", "model-a")

	f.Fuzz(func(t *testing.T, current, requested string) {
		chat := &marotte.Chat{Model: current}
		p := marotte.SwitchModelCommand{Model: requested}
		model := resolveSwitchModel(chat, p)

		want := requested
		if requested == "" {
			want = current
		}
		if model != want {
			t.Errorf("resolveSwitchModel(%q, %q) = %q, want %q", current, requested, model, want)
		}
	})
}
