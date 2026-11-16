package bot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClearMemory_RewritesTheNotesToTheStartingText(t *testing.T) {
	paths := homePaths(t)
	writeFile(t, memoryFile(paths), "- the backlog is at seven\n")

	if err := ClearMemory(startedBot(true, ""), paths); err != nil {
		t.Fatalf("ClearMemory: %v", err)
	}

	if got := readFile(t, memoryFile(paths)); got != startingMemory {
		t.Errorf("memory.md = %q, want the starting text %q", got, startingMemory)
	}
	if got := perm(t, memoryFile(paths)); got != 0o600 {
		t.Errorf("memory.md mode = %o, want 600", got)
	}
}

func TestClearMemory_WritesTheStartingTextWhereNoFileIs(t *testing.T) {
	paths := homePaths(t)

	if err := ClearMemory(startedBot(true, ""), paths); err != nil {
		t.Fatalf("ClearMemory: %v", err)
	}

	if got := readFile(t, memoryFile(paths)); got != startingMemory {
		t.Errorf("memory.md = %q, want the starting text", got)
	}
	if got := perm(t, filepath.Dir(memoryFile(paths))); got != 0o700 {
		t.Errorf("the Bot's directory mode = %o, want 700", got)
	}
}

func TestClearMemory_RefusesALink(t *testing.T) {
	for _, tc := range []struct {
		link func(paths Paths) string
		name string
	}{
		{name: "bots/", link: func(p Paths) string { return filepath.Join(p.KiroHome, "bots") }},
		{name: "the Bot's directory", link: func(p Paths) string { return filepath.Join(p.KiroHome, "bots", testChatID) }},
		{name: "memory.md", link: memoryFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := homePaths(t)
			outside := filepath.Join(paths.KiroHome, "steering")
			kept := filepath.Join(outside, "memory.md")
			writeFile(t, kept, "steering")
			target := outside
			if tc.name == "memory.md" {
				target = kept
			}
			at := tc.link(paths)
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

			if err := ClearMemory(startedBot(true, ""), paths); err == nil {
				t.Error("ClearMemory wrote through a link, want a refusal")
			}

			if got := readFile(t, kept); got != "steering" {
				t.Errorf("the link's target = %q, want it untouched", got)
			}
		})
	}
}
