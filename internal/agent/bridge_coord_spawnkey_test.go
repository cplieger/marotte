package agent

import (
	"testing"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// TestBridgeSpawnKey_ByteIdenticalForValidatedFields pins that validated fields encode to the plain concatenation.
func TestBridgeSpawnKey_ByteIdenticalForValidatedFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		chatID        marotte.ChatID
		modelOverride string
		want          string
	}{
		{"c1", "", "c1:"},
		{"c1", "auto", "c1:auto"},
		{"my_chat-01", "claude-sonnet-4.5", "my_chat-01:claude-sonnet-4.5"},
	}
	for _, tc := range cases {
		if !ids.ValidChatID(string(tc.chatID)) {
			t.Fatalf("test case chat id %q does not pass ids.ValidChatID", tc.chatID)
		}
		if !ids.ValidIdent(tc.modelOverride) {
			t.Fatalf("test case model %q does not pass ids.ValidIdent", tc.modelOverride)
		}
		if got := bridgeSpawnKey(tc.chatID, tc.modelOverride); got != tc.want {
			t.Errorf("bridgeSpawnKey(%q, %q) = %q, want %q",
				tc.chatID, tc.modelOverride, got, tc.want)
		}
	}
}

// TestBridgeSpawnKey_DistinctPairsNeverCollapse pins injectivity even for values today's
// validators reject: a collapse hands one caller another model's bridge.
func TestBridgeSpawnKey_DistinctPairsNeverCollapse(t *testing.T) {
	t.Parallel()

	pairs := []struct {
		name           string
		aChat          marotte.ChatID
		aModel         string
		bChat          marotte.ChatID
		bModel         string
		naiveSeparator string
	}{
		// The pair a plain ':' join would collapse.
		{"boundary moves across the colon", "c", "1:m", "c:1", "m", ":"},
		// The pair a 0x00 join would collapse.
		{"boundary moves across the NUL", "c", "1\x00m", "c\x001", "m", "\x00"},
		// Escaping next to a separator must not reintroduce the ambiguity.
		{"escape adjacent to a separator", `a\`, "b:c", `a\:b`, "c", ":"},
		// The empty override is a real value and must not alias a chat id ending in the separator.
		{"empty override distinct from separator-suffixed chat", "c1:", "", "c1", ":", ":"},
	}

	for _, tc := range pairs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			naiveA := string(tc.aChat) + tc.naiveSeparator + tc.aModel
			naiveB := string(tc.bChat) + tc.naiveSeparator + tc.bModel
			if naiveA != naiveB {
				t.Fatalf("test setup: the naive join does not collapse this pair (%q vs %q)", naiveA, naiveB)
			}
			a := bridgeSpawnKey(tc.aChat, tc.aModel)
			b := bridgeSpawnKey(tc.bChat, tc.bModel)
			if a == b {
				t.Errorf("bridgeSpawnKey collapsed (%q,%q) and (%q,%q) onto %q",
					tc.aChat, tc.aModel, tc.bChat, tc.bModel, a)
			}
		})
	}
}
