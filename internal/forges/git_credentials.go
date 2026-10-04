// Legacy ~/.git-credentials scrubbing.
//
// An earlier tea integration wrote the forge token in cleartext into
// ~/.git-credentials and never removed it on disconnect. A stale line
// keeps the token on disk and answers git wherever the store helper is
// consulted, so every connect and disconnect scrubs the host's lines.

package forges

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v4"
)

// scrubCleartext is scrubGitCredentials for a path that must not fail on it.
func scrubCleartext(ctx context.Context, host string) {
	if err := scrubGitCredentials(ctx, host); err != nil {
		slog.Warn("forges: git-credentials scrub failed", "host", host, "error", err)
	}
}

// scrubGitCredentials removes every ~/.git-credentials line that
// carries a credential for host. Removing the file's last line removes
// the file. A missing file is a no-op.
func scrubGitCredentials(ctx context.Context, host string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	credFile := filepath.Join(home, ".git-credentials")
	data, err := os.ReadFile(credFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var kept []string
	changed := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}
		// Store-format lines look like https://user:token@host or
		// https://user:token@host/; match on the authority suffix.
		if strings.Contains(line, "@"+host+"/") || strings.HasSuffix(line, "@"+host) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	if len(kept) == 0 {
		return os.Remove(credFile)
	}
	_, err = atomicfile.WriteFile(ctx, credFile,
		[]byte(strings.Join(kept, "\n")+"\n"), atomicfile.WithMode(0o600))
	return err
}
