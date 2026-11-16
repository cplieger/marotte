package bot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadMemory_AnswersTheNotesWithTheirSizeAndTime(t *testing.T) {
	paths := homePaths(t)
	notes := "- the backlog is at seven\n"
	writeFile(t, memoryFile(paths), notes)
	at := time.UnixMilli(1_700_000_123_000)
	if err := os.Chtimes(memoryFile(paths), at, at); err != nil {
		t.Fatal(err)
	}

	got, found, err := ReadMemory(startedBot(true, ""), paths)

	if err != nil || !found {
		t.Fatalf("ReadMemory = found %v, %v; want the notes", found, err)
	}
	if got.Text != notes || got.Bytes != int64(len(notes)) {
		t.Errorf("ReadMemory = %q (%d bytes), want %q (%d bytes)", got.Text, got.Bytes, notes, len(notes))
	}
	if !got.UpdatedAt.Equal(at) {
		t.Errorf("updated at %v, want the file's time %v", got.UpdatedAt, at)
	}
}

func TestReadMemory_AMissingFileIsNotFound(t *testing.T) {
	got, found, err := ReadMemory(startedBot(true, ""), homePaths(t))
	if err != nil || found || got.Text != "" {
		t.Errorf("ReadMemory with no file = %+v, found %v, %v; want not found and no error", got, found, err)
	}
}

func TestReadMemory_RefusesALink(t *testing.T) {
	paths := homePaths(t)
	secret := filepath.Join(paths.KiroHome, "settings", "cli.json")
	writeFile(t, secret, `{"token":"x"}`)
	if err := os.MkdirAll(filepath.Dir(memoryFile(paths)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "settings", "cli.json"), memoryFile(paths)); err != nil {
		t.Fatal(err)
	}

	got, _, err := ReadMemory(startedBot(true, ""), paths)

	if err == nil {
		t.Error("ReadMemory read through a link, want a refusal")
	}
	if got.Text != "" {
		t.Errorf("ReadMemory answered %q through a link, want nothing", got.Text)
	}
}
