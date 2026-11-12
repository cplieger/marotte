package bot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// botsDir is KiroHome's directory holding one directory per Bot.
const botsDir = "bots"

const memoryName = "memory.md"

// memoryStartingText is a new memory file's text (design §7.2 "Starting text").
const memoryStartingText = "# Memory\n\nYour notes for future turns. Keep this file under 40,000 characters.\n"

var errLinked = errors.New("not a directory or regular file of its own (a link or another kind of file)")

// Dir is the Bot id's own directory, <KiroHome>/bots/<id>. False when id is not one folder name (ids.ValidChatID) or
// KiroHome is not absolute.
func Dir(id string, paths Paths) (string, bool) {
	if !ids.ValidChatID(id) || !filepath.IsAbs(paths.KiroHome) {
		return "", false
	}
	return filepath.Join(paths.KiroHome, botsDir, id), true
}

// EnsureMemory makes the Bot rec's directory (0700) and memory file (0600, the starting text) where either is missing,
// and keeps what exists. KiroHome must exist; a link at bots/, the Bot's directory or the file is refused and nothing
// is written through it.
func EnsureMemory(rec *marotte.Chat, paths Paths) error {
	if _, ok := Dir(rec.ID, paths); !ok {
		return fmt.Errorf("bot %q: no directory under an absolute KIRO_HOME", rec.ID)
	}
	root, err := os.OpenRoot(paths.KiroHome)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir := filepath.Join(botsDir, rec.ID)
	for _, d := range []string{botsDir, dir} {
		if err := ownDir(root, d); err != nil {
			return err
		}
	}
	return createMemory(root, filepath.Join(dir, memoryName))
}

// ownDir makes name in root at 0700 unless it exists, then requires it to be a directory and no link: os.Root follows
// a link that stays in root, which would point the Bot's files at another KiroHome tree.
func ownDir(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %w", name, errLinked)
	}
	return nil
}

// createMemory writes the starting text to a new file at name. O_EXCL refuses an existing name without following a
// link there, so an existing regular file is kept and anything else is refused.
func createMemory(root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: %w", name, errLinked)
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(memoryStartingText)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// A partial file would be kept as the Bot's notes by the next open.
		_ = root.Remove(name)
	}
	return err
}
