// Package secretstore holds the MCP OAuth state KAS asks marotte to keep via the v3
// `_kiro/secret/*` requests. KAS runs MCP OAuth but keeps it only in memory, so without this
// store every engine start re-runs Dynamic Client Registration.
//
// KAS keys its state `kiro.mcp.<sha256 of the server config>.<kind>`. The grant (kinds
// client and tokens) persists; every other key (the PKCE verifier, the discovery state, any
// kind KAS adds later) is held in memory until ForgetSignInState, the lifetime KAS gives it
// with no client storage, so a spent verifier or a stale authorization server never outlives
// its engine.
//
// The grant lives in one file, <configDir>/mcp-secrets.json, mode 0600, written atomically:
// `{"secrets": {"<key>": "<base64 value>"}}`. Base64 round-trips values byte-exactly; it is an
// encoding, the 0600 is the protection. One keyed map rather than a file per key, so a key
// can never traverse a path. Values are never logged; log lines carry the key.
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
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/filemode"
)

// Bounds far above anything KAS produces, so a buggy or hostile flow cannot grow the file
// or the memory half without limit.
const (
	// maxValueBytes caps one credential blob.
	maxValueBytes = 64 << 10
	// maxKeyBytes caps a key. KAS's own keys are ~80 bytes.
	maxKeyBytes = 512
	// maxEntries caps the persisted keys: two per server configuration, a ceiling of 512.
	maxEntries = 1024
	// maxSignInEntries caps the memory half: two per server configuration in one engine.
	maxSignInEntries = 512
	// maxFileBytes bounds the whole file on both read and write.
	maxFileBytes = 8 << 20

	fileMode = 0o600
	dirMode  = 0o700
	fileName = "mcp-secrets.json"
)

// errTooLarge is returned when a key or value exceeds its bound, or when the
// store is full.
var errTooLarge = errors.New("secretstore: value rejected (over limit)")

// durableKey matches the keys that persist: KAS's client registration and token set.
var durableKey = regexp.MustCompile(`^kiro\.mcp\.[0-9a-f]{64}\.(client|tokens)$`)

// file is the on-disk shape: key → base64(value). A named field so an addition needs no
// format migration.
type file struct {
	Secrets map[string]string `json:"secrets"`
}

// Store is a process-global keyed credential store shared by every engine generation: KAS's
// key namespace is global, so a restarted engine reuses the first one's registration.
type Store struct {
	// Field order is govet fieldalignment's.
	// secrets is the persisted grant, mirrored in the file.
	secrets map[string]string
	// signIn holds every other key, never written.
	signIn map[string]string
	path   string
	mu     sync.RWMutex
}

// New opens (or creates) the store under configDir. A missing file is the first-run state;
// an unparseable one is moved aside and treated as empty (the credentials are re-derivable).
func New(configDir string) (*Store, error) {
	s := &Store{
		path:    filepath.Join(configDir, fileName),
		secrets: map[string]string{},
		signIn:  map[string]string{},
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
		if !durableKey.MatchString(k) {
			// The file holds only what Set persists; the next persist rewrites it without k.
			slog.Debug("secretstore: non-durable entry dropped", "key", k)
			continue
		}
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
	if durableKey.MatchString(key) {
		v, ok := s.secrets[key]
		return v, ok
	}
	v, ok := s.signIn[key]
	return v, ok
}

// Set stores value under key. A grant key persists the whole store, and a persist error is
// RETURNED: KAS rethrows it, where a swallowed one would read back empty on the next spawn.
// Any other key is held in memory only.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if len(value) > maxValueBytes {
		return fmt.Errorf("%w: value is %d bytes, limit %d", errTooLarge, len(value), maxValueBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !durableKey.MatchString(key) {
		if _, exists := s.signIn[key]; !exists && len(s.signIn) >= maxSignInEntries {
			return fmt.Errorf("%w: sign-in state holds %d entries, limit %d", errTooLarge, len(s.signIn), maxSignInEntries)
		}
		s.signIn[key] = value
		return nil
	}
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
	if !durableKey.MatchString(key) {
		delete(s.signIn, key)
		return nil
	}
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

// ForgetSignInState drops every key held in memory and keeps the persisted grant. The caller
// runs it once per engine generation, before the engine can ask. Safe on a nil Store.
func (s *Store) ForgetSignInState() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.signIn)
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
