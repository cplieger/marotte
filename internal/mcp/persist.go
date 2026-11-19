package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/cplieger/go/filesafe"
)

// UnmarshalJSON validates the transport field at the JSON parse boundary, rejecting an unknown value early
// rather than letting it flow through to runtime dispatch; each of the four transports is kept verbatim.
func (s *Server) UnmarshalJSON(data []byte) error {
	type serverAlias Server
	var raw serverAlias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Transport != "" {
		parsed, err := parseTransport(string(raw.Transport))
		if err != nil {
			return err
		}
		raw.Transport = parsed
	}
	*s = Server(raw)
	return nil
}

// file is the on-disk schema: versioned envelope + ordered list, and the organization provider. Each entry is a
// Server and the provider an IdentityProvider, each decoded on its own so one bad member costs only itself (load).
type file struct {
	IdentityProvider json.RawMessage   `json:"identity_provider,omitempty"`
	Servers          []json.RawMessage `json:"servers"`
	Version          int               `json:"version"`
}

const fileVersion = 1

// marshalRecord is the record's bytes: the servers, then the entries load could not read, with the same value, and
// the provider, or the member load could not read.
func (s *Store) marshalRecord() ([]byte, error) {
	entries := make([]json.RawMessage, 0, len(s.servers)+len(s.unreadable))
	for _, sv := range s.servers {
		entry, err := json.Marshal(sv)
		if err != nil {
			return nil, fmt.Errorf("%w mcp.json: %w", errPersistMarshal, err)
		}
		entries = append(entries, entry)
	}
	entries = append(entries, s.unreadable...)
	rec := file{Version: fileVersion, Servers: entries, IdentityProvider: s.heldIdentityProvider}
	if s.identityProvider != nil {
		idp, err := json.Marshal(s.identityProvider)
		if err != nil {
			return nil, fmt.Errorf("%w mcp.json: %w", errPersistMarshal, err)
		}
		rec.IdentityProvider = idp
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w mcp.json: %w", errPersistMarshal, err)
	}
	return data, nil
}

// persist writes marotte's own record and then RENDERS KAS's config file from it. KAS's file is read first, so one
// it would refuse to rewrite fails the save before the record changes; a later KAS write failure puts the record's
// previous bytes back, so a failed save is absent at the next boot. The caller rolls its in-memory mutation back on
// error. MUST be called on the write turn with s.mu held for writing: sync.RWMutex is not reentrant, and every call
// site already holds both across its mutation and this write.
func (s *Store) persist(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	kasDoc, _, err := s.readKASDoc()
	if err != nil {
		return err
	}
	data, err := s.marshalRecord()
	if err != nil {
		return err
	}
	prev, prevErr := os.ReadFile(s.path)
	if prevErr != nil && !errors.Is(prevErr, os.ErrNotExist) {
		return fmt.Errorf("%w: read the previous record: %w", errPersistWrite, prevErr)
	}
	if _, err := filesafe.WriteFile(ctx, s.path, data,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", errPersistWrite, err)
	}
	policy, _ := s.renderPolicy(ctx)
	kasErr := s.writeKASConfigWith(ctx, kasDoc, s.servers, policy)
	if kasErr == nil {
		return nil
	}
	if rErr := s.restoreRecord(ctx, prev, prevErr == nil); rErr != nil {
		return errors.Join(kasErr, rErr)
	}
	return kasErr
}

// restoreRecord puts back the record bytes a failed save replaced, or removes the record it created. It runs past
// ctx's end: a cancelled save is the case it repairs.
func (s *Store) restoreRecord(ctx context.Context, prev []byte, existed bool) error {
	if !existed {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: remove the record a failed save created: %w", errPersistWrite, err)
		}
		return nil
	}
	if _, err := filesafe.WriteFile(context.WithoutCancel(ctx), s.path, prev,
		filesafe.WithMode(0o600), filesafe.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: restore the record a failed save replaced: %w", errPersistWrite, err)
	}
	return nil
}
