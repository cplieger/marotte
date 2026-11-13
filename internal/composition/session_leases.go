package composition

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	"github.com/cplieger/marotte/internal/kascap"
)

// sessionLeasesDir is where KAS keeps one lease per session it writes, beside its sessions dir.
const sessionLeasesDir = "session-leases"

// leaseStampMax is KAS's parseStamp cap: a longer file is no stamp to KAS either.
const leaseStampMax = 4096

// leaseStamp is the v:1 stamp KAS writes into every lease, lease lock and break marker (its stampFor).
type leaseStamp struct {
	PID        *int   `json:"pid"`
	ParentPID  *int   `json:"parentPid"`
	InstanceID string `json:"instanceId"`
	HostID     string `json:"hostId"`
	AcquiredAt string `json:"acquiredAt"`
	Token      string `json:"token"`
	ClientName string `json:"clientName"`
	V          int    `json:"v"`
}

// leaseIDRe is KAS's instanceId and token schema.
var leaseIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// valid applies the constraints of KAS's stamp schema, so a file KAS would not read as a stamp is
// left to KAS.
func (s *leaseStamp) valid() bool {
	const maxInt31 = 1<<31 - 1
	return s.V == 1 && s.PID != nil && *s.PID >= 1 && *s.PID <= maxInt31 &&
		s.ParentPID != nil && *s.ParentPID >= 0 && *s.ParentPID <= maxInt31 &&
		leaseIDRe.MatchString(s.InstanceID) && leaseIDRe.MatchString(s.Token) &&
		inRuneRange(s.HostID, 255) && inRuneRange(s.AcquiredAt, 40)
}

func inRuneRange(s string, maxLen int) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= maxLen
}

// sweepOwnSessionLeases removes the session leases a dead marotte engine left in KIRO_HOME, which
// KAS would otherwise read as live for up to 135 s and refuse every write to those sessions. It
// runs only under a full kiro_home claim, before the first engine start: no other marotte, so no
// other marotte engine, can be writing this home then, and only marotte stamps kascap.ClientTitle,
// so every such stamp was written by an engine that is gone.
func sweepOwnSessionLeases(roots *heldRoots) {
	home, ok := roots.fullyHeld(roleKiroHome)
	if !ok {
		return
	}
	if n := sweepLeaseDir(filepath.Join(home, sessionLeasesDir)); n > 0 {
		slog.Info("cleared the session leases a stopped marotte engine left, so its sessions take writes at once",
			"removed", n)
	}
}

// sweepLeaseDir removes every regular file in dir whose content is a valid stamp naming
// kascap.ClientTitle and answers how many went. The rule is on content, not name, so a lease, its
// lock and a break marker go alike; anything else is KAS's to judge.
func sweepLeaseDir(dir string) (removed int) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		slog.Warn("could not read kiro-cli's session leases, so a session a stopped engine held refuses writes for up to 135 s",
			"path", dir, "error", err)
		return 0
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !isOwnLease(path) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("could not remove a session lease a stopped marotte engine left, so its session refuses writes for up to 135 s",
				"path", path, "error", err)
			continue
		}
		removed++
	}
	return removed
}

func isOwnLease(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, leaseStampMax+1))
	if err != nil || len(raw) > leaseStampMax {
		return false
	}
	var s leaseStamp
	if json.Unmarshal(raw, &s) != nil || !s.valid() {
		return false
	}
	return s.ClientName == kascap.ClientTitle
}
