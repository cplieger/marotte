package runlease

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStore_RoundTripsALeaseAcrossARestart pins lease durability: a run outlives the process.
func TestStore_RoundTripsALeaseAcrossARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	slot := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	want := Lease{
		StartedAt:    time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Deadline:     slot,
		SlotAt:       slot,
		WorkflowID:   "wf_1",
		Recipe:       "publish",
		Origin:       OriginScheduled,
		ScheduleID:   "sched-1",
		Life:         "life-1",
		KeepChats:    []string{"chat-a", "chat-b"},
		Unattended:   true,
		Unattributed: true,
	}
	if err := s.Put(t.Context(), &want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("wf_1")
	if !ok {
		t.Fatal("the lease did not survive the restart, so the run is unbounded and unattributed")
	}
	if got.Recipe != want.Recipe || got.Origin != want.Origin || got.ScheduleID != want.ScheduleID {
		t.Errorf("lease = %+v, want recipe/origin/schedule from %+v", got, want)
	}
	if !got.Unattended {
		t.Error("the unattended mark did not survive; the permission floor would not arm at 03:00")
	}
	if !got.SlotAt.Equal(slot) {
		t.Errorf("SlotAt = %v, want %v; a re-arm could not re-apply the slot bound", got.SlotAt, slot)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if got.Life != "life-1" || !slices.Equal(got.KeepChats, want.KeepChats) || !got.Unattributed {
		t.Errorf("life/keep/unattributed = %q/%v/%v, want %q/%v/true; a restart lost the run's keep state",
			got.Life, got.KeepChats, got.Unattributed, want.Life, want.KeepChats)
	}
	// The DEADLINE must NOT survive: it was set by a dead process.
	if got.Bounded() {
		t.Errorf("the reloaded lease carries deadline %v; a stale deadline would cancel a run "+
			"the moment it resumed", got.Deadline)
	}
}

// TestStore_FileShapeIsAVersionedObject pins that the file carries a version from the first write.
func TestStore_FileShapeIsAVersionedObject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(t.Context(), &Lease{
		StartedAt: time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Recipe:    "publish", WorkflowID: "wf_1", Origin: OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read %s: %v", FileName, err)
	}
	body := string(raw)
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("the file is not a JSON object, so it has nowhere to carry a version:\n%s", body)
	}
	if !strings.Contains(body, `"version": 1`) {
		t.Errorf("the file carries no version field:\n%s", body)
	}
	if !strings.Contains(body, `"leases"`) {
		t.Errorf("the file carries no leases array:\n%s", body)
	}
	// A parked lease must not write a zero timestamp a reader has to interpret.
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("a zero time reached the file:\n%s", body)
	}
}

// TestStore_RoundTripsTheLaunchingChat: the chat id is the live-runs projection's payload.
func TestStore_RoundTripsTheLaunchingChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(t.Context(), &Lease{
		StartedAt: time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Recipe:    "publish", WorkflowID: "wf_1", ChatID: "c-live", Origin: OriginAgent,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("wf_1")
	if !ok {
		t.Fatal("the lease did not survive the restart")
	}
	if got.ChatID != "c-live" {
		t.Errorf("ChatID = %q after a restart, want %q", got.ChatID, "c-live")
	}
}

// TestNewStore_APreUpgradeRowDecodesWithAnEmptyChatID pins ChatID as additive at version 1:
// an empty chat id already means "no chat"; a version bump would discard every live lease.
func TestNewStore_APreUpgradeRowDecodesWithAnEmptyChatID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"version":1,"leases":[{"started_at":"2026-08-01T03:00:00Z",` +
		`"workflow_id":"wf_old","recipe":"nightly","origin":"scheduled",` +
		`"schedule_id":"sched-1","unattended":true}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore rejected a version-1 file a pre-upgrade build wrote: %v", err)
	}
	got, ok := s.Get("wf_old")
	if !ok {
		t.Fatal("the pre-upgrade lease was dropped; its run is unbounded and its recipe reads idle")
	}
	if got.ChatID != "" {
		t.Errorf("ChatID = %q for a row written before the field existed, want empty", got.ChatID)
	}
	if got.Recipe != "nightly" || got.Origin != OriginScheduled || !got.Unattended {
		t.Errorf("the pre-upgrade row lost fields it did carry: %+v", got)
	}
}

// TestStore_RejectsAVersionItDoesNotKnow pins discard-not-refuse: a usable empty store plus
// an error the caller logs.
func TestStore_RejectsAVersionItDoesNotKnow(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"a future version":                    `{"version":99,"leases":[{"workflow_id":"wf_1","origin":"manual"}]}`,
		"no version at all":                   `{"leases":[{"workflow_id":"wf_1","origin":"manual"}]}`,
		"the array shape schedules.json uses": `[{"workflow_id":"wf_1","origin":"manual"}]`,
		"malformed JSON":                      `{"version":1,"leases":`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s, err := NewStore(dir)
			if err == nil {
				t.Error("the store accepted a file it cannot reason about, and its leases are now live")
			}
			if s == nil {
				t.Fatal("NewStore returned no store; the runtime would have no lease registry at all")
			}
			if got := len(s.List()); got != 0 {
				t.Errorf("the store kept %d leases from an unreadable file", got)
			}
			// Still writable, so the next launch is bounded.
			if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_2", Origin: OriginManual}); err != nil {
				t.Errorf("the store refused a fresh lease after a bad read: %v", err)
			}
			if _, ok := s.Get("wf_2"); !ok {
				t.Error("the fresh lease did not land")
			}
		})
	}
}

// TestStore_RefusesALeaseItCouldNotActOn: a lease with no id has no key, and an unknown
// origin says neither whether the run is sweepable nor whether it is unattended.
func TestStore_RefusesALeaseItCouldNotActOn(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{Origin: OriginManual}); err == nil {
		t.Error("a lease with no workflow id was accepted")
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1"}); err == nil {
		t.Error("a lease with no origin was accepted")
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: "tui"}); err == nil {
		t.Error("a lease with an unknown origin was accepted")
	}
	if got := len(s.List()); got != 0 {
		t.Errorf("a refused lease landed anyway: %d held", got)
	}
}

// TestStore_ALeaseItCannotActOnMovesTheFileAside: a hand-edited or partial record is not a sweep candidate, and
// dropping it alone would let the next write erase its keep holders, so the whole file goes aside.
func TestStore_ALeaseItCannotActOnMovesTheFileAside(t *testing.T) {
	t.Parallel()
	for name, bad := range map[string]string{
		"an empty workflow id":  `{"workflow_id":"","origin":"manual"}`,
		"a path as workflow id": `{"workflow_id":"../x","origin":"manual"}`,
		"an unknown origin":     `{"workflow_id":"wf_unknown_origin","origin":"tui"}`,
		"no origin":             `{"workflow_id":"wf_no_origin"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			body := `{"version":1,"leases":[{"workflow_id":"wf_good","origin":"manual","keep_chats":["c-a"]},` + bad + `]}`
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s, err := NewStore(dir)
			if err == nil {
				t.Error("NewStore over a lease it cannot act on = nil error, want a diagnostic")
			}
			if got := s.List(); len(got) != 0 {
				t.Errorf("held %+v, want none from a file moved aside", got)
			}
			if s.Unread() == "" {
				t.Fatal("Unread() is empty, want the aside")
			}
			kept, err := os.ReadFile(s.Unread())
			if err != nil || string(kept) != body {
				t.Errorf("the aside holds %q (%v), want the file unchanged", kept, err)
			}
		})
	}
}

// TestStore_ReleaseEndsOnlyTheLifeItNames: a release reading an older life leaves a newer grant's lease.
func TestStore_ReleaseEndsOnlyTheLifeItNames(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginAgent, Life: "life-2"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := s.Release(t.Context(), "wf_1", "life-1"); err != nil || got != ReleaseSuperseded {
		t.Errorf("Release(life-1) = %v, %v, want ReleaseSuperseded", got, err)
	}
	if _, ok := s.Get("wf_1"); !ok {
		t.Fatal("an older life's release deleted the newer life's lease")
	}
	if got, err := s.Release(t.Context(), "wf_1", "life-2"); err != nil || got != Released {
		t.Errorf("Release(life-2) = %v, %v, want Released", got, err)
	}
	if got, err := s.Release(t.Context(), "wf_1", "life-2"); err != nil || got != ReleaseAbsent {
		t.Errorf("Release of a gone lease = %v, %v, want ReleaseAbsent", got, err)
	}
}

// TestStore_KeepChatsOnlyGrow: SetKeepChats and Renew union their holders; a call naming fewer removes none.
func TestStore_KeepChatsOnlyGrow(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginAgent, KeepChats: []string{"c2"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	steps := []struct {
		name string
		do   func() error
		want []string
	}{
		{"add", func() error { return s.SetKeepChats(t.Context(), "wf_1", []string{"c1"}) }, []string{"c1", "c2"}},
		{"fewer", func() error { return s.SetKeepChats(t.Context(), "wf_1", nil) }, []string{"c1", "c2"}},
		{"renew", func() error { return s.Renew(t.Context(), "wf_1", "life-9", []string{"c3"}) }, []string{"c1", "c2", "c3"}},
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if got, _ := s.Get("wf_1"); !slices.Equal(got.KeepChats, st.want) {
			t.Errorf("%s: KeepChats = %v, want %v", st.name, got.KeepChats, st.want)
		}
	}
	if got, _ := s.Get("wf_1"); got.Life != "life-9" {
		t.Errorf("Renew left life %q, want life-9", got.Life)
	}
	if err := s.SetKeepChats(t.Context(), "wf_gone", []string{"c1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetKeepChats(wf_gone) = %v, want ErrNotFound", err)
	}
}

// TestNewStore_DropsAnInvalidKeepHolderAndAnOldAbsenceClock: a holder naming no chat is dropped on load, and a dev
// file's first_absent_at loads and is gone after the next write.
func TestNewStore_DropsAnInvalidKeepHolderAndAnOldAbsenceClock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := `{"version":1,"leases":[{"workflow_id":"wf_1","origin":"agent","recipe":"r",` +
		`"first_absent_at":"2026-10-01T00:00:00Z","keep_chats":["ok-1","../x"]}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got, ok := s.Get("wf_1")
	if !ok || !slices.Equal(got.KeepChats, []string{"ok-1"}) {
		t.Fatalf("lease = %+v, %v, want KeepChats [ok-1]", got, ok)
	}
	if err := s.SetUnattributed(t.Context(), "wf_1", true); err != nil {
		t.Fatalf("SetUnattributed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "first_absent_at") {
		t.Errorf("the rewritten file still carries first_absent_at:\n%s", data)
	}
}

// TestStore_SetDeadlineIsTheReArm: every start re-stamps, every pause parks, and a lease
// that is gone cannot be re-armed.
func TestStore_SetDeadlineIsTheReArm(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	const id = "wf_1"
	if err := s.Put(t.Context(), &Lease{WorkflowID: id, Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	first := time.Now().Add(time.Hour)
	if err := s.SetDeadline(t.Context(), id, first); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if got, _ := s.Get(id); !got.Deadline.Equal(first) {
		t.Errorf("deadline = %v, want %v", got.Deadline, first)
	}

	if err := s.SetDeadline(t.Context(), id, time.Time{}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if got, _ := s.Get(id); got.Bounded() {
		t.Error("a parked lease still reports bounded, so its timer could still cancel it")
	}

	// The resume gets a FRESH budget, not the first one's remainder.
	second := time.Now().Add(time.Hour)
	if err := s.SetDeadline(t.Context(), id, second); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if got, _ := s.Get(id); !got.Deadline.Equal(second) {
		t.Errorf("the resume did not take the fresh deadline: %v", got.Deadline)
	}

	if err := s.SetDeadline(t.Context(), "wf_gone", first); err == nil {
		t.Error("a released lease was re-armed, so a timer would outlive its run")
	}
}

// TestStore_ReleaseIsIdempotent: both the terminal frame and the cancel path
// release, and neither knows which arrived first.
func TestStore_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := s.Release(t.Context(), "wf_1", ""); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := s.Release(t.Context(), "wf_1", ""); err != nil {
		t.Errorf("a second release failed: %v", err)
	}
	if _, err := s.Release(t.Context(), "", ""); err != nil {
		t.Errorf("releasing the empty id failed: %v", err)
	}
	if _, ok := s.Get("wf_1"); ok {
		t.Error("the lease survived its release")
	}
}

// TestStore_ListIsOrdered keeps the file's own bytes stable across rewrites, so a
// diff of runs.json shows a change rather than a reshuffle.
func TestStore_ListIsOrdered(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	for _, id := range []string{"wf_c", "wf_a", "wf_b"} {
		if err := s.Put(t.Context(), &Lease{WorkflowID: id, Origin: OriginManual}); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	got := s.List()
	for i, want := range []string{"wf_a", "wf_b", "wf_c"} {
		if got[i].WorkflowID != want {
			t.Errorf("List()[%d] = %q, want %q", i, got[i].WorkflowID, want)
		}
	}
}

// TestStore_MemoryPersistsNothing pins the zero-path branch a test agent relies on:
// no file is created, and nothing fails for the want of one.
func TestStore_MemoryPersistsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the memory store wrote %d files", len(entries))
	}
}

// TestStore_WritesA0600File covers a permissive umask and a mode widened between writes.
// Not parallel: it sets the process umask.
func TestStore_WritesA0600File(t *testing.T) {
	dir := t.TempDir()
	// Restored before any assertion runs, so a failure cannot leak it into the package.
	prev := syscall.Umask(0)
	s, err := NewStore(dir)
	if err != nil {
		syscall.Umask(prev)
		t.Fatalf("NewStore: %v", err)
	}
	putErr := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual})
	syscall.Umask(prev)
	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	path := filepath.Join(dir, FileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s is %v under umask 000, want 0600: the mode a caller asks for is a REQUEST, "+
			"and this file's is meant to be enforced by the write", FileName, got)
	}

	// The next write corrects it, because the published inode is the temp's.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := s.Release(t.Context(), "wf_1", ""); err != nil {
		t.Fatalf("Release: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s stayed %v after a rewrite, so a widened mode survives every later write", FileName, got)
	}
}

// TestStore_ListStampedPairsTheSetWithItsVersion pins that a changing mutation moves the
// version, a no-op leaves it, and ListStamped reads both in one critical section.
func TestStore_ListStampedPairsTheSetWithItsVersion(t *testing.T) {
	s := NewMemory()
	if got, version := s.ListStamped(); len(got) != 0 || version != "0" {
		t.Fatalf("fresh ListStamped = (%d leases, %q), want (0, \"0\")", len(got), version)
	}
	ctx := t.Context()
	lease := Lease{WorkflowID: "wf_1", Origin: OriginManual, StartedAt: time.Now()}
	if err := s.Put(ctx, &lease); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, afterPut := s.ListStamped()
	if len(got) != 1 || afterPut != "1" {
		t.Fatalf("after Put: ListStamped = (%d leases, %q), want (1, \"1\")", len(got), afterPut)
	}
	deadline := time.Now().Add(time.Hour)
	if err := s.SetDeadline(ctx, "wf_1", deadline); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, v := s.ListStamped(); v != "2" {
		t.Errorf("after SetDeadline: version = %q, want \"2\"", v)
	}
	if err := s.SetDeadline(ctx, "wf_1", deadline); err != nil {
		t.Fatalf("SetDeadline repeat: %v", err)
	}
	if _, v := s.ListStamped(); v != "2" {
		t.Errorf("after an unchanged SetDeadline: version = %q, want \"2\" (nothing moved)", v)
	}
	if err := s.SetKeepChats(ctx, "wf_1", []string{"c1"}); err != nil {
		t.Fatalf("SetKeepChats: %v", err)
	}
	if _, v := s.ListStamped(); v != "3" {
		t.Errorf("after SetKeepChats: version = %q, want \"3\"", v)
	}
	if _, err := s.Release(ctx, "wf_1", ""); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got, afterRelease := s.ListStamped()
	if len(got) != 0 || afterRelease != "4" {
		t.Errorf("after Release: ListStamped = (%d leases, %q), want (0, \"4\")", len(got), afterRelease)
	}
	if _, err := s.Release(ctx, "wf_1", ""); err != nil {
		t.Fatalf("Release repeat: %v", err)
	}
	if _, v := s.ListStamped(); v != "4" {
		t.Errorf("after releasing a released lease: version = %q, want \"4\" (nothing moved)", v)
	}
}

// The parent session survives a restart: after one, it is the only host marotte reads the run's
// questions through.
func TestStore_SetParentSessionIDIsDurable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginAgent}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.SetParentSessionID(t.Context(), "wf_1", "sess_parent"); err != nil {
		t.Fatalf("SetParentSessionID: %v", err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ := reopened.Get("wf_1"); got.ParentSessionID != "sess_parent" {
		t.Errorf("after a restart ParentSessionID = %q, want sess_parent", got.ParentSessionID)
	}
	if err := s.SetParentSessionID(t.Context(), "wf_gone", "sess_parent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetParentSessionID on no lease = %v, want ErrNotFound", err)
	}
}

// TestStore_HostSecuritySurvivesAReopenAndIsNeverShared pins the parentless run's launch presets across a full
// restart, an empty Custom profile included, and that no caller can edit the stored copy.
func TestStore_HostSecuritySurvivesAReopenAndIsNeverShared(t *testing.T) {
	t.Parallel()
	for name, presets := range map[string][]string{
		"a profile's presets": {"preset:read-only", "preset:no-shell"},
		"the Custom profile":  {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s, err := NewStore(dir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			l := Lease{WorkflowID: "wf_1", Recipe: "r", Origin: OriginManual, Host: &HostSecurity{Presets: slices.Clone(presets)}}
			if err := s.Put(t.Context(), &l); err != nil {
				t.Fatalf("Put: %v", err)
			}
			l.Host.Presets = append(l.Host.Presets, "preset:widened")
			got, _ := s.Get("wf_1")
			if len(got.Host.Presets) != len(presets) {
				t.Errorf("an edit of the Put copy reached the store: %v", got.Host.Presets)
			}
			if len(got.Host.Presets) > 0 {
				got.Host.Presets[0] = "edited"
			}
			reopened, err := NewStore(dir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			back, ok := reopened.Get("wf_1")
			if !ok {
				t.Fatal("the lease did not survive the reopen")
			}
			reloaded, err := back.HostPresets()
			if err != nil {
				t.Fatalf("HostPresets after a reopen: %v", err)
			}
			if !slices.Equal(reloaded, presets) {
				t.Errorf("presets after a reopen = %v, want %v", reloaded, presets)
			}
		})
	}
}

// TestLease_HostPresetsRefusesAnUnrecordedLaunch: a parentless lease with no recorded security input fails closed.
func TestLease_HostPresetsRefusesAnUnrecordedLaunch(t *testing.T) {
	t.Parallel()
	for name, l := range map[string]Lease{
		"no host":             {WorkflowID: "wf_1", Origin: OriginManual},
		"a host with no list": {WorkflowID: "wf_1", Origin: OriginManual, Host: &HostSecurity{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := l.HostPresets(); !errors.Is(err, errNoHostSecurity) {
				t.Errorf("HostPresets() = %v, want errNoHostSecurity", err)
			}
		})
	}
	t.Run("an old file's record decodes with no host", func(t *testing.T) {
		dir := t.TempDir()
		body := `{"version":1,"leases":[{"workflow_id":"wf_1","recipe":"r","origin":"manual","parent_session_id":"sess_1"}]}`
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		l, _ := s.Get("wf_1")
		if _, err := l.HostPresets(); !errors.Is(err, errNoHostSecurity) {
			t.Errorf("an old record's HostPresets() = %v, want errNoHostSecurity", err)
		}
	})
}
