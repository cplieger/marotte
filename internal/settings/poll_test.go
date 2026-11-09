package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

const seededDoc = `{"security_profile":"trusted","settings_epoch":5,"settings_revision":3,"theme":"dark"}`

var seededStamp = stamp{epoch: 5, revision: 3}

// pollFixture is a config dir whose cache has read seed once, as boot's first setting read does.
func pollFixture(t *testing.T, seed string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	resetCache(t, dir)
	t.Cleanup(func() { resetCache(t, dir) })
	path = filepath.Join(dir, Filename)
	if seed != "" {
		writeFile(t, path, seed)
	}
	if _, _, err := Read(t.Context(), dir); err != nil {
		t.Fatalf("seed read: %v", err)
	}
	return dir, path
}

func setRaw(key, raw string) func(map[string]json.RawMessage) error {
	return func(doc map[string]json.RawMessage) error {
		doc[key] = json.RawMessage(raw)
		return nil
	}
}

func writeJSON(t *testing.T, path string, doc map[string]json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func poll(t *testing.T, dir string, ev *events) ([]string, bool) {
	t.Helper()
	changed, ok, err := Poll(t.Context(), dir, ev.publish)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	return changed, ok
}

func payloadOf(s stamp) marotte.SettingsUpdatedPayload {
	return marotte.SettingsUpdatedPayload{SettingsEpoch: s.epoch, SettingsRevision: s.revision}
}

func stampOfPayload(p marotte.SettingsUpdatedPayload) stamp {
	return stamp{epoch: p.SettingsEpoch, revision: p.SettingsRevision}
}

func TestPoll_ReportsAnOutsideWriteSinceTheFirstRead(t *testing.T) {
	dir, path := pollFixture(t, seededDoc)
	edited := `{"security_profile":"trusted","settings_epoch":5,"settings_revision":3,"theme":"lite"}`
	writeFile(t, path, edited)
	var ev events

	changed, ok := poll(t, dir, &ev)
	if !ok || !slices.Equal(changed, []string{KeyTheme}) {
		t.Errorf("first Poll = (%v, %v), want ([theme], true)", changed, ok)
	}
	if want := []marotte.SettingsUpdatedPayload{payloadOf(seededStamp)}; !slices.Equal(ev.got, want) {
		t.Errorf("published %v, want %v", ev.got, want)
	}
	if got := fileBytes(t, path); string(got) != edited {
		t.Errorf("config.json = %s, want the outside bytes unchanged", got)
	}
	if changed, ok := poll(t, dir, &ev); ok || len(changed) != 0 {
		t.Errorf("second Poll = (%v, %v), want nothing", changed, ok)
	}
	if _, err := Update(t.Context(), dir, setRaw(KeyChatRetentionDays, "-1"), nil); err != nil {
		t.Fatal(err)
	}
	if changed, ok := poll(t, dir, &ev); ok || len(changed) != 0 {
		t.Errorf("Poll after Update = (%v, %v), want nothing", changed, ok)
	}

	ev = events{}
	writeFile(t, path, `{`)
	if changed, ok := poll(t, dir, &ev); ok || changed != nil || len(ev.got) != 0 {
		t.Errorf("first Poll over a partial file = (%v, %v), published %v; want nothing", changed, ok, ev.got)
	}
	if changed, ok := poll(t, dir, &ev); !ok || changed != nil {
		t.Errorf("second Poll over a partial file = (%v, %v), want (nil, true)", changed, ok)
	}

	t.Run("no_read_before_the_first_poll", func(t *testing.T) {
		dir := t.TempDir()
		resetCache(t, dir)
		t.Cleanup(func() { resetCache(t, dir) })
		writeFile(t, filepath.Join(dir, Filename), seededDoc)
		var ev events
		if changed, ok := poll(t, dir, &ev); ok || changed != nil || len(ev.got) != 0 {
			t.Errorf("first Poll = (%v, %v), published %v; want nothing", changed, ok, ev.got)
		}
	})
}

func TestPoll_ReportsAnOutsideEditAnUpdateFoldedIn(t *testing.T) {
	dir, path := pollFixture(t, seededDoc)
	writeFile(t, path, `{"security_profile":"guarded","settings_epoch":5,"settings_revision":3,"theme":"dark"}`)
	if _, err := Update(t.Context(), dir, setKey(KeyTheme, "lite"), nil); err != nil {
		t.Fatal(err)
	}
	var ev events
	changed, ok := poll(t, dir, &ev)
	if !ok || !slices.Contains(changed, KeySecurityProfile) {
		t.Errorf("Poll = (%v, %v), want a set holding security_profile", changed, ok)
	}
	if changed, ok := poll(t, dir, &ev); ok || len(changed) != 0 {
		t.Errorf("second Poll = (%v, %v), want nothing", changed, ok)
	}
}

func TestPoll_AnAbsentFileReportsEveryKeyItHeld(t *testing.T) {
	dir, path := pollFixture(t, seededDoc)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var ev events
	if changed, ok := poll(t, dir, &ev); ok || changed != nil || len(ev.got) != 0 {
		t.Errorf("first Poll = (%v, %v), published %v; want nothing", changed, ok, ev.got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("after one Poll config.json exists (%v), want it still absent", err)
	}
	changed, ok := poll(t, dir, &ev)
	if want := []string{KeySecurityProfile, KeyTheme}; !ok || !slices.Equal(changed, want) {
		t.Errorf("second Poll = (%v, %v), want (%v, true)", changed, ok, want)
	}
	doc := readConfig(t, dir)
	if len(doc) != 2 || !seededStamp.below(stampOf(doc)) {
		t.Errorf("config.json = %v, want only the stamp keys above %v", doc, seededStamp)
	}
	if want := []marotte.SettingsUpdatedPayload{payloadOf(stampOf(doc))}; !slices.Equal(ev.got, want) {
		t.Errorf("published %v, want %v", ev.got, want)
	}
	if changed, ok := poll(t, dir, &ev); ok || len(changed) != 0 {
		t.Errorf("third Poll = (%v, %v), want nothing", changed, ok)
	}

	t.Run("absent_at_the_seed", func(t *testing.T) {
		dir, path := pollFixture(t, "")
		var ev events
		for range 3 {
			if changed, ok := poll(t, dir, &ev); ok || changed != nil {
				t.Errorf("Poll = (%v, %v), want nothing", changed, ok)
			}
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) || len(ev.got) != 0 {
			t.Errorf("config.json stat %v, published %v; want no file and no event", err, ev.got)
		}
	})
}

func TestPoll_RestampsADocumentBelowTheHighestStamp(t *testing.T) {
	const themeLite = `"theme":"lite"`
	check := func(t *testing.T, dir string, ev *events, above stamp, wantChanged []string, wantOK bool) {
		t.Helper()
		changed, ok := poll(t, dir, ev)
		if ok != wantOK || !slices.Equal(changed, wantChanged) {
			t.Errorf("Poll = (%v, %v), want (%v, %v)", changed, ok, wantChanged, wantOK)
		}
		doc := readConfig(t, dir)
		got := stampOf(doc)
		if !above.below(got) {
			t.Errorf("config.json stamped %v, want above %v", got, above)
		}
		if len(ev.got) == 0 || stampOfPayload(ev.got[len(ev.got)-1]) != got {
			t.Errorf("published %v, want the rewritten stamp %v last", ev.got, got)
		}
		if changed, ok := poll(t, dir, ev); ok || len(changed) != 0 {
			t.Errorf("next Poll = (%v, %v), want nothing", changed, ok)
		}
	}

	t.Run("written_without_the_stamp_keys", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		writeFile(t, path, `{"security_profile":"trusted",`+themeLite+`}`)
		var ev events
		check(t, dir, &ev, seededStamp, []string{KeyTheme}, true)
		if v := readConfig(t, dir)[KeyTheme]; string(v) != `"lite"` {
			t.Errorf("theme = %s, want the outside value kept", v)
		}
	})
	t.Run("an_older_stamped_copy", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		if _, err := Update(t.Context(), dir, setRaw(KeyChatRetentionDays, "-1"), nil); err != nil {
			t.Fatal(err)
		}
		written := storedStamp(t, dir)
		writeFile(t, path, `{"chat_retention_days":-1,"security_profile":"trusted","settings_epoch":5,"settings_revision":3,`+themeLite+`}`)
		var ev events
		check(t, dir, &ev, written, []string{KeyTheme}, true)
	})
	t.Run("a_copy_below_an_announced_hand_typed_stamp", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		handTyped := stamp{epoch: 9, revision: 1}
		writeFile(t, path, `{"security_profile":"trusted","settings_epoch":9,"settings_revision":1,"theme":"dark"}`)
		var ev events
		Announce(t.Context(), dir, ev.publish)
		writeFile(t, path, `{"security_profile":"trusted","settings_epoch":5,"settings_revision":3,`+themeLite+`}`)
		check(t, dir, &ev, handTyped, []string{KeyTheme}, true)
	})
	t.Run("the_last_committed_stamp_after_a_later_outside_one", func(t *testing.T) {
		for _, updateFirst := range []bool{false, true} {
			dir, path := pollFixture(t, seededDoc)
			if _, err := Update(t.Context(), dir, setRaw(KeyChatRetentionDays, "-1"), nil); err != nil {
				t.Fatal(err)
			}
			committedBytes := fileBytes(t, path)
			s := storedStamp(t, dir)
			outside := stamp{epoch: s.epoch, revision: s.revision + 5}
			doc := readConfig(t, dir)
			outside.write(doc)
			doc[KeyTheme] = []byte(`"lite"`)
			writeJSON(t, path, doc)
			var ev events
			if _, ok := poll(t, dir, &ev); !ok {
				t.Fatal("Poll over the outside document reported nothing")
			}
			if err := os.WriteFile(path, committedBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if updateFirst {
				if _, err := Update(t.Context(), dir, setRaw(KeyDebugLogs, "true"), nil); err != nil {
					t.Fatal(err)
				}
				if got := storedStamp(t, dir); !outside.below(got) {
					t.Errorf("Update over the copied-back document stamped %v, want above %v", got, outside)
				}
				continue
			}
			check(t, dir, &ev, outside, []string{KeyTheme}, true)
		}
	})
	t.Run("stamp_keys_removed_and_nothing_else", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		writeFile(t, path, `{"security_profile":"trusted","theme":"dark"}`)
		var ev events
		check(t, dir, &ev, seededStamp, nil, false)
		if doc := readConfig(t, dir); string(doc[KeyTheme]) != `"dark"` || string(doc[KeySecurityProfile]) != `"trusted"` {
			t.Errorf("config.json = %v, want its keys unchanged", doc)
		}
	})
	t.Run("an_older_copy_equal_in_every_other_key", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
			t.Fatal(err)
		}
		written := storedStamp(t, dir)
		writeFile(t, path, seededDoc)
		var ev events
		check(t, dir, &ev, written, nil, false)
	})
	t.Run("an_unparseable_document_is_never_rewritten", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		writeFile(t, path, `{"theme":`)
		var ev events
		poll(t, dir, &ev)
		changed, ok := poll(t, dir, &ev)
		if !ok || changed != nil {
			t.Errorf("second Poll = (%v, %v), want (nil, true)", changed, ok)
		}
		if want := []marotte.SettingsUpdatedPayload{{}}; !slices.Equal(ev.got, want) {
			t.Errorf("published %v, want the zero stamp once", ev.got)
		}
		if got := fileBytes(t, path); string(got) != `{"theme":` {
			t.Errorf("config.json = %s, want it untouched", got)
		}
	})
}

func TestPoll_WaitsOutAnAbsentOrPartialMoment(t *testing.T) {
	saved := `{"security_profile":"trusted","settings_epoch":5,"settings_revision":3,"theme":"lite"}`
	t.Run("absent_then_saved", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		var ev events
		poll(t, dir, &ev)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("after one Poll config.json exists (%v)", err)
		}
		writeFile(t, path, saved)
		if changed, ok := poll(t, dir, &ev); !ok || !slices.Equal(changed, []string{KeyTheme}) {
			t.Errorf("Poll = (%v, %v), want ([theme], true)", changed, ok)
		}
		if got := fileBytes(t, path); string(got) != saved {
			t.Errorf("config.json = %s, want the saved bytes", got)
		}
	})
	t.Run("partial_then_saved", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		writeFile(t, path, `{"theme":`)
		var ev events
		poll(t, dir, &ev)
		writeFile(t, path, saved)
		if changed, ok := poll(t, dir, &ev); !ok || !slices.Equal(changed, []string{KeyTheme}) {
			t.Errorf("Poll = (%v, %v), want ([theme], true)", changed, ok)
		}
	})
	t.Run("reset_by_a_write", func(t *testing.T) {
		dir, path := pollFixture(t, seededDoc)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		var ev events
		poll(t, dir, &ev)
		if _, err := Update(t.Context(), dir, setRaw(KeyDebugLogs, "true"), nil); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if changed, ok := poll(t, dir, &ev); ok || changed != nil {
			t.Errorf("first Poll after the write = (%v, %v), want nothing", changed, ok)
		}
		changed, ok := poll(t, dir, &ev)
		if want := []string{KeyDebugLogs, KeySecurityProfile, KeyTheme}; !ok || !slices.Equal(changed, want) {
			t.Errorf("second Poll = (%v, %v), want (%v, true)", changed, ok, want)
		}
	})
}

func TestPoll_SeesASameLengthRewriteThatKeepsTheStat(t *testing.T) {
	dir, path := pollFixture(t, seededDoc)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Replace([]byte(seededDoc), []byte(`"dark"`), []byte(`"lite"`), 1)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(rewritten); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Time{}, info.ModTime()); err != nil {
		t.Fatal(err)
	}
	var ev events
	if changed, ok := poll(t, dir, &ev); !ok || !slices.Equal(changed, []string{KeyTheme}) {
		t.Errorf("Poll = (%v, %v), want ([theme], true)", changed, ok)
	}
	if got, _ := Field[string](t.Context(), dir, KeyTheme); got != "lite" {
		t.Errorf("theme after the Poll = %q, want lite", got)
	}
}

// A held absence after an adopted refusal is that refusal to every reader, never the empty document: refused bytes
// answer ErrUnparseable at the applied generation, a refused read with no bytes ErrUnreadable at generation 0.
func TestRead_AHeldAbsenceAfterAnAdoptedRefusalAnswersThatRefusal(t *testing.T) {
	t.Run("refused_bytes", func(t *testing.T) {
		dir, path := pollFixture(t, `{"chat_retention_days":-1}`)
		writeFile(t, path, `{"theme":`)
		var ev events
		poll(t, dir, &ev)
		poll(t, dir, &ev)
		if _, _, err := Read(t.Context(), dir); !errors.Is(err, ErrUnparseable) {
			t.Fatalf("Read over the adopted refusal = %v, want ErrUnparseable", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		_, gen, err := Read(t.Context(), dir)
		if !errors.Is(err, ErrUnparseable) || gen == 0 {
			t.Errorf("Read over the held absence = gen %d, %v; want ErrUnparseable at the applied record's generation", gen, err)
		}
		if !RetentionEnabled(t.Context(), dir) {
			t.Error("RetentionEnabled over the held absence = false, want true (it fails toward keeping)")
		}
		assertUpdateRefusedCreatingNothing(t, dir, path)
	})
	t.Run("a_directory", func(t *testing.T) {
		dir, path := pollFixture(t, `{"chat_retention_days":-1}`)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		var ev events
		poll(t, dir, &ev)
		poll(t, dir, &ev)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		_, gen, err := Read(t.Context(), dir)
		if !errors.Is(err, ErrUnreadable) || gen != 0 {
			t.Errorf("Read over the held absence = gen %d, %v; want ErrUnreadable at generation 0", gen, err)
		}
		assertUpdateRefusedCreatingNothing(t, dir, path)
	})
}

func assertUpdateRefusedCreatingNothing(t *testing.T, dir, path string) {
	t.Helper()
	if _, err := Update(t.Context(), dir, setRaw(KeyTheme, `"dark"`), nil); !errors.Is(err, ErrUnreadable) {
		t.Errorf("Update over the held absence = %v, want ErrUnreadable", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config.json after the refused Update: stat %v, want no file created", err)
	}
	if _, err := Stored(dir); err == nil {
		t.Error("Stored over the held absence = a document, want the refusal")
	}
}
