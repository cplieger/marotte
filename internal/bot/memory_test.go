package bot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// startingMemory is design §7.2 "Starting text".
const startingMemory = "# Memory\n\nYour notes for future turns. Keep this file under 40,000 characters.\n"

// homePaths is testPaths with KiroHome created, as kiro-cli and marotte's boot steering leave it.
func homePaths(t *testing.T) Paths {
	t.Helper()
	p := testPaths(t)
	if err := os.Mkdir(p.KiroHome, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func perm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// TestEnsureMemory_CreatesTheDirectoryAndTheStartingFile pins design §3.4 and §7.2 "Starting text": turning memory on
// makes <KiroHome>/bots/<chat id>/ at 0700 holding memory.md at 0600 with the starting text.
func TestEnsureMemory_CreatesTheDirectoryAndTheStartingFile(t *testing.T) {
	paths := homePaths(t)
	if err := EnsureMemory(startedBot(true, ""), paths); err != nil {
		t.Fatalf("EnsureMemory: %v", err)
	}
	file := memoryFile(paths)
	if got := readFile(t, file); got != startingMemory {
		t.Errorf("memory.md = %q, want %q", got, startingMemory)
	}
	if got := perm(t, file); got != 0o600 {
		t.Errorf("memory.md mode = %o, want 600", got)
	}
	if got := perm(t, filepath.Dir(file)); got != 0o700 {
		t.Errorf("the Bot's directory mode = %o, want 700", got)
	}
	if got := perm(t, filepath.Join(paths.KiroHome, "bots")); got != 0o700 {
		t.Errorf("bots/ mode = %o, want 700", got)
	}
}

// TestEnsureMemory_KeepsAFileThatExists pins design §7.2 "Memory off": the notes outlive memory turned off, so turning
// it on again restores them rather than the starting text.
func TestEnsureMemory_KeepsAFileThatExists(t *testing.T) {
	paths := homePaths(t)
	writeFile(t, memoryFile(paths), "- the backlog is at seven\n")
	if err := EnsureMemory(startedBot(true, ""), paths); err != nil {
		t.Fatalf("EnsureMemory: %v", err)
	}
	if got := readFile(t, memoryFile(paths)); got != "- the backlog is at seven\n" {
		t.Errorf("memory.md = %q, want the notes kept", got)
	}
}

// TestEnsureMemory_RefusesALink pins design §3.4: nothing but the Bot's own directory and file is written, so a link at
// bots/, at the Bot's directory or at its file is refused and its target is left as it was.
func TestEnsureMemory_RefusesALink(t *testing.T) {
	for _, tc := range []struct {
		name string
		link func(paths Paths) string
	}{
		{"bots/", func(p Paths) string { return filepath.Join(p.KiroHome, "bots") }},
		{"the Bot's directory", func(p Paths) string { return filepath.Join(p.KiroHome, "bots", testChatID) }},
		{"memory.md", func(p Paths) string { return memoryFile(p) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := homePaths(t)
			target := filepath.Join(paths.KiroHome, "steering")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			at := tc.link(paths)
			if tc.name == "memory.md" {
				target = filepath.Join(target, "environment.md")
				writeFile(t, target, "steering")
			}
			if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
				t.Fatal(err)
			}
			// Relative, so it stays in KiroHome: os.Root refuses an absolute link target itself.
			rel, err := filepath.Rel(filepath.Dir(at), target)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(rel, at); err != nil {
				t.Fatal(err)
			}
			if err := EnsureMemory(startedBot(true, ""), paths); err == nil {
				t.Error("EnsureMemory followed a link, want a refusal")
			}
			if tc.name == "memory.md" {
				if got := readFile(t, target); got != "steering" {
					t.Errorf("the link's target = %q, want it untouched", got)
				}
				return
			}
			if entries, _ := os.ReadDir(target); len(entries) != 0 {
				t.Errorf("the link's target holds %v, want nothing written there", entries)
			}
		})
	}
}

// TestEnsureMemory_RefusesAPathItCannotName pins design §7.2: an id that is not one folder name, or a relative
// KiroHome, names no file, so nothing is written.
func TestEnsureMemory_RefusesAPathItCannotName(t *testing.T) {
	paths := homePaths(t)
	rec := startedBot(true, "")
	rec.ID = "../steering"
	if err := EnsureMemory(rec, paths); err == nil {
		t.Error("EnsureMemory accepted a parent step in the id")
	}
	if err := EnsureMemory(startedBot(true, ""), Paths{KiroHome: "kiro-home"}); err == nil {
		t.Error("EnsureMemory accepted a relative KiroHome")
	}
	if _, err := os.Lstat(filepath.Join(paths.KiroHome, "bots")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("bots/ stat = %v, want nothing created", err)
	}
}

// sweepFixture is a KiroHome holding a bots/ directory with one Bot directory per id, each with its memory file.
func sweepFixture(t *testing.T, ids ...string) Paths {
	t.Helper()
	paths := homePaths(t)
	for _, id := range ids {
		writeFile(t, filepath.Join(paths.KiroHome, "bots", id, "memory.md"), "notes of "+id)
	}
	return paths
}

func recordsOf(complete bool, ids ...marotte.ChatID) func(context.Context) Records {
	return func(context.Context) Records {
		read := make(map[marotte.ChatID]struct{}, len(ids))
		for _, id := range ids {
			read[id] = struct{}{}
		}
		return Records{Read: read, Complete: complete}
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// TestSweepDirs_RemovesADirectoryWhoseChatHasNoRecord pins design §8.6 "Boot sweep": after a complete chat scan, a Bot
// directory whose chat has no record is removed, so a delete interrupted between the record and the directory heals;
// every directory with a record stays.
func TestSweepDirs_RemovesADirectoryWhoseChatHasNoRecord(t *testing.T) {
	paths := sweepFixture(t, "c-kept", "c-gone")
	removed, err := SweepDirs(t.Context(), paths, recordsOf(true, "c-kept", "c-other"))
	if err != nil {
		t.Fatalf("SweepDirs: %v", err)
	}
	if !slices.Equal(removed, []string{"c-gone"}) {
		t.Errorf("removed = %v, want [c-gone]", removed)
	}
	bots := filepath.Join(paths.KiroHome, "bots")
	if exists(t, filepath.Join(bots, "c-gone")) {
		t.Error("c-gone's directory is still there")
	}
	if got := readFile(t, filepath.Join(bots, "c-kept", "memory.md")); got != "notes of c-kept" {
		t.Errorf("c-kept's memory = %q, want it kept", got)
	}
}

// TestSweepDirs_AnIncompleteScanRemovesNothing pins design §8.6: a record the scan could not read may exist, so an
// incomplete scan removes no directory and says why.
func TestSweepDirs_AnIncompleteScanRemovesNothing(t *testing.T) {
	paths := sweepFixture(t, "c-unread")
	removed, err := SweepDirs(t.Context(), paths, recordsOf(false))
	if !errors.Is(err, ErrIncompleteScan) {
		t.Errorf("SweepDirs error = %v, want ErrIncompleteScan", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none", removed)
	}
	if !exists(t, filepath.Join(paths.KiroHome, "bots", "c-unread", "memory.md")) {
		t.Error("c-unread's memory was removed by an incomplete scan")
	}
}

// TestSweepDirs_LeavesWhatIsNoBotDirectory pins design §3.4 and §8.6: only a real directory named by a chat id is a
// Bot's, so a name that is not a chat id, a plain file and a link (and its target) are left alone.
func TestSweepDirs_LeavesWhatIsNoBotDirectory(t *testing.T) {
	paths := sweepFixture(t)
	bots := filepath.Join(paths.KiroHome, "bots")
	writeFile(t, filepath.Join(bots, "notes.md", "memory.md"), "not a chat id")
	writeFile(t, filepath.Join(bots, "c-file"), "a plain file")
	outside := filepath.Join(paths.KiroHome, "steering")
	writeFile(t, filepath.Join(outside, "environment.md"), "steering")
	if err := os.Symlink(filepath.Join("..", "steering"), filepath.Join(bots, "c-link")); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepDirs(t.Context(), paths, recordsOf(true))
	if err != nil {
		t.Fatalf("SweepDirs: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none", removed)
	}
	for _, name := range []string{"notes.md", "c-file", "c-link"} {
		if !exists(t, filepath.Join(bots, name)) {
			t.Errorf("%s was removed", name)
		}
	}
	if got := readFile(t, filepath.Join(outside, "environment.md")); got != "steering" {
		t.Errorf("the link's target = %q, want it untouched", got)
	}
}

// TestSweepDirs_ALinkedBotsDirectoryIsNotSwept: a bots/ that is a link is no directory of Bots, so the sweep removes
// nothing below its target and says why.
func TestSweepDirs_ALinkedBotsDirectoryIsNotSwept(t *testing.T) {
	paths := homePaths(t)
	kept := filepath.Join(paths.KiroHome, "steering", "c-gone", "memory.md")
	writeFile(t, kept, "steering")
	// Relative, so it stays in KiroHome: os.Root refuses an absolute link target itself.
	if err := os.Symlink("steering", filepath.Join(paths.KiroHome, "bots")); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepDirs(t.Context(), paths, recordsOf(true))
	if err == nil || len(removed) != 0 {
		t.Errorf("SweepDirs = %v, %v; want a refusal and nothing removed", removed, err)
	}
	if !exists(t, kept) {
		t.Error("the sweep removed a directory below the link's target")
	}
}

// TestSweepDirs_ListsBeforeTheScan pins design §8.6: a Bot's record is written before its directory, so a directory
// listed before the scan began has a record the scan reads; one made while the scan runs is not listed and stays.
func TestSweepDirs_ListsBeforeTheScan(t *testing.T) {
	paths := sweepFixture(t, "c-kept")
	created := filepath.Join(paths.KiroHome, "bots", "c-new", "memory.md")
	scanned := false
	_, err := SweepDirs(t.Context(), paths, func(context.Context) Records {
		scanned = true
		writeFile(t, created, startingMemory)
		return Records{Read: map[marotte.ChatID]struct{}{"c-kept": {}}, Complete: true}
	})
	if err != nil {
		t.Fatalf("SweepDirs: %v", err)
	}
	if !scanned {
		t.Fatal("SweepDirs never scanned the chats with c-kept's directory to judge")
	}
	if !exists(t, created) {
		t.Error("a Bot directory made during the scan was removed")
	}
}

// TestSweepDirs_NoBotDirectoryIsNothingToDo: a KiroHome no Bot ever used has no bots/, and one whose Bots are all gone
// may hold an empty bots/ or names that are no Bot's; none is an error, and none needs a scan of every chat record.
func TestSweepDirs_NoBotDirectoryIsNothingToDo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, paths Paths)
	}{
		{"no bots directory", func(*testing.T, Paths) {}},
		{"an empty bots directory", func(t *testing.T, paths Paths) {
			if err := os.Mkdir(filepath.Join(paths.KiroHome, "bots"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"a bots directory holding no Bot's", func(t *testing.T, paths Paths) {
			writeFile(t, filepath.Join(paths.KiroHome, "bots", "README.md"), "not a Bot")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := homePaths(t)
			tc.setup(t, paths)
			scanned := false
			removed, err := SweepDirs(t.Context(), paths, func(context.Context) Records {
				scanned = true
				return Records{Complete: true}
			})
			if err != nil || len(removed) != 0 {
				t.Errorf("SweepDirs = %v, %v; want nothing removed and no error", removed, err)
			}
			if scanned {
				t.Error("SweepDirs scanned the chats with no Bot directory to judge")
			}
		})
	}
}

// TestDir_IsTheBotsOwnDirectoryUnderKiroHome pins design §3.4: a Bot's directory is <KiroHome>/bots/<chat id>, named
// by a server-minted id that passes ids.ValidChatID, under an absolute KiroHome.
func TestDir_IsTheBotsOwnDirectoryUnderKiroHome(t *testing.T) {
	paths := testPaths(t)
	if got, ok := Dir(testChatID, paths); !ok || got != filepath.Join(paths.KiroHome, "bots", testChatID) {
		t.Errorf("Dir = %q, %v; want %q, true", got, ok, filepath.Join(paths.KiroHome, "bots", testChatID))
	}
	for _, tc := range []struct {
		name, id, home string
	}{
		{"a parent step", "..", paths.KiroHome},
		{"the bots directory itself", ".", paths.KiroHome},
		{"a nested path", "c1/../../steering", paths.KiroHome},
		{"a dotted id", "c1.md", paths.KiroHome},
		{"no id", "", paths.KiroHome},
		{"a relative KiroHome", testChatID, "kiro-home"},
		{"no KiroHome", testChatID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := Dir(tc.id, Paths{KiroHome: tc.home}); ok {
				t.Errorf("Dir = %q, want none", got)
			}
		})
	}
}
