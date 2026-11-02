package signin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
)

const (
	fileName     = "signin.json"
	maxFileBytes = 1 << 20
	retryMax     = 60 * time.Second
	// stopFlushBudget bounds the last save at shutdown.
	stopFlushBudget = 5 * time.Second
)

// User text for a file Open could not use. The file is kept either way.
const (
	textNotPrivate = "The saved sign-in is not a private file, so marotte did not use it. Sign in again to replace it."
	textUnreadable = "The saved sign-in could not be read. Sign in again to replace it."
)

var errStagedCopyLeft = errors.New("a staged copy of an earlier save is still on disk")

const msgStagedCopyLeft = "sign-in: a staged copy of an earlier save is still on disk, so the change was not saved; " +
	"sign-in works again once the cleanup retry or a manual removal deletes it"

// fileShape is signin.json. Lapsed is the vault's own mark: the backend's grant was rejected.
type fileShape struct {
	Backends map[BackendID]fileEntry `json:"backends"`
}

type fileEntry struct {
	Data   json.RawMessage `json:"data"`
	Lapsed bool            `json:"lapsed,omitempty"`
}

func entryOf(r *record) fileEntry { return fileEntry{Data: r.cred.Data, Lapsed: r.lapsed} }

func (v *Vault) load() {
	data, err := filemode.ReadPrivate(v.disk, v.path, maxFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if npe, ok := errors.AsType[*filemode.NotPrivateError](err); ok {
		slog.Error("sign-in: the saved sign-in is not private, so it was not used", "path", v.path, "mode", npe.Mode.String(), "error", err)
		v.markUnavailable(textNotPrivate)
		return
	}
	if err != nil {
		slog.Error("sign-in: the saved sign-in could not be read", "path", v.path, "error", err)
		v.markUnavailable(textUnreadable)
		return
	}
	var f fileShape
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Error("sign-in: the saved sign-in is malformed", "path", v.path, "error", err)
		v.markUnavailable(textUnreadable)
		return
	}
	for id, e := range f.Backends {
		s := v.slots[id]
		if s == nil {
			slog.Warn("sign-in: the saved sign-in names a backend marotte does not have; the next save drops it", "backend", id)
			continue
		}
		c := Credential{Data: e.Data}
		if err := s.b.Validate(c); err != nil {
			slog.Error("sign-in: a saved sign-in is not usable", "backend", id, "error", err)
			s.unavailable = textUnreadable
			continue
		}
		r := &record{cred: c, gen: v.gens.Add(1), lapsed: e.Lapsed}
		s.desired, s.effective = r, r
	}
}

func (v *Vault) markUnavailable(reason string) {
	for _, s := range v.slots {
		s.unavailable = reason
	}
}

// job is a sign-in or sign-out handed to the persistence worker, which alone saves it and
// publishes its outcome.
type job struct {
	s *slot
	// rec is the sign-in's record, nil for a sign-out.
	rec *record
	// verdict receives a sign-out's outcome. It is buffered, so the worker never waits on a caller
	// that stopped waiting.
	verdict chan error
	attempt uint64
}

func (j *job) answer(err error) {
	if j.verdict != nil {
		j.verdict <- err
	}
}

// submit queues j for the worker; it never waits for the worker or the filesystem.
func (v *Vault) submit(j *job) {
	v.queueMu.Lock()
	v.queue = append(v.queue, j)
	v.queueMu.Unlock()
	v.nudge()
}

func (v *Vault) take() *job {
	v.queueMu.Lock()
	defer v.queueMu.Unlock()
	if len(v.queue) == 0 {
		return nil
	}
	j := v.queue[0]
	v.queue[0] = nil
	v.queue = v.queue[1:]
	return j
}

func (v *Vault) nudge() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// snapshot is every backend's desired record, an entry of over (nil for a sign-out) replacing it.
func (v *Vault) snapshot(over map[BackendID]*record) map[BackendID]*record {
	snap := make(map[BackendID]*record, len(v.slots))
	for sid, s := range v.slots {
		if r, ok := over[sid]; ok {
			snap[sid] = r
			continue
		}
		s.mu.Lock()
		snap[sid] = s.desired
		s.mu.Unlock()
	}
	return snap
}

// persist is the vault's only writer of the file, so no two writes overlap and the leftovers need
// no lock. It saves each submitted sign-in and sign-out in order and publishes the outcome the
// file then holds, and writes the desired map while a backend is dirty, retrying from retryBase,
// doubling to retryMax. Each write serializes the map as it is then, so an older retry can never
// overwrite a newer sign-in.
func (v *Vault) persist() {
	defer close(v.stopped)
	retry := time.NewTimer(0)
	retry.Stop()
	defer retry.Stop()
	var delay time.Duration
	failing := false
	for {
		select {
		case <-v.lifetime.Done():
			v.flushOnStop()
			return
		case <-v.wake:
		case <-retry.C:
		}
		// The wake coalesces, so whatever woke the worker, the state decides: clean, or a retry armed.
		err := v.pass()
		if len(v.leftovers) == 0 && !v.anyDirty() {
			if failing {
				slog.Info("sign-in: the saved sign-in is current again", "path", v.path)
			}
			failing, delay = false, 0
			continue
		}
		if err != nil && !failing && v.lifetime.Err() == nil {
			slog.Error("sign-in: the updated sign-in could not be saved; retrying", "path", v.path, "error", err)
			failing = true
		}
		delay = min(max(2*delay, v.retryBase), retryMax)
		retry.Reset(delay)
	}
}

// pass applies the queued sign-ins and sign-outs in order, then saves the dirty map unless one of
// them wrote: a job's write already carries every backend's desired record. err is the last write's.
func (v *Vault) pass() error {
	wrote := false
	var err error
	for v.lifetime.Err() == nil {
		j := v.take()
		if j == nil {
			break
		}
		if w, jErr := v.apply(j); w {
			wrote, err = true, jErr
		}
	}
	if wrote || v.lifetime.Err() != nil {
		return err
	}
	return v.saveDirty(v.lifetime, false)
}

// apply saves j and publishes the outcome the file then holds: the change when the file holds it,
// the last published state when it does not. A write that may not be durable is settled by reading
// the file back before the next job. wrote is false when j wrote nothing: a sign-in cancelled
// before its turn, a sign-out with no record, or a write refused while a staged copy is left.
func (v *Vault) apply(j *job) (wrote bool, err error) {
	s, id := j.s, j.s.decl.Backend
	s.mu.Lock()
	idle := s.desired == nil
	if j.rec != nil {
		idle = !s.currentLocked(j.attempt)
	}
	s.mu.Unlock()
	if idle {
		j.answer(nil)
		return false, nil
	}
	snap := v.snapshot(map[BackendID]*record{id: j.rec})
	if lo := v.stagedCopyLeft(); lo != nil && stages(snap) {
		slog.Error(msgStagedCopyLeft, "backend", id, "path", lo.Name())
		err = fmt.Errorf("%w: %s", errStagedCopyLeft, lo.Name())
		publishFailure(j, err)
		return false, err
	}
	err = v.write(v.lifetime, snap)
	landed, unsynced := err == nil, errors.Is(err, filemode.ErrNotDurable)
	if unsynced {
		// The file may not keep what it holds now, so the worker writes desired until a write is.
		markDirty(s)
		holds, known := v.readBack(id, j.rec)
		if !known {
			slog.Error("sign-in: the sign-in change could not be confirmed before shutdown", "backend", id, "error", err)
			j.answer(fmt.Errorf("%w: %w", ErrInDoubt, err))
			return true, err
		}
		landed = holds
	}
	if !landed {
		publishFailure(j, err)
		return true, err
	}
	v.settle(snap, id)
	v.publishChange(j, unsynced)
	return true, err
}

func markDirty(s *slot) {
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
}

// publishChange makes j visible once the file holds it. unsynced leaves the backend dirty, so the
// worker writes the map again until a write is durable.
func (v *Vault) publishChange(j *job, unsynced bool) {
	s, id := j.s, j.s.decl.Backend
	s.mu.Lock()
	from := s.kindLocked()
	if j.rec == nil {
		s.desired, s.effective, s.unreachable = nil, nil, false
	} else {
		s.endAttemptLocked(j.attempt, AttemptSignedIn, "")
		if s.basis == nil || s.b.LiveSwitchable(s.basis.cred, j.rec.cred) {
			s.desired, s.effective, s.unreachable = j.rec, j.rec, false
		} else {
			s.desired = j.rec
		}
	}
	s.settleLocked(j.rec)
	s.dirty = s.dirty || unsynced
	c := Change{Backend: id, From: from, To: s.kindLocked(), AccountPending: s.accountPendingLocked()}
	t := v.ticket()
	at, a := v.noteAnswerLocked(s)
	s.mu.Unlock()
	if j.rec == nil {
		slog.Info("sign-in: signed out", "backend", id)
	} else {
		slog.Info("sign-in: signed in", "backend", id, "account_pending", c.AccountPending)
	}
	v.deliver(t, c)
	v.deliverAnswer(at, id, a)
	j.answer(nil)
}

func publishFailure(j *job, err error) {
	s, id := j.s, j.s.decl.Backend
	if j.rec == nil {
		slog.Error("sign-in: the saved sign-in could not be removed", "backend", id, "error", err)
		j.answer(fmt.Errorf("%w: %w", errNotSaved, err))
		return
	}
	s.mu.Lock()
	s.endAttemptLocked(j.attempt, AttemptFailed, textSaveFailed)
	s.mu.Unlock()
	slog.Error("sign-in: the sign-in could not be saved", "backend", id, "error", err)
}

// readBack reads the file after a write that may not be durable and reports whether it holds r for
// id. A read the filesystem fails is retried from retryBase; known is false when the lifetime ended
// first.
func (v *Vault) readBack(id BackendID, r *record) (holds, known bool) {
	var delay time.Duration
	for {
		holds, err := v.fileHolds(id, r)
		if err == nil {
			return holds, true
		}
		if delay == 0 {
			slog.Error("sign-in: the saved sign-in could not be read back; retrying", "path", v.path, "error", err)
		}
		delay = min(max(2*delay, v.retryBase), retryMax)
		t := time.NewTimer(delay)
		select {
		case <-v.lifetime.Done():
			t.Stop()
			return false, false
		case <-t.C:
		}
	}
}

// fileHolds reports whether the file holds r for id (no entry for a nil r). A file a restart would
// refuse (not private, too large, malformed) holds nothing; only the filesystem's own error is
// returned.
func (v *Vault) fileHolds(id BackendID, r *record) (bool, error) {
	data, err := filemode.ReadPrivate(v.disk, v.path, maxFileBytes)
	var f fileShape
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, filemode.ErrNotPrivate), errors.Is(err, filemode.ErrTooLarge):
		return false, nil
	case err != nil:
		return false, err
	case json.Unmarshal(data, &f) != nil:
		return false, nil
	}
	e, ok := f.Backends[id]
	if r == nil || !ok {
		return r == nil && !ok, nil
	}
	// Both sides go through the encoder write used, which compacts and escapes Data.
	got, gErr := json.Marshal(e)
	want, wErr := json.Marshal(entryOf(r))
	return gErr == nil && wErr == nil && bytes.Equal(got, want), nil
}

// write makes the file hold snap, durably: written owner-only and proved private, or removed when
// snap holds no record. An error matching filemode.ErrNotDurable means the file holds snap but may
// not survive a power loss; any other error means the file still holds what it held.
func (v *Vault) write(ctx context.Context, snap map[BackendID]*record) error {
	f := fileShape{Backends: map[BackendID]fileEntry{}}
	for id, r := range snap {
		if r != nil {
			f.Backends[id] = entryOf(r)
		}
	}
	if len(f.Backends) == 0 {
		return filemode.RemoveDurably(v.disk, v.path)
	}
	return v.writeData(ctx, f)
}

func (v *Vault) writeData(ctx context.Context, f fileShape) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	err = filemode.WritePrivate(ctx, v.disk, v.path, data)
	if npe, ok := errors.AsType[*filemode.NotPrivateError](err); ok {
		attrs := []any{"path", v.path, "error", err}
		if npe.Mode != 0 {
			attrs = append(attrs, "mode", npe.Mode.String())
		}
		slog.Error("sign-in: the credential file could not be made private, so it was not written", attrs...)
	}
	if lo, ok := errors.AsType[*filemode.LeftoverError](err); ok {
		slog.Error("sign-in: a staged copy of the sign-in could not be removed; retrying", "path", lo.Name(), "error", lo.Err)
		v.leftovers = append(v.leftovers, lo)
	}
	return err
}

// stagedCopyLeft retries the removal of every staged file a failed write left and returns one
// still on disk, or nil. While one is, only a removal or the last attempt at shutdown may write:
// each failed write can leave another copy of the credential.
func (v *Vault) stagedCopyLeft() *filemode.LeftoverError {
	kept := v.leftovers[:0]
	for _, lo := range v.leftovers {
		if lo.Discard() != nil {
			kept = append(kept, lo)
			continue
		}
		slog.Info("sign-in: the staged copy of the sign-in was removed", "path", lo.Name())
	}
	v.leftovers = kept
	if len(kept) == 0 {
		return nil
	}
	return kept[0]
}

// stages reports whether writing snap stages a file, rather than removing the file.
func stages(snap map[BackendID]*record) bool {
	for _, r := range snap {
		if r != nil {
			return true
		}
	}
	return false
}

// settle follows a write that landed, settling every backend of snap but skip, which the caller
// settles under its own lock.
func (v *Vault) settle(snap map[BackendID]*record, skip BackendID) {
	for id, r := range snap {
		if id == skip {
			continue
		}
		s := v.slots[id]
		s.mu.Lock()
		s.settleLocked(r)
		s.mu.Unlock()
	}
}

// settleLocked clears dirty when desired is still r, the record the file now holds, so a newer one
// installed meanwhile is written again. It clears the load failure, whose file is gone. The caller
// holds s.mu.
func (s *slot) settleLocked(r *record) {
	if s.desired == r {
		s.dirty = false
	}
	s.unavailable = ""
}

func (v *Vault) anyDirty() bool {
	for _, s := range v.slots {
		s.mu.Lock()
		d := s.dirty
		s.mu.Unlock()
		if d {
			return true
		}
	}
	return false
}

// flushOnStop makes one last attempt at a dirty map when the lifetime ends, so a renewal that
// rotated the refresh token just before shutdown is not lost to the next start.
func (v *Vault) flushOnStop() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(v.lifetime), stopFlushBudget)
	defer cancel()
	if err := v.saveDirty(ctx, true); err != nil {
		slog.Error("sign-in: the updated sign-in could not be saved before shutdown", "path", v.path, "error", err)
	}
	if n := len(v.leftovers); n > 0 {
		slog.Error("sign-in: a staged copy of the sign-in is still on disk at shutdown", "path", v.path, "count", n)
	}
}

// saveDirty writes the desired map when any backend is dirty, under stagedCopyLeft's rule. The
// last attempt at shutdown is the exception that adds at most one copy rather than losing a
// rotated refresh token.
func (v *Vault) saveDirty(ctx context.Context, last bool) error {
	left := v.stagedCopyLeft()
	if !v.anyDirty() {
		return nil
	}
	snap := v.snapshot(nil)
	if !last && left != nil && stages(snap) {
		return nil
	}
	err := v.write(ctx, snap)
	if err == nil {
		v.settle(snap, "")
	}
	return err
}
