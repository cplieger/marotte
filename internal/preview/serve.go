package preview

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"golang.org/x/sys/unix"
)

// ServeHTTP answers /preview/<token>/<rel>. The preview header set goes on
// first, so every response, a refusal included, is sandboxed and framable only
// by marotte itself.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hostOK := len(r.Host) <= maxHostBytes && hostPattern.MatchString(r.Host)
	tok, rel, perr := splitServePath(r.URL.EscapedPath())
	src := ""
	if hostOK && len(tok) <= tokenMaxLen && tokenPattern.MatchString(tok) {
		src = "http://" + r.Host + PathPrefix + tok + "/"
	}
	setPreviewHeaders(w.Header(), src)

	folder, ok := h.admit(w, r, hostOK, tok, rel, perr)
	if !ok {
		return
	}
	h.serveFile(w, r, folder, rel)
}

func (h *Handler) admit(w http.ResponseWriter, r *http.Request, hostOK bool, tok string, rel []string, perr error) (string, bool) {
	switch {
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodHead)
		return "", false
	case !hostOK:
		httpreply.BadRequest(w, "bad Host header")
		return "", false
	case h.workFD < 0:
		writePageError(w, refuse(http.StatusServiceUnavailable, "previews are unavailable: the workspace could not be opened"))
		return "", false
	case perr != nil:
		httpreply.BadRequest(w, "malformed preview path")
		return "", false
	}
	capb, err := h.signer.verify(tok)
	if err != nil {
		httpreply.Forbidden(w, "This preview link has expired or is invalid. Reload the preview.")
		return "", false
	}
	if len(rel) == 0 || rel[len(rel)-1] == "" {
		httpreply.NotFound(w, "no directory listing")
		return "", false
	}
	for _, s := range rel {
		if strings.HasPrefix(s, ".") {
			httpreply.NotFound(w, "not found")
			return "", false
		}
	}
	return capb.Folder, true
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, folder string, rel []string) {
	folderFD, err := h.openFolder(folder)
	if err != nil {
		writePageError(w, err)
		return
	}
	defer func() { _ = unix.Close(folderFD) }()
	name := rel[len(rel)-1]
	fd, st, err := openRegular(folderFD, strings.Join(rel, "/"))
	if err != nil {
		writePageError(w, h.resolveError(err, "not found"))
		return
	}
	f := os.NewFile(uintptr(fd), "preview")
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentTypeFor(name))
	if !isPageName(name) {
		if st.Size > maxFileBytes {
			httpreply.NotFound(w, "file too large to preview (over 64 MiB)")
			return
		}
		sizeAccepted()
		// ServeContent seeks to the end to size the response, so a file growing
		// after the check would be served past the cap; the section pins it.
		http.ServeContent(w, r, name, time.Time{}, io.NewSectionReader(f, 0, st.Size))
		return
	}
	if st.Size > maxHTMLBytes {
		httpreply.NotFound(w, "page too large to preview (over 8 MiB)")
		return
	}
	servePage(w, r, f)
}

// sizeAccepted runs between the size check and the serve, so a test can grow
// the file inside that window.
var sizeAccepted = func() {}

func servePage(w http.ResponseWriter, r *http.Request, f *os.File) {
	body, err := io.ReadAll(io.LimitReader(f, maxHTMLBytes+1))
	if err != nil || len(body) > maxHTMLBytes {
		httpreply.NotFound(w, "the page could not be read")
		return
	}
	out := injectShim(body)
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(out)
}

// setPreviewHeaders writes the preview policy over the global baseline, which
// webhttp.SecurityHeaders sets before the handler and documents as overridable.
// src is the granted folder as a CSP source, or "" when the request named none
// worth trusting, in which case every list that would hold only it is 'none'.
func setPreviewHeaders(h http.Header, src string) {
	h.Set("Content-Security-Policy", previewCSP(src))
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	// The sandboxed document's origin is opaque, so its module scripts, fonts and
	// fetches to its own folder are cross-origin CORS requests; none carries
	// credentials.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Referrer-Policy", "no-referrer")
}

func previewCSP(src string) string {
	list := func(srcs ...string) string {
		var kept []string
		for _, s := range srcs {
			if s != "" {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			return "'none'"
		}
		return strings.Join(kept, " ")
	}
	return "sandbox allow-scripts allow-forms allow-modals; default-src 'none'" +
		"; script-src " + list(src, "https:", "'unsafe-inline'", "'unsafe-eval'", "'wasm-unsafe-eval'", "blob:") +
		"; style-src " + list(src, "https:", "'unsafe-inline'") +
		"; img-src " + list(src, "https:", "data:", "blob:") +
		"; font-src " + list(src, "https:", "data:") +
		"; media-src " + list(src, "https:", "data:", "blob:") +
		"; connect-src " + list(src) +
		"; worker-src " + list(src, "blob:") +
		"; manifest-src 'none'; object-src 'none'; frame-src 'none'" +
		"; base-uri " + list(src) +
		"; form-action " + list(src) +
		"; frame-ancestors 'self'"
}
