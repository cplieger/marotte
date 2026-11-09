package chat

import (
	"net/http"
	"net/http/httptest"

	"github.com/cplieger/marotte/internal/route/routetest"
)

// serveRouter serves req through s's bound chat routes.
func serveRouter(s *Store, rec *httptest.ResponseRecorder, req *http.Request) {
	mux := http.NewServeMux()
	s.RegisterRoutes(routetest.On(mux))
	mux.ServeHTTP(rec, req)
}
