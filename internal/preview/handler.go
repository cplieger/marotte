package preview

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/cplieger/marotte/internal/filebrowse"
	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/webhttp/v3"
	"golang.org/x/sys/unix"
)

const (
	grantTTL = 12 * time.Hour
	// maxFolderBytes keeps a token, which appears in nine CSP source lists, well
	// under a proxy's header limit.
	maxFolderBytes = 512
	// maxHostBytes is the longest DNS name plus a port. The Host is repeated in
	// nine CSP source lists, so an unbounded one multiplies the request's header
	// allowance into the response.
	maxHostBytes = 253 + len(":65535")
	maxFileBytes = 64 << 20
	maxHTMLBytes = 8 << 20
)

var (
	hostPattern  = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]+$`)
	tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Handler serves the preview routes: the sandboxed file route under PathPrefix
// and the grant and stamp endpoints the client mints and polls with.
type Handler struct {
	signer  *Signer
	log     *slog.Logger
	workDir string
	// realWorkDir is workDir with its symlinks resolved. Every open beneath
	// workFD follows no symlink, so realWorkDir joined with a relative path is
	// the path the kernel opens.
	realWorkDir string
	sensitive   filebrowse.Sensitive
	// workFD is the workspace root every resolution starts from; -1 means it
	// could not be opened and every route answers 503.
	workFD     int
	resolveLog sync.Once
}

// New returns a Handler confined to workDir that refuses any folder sensitive
// reports exposed. A workspace that cannot be opened is logged once and leaves
// every route answering 503.
func New(workDir string, sensitive filebrowse.Sensitive, s *Signer, log *slog.Logger) *Handler {
	h := &Handler{signer: s, log: log, sensitive: sensitive, workDir: filepath.Clean(workDir), workFD: -1}
	fd, err := unix.Open(h.workDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		log.Warn("preview: workspace root could not be opened; previews are unavailable",
			"work_dir", h.workDir, "error", err)
		return h
	}
	resolved, err := evalSymlinks(h.workDir)
	if err != nil {
		_ = unix.Close(fd)
		log.Warn("preview: workspace root could not be resolved; previews are unavailable",
			"work_dir", h.workDir, "error", err)
		return h
	}
	h.realWorkDir, h.workFD = resolved, fd
	return h
}

// RegisterRoutes mounts the three preview routes. Each gates its own method,
// because the "/" SPA mount absorbs a ServeMux 405.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle(PathPrefix, h)
	mux.HandleFunc("/api/preview/grant", h.handleGrant)
	mux.HandleFunc("/api/preview/stamp", h.handleStamp)
}

type pageError struct {
	msg    string
	status int
}

func (e *pageError) Error() string { return e.msg }

func refuse(status int, msg string) *pageError { return &pageError{status: status, msg: msg} }

func writePageError(w http.ResponseWriter, err error) {
	var pe *pageError
	if !errors.As(err, &pe) {
		httpreply.InternalError(w, err)
		return
	}
	webhttp.WriteJSONStatus(w, pe.status, httpreply.ErrorJSON(pe.msg))
}

type page struct {
	folder   string
	name     string
	folderFD int
}

// openPage resolves the folder and file through the kernel's no-follow
// resolution, so the folder a grant names is the folder on disk. The caller
// closes folderFD.
func (h *Handler) openPage(p string) (*page, []byte, error) {
	pg, err := h.openPageFolder(p)
	if err != nil {
		return nil, nil, err
	}
	head, err := h.readHead(pg)
	if err != nil {
		_ = unix.Close(pg.folderFD)
		return nil, nil, err
	}
	return pg, head, nil
}

func (h *Handler) openPageFolder(p string) (*page, error) {
	if h.workFD < 0 {
		return nil, refuse(http.StatusServiceUnavailable, "previews are unavailable: the workspace could not be opened")
	}
	if err := checkPageShape(p); err != nil {
		return nil, refuse(http.StatusBadRequest, err.Error())
	}
	rel, ok := relBeneath(h.workDir, p)
	if !ok {
		return nil, refuse(http.StatusForbidden, "only pages under "+h.workDir+" can be previewed")
	}
	if err := checkRelComponents(rel); err != nil {
		return nil, refuse(http.StatusBadRequest, err.Error())
	}
	folder := path.Dir(p)
	if folder == h.workDir {
		return nil, refuse(http.StatusBadRequest, fmt.Sprintf(
			"Put the page in its own folder under %s: a page directly in %s would give the preview the whole workspace.",
			h.workDir, h.workDir,
		))
	}
	if len(folder) > maxFolderBytes {
		return nil, refuse(http.StatusBadRequest, "path too long to preview")
	}
	folderFD, err := h.openFolder(folder)
	if err != nil {
		return nil, err
	}
	return &page{folder: folder, name: path.Base(p), folderFD: folderFD}, nil
}

// readHead reads the first hintScanBytes of the page. It leaves folderFD open.
func (h *Handler) readHead(pg *page) ([]byte, error) {
	fd, _, err := openRegular(pg.folderFD, pg.name)
	if err != nil {
		return nil, h.resolveError(err, "no such page")
	}
	f := os.NewFile(uintptr(fd), pg.name)
	head, rerr := io.ReadAll(io.LimitReader(f, hintScanBytes))
	_ = f.Close()
	if rerr != nil {
		return nil, refuse(http.StatusNotFound, "the page could not be read")
	}
	return head, nil
}

func (h *Handler) openFolder(folder string) (int, error) {
	rel, ok := relBeneath(h.workDir, folder)
	if !ok {
		return -1, refuse(http.StatusForbidden, "the granted folder is outside the workspace")
	}
	if h.sensitive.ExposedBy(folder) || h.sensitive.ExposedBy(filepath.Join(h.realWorkDir, rel)) {
		return -1, refuse(http.StatusForbidden,
			"This folder would expose marotte's own configuration or credentials, so it cannot be previewed.")
	}
	fd, err := openDir(h.workFD, rel)
	if err != nil {
		return -1, h.resolveError(err, "no such folder")
	}
	return fd, nil
}

func (h *Handler) resolveError(err error, notFound string) error {
	if errors.Is(err, errResolveUnsupported) {
		h.resolveLog.Do(func() {
			h.log.Error("preview: the kernel refused openat2; previews are disabled rather than served without no-follow resolution")
		})
		return refuse(http.StatusServiceUnavailable, "previews are unavailable on this kernel")
	}
	if errors.Is(err, errResolveBusy) {
		return refuse(http.StatusServiceUnavailable, "the folder kept changing while it was opened. Reload the preview")
	}
	return refuse(http.StatusNotFound, notFound)
}

// Grant validates path and mints a grant expiring at exp. A refusal is a
// *pageError carrying its HTTP status.
func (h *Handler) Grant(p string, exp time.Time) (marotte.PreviewGrant, error) {
	pg, err := h.openPageFolder(p)
	if err != nil {
		return marotte.PreviewGrant{}, err
	}
	defer func() { _ = unix.Close(pg.folderFD) }()
	// The stamp precedes the head read: a rewrite between them then leaves the
	// baseline older than the hint, so the client's next poll differs and
	// reloads. The reverse order can pair an old hint with the new tree's stamp.
	stamp, _, _, err := folderStamp(pg.folderFD)
	if err != nil {
		return marotte.PreviewGrant{}, fmt.Errorf("preview: stamp %s: %w", pg.folder, err)
	}
	grantStamped()
	head, err := h.readHead(pg)
	if err != nil {
		return marotte.PreviewGrant{}, err
	}
	base := PathPrefix + h.signer.Mint(pg.folder, exp) + "/"
	return marotte.PreviewGrant{
		URL:       base + url.PathEscape(pg.name),
		Base:      base,
		ExpiresAt: exp.UTC(),
		Epoch:     h.signer.Epoch(),
		Hint:      extractHint(head),
		Stamp:     stamp,
	}, nil
}

// grantStamped runs between a grant's folder stamp and its head read, so a test
// can rewrite the page inside that window.
var grantStamped = func() {}

// evalSymlinks resolves the workspace root once it opened; a test fails it, since
// only a root removed between the open and the resolve reaches that arm.
var evalSymlinks = filepath.EvalSymlinks

func (h *Handler) handleGrant(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodPost) {
		return
	}
	var req marotte.PreviewGrantRequest
	if !httpreply.DecodeBody(w, r, &req, "bad request") {
		return
	}
	g, err := h.Grant(req.Path, h.signer.now().Add(grantTTL))
	if err != nil {
		writePageError(w, err)
		return
	}
	webhttp.WriteJSON(w, g)
}

func (h *Handler) handleStamp(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	pg, _, err := h.openPage(r.URL.Query().Get("path"))
	if err != nil {
		writePageError(w, err)
		return
	}
	defer func() { _ = unix.Close(pg.folderFD) }()
	stamp, n, truncated, err := folderStamp(pg.folderFD)
	if err != nil {
		httpreply.ServerError(w, "the folder could not be read", err)
		return
	}
	webhttp.WriteJSON(w, marotte.PreviewStamp{
		Stamp: stamp, Entries: n, Truncated: truncated, Epoch: h.signer.Epoch(),
	})
}
