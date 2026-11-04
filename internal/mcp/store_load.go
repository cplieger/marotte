package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
)

// entryError is one record entry load could not decode.
type entryError struct {
	err   error
	index int
}

// load reads the record. A file that does not decode as the record's envelope, or whose every
// entry is unreadable, is moved aside and the store starts empty. Otherwise each unreadable entry
// is logged by index and kept for persist, and the file is copied aside. nameless reports an
// unreadable entry whose name does not read (holdBootOrphans).
func (s *Store) load() (nameless bool, err error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read mcp.json: %w", err)
	}
	// mcp.json holds plaintext API keys, so its 0600 is the whole protection; EnforceFile reports a
	// filesystem that stores wider and refuses a symlink. Warn-and-continue, so a reshaped /config
	// still boots and can be repaired.
	if _, chErr := filemode.EnforceFile(s.path, 0o600); chErr != nil {
		slog.Warn("mcp: mcp.json is not 0600 and could not be made 0600; the API keys in it may be readable by other users on this host",
			"path", s.path, "error", chErr)
	}
	data, empty := decodeUserJSON(raw)
	if empty {
		return false, nil
	}
	var f file
	if uErr := json.Unmarshal(data, &f); uErr != nil {
		s.moveAside(uErr)
		return false, nil
	}
	servers, unreadable, errs := decodeEntries(f.Servers)
	if len(servers) == 0 && len(unreadable) > 0 {
		s.moveAside(fmt.Errorf("entry %d: %w", errs[0].index, errs[0].err))
		return false, nil
	}
	s.servers = servers
	if len(unreadable) == 0 {
		return false, nil
	}
	s.unreadable = unreadable
	s.held = make(map[string]bool, len(unreadable))
	for i, entry := range unreadable {
		slog.Warn("mcp: an mcp.json entry is unreadable; it stays in the file and out of the server list until it is fixed",
			"path", s.path, "index", errs[i].index, "error", errs[i].err)
		if name, ok := heldName(entry); ok {
			s.held[name] = true
		} else {
			nameless = true
		}
	}
	s.copyAside(raw)
	return nameless, nil
}

// decodeEntries decodes each record entry on its own; unreadable and errs are index-aligned.
func decodeEntries(entries []json.RawMessage) (servers []*Server, unreadable []json.RawMessage, errs []entryError) {
	servers = make([]*Server, 0, len(entries))
	for i, entry := range entries {
		var sv Server
		err := json.Unmarshal(entry, &sv)
		if err == nil && string(entry) == "null" {
			err = errors.New("the entry is null")
		}
		if err != nil {
			unreadable = append(unreadable, entry)
			errs = append(errs, entryError{index: i, err: err})
			continue
		}
		servers = append(servers, &sv)
	}
	return servers, unreadable, errs
}

// heldName is the KAS key an unreadable entry was rendered under, when its name still reads.
func heldName(entry json.RawMessage) (string, bool) {
	var probe map[string]json.RawMessage
	var name string
	if json.Unmarshal(entry, &probe) != nil || json.Unmarshal(probe["name"], &name) != nil || name == "" {
		return "", false
	}
	return name, true
}

// holdBootOrphans holds every key of KAS's mcpServers member at boot that no readable server
// renders, since an unreadable entry whose name does not read may own any of them. A key added or
// a server deleted after boot is not held.
func (s *Store) holdBootOrphans(file json.RawMessage) {
	rendered := renderKASServers(s.servers, kasRenderPolicy{})
	for name := range objectOrEmpty(file) {
		if _, own := rendered[name]; !own {
			s.held[name] = true
		}
	}
}

func (s *Store) asidePath() string {
	return fmt.Sprintf("%s.corrupt.%s.%d", s.path, time.Now().UTC().Format("20060102-150405"), os.Getpid())
}

// moveAside renames a record load cannot use, so the next persist does not overwrite it.
func (s *Store) moveAside(cause error) {
	aside := s.asidePath()
	if err := os.Rename(s.path, aside); err != nil {
		slog.Error("mcp: preserve corrupt mcp.json failed",
			"path", s.path, "error", err, "parse_error", cause)
		return
	}
	slog.Warn("mcp: mcp.json unparseable, moved aside",
		"from", s.path, "to", aside, "parse_error", cause)
}

// copyAside keeps the record as read before a persist re-indents its unreadable entries.
func (s *Store) copyAside(raw []byte) {
	aside := s.asidePath()
	f, err := os.OpenFile(aside, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.Write(raw)
		if cErr := f.Close(); err == nil {
			err = cErr
		}
		if err != nil {
			err = errors.Join(err, os.Remove(aside))
		}
	}
	if err != nil {
		slog.Error("mcp: copying mcp.json with unreadable entries aside failed",
			"path", s.path, "to", aside, "error", err)
		return
	}
	slog.Warn("mcp: mcp.json has unreadable entries; copied it aside",
		"from", s.path, "to", aside, "unreadable", len(s.unreadable))
}
