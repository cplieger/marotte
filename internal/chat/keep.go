package chat

// Keep facts: every reason a chat must not be purged is written onto its own chat.json under the record lock, by the
// subsystem where it happens, and leaves only on evidence that its thing ended (Release), by the claim that wrote it
// (Rollback, EndStop), or with the record. Every writer of marotte.Chat.Keep is here; startKeep is the one a create
// calls.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

// ErrInTeardown refuses a write for a chat whose purge teardown still runs, whatever the tombstone TTL says. It wraps
// ErrTombstoned, so every caller that refuses a tombstoned record refuses it too.
var ErrInTeardown = fmt.Errorf("%w: its teardown is still running", ErrTombstoned)

var errNoEvidence = errors.New("chat: a keep fact is released only on evidence that its thing ended")

// KeepScope says whether a fact outlives the process that wrote it.
type KeepScope int

const (
	// KeepDurable facts stay until their evidence end.
	KeepDurable KeepScope = iota
	// keepProcess facts die with the boot that wrote them.
	keepProcess
)

// keepPrior is what a hold found under its key before it wrote.
type keepPrior int

const (
	// PriorAbsent means the key was not on the record.
	PriorAbsent keepPrior = iota
	// PriorThisBoot means a fact this boot wrote was there.
	PriorThisBoot
	// priorForeignBoot means a fact another boot wrote was there.
	priorForeignBoot
	// priorUnknown means the record could not be read, so the hold was pended (HoldOrPend).
	priorUnknown
)

// Claim is one hold's receipt, the only input Rollback takes.
type Claim struct {
	ChatID    marotte.ChatID
	Key       string
	Token     string
	PriorFact marotte.KeepFact
	Prior     keepPrior
}

// Evidence names why a fact's thing ended. It has no value for absence, so no absence path can release a fact.
type Evidence int

const (
	evidenceNone Evidence = iota
	// EvidenceTabClosed is a tab close the tab store committed, owed releases included.
	EvidenceTabClosed
	// EvidenceRunCancelled is a stopped run's cancel, read back terminal.
	EvidenceRunCancelled
	// EvidenceRunDeleted is a stopped run's delete, which the engine took.
	EvidenceRunDeleted
	// EvidenceTurnEnded is the turn lifecycle's idle transition.
	EvidenceTurnEnded
	// EvidenceProcessGone is a process-scope fact another boot wrote.
	EvidenceProcessGone
)

func (e Evidence) String() string {
	switch e {
	case EvidenceTabClosed:
		return "tab_closed"
	case EvidenceRunCancelled:
		return "run_cancelled"
	case EvidenceRunDeleted:
		return "run_deleted"
	case EvidenceTurnEnded:
		return "turn_ended"
	case EvidenceProcessGone:
		return "process_gone"
	case evidenceNone:
	}
	return "none"
}

// RunMarkRefresh is how often a run's progress rewrites its keep mark. PurgeIdle counts a run mark as keeping until
// one retention period after its Since plus this, so the throttle never lets a mark expire before its last sign of
// life is a retention period old.
const RunMarkRefresh = time.Hour

func (e Evidence) valid() bool { return e > evidenceNone && e <= EvidenceProcessGone }

// keepSets is the store's in-memory keep state, every map under mu, a leaf lock never held around I/O.
type keepSets struct {
	pending          map[marotte.ChatID]map[string]marotte.KeepFact
	unattributed     map[string]struct{}
	teardownChats    map[marotte.ChatID]int
	teardownSessions map[string]int
	reconciled       map[marotte.ChatID]struct{}
	mu               sync.Mutex
}

func newKeepSets() keepSets {
	return keepSets{
		pending:          make(map[marotte.ChatID]map[string]marotte.KeepFact),
		unattributed:     make(map[string]struct{}),
		teardownChats:    make(map[marotte.ChatID]int),
		teardownSessions: make(map[string]int),
		reconciled:       make(map[marotte.ChatID]struct{}),
	}
}

func randomHex(byteLen int) string {
	b := make([]byte, byteLen)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Boot is this process's boot id, written on every fact it holds.
func (s *Store) Boot() string { return s.boot }

// factLive reports whether a fact still keeps its chat. An unrecognised key is live: a reader that cannot interpret a
// fact must not read it as absence.
func (s *Store) factLive(key string, f marotte.KeepFact) bool {
	if kind, _ := marotte.KeepKind(key); kind == "" {
		return true
	}
	return !f.Process || f.Boot == s.boot
}

func (s *Store) newFact(scope KeepScope) marotte.KeepFact {
	return marotte.KeepFact{Since: time.Now().UnixMilli(), Boot: s.boot, Process: scope == keepProcess}
}

func setKeep(c *marotte.Chat, key string, f marotte.KeepFact) {
	if c.Keep == nil {
		c.Keep = make(map[string]marotte.KeepFact)
	}
	c.Keep[key] = f
}

func deleteKeep(c *marotte.Chat, key string) {
	delete(c.Keep, key)
	if len(c.Keep) == 0 {
		c.Keep = nil
	}
}

// absentErr is the answer for a record that does not exist: the teardown first, then the tombstone, then not found.
func (s *Store) absentErr(chatID marotte.ChatID) error {
	switch {
	case s.inTeardown(chatID):
		return ErrInTeardown
	case s.isTombstoned(chatID):
		return ErrTombstoned
	}
	return fmt.Errorf("%w: %s", ErrChatNotFound, chatID)
}

// isAbsence reports an answer that means the record is gone, as opposed to unreadable.
func isAbsence(err error) bool {
	return errors.Is(err, ErrTombstoned) || errors.Is(err, ErrChatNotFound)
}

// loadKeepLocked loads chatID's record for a keep write. The caller holds the record lock.
func (s *Store) loadKeepLocked(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, error) {
	c, err := s.load(ctx, chatID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, s.absentErr(chatID)
	}
	if err != nil {
		return nil, err
	}
	if c.ID != string(chatID) {
		return nil, errChatIDMismatch(chatID, c.ID)
	}
	return c, nil
}

// flushPendingLocked writes chatID's pending holds onto c and answers them; the caller drops them with settlePending
// once the header write succeeded. The caller holds the record lock, the only place the chat's pending entry changes.
func (s *Store) flushPendingLocked(chatID marotte.ChatID, c *marotte.Chat) map[string]marotte.KeepFact {
	s.keep.mu.Lock()
	p := maps.Clone(s.keep.pending[chatID])
	s.keep.mu.Unlock()
	for k, f := range p {
		setKeep(c, k, f)
	}
	return p
}

func (s *Store) settlePending(chatID marotte.ChatID, flushed map[string]marotte.KeepFact) {
	if len(flushed) == 0 {
		return
	}
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	for k := range flushed {
		delete(s.keep.pending[chatID], k)
	}
	if len(s.keep.pending[chatID]) == 0 {
		delete(s.keep.pending, chatID)
	}
}

func (s *Store) pend(chatID marotte.ChatID, key string, f marotte.KeepFact, cause error) {
	s.keep.mu.Lock()
	if s.keep.pending[chatID] == nil {
		s.keep.pending[chatID] = make(map[string]marotte.KeepFact)
	}
	s.keep.pending[chatID][key] = f
	s.keep.mu.Unlock()
	slog.Warn("keep fact pending: the record could not be written; the chat is kept until it is",
		"chat_id", chatID, "key", key, "error", cause)
}

// dropPendingClaim drops chatID's pended key only while it carries token.
func (s *Store) dropPendingClaim(chatID marotte.ChatID, key, token string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	if f, ok := s.keep.pending[chatID][key]; !ok || f.Token != token {
		return
	}
	delete(s.keep.pending[chatID], key)
	if len(s.keep.pending[chatID]) == 0 {
		delete(s.keep.pending, chatID)
	}
}

func (s *Store) dropPending(chatID marotte.ChatID, key string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	delete(s.keep.pending[chatID], key)
	if len(s.keep.pending[chatID]) == 0 {
		delete(s.keep.pending, chatID)
	}
}

// Hold writes the fact key onto chatID's record and answers the claim a failed open rolls back. An absent record is
// ErrInTeardown, ErrTombstoned or ErrChatNotFound; any other failure is returned and nothing is held. Every call writes,
// so the last writer's token is on the record. It stamps no UpdatedAt and broadcasts nothing.
func (s *Store) Hold(ctx context.Context, chatID marotte.ChatID, key string, scope KeepScope) (Claim, error) {
	if err := ctx.Err(); err != nil {
		return Claim{}, err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	return s.holdLocked(ctx, chatID, key, scope)
}

func (s *Store) holdLocked(ctx context.Context, chatID marotte.ChatID, key string, scope KeepScope) (Claim, error) {
	c, err := s.loadKeepLocked(ctx, chatID)
	if err != nil {
		return Claim{}, err
	}
	flushed := s.flushPendingLocked(chatID, c)
	claim := Claim{ChatID: chatID, Key: key, Token: randomHex(8)}
	if prev, ok := c.Keep[key]; ok {
		claim.PriorFact = prev
		claim.Prior = priorForeignBoot
		if prev.Boot == s.boot {
			claim.Prior = PriorThisBoot
		}
	}
	f := s.newFact(scope)
	f.Token = claim.Token
	setKeep(c, key, f)
	if err := s.writeHeader(ctx, chatID, c); err != nil {
		return Claim{}, err
	}
	s.settlePending(chatID, flushed)
	return claim, nil
}

// HoldOrPend is Hold for a writer whose action cannot be refused: a read or write failure on a record that may exist
// pends the fact, which keeps the chat until a later read of the record writes it, and answers a priorUnknown claim
// with a nil error.
func (s *Store) HoldOrPend(ctx context.Context, chatID marotte.ChatID, key string, scope KeepScope) (Claim, error) {
	if _, err := s.pathFor(chatID); err != nil {
		return Claim{}, err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	claim, err := s.holdLocked(ctx, chatID, key, scope)
	if err == nil || isAbsence(err) {
		return claim, err
	}
	s.pend(chatID, key, s.newFact(scope), err)
	return Claim{ChatID: chatID, Key: key, Prior: priorUnknown}, nil
}

// HoldTurn is the turn registry's reservation hold: the process-scope turn fact, written unless this boot already
// holds it. It answers only ErrInTeardown or ErrTombstoned; a record that does not exist yet answers nil with nothing
// written, and every read or write failure pends.
func (s *Store) HoldTurn(ctx context.Context, chatID marotte.ChatID) error {
	if _, err := s.pathFor(chatID); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.loadKeepLocked(ctx, chatID)
	switch {
	case errors.Is(err, ErrTombstoned):
		return err
	case errors.Is(err, ErrChatNotFound):
		return nil
	case err != nil:
		s.pend(chatID, marotte.KeyTurn, s.newFact(keepProcess), err)
		return nil
	}
	s.turnFactLocked(ctx, chatID, c)
	return nil
}

// turnFactLocked writes the turn fact onto c unless this boot holds it, pending it when the write fails.
func (s *Store) turnFactLocked(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat) {
	flushed := s.flushPendingLocked(chatID, c)
	if f, ok := c.Keep[marotte.KeyTurn]; ok && f.Boot == s.boot && len(flushed) == 0 {
		return
	}
	setKeep(c, marotte.KeyTurn, s.newFact(keepProcess))
	if err := s.writeHeader(ctx, chatID, c); err != nil {
		s.pend(chatID, marotte.KeyTurn, s.newFact(keepProcess), err)
		return
	}
	s.settlePending(chatID, flushed)
}

// openTurnFactLocked is OpenTurn's write: the turn fact, rewritten whatever boot held it, and the UpdatedAt stamp,
// since a turn opening is use. A failure pends the fact: a wire turn cannot be refused.
func (s *Store) openTurnFactLocked(ctx context.Context, chatID marotte.ChatID) {
	c, err := s.loadKeepLocked(ctx, chatID)
	if err == nil {
		flushed := s.flushPendingLocked(chatID, c)
		setKeep(c, marotte.KeyTurn, s.newFact(keepProcess))
		c.UpdatedAt = time.Now().UnixMilli()
		if err = s.writeHeader(ctx, chatID, c); err == nil {
			s.settlePending(chatID, flushed)
			return
		}
	}
	s.pend(chatID, marotte.KeyTurn, s.newFact(keepProcess), err)
}

// Rollback undoes one failed open: while the record still carries the claim's token it deletes a key the claim found
// absent and otherwise writes back the value the claim overwrote. A fact another writer took since, or one the claim
// did not create, survives unchanged. A missing record answers nil; a read or write error is returned and the fact
// stays, which keeps the chat.
func (s *Store) Rollback(ctx context.Context, cl *Claim) error {
	if cl.Prior == priorUnknown || cl.Token == "" {
		return nil
	}
	m := s.lock(cl.ChatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.loadKeepLocked(ctx, cl.ChatID)
	if isAbsence(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if f, ok := c.Keep[cl.Key]; !ok || f.Token != cl.Token {
		return nil
	}
	flushed := s.flushPendingLocked(cl.ChatID, c)
	if cl.Prior == PriorAbsent {
		deleteKeep(c, cl.Key)
	} else {
		setKeep(c, cl.Key, cl.PriorFact)
	}
	if err := s.writeHeader(ctx, cl.ChatID, c); err != nil {
		return err
	}
	s.settlePending(cl.ChatID, flushed)
	return nil
}

// Release removes the fact key from chatID's record on evidence that its thing ended. A pended hold of the key is
// dropped first, whatever the load answers, so it is never flushed later. A missing record or key answers nil; a read
// or write failure is returned and the fact stays, which keeps the chat.
func (s *Store) Release(ctx context.Context, chatID marotte.ChatID, key string, ev Evidence) error {
	if !ev.valid() {
		return errNoEvidence
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	return s.releaseLocked(ctx, chatID, key, ev)
}

func (s *Store) releaseLocked(ctx context.Context, chatID marotte.ChatID, key string, ev Evidence) error {
	if !ev.valid() {
		return errNoEvidence
	}
	s.dropPending(chatID, key)
	c, err := s.loadKeepLocked(ctx, chatID)
	if isAbsence(err) {
		return nil
	}
	if err != nil {
		return err
	}
	flushed := s.flushPendingLocked(chatID, c)
	_, had := c.Keep[key]
	if !had && len(flushed) == 0 {
		return nil
	}
	deleteKeep(c, key)
	if err := s.writeHeader(ctx, chatID, c); err != nil {
		return err
	}
	s.settlePending(chatID, flushed)
	if had {
		slog.Debug("keep fact released", "chat_id", chatID, "key", key, "evidence", ev.String())
	}
	return nil
}

// SwapTabForStop is a person's close of chatID's last tab: one header write replaces the tab fact with the
// process-scope stop hold, so no purge pass reads the record with neither. swapped is false when the record could not
// be read or written: the hold then pends and the tab fact stays on disk until the close's owed release lands. An
// absent record answers swapped with a claim that holds nothing.
func (s *Store) SwapTabForStop(ctx context.Context, chatID marotte.ChatID) (cl Claim, swapped bool) {
	if _, err := s.pathFor(chatID); err != nil {
		return Claim{}, false
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	cl = Claim{ChatID: chatID, Key: marotte.KeyStop, Token: randomHex(8)}
	f := s.newFact(keepProcess)
	f.Token = cl.Token
	c, err := s.loadKeepLocked(ctx, chatID)
	if isAbsence(err) {
		return Claim{}, true
	}
	if err == nil {
		flushed := s.flushPendingLocked(chatID, c)
		deleteKeep(c, marotte.KeyTab)
		setKeep(c, marotte.KeyStop, f)
		if err = s.writeHeader(ctx, chatID, c); err == nil {
			s.settlePending(chatID, flushed)
			return cl, true
		}
	}
	s.pend(chatID, marotte.KeyStop, f, err)
	return cl, false
}

// EndStop drops cl's stop hold from the pending set and the record while either carries cl's token, so a later
// close's hold survives an earlier close's end. The caller calls it once every stop its close started has ended. A
// read or write failure is logged; a hold already on the record then keeps the chat until this process exits.
func (s *Store) EndStop(ctx context.Context, cl *Claim) {
	if cl.Token == "" {
		return
	}
	m := s.lock(cl.ChatID)
	m.Lock()
	defer m.Unlock()
	s.dropPendingClaim(cl.ChatID, cl.Key, cl.Token)
	c, err := s.loadKeepLocked(ctx, cl.ChatID)
	if isAbsence(err) {
		return
	}
	if err == nil {
		flushed := s.flushPendingLocked(cl.ChatID, c)
		f, held := c.Keep[cl.Key]
		mine := held && f.Token == cl.Token
		if !mine && len(flushed) == 0 {
			return
		}
		if mine {
			deleteKeep(c, cl.Key)
		}
		if err = s.writeHeader(ctx, cl.ChatID, c); err == nil {
			s.settlePending(cl.ChatID, flushed)
			return
		}
	}
	slog.Warn("close: the stop hold could not be released from the record; a hold already written keeps the chat until restart",
		"chat_id", cl.ChatID, "error", err)
}

// startKeep gives a created record no keep facts, so a fork's Init copying its parent cannot carry the parent's, and
// starts its fill-in stamp.
func startKeep(c *marotte.Chat) {
	c.Keep = nil
	c.KeepFilledAt = time.Now().UnixMilli()
}

// EndTurn releases the turn fact at the lifecycle's idle transition and stamps UpdatedAt, since a turn ending is use.
// A pended turn hold is dropped first. announce is true when the header changed, for AnnounceHeader after the caller's
// lifecycle lock. A failure keeps the fact until this process exits.
func (s *Store) EndTurn(ctx context.Context, chatID marotte.ChatID) (announce bool) {
	if _, err := s.pathFor(chatID); err != nil {
		return false
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	s.dropPending(chatID, marotte.KeyTurn)
	c, err := s.loadKeepLocked(ctx, chatID)
	if isAbsence(err) {
		return false
	}
	if err != nil {
		slog.Warn("turn fact not released; the chat is kept until restart", "chat_id", chatID, "error", err)
		return false
	}
	flushed := s.flushPendingLocked(chatID, c)
	_, had := c.Keep[marotte.KeyTurn]
	if !had && len(flushed) == 0 {
		return false
	}
	deleteKeep(c, marotte.KeyTurn)
	if had {
		c.UpdatedAt = time.Now().UnixMilli()
	}
	if err := s.writeHeader(ctx, chatID, c); err != nil {
		slog.Warn("turn fact not released; the chat is kept until restart", "chat_id", chatID, "error", err)
		return false
	}
	s.settlePending(chatID, flushed)
	return had
}

// AnnounceHeader mints the chat's versions and broadcasts its header, for a write that stamped UpdatedAt outside
// Mutate.
func (s *Store) AnnounceHeader(ctx context.Context, chatID marotte.ChatID) {
	s.bumpChatWindow(ctx, chatID)
}

// HoldUnattributed records a live run whose launching chat could not be named because the chats directory could not
// be listed; PurgeIdle keeps every chat while any is recorded.
func (s *Store) HoldUnattributed(workflowID string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	s.keep.unattributed[workflowID] = struct{}{}
}

// ClearUnattributed drops workflowID's mark, once the run is attributed or ended.
func (s *Store) ClearUnattributed(workflowID string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	delete(s.keep.unattributed, workflowID)
}

func (s *Store) anyUnattributed() bool {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	return len(s.keep.unattributed) > 0
}

// beginTeardown marks chatID and its chain as torn down. Called under the record lock before remove, so no writer can
// slip between the removal and the mark.
func (s *Store) beginTeardown(chatID marotte.ChatID, chain []string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	s.keep.teardownChats[chatID]++
	for _, id := range chain {
		s.keep.teardownSessions[id]++
	}
}

// EndTeardown clears what PurgeIdle marked for chatID's teardown. Only the end of the teardown clears it, never time.
func (s *Store) EndTeardown(chatID marotte.ChatID, chain []string) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	s.keep.teardownChats[chatID]--
	if s.keep.teardownChats[chatID] <= 0 {
		delete(s.keep.teardownChats, chatID)
	}
	for _, id := range chain {
		s.keep.teardownSessions[id]--
		if s.keep.teardownSessions[id] <= 0 {
			delete(s.keep.teardownSessions, id)
		}
	}
}

func (s *Store) inTeardown(chatID marotte.ChatID) bool {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	return s.keep.teardownChats[chatID] > 0
}

// SessionInTeardown reports whether sessionID is in the chain of a chat whose purge teardown still runs.
func (s *Store) SessionInTeardown(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	return s.keep.teardownSessions[sessionID] > 0
}

// bindsTornDownSession reports a save whose chain gains a session in teardown that prior did not hold.
func (s *Store) bindsTornDownSession(prior, next []string) bool {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	if len(s.keep.teardownSessions) == 0 {
		return false
	}
	for _, id := range next {
		if s.keep.teardownSessions[id] > 0 && !slices.Contains(prior, id) {
			return true
		}
	}
	return false
}

func (s *Store) reconciled(chatID marotte.ChatID) bool {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	_, ok := s.keep.reconciled[chatID]
	return ok
}

func (s *Store) markReconciled(chatID marotte.ChatID) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	s.keep.reconciled[chatID] = struct{}{}
}

func (s *Store) forgetReconciled(chatID marotte.ChatID) {
	s.keep.mu.Lock()
	defer s.keep.mu.Unlock()
	delete(s.keep.reconciled, chatID)
}

// Reconciled reports whether the keep-state reconcile (or the record's create) has passed over chatID this boot.
func (s *Store) Reconciled(chatID marotte.ChatID) bool { return s.reconciled(chatID) }

// KeepRecord is the reconcile's view of one record under its lock. Its writes are the only reconcile writes to Keep.
type KeepRecord struct {
	c       *marotte.Chat
	store   *Store
	filled  int64
	changed bool
}

// Facts answers a copy of the record's keep facts.
func (r *KeepRecord) Facts() map[string]marotte.KeepFact { return maps.Clone(r.c.Keep) }

// FilledAt is the record's KeepFilledAt as loaded, zero on a record that predates keep facts.
func (r *KeepRecord) FilledAt() int64 { return r.filled }

// Add writes key unless the record holds it.
func (r *KeepRecord) Add(key string, scope KeepScope) {
	if _, ok := r.c.Keep[key]; ok {
		return
	}
	setKeep(r.c, key, r.store.newFact(scope))
	r.changed = true
}

// Release removes key on evidence; no evidence value releases nothing.
func (r *KeepRecord) Release(key string, ev Evidence) {
	if !ev.valid() {
		return
	}
	if _, ok := r.c.Keep[key]; !ok {
		return
	}
	deleteKeep(r.c, key)
	r.changed = true
	slog.Debug("keep fact released", "chat_id", r.c.ID, "key", key, "evidence", ev.String())
}

// ReconcileKeep runs fn over chatID's record under its lock, sets KeepFilledAt when it is unset, writes when anything
// changed (no UpdatedAt stamp, no broadcast), and marks the record reconciled for this boot. A read or write error is
// returned and the record stays unreconciled.
func (s *Store) ReconcileKeep(ctx context.Context, chatID marotte.ChatID, fn func(r *KeepRecord)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.loadKeepLocked(ctx, chatID)
	if err != nil {
		return err
	}
	flushed := s.flushPendingLocked(chatID, c)
	r := &KeepRecord{c: c, store: s, filled: max(c.KeepFilledAt, 0), changed: len(flushed) > 0}
	fn(r)
	if c.KeepFilledAt <= 0 {
		c.KeepFilledAt = time.Now().UnixMilli()
		r.changed = true
	}
	if r.changed {
		if err := s.writeHeader(ctx, chatID, c); err != nil {
			return err
		}
	}
	s.settlePending(chatID, flushed)
	s.markReconciled(chatID)
	return nil
}

// PurgeIdle removes chatID when its record, read under the record lock, holds no keep fact but run marks and no
// pending hold, no unsent words, a last use and freshest run mark (runMarkUntil) before cutoff, the reconcile has
// passed over it this boot, and no run is unattributed. A record that cannot be read is never removed: the error is
// returned and the record stays. A removal marks the chat and its chain in teardown until the caller's EndTeardown,
// and broadcasts chat_deleted.
func (s *Store) PurgeIdle(ctx context.Context, chatID marotte.ChatID, cutoff time.Time) (archive.PurgeVerdict, error) {
	if err := ctx.Err(); err != nil {
		return archive.PurgeVerdict{}, err
	}
	m := s.lock(chatID)
	m.Lock()
	v, chatsVersion, err := s.purgeIdleLocked(ctx, chatID, cutoff)
	m.Unlock()
	if err != nil || v.Outcome != archive.PurgePurged {
		return v, err
	}
	s.broadcastDeleted(ctx, chatID, chatsVersion)
	return v, nil
}

func (s *Store) purgeIdleLocked(ctx context.Context, chatID marotte.ChatID, cutoff time.Time) (archive.PurgeVerdict, string, error) {
	kept := archive.PurgeVerdict{Outcome: archive.PurgeKept}
	c, err := s.load(ctx, chatID)
	if errors.Is(err, os.ErrNotExist) {
		return archive.PurgeVerdict{Outcome: archive.PurgeAbsent}, "", nil
	}
	if err != nil {
		return kept, "", err
	}
	if c.ID != string(chatID) {
		return kept, "", errChatIDMismatch(chatID, c.ID)
	}
	use := lastUse(c)
	if by := s.keptBy(ctx, chatID, c); by != "" {
		if use > 0 && time.UnixMilli(use).Before(cutoff) {
			kept.KeptBy = by
		}
		return kept, "", nil
	}
	if use <= 0 {
		slog.Warn("chat purge: a chat records no last use; kept", "chat_id", chatID)
		return kept, "", nil
	}
	if until := max(use, runMarkUntil(c)); !time.UnixMilli(until).Before(cutoff) {
		kept.Deadline = time.UnixMilli(until).Add(time.Since(cutoff))
		if time.UnixMilli(use).Before(cutoff) {
			kept.KeptBy = marotte.KeepKindRun
		}
		return kept, "", nil
	}
	chain := c.SessionChain()
	s.beginTeardown(chatID, chain)
	chatsVersion, err := s.remove(chatID)
	if err != nil {
		s.EndTeardown(chatID, chain)
		return kept, "", err
	}
	return archive.PurgeVerdict{Outcome: archive.PurgePurged, SessionChain: chain}, chatsVersion, nil
}

// keptBy answers the first reason c must stay whatever its age, "" when there is none. Pending holds are flushed onto
// the record on the way; a failed flush leaves them pending, which keeps the chat either way.
func (s *Store) keptBy(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat) string {
	if flushed := s.flushPendingLocked(chatID, c); len(flushed) > 0 {
		if err := s.writeHeader(ctx, chatID, c); err == nil {
			s.settlePending(chatID, flushed)
		}
		return archive.KeptByPending
	}
	if !s.reconciled(chatID) {
		return archive.KeptByUnreconciled
	}
	if s.anyUnattributed() {
		return archive.KeptByUnattributed
	}
	for _, key := range slices.Sorted(maps.Keys(c.Keep)) {
		f := c.Keep[key]
		if !s.factLive(key, f) {
			continue
		}
		kind, _ := marotte.KeepKind(key)
		switch {
		case kind == "":
			return archive.KeptByUnknown
		case kind == marotte.KeepKindRun && f.Since > 0:
			// Kept by time, not by presence (runMarkUntil).
			continue
		}
		return kind
	}
	if c.Draft != "" || len(marotte.RunRows(c.Run)) > 0 {
		return archive.KeptByWords
	}
	return ""
}

// runMarkUntil is the Unix ms a run mark keeps c's purge clock at, from its freshest run mark: that mark's Since
// plus RunMarkRefresh. Zero when c holds none.
func runMarkUntil(c *marotte.Chat) int64 {
	var until int64
	for key, f := range c.Keep {
		if kind, _ := marotte.KeepKind(key); kind == marotte.KeepKindRun {
			until = max(until, f.Since+RunMarkRefresh.Milliseconds())
		}
	}
	return until
}

// lastUse is the instant the purge ages a chat from: the later of its last use and its keep fill-in.
func lastUse(c *marotte.Chat) int64 { return max(c.UpdatedAt, c.KeepFilledAt) }
