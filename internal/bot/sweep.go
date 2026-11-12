package bot

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// ErrIncompleteScan is SweepDirs refusing to remove anything: a record the chat scan could not read may own a
// directory.
var ErrIncompleteScan = errors.New("the chat scan was incomplete, so no Bot directory was removed")

// Records is one chat scan: every chat id whose record was read, and whether every record was.
type Records struct {
	Read     map[marotte.ChatID]struct{}
	Complete bool
}

// SweepDirs removes each Bot directory under KiroHome whose chat has no record, a delete cut between the record and
// the directory (design §8.6), and returns the removed ids. It lists bots/ before calling records: a Bot's record is
// written before its directory, so a directory made after the listing is not judged. Only a real directory named by a
// chat id is a Bot's; anything else under bots/ is left alone. With no Bot directory to judge, records is not called.
func SweepDirs(ctx context.Context, paths Paths, records func(context.Context) Records) ([]string, error) {
	if !filepath.IsAbs(paths.KiroHome) {
		return nil, fmt.Errorf("KIRO_HOME %q is not absolute", paths.KiroHome)
	}
	root, err := os.OpenRoot(paths.KiroHome)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(botsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, fmt.Errorf("%s: %w", botsDir, errLinked)
	}
	listed, err := botDirNames(root)
	if err != nil || len(listed) == 0 {
		return nil, err
	}
	recs := records(ctx)
	if !recs.Complete {
		return nil, ErrIncompleteScan
	}
	var removed []string
	var errs []error
	for _, id := range listed {
		if _, ok := recs.Read[marotte.ChatID(id)]; ok {
			continue
		}
		if err := root.RemoveAll(filepath.Join(botsDir, id)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}

// botDirNames lists bots/'s entries that are directories, not links, named by a chat id, sorted.
func botDirNames(root *os.Root) ([]string, error) {
	f, err := root.Open(botsDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && ids.ValidChatID(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}
