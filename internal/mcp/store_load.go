package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/filemode"
	"github.com/cplieger/marotte/internal/logsafe"
)

type entryError struct {
	err      error
	position int
}

// load reads the record. A file that does not decode as the record's envelope, or whose every
// entry is unreadable, is moved aside and the store starts empty. Otherwise each unreadable entry
// is logged by its 1-based position and kept for persist, the file is copied aside, and the KAS
// keys the entries may own are held (held, orphanCheck).
func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read mcp.json: %w", err)
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
		return nil
	}
	var f file
	if uErr := json.Unmarshal(data, &f); uErr != nil {
		s.moveAside(uErr)
		return nil
	}
	servers, unreadable, errs := decodeEntries(f.Servers)
	if len(servers) == 0 && len(unreadable) > 0 {
		s.moveAside(fmt.Errorf("entry at position %d: %w", errs[0].position, errs[0].err))
		return nil
	}
	s.servers = servers
	if len(unreadable) == 0 {
		return nil
	}
	s.unreadable = unreadable
	s.held = make(map[string]bool, len(unreadable))
	owned := s.serverKeys()
	for i, entry := range unreadable {
		name, named := heldName(entry)
		slog.Warn("mcp: an mcp.json entry is unreadable; it stays in the file and out of the server list until it is fixed",
			"path", s.path, "position", errs[i].position, "name", logsafe.Field(name), "error", errs[i].err)
		switch {
		case !named:
			s.orphanCheck = owned
		case !owned[nameKey(name)]:
			s.held[name] = true
		}
	}
	s.copyAside(raw)
	return nil
}

// serverKeys is the nameKey of every stored server.
func (s *Store) serverKeys() map[string]bool {
	keys := make(map[string]bool, len(s.servers))
	for _, sv := range s.servers {
		keys[nameKey(sv.Name)] = true
	}
	return keys
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
			errs = append(errs, entryError{position: i + 1, err: err})
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

// holdOrphans holds every key of file, the mcpServers member of the first KAS file readKASDoc reads
// after boot, that no server load read and no server stored at that read owns: an unreadable entry
// whose name does not read may own any of them. No save lands before that read (persist reads KAS's
// file first), so a server deleted since boot is never held; heldNameErr takes the snapshot before a
// new name joins s.servers. A key added to KAS's file later is not held.
func (s *Store) holdOrphans(file json.RawMessage) {
	current := s.serverKeys()
	for name := range objectOrEmpty(file) {
		if key := nameKey(name); !s.orphanCheck[key] && !current[key] {
			s.held[name] = true
		}
	}
	s.orphanCheck = nil
}

// heldNameErr refuses a name whose KAS key an unreadable entry may own: a server under it would
// take the key over, and its delete would then leave the key in KAS's file (renderServersKey).
// MUST be called on the write turn before name joins s.servers: a pending orphan snapshot is taken
// here, or the new server would own the key it is checked against.
func (s *Store) heldNameErr(name string) error {
	if s.orphanCheck != nil {
		if _, _, err := s.readKASDoc(); err != nil {
			return err
		}
	}
	key := nameKey(name)
	for held := range s.held {
		if nameKey(held) == key {
			return fmt.Errorf("%w: %q may belong to an entry of marotte's mcp.json that could not be read. Fix or delete that entry in the file, then restart marotte",
				errNameConflict, held)
		}
	}
	return nil
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

// copyAside keeps the record as read before a persist re-indents its unreadable entries, once:
// every copy holds the API keys, so a boot over an unchanged record makes no new one.
func (s *Store) copyAside(raw []byte) {
	if existing, ok := s.identicalAside(raw); ok {
		slog.Warn("mcp: mcp.json has unreadable entries; a copy of it is already aside",
			"path", s.path, "copy", existing, "unreadable", len(s.unreadable))
		return
	}
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

// identicalAside returns a regular mcp.json.corrupt.* sibling whose bytes equal raw.
func (s *Store) identicalAside(raw []byte) (string, bool) {
	dir, base := filepath.Split(s.path)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), base+".corrupt.") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if info, iErr := e.Info(); iErr != nil || info.Size() != int64(len(raw)) {
			continue
		}
		if got, rErr := os.ReadFile(path); rErr == nil && bytes.Equal(got, raw) {
			return path, true
		}
	}
	return "", false
}
