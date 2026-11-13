package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// readsWithoutEngineWait marks every API GET and HEAD as a read that answers at once while the agent engine is not
// ready (marotte.WithoutEngineWait): over HTTP/1.1 six such reads held open fill the browser's connection pool and
// stall the chat reads queued behind them. A write still waits, since the engine may be seconds from ready.
func readsWithoutEngineWait(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			(r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, apiPathPrefix)) {
			r = r.WithContext(marotte.WithoutEngineWait(r.Context()))
		}
		next.ServeHTTP(w, r)
	})
}
