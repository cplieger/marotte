package runlease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func seedFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func asides(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, FileName+".unreadable-*"))
	if err != nil {
		t.Fatalf("glob asides: %v", err)
	}
	return matches
}

func TestNewStore_MovesAnUnreadLeaseFileAside(t *testing.T) {
	for name, body := range map[string]string{
		"malformed JSON":    `{"version":1,"leases":`,
		"a future version":  `{"version":99,"leases":[]}`,
		"the array shape":   `[{"workflow_id":"wf_1","origin":"manual"}]`,
		"zero length":       ``,
		"an unusable lease": `{"version":1,"leases":[{"workflow_id":"","origin":"manual"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			seedFile(t, dir, body)

			s, _ := NewStore(dir)
			got := asides(t, dir)
			if len(got) != 1 || s.Unread() != got[0] {
				t.Fatalf("asides %v with Unread() %q, want one aside named by Unread", got, s.Unread())
			}
			if kept, err := os.ReadFile(got[0]); err != nil || string(kept) != body {
				t.Errorf("the aside holds %q (%v), want the unread bytes", kept, err)
			}
			if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_2", Origin: OriginManual}); err != nil {
				t.Errorf("Put after the move aside = %v, want the store to persist", err)
			}
		})
	}
}

func TestNewStore_AReadLeaseFileIsNotUnread(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, `{"version":1,"leases":[{"workflow_id":"wf_1","origin":"manual"}]}`)
	s, err := NewStore(dir)
	if err != nil || s.Unread() != "" {
		t.Errorf("NewStore over a readable file = %v with Unread() %q, want nil and empty", err, s.Unread())
	}
}

func TestNewStore_AnAsideKeepsTheNextBootUnreadAfterAPut(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "nonsense")
	first, _ := NewStore(dir)
	aside := first.Unread()
	if err := first.Put(t.Context(), &Lease{WorkflowID: "wf_new", Origin: OriginManual}); err != nil {
		t.Fatalf("Setup: Put: %v", err)
	}

	second, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore after a clean write: %v", err)
	}
	if second.Unread() != aside {
		t.Errorf("Unread() on the next boot = %q, want the aside %q: a new lease must not read as all the leases", second.Unread(), aside)
	}
	if _, ok := second.Get("wf_new"); !ok {
		t.Error("the lease written after the aside did not load")
	}
}

func TestNewStore_RefusesToWriteOverALeaseFileItCouldNotMoveAside(t *testing.T) {
	restore := moveAside
	moveAside = func(string) (string, error) { return "", errors.New("rename: read-only file system") }
	t.Cleanup(func() { moveAside = restore })
	dir := t.TempDir()
	seedFile(t, dir, "nonsense")

	s, _ := NewStore(dir)
	if want := filepath.Join(dir, FileName); s.Unread() != want {
		t.Errorf("Unread() = %q, want %q", s.Unread(), want)
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); !errors.Is(err, errWriteRefused) {
		t.Errorf("Put = %v, want errWriteRefused", err)
	}
	if kept, _ := os.ReadFile(filepath.Join(dir, FileName)); string(kept) != "nonsense" {
		t.Errorf("runs.json = %q after a refused write, want the unread bytes", kept)
	}
}

func TestNewStore_AListingFailureRefusesLeaseWrites(t *testing.T) {
	restore := findAside
	findAside = func(string, string) (string, error) { return "", errors.New("readdir: input/output error") }
	t.Cleanup(func() { findAside = restore })
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err == nil || s.Unread() != dir {
		t.Errorf("NewStore with the listing failing = %v with Unread() %q, want a diagnostic and %q", err, s.Unread(), dir)
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); !errors.Is(err, errWriteRefused) {
		t.Errorf("Put = %v, want errWriteRefused", err)
	}
}

func TestStore_PutRefusesAWorkflowIDItsLoadWouldRefuse(t *testing.T) {
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "../x", Origin: OriginManual}); err == nil {
		t.Error("Put(../x) = nil, want a refusal")
	}
}
