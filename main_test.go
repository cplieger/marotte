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
	"github.com/cplieger/keyenc"
	"github.com/cplieger/marotte/internal/forges"
)

// TestMain runs runMain when invoked the way git invokes the helper, so the test drives the real argv dispatch.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == forges.HelperCommand {
		os.Exit(runMain())
	}
	os.Exit(m.Run())
}

// runHelperGet runs the built binary as git runs its credential helper for
// forge.test over cfgDir, and answers its stdout, its stderr and its error.
func runHelperGet(t *testing.T, cfgDir string) (stdout, stderr string, err error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, forges.HelperCommand, "--config-dir", cfgDir, "get")
	cmd.Env = append(os.Environ(), "MAROTTE_BUNDLED_TOOLS="+filepath.Join(cfgDir, "absent.json"))
	cmd.Stdin = strings.NewReader("protocol=https\nhost=forge.test\n\n")
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	err = cmd.Run()
	return out.String(), diag.String(), err
}

// The fixture is the on-disk format written from outside the package: the
// record as JSON and the credential under the slot key keyenc builds.
func TestRunMain_HelperBranchWritesNoBootWarning(t *testing.T) {
	cfgDir := t.TempDir()
	store, err := creds.OpenFileStore(filepath.Join(cfgDir, "forge-store"))
	if err != nil {
		t.Fatalf("Setup: open store: %v", err)
	}
	id := forges.MakeID(forges.KindGitHub, "forge.test")
	if err := store.Save(keyenc.Join(id, "0"), creds.Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: "https://forge.test",
		Kind: forgeapi.CredKindStaticPAT, Token: "main-token", Issued: time.Now(),
	}); err != nil {
		t.Fatalf("Setup: save record: %v", err)
	}

	t.Run("no records file", func(t *testing.T) {
		stdout, stderr, err := runHelperGet(t, cfgDir)
		if err != nil || stderr != "" || stdout != "" {
			t.Errorf("marotte %s get with no records file = %v, stdout %q, stderr %q; want exit 0 and nothing",
				forges.HelperCommand, err, stdout, stderr)
		}
	})

	t.Run("a usable record", func(t *testing.T) {
		doc := `{"connections":[{"id":"` + id + `","kind":"github","host":"forge.test","slot":0,"sign_in":"sign-in-a"}]}`
		if err := os.WriteFile(filepath.Join(cfgDir, "forge-connections.json"), []byte(doc), 0o600); err != nil {
			t.Fatalf("Setup: write the records: %v", err)
		}
		stdout, stderr, err := runHelperGet(t, cfgDir)
		if err != nil {
			t.Errorf("marotte %s get exited with %v; stderr %q", forges.HelperCommand, err, stderr)
		}
		if stderr != "" {
			t.Errorf("marotte %s get wrote to stderr %q, want nothing with the bundled-tools file absent", forges.HelperCommand, stderr)
		}
		if want := "username=x-access-token\npassword=main-token\n"; stdout != want {
			t.Errorf("marotte %s get stdout = %q, want %q", forges.HelperCommand, stdout, want)
		}
	})
}
