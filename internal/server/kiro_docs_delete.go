package server

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
)

// docDeleter removes one browsable path behind the file browser's delete guards.
type docDeleter interface {
	Delete(reqPath string) error
}

var _ docDeleter = (*filebrowse.Handler)(nil)

// kiroDocDeleteRequest is POST /api/workspace/kiro-docs/delete's body: a row's `path` verbatim.
type kiroDocDeleteRequest struct {
	Path string `json:"path"`
}

// errDocLinked refuses a target whose own final component is a symlink: the browser's delete
// resolves the link and would remove what it points at.
var errDocLinked = errors.New("kiro docs: target is a symlink")

// handleKiroDocDelete removes everything one listed row was built from, so the next listing
// has no such row.
func (s *Server) handleKiroDocDelete(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	var req kiroDocDeleteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if s.docDeleter == nil {
		httpserver.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("deleting is unavailable"))
		return
	}
	row, ok := s.findKiroDoc(r, req.Path)
	if !ok {
		httpreply.NotFound(w, "that document is no longer listed")
		return
	}
	if row.DeleteProtected || row.ReadOnly {
		httpreply.Forbidden(w, "this document cannot be deleted here")
		return
	}
	for _, target := range docDeleteTargets(&row) {
		err := deleteDocTarget(s.docDeleter, target)
		if refusal, isRefusal := errors.AsType[filebrowse.DeleteRefusedError](err); isRefusal {
			httpreply.Forbidden(w, refusal.Reason)
			return
		}
		if errors.Is(err, errDocLinked) {
			httpreply.Forbidden(w, path.Base(target)+" is a link; delete it where it points")
			return
		}
		if err != nil {
			httpreply.ServerError(w, "could not delete the document", err)
			return
		}
	}
	slog.Info("kiro docs: deleted", "category", row.Category, "path", logsafe.Field(row.Path))
	s.agent.Broadcast(durable.Context(r.Context()),
		marotte.NewEvent(marotte.EventKiroDocsChanged, "", marotte.KiroDocsChangedPayload{}))
	httpserver.OK(w)
}

// findKiroDoc answers the listed row whose path is p, so only a row the page shows is deletable.
func (s *Server) findKiroDoc(r *http.Request, p string) (KiroDoc, bool) {
	docs := s.collectKiroDocs(r.Context()).Docs
	i := slices.IndexFunc(docs, func(d KiroDoc) bool { return d.Path == p })
	if i < 0 {
		return KiroDoc{}, false
	}
	return docs[i], true
}

// docDeleteTargets names what row stands for: a skill is its whole directory, and an agent is
// both spellings of its name, since the scan lists the `.json` once the `.md` is gone.
func docDeleteTargets(row *KiroDoc) []string {
	switch row.Category {
	case catSkill:
		return []string{path.Dir(row.Path)}
	case catAgent:
		if base, isMD := strings.CutSuffix(row.Path, ".md"); isMD {
			return []string{row.Path, base + ".json"}
		}
		return []string{row.Path, strings.TrimSuffix(row.Path, ".json") + ".md"}
	default:
		return []string{row.Path}
	}
}

// deleteDocTarget removes one target, refusing a link and treating an absent one as done.
func deleteDocTarget(d docDeleter, target string) error {
	fi, err := os.Lstat("/" + target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errDocLinked
	}
	return d.Delete(target)
}
