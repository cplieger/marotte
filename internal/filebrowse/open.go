package filebrowse

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/webhttp/v3"
)

// readable is a file opened for a read route. The caller closes f.
type readable struct {
	f    *os.File
	info fs.FileInfo
	abs  string
	// readOnly marks a KAS tool output, which the editor never offers to edit.
	readOnly bool
}

// afterPolicy is a test seam: it runs between the policy check and the open, where a swap
// would race the descent.
var afterPolicy = func(string) {}

// openForRead is the one confined open behind every read route: a KAS tool output
// through its pinned descent, anything else through the policy check and then a
// component-by-component O_NOFOLLOW descent, so an in-root symlink swapped in after the
// check cannot redirect the read (an os.Root open would follow it). It writes the
// refusal itself and reports false.
func (h *Handler) openForRead(w http.ResponseWriter, reqPath string) (readable, bool) {
	if out, granted, err := h.openToolOutput(reqPath); granted {
		if err != nil {
			writeOpenError(w, out.abs, err)
			return readable{}, false
		}
		return readable{f: out.f, info: out.info, abs: out.abs, readOnly: true}, true
	}
	l, ok := h.resolveOrForbid(w, reqPath)
	if !ok {
		return readable{}, false
	}
	afterPolicy(l.abs)
	f, err := openPinnedRoot(l)
	if err != nil {
		writeOpenError(w, l.abs, err)
		return readable{}, false
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		writeOpenError(w, l.abs, err)
		return readable{}, false
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		if info.IsDir() {
			httpreply.BadRequest(w, "path is a directory")
		} else {
			httpreply.BadRequest(w, "not a regular file")
		}
		return readable{}, false
	}
	return readable{f: f, info: info, abs: l.abs}, true
}

// writeOpenError maps an open or read failure onto the status the client branches on. A
// cancelled request is silent: nobody is left to read it.
func writeOpenError(w http.ResponseWriter, abs string, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, fs.ErrNotExist):
		httpreply.NotFound(w, "not found")
	case isSwapRefusal(err), errors.Is(err, errChanged):
		writeChanged(w, "")
	default:
		slog.Warn("filebrowse: read failed", "path", logsafe.Field(abs), "error", logsafe.Field(err.Error()))
		webhttp.WriteJSONStatus(w, http.StatusInternalServerError, httpreply.ErrorJSON(errReadFailed))
	}
}

// writeChanged answers a read that could not hold the file still, or whose identity is not
// the one the client asked for; id is the current identity when one was read.
func writeChanged(w http.ResponseWriter, id string) {
	webhttp.WriteJSONStatus(w, http.StatusConflict, FileRefusal{
		Error:  "This file is changing too fast to show. Download it instead.",
		Code:   RefusalChanged,
		FileID: id,
	})
}
