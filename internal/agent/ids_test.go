package agent

import (
	"testing"
	"time"
)

func TestNewMessageID_UUIDv7Format(t *testing.T) {
	a := newMessageID()
	time.Sleep(2 * time.Millisecond)
	b := newMessageID()

	// UUIDv7 is 36 chars: 8-4-4-4-12.
	if len(a) != 36 {
		t.Errorf("expected 36-char UUID, got %d: %q", len(a), a)
	}
	if a[8] != '-' || a[13] != '-' || a[18] != '-' || a[23] != '-' {
		t.Errorf("not UUID format: %q", a)
	}
	// The version nibble at 14 is '7'.
	if a[14] != '7' {
		t.Errorf("version nibble = %c, want '7': %q", a[14], a)
	}
	// Variant bits at 19 are 8, 9, a or b.
	v := a[19]
	if v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Errorf("variant nibble = %c, want 8/9/a/b: %q", v, a)
	}
	// UUIDv7 sorts by creation time.
	if a >= b {
		t.Errorf("expected a < b for time ordering:\n  a=%s\n  b=%s", a, b)
	}
}

func TestNewMessageID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := newMessageID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d draws: %q", len(seen), id)
		}
		seen[id] = struct{}{}
	}
}
