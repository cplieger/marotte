package chat

// The header file beside an entry log: <root>/chat.json, the whole marotte.Chat;
// the transcript is the log beside it. Small, so a whole-file atomic
// replace costs nothing; a run root has none.
//
// The header's turn_count and last_turn_outcome are CACHES. After any crash the
// header may lag the log, and the log wins: OpenEntryLog hands this writer the
// log's own values, and Counters below is what applies them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/marotte"
)

// maxHeaderBytes bounds one header read. The header holds no transcript, so its
// size is a chat's field set plus a draft and its attachment paths; a file past this
// is not one this store wrote.
const maxHeaderBytes = 8 << 20

// EntryHeader reads and writes one log root's header file.
//
// A VALUE holding a path and no state: the per-chat lock that serialises a header
// write against an append belongs to the caller, which holds it across a read and
// the write that follows.
type EntryHeader struct {
	root string
}

// NewEntryHeader names the header beside the log under root.
func NewEntryHeader(root string) EntryHeader { return EntryHeader{root: root} }

// path is the header file.
func (h EntryHeader) path() string { return filepath.Join(h.root, headerFileName) }

// Read answers the stored header, or os.ErrNotExist for a root holding no
// chat.json. The transcript is the log's; the header carries none of it.
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

// Write replaces the header atomically: temp, fsync, rename, dir fsync, through
// atomicfile. The directory is created 0700 because a header carries the chat's
// name, draft and attachment paths.
func (h EntryHeader) Write(ctx context.Context, c *marotte.Chat) error {
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

// Update applies every change in ONE header write, and answers false when apply
// declines, so a caller changing two fields cannot crash between them and leave the
// record half moved.
//
// A root holding no header yet is written as the bare record apply is handed.
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
	return true, h.Write(ctx, c)
}

// EntryHeader is the header policy a CHAT root supplies to its log.
var _ LogHeader = EntryHeader{}

// Counters caches the log's turn_count and last_turn_outcome, writing only when the
// header disagrees. The values are the LOG's, which is what makes the two agree
// after a crash left the header behind.
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

// CloserModel is the model a synthesized closer stamps. Absent on a header this
// process cannot read, which is honest: the closer then carries no model rather
// than a guess.
func (h EntryHeader) CloserModel() string {
	c, err := h.Read(context.Background())
	if err != nil {
		return ""
	}
	return c.Model
}

// Reconcilable is true: a chat's header names the session whose history this record
// may have lost, so every condition of the log's reconcile predicate is asked here.
func (h EntryHeader) Reconcilable() bool { return true }

// SessionID is the session the header names, the string condition (i) of the log's
// reconcile predicate compares against the sessions the log's own turn_bind entries
// name. Empty on a header this process cannot read, which makes the condition false
// rather than a guess.
func (h EntryHeader) SessionID() string {
	c, err := h.Read(context.Background())
	if err != nil {
		return ""
	}
	return c.ACPSessionID
}
