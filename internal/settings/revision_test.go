package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
)

func storedStamp(t *testing.T, dir string) stamp {
	t.Helper()
	return stampOf(readConfig(t, dir))
}

// after reports whether a orders after b, as a client compares them.
func (a stamp) after(b stamp) bool {
	return a.epoch > b.epoch || (a.epoch == b.epoch && a.revision > b.revision)
}

// forgetProcess drops dir's cache, as a restarted process starts without one.
func forgetProcess(dir string) {
	globalCacheMu.Lock()
	delete(globalCaches, dir)
	globalCacheMu.Unlock()
}

// events records the settings_updated payloads Update and Announce publish.
type events struct {
	got   []marotte.SettingsUpdatedPayload
	other []marotte.EventType
	mu    sync.Mutex
}

func (e *events) publish(_ context.Context, evt marotte.ServerEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := evt.Payload.(marotte.SettingsUpdatedPayload); ok && evt.Type == marotte.EventSettingsUpdated {
		e.got = append(e.got, p)
		return
	}
	e.other = append(e.other, evt.Type)
}

func TestUpdate_BumpsTheRevisionOncePerWrite(t *testing.T) {
	dir := t.TempDir()
	var ev events
	for want := uint64(1); want <= 3; want++ {
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), ev.publish); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := storedStamp(t, dir).revision; got != want {
			t.Errorf("settings_revision = %d, want %d", got, want)
		}
	}
	epoch := storedStamp(t, dir).epoch
	if epoch == 0 {
		t.Fatal("no settings_epoch after the first write")
	}
	want := []marotte.SettingsUpdatedPayload{
		{SettingsEpoch: epoch, SettingsRevision: 1},
		{SettingsEpoch: epoch, SettingsRevision: 2},
		{SettingsEpoch: epoch, SettingsRevision: 3},
	}
	if !slices.Equal(ev.got, want) || len(ev.other) != 0 {
		t.Errorf("events = %v (other %v), want one settings_updated per write with its own stamp %v", ev.got, ev.other, want)
	}
}

func TestUpdate_ConcurrentWritesPublishInRevisionOrder(t *testing.T) {
	dir := t.TempDir()
	const writers = 16
	var ev events
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), ev.publish); err != nil {
				t.Errorf("Update: %v", err)
			}
		})
	}
	wg.Wait()
	if got := storedStamp(t, dir).revision; got != writers {
		t.Errorf("settings_revision = %d after %d writes, want %d", got, writers, writers)
	}
	if len(ev.got) != writers {
		t.Fatalf("%d events for %d writes, want one each", len(ev.got), writers)
	}
	for i, p := range ev.got {
		if p.SettingsRevision != uint64(i+1) {
			t.Errorf("event %d carries revision %d, want %d: %v", i, p.SettingsRevision, i+1, ev.got)
			break
		}
	}
}

func TestUpdate_NoWriteCommitsWhileAnEventIsPublished(t *testing.T) {
	dir := t.TempDir()
	var ev events
	second := make(chan error, 1)
	first := func(ctx context.Context, evt marotte.ServerEvent) {
		go func() {
			_, err := Update(t.Context(), dir, setKey(KeyTheme, "light"), ev.publish)
			second <- err
		}()
		select {
		case err := <-second:
			t.Errorf("a write committed (err %v) while the previous write's event was being published", err)
			second <- err
		case <-time.After(100 * time.Millisecond):
		}
		ev.publish(ctx, evt)
	}
	if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), first); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if len(ev.got) != 2 || ev.got[0].SettingsRevision != 1 || ev.got[1].SettingsRevision != 2 {
		t.Errorf("events = %v, want revisions 1 then 2", ev.got)
	}
}

func TestUpdate_ARefusedWriteKeepsTheStampAndPublishesNothing(t *testing.T) {
	t.Run("the merge refuses", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `{"settings_epoch":9,"settings_revision":4}`)
		var ev events
		refusal := errors.New("refused")
		if _, err := Update(t.Context(), dir, func(map[string]json.RawMessage) error { return refusal }, ev.publish); !errors.Is(err, refusal) {
			t.Fatalf("Update = %v, want the merge's refusal", err)
		}
		if got := storedStamp(t, dir); got != (stamp{epoch: 9, revision: 4}) {
			t.Errorf("stamp = %+v after a refused write, want epoch 9 revision 4", got)
		}
		if len(ev.got)+len(ev.other) != 0 {
			t.Errorf("events = %v %v after a refused write, want none", ev.got, ev.other)
		}
	})
	t.Run("the stored document is unreadable", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `{"settings_revision":4,`)
		var ev events
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), ev.publish); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("Update = %v, want ErrUnreadable", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, Filename))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"settings_revision":4,` {
			t.Errorf("config.json = %q after a refused write, want it untouched", data)
		}
		if len(ev.got)+len(ev.other) != 0 {
			t.Errorf("events = %v %v after a refused write, want none", ev.got, ev.other)
		}
	})
}

func TestStamp_PersistsInConfigJSONAcrossAReload(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	before := storedStamp(t, dir)
	forgetProcess(dir)

	effective, rejected := EffectiveFrom(readConfig(t, dir))
	if len(rejected) > 0 || effective.SettingsEpoch != before.epoch || effective.SettingsRevision != 2 {
		t.Errorf("EffectiveFrom(config.json) = epoch %d revision %d (rejected %v), want %d and 2",
			effective.SettingsEpoch, effective.SettingsRevision, rejected, before.epoch)
	}
	if _, err := Update(t.Context(), dir, setKey(KeyTheme, "light"), nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := storedStamp(t, dir); !got.after(before) {
		t.Errorf("stamp after a reload and a write = %+v, want one after %+v", got, before)
	}
}

func TestUpdate_StartsALaterEpochOverAReplacedFile(t *testing.T) {
	tests := []struct {
		replace func(t *testing.T, dir string)
		name    string
	}{
		{name: "config.json deleted", replace: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, Filename)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config.json restored to an older copy", replace: func(t *testing.T, dir string) {
			doc := readConfig(t, dir)
			doc[KeySettingsRevision] = json.RawMessage(`1`)
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			writeConfig(t, dir, string(data))
		}},
		{name: "the stamp removed by hand", replace: func(t *testing.T, dir string) {
			writeConfig(t, dir, `{}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for range 3 {
				if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
					t.Fatalf("Update: %v", err)
				}
			}
			held := storedStamp(t, dir)
			tc.replace(t, dir)
			if _, err := Update(t.Context(), dir, setKey(KeyTheme, "light"), nil); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got := storedStamp(t, dir); got.epoch <= held.epoch || got.revision != 1 {
				t.Errorf("stamp = %+v after %s, want an epoch after %d at revision 1", got, tc.name, held.epoch)
			}
		})
	}
}

func TestUpdate_ALaterEpochIsAboveTheStoredOne(t *testing.T) {
	dir := t.TempDir()
	future := uint64(time.Now().Add(24 * time.Hour).UnixMilli())
	writeConfig(t, dir, fmt.Sprintf(`{"settings_epoch":%d,"settings_revision":3}`, future))
	if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := storedStamp(t, dir); got != (stamp{epoch: future + 1, revision: 1}) {
		t.Errorf("stamp = %+v, want epoch %d revision 1", got, future+1)
	}
}

// float64 is the IEEE 754 binary64 a JavaScript number is, so these comparisons are the page's.
func TestStamp_StaysWhereAJavaScriptNumberOrdersIt(t *testing.T) {
	t.Run("an epoch above the bound reads as malformed and the next write starts afresh", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, `{"settings_epoch":1152921504606846976,"settings_revision":3}`)
		effective, rejected := EffectiveFrom(readConfig(t, dir))
		if effective.SettingsEpoch != 0 || !slices.Contains(rejected, KeySettingsEpoch) {
			t.Errorf("EffectiveFrom = epoch %d (rejected %v), want 0 with %s rejected", effective.SettingsEpoch, rejected, KeySettingsEpoch)
		}
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := storedStamp(t, dir); got.epoch == 0 || got.epoch > maxStamp || got.revision != 1 {
			t.Errorf("stamp = %+v, want a fresh epoch within %d at revision 1", got, uint64(maxStamp))
		}
	})
	t.Run("the epoch below the bound is followed by the bound, which a JavaScript number orders after it", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, fmt.Sprintf(`{"settings_epoch":%d,"settings_revision":3}`, uint64(maxStamp-1)))
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := storedStamp(t, dir)
		if got != (stamp{epoch: maxStamp, revision: 1}) || float64(got.epoch) <= float64(maxStamp-1) {
			t.Errorf("stamp = %+v, want epoch %d revision 1, above %d as a float64", got, uint64(maxStamp), uint64(maxStamp-1))
		}
	})
	t.Run("an epoch at the bound continues its count", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, fmt.Sprintf(`{"settings_epoch":%d,"settings_revision":3}`, uint64(maxStamp)))
		if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := storedStamp(t, dir); got != (stamp{epoch: maxStamp, revision: 4}) {
			t.Errorf("stamp = %+v, want epoch %d revision 4", got, uint64(maxStamp))
		}
	})
}

func TestUpdate_TheStampIsNeverTheMergesToSet(t *testing.T) {
	tests := []struct {
		merge func(map[string]json.RawMessage) error
		name  string
	}{
		{
			name: "a merge writing its own values",
			merge: func(doc map[string]json.RawMessage) error {
				doc[KeySettingsEpoch] = json.RawMessage(`1`)
				doc[KeySettingsRevision] = json.RawMessage(`100`)
				return nil
			},
		},
		{
			name: "a merge deleting them",
			merge: func(doc map[string]json.RawMessage) error {
				delete(doc, KeySettingsEpoch)
				delete(doc, KeySettingsRevision)
				return nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), nil); err != nil {
				t.Fatalf("Update: %v", err)
			}
			first := storedStamp(t, dir)
			if _, err := Update(t.Context(), dir, tc.merge, nil); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got, want := storedStamp(t, dir), (stamp{epoch: first.epoch, revision: 2}); got != want {
				t.Errorf("stamp = %+v, want %+v", got, want)
			}
		})
	}
}

func TestAnnounce_CarriesTheStoredStampWithNoWrite(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"settings_epoch":9,"settings_revision":12}`)
	var ev events
	Announce(t.Context(), dir, ev.publish)
	want := []marotte.SettingsUpdatedPayload{{SettingsEpoch: 9, SettingsRevision: 12}}
	if !slices.Equal(ev.got, want) || len(ev.other) != 0 {
		t.Errorf("events = %v (other %v), want %v", ev.got, ev.other, want)
	}
	if got := storedStamp(t, dir); got != (stamp{epoch: 9, revision: 12}) {
		t.Errorf("stamp = %+v after Announce, want it unchanged", got)
	}
}

func TestAnnounce_CarriesNoVersionForAnUnreadableDocument(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"settings_revision":`)
	var ev events
	Announce(t.Context(), dir, ev.publish)
	if want := []marotte.SettingsUpdatedPayload{{}}; !slices.Equal(ev.got, want) || len(ev.other) != 0 {
		t.Errorf("events = %v (other %v) for an unreadable config.json, want %v", ev.got, ev.other, want)
	}
}

func TestAnnounce_PublishesInOrderWithConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	var ev events
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := Update(t.Context(), dir, setKey(KeyTheme, "dark"), ev.publish); err != nil {
				t.Errorf("Update: %v", err)
			}
		})
		wg.Go(func() { Announce(t.Context(), dir, ev.publish) })
	}
	wg.Wait()
	for i := 1; i < len(ev.got); i++ {
		if ev.got[i].SettingsRevision < ev.got[i-1].SettingsRevision {
			t.Errorf("event %d at revision %d follows one at %d: %v", i, ev.got[i].SettingsRevision, ev.got[i-1].SettingsRevision, ev.got)
			break
		}
	}
}
