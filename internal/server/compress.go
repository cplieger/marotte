package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/preview"
)

// compressMinBytes is the body size at which gzip starts paying for itself: below it
// the framing and CPU cost outweigh the saving.
const compressMinBytes = 1024

// compressSkipPaths are answered without wrapping at all: a response whose contract is that
// each write reaches the client immediately cannot be buffered to measure it.
var compressSkipPaths = []string{"/api/events", "/api/shell/ws"}

// compressJSON negotiates Content-Encoding: gzip for JSON bodies over compressMinBytes when
// the request offers gzip. The Content-Type gate also keeps precompressed static assets out.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isCompressSkipped(r.URL.Path) {
			// Not negotiated, so no Vary either.
			next.ServeHTTP(w, r)
			return
		}
		// Announced before the outcome: a small body or a `gzip;q=0` refusal was also selected on
		// Accept-Encoding, which a shared cache cannot see from the body.
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

// isCompressSkipped reports whether path is a streaming surface the wrapper must not
// see.
func isCompressSkipped(path string) bool {
	// A preview file is served with Range support, which a gzip wrapper would break.
	if strings.HasPrefix(path, preview.PathPrefix) {
		return true
	}
	for _, p := range compressSkipPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// A `gzip;q=0` is a REFUSAL rather than an offer (RFC 9110 section 12.5.3), and a bare `*` with no
// gzip entry is an offer.
func acceptsGzip(header string) bool {
	wildcard := false
	for part := range strings.SplitSeq(header, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(token))
		if name != encodingGzip && name != "*" {
			continue
		}
		if qZero(params) {
			if name == encodingGzip {
				return false
			}
			continue
		}
		if name == encodingGzip {
			return true
		}
		wildcard = true
	}
	return wildcard
}

// qZero reports whether an Accept-Encoding parameter list carries q=0, the
// spelling that turns an entry from an offer into a refusal.
func qZero(params string) bool {
	for part := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		switch strings.TrimSpace(v) {
		case "0", "0.", "0.0", "0.00", "0.000":
			return true
		}
	}
	return false
}

// isJSONMediaType reports whether a Content-Type names JSON: exactly application/json or a
// `+json` suffix (RFC 6839). Not a substring test: application/x-ndjson is a STREAM.
func isJSONMediaType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	if mt == "application/json" {
		return true
	}
	_, suffix, ok := strings.Cut(mt, "+")
	return ok && suffix == "json"
}

// compressMode is what compressWriter has decided about the body so far.
type compressMode int

const (
	// modeBuffering holds the body, and the status line with it, until the size is
	// worth compressing.
	modeBuffering compressMode = iota
	modePlain
	modeGzip
)

// That needs the Content-Type (at WriteHeader) and the body size (as it arrives), so the status
// line is held back until the threshold is crossed or the handler returns.
type compressWriter struct {
	http.ResponseWriter
	gz     *gzip.Writer
	buf    bytes.Buffer
	status int
	mode   compressMode
	// headerWritten records that the HANDLER committed a status, making a second
	// WriteHeader the no-op net/http's own writer makes it.
	headerWritten bool
	// committed records that the status line reached the underlying writer, which
	// while buffering is not the same question.
	committed bool
}

// Unwrap exposes the wrapped writer to http.ResponseController, so a handler reaching
// Flush or SetWriteDeadline through it finds the real implementation.
func (cw *compressWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }

// WriteHeader records the status and decides whether this response is a gzip candidate
// at all. A candidate's status line is held back until Write or finish settles size.
func (cw *compressWriter) WriteHeader(code int) {
	if cw.headerWritten {
		return
	}
	cw.headerWritten = true
	cw.status = code
	if !cw.gzipCandidate(code) {
		cw.passThrough()
	}
}

const encodingGzip = "gzip"

func (cw *compressWriter) gzipCandidate(code int) bool {
	if code < http.StatusOK || code == http.StatusNoContent || code == http.StatusNotModified {
		return false
	}
	h := cw.Header()
	if h.Get("Content-Encoding") != "" {
		return false
	}
	return isJSONMediaType(h.Get("Content-Type"))
}

func (cw *compressWriter) passThrough() {
	cw.mode = modePlain
	cw.commit()
	if cw.buf.Len() > 0 {
		_, _ = cw.ResponseWriter.Write(cw.buf.Bytes())
		cw.buf.Reset()
	}
}

// commit writes the status line to the underlying writer, once.
func (cw *compressWriter) commit() {
	if cw.committed {
		return
	}
	cw.committed = true
	if cw.status == 0 {
		cw.status = http.StatusOK
	}
	cw.ResponseWriter.WriteHeader(cw.status)
}

func (cw *compressWriter) Write(p []byte) (int, error) {
	if !cw.headerWritten {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.mode == modePlain {
		return cw.ResponseWriter.Write(p)
	}
	if cw.mode == modeGzip {
		return cw.gz.Write(p)
	}
	cw.buf.Write(p)
	if cw.buf.Len() >= compressMinBytes {
		cw.startGzip()
	}
	return len(p), nil
}

// Content-Length is dropped: the handler's value describes the identity representation.
func (cw *compressWriter) startGzip() {
	h := cw.Header()
	gz, err := gzip.NewWriterLevel(cw.ResponseWriter, gzip.DefaultCompression)
	if err != nil {
		// Unreachable (the level is a constant); answer plainly rather than lose the body.
		cw.passThrough()
		return
	}
	h.Set("Content-Encoding", encodingGzip)
	h.Del("Content-Length")
	cw.mode = modeGzip
	cw.gz = gz
	cw.commit()
	if cw.buf.Len() > 0 {
		_, _ = gz.Write(cw.buf.Bytes())
		cw.buf.Reset()
	}
}

// Flush resolves an undecided response as plain first: a flushing handler wants these bytes
// sent now, which buffering cannot honour.
func (cw *compressWriter) Flush() {
	if cw.mode == modeBuffering {
		cw.passThrough()
	}
	if cw.mode == modeGzip {
		_ = cw.gz.Flush()
	}
	_ = http.NewResponseController(cw.ResponseWriter).Flush()
}

// Hijack gives up on encoding first: a hijacked connection is no longer HTTP framing,
// so nothing this wrapper does can apply to it.
func (cw *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := cw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("compress: underlying ResponseWriter is not a Hijacker")
	}
	cw.mode = modePlain
	return hj.Hijack()
}

// Called from the middleware's defer, so a panicking handler still leaves a consistent response for
// the recoverer.
func (cw *compressWriter) finish() {
	switch cw.mode {
	case modeGzip:
		_ = cw.gz.Close()
	case modeBuffering:
		cw.passThrough()
	case modePlain:
	}
}
