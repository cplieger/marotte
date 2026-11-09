package ownedfile

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/filesafe"
)

func TestGate_Observe(t *testing.T) {
	valid := Read{Kind: Valid, Key: "v"}
	absent := Read{Kind: Absent}
	broken := Read{Kind: Unreadable, Key: "a"}
	other := Read{Kind: Unreadable, Key: "b"}

	type step struct {
		reset bool
		r     Read
		want  bool
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{name: "a valid read acts at once", steps: []step{{r: valid, want: true}}},
		{name: "a valid read ends a hold", steps: []step{
			{r: absent}, {r: valid, want: true}, {r: absent},
		}},
		{name: "absent then absent acts at the second", steps: []step{
			{r: absent}, {r: absent, want: true},
		}},
		{name: "absent, unreadable, absent does not act at the third", steps: []step{
			{r: absent}, {r: broken}, {r: absent},
		}},
		{name: "a reset ends the hold", steps: []step{
			{r: absent}, {reset: true}, {r: absent}, {r: absent, want: true},
		}},
		{name: "two unreadable reads with different keys do not act", steps: []step{
			{r: broken}, {r: other}, {r: other, want: true},
		}},
		{name: "after acting the same read acts again", steps: []step{
			{r: broken}, {r: broken, want: true}, {r: broken, want: true},
		}},
		{name: "a reset after acting holds the same read again", steps: []step{
			{r: absent}, {r: absent, want: true}, {reset: true}, {r: absent},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g Gate
			for i, s := range tt.steps {
				if s.reset {
					g.Reset()
					continue
				}
				if got := g.Observe(s.r); got != s.want {
					t.Fatalf("step %d: Observe(%v) = %v, want %v", i, s.r, got, s.want)
				}
			}
		})
	}
}

func TestReadingAndMerging(t *testing.T) {
	valid := Read{Kind: Valid, Key: "v"}
	validB := Read{Kind: Valid, Key: "w"}
	absent := Read{Kind: Absent}
	broken := Read{Kind: Unreadable, Key: "a"}
	brokenB := Read{Kind: Unreadable, Key: "b"}

	tests := []struct {
		name         string
		r, applied   Read
		have         bool
		reading, mer Source
	}{
		{name: "valid, equal to applied", r: valid, applied: valid, have: true, reading: Disk, mer: Disk},
		{name: "valid, nothing applied", r: valid, reading: Disk, mer: Disk},
		{name: "valid, over applied valid", r: validB, applied: valid, have: true, reading: Disk, mer: Disk},
		{name: "valid, over applied absent", r: valid, applied: absent, have: true, reading: Disk, mer: Disk},
		{name: "valid, over applied unreadable", r: valid, applied: broken, have: true, reading: Disk, mer: Disk},

		{name: "absent, equal to applied", r: absent, applied: absent, have: true, reading: Seed, mer: Seed},
		{name: "absent, nothing applied", r: absent, reading: Seed, mer: Seed},
		{name: "absent, held over applied valid", r: absent, applied: valid, have: true, reading: Applied, mer: Applied},
		{name: "absent, held over applied unreadable", r: absent, applied: broken, have: true, reading: Refused, mer: Refused},

		{name: "unreadable, equal to applied", r: broken, applied: broken, have: true, reading: Refused, mer: Refused},
		{name: "unreadable, nothing applied", r: broken, reading: Refused, mer: Refused},
		{name: "unreadable, held over applied valid", r: broken, applied: valid, have: true, reading: Applied, mer: Refused},
		{name: "unreadable, held over applied absent", r: broken, applied: absent, have: true, reading: Seed, mer: Refused},
		{name: "unreadable, held over applied unreadable", r: brokenB, applied: broken, have: true, reading: Refused, mer: Refused},

		// have false wins over an applied value the caller left in place.
		{name: "absent, applied ignored without have", r: absent, applied: valid, reading: Seed, mer: Seed},
		{name: "unreadable, applied ignored without have", r: broken, applied: valid, reading: Refused, mer: Refused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Reading(tt.r, tt.applied, tt.have); got != tt.reading {
				t.Errorf("Reading(%v, %v, %v) = %v, want %v", tt.r, tt.applied, tt.have, got, tt.reading)
			}
			if got := Merging(tt.r, tt.applied, tt.have); got != tt.mer {
				t.Errorf("Merging(%v, %v, %v) = %v, want %v", tt.r, tt.applied, tt.have, got, tt.mer)
			}
		})
	}
}

func acceptAll([]byte) error { return nil }

func classifyWithin(t *testing.T, path string, o Options) (Read, []byte, fs.FileInfo, error) {
	t.Helper()
	type result struct {
		r    Read
		data []byte
		info fs.FileInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, data, info, err := Classify(path, o, acceptAll)
		done <- result{r, data, info, err}
	}()
	select {
	case res := <-done:
		return res.r, res.data, res.info, res.err
	case <-time.After(time.Second):
		t.Fatalf("Classify(%s, %+v) has not returned after 1s", path, o)
		return Read{}, nil, nil, nil
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	modes := []struct {
		name string
		o    Options
	}{
		{name: "no follow", o: Options{}},
		{name: "follow", o: Options{FollowFinalSymlink: true}},
	}

	t.Run("an owner's own write is the read Classify answers for it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f")
		content := []byte(`{"theme":"dark"}`)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		r, _, _, _ := Classify(path, Options{}, func([]byte) error { return nil })
		if w := Written(content); r != w {
			t.Errorf("Classify = %v, Written = %v, want equal", r, w)
		}
		if Written(content) == Written([]byte(`{"theme":"lite"}`)) {
			t.Error("Written answers one read for two byte sets")
		}
	})

	t.Run("a FIFO with no writer is unreadable and left closed", func(t *testing.T) {
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "f")
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				r, data, info, err := classifyWithin(t, path, m.o)
				if r.Kind != Unreadable || data != nil || !errors.Is(err, filesafe.ErrNotRegular) {
					t.Errorf("Classify = %v, %q, %v, want Unreadable, nil data, ErrNotRegular", r, data, err)
				}
				if info == nil || info.Mode()&fs.ModeNamedPipe == 0 {
					t.Errorf("info = %v, want the FIFO's", info)
				}
				// A non-blocking write open of a FIFO fails with ENXIO only while no reader holds it.
				w, werr := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if werr == nil {
					_ = w.Close()
				}
				if !errors.Is(werr, syscall.ENXIO) {
					t.Errorf("a write open after Classify = %v, want ENXIO (no reader left open)", werr)
				}
			})
		}
	})

	t.Run("a directory is unreadable keyed by mode and identity", func(t *testing.T) {
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				dir := t.TempDir()
				a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
				for _, p := range []string{a, b} {
					if err := os.Mkdir(p, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				r1, data, info, err := classifyWithin(t, a, m.o)
				if r1.Kind != Unreadable || data != nil || info == nil || !info.IsDir() || !errors.Is(err, filesafe.ErrNotRegular) {
					t.Fatalf("Classify(dir) = %v, %q, %v, %v", r1, data, info, err)
				}
				if !strings.Contains(r1.Key, info.Mode().String()) {
					t.Errorf("key %q does not name the mode %v", r1.Key, info.Mode())
				}
				r2, _, _, _ := classifyWithin(t, a, m.o)
				if r1 != r2 {
					t.Errorf("two reads of one directory = %v and %v, want equal", r1, r2)
				}
				rb, _, _, _ := classifyWithin(t, b, m.o)
				if rb == r1 {
					t.Errorf("two directories share the key %q", rb.Key)
				}
				if err := os.Chmod(a, 0o750); err != nil {
					t.Fatal(err)
				}
				if r3, _, _, _ := classifyWithin(t, a, m.o); r3 == r1 {
					t.Errorf("a mode change kept the key %q", r3.Key)
				}
			})
		}
	})

	t.Run("a socket is unreadable and not regular", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "s")
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Skipf("unix socket: %v", err)
		}
		t.Cleanup(func() { _ = l.Close() })
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				r, data, info, err := classifyWithin(t, path, m.o)
				if r.Kind != Unreadable || data != nil || !errors.Is(err, filesafe.ErrNotRegular) {
					t.Errorf("Classify(socket) = %v, %q, %v, want Unreadable, nil, ErrNotRegular", r, data, err)
				}
				if info == nil || info.Mode()&fs.ModeSocket == 0 {
					t.Errorf("info = %v, want the socket's", info)
				}
			})
		}
	})

	t.Run("a final symlink is read only with FollowFinalSymlink", func(t *testing.T) {
		dir := t.TempDir()
		target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
		writeFile(t, target, `{"a":1}`)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		r, data, info, err := classifyWithin(t, link, Options{FollowFinalSymlink: true})
		if r.Kind != Valid || string(data) != `{"a":1}` || err != nil || info == nil || !info.Mode().IsRegular() {
			t.Errorf("follow: Classify(link) = %v, %q, %v, %v, want Valid with the target's bytes", r, data, info, err)
		}
		r, data, _, err = classifyWithin(t, link, Options{})
		if r.Kind != Unreadable || data != nil || !errors.Is(err, filesafe.ErrSymlinkTarget) {
			t.Errorf("no follow: Classify(link) = %v, %q, %v, want Unreadable, ErrSymlinkTarget", r, data, err)
		}
	})

	t.Run("a dangling symlink is absent only with FollowFinalSymlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(filepath.Join(filepath.Dir(link), "gone"), link); err != nil {
			t.Fatal(err)
		}
		if r, _, _, err := classifyWithin(t, link, Options{FollowFinalSymlink: true}); r != (Read{Kind: Absent}) || err != nil {
			t.Errorf("follow: Classify(dangling) = %v, %v, want Absent", r, err)
		}
		if r, _, _, _ := classifyWithin(t, link, Options{}); r.Kind != Unreadable {
			t.Errorf("no follow: Classify(dangling) = %v, want Unreadable", r)
		}
	})

	t.Run("a file over Limit is unreadable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f")
		writeFile(t, path, "12345")
		r, data, _, err := classifyWithin(t, path, Options{Limit: 4})
		if r.Kind != Unreadable || !errors.Is(err, filesafe.ErrFileTooLarge) || string(data) != "12345" {
			t.Errorf("one byte over: Classify = %v, %q, %v, want Unreadable with the bytes read, ErrFileTooLarge", r, data, err)
		}
		if r, data, _, err := classifyWithin(t, path, Options{Limit: 5}); r.Kind != Valid || string(data) != "12345" || err != nil {
			t.Errorf("at Limit: Classify = %v, %q, %v, want Valid", r, data, err)
		}
		if r, data, _, err := classifyWithin(t, path, Options{}); r.Kind != Valid || string(data) != "12345" || err != nil {
			t.Errorf("Limit 0: Classify = %v, %q, %v, want Valid with the whole file", r, data, err)
		}
	})

	t.Run("accept's error is unreadable with the bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f")
		writeFile(t, path, "{")
		bad := errors.New("not an object")
		r, data, info, err := Classify(path, Options{}, func([]byte) error { return bad })
		if r.Kind != Unreadable || string(data) != "{" || !errors.Is(err, bad) || info == nil {
			t.Errorf("Classify = %v, %q, %v, %v, want Unreadable with the bytes and accept's error", r, data, info, err)
		}
		v, _, _, _ := Classify(path, Options{}, acceptAll)
		if v.Kind != Valid || v.Key != r.Key {
			t.Errorf("the same bytes accepted = %v, want Valid under the refused read's key %q", v, r.Key)
		}
	})

	t.Run("a missing path is absent with no key", func(t *testing.T) {
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				r, data, info, err := classifyWithin(t, filepath.Join(t.TempDir(), "missing"), m.o)
				if r != (Read{Kind: Absent}) || data != nil || info != nil || err != nil {
					t.Errorf("Classify(missing) = %v, %q, %v, %v, want Absent and nothing else", r, data, info, err)
				}
			})
		}
	})

	t.Run("a stat error is unreadable keyed by its text", func(t *testing.T) {
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				file := filepath.Join(t.TempDir(), "file")
				writeFile(t, file, "x")
				path := filepath.Join(file, "child")
				r, data, info, err := classifyWithin(t, path, m.o)
				if r.Kind != Unreadable || data != nil || info != nil || !errors.Is(err, syscall.ENOTDIR) {
					t.Errorf("Classify(file/child) = %v, %q, %v, %v, want Unreadable, ENOTDIR", r, data, info, err)
				}
				if !strings.Contains(r.Key, err.Error()) {
					t.Errorf("key %q does not carry the error %q", r.Key, err)
				}
			})
		}
	})

	t.Run("keys follow the bytes", func(t *testing.T) {
		dir := t.TempDir()
		a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
		writeFile(t, a, `{"theme":"dark"}`)
		writeFile(t, b, `{"theme":"dark"}`)
		writeFile(t, c, `{"theme":"dark!`)
		ra, _, _, _ := Classify(a, Options{}, acceptAll)
		rb, _, _, _ := Classify(b, Options{}, acceptAll)
		rc, _, _, _ := Classify(c, Options{}, acceptAll)
		if ra != rb || ra.Key == "" {
			t.Errorf("equal bytes = %v and %v, want one non-empty key", ra, rb)
		}
		if rc == ra {
			t.Errorf("a one-byte change kept the key %q", rc.Key)
		}
	})

	t.Run("an empty regular file has non-nil bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "f")
		writeFile(t, path, "")
		r, data, _, err := Classify(path, Options{Limit: 10}, acceptAll)
		if r.Kind != Valid || data == nil || len(data) != 0 || err != nil {
			t.Errorf("Classify(empty) = %v, %#v, %v, want Valid with empty non-nil bytes", r, data, err)
		}
	})
}
