package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// cappedStoreWithTurn is a store under capBytes holding one open turn, with the
// store's slog captured. Serial: the capture swaps the process-global default.
func cappedStoreWithTurn(t *testing.T, capBytes int64) (*Store, string, func() string) {
	t.Helper()
	logged := captureStoreSlog(t)
	s, err := NewStore(t.TempDir(), WithChatFileCap(capBytes))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	turn := openPromptTurn(t, s, "c1", "m-1")
	return s, turn, logged.String
}

func appendText(t *testing.T, s *Store, turn, id, text string) error {
	t.Helper()
	return s.Append(t.Context(), "c1", entryOf(turn, "", id, marotte.EntryKindText, marotte.EntryText{Text: text}))
}

// The refusal reaches the caller as ErrFileTooLarge and the log keeps what it held:
// the total cap is checked BEFORE the line is written, so a refused append loses one
// entry and never the record. Unlimited is the live setting and the design's default.
func TestStoreCap_OverCapRefusalLeavesTheLogIntact(t *testing.T) {
	s, turn, _ := cappedStoreWithTurn(t, 2<<10)
	if err := appendText(t, s, turn, "say-1", "small"); err != nil {
		t.Fatalf("Append(small): %v", err)
	}
	beforePage, err := s.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("TurnPage: %v", err)
	}
	before := beforePage.Entries

	err = appendText(t, s, turn, "say-2", strings.Repeat("y", 4<<10))
	if !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Fatalf("Append over the cap = %v, want atomicfile.ErrFileTooLarge", err)
	}
	afterPage, err := s.TurnPage(t.Context(), "c1", turn, 0)
	if err != nil {
		t.Fatalf("TurnPage after the refusal: %v", err)
	}
	after := afterPage.Entries
	if len(after) != len(before) || len(after) != 2 {
		t.Errorf("entries after the refusal = %d, want the %d held before it (turn_open + say-1)", len(after), len(before))
	}
}

// logSize is the entries.jsonl byte size, the quantity the cap is measured against.
func logSize(t *testing.T, s *Store) int64 {
	t.Helper()
	dir, err := s.pathFor("c1")
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, entriesFileName))
	if err != nil {
		t.Fatalf("stat the log: %v", err)
	}
	return info.Size()
}

// A successful append landing inside the last tenth of the cap is reported once, with
// the room left, so an operator sees the wall before a turn hits it. The fixture
// measures one line's envelope so the final append lands at 95% of the cap exactly.
func TestStoreCap_NearTheCapWarnsWhileThereIsRoom(t *testing.T) {
	const capBytes = 8 << 10
	s, turn, logged := cappedStoreWithTurn(t, capBytes)
	before := logSize(t, s)
	if err := appendText(t, s, turn, "say-1", "x"); err != nil {
		t.Fatalf("Append(probe): %v", err)
	}
	after := logSize(t, s)
	overhead := after - before - 1
	textLen := capBytes - capBytes/20 - after - overhead
	if textLen <= 0 {
		t.Fatalf("Setup: a probe line costs %d bytes, more than the fixture's cap allows", overhead)
	}
	if err := appendText(t, s, turn, "say-2", strings.Repeat("y", int(textLen))); err != nil {
		t.Fatalf("Append near the cap: %v", err)
	}
	out := logged()
	if !strings.Contains(out, `msg="entry log: this log is near the file cap"`) || !strings.Contains(out, "headroom_bytes=") {
		t.Errorf("no headroom warning after an append inside the last tenth; log:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("an append that landed logged at ERROR; log:\n%s", out)
	}
}

// Cap 0 is UNLIMITED: an append far past any small cap lands, and neither the headroom
// warning nor a refusal is logged, because there is no wall to be near.
func TestStoreCap_UnlimitedMeansUnlimitedAndLogsNeither(t *testing.T) {
	s, turn, logged := cappedStoreWithTurn(t, 0)
	if err := appendText(t, s, turn, "say-1", strings.Repeat("y", 256<<10)); err != nil {
		t.Fatalf("Append under an unlimited cap: %v", err)
	}
	out := logged()
	if strings.Contains(out, "near the file cap") || strings.Contains(out, "level=ERROR") {
		t.Errorf("an unlimited store logged a cap message; log:\n%s", out)
	}
}
