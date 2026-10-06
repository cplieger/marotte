// Package chat implements per-chat persistence: one directory per chat under
// <dir>/<chat_id>/ holding chat.json, the header, and entries.jsonl, the turn log.
// The header is atomically replaced on every mutation; the log is appended one sealed
// entry at a time. The directory listing is the index, and the store is the single
// source of truth for chat state. A chat's ACP session id lives in the header so a
// container restart can resume via session/load.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"golang.org/x/sync/singleflight"
)

// errInvalidUTF8 marks content that cannot round-trip through JSON, the storage format.
var errInvalidUTF8 = errors.New("chat: content contains invalid UTF-8")

// errDraftTooLarge is returned when a composer draft exceeds marotte.MaxDraftBytes.
var errDraftTooLarge = errors.New("chat: draft exceeds the size cap")

// The two ways a staged attachment list is refused at the store: more entries than
// marotte.MaxAttachments, and a path empty, over the byte cap or not UTF-8.
var (
	errTooManyAttachments = errors.New("chat: too many attachments")
	errBadAttachmentPath  = errors.New("chat: attachment path is empty or too long")
)

// broadcaster is the SSE fan-out this store emits chat lifecycle events through.
type broadcaster interface {
	Broadcast(ctx context.Context, evt marotte.ServerEvent)
}

// Compile-time assertion: Store satisfies archive.StoreAccess.
var _ archive.StoreAccess = (*Store)(nil)

// fileMode is the on-disk mode for chat files. The parent dir uses 0o700 because
// chat content may contain secrets the user pasted into prompts.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// OpenTurnTail is one open turn of a chat as the runtime holds it: the turn id and
// the entries still coalescing, which the GET serves beside the log's window.
type OpenTurnTail struct {
	ID      string
	Entries []marotte.OpenEntry
}

// Store owns the chat directory. Each chat has its own mutex so different chats
// never block each other; same-chat mutations serialize, header writes and log
// appends alike. A short-TTL tombstone set closes the delete-during-turn race:
// without it, an append arriving after a concurrent Delete would re-create the chat
// directory as a ghost row.
type Store struct {
	broadcast   broadcaster
	versions    *subject.Versions
	epoch       func() string
	listSF      singleflight.Group
	onPurge     func(chatID marotte.ChatID, sessionChain []string)
	isLive      func(chatID marotte.ChatID) bool
	hasOpenTab  func(chatID marotte.ChatID) bool
	live        func(chatID marotte.ChatID) bool
	openTurns   func(chatID marotte.ChatID) []OpenTurnTail
	tombstone   map[marotte.ChatID]tombstone
	archive     *archive.Service
	dequeued    dequeues
	locks       sync.Map
	logs        sync.Map
	dir         string
	index       searchIndex
	fileCap     chatFileCap
	archiveOnce sync.Once
	tombMu      sync.Mutex
}

// tombstoneTTL is how long a deleted chat id blocks re-creation via Mutate: longer
// than any real prompt roundtrip, short enough not to blacklist a recycled id.
const tombstoneTTL = 10 * time.Minute

// tombstone keeps the display name a deleted chat had, because the record goes before
// its bridge's teardown and a notice that bridge raises meanwhile still names it.
type tombstone struct {
	at   time.Time
	name string
}

// NewStore opens (or creates) the chat directory at dir; an error means startup must fail. A mode
// that cannot be enforced only warns, because the running container is the operator's way in to
// repair /config.
func NewStore(dir string, opts ...StoreOption) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("chat store: mkdir %s: %w", dir, err)
	}
	// MkdirAll's mode is only a request (setgid parents, inheritable ACLs). EnforceDir re-stats the
	// descriptor it chmod'ed, so the mode logged below is what the filesystem stored, and it
	// refuses a symlink at the name.
	stored, err := filemode.EnforceDir(dir, dirMode)
	mode := stored.String()
	if err != nil {
		slog.Warn("chat store: chat dir is not 0700 and could not be made 0700; chat content may be readable by other users on this host",
			"dir", dir, "error", err)
		// The mode is genuinely UNKNOWN here (the open or the stat failed), so the
		// breadcrumb must not print a zero FileMode as an observation.
		mode = "unverified"
	}
	slog.Info("chat store: opened", "dir", dir, "mode", mode)
	s := &Store{
		dir:       dir,
		fileCap:   resolveChatFileCap(),
		tombstone: make(map[marotte.ChatID]tombstone),
		versions:  &subject.Versions{},
	}
	// Options land AFTER the derivation so WithChatFileCap overrides it, and the
	// derivation's own log line still records what the container asked for.
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// StoreOption configures optional dependencies on a Store at construction time.
type StoreOption func(*Store)

// WithBroadcaster sets the SSE broadcaster used by the store to emit
// chat_created / chat_updated / chat_deleted events.
func WithBroadcaster(b broadcaster) StoreOption {
	return func(s *Store) { s.broadcast = b }
}

// WithVersions makes the store mint its `chat` and `chats` versions into the
// shared registry the digest resolver and the REST envelopes read. Without it
// the store mints into a private registry, which keeps every return honest but
// reaches no resolver.
func WithVersions(v *subject.Versions) StoreOption {
	return func(s *Store) { s.versions = v }
}

// WithEpoch supplies the hub epoch the two chat GET envelopes stamp beside their
// version. Without it the stamps carry no epoch, which a client's version map
// refuses, so composition always wires it.
func WithEpoch(fn func() string) StoreOption {
	return func(s *Store) { s.epoch = fn }
}

// WithLive registers the live-chat predicate purging exempts. See
// archive.WithLiveChats.
func WithLive(fn func(chatID marotte.ChatID) bool) StoreOption {
	return func(s *Store) { s.isLive = fn }
}

// WithOpenTab registers retention's second exemption: a chat with an open TAB is
// never purged, however old. See archive.WithOpenTabs for what that costs.
func WithOpenTab(fn func(chatID marotte.ChatID) bool) StoreOption {
	return func(s *Store) { s.hasOpenTab = fn }
}

// WithLiveTurn registers the runtime's turn-in-flight predicate, so this package's
// HTTP surface can STATE whether the chat's own turn is live rather than infer it
// from the log. Injected post-construction because the agent runtime needs the
// store, so the store cannot import it.
func WithLiveTurn(fn func(chatID marotte.ChatID) bool) StoreOption {
	return func(s *Store) { s.live = fn }
}

// WithOpenTurns registers the runtime's reader of a chat's open turns and their
// still-coalescing entries, the content half of what WithLiveTurn states: the GET
// serves them as open_entries beside the log's window, and stamps one live_turn
// subject per open turn it serves.
func WithOpenTurns(fn func(chatID marotte.ChatID) []OpenTurnTail) StoreOption {
	return func(s *Store) { s.openTurns = fn }
}

// Live reports whether the chat's own turn is open, a prompt is admitted and
// awaiting its bracket, or the admission slot is held. False when no predicate was
// injected: an unwired Store reads the record as final, the safe direction.
func (s *Store) Live(chatID marotte.ChatID) bool {
	if s.live == nil {
		return false
	}
	return s.live(chatID)
}

// WithOnPurge registers a callback fired after a retention purge removes a chat.
// sessionChain carries every KAS session the chat ran on, captured before the chat
// directory was removed, so the purge can reap its own session directories.
func WithOnPurge(fn func(chatID marotte.ChatID, sessionChain []string)) StoreOption {
	return func(s *Store) { s.onPurge = fn }
}

// chatIDPattern reports whether id is a valid chat identifier.
func chatIDPattern(id marotte.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// Get returns the chat's header at chatID, or false if it does not exist.
func (s *Store) Get(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, bool) {
	c, _, ok := s.GetStamped(ctx, chatID)
	return c, ok
}

// GetStamped is Get plus the `chat` stamp the REST envelope carries: the current
// version read under the same per-chat mutex the load holds, so a Mutate or a
// composer write cannot land between the record and the version that vouches
// for it. A chat never mutated this process stamps subject.Unminted.
func (s *Store) GetStamped(ctx context.Context, chatID marotte.ChatID) (*marotte.Chat, *marotte.SubjectStamp, bool) {
	if ctx.Err() != nil {
		return nil, nil, false
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(ctx, chatID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat get", "chat_id", chatID, "error", err)
		}
		return nil, nil, false
	}
	version, _ := s.versions.Current(subject.KindChat, string(chatID))
	return c, s.restStamp(subject.KindChat, string(chatID), version), true
}

// restStamp builds a REST envelope's stamp: the version with the hub epoch.
func (s *Store) restStamp(kind subject.Kind, ref, version string) *marotte.SubjectStamp {
	stamp := marotte.NewSubjectStamp(string(kind), ref, version)
	if s.epoch != nil {
		stamp.Epoch = s.epoch()
	}
	return stamp
}

// Mutate is the header's mutation primitive: load → apply → save → broadcast, under the per-chat
// mutex, on the current header or a fresh zero-value chat. Returning false aborts without side
// effects; a write to a recently deleted id is refused with ErrTombstoned.
//
// A mutator must not overwrite c.ID (refused); c.CreatedAt is restored. The returned version is the
// `chat:<id>` version this save minted, or "" when the mutator declined.
func (s *Store) Mutate(ctx context.Context, chatID marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	return s.mutateLocked(ctx, chatID, mutate)
}

// mutateLocked is Mutate's body, for a caller already holding the chat's lock.
func (s *Store) mutateLocked(ctx context.Context, chatID marotte.ChatID, mutate func(c *marotte.Chat, exists bool) bool) (string, error) {
	c, err := s.load(ctx, chatID)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !exists {
		// A late write must not resurrect a deleted chat as a ghost row. The refusal is named
		// because a caller reading it as success would spawn a bridge for output discarded at
		// persist.
		if s.isTombstoned(chatID) {
			slog.Info("chat: refused to resurrect tombstoned id", "chat_id", logsafe.Field(string(chatID)))
			return "", ErrTombstoned
		}
		c = &marotte.Chat{ID: string(chatID), CreatedAt: time.Now().UnixMilli()}
	}
	stripped := exists && s.dequeued.strip(chatID, c)
	originalCreatedAt := c.CreatedAt
	if !mutate(c, exists) {
		if !stripped {
			return "", nil
		}
		// The mutator declined and may have touched c, so the pending removal is
		// written alone from a fresh read.
		if c, err = s.load(ctx, chatID); err != nil {
			return "", err
		}
		s.dequeued.strip(chatID, c)
	}
	// A reassigned id would let s.save write under a mismatched per-chat mutex.
	if c.ID != string(chatID) {
		slog.Error("chat mutate: mutator reassigned chat id",
			"expected", logsafe.Field(string(chatID)), "got", logsafe.Field(c.ID))
		return "", fmt.Errorf("chat mutate: mutator reassigned id %q → %q", chatID, c.ID)
	}
	c.CreatedAt = originalCreatedAt
	if err := validateChatUTF8(c); err != nil {
		return "", err
	}
	if err := s.save(ctx, chatID, c); err != nil {
		return "", err
	}
	if stripped {
		s.dequeued.settle(chatID)
	}
	// The cross-chat filter indexes the title, so a header write invalidates it.
	s.index.drop(chatID)
	version := s.versions.BumpCounter(subject.KindChat, string(chatID))
	s.broadcastMutation(ctx, chatID, c, exists)
	slog.Debug("chat mutate", "chat_id", logsafe.Field(string(chatID)), "existed", exists)
	return version, nil
}

// validateChatUTF8 returns errInvalidUTF8 when the chat name or the composer draft
// is not valid UTF-8 — content that would not round-trip through the JSON storage
// format.
func validateChatUTF8(c *marotte.Chat) error {
	if !utf8.ValidString(c.Name) {
		return errInvalidUTF8
	}
	if !utf8.ValidString(c.Draft) {
		return errInvalidUTF8
	}
	return nil
}

// broadcastMutation mints the `chats` version for a successful Mutate and emits chat_created or
// chat_updated stamped with it. Every save bumps `chats`, broadcaster or not, because title,
// updated_at and sort order move on each.
func (s *Store) broadcastMutation(ctx context.Context, chatID marotte.ChatID, c *marotte.Chat, exists bool) {
	chatsVersion := s.versions.BumpCounter(subject.KindChats, "")
	if s.broadcast == nil {
		return
	}
	evt := marotte.EventChatUpdated
	if !exists {
		evt = marotte.EventChatCreated
	}
	frame := marotte.NewEvent(evt, chatID, c.Header())
	frame.Subject = marotte.NewSubjectStamp(string(subject.KindChats), "", chatsVersion)
	s.broadcast.Broadcast(ctx, frame)
}

// SetDraft persists the chat's unsent composer text. Not a Mutate: it leaves UpdatedAt alone
// (retention ages from it) and broadcasts nothing; a missing chat is a no-op. Returns nil when
// nothing was written, else the whole composer state for the draft_changed broadcast.
func (s *Store) SetDraft(ctx context.Context, chatID marotte.ChatID, text string) (*marotte.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The command boundary already refuses both, but the store owns what reaches the
	// file, and a draft that cannot round-trip through JSON is unloadable.
	if len(text) > marotte.MaxDraftBytes {
		return nil, errDraftTooLarge
	}
	if !utf8.ValidString(text) {
		return nil, errInvalidUTF8
	}
	return s.setComposer(ctx, chatID, "chat draft", func(c *marotte.Chat) bool {
		if c.Draft == text {
			return false
		}
		c.Draft = text
		return true
	})
}

// SetAttachments persists the paths staged beside the chat's draft, replacing
// whatever was there. The draft's twin: no Mutate, so UpdatedAt is untouched, and no
// record means no-op rather than a created chat. An empty slice clears the row.
func (s *Store) SetAttachments(ctx context.Context, chatID marotte.ChatID, paths []string) (*marotte.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The store owns what reaches the file; see SetDraft.
	if len(paths) > marotte.MaxAttachments {
		return nil, errTooManyAttachments
	}
	for _, p := range paths {
		if p == "" || len(p) > marotte.MaxAttachmentPathBytes {
			return nil, errBadAttachmentPath
		}
		if !utf8.ValidString(p) {
			return nil, errInvalidUTF8
		}
	}
	next := slices.Clone(paths)
	if len(next) == 0 {
		// nil rather than empty: `omitempty` keeps the field out of the chat file.
		next = nil
	}
	return s.setComposer(ctx, chatID, "chat attachments", func(c *marotte.Chat) bool {
		if slices.Equal(c.Attachments, next) {
			return false
		}
		c.Attachments = next
		return true
	})
}

// setComposer is the shared body of the two composer writers: load under the chat's
// own lock, apply, write only when something moved, and report the state that
// landed. `what` names the caller in the mismatch log.
func (s *Store) setComposer(ctx context.Context, chatID marotte.ChatID, what string, apply func(*marotte.Chat) bool) (*marotte.ComposerState, error) {
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(ctx, chatID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// This record claims to be a different chat, so nothing about it can be
	// persisted under this id's lock.
	if c.ID != string(chatID) {
		slog.Error(what+": chat header holds another chat's id",
			"chat_id", chatID, "stored_id", c.ID)
		return nil, errChatIDMismatch(chatID, c.ID)
	}
	if !apply(c) {
		return nil, nil
	}
	if err := s.writeHeader(ctx, chatID, c); err != nil {
		return nil, err
	}
	state := c.Composer()
	// The composer is part of the `chat` projection, so the write bumps `chat` even though it
	// bypasses save; the version rides the returned state.
	state.Version = s.versions.BumpCounter(subject.KindChat, string(chatID))
	return &state, nil
}

// Delete removes the chat directory and broadcasts chat_deleted. Records a
// tombstone first so a concurrent Mutate cannot resurrect the id.
func (s *Store) Delete(ctx context.Context, chatID marotte.ChatID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	chatsVersion, rmErr := s.Remove(chatID)
	missing := errors.Is(rmErr, os.ErrNotExist)
	m.Unlock()
	if rmErr != nil && !missing {
		return rmErr
	}
	if s.broadcast != nil {
		frame := marotte.NewEvent(marotte.EventChatDeleted, chatID, marotte.ChatDeletedPayload{ID: string(chatID)})
		frame.Subject = marotte.NewSubjectStamp(string(subject.KindChats), "", chatsVersion)
		s.broadcast.Broadcast(ctx, frame)
	}
	if missing {
		slog.Info("chat delete: no-op on missing chat", "chat_id", chatID)
	} else {
		// Read outside markDeleted's lock; a creep pattern needs no dedicated gauge.
		s.tombMu.Lock()
		tombCount := len(s.tombstone)
		s.tombMu.Unlock()
		slog.Debug("chat delete", "chat_id", chatID, "tombstones", tombCount)
	}
	return nil
}
