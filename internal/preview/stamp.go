package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	stampMaxEntries = 2000
	stampMaxDepth   = 8
	// stampMaxScanned bounds the names read, dot entries and skipped kinds
	// included, so a directory of ignored names cannot make the walk unbounded.
	stampMaxScanned = 4 * stampMaxEntries
	stampBatch      = 256
)

// stampWalk digests a folder's tree for live reload: every non-dot entry's
// relative path, size and mtime, at most stampMaxEntries entries and
// stampMaxDepth levels deep. node_modules is skipped and symlinks are never
// followed, both because a demo's own files are what reload should track.
type stampWalk struct {
	h         hash.Hash
	entries   int
	scanned   int
	truncated bool
}

func folderStamp(dirfd int) (stamp string, entries int, truncated bool, err error) {
	w, err := walkStamp(dirfd)
	if err != nil {
		return "", 0, false, err
	}
	return hex.EncodeToString(w.h.Sum(nil)[:16]), w.entries, w.truncated, nil
}

func walkStamp(dirfd int) (*stampWalk, error) {
	w := &stampWalk{h: sha256.New()}
	if err := w.dir(dirfd, "", 1); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *stampWalk) dir(dirfd int, prefix string, depth int) error {
	names, complete, err := w.readNames(dirfd, prefix)
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		if err := w.entry(dirfd, prefix, name, depth); err != nil {
			return err
		}
		if w.truncated {
			return nil
		}
	}
	if !complete {
		w.truncated = true
	}
	return nil
}

// readNames reads the non-dot names of a directory in batches, stopping once it
// holds one more than the remaining entry budget (the witness that truncates)
// or the scan budget is spent. complete reports that the directory was read to
// its end, so a sorted digest of a directory under the cap is independent of
// readdir order.
func (w *stampWalk) readNames(dirfd int, prefix string) (names []string, complete bool, err error) {
	fd, err := unix.Dup(dirfd)
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), prefix)
	defer func() { _ = f.Close() }()
	want := stampMaxEntries - w.entries + 1
	for len(names) < want && w.scanned < stampMaxScanned {
		batch, err := f.Readdirnames(min(stampBatch, stampMaxScanned-w.scanned))
		w.scanned += len(batch)
		for _, name := range batch {
			if !strings.HasPrefix(name, ".") {
				names = append(names, name)
			}
		}
		if errors.Is(err, io.EOF) {
			return names, true, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	return names, false, nil
}

func (w *stampWalk) entry(dirfd int, prefix, name string, depth int) error {
	var st unix.Stat_t
	if unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return nil
	}
	kind := st.Mode & unix.S_IFMT
	if kind == unix.S_IFLNK || (kind == unix.S_IFDIR && name == "node_modules") {
		return nil
	}
	if w.entries == stampMaxEntries {
		w.truncated = true
		return nil
	}
	w.entries++
	rel := prefix + name
	if kind != unix.S_IFDIR {
		w.h.Write([]byte(rel + "\x00" + strconv.FormatInt(st.Size, 10) + "\x00" +
			strconv.FormatInt(st.Mtim.Nano(), 10) + "\n"))
		return nil
	}
	// A directory contributes its name only: its mtime also moves when an
	// ignored child changes (a dot entry, node_modules, a file past the depth
	// bound), which must not reload the preview.
	w.h.Write([]byte(rel + "/\n"))
	if depth >= stampMaxDepth {
		return nil
	}
	sub, err := openBeneath(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil
	}
	defer func() { _ = unix.Close(sub) }()
	return w.dir(sub, rel+"/", depth+1)
}
