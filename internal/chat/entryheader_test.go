package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

func newHeaderFixture(t *testing.T) (EntryHeader, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "chats", "c-abcdef01")
	return NewEntryHeader(root), root
}

// The header holds every marotte.Chat field; no `messages` key reaches the file.
func TestEntryHeader_RoundTripsEveryField(t *testing.T) {
	h, root := newHeaderFixture(t)
	want := &marotte.Chat{
		ID: "c-abcdef01", Name: "a chat", Model: "opus", ACPSessionID: "sess-1",
		PriorACPSessionIDs: []string{"sess-0"}, Draft: "half a sentence",
		Attachments: []string{"/workspace/a.go"}, Effort: "high",
		CompactionWatermark: "compaction-abc", TurnCount: 7,
		LastTurnOutcome: marotte.TurnOutcomeFailed, PendingModel: "sonnet",
		SupervisedMode: true,
	}
	if err := h.Write(t.Context(), want); err != nil {
		t.Fatalf("write header: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, headerFileName))
	if err != nil {
		t.Fatalf("read the header file: %v", err)
	}
	if strings.Contains(string(raw), `"messages"`) {
		t.Fatalf("the header file carries a transcript key:\n%s", raw)
	}

	got, err := h.Read(t.Context())
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	if got.Name != want.Name || got.Model != want.Model || got.ACPSessionID != want.ACPSessionID ||
		got.Draft != want.Draft || got.Effort != want.Effort || got.TurnCount != want.TurnCount ||
		got.LastTurnOutcome != want.LastTurnOutcome ||
		got.PendingModel != want.PendingModel || !got.SupervisedMode || got.CompactionWatermark != want.CompactionWatermark {
		t.Errorf("the header round-tripped as %+v, want %+v", got, want)
	}
	if len(got.Attachments) != 1 || got.Attachments[0] != "/workspace/a.go" {
		t.Errorf("attachments round-tripped as %v, want the one staged path", got.Attachments)
	}
	if len(got.PriorACPSessionIDs) != 1 || got.PriorACPSessionIDs[0] != "sess-0" {
		t.Errorf("the session chain round-tripped as %v, want the retired session", got.PriorACPSessionIDs)
	}
}

// A root with no header reads as absent, so a run root and an uncreated chat are treated alike.
func TestEntryHeader_AnAbsentHeaderReadsAsNotExist(t *testing.T) {
	h, _ := newHeaderFixture(t)
	if _, err := h.Read(t.Context()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reading an absent header answered %v, want os.ErrNotExist", err)
	}
}

// Update is one write, so two fields cannot be left half moved by a crash.
func TestEntryHeader_UpdateAppliesEveryChangeInOneWrite(t *testing.T) {
	h, root := newHeaderFixture(t)
	if err := h.Write(t.Context(), &marotte.Chat{ID: "c-abcdef01"}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	applied, err := h.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ACPSessionID = "sess-1"
		c.PendingModel = "sonnet"
		return true
	})
	if err != nil || !applied {
		t.Fatalf("update reported (%v, %v), want applied", applied, err)
	}
	got := mustReadHeader(t, h)
	if got.ACPSessionID != "sess-1" || got.PendingModel != "sonnet" {
		t.Errorf("the header holds %+v, want both fields from one write", got)
	}
	stamp := backdate(t, filepath.Join(root, headerFileName))
	if again, err := h.Update(t.Context(), func(*marotte.Chat) bool { return false }); err != nil || again {
		t.Fatalf("a declining update reported (%v, %v), want no write", again, err)
	}
	if !modTime(t, filepath.Join(root, headerFileName)).Equal(stamp) {
		t.Error("a declining update rewrote the header, which every device re-reads")
	}
}

// Update on an absent header writes the bare record, as a turn open for an unknown id needs.
func TestEntryHeader_UpdateCreatesAnAbsentHeader(t *testing.T) {
	h, _ := newHeaderFixture(t)
	applied, err := h.Update(t.Context(), func(c *marotte.Chat) bool {
		c.ID = "c-abcdef01"
		c.Name = "from the prompt"
		c.Model = "opus"
		c.SupervisedMode = true
		return true
	})
	if err != nil || !applied {
		t.Fatalf("update reported (%v, %v), want applied", applied, err)
	}
	got := mustReadHeader(t, h)
	if got.ID != "c-abcdef01" || got.Name != "from the prompt" || got.Model != "opus" || !got.SupervisedMode {
		t.Errorf("the created header is %+v, want the record the caller supplied", got)
	}
}

// A header write carries none of the retired fields. The write is checked, not the struct, because a re-added field
// reaches the file under its json tag. `revision` gave way to turn_revert records; `needs_reconcile` and `degraded`
// to the log's own records.
func TestEntryHeader_AHeaderWriteCarriesNoRetiredField(t *testing.T) {
	h, root := newHeaderFixture(t)
	// Both writers, Write and Update.
	if err := h.Write(t.Context(), &marotte.Chat{
		ID: "c-abcdef01", Name: "a chat", TurnCount: 7,
		LastTurnOutcome: marotte.TurnOutcomeFailed, SupervisedMode: true,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := h.Update(t.Context(), func(c *marotte.Chat) bool {
		c.Name = "renamed"
		return true
	}); err != nil {
		t.Fatalf("update header: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, headerFileName))
	if err != nil {
		t.Fatalf("read the header file: %v", err)
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("decode the header file: %v", err)
	}
	for _, retired := range []string{"revision", "needs_reconcile", "degraded"} {
		if _, ok := keyed[retired]; ok {
			t.Errorf("the header file carries the retired key %q:\n%s", retired, raw)
		}
	}
}

// The counter cache is written only on disagreement; a no-op write is a file every device re-reads.
func TestEntryHeader_CachesAndFlagsWriteOnlyOnAChange(t *testing.T) {
	h, root := newHeaderFixture(t)
	stored := &marotte.Chat{
		ID: "c-abcdef01", TurnCount: 2,
		LastTurnOutcome: marotte.TurnOutcomeCompleted,
	}
	if err := h.Write(t.Context(), stored); err != nil {
		t.Fatalf("write header: %v", err)
	}
	path := filepath.Join(root, headerFileName)
	stamp := backdate(t, path)

	if err := h.Counters(t.Context(), 2, marotte.TurnOutcomeCompleted); err != nil {
		t.Fatalf("counters: %v", err)
	}
	if !modTime(t, path).Equal(stamp) {
		t.Error("a write that changes nothing still rewrote the header")
	}
	got := mustReadHeader(t, h)
	if got.TurnCount != 2 || got.LastTurnOutcome != marotte.TurnOutcomeCompleted {
		t.Fatalf("an agreeing write changed the header to %+v", got)
	}

	if err := h.Counters(t.Context(), 3, marotte.TurnOutcomeFailed); err != nil {
		t.Fatalf("counters: %v", err)
	}
	got = mustReadHeader(t, h)
	if got.TurnCount != 3 || got.LastTurnOutcome != marotte.TurnOutcomeFailed {
		t.Errorf("the corrected header is (%d, %q), want (3, failed)", got.TurnCount, got.LastTurnOutcome)
	}
}

// A header past the read bound is refused: it is not one this store wrote.
func TestEntryHeader_AnOversizeHeaderIsRefused(t *testing.T) {
	h, root := newHeaderFixture(t)
	if err := os.MkdirAll(root, dirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bloat := make([]byte, maxHeaderBytes+1)
	for i := range bloat {
		bloat[i] = ' '
	}
	if err := os.WriteFile(filepath.Join(root, headerFileName), bloat, fileMode); err != nil {
		t.Fatalf("write an oversize header: %v", err)
	}
	if _, err := h.Read(t.Context()); !errors.Is(err, atomicfile.ErrFileTooLarge) {
		t.Errorf("reading an oversize header answered %v, want ErrFileTooLarge", err)
	}
}

// backdate stamps path in the past and returns the stamp, so a modTime comparison detects a write without racing the
// clock.
func backdate(t *testing.T, path string) time.Time {
	t.Helper()
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("backdate %s: %v", path, err)
	}
	return modTime(t, path)
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

func mustReadHeader(t *testing.T, h EntryHeader) *marotte.Chat {
	t.Helper()
	c, err := h.Read(t.Context())
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	return c
}
