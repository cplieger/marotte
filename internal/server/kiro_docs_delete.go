package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/marotte/internal/durable"
	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
)

// docDeleter removes one browsable path behind the file browser's delete guards, and answers
// the refusal a delete would get without removing anything.
type docDeleter interface {
	Delete(reqPath string) error
	DeleteFile(reqPath string) error
	CheckDelete(reqPath string) error
}

var _ docDeleter = (*filebrowse.Handler)(nil)

// kiroDocDeleteRequest is POST /api/workspace/kiro-docs/delete's body: a row's `path` verbatim.
type kiroDocDeleteRequest struct {
	Path string `json:"path"`
}

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
	removed, err := deleteDocRow(s.docDeleter, &row)
	s.reportFileChange(durable.Context(r.Context()), filebrowse.Change{Paths: removed})
	if refusal, isRefusal := errors.AsType[filebrowse.DeleteRefusedError](err); isRefusal {
		httpreply.Forbidden(w, refusal.Reason)
		return
	}
	if err != nil {
		httpreply.ServerError(w, "could not delete the document", err)
		return
	}
	slog.Info("kiro docs: deleted", "category", row.Category, "path", logsafe.Field(row.Path))
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

// reportFileChange hands ch to the file-change function, when one is wired and ch names a path.
func (s *Server) reportFileChange(ctx context.Context, ch filebrowse.Change) {
	if s.fileChanges != nil && len(ch.Paths) > 0 {
		s.fileChanges(ctx, ch)
	}
}

// deleteDocRow removes every target of row, or none when any one of them is refused, and answers the absolute paths
// it removed, those removed before a failure included.
func deleteDocRow(d docDeleter, row *KiroDoc) ([]string, error) {
	targets, err := presentDocTargets(d, docDeleteTargets(row))
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(targets))
	for _, target := range targets {
		remove := d.Delete
		if target.twin {
			remove = d.DeleteFile
		}
		if err := remove(target.path); err != nil {
			return removed, err
		}
		removed = append(removed, "/"+target.path)
	}
	return removed, nil
}

// docTarget is one path a row's delete removes. A twin is removed only as a regular file.
type docTarget struct {
	path string
	twin bool
}

// docDeleteTargets names what row stands for, row.Path last so a removal failing partway leaves
// the row listed for a retry: a skill is its whole directory, and an agent is both spellings of
// its name, since the scan lists the `.json` once the `.md` is gone.
func docDeleteTargets(row *KiroDoc) []docTarget {
	switch row.Category {
	case catSkill:
		return []docTarget{{path: path.Dir(row.Path)}}
	case catAgent:
		return []docTarget{{path: agentTwin(row.Path), twin: true}, {path: row.Path}}
	default:
		return []docTarget{{path: row.Path}}
	}
}

// agentTwin names the other spelling of an agent file: the `.md` for a `.json` and back.
func agentTwin(file string) string {
	if base, isMD := strings.CutSuffix(file, ".md"); isMD {
		return base + ".json"
	}
	return strings.TrimSuffix(file, ".json") + ".md"
}

// presentDocTargets answers the targets to remove, or the refusal of the first one any delete
// guard or a link refuses, before anything is removed. An absent target, or a twin that does not
// resolve to a regular file, is left out.
func presentDocTargets(d docDeleter, targets []docTarget) ([]docTarget, error) {
	present := make([]docTarget, 0, len(targets))
	for _, target := range targets {
		fi, err := os.Lstat("/" + target.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if target.twin && !resolvesToRegular(target.path) {
			continue
		}
		// A linked twin refuses the row rather than being skipped: the scan lists it once the
		// row's own file is gone, so the row would survive its delete.
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, filebrowse.DeleteRefusedError{Reason: path.Base(target.path) + " is a link; delete it where it points"}
		}
		if err := d.CheckDelete(target.path); err != nil {
			return nil, err
		}
		present = append(present, target)
	}
	return present, nil
}

func resolvesToRegular(p string) bool {
	fi, err := os.Stat("/" + p)
	return err == nil && fi.Mode().IsRegular()
}
