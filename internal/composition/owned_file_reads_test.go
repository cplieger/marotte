package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/ownedfile"
	"github.com/cplieger/marotte/internal/policyfile"
	"github.com/cplieger/marotte/internal/securityprofile"
	"github.com/cplieger/marotte/internal/settings"
	"github.com/cplieger/toolbelt/v3"
)

// ownedReadsFixture is config.json as every config.json cell and reader row starts from: a stored -1 (keep every
// chat), a theme and a non-default profile, written through settings.Update so it is stamped and applied.
var ownedReadsFixture = map[string]string{
	settings.KeyChatRetentionDays: `-1`,
	settings.KeySecurityProfile:   `"trusted"`,
	settings.KeyTheme:             `"dark"`,
}

// The truncated documents the unparseable cells write.
const (
	truncatedConfig   = `{"theme":`
	truncatedManifest = `{"version":`
)

const chatRetentionErrorMsg = "chat retention: config.json cannot be read"

// configReaders is what the config.json readers outside the settings package answer. theme is the settings GET's
// stored theme ("" for none); refused means the GET serves the defaults because the stored document was refused.
type configReaders struct {
	profile        string
	theme          string
	retention      time.Duration
	runEnabled     bool
	retentionError bool
	refused        bool
}

var (
	// appliedReaders answer ownedReadsFixture.
	appliedReaders = configReaders{profile: policyfile.ProfileTrusted, theme: `"dark"`}
	// seededReaders answer the seed a settled absence writes: the defaults.
	seededReaders = configReaders{
		profile: policyfile.DefaultProfile, retention: settings.DefaultChatRetentionDays * 24 * time.Hour, runEnabled: true,
	}
	// refusedReaders answer a config.json marotte refused.
	refusedReaders = configReaders{profile: policyfile.DefaultProfile, retentionError: true, refused: true}
)

func readConfigReaders(t *testing.T, dir string, logs *bytes.Buffer) configReaders {
	t.Helper()
	before := logCount(t, logs, "ERROR", chatRetentionErrorMsg)
	r := configReaders{retention: chatRetention(t.Context(), dir)}
	r.retentionError = logCount(t, logs, "ERROR", chatRetentionErrorMsg) > before
	r.runEnabled = runRetention(t.Context(), dir).Enabled
	r.profile = (&securityprofile.Store{ConfigDir: dir}).Active(t.Context())
	stored, err := settings.Stored(dir)
	r.refused = err != nil
	r.theme = string(stored[settings.KeyTheme])
	return r
}

// logCount counts the JSON log records at level whose message starts with prefix.
func logCount(t *testing.T, logs *bytes.Buffer, level, prefix string) int {
	t.Helper()
	n := 0
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec struct {
			Msg   string `json:"msg"`
			Level string `json:"level"`
		}
		if err := dec.Decode(&rec); err != nil {
			return n
		}
		if rec.Level == level && strings.HasPrefix(rec.Msg, prefix) {
			n++
		}
	}
}

// pollConfig is one pass of the config.json watch's outside-write check, with no runtime behind it.
func pollConfig(t *testing.T, dir string) {
	t.Helper()
	if _, _, err := settings.Poll(context.WithoutCancel(t.Context()), dir, func(context.Context, marotte.ServerEvent) {}); err != nil {
		t.Fatalf("Setup: settings.Poll: %v", err)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// renameAway moves path aside as an editor's rename-away save does before it creates the file again.
func renameAway(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+"~"); err != nil {
		t.Fatal(err)
	}
}

func absent(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

// editorGet reads path through the editor's read and answers its status and the content it served.
func (fx *ownedFilesFixture) editorGet(t *testing.T, path string) (code int, content string) {
	t.Helper()
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file?path="+url.QueryEscape(path), nil))
	var read filebrowse.FileRead
	_ = json.Unmarshal(rec.Body.Bytes(), &read)
	return rec.Code, read.Content
}

// configCounts is what the config.json owner has told the runtime and logged so far.
type configCounts struct {
	events  []marotte.SettingsUpdatedPayload
	written [][]string
	warns   int
}

func (fx *ownedFilesFixture) configCounts(t *testing.T) configCounts {
	t.Helper()
	return configCounts{
		events:  fx.reactor.settingsStamps(),
		written: fx.reactor.writtenKeys(),
		warns:   configWarns(t, fx.logs, fx.configPath()),
	}
}

// sameCounts reports whether nothing more was published, applied or warned since before.
func sameCounts(before, after configCounts) bool {
	return slices.Equal(before.events, after.events) && len(before.written) == len(after.written) && before.warns == after.warns
}

// onlyStampKeys reports whether data is a document holding the two stamp keys and nothing else.
func onlyStampKeys(t *testing.T, data []byte) bool {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}
	return slices.Equal(slices.Sorted(maps.Keys(doc)), []string{settings.KeySettingsEpoch, settings.KeySettingsRevision})
}

// fixtureKeys are the keys ownedReadsFixture holds besides its stamp, sorted.
func fixtureKeys() []string {
	return slices.Sorted(maps.Keys(ownedReadsFixture))
}

func TestOwnedFileReads(t *testing.T) {
	t.Run("cells", func(t *testing.T) {
		t.Run("config.json", testConfigJSONCells)
		t.Run("tools.json", testToolsJSONCells)
	})
	t.Run("reader", func(t *testing.T) {
		t.Run("config.json", testConfigJSONReaders)
		t.Run("tools.json", testToolsJSONReaders)
	})
	t.Run("seam", testConfigJSONSeam)
}

func testConfigJSONCells(t *testing.T) {
	t.Run("up/mid-save rename", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seeded := seedConfig(t, fx.configDir, ownedReadsFixture)
		renameAway(t, fx.configPath())

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !absent(fx.configPath()) || !sameCounts(configCounts{}, c) {
			t.Errorf("after tick 1: absent %v, told %+v; want still absent and nothing published, applied or warned", absent(fx.configPath()), c)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != appliedReaders {
			t.Errorf("readers after tick 1 = %+v, want the applied document's %+v", got, appliedReaders)
		}
		person := handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })

		fx.watch.pass(t.Context())
		if disk, _ := os.ReadFile(fx.configPath()); !bytes.Equal(disk, person) {
			t.Errorf("config.json after tick 2 = %q, want the person's bytes %q", disk, person)
		}
		after2 := fx.configCounts(t)
		if !slices.Equal(after2.events, []marotte.SettingsUpdatedPayload{stampOfConfig(t, person)}) {
			t.Errorf("settings_updated events after tick 2 = %+v, want one at the person's stamp", after2.events)
		}
		if len(after2.written) != 1 || !slices.Equal(after2.written[0], []string{settings.KeyTheme}) {
			t.Errorf("SettingsWritten calls after tick 2 = %q, want one with [theme]", after2.written)
		}

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(after2, c) {
			t.Errorf("tick 3 told %+v, want nothing more than after tick 2", c)
		}
	})
	t.Run("up/removed", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seeded := stampOfConfig(t, seedConfig(t, fx.configDir, ownedReadsFixture))
		removeFile(t, fx.configPath())

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !absent(fx.configPath()) || !sameCounts(configCounts{}, c) {
			t.Errorf("after tick 1: absent %v, told %+v; want still absent and nothing told", absent(fx.configPath()), c)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != appliedReaders {
			t.Errorf("readers after tick 1 = %+v, want the applied document's %+v", got, appliedReaders)
		}

		fx.watch.pass(t.Context())
		disk, err := os.ReadFile(fx.configPath())
		if err != nil || !onlyStampKeys(t, disk) {
			t.Fatalf("config.json after tick 2 = %q (%v), want a document of the stamp keys alone", disk, err)
		}
		stamp := stampOfConfig(t, disk)
		if !stampAbove(stamp, seeded) {
			t.Errorf("the seed's stamp %+v, want above the fixture's %+v", stamp, seeded)
		}
		after2 := fx.configCounts(t)
		if !slices.Equal(after2.events, []marotte.SettingsUpdatedPayload{stamp}) {
			t.Errorf("settings_updated events after tick 2 = %+v, want one at the seed's stamp %+v", after2.events, stamp)
		}
		if len(after2.written) != 1 || !slices.Equal(after2.written[0], fixtureKeys()) {
			t.Errorf("SettingsWritten calls after tick 2 = %q, want one with every key the fixture held %q", after2.written, fixtureKeys())
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != seededReaders {
			t.Errorf("readers after tick 2 = %+v, want the defaults %+v", got, seededReaders)
		}

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(after2, c) {
			t.Errorf("tick 3 told %+v, want nothing more than after tick 2", c)
		}
	})
	t.Run("up/unparseable", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seedConfig(t, fx.configDir, ownedReadsFixture)
		writeConfig(t, fx.configPath(), truncatedConfig)

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(configCounts{}, c) {
			t.Errorf("tick 1 told %+v, want nothing", c)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != appliedReaders {
			t.Errorf("readers after tick 1 = %+v, want the applied document's %+v", got, appliedReaders)
		}
		if err := updateDebugLogs(t, fx.configDir); !errors.Is(err, settings.ErrUnreadable) {
			t.Errorf("Update of another key after tick 1 = %v, want ErrUnreadable", err)
		}
		assertBytes(t, fx.configPath(), truncatedConfig, "after tick 1 and a refused Update")

		fx.watch.pass(t.Context())
		assertBytes(t, fx.configPath(), truncatedConfig, "after tick 2")
		after2 := fx.configCounts(t)
		if !slices.Equal(after2.events, []marotte.SettingsUpdatedPayload{{}}) {
			t.Errorf("settings_updated events after tick 2 = %+v, want one at the zero stamp", after2.events)
		}
		if len(after2.written) != 1 || after2.written[0] != nil {
			t.Errorf("SettingsWritten calls after tick 2 = %q, want one with nil keys", after2.written)
		}
		if after2.warns != 1 {
			t.Errorf("Warns after tick 2 = %d, want 1", after2.warns)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != refusedReaders {
			t.Errorf("readers after tick 2 = %+v, want the refusal's %+v", got, refusedReaders)
		}

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(after2, c) {
			t.Errorf("tick 3 told %+v, want nothing more (no further Warn)", c)
		}
	})
	t.Run("up/mid-save rename from an adopted unparseable file", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seeded := seedConfig(t, fx.configDir, ownedReadsFixture)
		adoptUnparseable(t, fx.configDir)
		fx.watch.pass(t.Context())
		before := fx.configCounts(t)
		renameAway(t, fx.configPath())

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !absent(fx.configPath()) || !sameCounts(before, c) {
			t.Errorf("after tick 1: absent %v, told %+v; want still absent and nothing more than %+v", absent(fx.configPath()), c, before)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != refusedReaders {
			t.Errorf("readers after tick 1 = %+v, want the refusal's %+v", got, refusedReaders)
		}
		assertReadRefused(t, fx.configDir)
		if err := updateDebugLogs(t, fx.configDir); !errors.Is(err, settings.ErrUnreadable) || !absent(fx.configPath()) {
			t.Errorf("Update of another key after tick 1 = %v, absent %v; want ErrUnreadable and no file created", err, absent(fx.configPath()))
		}
		person := handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })
		if got, want := readConfigReaders(t, fx.configDir, fx.logs), (configReaders{profile: policyfile.ProfileTrusted, theme: `"light"`}); got != want {
			t.Errorf("readers after the create = %+v, want the person's document %+v (a valid read acts at once)", got, want)
		}

		fx.watch.pass(t.Context())
		if disk, _ := os.ReadFile(fx.configPath()); !bytes.Equal(disk, person) {
			t.Errorf("config.json after tick 2 = %q, want the person's bytes %q", disk, person)
		}
		after2 := fx.configCounts(t)
		if want := append(slices.Clone(before.events), stampOfConfig(t, person)); !slices.Equal(after2.events, want) {
			t.Errorf("settings_updated events after tick 2 = %+v, want %+v (one more, at the person's stamp)", after2.events, want)
		}
		if len(after2.written) != len(before.written)+1 || after2.written[len(after2.written)-1] != nil {
			t.Errorf("SettingsWritten calls after tick 2 = %q, want one more with nil keys (the applied read was refused)", after2.written)
		}

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(after2, c) {
			t.Errorf("tick 3 told %+v, want nothing more than after tick 2", c)
		}
	})
	t.Run("up/removed from an adopted unparseable file", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seedConfig(t, fx.configDir, ownedReadsFixture)
		adoptUnparseable(t, fx.configDir)
		fx.watch.pass(t.Context())
		before := fx.configCounts(t)
		removeFile(t, fx.configPath())

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !absent(fx.configPath()) || !sameCounts(before, c) {
			t.Errorf("after tick 1: absent %v, told %+v; want still absent and nothing more", absent(fx.configPath()), c)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != refusedReaders {
			t.Errorf("readers after tick 1 = %+v, want the refusal's %+v", got, refusedReaders)
		}

		fx.watch.pass(t.Context())
		disk, err := os.ReadFile(fx.configPath())
		if err != nil || !onlyStampKeys(t, disk) {
			t.Fatalf("config.json after tick 2 = %q (%v), want a document of the stamp keys alone", disk, err)
		}
		after2 := fx.configCounts(t)
		if want := append(slices.Clone(before.events), stampOfConfig(t, disk)); !slices.Equal(after2.events, want) {
			t.Errorf("settings_updated events after tick 2 = %+v, want %+v (one more, at the seed's stamp)", after2.events, want)
		}
		if len(after2.written) != len(before.written)+1 || after2.written[len(after2.written)-1] != nil {
			t.Errorf("SettingsWritten calls after tick 2 = %q, want one more with nil keys (the applied read was refused)", after2.written)
		}
		if got := readConfigReaders(t, fx.configDir, fx.logs); got != seededReaders {
			t.Errorf("readers after tick 2 = %+v, want the defaults %+v", got, seededReaders)
		}

		fx.watch.pass(t.Context())
		if c := fx.configCounts(t); !sameCounts(after2, c) {
			t.Errorf("tick 3 told %+v, want nothing more than after tick 2", c)
		}
	})
	for _, event := range []struct {
		apply    func(t *testing.T, path string)
		name     string
		recreate bool // the person's editor creates the file again after the ticks
	}{
		{name: "mid-save rename", apply: renameAway, recreate: true},
		{name: "removed", apply: removeFile},
		{name: "unparseable", apply: func(t *testing.T, path string) {
			t.Helper()
			writeConfig(t, path, truncatedConfig)
		}},
	} {
		t.Run("down/"+event.name, func(t *testing.T) {
			fx := newOwnedFilesFixture(t)
			fx.reactor.draining = true
			seeded := seedConfig(t, fx.configDir, ownedReadsFixture)
			event.apply(t, fx.configPath())
			left, _ := os.ReadFile(fx.configPath())

			for tick := 1; tick <= 3; tick++ {
				fx.watch.pass(t.Context())
				if disk, _ := os.ReadFile(fx.configPath()); !bytes.Equal(disk, left) {
					t.Errorf("config.json after tick %d = %q, want %q as the event left it", tick, disk, left)
				}
				if c := fx.configCounts(t); !sameCounts(configCounts{}, c) {
					t.Errorf("tick %d told %+v, want nothing (no pass polls while the runtime drains)", tick, c)
				}
				if got := readConfigReaders(t, fx.configDir, fx.logs); got != appliedReaders {
					t.Errorf("readers after tick %d = %+v, want the applied document's %+v", tick, got, appliedReaders)
				}
			}
			if !event.recreate {
				return
			}
			handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })
			if got, want := readConfigReaders(t, fx.configDir, fx.logs), (configReaders{profile: policyfile.ProfileTrusted, theme: `"light"`}); got != want {
				t.Errorf("readers after the create = %+v, want the person's document %+v (a valid read acts at once)", got, want)
			}
		})
	}
}

// adoptUnparseable writes a truncated config.json and settles it over two passes, so the owner refused it.
func adoptUnparseable(t *testing.T, dir string) {
	t.Helper()
	writeConfig(t, filepath.Join(dir, settings.Filename), truncatedConfig)
	pollConfig(t, dir)
	pollConfig(t, dir)
}

// assertReadRefused checks settings.Read answers ErrUnparseable under one generation, never the unordered 0 an
// unreadable read with no bytes gets, so the agent's last readable values stay held.
func assertReadRefused(t *testing.T, dir string) {
	t.Helper()
	_, gen, err := settings.Read(t.Context(), dir)
	_, again, errAgain := settings.Read(t.Context(), dir)
	if !errors.Is(err, settings.ErrUnparseable) || gen == 0 || !errors.Is(errAgain, settings.ErrUnparseable) || again != gen {
		t.Errorf("settings.Read = gen %d (%v), then gen %d (%v); want ErrUnparseable twice under one generation above 0", gen, err, again, errAgain)
	}
}

func updateDebugLogs(t *testing.T, dir string) error {
	t.Helper()
	_, err := settings.Update(t.Context(), dir, func(doc map[string]json.RawMessage) error {
		doc[settings.KeyDebugLogs] = json.RawMessage(`true`)
		return nil
	}, nil)
	return err
}

func assertBytes(t *testing.T, path, want, when string) {
	t.Helper()
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Errorf("%s %s = %q, want %q unchanged", filepath.Base(path), when, got, want)
	}
}

// configState is one read state config.json's readers are asked in; plant leaves dir in it from the applied fixture.
type configState struct {
	plant func(t *testing.T, dir string)
	name  string
}

func configStates() []configState {
	held := func(edit func(t *testing.T, path string)) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			t.Helper()
			edit(t, filepath.Join(dir, settings.Filename))
			pollConfig(t, dir)
		}
	}
	truncate := func(t *testing.T, path string) {
		t.Helper()
		writeConfig(t, path, truncatedConfig)
	}
	return []configState{
		{name: "held absence", plant: held(removeFile)},
		{name: "held unparseable", plant: held(truncate)},
		{name: "adopted absence", plant: func(t *testing.T, dir string) {
			t.Helper()
			removeFile(t, filepath.Join(dir, settings.Filename))
			pollConfig(t, dir)
			pollConfig(t, dir)
		}},
		{name: "adopted unparseable", plant: adoptUnparseable},
		{name: "held absence after an adopted unparseable read", plant: func(t *testing.T, dir string) {
			t.Helper()
			adoptUnparseable(t, dir)
			removeFile(t, filepath.Join(dir, settings.Filename))
		}},
	}
}

// configStateWant is what each reader answers in one configState, from "Every reader, through the rule".
type configStateWant struct {
	// update is the document an Update of debug_logs leaves, key by key ("" for a key it lacks); nil when it refuses.
	update     map[string]string
	theme      string // settings.Read's theme
	readers    configReaders
	editorCode int
	readErr    bool // settings.Read answers ErrUnparseable
}

func configStateWants() map[string]configStateWant {
	merged := map[string]string{settings.KeyTheme: `"dark"`, settings.KeyChatRetentionDays: `-1`, settings.KeyDebugLogs: `true`}
	return map[string]configStateWant{
		"held absence":     {readers: appliedReaders, theme: "dark", update: merged, editorCode: http.StatusNotFound},
		"held unparseable": {readers: appliedReaders, theme: "dark", editorCode: http.StatusOK},
		"adopted absence": {
			readers: seededReaders, editorCode: http.StatusOK,
			update: map[string]string{settings.KeyTheme: "", settings.KeyChatRetentionDays: "", settings.KeyDebugLogs: `true`},
		},
		"adopted unparseable":                            {readers: refusedReaders, readErr: true, editorCode: http.StatusOK},
		"held absence after an adopted unparseable read": {readers: refusedReaders, readErr: true, editorCode: http.StatusNotFound},
	}
}

func testConfigJSONReaders(t *testing.T) {
	wants := configStateWants()
	for _, state := range configStates() {
		want, ok := wants[state.name]
		if !ok {
			t.Fatalf("Setup: no expected answers for %q", state.name)
		}
		t.Run(state.name, func(t *testing.T) {
			fx := newOwnedFilesFixture(t)
			seedConfig(t, fx.configDir, ownedReadsFixture)
			state.plant(t, fx.configDir)
			before, _ := os.ReadFile(fx.configPath())
			wasAbsent := absent(fx.configPath())

			t.Run("chatRetention, runRetention, securityprofile.Active and the settings GET", func(t *testing.T) {
				if got := readConfigReaders(t, fx.configDir, fx.logs); got != want.readers {
					t.Errorf("readers = %+v, want %+v", got, want.readers)
				}
			})
			t.Run("settings.Read", func(t *testing.T) {
				if want.readErr {
					assertReadRefused(t, fx.configDir)
					return
				}
				doc, gen, err := settings.Read(t.Context(), fx.configDir)
				again, genAgain, errAgain := settings.Read(t.Context(), fx.configDir)
				if err != nil || settings.LookupOr(doc, settings.KeyTheme, "") != want.theme {
					t.Errorf("Read = theme %q, %v; want %q", settings.LookupOr(doc, settings.KeyTheme, ""), err, want.theme)
				}
				if errAgain != nil || genAgain != gen || settings.LookupOr(again, settings.KeyTheme, "") != want.theme {
					t.Errorf("a second Read = gen %d (%v), want the first's generation %d and document", genAgain, errAgain, gen)
				}
			})
			t.Run("the editor GET", func(t *testing.T) {
				code, content := fx.editorGet(t, fx.configPath())
				if code != want.editorCode || code == http.StatusOK && content != string(before) {
					t.Errorf("editor GET = %d %q, want %d with the bytes on disk %q", code, content, want.editorCode, before)
				}
			})
			t.Run("settings.Update", func(t *testing.T) {
				err := updateDebugLogs(t, fx.configDir)
				if want.update == nil {
					after, _ := os.ReadFile(fx.configPath())
					if !errors.Is(err, settings.ErrUnreadable) || !bytes.Equal(after, before) || absent(fx.configPath()) != wasAbsent {
						t.Errorf("Update = %v, config.json %q after; want ErrUnreadable and %q unchanged (no file created)", err, after, before)
					}
					return
				}
				if err != nil {
					t.Fatalf("Update = %v, want it written", err)
				}
				stored, err := settings.Stored(fx.configDir)
				if err != nil {
					t.Fatalf("Stored after the Update: %v", err)
				}
				for key, v := range want.update {
					if got := string(stored[key]); got != v {
						t.Errorf("config.json after the Update: %s = %q, want %q", key, got, v)
					}
				}
			})
		})
	}
	t.Run("chatRetention/a held read within one tick of a valid outside write", func(t *testing.T) {
		fx := newOwnedFilesFixture(t)
		seeded := seedConfig(t, fx.configDir, ownedReadsFixture)
		handEdit(t, fx.configDir, seeded, func(d map[string]json.RawMessage) { d[settings.KeyChatRetentionDays] = json.RawMessage(`3`) })
		if got := chatRetention(t.Context(), fx.configDir); got != 72*time.Hour {
			t.Fatalf("Setup: chatRetention over the outside write = %v, want 72h (a valid read acts at once)", got)
		}
		renameAway(t, fx.configPath())
		pollConfig(t, fx.configDir)

		if got := chatRetention(t.Context(), fx.configDir); got != 0 {
			t.Errorf("chatRetention over a held absence before a pass applied the outside write = %v, want 0, the -1 applied before it", got)
		}
	})
	t.Run("settings.Read/each move of the applied record answers a later generation", testAppliedGenerations)
}

// testAppliedGenerations moves config.json's applied record by each of its writers in turn and checks every move
// answers a generation above every earlier one, and that no generation answers two documents.
func testAppliedGenerations(t *testing.T) {
	fx := newOwnedFilesFixture(t)
	docs := map[uint64]string{}
	var last uint64
	read := func(t *testing.T, move string) uint64 {
		t.Helper()
		doc, gen, err := settings.Read(t.Context(), fx.configDir)
		if err != nil {
			t.Fatalf("Read after %s: %v", move, err)
		}
		body := fmt.Sprintf("theme %q, debug_logs %v", settings.LookupOr(doc, settings.KeyTheme, ""),
			settings.LookupOr(doc, settings.KeyDebugLogs, false))
		if seen, ok := docs[gen]; ok && seen != body {
			t.Errorf("generation %d answered %s after %s, and %s before", gen, body, move, seen)
		}
		docs[gen] = body
		return gen
	}
	moved := func(t *testing.T, move string) {
		t.Helper()
		gen := read(t, move)
		if gen <= last {
			t.Errorf("generation after %s = %d, want above every earlier one (%d)", move, gen, last)
		}
		last = gen
		heldGen := read(t, move+", read again")
		if heldGen != gen {
			t.Errorf("a second read after %s = generation %d, want %d", move, heldGen, gen)
		}
	}

	writeConfig(t, fx.configPath(), `{"theme":"dark"}`)
	moved(t, "the first read")
	if err := updateDebugLogs(t, fx.configDir); err != nil {
		t.Fatal(err)
	}
	moved(t, "an Update")
	current, _ := os.ReadFile(fx.configPath())
	replaced := handEditBytes(t, current, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"light"`) })
	if _, _, err := settings.Replace(t.Context(), settings.AgentDirs{Config: fx.configDir}, replaced, nil, fx.reactor.Broadcast); err != nil {
		t.Fatal(err)
	}
	moved(t, "a Replace")
	current, _ = os.ReadFile(fx.configPath())
	handEdit(t, fx.configDir, current, func(d map[string]json.RawMessage) { d[settings.KeyTheme] = json.RawMessage(`"dim"`) })
	read(t, "an outside write")
	pollConfig(t, fx.configDir)
	renameAway(t, fx.configPath())
	pollConfig(t, fx.configDir)
	held := read(t, "Poll's record, while a later absence is held")
	if held <= last {
		t.Errorf("generation of the held absence after Poll's record = %d, want above every earlier move (%d)", held, last)
	}
	last = held
	pollConfig(t, fx.configDir)
	moved(t, "the seed write")
}

// handEditBytes is from's document with edit applied.
func handEditBytes(t *testing.T, from []byte, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(from, &doc); err != nil {
		t.Fatalf("Setup: parse %q: %v", from, err)
	}
	edit(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// configSeamShapes are first reads of config.json: each is the first read this process makes, so it is adopted as
// it is, and one rule decides which of them is one JSON object.
type configSeamShape struct {
	body     []byte
	name     string
	readable bool
	absent   bool
	dir      bool
}

func configSeamShapes() []configSeamShape {
	object := []byte(`{"chat_retention_days":3,"security_profile":"trusted"}`)
	bom := []byte("\xef\xbb\xbf")
	return []configSeamShape{
		{name: "absent", absent: true},
		{name: "directory", dir: true},
		{name: "empty file", body: []byte{}},
		{name: "null", body: []byte("null\n")},
		{name: "null after a BOM", body: append(slices.Clone(bom), "null"...)},
		{name: "array", body: []byte(`[{"theme":"dark"}]`)},
		{name: "string", body: []byte(`"dark"`)},
		{name: "number", body: []byte(`7`)},
		{name: "truncated", body: []byte(truncatedConfig)},
		{name: "over the cap with a valid prefix", body: append(slices.Clone(object), bytes.Repeat([]byte(" "), 1<<20)...)},
		{name: "valid after a BOM", body: append(slices.Clone(bom), object...), readable: true},
		{name: "valid", body: object, readable: true},
	}
}

func testConfigJSONSeam(t *testing.T) {
	readable := configReaders{profile: policyfile.ProfileTrusted, retention: 72 * time.Hour, runEnabled: true}
	for _, shape := range configSeamShapes() {
		t.Run(shape.name, func(t *testing.T) {
			dir := t.TempDir()
			logs := captureDefaultLogger(t)
			path := filepath.Join(dir, settings.Filename)
			switch {
			case shape.dir:
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case !shape.absent:
				if err := os.WriteFile(path, shape.body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := refusedReaders
			switch {
			case shape.absent:
				want = seededReaders
			case shape.readable:
				want = readable
			}
			if got := readConfigReaders(t, dir, logs); got != want {
				t.Errorf("readers = %+v, want %+v", got, want)
			}
		})
	}
}

// toolsState is one read state tools.json's readers are asked in, from the engine up over installedTools.
type toolsState struct {
	plant  func(t *testing.T, fx *ownedFilesFixture)
	name   string
	down   bool // marotte has taken the engine down by then
	absent bool // tools.json is absent by then
}

func toolsStates() []toolsState {
	ticks := func(n int, edit func(t *testing.T, fx *ownedFilesFixture)) func(*testing.T, *ownedFilesFixture) {
		return func(t *testing.T, fx *ownedFilesFixture) {
			t.Helper()
			edit(t, fx)
			for range n {
				fx.tools.tick()
			}
		}
	}
	remove := func(t *testing.T, fx *ownedFilesFixture) {
		t.Helper()
		removeFile(t, fx.manifest)
	}
	truncate := func(t *testing.T, fx *ownedFilesFixture) {
		t.Helper()
		writeConfig(t, fx.manifest, truncatedManifest)
	}
	return []toolsState{
		{name: "held absence", plant: ticks(1, remove)},
		{name: "held unparseable", plant: ticks(1, truncate)},
		{name: "adopted absence", plant: ticks(2, remove)},
		{name: "adopted unparseable", plant: ticks(2, truncate), down: true},
		{name: "held absence after an adopted unparseable read", down: true, absent: true, plant: func(t *testing.T, fx *ownedFilesFixture) {
			t.Helper()
			ticks(2, truncate)(t, fx)
			ticks(1, remove)(t, fx)
		}},
	}
}

// toolsStateWant is what the engine's readers answer in one toolsState, from "Every reader, through the rule".
type toolsStateWant struct {
	inventory []string // the rows Inventory lists
	afterAdd  []string // tools.json's rows after an Add of c; nil when the Add refuses
	keepsB    bool     // b's binary is still installed after a Reconcile runs to completion
}

func testToolsJSONReaders(t *testing.T) {
	wants := map[string]toolsStateWant{
		"held absence":     {inventory: []string{"a", "b"}, keepsB: true, afterAdd: []string{"a", "b", "c"}},
		"held unparseable": {inventory: []string{"a", "b"}, keepsB: true},
		"adopted absence":  {inventory: []string{"a"}, afterAdd: []string{"a", "c"}},
	}
	for _, state := range toolsStates() {
		t.Run(state.name, func(t *testing.T) {
			if state.down {
				fx := newOwnedFilesFixtureWith(t, installedTools())
				state.plant(t, fx)
				engine, reason := fx.tools.Engine()
				if engine != nil || reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid") {
					t.Errorf("Engine() = (%v, %v), want down saying tools.json is invalid", engine, reason)
				}
				if absent(fx.manifest) != state.absent {
					t.Errorf("tools.json absent = %v, want %v (no seed written over a refused manifest)", absent(fx.manifest), state.absent)
				}
				return
			}
			want, ok := wants[state.name]
			if !ok {
				t.Fatalf("Setup: no expected answers for %q", state.name)
			}
			setup := func(t *testing.T) (*ownedFilesFixture, *toolbelt.Engine) {
				t.Helper()
				fx := newOwnedFilesFixtureWith(t, installedTools())
				state.plant(t, fx)
				engine, reason := fx.tools.Engine()
				if engine == nil {
					t.Fatalf("Setup: Engine() = (nil, %v), want up", reason)
				}
				return fx, engine
			}
			t.Run("Inventory", func(t *testing.T) {
				_, engine := setup(t)
				inv, err := engine.Inventory()
				if err != nil {
					t.Fatalf("Inventory: %v", err)
				}
				var names []string
				for _, tool := range inv.Tools {
					names = append(names, tool.Name)
				}
				slices.Sort(names)
				if !slices.Equal(names, want.inventory) {
					t.Errorf("Inventory lists %q, want %q", names, want.inventory)
				}
			})
			t.Run("Reconcile", func(t *testing.T) {
				fx, engine := setup(t)
				reconcileAndWait(t, engine)
				if got := !absent(filepath.Join(fx.toolsDir, "bin", "b")); got != want.keepsB {
					t.Errorf("b's binary present after a Reconcile = %v, want %v", got, want.keepsB)
				}
			})
			t.Run("Add", func(t *testing.T) {
				fx, engine := setup(t)
				before, _ := os.ReadFile(fx.manifest)
				_, err := engine.Add(t.Context(), &toolbelt.AddRequest{Name: "c", Source: toolbelt.SourceManual, Install: "true", Disabled: true})
				after, _ := os.ReadFile(fx.manifest)
				if want.afterAdd == nil {
					if !errors.Is(err, toolbelt.ErrManifestInvalid) || !bytes.Equal(after, before) {
						t.Errorf("Add = %v, tools.json %q; want ErrManifestInvalid and %q unchanged", err, after, before)
					}
					return
				}
				m, perr := toolbelt.ParseManifest(after)
				if err != nil || perr != nil || !slices.Equal(slices.Sorted(maps.Keys(m.Tools)), want.afterAdd) {
					t.Errorf("Add = %v; tools.json %q (%v), want rows %q", err, after, perr, want.afterAdd)
				}
			})
		})
	}
	t.Run("readManifest/a FIFO answers within 1s", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), toolsManifestName)
		fifoAt(t, path)
		var r ownedfile.Read
		var problem error
		within(t, time.Second, "readManifest over a FIFO", func() { r, _, problem = readManifest(path) })
		if r.Kind != ownedfile.Unreadable || problem == nil || !strings.Contains(problem.Error(), "not a regular file") {
			t.Errorf("readManifest over a FIFO = %+v, %v; want Unreadable naming a file that is not regular", r, problem)
		}
	})
}

// reconcileAndWait runs a ReconcileMissing pass to completion.
func reconcileAndWait(t *testing.T, engine *toolbelt.Engine) {
	t.Helper()
	job, enqueued, err := engine.Reconcile(toolbelt.ReconcileMissing)
	if err != nil || !enqueued {
		t.Fatalf("Reconcile = (%v, %v), want a job", enqueued, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done, err := engine.Wait(ctx, job.ID)
	if err != nil || done.State != toolbelt.JobDone {
		t.Fatalf("the reconcile job ended %+v (%v), want done", done, err)
	}
}

func testToolsJSONCells(t *testing.T) {
	jobsSince := func(fx *ownedFilesFixture, n int) []string { return fx.enqueued()[n:] }
	t.Run("up/mid-save rename", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		engine := upEngine(t, fx)
		renameAway(t, fx.manifest)

		fx.tools.tick()
		if !absent(fx.manifest) || len(fx.enqueued()) != 0 {
			t.Errorf("after tick 1: absent %v, jobs %v; want still absent and none", absent(fx.manifest), fx.enqueued())
		}
		assertInventory(t, engine, []string{"a", "b"})
		reconcileAndWait(t, engine)
		if absent(filepath.Join(fx.toolsDir, "bin", "b")) {
			t.Error("a Reconcile inside the hold uninstalled b, want nothing uninstalled")
		}
		base := len(fx.enqueued())
		person := manifestOf(pinnedManualRow("a"), pinnedManualRow("b"), disabledRow("mine"))
		writeConfig(t, fx.manifest, person)

		fx.tools.tick()
		assertBytes(t, fx.manifest, person, "after tick 2")
		if got := jobsSince(fx, base); !slices.Equal(got, []string{toolbelt.JobKindReconcile}) {
			t.Errorf("jobs queued by tick 2 = %v, want one %q", got, toolbelt.JobKindReconcile)
		}

		fx.tools.tick()
		if got := jobsSince(fx, base); len(got) != 1 {
			t.Errorf("jobs queued by ticks 2 and 3 = %v, want only tick 2's", got)
		}
	})
	t.Run("up/removed", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		engine := upEngine(t, fx)
		seed := seedAsNewWritesIt(t)
		removeFile(t, fx.manifest)

		fx.tools.tick()
		if !absent(fx.manifest) || len(fx.enqueued()) != 0 {
			t.Errorf("after tick 1: absent %v, jobs %v; want still absent and none", absent(fx.manifest), fx.enqueued())
		}
		assertInventory(t, engine, []string{"a", "b"})

		fx.tools.tick()
		if got, _ := os.ReadFile(fx.manifest); !bytes.Equal(got, seed) {
			t.Errorf("tools.json after tick 2 = %q, want the seed as New writes it %q", got, seed)
		}
		if got := fx.enqueued(); !slices.Equal(got, []string{toolbelt.JobKindReconcile}) {
			t.Errorf("jobs after tick 2 = %v, want one %q", got, toolbelt.JobKindReconcile)
		}
		reconcileAndWait(t, engine)
		if !absent(filepath.Join(fx.toolsDir, "bin", "b")) {
			t.Error("b, which the seed lacks, is still installed after the seed's reconcile")
		}
		base := len(fx.enqueued())

		fx.tools.tick()
		if got := jobsSince(fx, base); len(got) != 0 {
			t.Errorf("jobs queued by tick 3 = %v, want none", got)
		}
	})
	t.Run("up/unparseable", func(t *testing.T) {
		fx := newOwnedFilesFixtureWith(t, installedTools())
		engine := upEngine(t, fx)
		writeConfig(t, fx.manifest, truncatedManifest)

		fx.tools.tick()
		if got, _ := fx.tools.Engine(); got == nil || len(fx.enqueued()) != 0 {
			t.Errorf("after tick 1: engine up %v, jobs %v; want up and none", got != nil, fx.enqueued())
		}
		assertInventory(t, engine, []string{"a", "b"})
		if _, err := engine.Add(t.Context(), &toolbelt.AddRequest{Name: "c", Source: toolbelt.SourceManual, Install: "true", Disabled: true}); !errors.Is(err, toolbelt.ErrManifestInvalid) {
			t.Errorf("Add after tick 1 = %v, want ErrManifestInvalid", err)
		}
		assertBytes(t, fx.manifest, truncatedManifest, "after a refused Add")

		fx.tools.tick()
		if got, reason := fx.tools.Engine(); got != nil || reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid") {
			t.Errorf("Engine() after tick 2 = (%v, %v), want down saying tools.json is invalid", got, reason)
		}
		if n := len(loggedManifestRefusals(t, fx.logs)); n != 1 {
			t.Errorf("refusals logged after tick 2 = %d, want 1", n)
		}

		fx.tools.tick()
		if n := len(loggedManifestRefusals(t, fx.logs)); n != 1 {
			t.Errorf("refusals logged after tick 3 = %d, want still 1", n)
		}
	})
	unfit := fmt.Errorf("toolbelt: %w", toolbelt.ErrRootIntegrity)
	t.Run("down/mid-save rename", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.builds = 0
		renameAway(t, c.manifest)

		c.slot.tick()
		if c.builds != 0 || !absent(c.manifest) {
			t.Errorf("after tick 1: %d builds, absent %v; want 0 and still absent", c.builds, absent(c.manifest))
		}
		person := manifestOf(disabledRow("mine"))
		writeConfig(t, c.manifest, person)

		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after tick 2 = %d, want 1 over the person's bytes", c.builds)
		}
		assertBytes(t, c.manifest, person, "after tick 2")

		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after tick 3 = %d, want still 1", c.builds)
		}
	})
	t.Run("down/removed", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.builds = 0
		c.remove(t)

		c.slot.tick()
		if c.builds != 0 {
			t.Errorf("builds after tick 1 = %d, want 0", c.builds)
		}

		c.slot.tick()
		if _, reason := c.slot.Engine(); c.builds != 1 || !absent(c.manifest) || !errors.Is(reason, errToolsRootUnfit) {
			t.Errorf("after tick 2: %d builds, absent %v, reason %v; want 1, still absent and the root-integrity reason", c.builds, absent(c.manifest), reason)
		}

		c.slot.tick()
		if c.builds != 1 {
			t.Errorf("builds after tick 3 = %d, want still 1", c.builds)
		}
	})
	t.Run("down/unparseable", func(t *testing.T) {
		c := newCountedSlot(t, manifestOf(), unfit)
		c.builds = 0
		writeConfig(t, c.manifest, truncatedManifest)

		c.slot.tick()
		if c.builds != 0 || c.refusals(t) != 0 {
			t.Errorf("after tick 1: %d builds, %d refusals; want 0 and 0", c.builds, c.refusals(t))
		}

		c.slot.tick()
		if _, reason := c.slot.Engine(); c.builds != 0 || c.refusals(t) != 1 || reason == nil || !strings.Contains(reason.Error(), "tools.json is invalid") {
			t.Errorf("after tick 2: %d builds, %d refusals, reason %v; want 0, 1 and a reason naming the invalid manifest", c.builds, c.refusals(t), reason)
		}

		c.slot.tick()
		if c.builds != 0 || c.refusals(t) != 1 {
			t.Errorf("after tick 3: %d builds, %d refusals; want 0 and still 1", c.builds, c.refusals(t))
		}
	})
}

func upEngine(t *testing.T, fx *ownedFilesFixture) *toolbelt.Engine {
	t.Helper()
	engine, reason := fx.tools.Engine()
	if engine == nil {
		t.Fatalf("Setup: Engine() = (nil, %v), want up", reason)
	}
	return engine
}

func assertInventory(t *testing.T, engine *toolbelt.Engine, want []string) {
	t.Helper()
	inv, err := engine.Inventory()
	if err != nil {
		t.Errorf("Inventory: %v", err)
		return
	}
	var names []string
	for _, tool := range inv.Tools {
		if tool.Installed {
			names = append(names, tool.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("Inventory's installed rows = %q, want %q", names, want)
	}
}
