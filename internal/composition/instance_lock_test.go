package composition

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const holdRootEnv = "MAROTTE_TEST_HOLD_ROOT"

func captureClaimLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	buf := new(bytes.Buffer)
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	return buf
}

func mustClaim(t *testing.T, roots ...ownedRoot) *heldRoots {
	t.Helper()
	held, err := claimRoots(roots)
	if err != nil {
		t.Fatalf("claimRoots(%v) = %v, want nil", roots, err)
	}
	t.Cleanup(held.release)
	return held
}

func TestClaimRoots_SecondInstanceOnAHeldKiroHomeIsRefused(t *testing.T) {
	logs := captureClaimLogs(t)
	kiro := t.TempDir()
	mustClaim(t, ownedRoot{roleConfigDir, t.TempDir()}, ownedRoot{roleKiroHome, kiro})

	configB := t.TempDir()
	held, err := claimRoots([]ownedRoot{{roleConfigDir, configB}, {roleKiroHome, kiro}})
	if held != nil {
		held.release()
	}
	refused, ok := errors.AsType[*rootHeldError](err)
	if !ok {
		t.Fatalf("second claim error = %v, want *rootHeldError", err)
	}
	if refused.Role != roleKiroHome || refused.Dir != kiro || refused.HolderPID != os.Getpid() {
		t.Errorf("refusal = %+v, want role %s, dir %s, holder pid %d", refused, roleKiroHome, kiro, os.Getpid())
	}
	line := logs.String()
	for _, want := range []string{
		`msg="another marotte instance holds this folder, so this one is refusing to start"`,
		"level=ERROR",
		"root=kiro_home",
		"path=" + kiro,
		fmt.Sprintf("holder_pid=%d", os.Getpid()),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("refusal log lacks %q:\n%s", want, line)
		}
	}
	mustClaim(t, ownedRoot{roleConfigDir, configB})
}

func TestClaimRoots_HoldsTheLockAcrossGarbageCollection(t *testing.T) {
	captureClaimLogs(t)
	dir := t.TempDir()
	held := mustClaim(t, ownedRoot{roleConfigDir, dir})
	runtime.GC()
	runtime.GC()
	if _, err := claimRoots([]ownedRoot{{roleConfigDir, dir}}); err == nil {
		t.Fatal("claim after GC succeeded, want the first claim still held")
	}
	runtime.KeepAlive(held)
}

func TestClaimRoots_ReleasedWhenTheHolderDies(t *testing.T) {
	captureClaimLogs(t)
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), holdRootEnv+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("holder said %q, %v; want held", line, err)
	}

	_, err = claimRoots([]ownedRoot{{roleConfigDir, dir}})
	refused, ok := errors.AsType[*rootHeldError](err)
	if !ok || refused.HolderPID != cmd.Process.Pid {
		t.Fatalf("claim while the holder lives = %v, want a refusal naming pid %d", err, cmd.Process.Pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	mustClaim(t, ownedRoot{roleConfigDir, dir})
}

// holdRootForParent is the child half of TestClaimRoots_ReleasedWhenTheHolderDies: it claims dir
// and holds it until killed or its stdin closes.
func holdRootForParent(dir string) int {
	held, err := claimRoots([]ownedRoot{{roleConfigDir, dir}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "hold root:", err)
		return 1
	}
	fmt.Println("held")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	runtime.KeepAlive(held)
	return 0
}

func TestClaimRoots_ClaimsAFolderSharedByTwoRolesOnce(t *testing.T) {
	captureClaimLogs(t)
	kiro := t.TempDir()
	alias := filepath.Join(t.TempDir(), "tools")
	if err := os.Symlink(kiro, alias); err != nil {
		t.Fatal(err)
	}
	mustClaim(t,
		ownedRoot{roleConfigDir, t.TempDir()},
		ownedRoot{roleKiroHome, kiro},
		ownedRoot{roleToolsDir, kiro},
		ownedRoot{roleToolsDir, alias})

	if _, err := claimRoots([]ownedRoot{{roleKiroHome, kiro}}); err == nil {
		t.Error("a second claim on the shared folder succeeded, want it held by the first")
	}
}

func TestClaimRoots_UnlockableFolderWarnsAndBoots(t *testing.T) {
	logs := captureClaimLogs(t)
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	mustClaim(t, ownedRoot{roleConfigDir, config}, ownedRoot{roleKiroHome, filepath.Join(file, "kiro")})

	line := logs.String()
	for _, want := range []string{"level=WARN", "root=kiro_home", "could not lock this folder"} {
		if !strings.Contains(line, want) {
			t.Errorf("warning lacks %q:\n%s", want, line)
		}
	}
	if _, err := claimRoots([]ownedRoot{{roleConfigDir, config}}); err == nil {
		t.Error("the lockable root was not claimed beside the unlockable one")
	}
}
