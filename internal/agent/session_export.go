package agent

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/marotte/internal/chat"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/ids"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"golang.org/x/sys/unix"
)

// kasExportDirPrefix is the mkdtemp prefix KAS's handleExportSession writes under.
const kasExportDirPrefix = "kiro-exports-"

// errExportNotOurs is a returned path outside KAS's export directory: never read,
// never deleted.
var errExportNotOurs = errors.New("session export path is outside the KAS export directory")

type sessionExportResult struct {
	FilePath string `json:"filePath"`
	Error    string `json:"error"`
	Success  bool   `json:"success"`
}

// handleKiroSessionExport serves every session in the chat's chain as one zip. A segment KAS refuses in band is
// skipped with a Warn; a transport failure or unconfined path fails the export.
func (rt *Runtime) handleKiroSessionExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !ids.ValidChatID(id) {
		httpreply.BadRequest(w, ids.ErrMsgInvalidChatID)
		return
	}
	chatID := marotte.ChatID(id)
	c, ok := rt.chatStore.Get(r.Context(), chatID)
	if !ok {
		httpreply.NotFound(w, "chat not found")
		return
	}

	rt.sessionExportMu.Lock()
	defer rt.sessionExportMu.Unlock()

	var segments []*kasExport
	defer func() {
		for _, e := range segments {
			e.remove()
		}
	}()
	for _, sid := range c.SessionChain() {
		e, err := rt.exportSession(r.Context(), chatID, sid)
		if err != nil {
			httpreply.ServerError(w, "Couldn't export the Kiro session", err)
			return
		}
		if e != nil {
			segments = append(segments, e)
		}
	}
	if len(segments) == 0 {
		httpreply.NotFound(w, "This chat has no Kiro session to download")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", chat.ExportDisposition(c.Name, chatID, ".kiro-session.zip"))
	if err := mergeSessionZips(w, segments); err != nil {
		// Headers are sent; the client sees a truncated download.
		slog.Warn("kiro session export: merge failed", "chat_id", chatID, "error", err)
	}
}

// exportSession asks KAS to export one session and opens the confined result; nil with nil error is a logged in-band refusal.
func (rt *Runtime) exportSession(ctx context.Context, chatID marotte.ChatID, sid string) (*kasExport, error) {
	raw, err := rt.callSessionExport(ctx, chatID, sid)
	if err != nil {
		return nil, err
	}
	var res sessionExportResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("session export %s: decode: %w", sid, err)
	}
	if !res.Success {
		slog.Warn("kiro session export: segment skipped", "chat_id", chatID,
			"session_id", sid, "reason", logsafe.Field(res.Error))
		return nil, nil
	}
	return openConfinedExport(res.FilePath, sid)
}

// Any process can answer from disk.
func (rt *Runtime) callSessionExport(ctx context.Context, chatID marotte.ChatID, sid string) (json.RawMessage, error) {
	if sb := rt.bridge.mgr.get(chatID); sb != nil && string(sb.SessionID()) == sid {
		resp, err := sb.Call(ctx, marotte.MethodSessionExport, map[string]any{marotte.KeySessionID: sid})
		if err != nil {
			return nil, fmt.Errorf("session export: %w", err)
		}
		if resp == nil {
			return nil, errors.New("session export: nil response")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("session export: %w", resp.Error)
		}
		return resp.Result, nil
	}
	return rt.utility.get().session.exportSessionRaw(ctx, sid)
}

// kasExport is one opened KAS export, holding the export directory and os.TempDir open so cleanup unlinks
// through the checked directories, not a swappable path.
type kasExport struct {
	f       *os.File
	dir     *os.File
	tmp     *os.File
	dirName string
	name    string
}

// openConfinedExport opens p only when it is the regular file kiro-session-<sid>.zip directly inside a real
// KAS export directory under os.TempDir, opening each level without following symlinks.
func openConfinedExport(p, sid string) (*kasExport, error) {
	p = filepath.Clean(p)
	dir := filepath.Dir(p)
	dirName, name := filepath.Base(dir), filepath.Base(p)
	if !filepath.IsAbs(p) || filepath.Dir(dir) != filepath.Clean(os.TempDir()) ||
		!strings.HasPrefix(dirName, kasExportDirPrefix) || name != "kiro-session-"+sid+".zip" {
		return nil, fmt.Errorf("%w: %s", errExportNotOurs, logsafe.Field(p))
	}
	tmp, err := os.OpenFile(filepath.Dir(dir), os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open temp dir: %w", err)
	}
	e := &kasExport{tmp: tmp, dirName: dirName, name: name}
	if e.dir, err = openAt(tmp, dirName, unix.O_DIRECTORY); err != nil {
		e.close()
		return nil, fmt.Errorf("%w: %s: %w", errExportNotOurs, logsafe.Field(dir), err)
	}
	if e.f, err = openAt(e.dir, name, unix.O_NONBLOCK); err != nil {
		e.close()
		return nil, fmt.Errorf("open session export: %w", err)
	}
	if fi, err := e.f.Stat(); err != nil || !fi.Mode().IsRegular() {
		e.close()
		return nil, fmt.Errorf("%w: not a regular file", errExportNotOurs)
	}
	return e, nil
}

// openAt opens name inside dir read-only, refusing a symlink at name.
func openAt(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|flags, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
}

func (e *kasExport) close() {
	for _, f := range []*os.File{e.f, e.dir, e.tmp} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// KAS prefixes entries with the session id, so names never collide.
func mergeSessionZips(w http.ResponseWriter, segments []*kasExport) error {
	zw := zip.NewWriter(w)
	for _, e := range segments {
		fi, err := e.f.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(e.f, fi.Size())
		if err != nil {
			return err
		}
		for _, entry := range zr.File {
			if err := zw.Copy(entry); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

func (e *kasExport) remove() {
	defer e.close()
	if err := unix.Unlinkat(int(e.dir.Fd()), e.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		slog.Warn("kiro session export: cleanup failed", "path", logsafe.Field(e.f.Name()), "error", err)
	}
	_ = unix.Unlinkat(int(e.tmp.Fd()), e.dirName, unix.AT_REMOVEDIR)
}
