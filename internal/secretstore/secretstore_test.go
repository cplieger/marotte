package secretstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realKey has the exact shape KAS derives (sha256("http://127.0.0.1:46877/mcp" + "|")).
const realKey = "kiro.mcp.2a0a3d1d4672ffaff77fcbe95f21be210e2e444f1b152fb537773dd72a3ddf3a.client"

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	return s
}

// TestRoundTripAcrossProcesses pins that a credential stored by one process is readable by
// the next.
func TestRoundTripAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()

	first, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	blob := `{"client_id":"probe-client-1","client_secret":"s3cret"}`
	if err := first.Set(ctx, realKey, blob); err != nil {
		t.Fatalf("Set() error = %v, want nil", err)
	}

	// A second Store over the same directory stands in for the next process.
	second, err := New(dir)
	if err != nil {
		t.Fatalf("New() (second) error = %v", err)
	}
	got, ok := second.Get(realKey)
	if !ok {
		t.Fatal("Get() ok = false after a Set in a previous process, want true")
	}
	if got != blob {
		t.Errorf("Get() = %q, want %q (the store must return the exact bytes it was given)", got, blob)
	}
}

// TestFileIs0600 pins the permission. These are OAuth client secrets and
// refresh tokens, so a group- or world-readable file is a leak.
func TestFileIs0600(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := s.Set(t.Context(), realKey, "v"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %#o, want 0600", perm)
	}
}

// TestLoadTightensLoosePerms covers a file written by an older build, or copied
// onto the volume by hand: opening it must narrow the mode rather than trust it.
func TestLoadTightensLoosePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	seeded := `{"secrets":{"k":"` + base64.StdEncoding.EncodeToString([]byte("v")) + `"}}`
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store perms = %#o after load, want 0600 (load must tighten)", perm)
	}
	if v, ok := s.Get("k"); !ok || v != "v" {
		t.Errorf("Get() = (%q, %v), want (\"v\", true): tightening must not lose the contents", v, ok)
	}
}

// TestGetMissIsNotAnError pins the contract KAS relies on: an absent
// credential is "not registered yet", which is the first-run answer.
func TestGetMissIsNotAnError(t *testing.T) {
	s := newStore(t)
	if v, ok := s.Get("kiro.mcp.deadbeef.tokens"); ok || v != "" {
		t.Errorf("Get(absent) = (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if err := s.Set(ctx, realKey, "v"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := s.Delete(ctx, realKey); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, ok := s.Get(realKey); ok {
		t.Error("Get() ok = true after Delete, want false")
	}
	// Absent-key delete succeeds: the requested post-state already holds.
	if err := s.Delete(ctx, realKey); err != nil {
		t.Errorf("Delete(absent) error = %v, want nil", err)
	}
}

// TestDeleteAbsentDoesNotWrite pins that a no-op delete leaves the file alone.
func TestDeleteAbsentDoesNotWrite(t *testing.T) {
	s := newStore(t)
	if err := s.Delete(t.Context(), "never-stored"); err != nil {
		t.Fatalf("Delete(absent) error = %v", err)
	}
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat store err = %v, want ErrNotExist: an absent-key delete must not create the file", err)
	}
}

func TestBounds(t *testing.T) {
	ctx := t.Context()

	t.Run("value over limit", func(t *testing.T) {
		s := newStore(t)
		err := s.Set(ctx, realKey, strings.Repeat("x", maxValueBytes+1))
		if !errors.Is(err, errTooLarge) {
			t.Errorf("Set(oversize value) error = %v, want ErrTooLarge", err)
		}
		if _, ok := s.Get(realKey); ok {
			t.Error("a rejected Set left the key present")
		}
	})

	t.Run("key over limit", func(t *testing.T) {
		s := newStore(t)
		if err := s.Set(ctx, strings.Repeat("k", maxKeyBytes+1), "v"); !errors.Is(err, errTooLarge) {
			t.Errorf("Set(oversize key) error = %v, want ErrTooLarge", err)
		}
	})

	// The limits are inclusive: a blob exactly at the cap is stored.
	t.Run("value exactly at the limit is stored", func(t *testing.T) {
		s := newStore(t)
		value := strings.Repeat("x", maxValueBytes)
		if err := s.Set(ctx, realKey, value); err != nil {
			t.Fatalf("Set(value of exactly %d bytes) error = %v, want nil", maxValueBytes, err)
		}
		got, ok := s.Get(realKey)
		if !ok {
			t.Fatalf("Get(%q) missing after a Set at the value limit", realKey)
		}
		if len(got) != maxValueBytes {
			t.Errorf("Get(%q) returned %d bytes, want %d", realKey, len(got), maxValueBytes)
		}
	})

	t.Run("key exactly at the limit is stored", func(t *testing.T) {
		s := newStore(t)
		key := strings.Repeat("k", maxKeyBytes)
		if err := s.Set(ctx, key, "v"); err != nil {
			t.Fatalf("Set(key of exactly %d bytes) error = %v, want nil", maxKeyBytes, err)
		}
		if got, ok := s.Get(key); !ok || got != "v" {
			t.Errorf("Get(key of exactly %d bytes) = %q, %v, want %q, true", maxKeyBytes, got, ok, "v")
		}
	})

	t.Run("empty key", func(t *testing.T) {
		s := newStore(t)
		if err := s.Set(ctx, "", "v"); err == nil {
			t.Error("Set(\"\") error = nil, want an error")
		}
	})

	t.Run("entry count", func(t *testing.T) {
		s := newStore(t)
		for i := range maxEntries {
			if err := s.Set(ctx, "k"+string(rune('a'+i%26))+strings.Repeat("z", i/26+1), "v"); err != nil {
				t.Fatalf("Set(#%d) error = %v", i, err)
			}
		}
		if got := s.count(); got != maxEntries {
			t.Fatalf("count = %d, want %d", got, maxEntries)
		}
		if err := s.Set(ctx, "one-too-many", "v"); !errors.Is(err, errTooLarge) {
			t.Errorf("Set() past the entry cap error = %v, want ErrTooLarge", err)
		}
		// An OVERWRITE at the cap is still allowed: KAS refreshes a token set in place.
		existing := firstKey(s)
		if err := s.Set(ctx, existing, "refreshed"); err != nil {
			t.Errorf("Set(existing key) at the cap error = %v, want nil", err)
		}
	})
}

func (s *Store) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.secrets)
}

// firstKey returns any key the store holds. Only for the cap test.
func firstKey(s *Store) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k := range s.secrets {
		return k
	}
	return ""
}

// TestCorruptStoreMovedAside covers the recovery posture: these credentials are
// re-derivable, so an unparseable file must not stop the store from opening.
func TestCorruptStoreMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() over a corrupt store error = %v, want nil (credentials are re-derivable)", err)
	}
	if got := s.count(); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want it renamed aside", path, err)
	}
	if got := corruptSiblings(t, dir); len(got) != 1 {
		t.Errorf("quarantine siblings = %v, want exactly one %s.corrupt.<ts>.<pid>", got, fileName)
	}
	if err := s.Set(t.Context(), realKey, "v"); err != nil {
		t.Errorf("Set() after corrupt recovery error = %v, want nil", err)
	}
}

// TestCorruptStoreReportsTheQuarantineNotAFailure pins the log line of a SUCCESSFUL
// quarantine: the log is the only record of whether the forensic copy exists.
func TestCorruptStoreReportsTheQuarantineNotAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	logs := captureLogs(t)
	if _, err := New(dir); err != nil {
		t.Fatalf("New() over a corrupt store error = %v, want nil", err)
	}
	got := logs.String()
	if !strings.Contains(got, "secretstore: store unparseable, moved aside") {
		t.Errorf("logs = %q, want the moved-aside record naming where the evidence went", got)
	}
	if strings.Contains(got, "secretstore: preserve corrupt store failed") {
		t.Errorf("logs = %q, must not report a preservation failure for a rename that succeeded", got)
	}
}

// The handler is global, so this package's tests never run in parallel.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevLogger, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevLogger)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// corruptSiblings returns the quarantine files in dir, by PREFIX (the name carries a
// timestamp and PID).
func corruptSiblings(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), fileName+".corrupt.") {
			found = append(found, e.Name())
		}
	}
	return found
}

// TestCorruptStoreKeepsTheFirstForensicCopy pins that the first corrupt store survives a
// later one. The earlier copy is staged directly: two real corruptions in one second and
// one PID would race for the same name.
func TestCorruptStoreKeepsTheFirstForensicCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)

	const firstCopy = `{"secrets":{"kiro.mcp.first.client":"dHJ1bmNhdGVk`
	first := path + ".corrupt"
	if err := os.WriteFile(first, []byte(firstCopy), 0o600); err != nil {
		t.Fatalf("stage the already-quarantined copy: %v", err)
	}
	const secondCorruption = `{"secrets":{"kiro.mcp.second.client":`
	if err := os.WriteFile(path, []byte(secondCorruption), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}

	if _, err := New(dir); err != nil {
		t.Fatalf("New() over a corrupt store error = %v, want nil", err)
	}

	got, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("the first forensic copy is gone: %v", err)
	}
	if string(got) != firstCopy {
		t.Errorf("the first forensic copy was overwritten: content = %q, want %q", got, firstCopy)
	}
	// The quarantine still has to happen: the store must be off the live path
	// and its bytes preserved under a distinct name.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want it renamed aside", path, err)
	}
	siblings := corruptSiblings(t, dir)
	if len(siblings) != 1 {
		t.Fatalf("quarantine siblings = %v, want exactly one %s.corrupt.<ts>.<pid>", siblings, fileName)
	}
	quarantined, err := os.ReadFile(filepath.Join(dir, siblings[0]))
	if err != nil {
		t.Fatalf("read %s: %v", siblings[0], err)
	}
	if string(quarantined) != secondCorruption {
		t.Errorf("quarantined content = %q, want the corrupt store's own bytes %q", quarantined, secondCorruption)
	}
}

// TestPersistFailureRollsBack pins that the in-memory map never claims a durability the disk
// lacks. The persist is broken with a DIRECTORY at the store path: rename(2) refuses it
// even for root, where an unwritable parent would not (uid 0 bypasses DAC).
func TestPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx := t.Context()
	if err := s.Set(ctx, realKey, "original"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	// Replace the store file with a directory so the atomic rename cannot land.
	if err := os.Remove(s.path); err != nil {
		t.Fatalf("remove store file: %v", err)
	}
	if err := os.Mkdir(s.path, 0o700); err != nil {
		t.Fatalf("mkdir in place of the store file: %v", err)
	}

	if err := s.Set(ctx, realKey, "replacement"); err == nil {
		t.Fatal("Set() error = nil with a directory at the store path, want an error")
	}
	if v, _ := s.Get(realKey); v != "original" {
		t.Errorf("Get() = %q after a failed Set, want %q (the failed write must roll back)", v, "original")
	}

	if err := s.Delete(ctx, realKey); err == nil {
		t.Fatal("Delete() error = nil with a directory at the store path, want an error")
	}
	if v, ok := s.Get(realKey); !ok || v != "original" {
		t.Errorf("Get() = (%q, %v) after a failed Delete, want (%q, true)", v, ok, "original")
	}
}

// TestOnDiskShape pins the file format: a flat map of PLAINTEXT key → base64 value.
func TestOnDiskShape(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := s.Set(t.Context(), realKey, "blob"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var f struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	if len(f.Secrets) != 1 {
		t.Fatalf("len(secrets) = %d, want 1", len(f.Secrets))
	}
	want := base64.StdEncoding.EncodeToString([]byte("blob"))
	if f.Secrets[realKey] != want {
		t.Errorf("secrets[%q] = %q, want %q (base64 of the value)", realKey, f.Secrets[realKey], want)
	}
	// The value must NOT appear verbatim, or the encoding is not applied.
	if strings.Contains(string(data), `"blob"`) {
		t.Error("the raw value appears in the file; values must be base64-encoded")
	}
}

// TestUndecodableEntryDropped covers a hand-edited or truncated entry: it costs
// that one credential (KAS re-derives it), never the whole store.
func TestUndecodableEntryDropped(t *testing.T) {
	dir := t.TempDir()
	good := base64.StdEncoding.EncodeToString([]byte("keepme"))
	seeded := `{"secrets":{"good":"` + good + `","bad":"!!!not base64!!!"}}`
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if v, ok := s.Get("good"); !ok || v != "keepme" {
		t.Errorf("Get(good) = (%q, %v), want (\"keepme\", true)", v, ok)
	}
	if _, ok := s.Get("bad"); ok {
		t.Error("Get(bad) ok = true, want false: an undecodable entry must be dropped")
	}
}

// FuzzKeysAndValues checks arbitrary (untrusted) keys and values: an accepted pair
// round-trips byte-for-byte, a rejected one leaves no trace, nothing escapes the directory.
func FuzzKeysAndValues(f *testing.F) {
	f.Add(realKey, `{"client_id":"x"}`)
	f.Add("", "")
	f.Add("../../etc/passwd", "pwned")
	f.Add("kiro.mcp.../../../x.client", "traversal")
	f.Add("a/b/c", "slashes")
	f.Add("k\x00v", "nul")
	f.Add("ключ", "юникод")
	f.Add(strings.Repeat("k", maxKeyBytes), "at the key limit")
	// Seeds for the two round-trip bugs found: a non-UTF-8 value and a non-UTF-8 key.
	f.Add(realKey, "\x9c")
	f.Add("\xfe", "0")

	f.Fuzz(func(t *testing.T, key, value string) {
		dir := t.TempDir()
		s, err := New(dir)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		ctx := t.Context()
		setErr := s.Set(ctx, key, value)
		if setErr != nil {
			if _, ok := s.Get(key); ok {
				t.Errorf("Set(%q) failed but the key is present", key)
			}
		} else {
			got, ok := s.Get(key)
			if !ok {
				t.Fatalf("Set(%q) succeeded but Get missed", key)
			}
			if got != value {
				t.Errorf("round-trip changed the value: got %q, want %q", got, value)
			}
			// Durability: a fresh Store over the same dir sees the same bytes.
			reopened, rErr := New(dir)
			if rErr != nil {
				t.Fatalf("New() (reopen) error = %v", rErr)
			}
			if v, ok2 := reopened.Get(key); !ok2 || v != value {
				t.Errorf("reopen lost the pair: got (%q, %v), want (%q, true)", v, ok2, value)
			}
		}

		// The ONLY file the store may create is its own: a key is never a path component.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		for _, e := range entries {
			if e.Name() != fileName {
				t.Errorf("store created an unexpected entry %q from key %q", e.Name(), key)
			}
		}
	})
}
