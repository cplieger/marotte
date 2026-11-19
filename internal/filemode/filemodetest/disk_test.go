package filemodetest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/marotte/internal/filemode"
)

// seeder puts a file at a path on the disk under test, as a previous run would have left it.
type seeder func(t *testing.T, path string, data []byte, perm fs.FileMode)

// Each case runs on the host filesystem and on Disk, so Disk answers filemode's readers and writers as the host does.
func TestDisk_AnswersAsTheHostFilesystem(t *testing.T) {
	disks := map[string]func(t *testing.T) (filemode.Disk, string, seeder){
		"os": func(t *testing.T) (filemode.Disk, string, seeder) {
			return filemode.OS(), t.TempDir(), func(t *testing.T, path string, data []byte, perm fs.FileMode) {
				t.Helper()
				if err := writeHostFile(path, data, perm); err != nil {
					t.Fatalf("Setup: seed %s: %v", path, err)
				}
			}
		},
		"memory": func(*testing.T) (filemode.Disk, string, seeder) {
			d := NewDisk()
			return d, "/state", func(_ *testing.T, path string, data []byte, perm fs.FileMode) { d.WriteFile(path, data, perm) }
		},
	}
	for name, mk := range disks {
		t.Run(name, func(t *testing.T) {
			t.Run("a missing file reads as not existing", func(t *testing.T) {
				d, dir, _ := mk(t)
				if _, err := filemode.ReadPrivate(d, filepath.Join(dir, "f.json"), 1<<10); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("ReadPrivate = %v, want fs.ErrNotExist", err)
				}
			})
			t.Run("a write reads back private and replaces the last", func(t *testing.T) {
				d, dir, _ := mk(t)
				path := filepath.Join(dir, "f.json")
				for _, want := range []string{"first", "second"} {
					if err := filemode.WritePrivate(t.Context(), d, path, []byte(want)); err != nil {
						t.Fatalf("WritePrivate(%q) = %v", want, err)
					}
					got, err := filemode.ReadPrivate(d, path, 1<<10)
					if err != nil || string(got) != want {
						t.Errorf("ReadPrivate = %q, %v; want %q", got, err, want)
					}
					fi, err := d.Lstat(path)
					if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
						t.Errorf("Lstat = %v, %v; want a regular 0600 file", fi, err)
					}
				}
			})
			t.Run("a widened file is narrowed on read", func(t *testing.T) {
				d, dir, seed := mk(t)
				path := filepath.Join(dir, "f.json")
				seed(t, path, []byte("x"), 0o644)
				if got, err := filemode.ReadPrivate(d, path, 1<<10); err != nil || string(got) != "x" {
					t.Errorf("ReadPrivate = %q, %v; want the file narrowed and read", got, err)
				}
				if fi, err := d.Lstat(path); err != nil || fi.Mode().Perm() != 0o600 {
					t.Errorf("Lstat after the read = %v, %v; want 0600", fi, err)
				}
			})
			t.Run("a read past the bound is refused", func(t *testing.T) {
				d, dir, seed := mk(t)
				path := filepath.Join(dir, "f.json")
				seed(t, path, []byte("12345"), 0o600)
				if _, err := filemode.ReadPrivate(d, path, 4); !errors.Is(err, filemode.ErrTooLarge) {
					t.Errorf("ReadPrivate = %v, want ErrTooLarge", err)
				}
			})
			t.Run("a removal leaves nothing, a missing file included", func(t *testing.T) {
				d, dir, seed := mk(t)
				path := filepath.Join(dir, "f.json")
				seed(t, path, []byte("x"), 0o600)
				for range 2 {
					if err := filemode.RemoveDurably(d, path); err != nil {
						t.Errorf("RemoveDurably = %v", err)
					}
				}
				if _, err := d.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat after the removal = %v, want fs.ErrNotExist", err)
				}
			})
			t.Run("a discarded stage leaves the path as it was and no staged file", func(t *testing.T) {
				d, dir, seed := mk(t)
				path := filepath.Join(dir, "f.json")
				seed(t, path, []byte("old"), 0o600)
				st, err := d.Stage(t.Context(), path)
				if err != nil {
					t.Fatalf("Stage = %v", err)
				}
				if fi, err := d.Lstat(st.Name()); err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
					t.Errorf("the staged file = %v, %v; want an empty 0600 file", fi, err)
				}
				if _, err := st.Write([]byte("new")); err != nil {
					t.Fatalf("Write = %v", err)
				}
				if err := st.Discard(); err != nil {
					t.Fatalf("Discard = %v", err)
				}
				if _, err := d.Lstat(st.Name()); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Lstat of the discarded stage = %v, want fs.ErrNotExist", err)
				}
				if got, err := filemode.ReadPrivate(d, path, 1<<10); err != nil || string(got) != "old" {
					t.Errorf("ReadPrivate = %q, %v; want the old content", got, err)
				}
			})
			t.Run("a cancelled write changes nothing", func(t *testing.T) {
				d, dir, seed := mk(t)
				path := filepath.Join(dir, "f.json")
				seed(t, path, []byte("old"), 0o600)
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := filemode.WritePrivate(ctx, d, path, []byte("new")); !errors.Is(err, context.Canceled) {
					t.Errorf("WritePrivate = %v, want context.Canceled", err)
				}
				if got, err := filemode.ReadPrivate(d, path, 1<<10); err != nil || string(got) != "old" {
					t.Errorf("ReadPrivate = %q, %v; want the old content", got, err)
				}
			})
		})
	}
}

// writeHostFile writes path with exactly perm, whatever the umask.
func writeHostFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}
