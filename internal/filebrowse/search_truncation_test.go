package filebrowse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v4"
)

// One rule in both directions: a part of the tree the search meant to read and could not marks the
// answer Truncated; a part it deliberately skipped does not.

// requireUnprivileged skips a fixture whose subject is a permission wall when the
// test runs as root, because root opens a 0000 directory and the assertion would
// pass without the property under test ever being exercised. The classifier table
// and the ReadDir case below are the witnesses that hold at any privilege.
func requireUnprivileged(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits this fixture is built from; " +
			"TestLogSearchReadError_ClassifiesLossVersusSkip and " +
			"TestWalkDir_ReadDirFailureMarksTruncated cover the same rule unprivileged")
	}
}

// TestSearch_UnreadableDirectoryMarksTruncated — a walk that cannot descend into a
// subdirectory must report a truncated answer, or a hit inside that subtree is
// indistinguishable from no hit at all.
func TestSearch_UnreadableDirectoryMarksTruncated(t *testing.T) {
	requireUnprivileged(t)
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"open/found.txt":   "the needle is here\n",
		"closed/hidden.md": "the needle is in here too\n",
	})
	closed := filepath.Join(dir, "closed")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with a subtree the walk could not open; the reply claims to have covered it")
	}
	if got := matchPaths(res); len(got) != 1 || !strings.HasSuffix(got[0], "open/found.txt") {
		t.Errorf("matches = %v, want just open/found.txt", got)
	}
}

// TestSearch_UnreadableFileMarksTruncated is the same rule one level down. The
// file was admitted by every gate and never read, so it is not in `scanned` —
// counting it would claim bytes the search never saw — and Truncated is what
// says the answer has a hole where it sat.
func TestSearch_UnreadableFileMarksTruncated(t *testing.T) {
	requireUnprivileged(t)
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"readable.txt": "the needle is here\n",
		"walled.txt":   "the needle may be in here\n",
	})
	walled := filepath.Join(dir, "walled.txt")
	if err := os.Chmod(walled, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(walled, 0o600) })

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with an admitted file the walk could not open")
	}
	if got := matchPaths(res); len(got) != 1 || !strings.HasSuffix(got[0], "readable.txt") {
		t.Errorf("matches = %v, want just readable.txt", got)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1: the walled file was never read, which is why the answer is partial", res.Scanned)
	}
}

// TestSearch_DeliberateSkipsDoNotMarkTruncated is the other direction, and the
// reason the flag is worth anything: it must stay EXACT. A tree full of entries
// the search chose not to read is completely covered, and a result that called
// itself partial whenever a symlink or a binary sat in the tree would train the
// reader to ignore the word.
func TestSearch_DeliberateSkipsDoNotMarkTruncated(t *testing.T) {
	h, dir, prefix := testDir(t)
	writeTree(t, dir, map[string]string{
		"found.txt":            "the needle is here\n",
		"node_modules/dep.txt": "needle inside an excluded directory\n",
		"blob.bin":             "binary\x00needle\n",
		"link-target.txt":      "needle behind a symlink\n",
	})
	if err := os.Symlink(filepath.Join(dir, "link-target.txt"), filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{
		"mode": "contents",
		"path": prefix, "q": "needle",
	}))

	if res.Truncated {
		t.Errorf("truncated = true on a tree whose unread entries were all deliberate skips (matches %v)", matchPaths(res))
	}
	if got := matchPaths(res); len(got) != 2 {
		t.Errorf("matches = %v, want found.txt and link-target.txt", got)
	}
}

// TestReadCandidate_VanishedFileIsCoveredNotLost is the same rule at the read: a
// file that vanishes between the dirent and the open is the ordinary state of a
// tree the agent is writing to, so it is a skip the answer covers. It stays in
// Scanned, because the walk classified it and nothing was refused to it, and it
// does not mark the answer partial.
func TestReadCandidate_VanishedFileIsCoveredNotLost(t *testing.T) {
	h, backing := searchHandlerAt(t, "/workspace")
	writeTree(t, backing, map[string]string{"gone.txt": "the needle was here\n"})
	dir := searchDirAt(t, h, "/workspace")
	cand := searchCandidate{name: "gone.txt", abs: "/workspace/gone.txt"}
	if err := os.Remove(filepath.Join(backing, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	v := newContentVisitor(t.Context(), "needle", false)
	got := v.readCandidate(dir, cand)
	if got.unread {
		t.Error("readCandidate reported a vanished file as unread; a file that is gone was not refused to the search")
	}
	if len(got.hits) != 0 {
		t.Errorf("readCandidate returned %d hits from a file that no longer exists", len(got.hits))
	}
	v.fold(got)
	if v.files != 1 {
		t.Errorf("scanned = %d after folding a vanished file, want 1: a covered skip stays scanned", v.files)
	}
	if v.truncated {
		t.Error("truncated = true after folding a vanished file: a covered skip is not a hole")
	}
}

// A batch admitted before the search's context died is never dispatched, and none of it may
// count as read: the deadline's reply is written, so its tally is what the reader sees.
func TestContentVisitor_UndispatchedBatchIsNotScanned(t *testing.T) {
	h, backing := searchHandlerAt(t, "/workspace")
	const files = 3 * searchWorkers
	for i := range files {
		writeTree(t, backing, map[string]string{fmt.Sprintf("f%02d.txt", i): "needle\n"})
	}
	dir := searchDirAt(t, h, "/workspace")

	ctx, cancel := context.WithCancel(t.Context())
	v := newContentVisitor(ctx, "needle", false)
	for i := range files {
		name := fmt.Sprintf("f%02d.txt", i)
		v.pending = append(v.pending, searchCandidate{name: name, abs: "/workspace/" + name})
	}
	cancel()
	v.flush(&walkDir{f: dir, abs: "/workspace"})

	res := v.result(false)
	if res.Scanned != 0 || len(res.Matches) != 0 {
		t.Errorf("scanned = %d rows = %d, want 0 and 0: no file of the batch was opened", res.Scanned, len(res.Matches))
	}
	if !res.Truncated {
		t.Error("truncated = false, want true: the batch's files went unread")
	}
}

// TestLogSearchReadError_ClassifiesLossVersusSkip pins the discriminator itself,
// at any privilege. One switch answers both questions — what to log and whether
// the answer is now partial — so the log level and the truncation flag cannot
// disagree about whether a file was lost.
func TestLogSearchReadError_ClassifiesLossVersusSkip(t *testing.T) {
	tests := map[string]struct {
		err      error
		wantLost bool
		why      string
	}{
		"permission wall": {
			err: syscall.EACCES, wantLost: true,
			why: "the search meant to read it and the kernel refused; nothing else reports that",
		},
		"io error": {
			err: syscall.EIO, wantLost: true,
			why: "the bytes exist and were not read",
		},
		"vanished file": {
			err: fs.ErrNotExist, wantLost: false,
			why: "ordinary on a tree the agent is writing to",
		},
		"not a regular file": {
			err: atomicfile.ErrNotRegular, wantLost: false,
			why: "a FIFO or device node was never in scope",
		},
		"oversize file": {
			err: atomicfile.ErrFileTooLarge, wantLost: true,
			why: "the ceiling is a read bound, not a skip: a file over it is read to the ceiling and " +
				"reported partial, so nothing may classify the sentinel as covered",
		},
		"symlink swapped in after admission": {
			err: syscall.ELOOP, wantLost: false,
			why: "the refusal IS the confinement guarantee",
		},
		"directory swapped in after admission": {
			err: syscall.ENOTDIR, wantLost: false,
			why: "same swap, other direction",
		},
		"request cancelled": {
			err: context.Canceled, wantLost: false,
			why: "the handler discards the body, so there is nothing to qualify",
		},
		"request deadline exceeded": {
			err: context.DeadlineExceeded, wantLost: false,
			why: "same as cancellation",
		},
		"wrapped permission wall": {
			err: errors.Join(errors.New("openat"), syscall.EACCES), wantLost: true,
			why: "the classification is by errors.Is, so a wrapped errno still counts",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := logSearchReadError("/mount/file", tc.err); got != tc.wantLost {
				t.Errorf("logSearchReadError(%v) lost = %v, want %v (%s)", tc.err, got, tc.wantLost, tc.why)
			}
		})
	}
}

// TestWalkDir_ReadDirFailureMarksTruncated asserts that the chunk in hand is consumed, but the rest of the
// directory was never listed, so the answer is truncated.
func TestWalkDir_ReadDirFailureMarksTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notadir.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	walk, v := contentWalk(t.Context(), "needle", false)
	if !walk.walk(&walkDir{f: f, abs: dir, pruneGit: true}) {
		t.Error("walk returned false (stop the whole scan) on one unenumerable directory; the other roots must still answer")
	}
	if !v.result(walk.truncated).Truncated {
		t.Error("truncated = false after a directory listing failed; the unread remainder is unreported")
	}
}

// TestWalkDir_EndOfDirectoryIsNotTruncation is the negative twin: EOF is how every
// successful enumeration ends, so it must never be read as a loss.
func TestWalkDir_EndOfDirectoryIsNotTruncation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	walk, v := contentWalk(t.Context(), "needle", false)
	if !walk.walk(&walkDir{f: f, abs: dir, pruneGit: true}) {
		t.Fatal("walk stopped the scan on an ordinary directory")
	}
	res := v.result(walk.truncated)
	if res.Truncated {
		t.Error("truncated = true after a directory was fully enumerated")
	}
	if len(res.Matches) != 1 {
		t.Errorf("matches = %d, want 1: the fixture's only file holds the needle", len(res.Matches))
	}
}

// TestSearch_MatchCapStopsTheWalk: a full reply stops the walk at the entry in hand, so `scanned`
// below the tree's file count proves the walk stopped rather than its tail being cut.
func TestSearch_MatchCapStopsTheWalk(t *testing.T) {
	h, dir, prefix := testDir(t)
	const files = maxSearchMatches + 100
	for i := range files {
		name := filepath.Join(dir, fmt.Sprintf("hit-%04d.txt", i))
		if err := os.WriteFile(name, []byte("needle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	res := decodeSearch(t, searchReq(t, h, map[string]string{"mode": "contents", "path": prefix, "q": "needle"}))

	if !res.Truncated {
		t.Error("truncated = false with more matching files than the reply holds")
	}
	if res.Scanned >= files {
		t.Errorf("scanned = %d with %d files in the tree; the walk was meant to stop, not to have its tail cut",
			res.Scanned, files)
	}
	if len(res.Matches) != maxSearchMatches {
		t.Errorf("matches = %d, want exactly the cap %d", len(res.Matches), maxSearchMatches)
	}
}
