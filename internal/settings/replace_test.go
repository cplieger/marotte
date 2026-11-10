package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ownedfile"
)

func replaceFixture(t *testing.T, stored string) (AgentDirs, string) {
	t.Helper()
	dirs := AgentDirs{Workspace: t.TempDir(), Config: t.TempDir()}
	resetCache(t, dirs.Config)
	t.Cleanup(func() { resetCache(t, dirs.Config) })
	path := filepath.Join(dirs.Config, Filename)
	if stored != "" {
		writeFile(t, path, stored)
	}
	return dirs, path
}

func TestReplace_RunsTheSameChecksAsThePatch(t *testing.T) {
	cases := []struct {
		name, key, raw string
	}{
		{name: "compaction", key: KeyAutoCompactPct, raw: `7`},
		{name: "trigger cap", key: keyTriggerMaxLiveRuns, raw: `0`},
		{name: "ignore entry", key: KeyAgentIgnoreFiles, raw: `["../x"]`},
		{name: "additional directory", key: KeyAdditionalDirectories},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, path := replaceFixture(t, `{"theme":"dark"}`)
			raw := tc.raw
			if raw == "" {
				b, _ := json.Marshal([]string{filepath.Join(dirs.Config, "sub")})
				raw = string(b)
			}
			patch := map[string]json.RawMessage{tc.key: json.RawMessage(raw)}
			want := CheckPatch(dirs, map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`)}, patch)
			if want == nil {
				t.Fatalf("CheckPatch(%s: %s) = nil, want the PATCH's refusal", tc.key, raw)
			}
			content, _ := json.Marshal(map[string]json.RawMessage{KeyTheme: json.RawMessage(`"dark"`), tc.key: json.RawMessage(raw)})
			_, _, err := Replace(t.Context(), dirs, content, nil, nil)
			if !errors.Is(err, ErrRefused) || err.Error() != want.Error() {
				t.Errorf("Replace = %v, want ErrRefused with %q", err, want)
			}
			if got := fileBytes(t, path); string(got) != `{"theme":"dark"}` {
				t.Errorf("config.json = %s after a refusal, want it unchanged", got)
			}
		})
	}
	t.Run("an unchanged invalid stored value does not refuse another key", func(t *testing.T) {
		dirs, path := replaceFixture(t, `{"auto_compact_pct":7,"theme":"dark"}`)
		if _, _, err := Replace(t.Context(), dirs, []byte(`{"auto_compact_pct":7,"theme":"lite"}`), nil, nil); err != nil {
			t.Fatalf("Replace = %v, want nil", err)
		}
		if doc := readConfig(t, filepath.Dir(path)); string(doc[KeyTheme]) != `"lite"` {
			t.Errorf("theme = %s, want lite", doc[KeyTheme])
		}
	})
	t.Run("content that is not one object", func(t *testing.T) {
		dirs, _ := replaceFixture(t, `{"theme":"dark"}`)
		for _, content := range []string{`null`, `[]`, `{"theme":`} {
			if _, _, err := Replace(t.Context(), dirs, []byte(content), nil, nil); !errors.Is(err, ErrRefused) {
				t.Errorf("Replace(%s) = %v, want ErrRefused", content, err)
			}
		}
	})
}

func TestReplace_StampsTheNextVersionWhateverTheBufferSays(t *testing.T) {
	dirs, _ := replaceFixture(t, `{"chat_retention_days":-1,"settings_epoch":5,"settings_revision":3,"theme":"dark"}`)
	var ev events
	written, changed, err := Replace(t.Context(), dirs,
		[]byte(`{"settings_epoch":5,"settings_revision":999999,"theme":"lite"}`), nil, ev.publish)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	doc := readConfig(t, dirs.Config)
	got := stampOf(doc)
	if got.revision == 999999 || !(stamp{epoch: 5, revision: 3}).below(got) {
		t.Errorf("stamp = %+v, want the server's next version above 5/3", got)
	}
	if want := []marotte.SettingsUpdatedPayload{payloadOf(got)}; !slices.Equal(ev.got, want) {
		t.Errorf("published %v, want %v", ev.got, want)
	}
	if !bytes.Equal(written, fileBytes(t, filepath.Join(dirs.Config, Filename))) {
		t.Errorf("written = %s, want the file's bytes", written)
	}
	if want := []string{KeyChatRetentionDays, KeyTheme}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v (a removed key is changed)", changed, want)
	}
}

func TestReplace_RepairsAnUnparseableFile(t *testing.T) {
	dirs, path := replaceFixture(t, `{not json`)
	if _, _, err := Replace(t.Context(), dirs, []byte(`{"auto_compact_pct":7}`), nil, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("Replace of a bad value over an unparseable file = %v, want ErrRefused (every key is checked)", err)
	}
	if _, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"dark"}`), nil, nil); err != nil {
		t.Fatalf("Replace = %v, want the repair written", err)
	}
	if doc := readConfig(t, filepath.Dir(path)); string(doc[KeyTheme]) != `"dark"` {
		t.Errorf("config.json = %v, want the repaired document", doc)
	}
}

// Over a refused stored file the keys that moved are unknown, so the repair answers nil.
func TestReplace_AnswersNilKeysOverARefusedStoredFile(t *testing.T) {
	dirs, _ := replaceFixture(t, `{not json`)
	_, changed, err := Replace(t.Context(), dirs, []byte(`{"theme":"dark"}`), nil, nil)
	if err != nil {
		t.Fatalf("Replace = %v, want the repair written", err)
	}
	if changed != nil {
		t.Errorf("changed = %q over a refused stored file, want nil", changed)
	}
}

func TestReplace_ChecksTheWholeFileOverTheReadCap(t *testing.T) {
	big := `{"pad":"` + strings.Repeat("a", maxBytes) + `"}`
	cases := []struct {
		prepare func(t *testing.T, dirs AgentDirs, path string)
		name    string
	}{
		{name: "a refused read", prepare: func(t *testing.T, _ AgentDirs, path string) { writeFile(t, path, big) }},
		{name: "a read held over an applied document", prepare: func(t *testing.T, dirs AgentDirs, path string) {
			if _, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"dark"}`), nil, nil); err != nil {
				t.Fatalf("Setup: Replace: %v", err)
			}
			writeFile(t, path, big)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, path := replaceFixture(t, "")
			tc.prepare(t, dirs, path)
			var saw []byte
			_, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"lite"}`), func(current []byte) error {
				saw = current
				return nil
			}, nil)
			if err != nil {
				t.Fatalf("Replace = %v, want the write", err)
			}
			if string(saw) != big {
				t.Errorf("check saw %d bytes, want the whole %d-byte file", len(saw), len(big))
			}
		})
	}
}

func TestReplace_WaitsForAnUpdateInFlight(t *testing.T) {
	dirs, path := replaceFixture(t, `{"theme":"dark"}`)
	before := fileBytes(t, path)
	entered, release := make(chan struct{}), make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		_, err := Update(t.Context(), dirs.Config, func(doc map[string]json.RawMessage) error {
			close(entered)
			<-release
			doc[KeyDebugLogs] = json.RawMessage(`true`)
			return nil
		}, nil)
		updated <- err
	}()
	<-entered
	errStale := errors.New("stale")
	replaced := make(chan error, 1)
	go func() {
		_, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"lite"}`), func(current []byte) error {
			if !bytes.Equal(current, before) {
				return errStale
			}
			return nil
		}, nil)
		replaced <- err
	}()
	select {
	case err := <-replaced:
		t.Fatalf("Replace returned %v while an Update held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-updated; err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := <-replaced; !errors.Is(err, errStale) {
		t.Errorf("Replace = %v, want its check's error", err)
	}
	if doc := readConfig(t, dirs.Config); string(doc[KeyDebugLogs]) != `true` || string(doc[KeyTheme]) != `"dark"` {
		t.Errorf("config.json = %v, want the Update's write", doc)
	}
}

// failReads makes every config.json read fail as an I/O error on the mount does, regular file and all.
func failReads(t *testing.T) {
	t.Helper()
	real := classify
	classify = func(path string) (ownedfile.Read, []byte, os.FileInfo, error) {
		err := &fs.PathError{Op: "read", Path: path, Err: syscall.EIO}
		return ownedfile.Read{Kind: ownedfile.Unreadable, Key: "error " + err.Error()}, nil, nil, err
	}
	t.Cleanup(func() { classify = real })
}

func TestReplace_AFileItCannotReadRefusesACheckedSave(t *testing.T) {
	cases := []struct {
		prepare func(t *testing.T, dirs AgentDirs)
		name    string
	}{
		{name: "nothing applied", prepare: func(*testing.T, AgentDirs) {}},
		{name: "held over an applied document", prepare: func(t *testing.T, dirs AgentDirs) {
			if _, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"dark"}`), nil, nil); err != nil {
				t.Fatalf("Setup: Replace: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, path := replaceFixture(t, `{"theme":"dark"}`)
			tc.prepare(t, dirs)
			before := fileBytes(t, path)
			failReads(t)
			checked := false
			_, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"lite"}`), func([]byte) error {
				checked = true
				return nil
			}, nil)
			if !errors.Is(err, ErrUnreadable) || !errors.Is(err, syscall.EIO) {
				t.Errorf("Replace = %v, want ErrUnreadable wrapping the read's EIO", err)
			}
			if checked {
				t.Error("check ran over a file that could not be read")
			}
			if got := fileBytes(t, path); !bytes.Equal(got, before) {
				t.Errorf("config.json = %s after the refusal, want it unchanged %s", got, before)
			}
			if _, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"lite"}`), nil, nil); err != nil {
				t.Errorf("Replace with no check = %v, want the write", err)
			}
		})
	}
}

func TestReplace_ACheckSeesNilForAnAbsentOrNonRegularFile(t *testing.T) {
	cases := []struct {
		plant func(t *testing.T, path string)
		name  string
	}{
		{name: "absent", plant: func(*testing.T, string) {}},
		{name: "directory", plant: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, path := replaceFixture(t, "")
			tc.plant(t, path)
			checked := false
			_, _, err := Replace(t.Context(), dirs, []byte(`{"theme":"lite"}`), func(current []byte) error {
				checked = true
				if current != nil {
					t.Errorf("check saw %q, want nil", current)
				}
				return nil
			}, nil)
			if !checked {
				t.Errorf("check did not run (Replace = %v)", err)
			}
			if tc.name == "absent" && err != nil {
				t.Errorf("Replace over an absent file = %v, want the write", err)
			}
		})
	}
}
