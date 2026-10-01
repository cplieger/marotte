package mcp

import (
	"context"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// autoApproveServer is one stdio server carrying BOTH tool lists, so every case
// below can check the suspended field and the unaffected one from one fixture.
func autoApproveServer() *Server {
	return &Server{
		Name:          "everything",
		Transport:     TransportStdio,
		Command:       "npx",
		Enabled:       true,
		DisabledTools: []string{"delete_repo"},
		AutoApprove:   []string{"search_repos"},
	}
}

// TestRenderKASServers_AutoApprovePostureDecidesTheField is the render half of the
// per-rung table in internal/policyfile: the decision arrives resolved, and false
// means the field is ABSENT rather than empty. Absent is what KAS reads as "this
// server auto-approves nothing"; an explicit empty array is a different
// declaration, and `omitempty` is what makes the two spellings distinguishable.
//
// `disabledTools` is asserted in BOTH arms and that is the load-bearing half. It
// NARROWS what a server may do, so a rung that suspends a widening has no reason to
// drop a restriction — and dropping it would silently re-enable every tool the user
// had turned off, which is the mirror image of the defect being fixed.
func TestRenderKASServers_AutoApprovePostureDecidesTheField(t *testing.T) {
	for _, tc := range []struct {
		name          string
		honour        bool
		wantApprove   []string
		wantApprovePr bool
	}{
		{"honoured: the user's chips reach the agent", true, []string{"search_repos"}, true},
		{"suspended: no autoApprove key at all", false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderKASServers([]*Server{autoApproveServer()}, tc.honour)
			entry, ok := got["everything"]
			if !ok {
				t.Fatalf("the server was not rendered: %v", got)
			}
			if (entry.AutoApprove != nil) != tc.wantApprovePr {
				t.Errorf("AutoApprove = %v, want present = %t", entry.AutoApprove, tc.wantApprovePr)
			}
			if !slices.Equal(entry.AutoApprove, tc.wantApprove) {
				t.Errorf("AutoApprove = %v, want %v", entry.AutoApprove, tc.wantApprove)
			}
			if !slices.Equal(entry.DisabledTools, []string{"delete_repo"}) {
				t.Errorf("DisabledTools = %v, want the stored list on every rung: a suspended widening must not drop a restriction",
					entry.DisabledTools)
			}
		})
	}
}

// TestStore_NoAutoApproveResolverSuspends pins the fail-closed default. A
// composition that never wires WithAutoApprove renders what the ladder's own
// default rung renders, so a forgotten wire cannot silently WIDEN every instance —
// which is the direction the pre-suspension behaviour would have preserved.
func TestStore_NoAutoApproveResolverSuspends(t *testing.T) {
	s, kasPath := newIsolatedStore(t)
	if _, err := s.Create(t.Context(), autoApproveServer()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entry := readKASServers(t, kasPath)["everything"]
	if _, ok := entry["autoApprove"]; ok {
		t.Errorf("an unwired store rendered autoApprove = %v; a missing resolver must suspend, not honour", entry["autoApprove"])
	}
	if _, ok := entry["disabledTools"]; !ok {
		t.Errorf("an unwired store dropped disabledTools: %v", entry)
	}
}

// TestStore_AutoApproveIsResolvedPerWrite is what makes a profile change reach the
// chats already running: the posture is read on every render rather than captured
// at construction, so RenderKASConfig after a selection renders the rung the user
// just picked. A captured value would render the boot-time posture for the life of
// the process, which is a suspension that never happens.
//
// It also pins that the suspension is RENDER-ONLY: the store's own record still
// carries the chips afterwards, so tightening the profile does not delete what the
// user typed and loosening it restores the grant with no re-entry.
func TestStore_AutoApproveIsResolvedPerWrite(t *testing.T) {
	var honour atomic.Bool
	honour.Store(true)

	dir := t.TempDir()
	kasPath := dir + "/kas-mcp.json"
	s, err := New(t.Context(), dir, nil,
		WithKASConfigPath(kasPath),
		WithAutoApprove(func(context.Context) bool { return honour.Load() }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Create(t.Context(), autoApproveServer()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := readKASServers(t, kasPath)["everything"]["autoApprove"]; !ok {
		t.Fatalf("the honouring posture rendered no autoApprove: %v", readKASServers(t, kasPath))
	}

	// The profile selection: the setting flips, then the handler re-renders.
	honour.Store(false)
	if err := s.RenderKASConfig(t.Context()); err != nil {
		t.Fatalf("RenderKASConfig: %v", err)
	}
	entry := readKASServers(t, kasPath)["everything"]
	if _, ok := entry["autoApprove"]; ok {
		t.Errorf("autoApprove = %v after the posture flipped; the answer was captured rather than resolved per write",
			entry["autoApprove"])
	}
	if _, ok := entry["disabledTools"]; !ok {
		t.Errorf("the re-render dropped disabledTools: %v", entry)
	}

	stored := s.List(t.Context())
	if len(stored) != 1 || !slices.Equal(stored[0].AutoApprove, []string{"search_repos"}) {
		t.Errorf("the store's own record = %+v, want the chips intact: a suspension renders less, it does not delete the user's list", stored)
	}

	// And back: loosening the rung restores the grant with nothing re-typed.
	honour.Store(true)
	if err := s.RenderKASConfig(t.Context()); err != nil {
		t.Fatalf("RenderKASConfig (restore): %v", err)
	}
	if _, ok := readKASServers(t, kasPath)["everything"]["autoApprove"]; !ok {
		t.Errorf("loosening the rung did not restore autoApprove: %v", readKASServers(t, kasPath))
	}
}

// renderNotifyStore builds an isolated store whose change callback sends on an
// UNBUFFERED channel. Unbuffered is the point rather than a detail: the sender
// parks until the test receives, so a notification the code should not have made
// is still waiting to be observed rather than having been dropped into a buffer
// nobody drains — which is what makes the negative case below able to fail.
func renderNotifyStore(t *testing.T) (*Store, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	changed := make(chan struct{})
	s, err := New(t.Context(), dir, func(context.Context) { changed <- struct{}{} },
		WithKASConfigPath(filepath.Join(dir, "kas", "mcp.json")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, changed
}

// TestRenderKASConfig_NotifiesTheChangeCallback is the FRESHNESS half of the
// wiring. A profile selection reaches KAS's file through this method, and the
// panel rendering the chips that file just suspended has no other trigger — its
// own settings-tab loader fires on FIRST activation only, and nothing else
// refetches. The render therefore fires the change callback, which the
// composition root already turns into the broadcast the panel refetches on.
//
// That WIDENS the callback's meaning from "the persisted set mutated" to "what
// GET /api/mcp answers changed"; the widening is deliberate and is what its one
// client-side consumer actually does with it.
func TestRenderKASConfig_NotifiesTheChangeCallback(t *testing.T) {
	s, changed := renderNotifyStore(t)

	go func() {
		if err := s.RenderKASConfig(t.Context()); err != nil {
			t.Errorf("RenderKASConfig: %v", err)
		}
	}()

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("RenderKASConfig did not fire the change callback; a profile selection " +
			"then reaches KAS's file and never the panel rendering the suspended chips")
	}
}

// TestRenderKASConfig_ASpentWriteAnnouncesNothing is the arm worth having: a write
// that did NOT land must not announce a change, or a client refetches and adopts a
// posture the file does not carry — which is the same class of disagreement the
// endpoint reads its answer from the render's own resolver to avoid.
//
// The failure is forced through the context, which writeKASConfig checks before it
// touches the disk, so nothing is left half-written for the next case to trip on.
func TestRenderKASConfig_ASpentWriteAnnouncesNothing(t *testing.T) {
	s, changed := renderNotifyStore(t)

	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.RenderKASConfig(dead); err == nil {
		t.Fatal("RenderKASConfig on a cancelled context returned nil; the case cannot fail")
	}

	select {
	case <-changed:
		t.Fatal("a failed render fired the change callback; a client would refetch and " +
			"adopt a posture KAS's file does not carry")
	case <-time.After(200 * time.Millisecond):
	}
}
