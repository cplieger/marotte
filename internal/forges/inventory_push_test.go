package forges

// The inventory's push: every entry a cycle writes goes out as one
// forge_inventory frame, stamped with the version the digest reads.

import (
	"context"
	"reflect"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
)

// recordingBroadcaster is the hub: it keeps every frame and answers one epoch.
type recordingBroadcaster struct {
	epoch  string
	events []marotte.ServerEvent
}

func (b *recordingBroadcaster) Broadcast(_ context.Context, evt marotte.ServerEvent) {
	b.events = append(b.events, evt)
}

func (b *recordingBroadcaster) Epoch() string { return b.epoch }

func pushPoller(t *testing.T, cores map[string]forgeapi.Core, origins []RepoOrigin, recs ...connectionRecord,
) (*PRStatusPoller, *fakeGate, *recordingBroadcaster, *subject.Versions, *Manager) {
	t.Helper()
	m := sourceManager(t, cores, recs...)
	g := &fakeGate{present: true}
	b := &recordingBroadcaster{epoch: "epoch-1"}
	v := &subject.Versions{}
	p := NewPRStatusPoller(NewManagerPRSource(m, fixedOrigins(origins...)), &fakeNotifier{}, g.Open, WithInventoryPush(v, b))
	return p, g, b, v, m
}

func inventoryVersion(v *subject.Versions, id string) string {
	got, _ := v.Current(subject.KindForgeInventory, id)
	return got
}

func lastStamp(b *recordingBroadcaster) string {
	if len(b.events) == 0 || b.events[len(b.events)-1].Subject == nil {
		return ""
	}
	return b.events[len(b.events)-1].Subject.Version
}

func framesByConnection(t *testing.T, events []marotte.ServerEvent) map[string][]marotte.ServerEvent {
	t.Helper()
	out := map[string][]marotte.ServerEvent{}
	for _, evt := range events {
		if evt.Type != marotte.EventForgeInventory {
			t.Errorf("broadcast a %q frame, want forge_inventory frames only", evt.Type)
			continue
		}
		if evt.Subject == nil {
			t.Errorf("a forge_inventory frame carries no stamp: %+v", evt)
			continue
		}
		out[evt.Subject.Ref] = append(out[evt.Subject.Ref], evt)
	}
	return out
}

func TestInventory_ChangedEntryBroadcastsOnce(t *testing.T) {
	gh, gl := githubRecord(), gitlabRecord()
	gh.OwnerScopes = []string{"acme"}
	ghCore := newScopeCore()
	ghCore.pages["bob"] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	ghCore.pages[""] = page(prIn(forgeapi.FamilyGitHub, "carol/lib", 2, forgeapi.CheckPending))
	p, _, b, _, _ := pushPoller(t, map[string]forgeapi.Core{gh.ID: ghCore, gl.ID: newScopeCore()}, nil, gh, gl)

	for cycle, version := range []string{"1", "2"} {
		b.events = nil
		p.sweep(t.Context())

		frames := framesByConnection(t, b.events)
		for _, rec := range []connectionRecord{gh, gl} {
			got := frames[rec.ID]
			if len(got) != 1 {
				t.Errorf("cycle %d broadcast %d frames for %s, want one whatever its scope count", cycle+1, len(got), rec.ID)
				continue
			}
			evt := got[0]
			wantStamp := marotte.SubjectStamp{Kind: string(subject.KindForgeInventory), Ref: rec.ID, Version: version}
			if *evt.Subject != wantStamp || evt.ChatID != "" {
				t.Errorf("cycle %d frame for %s: stamp %+v, chat %q; want %+v on the workspace topic",
					cycle+1, rec.ID, *evt.Subject, evt.ChatID, wantStamp)
			}
			payload, ok := evt.Payload.(InventoryChangedPayload)
			if want := entryFor(t, p, rec.ID); !ok || !reflect.DeepEqual(payload.Entry, want) {
				t.Errorf("cycle %d frame for %s carries %+v, want the entry the cycle wrote, %+v", cycle+1, rec.ID, evt.Payload, want)
			}
		}
	}
}

func TestInventory_UnchangedCycleBroadcastsNothing(t *testing.T) {
	core := newScopeCore()
	core.pages[""] = page(prIn(forgeapi.FamilyGitHub, "bob/app", 1, forgeapi.CheckPending))
	rec := githubRecord()
	clone := RepoOrigin{Dir: "app", WebBase: "https://github.com", Slug: "bob/app"}
	p, g, b, v, m := pushPoller(t, map[string]forgeapi.Core{rec.ID: core}, []RepoOrigin{clone}, rec)
	p.sweep(t.Context())
	if len(b.events) != 1 {
		t.Fatalf("Setup: the present cycle broadcast %d frames, want 1", len(b.events))
	}

	g.present, g.push = false, true
	p.sweep(t.Context())
	if len(core.asked) != 3 {
		t.Fatalf("Setup: lists made = %d, want 3: the push-only cycle must have read the authored call", len(core.asked))
	}
	p.inventoryFor(m.List(t.Context()))

	if len(b.events) != 1 || inventoryVersion(v, rec.ID) != "1" {
		t.Errorf("after a push-only cycle and a read: %d frames, version %q; want 1 and \"1\": neither writes the entry",
			len(b.events), inventoryVersion(v, rec.ID))
	}
}

func TestInventory_VersionBumpsWithTheEntry(t *testing.T) {
	rec := githubRecord()
	p, g, b, v, m := pushPoller(t, map[string]forgeapi.Core{rec.ID: newScopeCore()}, nil, rec)

	p.sweep(t.Context())
	p.sweep(t.Context())
	if got := inventoryVersion(v, rec.ID); got != "2" || len(b.events) != 2 || lastStamp(b) != got {
		t.Fatalf("after two present cycles: version %q, %d frames, last stamp %q; want \"2\" on the second frame",
			got, len(b.events), lastStamp(b))
	}

	if err := m.store.Delete(rec.ID); err != nil {
		t.Fatalf("Setup: delete the credential: %v", err)
	}
	m.invalidate()
	p.sweep(t.Context())
	if got := inventoryVersion(v, rec.ID); got != "3" || len(b.events) != 2 {
		t.Errorf("after the connection was dropped: version %q, %d frames; want \"3\" and no frame: "+
			"a client holding the entry must read it changed", got, len(b.events))
	}

	seedStoreRecord(t, m.configDir, rec.ID, "bob")
	m.invalidate()
	p.sweep(t.Context())
	if got := inventoryVersion(v, rec.ID); got != "4" || lastStamp(b) != "4" {
		t.Errorf("after it reconnected: version %q, last stamp %q; want \"4\", never a version a client already holds",
			got, lastStamp(b))
	}

	g.present = false
	p.sweep(t.Context())
	if got := inventoryVersion(v, rec.ID); got != "5" || len(b.events) != 3 {
		t.Errorf("after the gate closed: version %q, %d frames; want \"5\" and no frame", got, len(b.events))
	}
	g.present = false
	p.sweep(t.Context())
	if got := inventoryVersion(v, rec.ID); got != "5" {
		t.Errorf("a second closed sweep moved the version to %q, want \"5\": nothing was held to remove", got)
	}
}
