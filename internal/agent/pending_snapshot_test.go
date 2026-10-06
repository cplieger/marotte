package agent

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/subject"
	"pgregory.net/rapid"
)

// pendingFixture is a Runtime holding only the three stores and their registry.
func pendingFixture() *Runtime {
	v := &subject.Versions{}
	rt := &Runtime{
		versions: v,
		bus:      &bus{pendingPerms: newPendingPermsTracker(), steers: newSteerRecords()},
		runs:     &Runs{},
	}
	rt.bus.pendingPerms.versions = v
	rt.bus.steers.versions = v
	rt.runs.asks.versions = v
	return rt
}

func permNeeded(chat marotte.ChatID, id int64) marotte.ServerEvent {
	return marotte.NewEvent(marotte.EventPermissionNeeded, chat, marotte.PermissionNeededPayload{RequestID: id})
}

// snapshotKeys reduces a snapshot's items to the identities the client would hold.
func snapshotKeys(t rapid.TB, items []json.RawMessage) []string {
	keys := make([]string, 0, len(items))
	for _, raw := range items {
		var evt struct {
			Type    string `json:"type"`
			ChatID  string `json:"chat_id"`
			Payload struct {
				RequestID int64  `json:"request_id"`
				AskID     string `json:"ask_id"`
				SteerID   string `json:"steer_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatalf("snapshot item is not an envelope: %v", err)
		}
		keys = append(keys, evt.Type+"/"+evt.ChatID+"/"+strconv.FormatInt(evt.Payload.RequestID, 10)+evt.Payload.AskID+evt.Payload.SteerID)
	}
	slices.Sort(keys)
	return keys
}

// serverKeys is the same identity over the stores' own current state.
func serverKeys(t rapid.TB, rt *Runtime) []string {
	payload, _ := rt.pendingSnapshot(nil)
	return snapshotKeys(t, payload.Items)
}

// TestPendingSnapshot_CounterFirstNeverCertifiesASetItLacks pins the interleaving property:
// whenever the client's version equals the server's, the sets are equal.
func TestPendingSnapshot_CounterFirstNeverCertifiesASetItLacks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rt := pendingFixture()
		// A resident population to resolve from.
		for id := int64(1); id <= 3; id++ {
			rt.bus.pendingPerms.Add(id, permNeeded("c1", id))
		}
		rt.runs.asks.Add(askOf("c2", "wf", "a1", "n1"))
		rt.bus.steers.SteerWaiting("c3", &marotte.SteerQueuedPayload{SteerID: "s1"})

		addAt := rapid.IntRange(-1, 3).Draw(t, "addAfterRead")
		resolveAt := rapid.IntRange(-1, 3).Draw(t, "resolveAfterRead")
		resolveID := rapid.Int64Range(1, 3).Draw(t, "resolveID")
		payload, stamp := rt.pendingSnapshot(func(n int) {
			if n == addAt {
				rt.bus.pendingPerms.Add(9, permNeeded("c1", 9))
			}
			if n == resolveAt {
				rt.bus.pendingPerms.TakeIfPresent("c1", resolveID)
			}
		})
		// The late arms (-1) land after the snapshot, like a queued live frame.
		if addAt == -1 {
			rt.bus.pendingPerms.Add(9, permNeeded("c1", 9))
		}
		if resolveAt == -1 {
			rt.bus.pendingPerms.TakeIfPresent("c1", resolveID)
		}
		server, _ := rt.versions.Current(subject.KindPending, "")
		client := snapshotKeys(t, payload.Items)
		want := serverKeys(t, rt)
		if stamp.Version == server && !slices.Equal(client, want) {
			t.Fatalf("digest would answer unchanged at %q while the client holds %v and the server %v", server, client, want)
		}
		stampV, err := strconv.ParseUint(stamp.Version, 10, 64)
		if err != nil {
			t.Fatalf("stamp version %q: %v", stamp.Version, err)
		}
		serverV, err := strconv.ParseUint(server, 10, 64)
		if err != nil {
			t.Fatalf("server version %q: %v", server, err)
		}
		if stampV > serverV {
			t.Fatalf("snapshot stamp %d is newer than the server's %d", stampV, serverV)
		}
	})
}

// TestPendingSnapshot_CounterLastHasAFalseUnchanged is the red check: a test-local counter-last copy
// yields a false unchanged, which is why the order is normative.
func TestPendingSnapshot_CounterLastHasAFalseUnchanged(t *testing.T) {
	rt := pendingFixture()
	rt.bus.pendingPerms.Add(1, permNeeded("c1", 1))
	counterLast := func(afterPerms func()) ([]marotte.ServerEvent, string) {
		events := rt.bus.pendingPerms.List("")
		afterPerms()
		events = append(events, rt.runs.asks.List("")...)
		events = append(events, rt.bus.steers.List("")...)
		version, _ := rt.versions.Current(subject.KindPending, "")
		return events, version
	}
	events, version := counterLast(func() { rt.bus.pendingPerms.Add(2, permNeeded("c1", 2)) })
	server, _ := rt.versions.Current(subject.KindPending, "")
	if version != server {
		t.Fatalf("counter-last stamp %q != server %q; the demonstration needs them equal", version, server)
	}
	if len(events) != 1 {
		t.Fatalf("counter-last snapshot holds %d items; the demonstration needs the added item missing", len(events))
	}
	// One item at the server's version: the false unchanged.
	if got := len(rt.bus.pendingPerms.List("")); got != 2 {
		t.Fatalf("server holds %d permissions, want 2", got)
	}
}

// TestPendingSnapshot_EmptySetIsOneFrameWithItems pins `items: []`, never null, at the unminted version.
func TestPendingSnapshot_EmptySetIsOneFrameWithItems(t *testing.T) {
	rt := pendingFixture()
	payload, stamp := rt.pendingSnapshotStamped()
	if payload.Items == nil || len(payload.Items) != 0 {
		t.Errorf("empty snapshot Items = %#v, want an empty non-nil slice", payload.Items)
	}
	want := marotte.SubjectStamp{Kind: "pending", Version: subject.Unminted}
	if stamp == nil || *stamp != want {
		t.Errorf("empty snapshot stamp = %+v, want %+v", stamp, want)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"items":[]}` {
		t.Errorf("empty snapshot JSON = %s, want {\"items\":[]}", data)
	}
}
