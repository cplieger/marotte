package marotte_test

// NewChatID must satisfy ids.ValidChatID (the filename and envelope gate) and be unguessable. An
// external test package, so it can import internal/ids, which internal/marotte must not.

import (
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

func TestNewChatID_Shape(t *testing.T) {
	id := string(marotte.NewChatID())

	cases := []struct {
		desc string
		got  bool
		want bool
	}{
		{desc: "carries the c- prefix every chat id has always carried", got: strings.HasPrefix(id, "c-"), want: true},
		{desc: "is 34 characters: c- plus 32 hex digits for 16 bytes", got: len(id) == 34, want: true},
		{desc: "passes the chat-id gate that names the chat file", got: ids.ValidChatID(id), want: true},
		{desc: "holds no path separator", got: strings.ContainsAny(id, "/\\"), want: false},
		{desc: "holds no traversal segment", got: strings.Contains(id, ".."), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s: got %v, want %v (id %q)", tc.desc, tc.got, tc.want, id)
			}
		})
	}
}

// TestNewChatID_HexOnly pins the alphabet rather than trusting the length: a mint
// that fell back to some other encoding could still be 34 characters and still
// pass ValidChatID while changing what the id is made of.
func TestNewChatID_HexOnly(t *testing.T) {
	body := strings.TrimPrefix(string(marotte.NewChatID()), "c-")
	for i, r := range body {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			t.Errorf("byte %d of the id body is %q, want a lowercase hex digit (body %q)", i, r, body)
		}
	}
}

// TestNewChatID_Unique is the crypto/rand claim reduced to something a test can
// assert: a timestamp-plus-6-random-characters mint collides at this volume, and a counter would collide immediately.
func TestNewChatID_Unique(t *testing.T) {
	const n = 4096
	seen := make(map[marotte.ChatID]struct{}, n)
	for range n {
		id := marotte.NewChatID()
		if _, dup := seen[id]; dup {
			t.Fatalf("minted %q twice in %d draws", id, n)
		}
		seen[id] = struct{}{}
	}
}

// TestNewChatID_LegacyShapeStillValid — existing chat ids stay valid under
// ValidChatID, so no chat data moves. If this fails, every chat file on the volume
// is unreachable.
func TestNewChatID_LegacyShapeStillValid(t *testing.T) {
	cases := []struct {
		desc string
		id   string
	}{
		{desc: "the client-minted c-<ms>-<base36> shape", id: "c-1756150000000-a1b2c3"},
		{desc: "the newly minted shape", id: string(marotte.NewChatID())},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if !ids.ValidChatID(tc.id) {
				t.Errorf("ValidChatID(%q) = false, want true", tc.id)
			}
		})
	}
}
