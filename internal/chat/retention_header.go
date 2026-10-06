package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/cplieger/jsoncap/v2"
	"github.com/cplieger/marotte/internal/chat/archive"
	"github.com/cplieger/marotte/internal/marotte"
)

// The projection's keys are marotte.Chat's JSON names; a rename there must land here.
const (
	keyUpdatedAt     = "updated_at"
	keySessionID     = "acp_session_id"
	keyPriorSessions = "prior_acp_session_ids"
	keyDraft         = "draft"
	keyQueued        = "queued_prompts"
)

// LoadRetentionHeader reads the retention projection of chatID's header.
func (s *Store) LoadRetentionHeader(chatID marotte.ChatID) (archive.RetentionHeader, error) {
	dir, err := s.pathFor(chatID)
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	return readRetentionHeader(filepath.Join(dir, headerFileName), "chat "+string(chatID))
}

// readRetentionHeader streams a chat header, decoding only the retention fields. A projection, not a sidecar that
// could disagree with the record. It reads to the end: a later `draft` key would otherwise be missed and the chat purged.
func readRetentionHeader(path, label string) (archive.RetentionHeader, error) {
	// openChatFile carries the path guard and the OpenRegular reasoning.
	f, info, err := openChatFile(path, label)
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxHeaderBytes {
		return archive.RetentionHeader{}, errFileTooLarge(label, info.Size(), maxHeaderBytes)
	}
	h, err := decodeRetentionHeader(bufio.NewReader(io.LimitReader(f, maxHeaderBytes)))
	if err != nil {
		return archive.RetentionHeader{}, fmt.Errorf("parse %s: %w", label, err)
	}
	return h, nil
}

// decodeRetentionHeader is the projection over any reader, testable without a file; other keys are skipped at the
// token level.
func decodeRetentionHeader(r io.Reader) (archive.RetentionHeader, error) {
	var (
		h        archive.RetentionHeader
		sessions marotte.ChatHeader
	)
	dec := jsoncap.NewDecoder(r, 0)
	err := dec.Object(func(key string) error {
		// EqualFold: encoding/json, this file's other reader, matches tags case-insensitively, so a "Draft" key must still
		// count (jsoncap.Object).
		switch {
		case strings.EqualFold(key, keyUpdatedAt):
			return dec.Decode(&h.UpdatedAt)
		case strings.EqualFold(key, keySessionID):
			return dec.Decode(&sessions.ACPSessionID)
		case strings.EqualFold(key, keyPriorSessions):
			return dec.Decode(&sessions.PriorACPSessionIDs)
		case strings.EqualFold(key, keyDraft):
			// The draft's presence, not its text.
			var draft string
			if derr := dec.Decode(&draft); derr != nil {
				return derr
			}
			h.Drafting = h.Drafting || draft != ""
			return nil
		case strings.EqualFold(key, keyQueued):
			// A queued follow-up is unsent words, so it keeps the chat like a draft; presence only.
			var queued []json.RawMessage
			if derr := dec.Decode(&queued); derr != nil {
				return derr
			}
			h.Drafting = h.Drafting || len(queued) > 0
			return nil
		default:
			return dec.Skip()
		}
	})
	if err != nil {
		return archive.RetentionHeader{}, err
	}
	// Trailing bytes make encoding/json refuse the file, so retention must not read a verdict from it.
	if err := dec.End(); err != nil {
		return archive.RetentionHeader{}, err
	}
	// marotte's own composition of the two id fields, so this view agrees on a chat's retention set.
	h.SessionChain = sessions.SessionChain()
	return h, nil
}
