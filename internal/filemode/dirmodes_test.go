package filemode

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// mkdirAt creates dir at exactly mode, past the umask.
func mkdirAt(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode() & (fs.ModePerm | fs.ModeSetgid)
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func tighten(paths ...string) []DirMode {
	dirs := make([]DirMode, 0, len(paths))
	for _, p := range paths {
		dirs = append(dirs, DirMode{Path: p, Want: NoGroupOrOtherWrite})
	}
	return dirs
}

func failedAt(results []DirModeResult) []string {
	var out []string
	for _, r := range results {
		if r.Err != nil {
			out = append(out, r.Path)
		}
	}
	return out
}

func TestSetDirModes_SetsEachFolderAndReportsTheChange(t *testing.T) {
	root := t.TempDir()
	top, child, deep := filepath.Join(root, "top"), filepath.Join(root, "top", "child"), filepath.Join(root, "top", "child", "deep")
	mkdirAt(t, deep, 0o770)
	mkdirAt(t, child, 0o775)
	mkdirAt(t, top, 0o777)
	ssh := filepath.Join(top, ".ssh")
	mkdirAt(t, ssh, 0o755)

	dirs := append(tighten(deep, child, top+"/"), DirMode{Path: ssh, Want: Exactly(0o700)})
	got := SetDirModes(dirs)

	for path, want := range map[string]fs.FileMode{top: 0o755, child: 0o755, deep: 0o750, ssh: 0o700} {
		if m := modeOf(t, path); m != want {
			t.Errorf("%s mode = %04o, want %04o", path, m, want)
		}
	}
	want := []DirModeResult{
		{Path: top, Old: 0o777, New: 0o755},
		{Path: child, Old: 0o775, New: 0o755},
		{Path: ssh, Old: 0o755, New: 0o700},
		{Path: deep, Old: 0o770, New: 0o750},
	}
	if !slices.Equal(got, want) {
		t.Errorf("results = %+v, want %+v", got, want)
	}
	if again := SetDirModes(dirs); len(again) != 0 {
		t.Errorf("a second pass over set folders reported %+v, want nothing", again)
	}
}

func TestSetDirModes_KeepsSpecialBits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	mkdirAt(t, dir, 0o775|fs.ModeSetgid)
	if modeOf(t, dir)&fs.ModeSetgid == 0 {
		t.Skip("the filesystem dropped setgid on a directory")
	}
	SetDirModes([]DirMode{{Path: dir, Want: Exactly(0o750)}})
	if m := modeOf(t, dir); m != 0o750|fs.ModeSetgid {
		t.Errorf("mode = %v, want setgid kept with 0750", m)
	}
}

// Every symlink below the named folder is a place the walk stops, at any depth, and an outside
// folder it points at keeps its mode.
func TestSetDirModes_NeverFollowsASymlink(t *testing.T) {
	cases := map[string]struct {
		// plant builds the tree under root and returns the listed folders, the outside folder a
		// symlink points at, and the path the walk must report stopping at.
		plant func(t *testing.T, root, outside string) (listed []string, stop string)
	}{
		"the named folder itself": {plant: func(t *testing.T, root, outside string) ([]string, string) {
			top := filepath.Join(root, "top")
			symlink(t, outside, top)
			return []string{top, filepath.Join(top, "sub")}, top
		}},
		"a listed folder below it": {plant: func(t *testing.T, root, outside string) ([]string, string) {
			top := filepath.Join(root, "top")
			mkdirAt(t, top, 0o755)
			symlink(t, outside, filepath.Join(top, "npm"))
			return []string{top, filepath.Join(top, "npm"), filepath.Join(top, "npm", "sub")}, filepath.Join(top, "npm")
		}},
		"an unlisted folder on the way": {plant: func(t *testing.T, root, outside string) ([]string, string) {
			top := filepath.Join(root, "top")
			mkdirAt(t, top, 0o755)
			symlink(t, filepath.Dir(outside), filepath.Join(top, "x"))
			return []string{top, filepath.Join(top, "x", filepath.Base(outside))}, filepath.Join(top, "x")
		}},
		"below a trailing slash": {plant: func(t *testing.T, root, outside string) ([]string, string) {
			top := filepath.Join(root, "top")
			mkdirAt(t, top, 0o755)
			symlink(t, outside, filepath.Join(top, ".kiro"))
			return []string{top + "/", filepath.Join(top, ".kiro")}, filepath.Join(top, ".kiro")
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "outside")
			mkdirAt(t, filepath.Join(outside, "sub"), 0o777)
			mkdirAt(t, outside, 0o777)
			root := filepath.Join(base, "root")
			mkdirAt(t, root, 0o755)
			listed, stop := tc.plant(t, root, outside)

			got := SetDirModes(tighten(listed...))

			if m := modeOf(t, outside); m != 0o777 {
				t.Errorf("outside folder mode = %04o, want 0777 kept", m)
			}
			if m := modeOf(t, filepath.Join(outside, "sub")); m != 0o777 {
				t.Errorf("outside sub mode = %04o, want 0777 kept", m)
			}
			if f := failedAt(got); !slices.Equal(f, []string{stop}) {
				t.Errorf("failures at %v, want one at %s", f, stop)
			}
			for _, r := range got {
				if r.Err != nil && (!errors.Is(r.Err, ErrNotADir) || !r.Stopped) {
					t.Errorf("failure at %s = %+v, want a stopping ErrNotADir", r.Path, r)
				}
			}
		})
	}
}

func TestSetDirModes_GoesOnPastAStop(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	mkdirAt(t, outside, 0o777)
	top := filepath.Join(root, "top")
	mkdirAt(t, filepath.Join(top, "python"), 0o775)
	mkdirAt(t, top, 0o775)
	symlink(t, outside, filepath.Join(top, "npm"))
	if err := os.WriteFile(filepath.Join(top, "opt"), nil, 0o666); err != nil {
		t.Fatal(err)
	}

	got := SetDirModes(tighten(top, filepath.Join(top, "npm"), filepath.Join(top, "opt"), filepath.Join(top, "python")))

	if m := modeOf(t, filepath.Join(top, "python")); m != 0o755 {
		t.Errorf("python mode = %04o, want 0755", m)
	}
	if m := modeOf(t, top); m != 0o755 {
		t.Errorf("top mode = %04o, want 0755", m)
	}
	if f := failedAt(got); !slices.Equal(f, []string{filepath.Join(top, "npm"), filepath.Join(top, "opt")}) {
		t.Errorf("failures at %v, want npm and opt", f)
	}
}

func TestSetDirModes_SkipsMissingFoldersSilently(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent")
	got := SetDirModes(tighten(missing, filepath.Join(missing, "bin"), filepath.Join(root, "no", "parent")))
	if len(got) != 0 {
		t.Errorf("results = %+v, want none", got)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat of the missing folder = %v, want it still missing", err)
	}
}

// The parent of a folder beneath no listed one is the operator's layout, so a symlink there is
// followed.
func TestSetDirModes_FollowsTheParentOfANamedFolder(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "mnt")
	mkdirAt(t, filepath.Join(real, "config"), 0o775)
	symlink(t, real, filepath.Join(root, "srv"))

	SetDirModes(tighten(filepath.Join(root, "srv", "config")))

	if m := modeOf(t, filepath.Join(real, "config")); m != 0o755 {
		t.Errorf("config mode = %04o, want 0755 through the symlinked parent", m)
	}
}

// A folder named through the kernel's path of a listed one is reached from that folder's
// descriptor, so a symlink between them is not followed.
func TestSetDirModes_WalksFromTheKernelPathOfAListedFolder(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "mnt", "config")
	mkdirAt(t, real, 0o755)
	symlink(t, filepath.Join(root, "mnt"), filepath.Join(root, "srv"))
	outside := filepath.Join(root, "outside")
	mkdirAt(t, filepath.Join(outside, ".kiro"), 0o777)
	symlink(t, outside, filepath.Join(real, "x"))

	got := SetDirModes(tighten(filepath.Join(root, "srv", "config"), filepath.Join(real, "x", ".kiro")))

	if m := modeOf(t, filepath.Join(outside, ".kiro")); m != 0o777 {
		t.Errorf("outside .kiro mode = %04o, want 0777 kept", m)
	}
	if f := failedAt(got); !slices.Equal(f, []string{filepath.Join(real, "x")}) {
		t.Errorf("failures at %v, want one at %s", f, filepath.Join(real, "x"))
	}
}

// A listed folder named through a symlinked path longer than its kernel path is still the base of
// a folder named through that kernel path, in either listing order.
func TestSetDirModes_WalksFromAFolderNamedDeeperThanItsKernelPath(t *testing.T) {
	for name, configFirst := range map[string]bool{"config listed first": true, "home listed first": false} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			real := filepath.Join(root, "m", "config")
			mkdirAt(t, real, 0o755)
			mkdirAt(t, filepath.Join(root, "a", "b", "c"), 0o755)
			symlink(t, filepath.Join(root, "m"), filepath.Join(root, "a", "b", "c", "d"))
			outside := filepath.Join(root, "outside")
			mkdirAt(t, filepath.Join(outside, "home"), 0o700)
			symlink(t, outside, filepath.Join(real, "x"))
			dirs := []DirMode{
				{Path: filepath.Join(root, "a", "b", "c", "d", "config"), Want: NoGroupOrOtherWrite},
				{Path: filepath.Join(real, "x", "home"), Want: Exactly(0o755)},
			}
			if !configFirst {
				slices.Reverse(dirs)
			}

			got := SetDirModes(dirs)

			if m := modeOf(t, filepath.Join(outside, "home")); m != 0o700 {
				t.Errorf("outside home mode = %04o, want 0700 kept", m)
			}
			if f := failedAt(got); !slices.Equal(f, []string{filepath.Join(real, "x")}) {
				t.Errorf("failures at %v, want one at %s", f, filepath.Join(real, "x"))
			}
			for _, r := range got {
				if r.Err != nil && (!errors.Is(r.Err, ErrNotADir) || !r.Stopped) {
					t.Errorf("failure at %s = %+v, want a stopping ErrNotADir", r.Path, r)
				}
			}
		})
	}
}

// A folder named through the kernel path of a listed folder the walk then refuses is skipped with
// it: that path is inside the refused folder's tree, which a rename can repoint.
func TestSetDirModes_SkipsAFolderBeneathARefusedOnesKernelPath(t *testing.T) {
	for name, viaSymlink := range map[string]bool{"config named by its real path": false, "config named through a symlink": true} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			real := filepath.Join(root, "m", "config")
			mkdirAt(t, real, 0o755)
			symlink(t, filepath.Join(root, "m"), filepath.Join(root, "srv"))
			outside := filepath.Join(root, "outside")
			mkdirAt(t, filepath.Join(outside, "home", ".kiro"), 0o777)
			symlink(t, outside, filepath.Join(real, "x"))
			config := real
			if viaSymlink {
				config = filepath.Join(root, "srv", "config")
			}

			got := SetDirModes(tighten(config, filepath.Join(real, "x", "home"), filepath.Join(outside, "home", ".kiro")))

			if m := modeOf(t, filepath.Join(outside, "home", ".kiro")); m != 0o777 {
				t.Errorf("outside .kiro mode = %04o, want 0777 kept", m)
			}
			if f := failedAt(got); !slices.Equal(f, []string{filepath.Join(real, "x")}) {
				t.Errorf("failures at %v, want one at %s", f, filepath.Join(real, "x"))
			}
		})
	}
}

// Two folders each named through the other's kernel path end the walk: the one reached first is
// set, and the symlink on the way to the other is where it stops.
func TestSetDirModes_EndsOnFoldersNamedThroughEachOther(t *testing.T) {
	root := t.TempDir()
	r, q := filepath.Join(root, "A", "r"), filepath.Join(root, "B", "q")
	mkdirAt(t, r, 0o775)
	mkdirAt(t, q, 0o777)
	symlink(t, filepath.Join(root, "A"), filepath.Join(q, "back"))
	symlink(t, filepath.Join(root, "B"), filepath.Join(r, "x"))

	got := SetDirModes(tighten(filepath.Join(r, "x", "q"), filepath.Join(q, "back", "r")))

	if m := modeOf(t, r); m != 0o755 {
		t.Errorf("r mode = %04o, want 0755", m)
	}
	if m := modeOf(t, q); m != 0o777 {
		t.Errorf("q mode = %04o, want 0777 kept behind the symlink below r", m)
	}
	if f := failedAt(got); !slices.Equal(f, []string{filepath.Join(r, "x")}) {
		t.Errorf("failures at %v, want one at %s", f, filepath.Join(r, "x"))
	}
}

// A rename after the open cannot redirect the change: it lands on the folder that was opened.
func TestSetDirModes_ActsOnTheOpenedFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "kiro-cli-versions")
	mkdirAt(t, filepath.Join(dir, "sub"), 0o775)
	mkdirAt(t, dir, 0o775)
	outside := filepath.Join(root, "outside")
	mkdirAt(t, filepath.Join(outside, "sub"), 0o777)
	mkdirAt(t, outside, 0o777)
	moved := filepath.Join(root, "moved")
	swap := func(p string) {
		if p != dir {
			return
		}
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		symlink(t, outside, dir)
	}
	testHookOpened = swap
	t.Cleanup(func() { testHookOpened = func(string) {} })

	SetDirModes(tighten(dir, filepath.Join(dir, "sub")))

	for _, p := range []string{outside, filepath.Join(outside, "sub")} {
		if m := modeOf(t, p); m != 0o777 {
			t.Errorf("%s mode = %04o, want 0777 kept", p, m)
		}
	}
	for _, p := range []string{moved, filepath.Join(moved, "sub")} {
		if m := modeOf(t, p); m != 0o755 {
			t.Errorf("%s mode = %04o, want 0755 on the opened folder", p, m)
		}
	}
}

func TestSetDirModes_ReportsAFailedChmodAndGoesOn(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	mkdirAt(t, a, 0o775)
	mkdirAt(t, b, 0o775)
	var calls int
	chmodFD = func(fd int, mode uint32) error {
		calls++
		if calls == 1 {
			return syscall.EROFS
		}
		return changeMode(fd, mode)
	}
	t.Cleanup(func() { chmodFD = changeMode })

	got := SetDirModes(tighten(a, b))

	want := []DirModeResult{{Path: a, Old: 0o775, Err: syscall.EROFS}, {Path: b, Old: 0o775, New: 0o755}}
	if len(got) != 2 || got[0].Path != want[0].Path || got[0].Old != want[0].Old || !errors.Is(got[0].Err, syscall.EROFS) || got[1] != want[1] {
		t.Errorf("results = %+v, want %+v", got, want)
	}
	if m := modeOf(t, a); m != 0o775 {
		t.Errorf("a mode = %04o, want 0775 after the refused chmod", m)
	}
}

// A refused mode change leaves only that folder's mode: the walk goes on through its descriptor, so a
// listed folder below it is still set.
func TestSetDirModes_AFailedChmodLeavesOnlyThatFolder(t *testing.T) {
	root := t.TempDir()
	parent, child := filepath.Join(root, "tools"), filepath.Join(root, "tools", "bin")
	mkdirAt(t, child, 0o775)
	mkdirAt(t, parent, 0o775)
	chmodFD = func(fd int, mode uint32) error {
		if k, err := kernelPath(fd); err == nil && k == parent {
			return syscall.EROFS
		}
		return changeMode(fd, mode)
	}
	t.Cleanup(func() { chmodFD = changeMode })

	got := SetDirModes(tighten(parent, child))

	if len(got) != 2 || got[0].Path != parent || !errors.Is(got[0].Err, syscall.EROFS) || got[0].Stopped {
		t.Fatalf("results = %+v, want a non-stopping EROFS at %s first", got, parent)
	}
	if want := (DirModeResult{Path: child, Old: 0o775, New: 0o755}); got[1] != want {
		t.Errorf("child result = %+v, want %+v", got[1], want)
	}
	if m := modeOf(t, child); m != 0o755 {
		t.Errorf("child mode = %04o, want 0755 below the refused parent", m)
	}
}

func TestSetDirModes_RefusesARelativePath(t *testing.T) {
	got := SetDirModes(tighten("relative/dir"))
	if f := failedAt(got); !slices.Equal(f, []string{"relative/dir"}) {
		t.Errorf("failures at %v, want the relative path", f)
	}
}

// unprivilegedDirEnv names the folder a test re-run by unprivilegedDir works in.
const unprivilegedDirEnv = "FILEMODE_UNPRIVILEGED_DIR"

// unprivilegedDir returns a folder the calling top-level test owns as an account that cannot open
// a folder it lacks read permission on. As root it re-runs that test as uid 65534 and returns ""
// once the re-run passed, so the caller returns.
func unprivilegedDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv(unprivilegedDirEnv); dir != "" {
		return dir
	}
	if os.Getuid() != 0 {
		return t.TempDir()
	}
	base := t.TempDir()
	for _, d := range []string{filepath.Dir(base), base} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(base, "filemode.test")
	copyExecutable(t, bin)
	work := filepath.Join(base, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(work, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GOCOVERDIR=")
	}), unprivilegedDirEnv+"="+work)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("re-run as uid 65534: %v\n%s", err, out)
	}
	return ""
}

// copyExecutable copies the test binary to dst, world-executable; ForkLock keeps a concurrent fork
// from holding it open for writing when it runs (ETXTBSY, golang/go#22315).
func copyExecutable(t *testing.T, dst string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// restoreOnCleanup makes each folder listable again before the temp folder is removed.
func restoreOnCleanup(t *testing.T, dirs ...string) {
	t.Cleanup(func() {
		for _, d := range dirs {
			_ = os.Chmod(d, 0o755)
		}
	})
}

// An owner may change the mode of a folder it can enter but not list, and of one inside a folder it
// cannot list; symlinks there are still refused.
func TestSetDirModes_SetsFoldersTheOwnerCannotRead(t *testing.T) {
	work := unprivilegedDir(t)
	if work == "" {
		return
	}
	outside := filepath.Join(work, "outside")
	mkdirAt(t, outside, 0o777)
	tools := filepath.Join(work, "tools")
	mkdirAt(t, filepath.Join(tools, "bin"), 0o775)
	symlink(t, outside, filepath.Join(tools, "npm"))
	mkdirAt(t, tools, 0o330)
	locked := filepath.Join(work, "locked")
	home := filepath.Join(locked, "home")
	mkdirAt(t, home, 0o300)
	mkdirAt(t, locked, 0o100)
	link := filepath.Join(work, "link")
	symlink(t, outside, link)
	restoreOnCleanup(t, tools, locked, home)

	got := SetDirModes([]DirMode{
		{Path: tools, Want: NoGroupOrOtherWrite},
		{Path: filepath.Join(tools, "bin"), Want: NoGroupOrOtherWrite},
		{Path: filepath.Join(tools, "npm"), Want: NoGroupOrOtherWrite},
		{Path: home, Want: Exactly(0o755)},
		{Path: link, Want: NoGroupOrOtherWrite},
	})

	for path, want := range map[string]fs.FileMode{
		tools: 0o310, filepath.Join(tools, "bin"): 0o755, home: 0o755, locked: 0o100, outside: 0o777,
	} {
		if m := modeOf(t, path); m != want {
			t.Errorf("%s mode = %04o, want %04o", path, m, want)
		}
	}
	if f := failedAt(got); !slices.Equal(f, []string{link, filepath.Join(tools, "npm")}) {
		t.Errorf("failures at %v, want the two symlinks", f)
	}
	for _, r := range got {
		if r.Err != nil && !errors.Is(r.Err, ErrNotADir) {
			t.Errorf("failure at %s = %v, want ErrNotADir", r.Path, r.Err)
		}
	}
}

// Where fchmodat2 is missing or refused, the mode still changes, on the folder that was opened.
func TestSetDirModes_FallsBackFromARefusedFchmodat2(t *testing.T) {
	for _, refusal := range []error{unix.EOPNOTSUPP, unix.EPERM} {
		t.Run(refusal.Error(), func(t *testing.T) {
			orig := chmodEmptyPath
			chmodEmptyPath = func(int, uint32) error { return refusal }
			t.Cleanup(func() {
				chmodEmptyPath = orig
				testHookOpened = func(string) {}
			})
			root := t.TempDir()
			dir, moved, outside := filepath.Join(root, "dir"), filepath.Join(root, "moved"), filepath.Join(root, "outside")
			mkdirAt(t, dir, 0o775)
			mkdirAt(t, outside, 0o777)
			testHookOpened = func(string) {
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				symlink(t, outside, dir)
			}

			got := SetDirModes(tighten(dir))

			if want := []DirModeResult{{Path: dir, Old: 0o775, New: 0o755}}; !slices.Equal(got, want) {
				t.Errorf("results = %+v, want %+v", got, want)
			}
			if m := modeOf(t, moved); m != 0o755 {
				t.Errorf("opened folder mode = %04o, want 0755", m)
			}
			if m := modeOf(t, outside); m != 0o777 {
				t.Errorf("outside folder mode = %04o, want 0777 kept", m)
			}
		})
	}
}

// A refusal fchmodat2 does not fall back from is the folder's failure.
func TestSetDirModes_ReportsAnotherFchmodat2Refusal(t *testing.T) {
	orig := chmodEmptyPath
	chmodEmptyPath = func(int, uint32) error { return unix.EROFS }
	t.Cleanup(func() { chmodEmptyPath = orig })
	dir := filepath.Join(t.TempDir(), "dir")
	mkdirAt(t, dir, 0o775)

	got := SetDirModes(tighten(dir))

	if len(got) != 1 || !errors.Is(got[0].Err, unix.EROFS) {
		t.Errorf("results = %+v, want one EROFS", got)
	}
	if m := modeOf(t, dir); m != 0o775 {
		t.Errorf("mode = %04o, want 0775 kept", m)
	}
}
