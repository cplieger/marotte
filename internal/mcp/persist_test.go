package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A save whose KAS write fails after the record landed leaves the record as it was before the save, so the server
// the caller reported as not saved is absent at the next boot too.
func TestPersist_ALateKASFailureRestoresTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
	}{
		{name: "an existing record", existing: true},
		{name: "no record yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			kasDir := filepath.Join(dir, "kas")
			var breakKAS atomic.Bool
			// The wait resolver runs between the record write and the KAS write: replacing KAS's directory with a
			// file there fails the KAS write alone (ENOTDIR, root-safe).
			s, err := New(t.Context(), dir, nil, withKASConfigPath(filepath.Join(kasDir, "mcp.json")),
				WithWaitForReady(func(context.Context) (bool, bool) {
					if breakKAS.Load() {
						if err := os.RemoveAll(kasDir); err != nil {
							t.Errorf("Setup: remove KAS's directory: %v", err)
						}
						if err := os.WriteFile(kasDir, nil, 0o600); err != nil {
							t.Errorf("Setup: block KAS's directory: %v", err)
						}
					}
					return false, true
				}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.existing {
				if _, err := s.Create(t.Context(), &Server{Name: "kept", Transport: TransportStdio, Command: "/bin/true"}); err != nil {
					t.Fatalf("Setup: Create(kept): %v", err)
				}
			} else if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
				t.Fatalf("Setup: remove the record: %v", err)
			}
			before, beforeErr := os.ReadFile(s.path)

			breakKAS.Store(true)
			_, err = s.Create(t.Context(), &Server{Name: "lost", Transport: TransportStdio, Command: "/bin/true"})

			if err == nil {
				t.Fatal("Create succeeded with KAS's file unwritable")
			}
			after, afterErr := os.ReadFile(s.path)
			switch {
			case tc.existing && (afterErr != nil || !bytes.Equal(after, before)):
				t.Errorf("record after the failed save = %q (%v), want the bytes before it %q", after, afterErr, before)
			case !tc.existing && (beforeErr == nil || !os.IsNotExist(afterErr)):
				t.Errorf("record after the failed save = %q (%v), want none, as before it", after, afterErr)
			}
		})
	}
}
