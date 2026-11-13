package composition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// marotteTitle is the clientInfo.title marotte declares (testdata/initialize.golden), which KAS stamps as clientName.
const marotteTitle = "Marotte for Kiro"

// leaseStamp is a v:1 stamp as KAS's stampFor writes it, from an engine on another host, with overrides applied; a
// nil override drops the member.
func leaseStampJSON(t *testing.T, overrides map[string]any) string {
	t.Helper()
	m := map[string]any{
		"v": 1, "pid": 961, "parentPid": 920, "instanceId": "4f1c-a2", "hostId": "a-recreated-container",
		"acquiredAt": "2026-11-13T09:00:00.000Z", "token": "9b7e-11", "clientName": marotteTitle,
	}
	for k, v := range overrides {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// paddedTo pads a JSON document with trailing spaces, which JSON.parse accepts, to exactly n bytes.
func paddedTo(t *testing.T, doc string, n int) string {
	t.Helper()
	if len(doc) > n {
		t.Fatalf("stamp is %d bytes, over the %d asked for", len(doc), n)
	}
	return doc + strings.Repeat(" ", n-len(doc))
}

func writeLease(content string) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSweepOwnSessionLeases(t *testing.T) {
	logs := captureClaimLogs(t)
	own := leaseStampJSON(t, nil)
	for _, tc := range []struct {
		name, file string
		plant      func(t *testing.T, path string)
		removed    bool
	}{
		{"a marotte lease from another host", "0a1b.lease", writeLease(own), true},
		{"a marotte lease lock", "0a1b.lease.lock", writeLease(own), true},
		{"a marotte break marker", "0a1b.lease.lock.5e2f.break", writeLease(own), true},
		{"a marotte stamp of exactly KAS's 4096-byte cap", "0a1b.lease", writeLease(paddedTo(t, own, 4096)), true},
		{"a kiro-cli TUI lease", "0a1b.lease", writeLease(leaseStampJSON(t, map[string]any{"clientName": "Kiro CLI"})), false},
		{"a stamp with no client name", "0a1b.lease", writeLease(leaseStampJSON(t, map[string]any{"clientName": nil})), false},
		{"a v2 stamp naming marotte", "0a1b.lease", writeLease(leaseStampJSON(t, map[string]any{"v": 2})), false},
		{"a marotte stamp KAS's schema refuses", "0a1b.lease", writeLease(leaseStampJSON(t, map[string]any{"pid": 0})), false},
		{"a marotte stamp with no parent pid", "0a1b.lease", writeLease(leaseStampJSON(t, map[string]any{"parentPid": nil})), false},
		{"an unparseable file", "0a1b.lease", writeLease(`{"v":1,"clientName":"Marotte for Kiro"`), false},
		{"a marotte stamp one byte over the cap", "0a1b.lease", writeLease(paddedTo(t, own, 4097)), false},
		{"a directory", "0a1b.lease", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a symlink to a marotte stamp", "0a1b.lease", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "0a1b.lease")
			writeLease(own)(t, target)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.file)
			tc.plant(t, path)

			want := 0
			if tc.removed {
				want = 1
			}
			if got := sweepLeaseDir(dir); got != want {
				t.Errorf("sweepLeaseDir removed %d, want %d", got, want)
			}
			if _, err := os.Lstat(path); (err == nil) == tc.removed {
				t.Errorf("after the sweep %s exists = %v, want %v", tc.file, err == nil, !tc.removed)
			}
		})
	}

	t.Run("a missing directory", func(t *testing.T) {
		logs.Reset()
		if got := sweepLeaseDir(filepath.Join(t.TempDir(), "session-leases")); got != 0 {
			t.Errorf("sweepLeaseDir removed %d, want 0", got)
		}
		if strings.Contains(logs.String(), "level=WARN") {
			t.Errorf("a missing directory warned:\n%s", logs.String())
		}
	})
}

func TestSweepOwnSessionLeases_SkipsWithoutAFullKiroHomeClaim(t *testing.T) {
	captureClaimLogs(t)
	plant := func(t *testing.T) (home, lease string) {
		t.Helper()
		home = t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "session-leases"), 0o700); err != nil {
			t.Fatal(err)
		}
		lease = filepath.Join(home, "session-leases", "0a1b.lease")
		writeLease(leaseStampJSON(t, nil))(t, lease)
		return home, lease
	}
	for _, tc := range []struct {
		name  string
		claim func(t *testing.T, home string) *heldRoots
		swept bool
	}{
		{"a full kiro_home claim sweeps", func(t *testing.T, home string) *heldRoots {
			return mustClaim(t, ownedRoot{roleKiroHome, home})
		}, true},
		{"an unusable lock file", func(t *testing.T, home string) *heldRoots {
			if err := os.Mkdir(filepath.Join(home, lockFileName), 0o700); err != nil {
				t.Fatal(err)
			}
			return mustClaim(t, ownedRoot{roleKiroHome, home})
		}, false},
		{"no kiro_home claim", func(t *testing.T, _ string) *heldRoots {
			return mustClaim(t, ownedRoot{roleConfigDir, t.TempDir()})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, lease := plant(t)
			sweepOwnSessionLeases(tc.claim(t, home))
			if _, err := os.Stat(lease); (err != nil) != tc.swept {
				t.Errorf("after the sweep the planted lease is gone = %v, want %v", err != nil, tc.swept)
			}
		})
	}
}
