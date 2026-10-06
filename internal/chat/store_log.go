package chat

// Every EntryLog call the agent and command packages make goes through the Store, under the chat's
// per-chat mutex, so a header write and a log append never race.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// ErrChatNotFound reports an operation on a chat with no header and no init to
// write one.
var ErrChatNotFound = errors.New("chat: not found")

// logFor answers the chat's entry log, opening it on first use with the header's
// five hooks and the store's file cap. The caller holds the chat's lock, so the
// open's store-open closer writes the header under it.
func (s *Store) logFor(ctx context.Context, chatID marotte.ChatID) (*EntryLog, error) {
	if v, ok := s.logs.Load(chatID); ok {
		if l, isLog := v.(*EntryLog); isLog {
			return l, nil
		}
	}
	dir, err := s.pathFor(chatID)
	if err != nil {
		return nil, err
	}
	l, err := OpenEntryLog(ctx, dir, s.header(chatID), WithEntryFileCap(int64(s.fileCap)))
	if err != nil {
		return nil, err
	}
	// The log is published only once the queue is settled: a cached log is never
	// held again, so an unsettled one would leave a previous process's rows
	// sendable for this process's life.
	if err := s.holdQueuedLocked(ctx, chatID, l); err != nil {
		if cerr := l.Close(); cerr != nil {
			slog.Warn("chat open: closing the unpublished log", "chat_id", chatID, "error", cerr)
		}
		return nil, fmt.Errorf("chat open: settle queued rows: %w", err)
	}
	s.logs.Store(chatID, l)
	return l, nil
}

// holdQueuedLocked settles the follow-up queue a previous process left, at the
// log's first open in this one and after the store-open closer. A row whose id a
// turn_open already carries was sent and only lost its removal, so it goes; every
// other row is marked Held, shown and never sent automatically, because nothing in
// this process saw it queued. A chat with no header has nothing to settle. Caller
// holds the chat's mutex.
func (s *Store) holdQueuedLocked(ctx context.Context, chatID marotte.ChatID, l *EntryLog) error {
	c, err := s.load(ctx, chatID)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !slices.ContainsFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return !q.Held }):
		return nil
	}
	entries, err := l.All()
	if err != nil {
		return err
	}
	sent := make(map[string]bool)
	for i := range entries {
		if open, ok := promptOf(&entries[i]); ok {
			sent[open.Prompt.ID] = true
		}
	}
	_, err = s.mutateLocked(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.QueuedPrompts = slices.DeleteFunc(c.QueuedPrompts, func(q marotte.QueuedPrompt) bool { return sent[q.ID] })
		for i := range c.QueuedPrompts {
			c.QueuedPrompts[i].Held = true
		}
		return true
	})
	return err
}

// OpenTurn appends a turn_open to the chat's log and answers the entry, whose ID is
// the turn id every later entry carries. init writes the header for a chat that has
// none yet, so the record exists before the log's first line; a chat with no header
// and no init is ErrChatNotFound, and a recently deleted id is ErrTombstoned.
func (s *Store) OpenTurn(ctx context.Context, chatID marotte.ChatID, spec *TurnSpec, init func(c *marotte.Chat)) (*marotte.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	if _, err := s.pathFor(chatID); err != nil {
		return nil, err
	}
	if !s.headerExists(chatID) {
		if s.isTombstoned(chatID) {
			return nil, ErrTombstoned
		}
		if init == nil {
			return nil, fmt.Errorf("%w: %s", ErrChatNotFound, chatID)
		}
		if _, err := s.mutateLocked(ctx, chatID, func(c *marotte.Chat, exists bool) bool {
			if exists {
				return false
			}
			init(c)
			return true
		}); err != nil {
			return nil, err
		}
	}
	l, err := s.logFor(ctx, chatID)
	if err != nil {
		return nil, err
	}
	opened, err := l.OpenTurn(ctx, spec)
	if err != nil {
		return nil, err
	}
	s.index.extend(chatID, opened)
	return opened, nil
}

// EntrySink is a chat's log as turnlog's Sink: one value per chat, handed to every
// turnlog.Turn opened on it.
type EntrySink struct {
	store  *Store
	chatID marotte.ChatID
}

// Sink answers the chat's EntrySink.
func (s *Store) Sink(chatID marotte.ChatID) EntrySink {
	return EntrySink{store: s, chatID: chatID}
}

// Append persists one sealed entry into the chat's log.
func (k EntrySink) Append(ctx context.Context, e *marotte.Entry) error {
	return k.store.Append(ctx, k.chatID, e)
}

// Append persists one sealed entry: seq assigned, ts stamped, written and synced,
// then folded into the chat's search filter; a refused append extends nothing.
func (s *Store) Append(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) error {
	return s.withLog(ctx, chatID, func(l *EntryLog) error {
		if err := l.Append(ctx, e); err != nil {
			return err
		}
		s.index.extend(chatID, e)
		return nil
	})
}

// AppendBetweenTurns files a lane-less entry belonging to no open turn after the
// newest turn's close, or opens an event turn on an empty log to land it in.
func (s *Store) AppendBetweenTurns(ctx context.Context, chatID marotte.ChatID, e *marotte.Entry) (opened *marotte.Entry, err error) {
	err = s.withLog(ctx, chatID, func(l *EntryLog) error {
		opened, err = l.AppendBetweenTurns(ctx, e)
		if err != nil {
			return err
		}
		if opened != nil {
			s.index.extend(chatID, opened)
		}
		s.index.extend(chatID, e)
		return nil
	})
	return opened, err
}

// withLog runs op on the chat's log under its lock. A chat with no header is
// ErrChatNotFound: nothing appends to a record that does not exist, and a removed
// chat's log answers ErrTombstoned from inside.
func (s *Store) withLog(ctx context.Context, chatID marotte.ChatID, op func(l *EntryLog) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	l, err := s.openedLog(ctx, chatID)
	if err != nil {
		return err
	}
	return op(l)
}

// openedLog is logFor behind the header check: a chat with no header has no log to
// open, and opening one would create the directory the header should have.
func (s *Store) openedLog(ctx context.Context, chatID marotte.ChatID) (*EntryLog, error) {
	if _, err := s.pathFor(chatID); err != nil {
		return nil, err
	}
	if !s.headerExists(chatID) {
		if s.isTombstoned(chatID) {
			return nil, ErrTombstoned
		}
		return nil, fmt.Errorf("%w: %s", ErrChatNotFound, chatID)
	}
	return s.logFor(ctx, chatID)
}

// All answers every entry of the chat's log in file order: the whole-transcript
// read the export, the in-chat search and the bulk tool fetch make. Empty for a
// chat whose log does not exist.
func (s *Store) All(ctx context.Context, chatID marotte.ChatID) ([]marotte.Entry, error) {
	var out []marotte.Entry
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		var err error
		out, err = l.All()
		return err
	})
	return out, err
}

// Page is GET /api/chats/{id}'s answer: the header, one window of whole turns,
// the open tails of the open turns inside it, the registry's liveness verdict
// and the stamps, one per list they certify: the `chat` stamp certifies Entries,
// each `live_turn` stamp its turn's newest sealed seq. Nothing certifies
// OpenEntries or Live; a turn opened between the registry read and the window
// read is in Entries with no stamp and no tail, and the client learns it from
// its own turn_opened frame.
type Page struct {
	Draft       string
	Entries     []marotte.Entry
	OpenEntries []marotte.OpenEntry
	Subject     []*marotte.SubjectStamp
	Chat        marotte.ChatHeader
	HasMore     bool
	Live        bool
}

// Page reads the newest `turns` whole turns, or the `turns` below `before`, with everything the
// transcript GET serves beside them. False for a chat with no header; a `before` the log does not
// hold is an error the handler answers 400.
// The registry (Live and the open tails) is read BEFORE the chat's lock: its writers take the store
// lock inside their own, so the reverse order deadlocks. The stamps are read under the lock with
// the window, so each certifies exactly the entries served; a tail sealed in between is reconciled
// by id (openTail). An older page carries the `chat` stamp alone and no tails.
func (s *Store) Page(ctx context.Context, chatID marotte.ChatID, turns int, before string) (*Page, bool, error) {
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	live := s.Live(chatID)
	var tails []OpenTurnTail
	if before == "" && s.openTurns != nil {
		tails = s.openTurns(chatID)
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	if _, err := s.load(ctx, chatID); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat page", "chat_id", chatID, "error", err)
		}
		return nil, false, nil
	}
	l, err := s.logFor(ctx, chatID)
	if err != nil {
		return nil, false, err
	}
	// Read again: a first open can rewrite the queue (holdQueuedLocked), and the
	// page must not serve the header from before it.
	c, err := s.load(ctx, chatID)
	if err != nil {
		return nil, false, err
	}
	win, err := l.Window(turns, before)
	if err != nil {
		return nil, false, err
	}
	version, _ := s.versions.Current(subject.KindChat, string(chatID))
	page := &Page{
		Chat:        c.Header(),
		Draft:       c.Draft,
		Entries:     win.Entries,
		OpenEntries: []marotte.OpenEntry{},
		HasMore:     win.HasMore,
		Live:        live,
		Subject:     []*marotte.SubjectStamp{s.restStamp(subject.KindChat, string(chatID), version)},
	}
	held := make(map[string]struct{}, 2)
	for i := range win.Entries {
		held[win.Entries[i].Turn] = struct{}{}
	}
	for _, tail := range tails {
		if _, inWindow := held[tail.ID]; !inWindow {
			continue
		}
		open, ok := openTail(tail, win.Entries)
		if !ok {
			continue
		}
		page.OpenEntries = append(page.OpenEntries, open...)
		seq, _ := l.NewestSeq(tail.ID)
		page.Subject = append(page.Subject,
			s.restStamp(subject.KindLiveTurn, tail.ID, liveTurnVersion(tail.ID, seq)))
	}
	return page, true, nil
}

// openTail reconciles a tail read before the store lock against the entries read
// under it: an open entry the window already holds sealed in between and is
// dropped (the client replaces an open entry by id on entry_sealed, so serving
// both would be the same shape), and a tail whose turn_close the window holds
// closed in between and is no tail at all, so false.
func openTail(tail OpenTurnTail, sealed []marotte.Entry) ([]marotte.OpenEntry, bool) {
	sealedIDs := make(map[string]struct{}, len(tail.Entries))
	for i := range sealed {
		e := &sealed[i]
		if e.Turn != tail.ID {
			continue
		}
		if e.Kind == marotte.EntryKindTurnClose {
			return nil, false
		}
		sealedIDs[e.ID] = struct{}{}
	}
	open := make([]marotte.OpenEntry, 0, len(tail.Entries))
	for _, oe := range tail.Entries {
		if _, done := sealedIDs[oe.ID]; done {
			continue
		}
		open = append(open, oe)
	}
	return open, true
}

// TurnPage is GET /api/chats/{id}/turns/{turn}'s answer: one turn's entries past
// `after`, its open tails, and its live_turn stamp when the registry holds it open.
type TurnPage struct {
	Entries     []marotte.Entry
	OpenEntries []marotte.OpenEntry
	Subject     []*marotte.SubjectStamp
}

// TurnPage reads one turn from seq from INCLUSIVE: the repair read a client runs
// when a seq it holds is behind the log's, and from == 0 the whole turn with its
// turn_open. A turn the log does not hold is an error the handler answers 404
// with; a chat with no header is ErrChatNotFound. The registry is read before the
// chat's lock for Page's reason, and the tail is reconciled the same way.
func (s *Store) TurnPage(ctx context.Context, chatID marotte.ChatID, turn string, from uint64) (*TurnPage, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var tails []OpenTurnTail
	if s.openTurns != nil {
		tails = s.openTurns(chatID)
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	l, err := s.openedLog(ctx, chatID)
	if err != nil {
		return nil, err
	}
	entries, newestSeq, err := l.TurnPage(turn, from)
	if err != nil {
		return nil, err
	}
	page := &TurnPage{
		Entries:     entries,
		OpenEntries: []marotte.OpenEntry{},
		Subject:     []*marotte.SubjectStamp{},
	}
	for _, tail := range tails {
		if tail.ID != turn {
			continue
		}
		open, ok := openTail(tail, entries)
		if !ok {
			continue
		}
		page.OpenEntries = append(page.OpenEntries, open...)
		page.Subject = append(page.Subject,
			s.restStamp(subject.KindLiveTurn, turn, liveTurnVersion(turn, newestSeq)))
	}
	return page, nil
}

// liveTurnVersion is the live_turn subject's version, `<turn>:<seq>`: the one
// spelling the page, the digest resolver and the entry frames share.
func liveTurnVersion(turn string, seq uint64) string {
	return turn + ":" + strconv.FormatUint(seq, 10)
}

// searchable is the in-chat search's read: every sealed entry beside the set of
// turn ids the rail draws, taken under one lock so the two describe one log state.
func (s *Store) searchable(ctx context.Context, chatID marotte.ChatID) (entries []marotte.Entry, drawn map[string]struct{}, err error) {
	err = s.readLog(ctx, chatID, func(l *EntryLog) error {
		var rerr error
		entries, rerr = l.All()
		if rerr != nil {
			return rerr
		}
		drawn = drawnSet(l.RailRows())
		return nil
	})
	return entries, drawn, err
}

// drawnSet is the rail's turn ids as a set.
func drawnSet(rows []marotte.TurnSummary) map[string]struct{} {
	drawn := make(map[string]struct{}, len(rows))
	for i := range rows {
		drawn[rows[i].ID] = struct{}{}
	}
	return drawn
}

// RailRows answers one row per drawn turn, oldest first; empty for a chat whose
// log does not exist.
func (s *Store) RailRows(ctx context.Context, chatID marotte.ChatID) ([]marotte.TurnSummary, error) {
	var out []marotte.TurnSummary
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		out = l.RailRows()
		return nil
	})
	return out, err
}

// readLog runs a read over the chat's log under its lock. A chat with a header and
// no log yet is the normal state of a fresh chat, so the log is opened (empty) rather
// than refused; a chat with no header is ErrChatNotFound.
func (s *Store) readLog(ctx context.Context, chatID marotte.ChatID, op func(l *EntryLog) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	l, err := s.openedLog(ctx, chatID)
	if err != nil {
		return err
	}
	return op(l)
}

// Revert is the rewind's store half: ONE appended turn_revert, nothing cut or re-closed. It answers
// the record for the caller's entry_appended and, when the log had to mint a carrier, the carrier's
// turn_open, which the caller announces ahead of the record.
// The header counters are a cache rebuilt from the log, so a failed counter write is a Warn and
// never fails a durable revert. The caller holds the lifecycle mutex across the registry check and
// this call.
func (s *Store) Revert(ctx context.Context, chatID marotte.ChatID, turn, kasMessageID string) (record, opened *marotte.Entry, err error) {
	err = s.withLog(ctx, chatID, func(l *EntryLog) error {
		s.index.drop(chatID)
		var rerr error
		record, opened, rerr = l.Revert(ctx, turn, marotte.TurnRevertCauseRewind, kasMessageID)
		return rerr
	})
	if err != nil {
		return nil, nil, err
	}
	if cerr := s.WriteCounters(ctx, chatID); cerr != nil {
		slog.Warn("chat store: counters not cached after a revert",
			"chat", chatID, "turn", turn, "error", cerr)
	}
	s.bumpChatWindow(ctx, chatID)
	return record, opened, nil
}

// Reconcile runs the resume's merge swap over the chat's log and header under the
// chat lock; swap answers whether it rewrote the log. On a rewrite the `chat`
// version is minted and the header the rewrite's hooks wrote is broadcast, and the
// minted version comes back for the subject_changed stamp. The caller holds the
// lifecycle mutex across this call, so the swap's gates and its rewrite see one
// log.
func (s *Store) Reconcile(ctx context.Context, chatID marotte.ChatID, swap func(l *EntryLog, h EntryHeader) (bool, error)) (version string, changed bool, err error) {
	err = s.withLog(ctx, chatID, func(l *EntryLog) error {
		// The swap rewrites the log through l.Rewrite, which this store cannot
		// observe, so the search filter is dropped for the whole call.
		s.index.drop(chatID)
		var serr error
		changed, serr = swap(l, s.header(chatID))
		return serr
	})
	if err != nil || !changed {
		return "", changed, err
	}
	return s.bumpChatWindow(ctx, chatID), true, nil
}

// bumpChatWindow mints the `chat` and `chats` versions after an operation that changed which turns
// the window holds (a revert's append, the merge swap), and broadcasts the header, so a
// disconnected client's digest sees the record move.
func (s *Store) bumpChatWindow(ctx context.Context, chatID marotte.ChatID) string {
	m := s.lock(chatID)
	m.Lock()
	c, err := s.load(ctx, chatID)
	m.Unlock()
	if err != nil {
		return ""
	}
	version := s.versions.BumpCounter(subject.KindChat, string(chatID))
	s.broadcastMutation(ctx, chatID, c, true)
	return version
}

// WriteCounters caches the log's turn_count and last_turn_outcome on the header
// and broadcasts the header when they moved: the write the closer and the turn
// open run after releasing the lifecycle mutex.
func (s *Store) WriteCounters(ctx context.Context, chatID marotte.ChatID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	l, err := s.openedLog(ctx, chatID)
	if err != nil {
		m.Unlock()
		return err
	}
	before, berr := s.load(ctx, chatID)
	err = l.WriteCounters(ctx)
	after, aerr := s.load(ctx, chatID)
	m.Unlock()
	if err != nil {
		return err
	}
	if berr != nil || aerr != nil {
		return errors.Join(berr, aerr)
	}
	if before.TurnCount == after.TurnCount && before.LastTurnOutcome == after.LastTurnOutcome {
		return nil
	}
	s.versions.BumpCounter(subject.KindChat, string(chatID))
	s.broadcastMutation(ctx, chatID, after, true)
	return nil
}

// NewestRevert answers the id of the LAST turn_revert in the chat's log in file order, false when
// none: the provenance a resume's projection snapshots at open and its swap re-reads.
// EntryLog.NewestRevert owns the definition.
func (s *Store) NewestRevert(ctx context.Context, chatID marotte.ChatID) (string, bool) {
	var id string
	var held bool
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		id, held = l.NewestRevert()
		return nil
	})
	if err != nil {
		return "", false
	}
	return id, held
}

// TurnSeq answers a turn's newest sealed seq, false for a turn the log does not
// hold: the live_turn stamp's version half.
func (s *Store) TurnSeq(ctx context.Context, chatID marotte.ChatID, turn string) (uint64, bool) {
	var seq uint64
	var held bool
	err := s.readLog(ctx, chatID, func(l *EntryLog) error {
		seq, held = l.NewestSeq(turn)
		return nil
	})
	if err != nil {
		return 0, false
	}
	return seq, held
}
