package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/spec"
	"github.com/cplieger/marotte/internal/specapproval"
	"github.com/cplieger/webhttp/v3"
)

// headerIfNoneMatch is the request header carrying the ETag a client holds.
const headerIfNoneMatch = "If-None-Match"

// specApprovalReader is the approval record as this endpoint uses it: one lookup
// per spec, returning the stored approvals with Stale unset. Declared here
// because this is the consumer; the store lives in internal/specapproval.
type specApprovalReader interface {
	For(dir string) map[string]marotte.SpecApproval
}

// handleSpec answers GET /api/specs/{dir} with one spec. dir is the
// workspace-relative spec directory as ONE percent-encoded segment, which
// ServeMux hands over already unescaped; it is never unescaped again here,
// or the %252F spelling would resolve too and restore the two-spelling shape
// {dir} was chosen to remove.
func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	root, name, ok := spec.Address(spec.Roots(s.workDir), r.PathValue("dir"))
	if !ok {
		httpreply.NotFound(w, "spec not found")
		return
	}
	sp, err := spec.Load(r.Context(), root, name, filebrowse.MaxFileSize)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, spec.ErrNoDocs):
		httpreply.NotFound(w, "spec not found")
		return
	case err != nil:
		slog.Warn("spec: load failed", "dir", logsafe.Field(root.Rel+"/specs/"+name), "error", logsafe.Field(err.Error()))
		httpreply.InternalError(w, nil)
		return
	}
	// Stale is DERIVED here, per read, against the hashes this load just
	// produced — never stored, so a document the agent rewrote after sign-off
	// reports itself as changed instead of continuing to look approved. A nil
	// reader (no config dir, so no store) simply carries no approvals.
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

// specETag is the quoted hex sha256 over each doc's NAME and hash, in order,
// then over the approvals.
//
// The name is in the digest because the hash alone is not sufficient: a document
// over maxBytes carries an EMPTY hash (spec.Load lists it with TooLarge and no
// content), so over hashes alone a rename, and the addition or removal of an
// over-size document, both leave the digest unmoved while changing the segment
// list this endpoint serves. NUL is the separator because a doc's File is a base
// name, which cannot contain one.
//
// The one change still invisible here is an over-size document's content moving
// while it stays over-size, which the page renders nothing for.
//
// The APPROVALS join the digest because they are part of this endpoint's answer
// now, and a 304 over the docs alone would serve a stale badge. All three fields
// go in: the phase and the approved hash say WHICH version was signed off, and
// the derived Stale is what the doc hashes above cannot supply — a document
// moving under an approval changes only the DOC's hash, which is already in the
// digest, but a document DISAPPEARING removes its own contribution while
// flipping Stale, so without it the two changes could cancel. At and User are in
// the digest because the badge renders them: re-approving a version already
// approved moves nothing else, so over hash and Stale alone a 304 would serve
// the previous timestamp. Iterated over
// specapproval.Phases() rather than over the map, because a map's order is
// random and a digest that moves per read would defeat the ETag entirely.
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
