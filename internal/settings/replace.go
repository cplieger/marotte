package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ownedfile"
)

// ErrRefused marks a Replace refusal of the content itself: not one JSON object of at most the
// size cap, or a value CheckPatch refuses. The error's text is the refusal's own.
var ErrRefused = errors.New("settings: content refused")

type refusedError struct{ error }

func (e refusedError) Is(target error) bool { return target == ErrRefused }
func (e refusedError) Unwrap() error        { return e.error }

// CheckPatch refuses a patch a settings write may not commit: each key's own value check and
// the additional directories judged against stored, the document it replaces.
func CheckPatch(dirs AgentDirs, stored, patch map[string]json.RawMessage) error {
	for _, check := range []func(map[string]json.RawMessage) error{
		validateAgentIgnorePatch, validateCompactionPatch, validateAgentPatch, validateTriggerCapsPatch,
	} {
		if err := check(patch); err != nil {
			return err
		}
	}
	return dirs.ValidatePatch(stored, patch)
}

// validateAgentIgnorePatch refuses an agent_ignore_files kiro-cli would refuse, or config.json
// would disagree with what is enforced. A malformed value is refused too, since a read takes it
// as the default.
func validateAgentIgnorePatch(patch map[string]json.RawMessage) error {
	raw, touched := patch[KeyAgentIgnoreFiles]
	if !touched {
		return nil
	}
	var entries []string
	if json.Unmarshal(raw, &entries) != nil {
		return fmt.Errorf("%s must be a list of ignore file names", KeyAgentIgnoreFiles)
	}
	for _, entry := range entries {
		if err := validAgentIgnoreEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// Replace writes content as the whole config.json under Update's lock. check, when not nil, sees
// the whole file on disk first (nil when absent or not a regular file) and its error is returned
// as is; a file there whose bytes cannot be read refuses the write with ErrUnreadable. Every
// non-stamp key whose value content changes runs CheckPatch against the stored document, or an
// empty one when that is refused, so a broken file can be repaired. The stamp keys are the
// server's: the next version is stamped whatever content holds. publish gets the write's event
// before the lock is released. Answers the bytes written and the keys whose value changed or
// went, nil when the stored document was refused, so any key may have moved.
func Replace(ctx context.Context, dirs AgentDirs, content []byte, check func(current []byte) error,
	publish func(context.Context, marotte.ServerEvent),
) (written []byte, changed []string, err error) {
	if dirs.Config == "" {
		return nil, nil, errors.New("settings: no config dir")
	}
	want, err := parseObject(content)
	if err != nil {
		return nil, nil, refusedError{err}
	}
	delete(want, KeySettingsEpoch)
	delete(want, KeySettingsRevision)
	path, err := filepath.Abs(filepath.Join(dirs.Config, filename))
	if err != nil {
		return nil, nil, err
	}
	c := getCache(dirs.Config)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	s, readErr := c.storedBytes(path, ownedfile.Reading)
	if checkErr := checkWhole(path, &s, check); checkErr != nil {
		return nil, nil, checkErr
	}
	stored := s.doc
	if readErr != nil {
		stored = map[string]json.RawMessage{}
	}
	keys := changedKeys(stored, want)
	if readErr == nil {
		changed = keys
	}
	patch := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := want[k]; ok {
			patch[k] = v
		}
	}
	_ = WarnUnknownKeys(slices.Collect(maps.Keys(want)), "editor save of config.json")
	if refusal := CheckPatch(dirs, stored, patch); refusal != nil {
		return nil, nil, refusedError{refusal}
	}
	next, ok := c.next(stampOf(stored), time.Now())
	if !ok {
		return nil, nil, errNoLaterStamp
	}
	written, err = c.writeLocked(ctx, path, want, next)
	if err != nil {
		return nil, nil, err
	}
	c.fold(s.base, written)
	if publish != nil {
		publish(ctx, next.event())
	}
	return written, changed, nil
}

// checkWhole runs check, when not nil, on the whole file at path, re-read when s's capped read
// stopped at the cap, and refuses with ErrUnreadable when that read could not see the bytes.
func checkWhole(path string, s *storedRead, check func([]byte) error) error {
	if check == nil {
		return nil
	}
	if err := ownedfile.Unseen(s.read, s.disk, s.diskErr); err != nil {
		return fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	disk := s.disk
	if len(disk) > maxBytes {
		_, whole, _, err := ownedfile.Classify(path, ownedfile.Options{}, func([]byte) error { return nil })
		if err != nil {
			return err
		}
		disk = whole
	}
	return check(disk)
}
