package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/cplieger/pathinside/v2"
)

// errAdditionalDirectory is the class of every refusal AgentDirs.checkEntry returns; each wraps it
// with the reason the user reads beside the field.
var errAdditionalDirectory = errors.New("invalid additional directory")

// AgentDirs names the two trees that decide which additional directories agents may reach:
// Workspace, which they reach already, and Config, marotte's config directory, which holds its
// secrets and which no additional directory may equal, sit inside or contain.
type AgentDirs struct {
	Workspace string
	Config    string
}

// checkEntry refuses an entry that is not an absolute, clean path to an existing directory reached
// through no symbolic link, or that equals, sits inside or contains the workspace or the config
// directory. Requiring the canonical path is what keeps a symlink from redirecting a granted
// root onto a protected tree, and lets confinement compare names lexically.
func (d AgentDirs) checkEntry(entry string) error {
	switch {
	case entry == "":
		return fmt.Errorf("%w: an entry cannot be empty", errAdditionalDirectory)
	case !filepath.IsAbs(entry):
		return fmt.Errorf("%w %q: give an absolute path", errAdditionalDirectory, entry)
	case filepath.Clean(entry) != entry:
		return fmt.Errorf("%w %q: write it as %q", errAdditionalDirectory, entry, filepath.Clean(entry))
	case entry == string(filepath.Separator):
		return fmt.Errorf("%w %q: the filesystem root holds marotte's config directory", errAdditionalDirectory, entry)
	}
	if err := d.checkOverlap(entry); err != nil {
		return err
	}
	info, err := os.Stat(entry)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w %q: no such directory", errAdditionalDirectory, entry)
	case err != nil:
		return fmt.Errorf("%w %q: %w", errAdditionalDirectory, entry, err)
	case !info.IsDir():
		return fmt.Errorf("%w %q: not a directory", errAdditionalDirectory, entry)
	}
	resolved, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return fmt.Errorf("%w %q: %w", errAdditionalDirectory, entry, err)
	}
	if resolved != entry {
		return fmt.Errorf("%w %q: it is reached through a symbolic link; add %q instead", errAdditionalDirectory, entry, resolved)
	}
	return nil
}

// checkOverlap compares entry with both protected trees as written and as resolved, so a
// protected tree reached through a symlink is still recognised.
func (d AgentDirs) checkOverlap(entry string) error {
	for _, p := range []struct {
		dir, name string
	}{
		{d.Config, "marotte's config directory, which holds its secrets"},
		{d.Workspace, "the workspace, which agents reach already"},
	} {
		for _, dir := range spellings(p.dir) {
			switch {
			case pathinside.Root(dir).Contains(entry):
				return fmt.Errorf("%w %q: it is or lies inside %s", errAdditionalDirectory, entry, p.name)
			case pathinside.Root(entry).Contains(dir):
				return fmt.Errorf("%w %q: it contains %s", errAdditionalDirectory, entry, p.name)
			}
		}
	}
	return nil
}

// spellings is dir as written and as resolved; an empty dir protects nothing.
func spellings(dir string) []string {
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	out := []string{clean}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
		out = append(out, resolved)
	}
	return out
}

// Assess splits entries into the ones granted today, in order and deduplicated, and the reason
// for each other one, keyed by the entry. An entry is granted when checkEntry accepts it and it
// neither lies inside nor contains an entry granted before it, so granted entries never
// overlap. refused is never nil. A stored entry is assessed again on every read: config.json is
// hand-editable, and a directory can change after it was added.
func (d AgentDirs) Assess(entries []string) (granted []string, refused map[string]string) {
	refused = map[string]string{}
	for _, entry := range entries {
		if slices.Contains(granted, entry) {
			continue
		}
		if err := d.checkEntry(entry); err != nil {
			refused[entry] = err.Error()
			continue
		}
		if err := overlapping(entry, granted); err != nil {
			refused[entry] = err.Error()
			continue
		}
		granted = append(granted, entry)
	}
	return granted, refused
}

func overlapping(entry string, granted []string) error {
	for _, g := range granted {
		switch {
		case pathinside.Root(g).Contains(entry):
			return fmt.Errorf("%w %q: it lies inside %q, which is already granted", errAdditionalDirectory, entry, g)
		case pathinside.Root(entry).Contains(g):
			return fmt.Errorf("%w %q: it contains %q, which is already granted", errAdditionalDirectory, entry, g)
		}
	}
	return nil
}

// Granted is Assess over the list stored in config.json now. An unreadable config.json grants
// nothing and returns the read error.
func (d AgentDirs) Granted(ctx context.Context) (granted []string, refused map[string]string, err error) {
	entries, _, err := FieldStrict[[]string](ctx, d.Config, KeyAdditionalDirectories)
	if err != nil {
		return nil, nil, err
	}
	granted, refused = d.Assess(entries)
	return granted, refused, nil
}

// ValidatePatch judges patch against stored, the document it is merged into. A patch without
// the key passes. Otherwise its additional_directories must be a list of strings naming no
// entry twice, and Assess over that list may refuse only entries it already refuses over the
// stored list; the first other refusal is returned with Assess's reason. So a write never
// adds a refused entry or takes the grant from a kept one, and one refused today never blocks
// removing or adding another. Run it under settings.Update's lock, so stored is what the patch
// replaces.
func (d AgentDirs) ValidatePatch(stored, patch map[string]json.RawMessage) error {
	raw, ok := patch[KeyAdditionalDirectories]
	if !ok {
		return nil
	}
	var entries []string
	if decodeInto(&entries, raw) != nil {
		return fmt.Errorf("%s must be a list of directory paths", KeyAdditionalDirectories)
	}
	for i, entry := range entries {
		if slices.Contains(entries[:i], entry) {
			return fmt.Errorf("%w %q: it is listed twice", errAdditionalDirectory, entry)
		}
	}
	var before []string
	if storedRaw, ok := stored[KeyAdditionalDirectories]; ok && decodeInto(&before, storedRaw) != nil {
		before = nil
	}
	_, refusedBefore := d.Assess(before)
	_, refused := d.Assess(entries)
	for _, entry := range entries {
		reason, isRefused := refused[entry]
		if _, already := refusedBefore[entry]; isRefused && !already {
			return errors.New(reason)
		}
	}
	return nil
}
