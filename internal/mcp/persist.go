package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cplieger/atomicfile/v4"
)

// UnmarshalJSON validates the transport field at the JSON parse boundary,
// rejecting unknown transport values early rather than letting them flow
// through to runtime dispatch. All three transports (stdio, http, sse) are
// accepted as first-class values and preserved verbatim — "sse" is no
// longer folded into "http", since KAS accepts a distinct SSE entry over
// the v3 wire (see the Transport doc in store.go).
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

// file is the on-disk schema: versioned envelope + ordered list.
type file struct {
	Servers []*Server `json:"servers"`
	Version int       `json:"version"`
}

const fileVersion = 1

// persist writes marotte's own record and then RENDERS KAS's config file
// from it. KAS's file is read first, so one it would refuse to rewrite fails
// the save before the record changes; a later KAS write failure still leaves
// the record written. The caller rolls its in-memory mutation back on error.
//
// MUST be called on the write turn with s.mu held for writing: sync.RWMutex
// is not reentrant, and every call site already holds both across its
// mutation and this write.
func (s *Store) persist(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	kasDoc, err := s.readKASConfig()
	if err != nil {
		return err
	}
	f := file{Version: fileVersion, Servers: s.servers}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("%w mcp.json: %w", errPersistMarshal, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o700)); err != nil {
		return fmt.Errorf("%w: %w", errPersistWrite, err)
	}
	policy, _ := s.renderPolicy(ctx)
	return s.writeKASConfigWith(ctx, kasDoc, s.servers, policy)
}
