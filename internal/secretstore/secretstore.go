// Package secretstore persists the opaque credential blobs KAS asks marotte to hold via the
// v3 `_kiro/secret/*` requests. KAS runs MCP OAuth but persists none of it, so without this
// store every bridge spawn re-runs Dynamic Client Registration.
//
// One file, <configDir>/mcp-secrets.json, mode 0600, written atomically: a flat
// `{"secrets": {"<key>": "<base64 value>"}}` map. Values are base64 so they round-trip
// byte-exactly (encoding/json would replace invalid UTF-8); this is an encoding, not
// encryption, and the 0600 is the protection. Keys and values are OPAQUE (KAS owns their
// derivation and shape). One keyed map rather than a file per key, so an attacker-adjacent
// key can never traverse a path. Values are secrets and are never logged; log lines carry
// the KEY only.
package secretstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
)

// Bounds far above anything KAS produces (blobs are 90-211 bytes), so a buggy or hostile
// flow cannot grow the file without limit.
const (
	// maxValueBytes caps one credential blob.
	maxValueBytes = 64 << 10
	// maxKeyBytes caps a key. KAS's own keys are ~80 bytes.
	maxKeyBytes = 512
	// maxEntries caps how many distinct keys the store holds. Three keys per
	// MCP server, so this is a ceiling of ~340 servers.
	maxEntries = 1024
	// maxFileBytes bounds the whole file on both read and write.
	maxFileBytes = 8 << 20

	fileMode = 0o600
	dirMode  = 0o700
	fileName = "mcp-secrets.json"
)

// errTooLarge is returned when a key or value exceeds its bound, or when the
// store is full.
var errTooLarge = errors.New("secretstore: value rejected (over limit)")

// file is the on-disk shape: key → base64(value). A named field so an addition needs no
// format migration.
type file struct {
	Secrets map[string]string `json:"secrets"`
}

// Store is a process-global keyed credential store shared by EVERY bridge: KAS's key
// namespace is global, so a second chat reuses the first chat's registration.
type Store struct {
	// Field order is govet fieldalignment's.
	secrets map[string]string
	path    string
	mu      sync.RWMutex
}

// New opens (or creates) the store under configDir. A missing file is the first-run state;
// an unparseable one is moved aside and treated as empty (the credentials are re-derivable).
func New(configDir string) (*Store, error) {
	s := &Store{
		path:    filepath.Join(configDir, fileName),
		secrets: map[string]string{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	// The verdict on the NAME comes before any read: reading first would parse a planted
	// symlink's target and block forever in open(2) on a FIFO, on the boot path.
	if _, chErr := filemode.EnforceFile(s.path, fileMode); chErr != nil {
		if errors.Is(chErr, os.ErrNotExist) {
			return nil // first run
		}
		// An unverifiable mode FAILS the load: the 0600 is these tokens' whole protection. The
		// caller degrades to a nil store (per-spawn DCR), so boot is not aborted. The file is not
		// deleted: on a mode-widening filesystem that would destroy credentials every boot.
		return fmt.Errorf("refusing to use %s: its mode could not be verified as %#o, so the credentials in it may be readable by other users on this host: %w",
			fileName, fileMode, chErr)
	}
	// EnforceFile reads a FIFO's bits as compliant; OpenRegular refuses non-regular files and
	// returns the descriptor the bytes are read from, so the object judged is the object read.
	fh, _, err := atomicfile.OpenRegular(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", fileName, err)
	}
	defer func() { _ = fh.Close() }()
	// ReadBoundedFile checks the size BEFORE allocating, so the bound holds against a file the
	// agent's shell can grow.
	data, err := atomicfile.ReadBoundedFile(context.Background(), fh, maxFileBytes)
	if err != nil {
		return fmt.Errorf("read %s: %w", fileName, err)
	}
	var stored file
	if uErr := json.Unmarshal(data, &stored); uErr != nil {
		s.moveCorruptAside(uErr)
		return nil
	}
	for k, encoded := range stored.Secrets {
		raw, dErr := base64.StdEncoding.DecodeString(encoded)
		if dErr != nil {
			// Drop the one unreadable entry, not the whole store: KAS re-derives what is missing.
			slog.Warn("secretstore: undecodable entry dropped; it will be re-derived",
				"key", k, "error", dErr)
			continue
		}
		s.secrets[k] = string(raw)
	}
	return nil
}

// moveCorruptAside renames an unparseable store aside; best-effort. The name carries a UTC
// timestamp and the PID so a second corruption cannot destroy the first forensic copy.
func (s *Store) moveCorruptAside(cause error) {
	corrupt := fmt.Sprintf("%s.corrupt.%s.%d",
		s.path,
		time.Now().UTC().Format("20060102-150405"),
		os.Getpid())
	if rErr := os.Rename(s.path, corrupt); rErr != nil {
		slog.Error("secretstore: preserve corrupt store failed",
			"path", s.path, "error", rErr, "parse_error", cause)
		return
	}
	slog.Warn("secretstore: store unparseable, moved aside; MCP credentials will be re-derived",
		"from", s.path, "to", corrupt, "parse_error", cause)
}

// Get returns the value for key and whether it was present. A miss is not an error: KAS
// treats it as "not registered yet".
func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.secrets[key]
	return v, ok
}

// Set stores value under key and persists the whole store. A persist error is RETURNED:
// KAS rethrows it, where a swallowed one would read back empty on the next spawn.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if len(value) > maxValueBytes {
		return fmt.Errorf("%w: value is %d bytes, limit %d", errTooLarge, len(value), maxValueBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.secrets[key]; !exists && len(s.secrets) >= maxEntries {
		return fmt.Errorf("%w: store holds %d entries, limit %d", errTooLarge, len(s.secrets), maxEntries)
	}
	prev, had := s.secrets[key]
	s.secrets[key] = value
	if err := s.persistLocked(ctx); err != nil {
		// Roll the in-memory map back so it never claims a durability the disk lacks.
		if had {
			s.secrets[key] = prev
		} else {
			delete(s.secrets, key)
		}
		return err
	}
	return nil
}

// Delete removes key. Deleting an absent key is a no-op and not an error (the
// post-state the caller asked for already holds), and it does not write.
func (s *Store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.secrets[key]
	if !had {
		return nil
	}
	delete(s.secrets, key)
	if err := s.persistLocked(ctx); err != nil {
		s.secrets[key] = prev
		return err
	}
	return nil
}

// Caller holds s.mu.
func (s *Store) persistLocked(ctx context.Context) error {
	encoded := make(map[string]string, len(s.secrets))
	for k, v := range s.secrets {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	data, err := json.MarshalIndent(&file{Secrets: encoded}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", fileName, err)
	}
	if _, err := atomicfile.WriteFile(ctx, s.path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxFileBytes)); err != nil {
		return fmt.Errorf("write %s: %w", fileName, err)
	}
	return nil
}

// validateKey rejects an empty, over-long, or non-UTF-8 key. A non-UTF-8 key would be
// stored under a different name (encoding/json writes U+FFFD) and never found again.
func validateKey(key string) error {
	if key == "" {
		return errors.New("secretstore: empty key")
	}
	if len(key) > maxKeyBytes {
		return fmt.Errorf("%w: key is %d bytes, limit %d", errTooLarge, len(key), maxKeyBytes)
	}
	if !utf8.ValidString(key) {
		return errors.New("secretstore: key is not valid UTF-8")
	}
	return nil
}
