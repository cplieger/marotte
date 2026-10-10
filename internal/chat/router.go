package chat

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// router owns the chat package's HTTP handlers and delegates persistence to its *Store. Store.RegisterRoutes
// delegates here, so consumers see the same store either way.
type router struct {
	store *Store
}

// NewRouter creates a router backed by the given Store.
func newRouter(s *Store) *router {
	return &router{store: s}
}

// register wires GET /api/chats, GET /api/chats/search and GET /api/chats/{id}. search is an exact literal so it wins
// over the `/api/chats/` prefix.
func (rt *router) register(mux *http.ServeMux) {
	mux.HandleFunc("/api/chats", rt.handleList)
	mux.HandleFunc("/api/chats/search", rt.handleSearchAll)
	mux.HandleFunc("/api/chats/", rt.handleOne)
}

// handleSearchAll serves GET /api/chats/search?q=: which chats match, ranked, each with its best line. handleSearch
// stays scoped to one chat.
func (rt *router) handleSearchAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, rt.store.searchAll(r.Context(), r.URL.Query().Get("q")))
}
