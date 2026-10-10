package chat

// The header beside an entry log is <root>/chat.json, the whole marotte.Chat; a run root has none. Its turn_count and
// last_turn_outcome are caches: after a crash the log wins, applied through Counters.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxHeaderBytes bounds one header read; a header holds fields, a draft and attachment paths, never transcript.
const maxHeaderBytes = 8 << 20

// EntryHeader reads and writes one log root's header file. A stateless value: the caller holds the per-chat lock
// across a read and its write.
type EntryHeader struct {
	root string
}

// NewEntryHeader names the header beside the log under root.
func NewEntryHeader(root string) EntryHeader { return EntryHeader{root: root} }

func (h EntryHeader) path() string { return filepath.Join(h.root, headerFileName) }

// Read returns the stored header, or os.ErrNotExist when the root has no chat.json.
func (h EntryHeader) Read(ctx context.Context) (*marotte.Chat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, info, err := openChatFile(h.path(), "chat header "+h.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxHeaderBytes {
		return nil, errFileTooLarge("chat header "+h.root, info.Size(), maxHeaderBytes)
	}
	data, err := atomicfile.ReadBoundedFile(ctx, f, maxHeaderBytes)
	if err != nil {
		return nil, fmt.Errorf("chat header %s: %w", h.root, err)
	}
	var c marotte.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse chat header %s: %w", h.path(), err)
	}
	return &c, nil
}

// write replaces the header atomically through atomicfile (temp, fsync, rename, dir fsync). The directory is 0700:
// a header carries the name, draft and attachment paths.
func (h EntryHeader) write(ctx context.Context, c *marotte.Chat) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	_, err = atomicfile.WriteFile(ctx, h.path(), data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxHeaderBytes))
	if err != nil {
		return fmt.Errorf("write chat header %s: %w", h.path(), err)
	}
	return nil
}

// Update applies every change in one header write and returns false when apply declines, so a crash cannot leave
// two fields half moved. A root with no header gets the bare record.
func (h EntryHeader) Update(ctx context.Context, apply func(c *marotte.Chat) bool) (bool, error) {
	c, err := h.Read(ctx)
	switch {
	case errors.Is(err, os.ErrNotExist):
		c = &marotte.Chat{}
	case err != nil:
		return false, err
	}
	if !apply(c) {
		return false, nil
	}
	return true, h.write(ctx, c)
}

var _ LogHeader = EntryHeader{}

// Counters caches the log's turn_count and last_turn_outcome, writing only on disagreement, so the header catches up
// after a crash.
func (h EntryHeader) Counters(ctx context.Context, turnCount uint64, last marotte.TurnOutcome) error {
	_, err := h.Update(ctx, func(c *marotte.Chat) bool {
		if c.TurnCount == ordinalAsInt(turnCount) && c.LastTurnOutcome == last {
			return false
		}
		c.TurnCount = ordinalAsInt(turnCount)
		c.LastTurnOutcome = last
		return true
	})
	return err
}

// CloserModel is the model a synthesized closer stamps; absent when the header is unreadable, so no guess.
func (h EntryHeader) CloserModel() string {
	c, err := h.Read(context.Background())
	if err != nil {
		return ""
	}
	return c.Model
}

// Reconcilable is true: a chat's header names the session whose history may be lost.
func (EntryHeader) Reconcilable() bool { return true }

// SessionID is the session the header names, condition (i) of the log's reconcile predicate against its turn_bind
// entries. Empty when the header is unreadable, making the condition false.
func (h EntryHeader) SessionID() string {
	c, err := h.Read(context.Background())
	if err != nil {
		return ""
	}
	return c.ACPSessionID
}
