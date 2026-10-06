package chat

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// Router owns the chat package's HTTP handlers and delegates persistence to its *Store. Store.RegisterRoutes
// delegates here, so consumers see the same store either way.
type Router struct {
	store *Store
}

// NewRouter creates a Router backed by the given Store.
func NewRouter(s *Store) *Router {
	return &Router{store: s}
}

// Register wires GET /api/chats, GET /api/chats/search and GET /api/chats/{id}. search is an exact literal so it wins
// over the `/api/chats/` prefix.
func (rt *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/chats", rt.handleList)
	mux.HandleFunc("/api/chats/search", rt.handleSearchAll)
	mux.HandleFunc("/api/chats/", rt.handleOne)
}

// handleSearchAll serves GET /api/chats/search?q=: which chats match, ranked, each with its best line. handleSearch
// stays scoped to one chat.
func (rt *Router) handleSearchAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, rt.store.SearchAll(r.Context(), r.URL.Query().Get("q")))
}
