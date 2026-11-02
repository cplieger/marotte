package signin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/slogx/capture"
)

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return fi.Mode()
}

// fileHolds reports whether the file at path contains sub.
func fileHolds(t *testing.T, path, sub string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Contains(string(data), sub)
}

// writeFaults are the ways a write fails once the staged file exists: the filesystem will not make
// it private, or it cannot be checked or published.
var writeFaults = []struct {
	set  func(d *faultDisk)
	name string
}{
	{name: "chmod_refused", set: func(d *faultDisk) { d.widen, d.chmodFails = 0o660, true }},
	{name: "stat_fails", set: func(d *faultDisk) { d.statErr = syscall.EIO }},
	{name: "commit_fails", set: func(d *faultDisk) { d.commitErr = syscall.EIO }},
}

func clearFaults(d *faultDisk) { d.widen, d.chmodFails, d.statErr, d.commitErr = 0, false, nil, nil }

func TestSwitch_AFailedWriteKeepsTheOldAccountOnDisk(t *testing.T) {
	for _, tc := range writeFaults {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFake(nil, "cb", "_test/auth/token")
			h := newHarness(t, fb)
			h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
			h.disk.set(tc.set)
			st := h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
			if st.Label.Email != "alice@example.com" || st.SignIn.State != AttemptFailed || st.SignIn.Error != textSaveFailed {
				t.Errorf("Status = %+v, want alice and the attempt failed with the save error", st)
			}
			if a := h.mustAnswer("cb"); a.Account != "alice" {
				t.Errorf("answered %q, want alice", a.Account)
			}
			if !fileHolds(t, h.path(), `"account":"alice"`) {
				t.Error("the file no longer holds alice")
			}
			h.disk.set(clearFaults)
			h.reopen()
			if st := h.v.Status("cb"); st.Label.Email != "alice@example.com" {
				t.Errorf("after reopen = %+v, want alice", st)
			}
		})
	}
}

func TestSignOut_AFailedWriteKeepsBothAccounts(t *testing.T) {
	for _, tc := range writeFaults {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
			h := newHarness(t, fa, fb)
			h.signIn(fa, approval{cred: credOf(t, h.account("alice"))})
			h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
			h.disk.set(tc.set)
			if err := h.v.SignOut("cb"); !errors.Is(err, errNotSaved) {
				t.Fatalf("SignOut = %v, want errNotSaved", err)
			}
			if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
				t.Errorf("Status = %v, want still signed_in", st.Kind)
			}
			h.disk.set(clearFaults)
			h.reopen()
			for id, want := range map[BackendID]string{"cb": "alice@example.com", "cb2": "bob@example.com"} {
				if st := h.v.Status(id); st.Label.Email != want {
					t.Errorf("after reopen %s = %+v, want %s", id, st, want)
				}
			}
		})
	}
}

// TestVaultFile_AWriteClearsEveryLoadFailureItReplaced: a load failure, of the whole file or of one
// backend's entry, ends for every backend once a write replaces the file, so Status reads what a
// reopen of that file reads; a failed write ends none.
func TestVaultFile_AWriteClearsEveryLoadFailureItReplaced(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
	}{
		{name: "whole_file", file: "{"},
		{name: "one_entry", file: `{"backends":{"cb2":{"data":{"account":""}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
			h := newHarness(t, fa, fb)
			h.close()
			if err := os.WriteFile(h.path(), []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			h.open()
			if st := h.v.Status("cb2"); st.Kind != statusUnavailable {
				t.Fatalf("cb2 at open = %+v, want unavailable", st)
			}
			h.disk.set(func(d *faultDisk) { d.writeFails = 1 })
			h.signIn(fa, approval{cred: credOf(t, h.account("alice"))})
			if st := h.v.Status("cb2"); st.Kind != statusUnavailable {
				t.Errorf("cb2 after a failed write = %v, want still unavailable", st.Kind)
			}
			h.signIn(fa, approval{cred: credOf(t, h.account("alice"))})
			if st := h.v.Status("cb2"); st.Kind != StatusSignedOut {
				t.Errorf("cb2 after the write = %+v, want signed_out", st)
			}
			h.reopen()
			if st := h.v.Status("cb2"); st.Kind != StatusSignedOut {
				t.Errorf("cb2 after reopen = %v, want signed_out", st.Kind)
			}
		})
	}
}

// TestVaultFile_AStagedFileAFailedWriteLeftIsRemovedLater: a staged copy whose removal failed
// holds a credential, so the vault keeps retrying until it is gone.
func TestVaultFile_AStagedFileAFailedWriteLeftIsRemovedLater(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.disk.set(func(d *faultDisk) { d.commitErr, d.discardFails = syscall.EIO, 3 })
	if st := h.signIn(fb, approval{cred: credOf(t, h.account("bob"))}); st.SignIn.State != AttemptFailed {
		t.Fatalf("SignIn = %+v, want the switch failed", st.SignIn)
	}
	h.disk.set(func(d *faultDisk) { d.commitErr = nil })
	eventually(t, "the staged copy to be removed", func() bool {
		entries, err := os.ReadDir(h.dir)
		return err == nil && len(entries) == 1 && entries[0].Name() == fileName
	})
	if !fileHolds(t, h.path(), `"account":"alice"`) {
		t.Error("the file no longer holds alice")
	}
}

// TestRefresh_AStagedCopyThatCannotBeRemovedHoldsBackTheNextSave: while a failed save's staged
// copy cannot be removed, the worker retries only the removal, so no second copy is staged.
func TestRefresh_AStagedCopyThatCannotBeRemovedHoldsBackTheNextSave(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	rotate(h, fb, "rotated")
	h.disk.set(func(d *faultDisk) { d.commitErr, d.discardFails = syscall.EIO, 1_000_000 })
	h.mustAnswer("cb")
	eventually(t, "several retries of the removal", func() bool { return h.disk.discardCount() >= 5 })
	if ops := h.disk.opLog(); !slices.Equal(ops, []string{"stage", "stage"}) {
		t.Errorf("ops = %v, want the sign-in's stage and one renewal stage", ops)
	}
	h.disk.set(func(d *faultDisk) { d.commitErr, d.discardFails = nil, 0 })
	eventually(t, "the renewal to be saved", func() bool { return !h.v.anyDirty() })
	if ops := h.disk.opLog(); !slices.Equal(ops, []string{"stage", "stage", "discard", "stage"}) {
		t.Errorf("ops = %v, want the staged copy removed before one current write", ops)
	}
	if !fileHolds(t, h.path(), `"token":"rotated"`) {
		t.Error("the file does not hold the renewal")
	}
	if names := dirEntries(t, h.dir); !slices.Equal(names, []string{fileName}) {
		t.Errorf("directory holds %v, want only %s", names, fileName)
	}
}

// TestRefresh_TheShutdownSaveIsWrittenDespiteAStagedCopy: the last save at shutdown does not wait
// on a staged copy it cannot remove, so a rotated refresh token reaches the next start.
func TestRefresh_TheShutdownSaveIsWrittenDespiteAStagedCopy(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	rotate(h, fb, "rotated")
	h.disk.set(func(d *faultDisk) { d.commitErr, d.discardFails = syscall.EIO, 1_000_000 })
	h.mustAnswer("cb")
	eventually(t, "a retry of the removal", func() bool { return h.disk.discardCount() >= 3 })
	h.disk.set(func(d *faultDisk) { d.commitErr = nil })
	h.close()
	if !fileHolds(t, h.path(), `"token":"rotated"`) {
		t.Error("the shutdown save did not write the renewal")
	}
	h.disk.set(func(d *faultDisk) { d.discardFails = 0 })
	h.open()
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("after reopen answered %q, want the rotated token", a.Token)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestVaultFile_WrittenMode0600(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if m := fileMode(t, h.path()); m != 0o600 {
		t.Errorf("mode = %v, want 0600", m)
	}
}

// TestVaultFile_AWidenedBirthSurvivesABoot is the boot on a filesystem that births files
// group-writable: the saved sign-in is narrowed on its handle and used, never deleted.
func TestVaultFile_AWidenedBirthSurvivesABoot(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.close()
	if err := os.Chmod(h.path(), 0o660); err != nil {
		t.Fatal(err)
	}
	h.open()
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Fatalf("Status after a boot on a widened file = %+v, want signed_in", st)
	}
	if m := fileMode(t, h.path()); m != 0o600 {
		t.Errorf("mode = %v, want the bits narrowed to 0600", m)
	}
}

// TestVaultFile_ARefusedFileIsKeptAndNeverParsed: a file the boot cannot narrow is unavailable, is
// not parsed (its content is malformed, which would read differently) and stays on disk.
func TestVaultFile_ARefusedFileIsKeptAndNeverParsed(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.close()
	if err := os.WriteFile(h.path(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.disk.set(func(d *faultDisk) { d.widen, d.chmodFails = 0o660, true })
	h.open()
	st := h.v.Status("cb")
	if st.Kind != statusUnavailable || st.Reason != textNotPrivate {
		t.Errorf("Status = %+v, want unavailable with the not-private reason", st)
	}
	if _, err := os.Lstat(h.path()); err != nil {
		t.Errorf("the refused file was removed: %v", err)
	}
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Error("Answer served a token from a refused file")
	}
}

// TestVaultFile_ASignOutWithNoRecordKeepsARefusedFile: signing out of an unavailable sign-in
// changes nothing, so the operator's file survives until a sign-in replaces it.
func TestVaultFile_ASignOutWithNoRecordKeepsARefusedFile(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.close()
	if err := os.WriteFile(h.path(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.disk.set(func(d *faultDisk) { d.widen, d.chmodFails = 0o640, true })
	h.open()
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if _, err := os.Lstat(h.path()); err != nil {
		t.Errorf("SignOut removed the refused file: %v", err)
	}
}

func TestVaultFile_TightenedThenRecheckedOnTheHandle(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.close()
	h.disk.set(func(d *faultDisk) { d.widen = 0o644 })
	h.open()
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("Status = %+v, want the record used after the handle was narrowed", st)
	}
}

func TestVaultFile_ASymlinkIsRefusedAndNotFollowed(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.close()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.Rename(h.path(), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.path()); err != nil {
		t.Fatal(err)
	}
	h.open()
	if st := h.v.Status("cb"); st.Kind != statusUnavailable {
		t.Errorf("Status through a symlink = %+v, want unavailable", st)
	}
	if fileMode(t, target) != 0o600 {
		t.Error("the symlink's target changed mode")
	}
	if fileMode(t, h.path())&os.ModeSymlink == 0 {
		t.Error("the symlink is gone")
	}
}

func TestVaultFile_ADirectoryOrFIFOIsRefused(t *testing.T) {
	for _, tc := range []struct {
		make func(path string) error
		name string
	}{
		{name: "directory", make: func(p string) error { return os.Mkdir(p, 0o700) }},
		{name: "fifo", make: func(p string) error { return syscall.Mkfifo(p, 0o600) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFake(nil, "cb", "_test/auth/token")
			h := newHarness(t, fb)
			h.close()
			if err := tc.make(h.path()); err != nil {
				t.Fatal(err)
			}
			h.open()
			if st := h.v.Status("cb"); st.Kind != statusUnavailable || st.Reason != textNotPrivate {
				t.Errorf("Status = %+v, want unavailable, not private", st)
			}
		})
	}
}

func TestVaultFile_ChmodFailureStoresNothingAndFailsTheSignIn(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.disk.set(func(d *faultDisk) { d.widen, d.chmodFails = 0o660, true })
	st := h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if st.Kind != StatusSignedOut || st.SignIn.Error != textSaveFailed {
		t.Errorf("Status = %+v, want signed_out with the save-failed text", st)
	}
	if _, rerr := h.answer("cb"); rerr == nil {
		t.Error("Answer served a token after the sign-in failed to save")
	}
	if _, err := os.Lstat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unprotected file this write created is still there: %v", err)
	}
}

func TestVaultFile_AnUnusableEntryIsUnavailable(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.close()
	if err := os.WriteFile(h.path(), []byte(`{"backends":{"cb":{"data":{"account":""}},"gone":{"data":{}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	if st := h.v.Status("cb"); st.Kind != statusUnavailable || st.Reason != textUnreadable {
		t.Errorf("Status = %+v, want unavailable, unreadable", st)
	}
}

func TestVaultFile_MalformedIsUnavailableAndASignInOverwrites(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.close()
	if err := os.WriteFile(h.path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	if st := h.v.Status("cb"); st.Kind != statusUnavailable || st.Reason != textUnreadable {
		t.Errorf("Status = %+v, want unavailable, unreadable", st)
	}
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("after the overwriting sign-in and a reopen = %+v, want signed_in", st)
	}
}

func TestSignIn_SaveFailureChangesNothingAndReportsTheError(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	seen := h.watch()
	h.disk.set(func(d *faultDisk) { d.writeFails = 1 })
	st := h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if st.Kind != StatusSignedOut || st.SignIn != (Attempt{ID: 1, State: AttemptFailed, Error: textSaveFailed}) {
		t.Errorf("Status = %+v, want signed_out and the attempt failed with the save error", st)
	}
	if got := seen.list(); len(got) != 0 {
		t.Errorf("OnChange fired %+v for an unsaved sign-in", got)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen = %v, want signed_out", st.Kind)
	}
}

func TestSwitch_SaveFailureKeepsTheOldAccount(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.disk.set(func(d *faultDisk) { d.writeFails = 1 })
	h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
	if a := h.mustAnswer("cb"); a.Account != "alice" {
		t.Errorf("answered %q, want alice", a.Account)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Label.Email != "alice@example.com" {
		t.Errorf("after reopen = %+v, want alice", st)
	}
}

func TestSwitch_SaveSuccessSurvivesAReopen(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
	h.reopen()
	if st := h.v.Status("cb"); st.Label.Email != "bob@example.com" {
		t.Errorf("after reopen = %+v, want bob", st)
	}
	if a := h.mustAnswer("cb"); a.Account != "bob" {
		t.Errorf("answered %q, want bob", a.Account)
	}
}

func TestSignOut_RemoveFailureKeepsTheAccountAndReturnsTheError(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	h.disk.set(func(d *faultDisk) { d.removeErr = errors.New("fault: busy") })
	if err := h.v.SignOut("cb"); !errors.Is(err, errNotSaved) {
		t.Fatalf("SignOut = %v, want errNotSaved", err)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("Status = %v, want still signed_in", st.Kind)
	}
	if a := h.mustAnswer("cb"); a.Account != "alice" {
		t.Errorf("answered %q, want alice", a.Account)
	}
}

func TestSignOut_AMissingFileIsASuccess(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if err := os.Remove(h.path()); err != nil {
		t.Fatal(err)
	}
	if err := h.v.SignOut("cb"); err != nil {
		t.Errorf("SignOut with the file already gone = %v", err)
	}
}

func TestSignOut_CommittedSignOutDoesNotResurrect(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the last sign-out left the file: %v", err)
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("after reopen = %v, want signed_out", st.Kind)
	}
}

func emailOf(st Status) string { return st.Label.Email }

func emailFor(account string) string {
	if account == "" {
		return ""
	}
	return account + "@example.com"
}

// persistOps are the user mutations the worker saves: before is the account signed in first and
// request the one signed in to, "" for none and for a sign-out.
var persistOps = []struct{ name, before, request string }{
	{name: "first_sign_in", request: "alice"},
	{name: "switch", before: "alice", request: "bob"},
	{name: "last_sign_out", before: "alice"},
}

// persistOutcomes are what the worker meets saving one. landed: the file ends holding the
// request; signOutLands: only the last sign-out, which stages nothing, does. A held outcome keeps
// the worker from a verdict until release; onDiskWhileHeld is then whether the file already holds
// the request. stagedCopy > 0 leaves a failed write's staged copy before the request, whose
// removal fails that many times.
var persistOutcomes = []struct {
	fault           func(d *faultDisk, gate, entered chan struct{})
	release         func(d *faultDisk, gate chan struct{})
	name            string
	stagedCopy      int
	landed          bool
	signOutLands    bool
	held            bool
	onDiskWhileHeld bool
}{
	{name: "durable", fault: func(*faultDisk, chan struct{}, chan struct{}) {}, landed: true},
	{name: "staged_copy_removed", fault: func(*faultDisk, chan struct{}, chan struct{}) {}, stagedCopy: 1, landed: true},
	{name: "staged_copy_left", fault: func(*faultDisk, chan struct{}, chan struct{}) {}, stagedCopy: 1_000_000, signOutLands: true},
	{
		name:         "staged_copy_left_unsynced",
		fault:        func(d *faultDisk, _, _ chan struct{}) { d.unsynced = 1 },
		stagedCopy:   1_000_000,
		signOutLands: true,
	},
	{name: "refused", fault: func(d *faultDisk, _, _ chan struct{}) { d.refusing = true }},
	{name: "unsynced", fault: func(d *faultDisk, _, _ chan struct{}) { d.unsynced = 1 }, landed: true},
	{
		name:            "unsynced_unreadable",
		fault:           func(d *faultDisk, _, _ chan struct{}) { d.unsynced, d.readErr = 1, syscall.EIO },
		release:         func(d *faultDisk, _ chan struct{}) { d.set(func(d *faultDisk) { d.readErr = nil }) },
		landed:          true,
		held:            true,
		onDiskWhileHeld: true,
	},
	{
		name:    "write_held",
		fault:   func(d *faultDisk, gate, entered chan struct{}) { d.writeGate, d.entered = gate, entered },
		release: func(_ *faultDisk, gate chan struct{}) { close(gate) },
		landed:  true,
		held:    true,
	},
}

// TestPersist_EveryOutcomeIsPublishedFromTheFile is operation × worker outcome × caller deadline,
// on the shipped cadence and sign-out wait. Its oracle is a second vault opened on the directory,
// as a restart after a power loss that kept every rename: while the worker has no verdict the
// vault shows the state before, and once it has one, Status is what that restart reads. A
// sign-out past its deadline reports ErrInDoubt and the worker still lands it, as the only writer.
func TestPersist_EveryOutcomeIsPublishedFromTheFile(t *testing.T) {
	deadlines := []struct {
		name      string
		releaseAt time.Duration
		past      bool
	}{
		{name: "in_time", releaseAt: 5 * time.Second},
		{name: "past", releaseAt: verdictWait + 5*time.Second, past: true},
	}
	for _, op := range persistOps {
		for _, out := range persistOutcomes {
			for _, dl := range deadlines {
				if dl.past && !out.held {
					continue
				}
				t.Run(op.name+"_"+out.name+"_"+dl.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						landed := out.landed || (op.request == "" && out.signOutLands)
						var row *harness
						checkPersistOutcome(t, op.before, op.request, landed, func(h *harness, fb *fakeBackend) (hold func()) {
							row = h
							if out.stagedCopy > 0 {
								leaveStagedCopy(h, fb, out.stagedCopy)
							}
							gate, entered := make(chan struct{}), make(chan struct{}, 1)
							h.disk.set(func(d *faultDisk) { out.fault(d, gate, entered) })
							if !out.held {
								return nil
							}
							return func() {
								time.Sleep(dl.releaseAt)
								synctest.Wait()
								onDisk := emailFor(op.before)
								if out.onDiskWhileHeld {
									onDisk = emailFor(op.request)
								}
								if got := emailOf(h.crashed().Status("cb")); got != onDisk {
									t.Errorf("while held the file holds %q, want %q", got, onDisk)
								}
								if got := emailOf(h.v.Status("cb")); got != emailFor(op.before) {
									t.Errorf("while held Status shows %q, want the state before, %q", got, emailFor(op.before))
								}
								out.release(h.disk, gate)
								// The read-back retries on its own backoff.
								time.Sleep(2 * retryMax)
							}
						}, dl.past)
						if out.stagedCopy > 0 {
							want := 0
							if !out.landed {
								want = 1
							}
							if got := stagedCopies(row); len(got) != want {
								t.Errorf("staged copies on disk = %v, want %d", got, want)
							}
						}
					})
				})
			}
		}
	}
}

func checkPersistOutcome(t *testing.T, before, request string, landed bool, arm func(h *harness, fb *fakeBackend) (hold func()), past bool) {
	t.Helper()
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newRetryingHarness(t, fb)
	if before != "" {
		h.signIn(fb, approval{cred: credOf(t, h.account(before))})
	}
	seen := h.watch()
	hold := arm(h, fb)
	start := time.Now()
	type verdict struct {
		err  error
		took time.Duration
	}
	signedOut := make(chan verdict, 1)
	if request == "" {
		go func() {
			err := h.v.SignOut("cb")
			signedOut <- verdict{err: err, took: time.Since(start)}
		}()
	} else {
		if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
			t.Fatal(err)
		}
		fb.approvals <- approval{cred: credOf(t, h.account(request))}
	}
	if hold != nil {
		synctest.Wait()
		hold = sync.OnceFunc(hold)
		defer hold()
		mid := h.v.Status("cb")
		if got := emailOf(mid); got != emailFor(before) {
			t.Errorf("with no verdict yet Status shows %q, want the state before, %q", got, emailFor(before))
		}
		if request != "" && mid.SignIn.State != AttemptPending {
			t.Errorf("with no verdict yet the attempt is %v, want pending", mid.SignIn.State)
		}
		hold()
	}
	synctest.Wait()

	want := emailFor(before)
	if landed {
		want = emailFor(request)
	}
	if request == "" {
		v := <-signedOut
		switch {
		case past:
			if !errors.Is(v.err, ErrInDoubt) || v.took != verdictWait {
				t.Errorf("SignOut = %v after %v, want ErrInDoubt at %v", v.err, v.took, verdictWait)
			}
		case landed:
			if v.err != nil {
				t.Errorf("SignOut = %v, want nil", v.err)
			}
		default:
			if !errors.Is(v.err, errNotSaved) || errors.Is(v.err, ErrInDoubt) {
				t.Errorf("SignOut = %v, want errNotSaved alone", v.err)
			}
		}
	} else {
		wantAttempt := AttemptSignedIn
		if !landed {
			wantAttempt = AttemptFailed
		}
		if st := h.v.Status("cb"); st.SignIn.State != wantAttempt {
			t.Errorf("attempt = %+v, want %v", st.SignIn, wantAttempt)
		}
	}
	if got := emailOf(h.v.Status("cb")); got != want {
		t.Errorf("after the verdict Status shows %q, want %q", got, want)
	}
	if got := emailOf(h.crashed().Status("cb")); got != want {
		t.Errorf("after the verdict a restart reads %q, want %q", got, want)
	}
	wantChanges := 0
	if landed {
		wantChanges = 1
	}
	if got := seen.list(); len(got) != wantChanges {
		t.Errorf("changes = %+v, want %d", got, wantChanges)
	}
	h.disk.set(func(d *faultDisk) { d.refusing = false })
	time.Sleep(retryMax)
	synctest.Wait()
	if h.v.anyDirty() || !h.disk.lastChangeDurable() {
		t.Error("the worker never wrote the published state durably")
	}
	if n := h.disk.maxOverlap(); n != 1 {
		t.Errorf("at most %d writes ran at once, want 1", n)
	}
}

// leaveStagedCopy fails a sign-in so that its staged file stays on disk until its removal has
// failed discards times; the account signed in, if any, is unchanged.
func leaveStagedCopy(h *harness, fb *fakeBackend, discards int) {
	h.t.Helper()
	h.disk.set(func(d *faultDisk) { d.commitErr, d.discardFails = syscall.EIO, discards })
	if st := h.signIn(fb, approval{cred: credOf(h.t, h.account("mallory"))}); st.SignIn.State != AttemptFailed {
		h.t.Fatalf("the sign-in leaving a staged copy = %+v, want failed", st.SignIn)
	}
	h.disk.set(func(d *faultDisk) { d.commitErr = nil })
	if len(stagedCopies(h)) == 0 {
		h.t.Fatal("the failed sign-in left no staged copy")
	}
}

func stagedCopies(h *harness) []string {
	h.t.Helper()
	return slices.DeleteFunc(dirEntries(h.t, h.dir), func(n string) bool { return n == fileName })
}

// TestPersist_ASubmissionAfterADeadlineWaitsForTheHeldWrite: a sign-out that gave up on a held
// write leaves that write the worker's, so a sign-in submitted next is saved after it, never
// beside it.
func TestPersist_ASubmissionAfterADeadlineWaitsForTheHeldWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fb := newFake(nil, "cb", "_test/auth/token")
		h := newRetryingHarness(t, fb)
		h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
		seen := h.watch()
		gate, entered := make(chan struct{}), make(chan struct{}, 1)
		h.disk.set(func(d *faultDisk) { d.writeGate, d.entered = gate, entered })
		if err := h.v.SignOut("cb"); !errors.Is(err, ErrInDoubt) {
			t.Fatalf("SignOut = %v, want ErrInDoubt", err)
		}
		if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
			t.Fatal(err)
		}
		fb.approvals <- approval{cred: credOf(t, h.account("carol"))}
		synctest.Wait()
		if got := emailOf(h.v.Status("cb")); got != "alice@example.com" {
			t.Errorf("while held Status shows %q, want alice", got)
		}
		close(gate)
		synctest.Wait()
		want := []Change{
			{Backend: "cb", From: StatusSignedIn, To: StatusSignedOut},
			{Backend: "cb", From: StatusSignedOut, To: StatusSignedIn},
		}
		if got := seen.list(); !slices.Equal(got, want) {
			t.Errorf("changes = %+v, want %+v", got, want)
		}
		if got := emailOf(h.crashed().Status("cb")); got != "carol@example.com" {
			t.Errorf("a restart reads %q, want carol", got)
		}
		if n := h.disk.maxOverlap(); n != 1 {
			t.Errorf("at most %d writes ran at once, want 1", n)
		}
	})
}

// TestSignOut_BeforeTheSignInsTurnDropsIt: a sign-in approved while the worker is busy, then
// cancelled by a sign-out before its turn, is never saved or shown.
func TestSignOut_BeforeTheSignInsTurnDropsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
		h := newRetryingHarness(t, fa, fb)
		seen := h.watch()
		gate, entered := make(chan struct{}), make(chan struct{}, 1)
		h.disk.set(func(d *faultDisk) { d.writeGate, d.entered = gate, entered })
		if _, err := h.v.StartSignIn(t.Context(), "cb2", FlowParams{Flow: FlowDevice}); err != nil {
			t.Fatal(err)
		}
		fb.approvals <- approval{cred: credOf(t, h.account("bob"))}
		<-entered
		if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
			t.Fatal(err)
		}
		fa.approvals <- approval{cred: credOf(t, h.account("alice"))}
		synctest.Wait()
		out := make(chan error, 1)
		go func() { out <- h.v.SignOut("cb") }()
		synctest.Wait()
		close(gate)
		if err := <-out; err != nil {
			t.Errorf("SignOut = %v", err)
		}
		synctest.Wait()
		if st := h.v.Status("cb"); st.Kind != StatusSignedOut || st.SignIn.State != attemptCancelled {
			t.Errorf("cb = %+v, want signed out with the attempt cancelled", st)
		}
		want := []Change{{Backend: "cb2", From: StatusSignedOut, To: StatusSignedIn}}
		if got := seen.list(); !slices.Equal(got, want) {
			t.Errorf("changes = %+v, want only bob's %+v", got, want)
		}
		if fileHolds(t, h.path(), `"account":"alice"`) {
			t.Error("the cancelled sign-in reached the file")
		}
	})
}

// TestPersist_AnIdleJobSharingARenewalsWakeStillSavesIt: the wake coalesces, so a renewal's nudge
// can be the one a sign-out with nothing to remove consumes. The renewal is installed here as renew
// leaves it when its nudge was dropped.
func TestPersist_AnIdleJobSharingARenewalsWakeStillSavesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
		h := newRetryingHarness(t, fa, fb)
		h.signIn(fa, approval{cred: credOf(t, h.account("alice"))})
		s := h.v.slots["cb"]
		rotated := h.account("alice")
		rotated.Token = "rotated"
		s.mu.Lock()
		renewed := &record{cred: credOf(t, rotated), gen: s.desired.gen}
		s.desired, s.effective, s.dirty = renewed, renewed, true
		s.mu.Unlock()
		if err := h.v.SignOut("cb2"); err != nil {
			t.Fatalf("SignOut = %v", err)
		}
		synctest.Wait()
		if h.v.anyDirty() {
			t.Error("the pass that took the sign-out did not save the renewal")
		}
		if a := h.crashed().slots["cb"].desired; a == nil || !strings.Contains(string(a.cred.Data), `"token":"rotated"`) {
			t.Error("a restart does not read the renewal")
		}
	})
}

// TestSignIn_AStagedCopyLeftRefusesTheSaveAndLogsItsPath: while a failed save's staged copy cannot
// be removed, a sign-in is refused rather than staging another copy, the log names the copy, and
// the sign-in is saved once the copy is gone, by the cleanup retry or by hand.
func TestSignIn_AStagedCopyLeftRefusesTheSaveAndLogsItsPath(t *testing.T) {
	for _, tc := range []struct {
		remove    func(h *harness, copyPath string)
		name      string
		retryBase time.Duration
	}{
		{name: "by_the_retry", retryBase: time.Millisecond, remove: func(h *harness, _ string) {
			h.disk.set(func(d *faultDisk) { d.discardFails = 0 })
			eventually(h.t, "the retry to remove the copy", func() bool { return len(stagedCopies(h)) == 0 })
		}},
		{name: "by_hand", retryBase: time.Hour, remove: func(h *harness, copyPath string) {
			if err := os.Remove(copyPath); err != nil {
				h.t.Fatal(err)
			}
			h.disk.set(func(d *faultDisk) { d.discardFails = 0 })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := capture.Default(t)
			fb := newFake(nil, "cb", "_test/auth/token")
			h := newTimedHarness(t, tc.retryBase, 5*time.Second, fb)
			h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
			leaveStagedCopy(h, fb, 1_000_000)
			copies := stagedCopies(h)
			copyPath := filepath.Join(h.dir, copies[0])
			st := h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
			if st.SignIn.State != AttemptFailed || st.SignIn.Error != textSaveFailed || st.Label.Email != "alice@example.com" {
				t.Errorf("Status = %+v, want alice kept and the attempt failed with %q", st, textSaveFailed)
			}
			if got := stagedCopies(h); !slices.Equal(got, copies) {
				t.Errorf("staged copies = %v, want only the earlier %v", got, copies)
			}
			if got := rec.AttrValuesExact(msgStagedCopyLeft, "path"); !slices.Equal(got, []string{copyPath}) {
				t.Errorf("%q paths = %v, want [%s]", msgStagedCopyLeft, got, copyPath)
			}
			if n := rec.CountLevel(slog.LevelError, msgStagedCopyLeft); n != 1 {
				t.Errorf("ERROR records of the refusal = %d, want 1", n)
			}
			tc.remove(h, copyPath)
			if st := h.signIn(fb, approval{cred: credOf(t, h.account("bob"))}); st.SignIn.State != AttemptSignedIn {
				t.Errorf("after the copy is gone the sign-in = %+v, want saved", st.SignIn)
			}
			if !fileHolds(t, h.path(), `"account":"bob"`) {
				t.Error("the file does not hold bob")
			}
		})
	}
}

// TestSignOut_AStagedCopyLeftRefusesOnlyARewrite: a sign-out that keeps another backend's sign-in
// stages a file, so it waits for the copy; one that removes the file stages nothing and is saved.
func TestSignOut_AStagedCopyLeftRefusesOnlyARewrite(t *testing.T) {
	fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
	h := newTimedHarness(t, time.Hour, 5*time.Second, fa, fb)
	h.signIn(fa, approval{cred: credOf(t, h.account("alice"))})
	h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
	leaveStagedCopy(h, fb, 1_000_000)
	if err := h.v.SignOut("cb"); !errors.Is(err, errNotSaved) || !strings.Contains(err.Error(), "staged copy") {
		t.Errorf("SignOut(cb) = %v, want errNotSaved naming the staged copy", err)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("cb = %v, want still signed in", st.Kind)
	}
	if !fileHolds(t, h.path(), `"account":"alice"`) || len(stagedCopies(h)) != 1 {
		t.Error("the refused sign-out changed the directory")
	}
	if err := h.v.SignOut("cb2"); !errors.Is(err, errNotSaved) {
		t.Errorf("SignOut(cb2) = %v, want errNotSaved while alice still needs a rewrite", err)
	}
	h.disk.set(func(d *faultDisk) { d.discardFails = 0 })
	if err := h.v.SignOut("cb"); err != nil {
		t.Errorf("SignOut(cb) once the copy is removable = %v", err)
	}
	leaveStagedCopy(h, fb, 1_000_000)
	if err := h.v.SignOut("cb2"); err != nil {
		t.Errorf("the last SignOut = %v, want the removal saved", err)
	}
	if _, err := os.Stat(h.path()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the last sign-out left the file: %v", err)
	}
}

// TestPersist_TheReadBackTrustsTheFileNotTheWrite: when something else rewrote the file between
// an unsynced write and its read-back, the change is not shown, and the worker writes the shown
// state back.
func TestPersist_TheReadBackTrustsTheFileNotTheWrite(t *testing.T) {
	for _, op := range persistOps[1:] {
		t.Run(op.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fb := newFake(nil, "cb", "_test/auth/token")
				h := newRetryingHarness(t, fb)
				h.signIn(fb, approval{cred: credOf(t, h.account(op.before))})
				seen := h.watch()
				h.disk.set(func(d *faultDisk) { d.unsynced, d.readErr = 1, syscall.EIO })
				out := make(chan error, 1)
				if op.request == "" {
					go func() { out <- h.v.SignOut("cb") }()
				} else {
					if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
						t.Fatal(err)
					}
					fb.approvals <- approval{cred: credOf(t, h.account(op.request))}
					close(out)
				}
				synctest.Wait()
				data, err := json.Marshal(fileShape{Backends: map[BackendID]fileEntry{"cb": {Data: credOf(t, h.account("mallory")).Data}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.path(), data, 0o600); err != nil {
					t.Fatal(err)
				}
				h.disk.set(func(d *faultDisk) { d.readErr = nil })
				time.Sleep(2 * retryMax)
				synctest.Wait()
				if op.request == "" {
					if err := <-out; !errors.Is(err, errNotSaved) {
						t.Errorf("SignOut = %v, want errNotSaved", err)
					}
				} else if st := h.v.Status("cb"); st.SignIn.State != AttemptFailed {
					t.Errorf("attempt = %+v, want failed", st.SignIn)
				}
				if got := emailOf(h.v.Status("cb")); got != emailFor(op.before) {
					t.Errorf("Status shows %q, want %q", got, emailFor(op.before))
				}
				if got := seen.list(); len(got) != 0 {
					t.Errorf("changes = %+v, want none", got)
				}
				if got := emailOf(h.crashed().Status("cb")); got != emailFor(op.before) {
					t.Errorf("a restart reads %q, want the shown state written back, %q", got, emailFor(op.before))
				}
			})
		})
	}
}

// TestPersist_AnUnconfirmedChangeIsWrittenBackAtShutdown: shutdown during the read-back leaves
// the change unshown, so the last save writes the shown state and a restart reads it.
func TestPersist_AnUnconfirmedChangeIsWrittenBackAtShutdown(t *testing.T) {
	for _, op := range persistOps {
		t.Run(op.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fb := newFake(nil, "cb", "_test/auth/token")
				h := newRetryingHarness(t, fb)
				if op.before != "" {
					h.signIn(fb, approval{cred: credOf(t, h.account(op.before))})
				}
				h.disk.set(func(d *faultDisk) { d.unsynced, d.readErr = 1, syscall.EIO })
				out := make(chan error, 1)
				if op.request == "" {
					go func() { out <- h.v.SignOut("cb") }()
				} else {
					if _, err := h.v.StartSignIn(t.Context(), "cb", FlowParams{Flow: FlowDevice}); err != nil {
						t.Fatal(err)
					}
					fb.approvals <- approval{cred: credOf(t, h.account(op.request))}
					close(out)
				}
				synctest.Wait()
				h.cancel()
				h.v.Wait()
				if err, ok := <-out; ok && !errors.Is(err, ErrInDoubt) {
					t.Errorf("SignOut = %v, want ErrInDoubt", err)
				}
				h.disk.set(func(d *faultDisk) { d.readErr = nil })
				if got := emailOf(h.crashed().Status("cb")); got != emailFor(op.before) {
					t.Errorf("a restart reads %q, want the shown state, %q", got, emailFor(op.before))
				}
			})
		})
	}
}

// TestSignOut_AfterAnUnsyncedSignInRemovesIt: a sign-in the file holds but may not keep is shown,
// so signing out of it removes it from the file.
func TestSignOut_AfterAnUnsyncedSignInRemovesIt(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.disk.set(func(d *faultDisk) { d.unsynced = 1 })
	if st := h.signIn(fb, approval{cred: credOf(t, h.account("alice"))}); st.Kind != StatusSignedIn {
		t.Fatalf("Status = %+v, want alice signed in", st)
	}
	if err := h.v.SignOut("cb"); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if st := h.crashed().Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("a restart reads %+v, want signed out", st)
	}
}

// TestSignOut_AfterShutdownIsNotSaved: no worker is left to save it, so it fails at once.
func TestSignOut_AfterShutdownIsNotSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fb := newFake(nil, "cb", "_test/auth/token")
		h := newRetryingHarness(t, fb)
		h.signIn(fb, approval{cred: credOf(t, h.account("alice"))})
		h.cancel()
		h.v.Wait()
		start := time.Now()
		if err := h.v.SignOut("cb"); !errors.Is(err, errNotSaved) {
			t.Errorf("SignOut = %v, want errNotSaved", err)
		}
		if took := time.Since(start); took != 0 {
			t.Errorf("SignOut took %v, want no wait", took)
		}
		if !fileHolds(t, h.path(), `"account":"alice"`) {
			t.Error("the file no longer holds alice")
		}
	})
}

// TestVaultFile_AnUnsyncedSaveClearsTheLoadFailure: the file Open could not use was replaced, so
// Status reads what a restart reads.
func TestVaultFile_AnUnsyncedSaveClearsTheLoadFailure(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.close()
	if err := os.WriteFile(h.path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.open()
	h.disk.set(func(d *faultDisk) { d.unsynced = 1 })
	if st := h.signIn(fb, approval{cred: credOf(t, h.account("alice"))}); st.Kind != StatusSignedIn {
		t.Errorf("Status = %+v, want signed_in", st)
	}
	if st := h.crashed().Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("after a crash = %v, want signed_in", st.Kind)
	}
}

// TestSaveLoop_AFailedSaveIsRetriedOnlyOnTheBackoff: a save the loop itself fails, whether it was
// not durable or left a staged copy, is retried at 1 s doubling to 60 s and never sooner.
func TestSaveLoop_AFailedSaveIsRetriedOnlyOnTheBackoff(t *testing.T) {
	edges := []time.Duration{
		0, 1 * time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 31 * time.Second,
		63 * time.Second, 123 * time.Second, 183 * time.Second, 243 * time.Second,
	}
	for _, tc := range []struct {
		fault    func(d *faultDisk)
		attempts func(d *faultDisk) int
		name     string
	}{
		{
			name:     "not_durable",
			fault:    func(d *faultDisk) { d.unsynced = len(edges) - 1 },
			attempts: (*faultDisk).writeCount,
		},
		{
			name:     "staged_copy_left",
			fault:    func(d *faultDisk) { d.commitErr, d.discardFails = syscall.EIO, 1_000_000 },
			attempts: (*faultDisk).discardCount,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fb := newFake(nil, "cb", "_test/auth/token")
				h := newRetryingHarness(t, fb)
				h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
				rotate(h, fb, "rotated")
				h.disk.set(tc.fault)
				base := tc.attempts(h.disk)
				h.mustAnswer("cb")
				synctest.Wait()
				start := time.Now()
				if got := tc.attempts(h.disk) - base; got != 1 {
					t.Errorf("after the renewal: %d attempts, want 1", got)
				}
				for i, edge := range edges[1:] {
					at := start.Add(edge)
					time.Sleep(time.Until(at) - time.Millisecond)
					synctest.Wait()
					if got := tc.attempts(h.disk) - base; got != i+1 {
						t.Errorf("just before %v: %d attempts, want %d", edge, got, i+1)
					}
					time.Sleep(time.Until(at))
					synctest.Wait()
					if got := tc.attempts(h.disk) - base; got != i+2 {
						t.Errorf("at %v: %d attempts, want %d", edge, got, i+2)
					}
				}
			})
		})
	}
}

// TestSaveLoop_ARenewalThatChangesNothingLeavesTheBackoffAlone: while a save keeps failing, every
// ask renews again and fails transiently; those renewals change nothing on disk, so the save is
// still retried only at the backoff's edges, never at the ask rate.
func TestSaveLoop_ARenewalThatChangesNothingLeavesTheBackoffAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fb := newFake(nil, "cb", "_test/auth/token")
		h := newRetryingHarness(t, fb)
		h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
		rotate(h, fb, "rotated")
		h.disk.set(func(d *faultDisk) { d.unsynced = 1_000_000 })
		base := h.disk.writeCount()
		h.mustAnswer("cb")
		synctest.Wait()
		start := time.Now()
		h.clk.advance(56 * time.Minute)
		fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, errors.New("fault: unreachable") })
		for i, edge := range []time.Duration{time.Second, 3 * time.Second, 7 * time.Second} {
			time.Sleep(time.Until(start.Add(edge)) - 100*time.Millisecond)
			for range 5 {
				if a := h.mustAnswer("cb"); a.Token != "rotated" {
					t.Fatalf("answered %q, want the unrenewed token still served", a.Token)
				}
				synctest.Wait()
			}
			if got := h.disk.writeCount() - base; got != i+1 {
				t.Errorf("before %v, after five asks: %d save attempts, want %d", edge, got, i+1)
			}
			time.Sleep(time.Until(start.Add(edge)))
			synctest.Wait()
			if got := h.disk.writeCount() - base; got != i+2 {
				t.Errorf("at %v: %d save attempts, want %d", edge, got, i+2)
			}
		}
		if n := fb.refreshCount(); n < 16 {
			t.Errorf("refreshes = %d, want one per ask", n)
		}
	})
}

func TestRefresh_AnUnsyncedSaveIsWrittenAgain(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	rotate(h, fb, "rotated")
	h.disk.set(func(d *faultDisk) { d.unsynced = 1 })
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("answered %q, want the renewal", a.Token)
	}
	eventually(t, "a second, durable save", func() bool { return h.disk.writeCount() >= 3 && !h.v.anyDirty() })
	h.reopen()
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("after reopen answered %q, want the rotated token", a.Token)
	}
}

// rotate makes fb's refresh rotate the refresh token, standing in for a new "Token".
func rotate(h *harness, fb *fakeBackend, token string) {
	fb.setRefresh(func(_ context.Context, c fakeCred) (fakeCred, error) {
		c.Token = token
		c.Expiry = h.clk.now().Add(time.Hour)
		return c, nil
	})
}

func TestRefresh_SaveFailureStillAnswersAndRetriesTheWrite(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	rotate(h, fb, "rotated")
	h.disk.set(func(d *faultDisk) { d.writeFails = 2 })
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("answered %q, want the renewal despite the failed save", a.Token)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedIn {
		t.Errorf("Status = %v, want signed_in", st.Kind)
	}
	eventually(t, "the retry to land", func() bool { return h.disk.writeCount() >= 4 })
	h.reopen()
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("after reopen answered %q, want the rotated token saved by the retry", a.Token)
	}
}

func TestRefresh_ANewerSignInStopsTheRetry(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	h.disk.set(func(d *faultDisk) { d.writeFails = 1_000_000 })
	h.mustAnswer("cb")
	eventually(t, "the retry to fail at least twice", func() bool { return h.disk.writeCount() >= 3 })
	h.disk.set(func(d *faultDisk) { d.writeFails = 0 })
	h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
	if h.v.anyDirty() {
		t.Error("the sign-in's write left the backend dirty, so the retry would write an older record")
	}
	h.reopen()
	if st := h.v.Status("cb"); st.Label.Email != "bob@example.com" {
		t.Errorf("after reopen = %+v, want bob", st)
	}
}

func TestRefresh_PrivacyRefusalOnTheRenewalIsRetried(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	rotate(h, fb, "rotated")
	h.disk.set(func(d *faultDisk) { d.widen, d.chmodFails = 0o660, true })
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("answered %q, want the renewal", a.Token)
	}
	eventually(t, "the refused renewal write and a retry", func() bool { return h.disk.writeCount() >= 3 })
	if !fileHolds(t, h.path(), `"token":"alice-token"`) {
		t.Error("a refused renewal write changed the file")
	}
	h.disk.set(func(d *faultDisk) { d.widen, d.chmodFails = 0, false })
	eventually(t, "the retry to write the renewal", func() bool { return fileHolds(t, h.path(), `"token":"rotated"`) })
	if m := fileMode(t, h.path()); m != 0o600 {
		t.Errorf("mode = %v, want 0600", m)
	}
	h.reopen()
	if a := h.mustAnswer("cb"); a.Token != "rotated" {
		t.Errorf("after reopen answered %q, want the rotated token", a.Token)
	}
}

// TestRefresh_ARenewalDuringAnotherSaveIsStillSaved: another backend's sign-in is writing a map
// that holds the old record when the renewal lands; that write must not count as saving it.
func TestRefresh_ARenewalDuringAnotherSaveIsStillSaved(t *testing.T) {
	fa, fb := newFake(nil, "cb", "_test/auth/token"), newFake(nil, "cb2", "_test/auth/token2")
	h := newHarness(t, fa, fb)
	h.signIn(fa, approval{cred: credOf(t, staleAccount(h, "alice"))})
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	h.disk.set(func(d *faultDisk) { d.writeGate, d.entered = gate, entered })
	if _, err := h.v.StartSignIn(t.Context(), "cb2", FlowParams{Flow: FlowDevice}); err != nil {
		t.Fatal(err)
	}
	fb.approvals <- approval{cred: credOf(t, h.account("bob"))}
	<-entered
	if a := h.mustAnswer("cb"); a.Token != "alice-token+r" {
		t.Errorf("answered %q, want the renewal", a.Token)
	}
	release()
	h.settled("cb2")
	eventually(t, "the renewal to be saved", func() bool { return fileHolds(t, h.path(), `"token":"alice-token+r"`) })
}

func TestLapse_SaveFailureStillAnswersAccessDenied(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	fb.setRefresh(func(context.Context, fakeCred) (fakeCred, error) { return fakeCred{}, ErrRejected })
	h.disk.set(func(d *faultDisk) { d.writeFails = 1_000_000 })
	if _, rerr := h.answer("cb"); rerr == nil || rerr.Message != "cb: rejected" {
		t.Errorf("answer = %v, want the rejected refusal", rerr)
	}
	if st := h.v.Status("cb"); st.Kind != StatusSignedOut {
		t.Errorf("Status = %v, want the lapse held in memory", st.Kind)
	}
}

func TestRefresh_PendingSwitchRenewsEffectiveInMemoryOnly(t *testing.T) {
	fb := newFake(nil, "cb", "_test/auth/token")
	notLive(fb)
	h := newHarness(t, fb)
	h.signIn(fb, approval{cred: credOf(t, staleAccount(h, "alice"))})
	h.answer("cb")
	h.signIn(fb, approval{cred: credOf(t, h.account("bob"))})
	writes := h.disk.writeCount()
	h.clk.advance(58 * time.Minute)
	if a := h.mustAnswer("cb"); a.Account != "alice" || a.Token != "alice-token+r+r" {
		t.Errorf("answered %+v, want alice's second renewal", a)
	}
	h.close()
	if got := h.disk.writeCount(); got != writes {
		t.Errorf("writes = %d, want %d: a renewal of the pending switch's old account was saved", got, writes)
	}
	h.open()
	if st := h.v.Status("cb"); st.Label.Email != "bob@example.com" {
		t.Errorf("after reopen = %+v, want the desired record, bob", st)
	}
}
