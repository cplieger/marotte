package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points git's global config away from this machine's for the whole package: a
// forge manager that loads a connected record registers this test binary as the credential
// helper, and a leaked entry outlives the binary and breaks every later git fetch.
func TestMain(m *testing.M) {
	os.Exit(runIsolatedFromGitConfig(m))
}

func runIsolatedFromGitConfig(m *testing.M) int {
	home, err := os.MkdirTemp("", "composition-gitconfig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Setup: temp git home:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "Setup:", k, err)
			return 1
		}
	}
	return m.Run()
}
