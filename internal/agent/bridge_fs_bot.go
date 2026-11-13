package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cplieger/go/pathinside"
	"github.com/cplieger/marotte/internal/bot"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/marotte"
)

// confineFor is confine for a request sessionID sent on chatID's routing. Past confine's roots, a read also reaches
// the edit snapshots of the run sessionID is a step of (confineSnapshot), and every verb reaches the Bot's own
// directory from a session of its chain (confineBotDir). A path none admits reports confine's error.
func (in *inbound) confineFor(
	ctx context.Context, chatID marotte.ChatID, sessionID marotte.SessionID, p string, access fsAccess,
) (fsTarget, error) {
	target, err := in.lifetime.confine(ctx, p, access)
	if err == nil {
		return target, nil
	}
	if access == fsRead {
		if t, ok := in.confineSnapshot(sessionID, p); ok {
			return t, nil
		}
	}
	if t, ok := in.confineBotDir(ctx, chatID, sessionID, p, access); ok {
		return t, nil
	}
	return fsTarget{}, err
}

// confineBotDir confines an absolute p to the Bot's own directory when chatID is a started Bot with memory on and
// sessionID is in its session chain. The grant is keyed on the request's session, not the routing: a step of a run the
// Bot launched routes here under its own session id, and KAS gives a step session the launching session's roots.
func (in *inbound) confineBotDir(
	ctx context.Context, chatID marotte.ChatID, sessionID marotte.SessionID, p string, access fsAccess,
) (fsTarget, bool) {
	dir, ok := in.botDirFor(ctx, chatID, sessionID)
	if !ok || !filepath.IsAbs(p) {
		return fsTarget{}, false
	}
	rel, ok := relBelow(dir, filepath.Clean(p))
	if !ok {
		return fsTarget{}, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fsTarget{}, false
	}
	if !linkFreeBelow(root, botPaths().KiroHome, dir) || !linkFreeNames(root, rel, access) {
		_ = root.Close()
		return fsTarget{}, false
	}
	return fsTarget{root: root, rel: rel, opened: true}, true
}

// botDirFor is the directory of chatID's Bot when it is started, keeps a memory and holds sessionID in its chain. An id
// no chat can hold (a parentless run's routing, the observer pass's "") is refused before the store logs it invalid.
func (in *inbound) botDirFor(ctx context.Context, chatID marotte.ChatID, sessionID marotte.SessionID) (string, bool) {
	if !ids.ValidChatID(string(chatID)) {
		return "", false
	}
	rec, ok := in.coord.chatStore.Get(ctx, chatID)
	if !ok || !rec.IsStartedBot() || !rec.Bot.Memory || !slices.Contains(rec.SessionChain(), string(sessionID)) {
		return "", false
	}
	return bot.Dir(rec.ID, botPaths())
}

// relBelow is p's name below dir, spelled through dir or through what dir resolves to: KIRO_HOME may be reached
// through a link, and a path KAS sends may name either side of it.
func relBelow(dir, p string) (string, bool) {
	bases := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		bases = append(bases, resolved)
	}
	for _, base := range bases {
		if pathinside.Root(base).Contains(p) {
			rel, err := filepath.Rel(base, p)
			return rel, err == nil
		}
	}
	return "", false
}

// linkFreeNames reports that no name on rel's way down from root is a link, and that the last is a directory, a
// regular file or absent. os.Root follows a link that stays in root, and marotte's verbs never make one, so a link in
// a Bot's directory was planted by a shell. An unlink's last name may be a link: delete removes it, never its target.
func linkFreeNames(root *os.Root, rel string, access fsAccess) bool {
	if rel == "." {
		return true
	}
	names := strings.Split(rel, string(filepath.Separator))
	for i := range names {
		info, err := root.Lstat(filepath.Join(names[:i+1]...))
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		if err != nil {
			return false
		}
		last := i == len(names)-1
		if info.Mode()&fs.ModeSymlink != 0 {
			return last && access == fsUnlink
		}
		if last {
			return info.IsDir() || info.Mode().IsRegular()
		}
	}
	return true
}
