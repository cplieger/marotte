package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// static-src/turn-wake.test.ts reads the same fixture, so the two wordings cannot drift apart.
const wakeLineFixture = "testdata/wake_line.json"

type wakeLineCase struct {
	Name  string                `json:"name"`
	Label string                `json:"label"`
	Body  string                `json:"body"`
	Wake  marotte.EntryTurnWake `json:"wake"`
}

func wakeLineCases(t *testing.T) []wakeLineCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(wakeLineFixture))
	if err != nil {
		t.Fatalf("read %s: %v", wakeLineFixture, err)
	}
	var fx struct {
		Cases []wakeLineCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse %s: %v", wakeLineFixture, err)
	}
	if len(fx.Cases) == 0 {
		t.Fatalf("%s holds no cases", wakeLineFixture)
	}
	return fx.Cases
}

func TestWakeLineContract_ServerHalf(t *testing.T) {
	for _, c := range wakeLineCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			label, body := marotte.WakeLine(&c.Wake)
			if label != c.Label {
				t.Errorf("label = %q, want %q", label, c.Label)
			}
			if body != c.Body {
				t.Errorf("body = %q, want %q", body, c.Body)
			}
		})
	}
}
