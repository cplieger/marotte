package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/kirosession"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/workspace"
)

// editSnapshotName is the file names KAS 2.37.1 writes for a workflowPrompt edit (its workflowWriteEditSnapshot).
var editSnapshotName = regexp.MustCompile(`^(edit-snapshot-r\d+\.md|edit-replaced-r\d+\.json)$`)

// confineRead is confine for a read by sessionID, which also admits the edit snapshot files in the directory of the
// run sessionID is a step of. KAS names that path to the run's creator step and reads it through the client's fs.
func (in *inbound) confineRead(ctx context.Context, sessionID marotte.SessionID, p string) (fsTarget, error) {
	target, err := in.lifetime.confine(ctx, p, fsRead)
	if err == nil {
		return target, nil
	}
	home, dir, ok := in.snapshotDir(sessionID, p)
	if !ok {
		return fsTarget{}, err
	}
	root, openErr := os.OpenRoot(dir)
	if openErr != nil {
		return fsTarget{}, err
	}
	name := filepath.Base(p)
	if !linkFreeBelow(root, home, dir) || !regularOrAbsent(root, name) {
		_ = root.Close()
		return fsTarget{}, err
	}
	return fsTarget{root: root, rel: name, opened: true}, nil
}

// linkFreeBelow reports that root, opened at dir, is the directory dir names with no symlink between home and it.
// os.OpenRoot follows links in the name it is given, so a linked run or workflows directory would hand this run's
// steps another run's snapshot. Links in home itself are the operator's and are followed.
func linkFreeBelow(root *os.Root, home, dir string) bool {
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil {
		return false
	}
	want := filepath.Join(realHome, rel)
	if got, evalErr := filepath.EvalSymlinks(dir); evalErr != nil || got != want {
		return false
	}
	opened, err := root.Stat(".")
	if err != nil {
		return false
	}
	named, err := os.Lstat(want)
	return err == nil && os.SameFile(opened, named)
}

// regularOrAbsent reports that name in root is no link or other non-regular file. The read and the stat follow a link
// that stays in root, so a snapshot name linking to the run's state file would expose a file the grant does not cover.
// An absent name passes, so its read answers not found.
func regularOrAbsent(root *os.Root, name string) bool {
	info, err := root.Lstat(name)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return info.Mode().IsRegular()
}

// snapshotDir reports KIRO_HOME and the run directory p names an edit snapshot file in, when that run is
// sessionID's.
func (in *inbound) snapshotDir(sessionID marotte.SessionID, p string) (home, dir string, ok bool) {
	if !editSnapshotName.MatchString(filepath.Base(p)) {
		return "", "", false
	}
	wf, ok := in.stepRun(sessionID)
	if !ok || wf == "" || !ids.ValidIdent(wf) {
		return "", "", false
	}
	home, err := workspace.KiroHomeDir()
	if err != nil {
		return "", "", false
	}
	sessions := filepath.Join(home, "sessions")
	// KAS keeps the run's directory in the bucket of all its roots, which its step session records.
	roots, ok := kirosession.WorkspacePaths(sessions, string(sessionID))
	if !ok {
		roots = []string{in.lifetime.workDir}
	}
	dir = filepath.Join(sessions, workspace.SessionsBucket(roots...), "workflows", wf)
	return home, dir, filepath.Dir(p) == dir
}
