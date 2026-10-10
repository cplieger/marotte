package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/webhttp/v3"
)

const headerIfNoneMatch = "If-None-Match"

// specApprovalReader is the approval record as this endpoint uses it: stored approvals with
// Stale unset.
type specApprovalReader interface {
	For(dir string) map[string]marotte.SpecApproval
}

// handleSpec answers GET /api/specs/{dir}. dir is ONE percent-encoded segment ServeMux has
// already unescaped; it is never unescaped again, or %252F would resolve too.
func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	root, name, ok := spec.Address(spec.Roots(s.workDir), r.PathValue("dir"))
	if !ok {
		httpreply.NotFound(w, "spec not found")
		return
	}
	sp, err := spec.Load(r.Context(), root, name, spec.MaxFileBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, spec.ErrNoDocs):
		httpreply.NotFound(w, "spec not found")
		return
	case err != nil:
		slog.Warn("spec: load failed", "dir", logsafe.Field(root.Rel+"/specs/"+name), "error", logsafe.Field(err.Error()))
		httpreply.InternalError(w, nil)
		return
	}
	// Stale is DERIVED per read against this load's hashes, never stored, so a doc rewritten
	// after sign-off reports itself changed. A nil reader carries no approvals.
	if s.specApprovals != nil {
		sp.Approvals = specapproval.Derive(s.specApprovals.For(sp.Dir), sp.Docs)
	}
	etag := specETag(sp.Docs, sp.Approvals)
	if r.Header.Get(headerIfNoneMatch) == etag {
		w.Header().Set(headerETag, etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set(headerETag, etag)
	webhttp.WriteJSON(w, sp)
}

// specETag is the quoted hex sha256 over each doc's NAME and hash, then the approvals. The name is
// needed because an over-size doc's hash is empty. Approvals contribute phase, hash, derived Stale
// (a doc disappearing would otherwise cancel), At and User (the badge renders them), iterated over
// specapproval.Phases() for a stable order. NUL separates fields.
func specETag(docs []marotte.SpecDoc, approvals map[string]marotte.SpecApproval) string {
	h := sha256.New()
	for _, d := range docs {
		h.Write([]byte(d.File))
		h.Write([]byte{0})
		h.Write([]byte(d.Hash))
		h.Write([]byte{0})
	}
	for _, phase := range specapproval.Phases() {
		a, ok := approvals[string(phase)]
		if !ok {
			continue
		}
		h.Write([]byte(phase))
		h.Write([]byte{0})
		h.Write([]byte(a.Hash))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(a.At.UnixNano(), 10)))
		h.Write([]byte{0})
		h.Write([]byte(a.User))
		h.Write([]byte{0})
		if a.Stale {
			h.Write([]byte{1})
		}
		h.Write([]byte{0})
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}
