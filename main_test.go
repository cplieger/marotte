package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/marotte/internal/forges"
)

// TestMain runs runMain when invoked the way git invokes the helper, so the test drives the real argv dispatch.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == forges.HelperCommand {
		os.Exit(runMain())
	}
	os.Exit(m.Run())
}

func TestRunMain_HelperBranchWritesNoBootWarning(t *testing.T) {
	cfgDir := t.TempDir()
	store, err := creds.OpenFileStore(filepath.Join(cfgDir, "forge-store"))
	if err != nil {
		t.Fatalf("Setup: open store: %v", err)
	}
	if err := store.Save("github:forge.test", creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://forge.test",
		Kind: forgeapi.CredKindStaticPAT, Token: "main-token", Issued: time.Now(),
	}); err != nil {
		t.Fatalf("Setup: save record: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, forges.HelperCommand, "--config-dir", cfgDir, "get")
	cmd.Env = append(os.Environ(), "MAROTTE_BUNDLED_TOOLS="+filepath.Join(cfgDir, "absent.json"))
	cmd.Stdin = strings.NewReader("protocol=https\nhost=forge.test\n\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	if runErr != nil {
		t.Errorf("marotte %s get exited with %v; stderr %q", forges.HelperCommand, runErr, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("marotte %s get wrote to stderr %q, want nothing with the bundled-tools file absent", forges.HelperCommand, stderr.String())
	}
	if got, want := stdout.String(), "username=x-access-token\npassword=main-token\n"; got != want {
		t.Errorf("marotte %s get stdout = %q, want %q", forges.HelperCommand, got, want)
	}
}
