package bot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveDir_RemovesTheBotsDirectoryAndItsNotes(t *testing.T) {
	paths := homePaths(t)
	writeFile(t, memoryFile(paths), "notes")
	other := filepath.Join(paths.KiroHome, "bots", "c-other", "memory.md")
	writeFile(t, other, "other notes")

	if err := RemoveDir(testChatID, paths); err != nil {
		t.Fatalf("RemoveDir: %v", err)
	}

	if exists(t, filepath.Dir(memoryFile(paths))) {
		t.Error("the Bot's directory is still there")
	}
	if got := readFile(t, other); got != "other notes" {
		t.Errorf("another Bot's memory = %q, want it kept", got)
	}
}

func TestRemoveDir_ANotesFreeChatRemovesNothing(t *testing.T) {
	for name, paths := range map[string]Paths{"no bots directory": homePaths(t), "no KiroHome yet": testPaths(t)} {
		t.Run(name, func(t *testing.T) {
			if err := RemoveDir(testChatID, paths); err != nil {
				t.Errorf("RemoveDir of a chat with no directory = %v, want nil", err)
			}
		})
	}
}

func TestRemoveDir_RefusesALinkedBotsDirectory(t *testing.T) {
	paths := homePaths(t)
	kept := filepath.Join(paths.KiroHome, "steering", testChatID, "memory.md")
	writeFile(t, kept, "steering")
	if err := os.Symlink("steering", filepath.Join(paths.KiroHome, "bots")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveDir(testChatID, paths); err == nil {
		t.Error("RemoveDir removed below a linked bots/, want a refusal")
	}

	if !exists(t, kept) {
		t.Error("RemoveDir removed a directory below the link's target")
	}
}

func TestRemoveDir_RefusesAPathItCannotName(t *testing.T) {
	paths := homePaths(t)
	writeFile(t, filepath.Join(paths.KiroHome, "steering", "environment.md"), "steering")

	if err := RemoveDir("../steering", paths); err == nil {
		t.Error("RemoveDir accepted a parent step in the id")
	}

	if !exists(t, filepath.Join(paths.KiroHome, "steering", "environment.md")) {
		t.Error("RemoveDir removed what the id named outside bots/")
	}
}
