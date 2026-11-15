package chat

import (
	"net/http"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/route"
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

// register binds every chat read, one binding per endpoint. search is an exact literal, so it wins over {id}.
func (rt *router) register(m routeMux) {
	id := route.WithParams[marotte.IDPath]
	m.Bind(route.JSON[struct{}, List]("GET /api/chats", http.HandlerFunc(rt.handleList)))
	m.Bind(route.WithQuery[SearchAllQuery](route.JSON[struct{}, SearchAllResult]("GET /api/chats/search", http.HandlerFunc(rt.handleSearchAll))))
	m.Bind(route.WithQuery[PageQuery](id(route.JSON[struct{}, Page]("GET /api/chats/{id}", http.HandlerFunc(rt.serveChatPage)))))
	m.Bind(id(route.JSON[struct{}, Turns]("GET /api/chats/{id}/turns", http.HandlerFunc(rt.handleTurns))))
	m.Bind(route.WithQuery[TurnQuery](route.WithParams[marotte.IDTurnPath](
		route.JSON[struct{}, TurnRange]("GET /api/chats/{id}/turns/{turn}", http.HandlerFunc(rt.handleTurnRange)),
	)))
	m.Bind(route.WithQuery[InChatSearchQuery](id(route.JSON[struct{}, SearchResult]("GET /api/chats/{id}/search", http.HandlerFunc(rt.handleSearch)))))
	m.Bind(route.WithQuery[ExportQuery](id(route.Raw("GET /api/chats/{id}/export", http.HandlerFunc(rt.handleExport)))))
	m.Bind(route.WithParams[marotte.IDToolPath](route.JSON[struct{}, marotte.ToolCallBulk]("GET /api/chats/{id}/tools/{tool}",
		http.HandlerFunc(rt.handleToolCall))))
}

// handleSearchAll serves GET /api/chats/search?q=: which chats match, ranked, each with its best line. handleSearch
// stays scoped to one chat.
func (rt *router) handleSearchAll(w http.ResponseWriter, r *http.Request) {
	httpserver.WriteJSON(w, rt.store.searchAll(r.Context(), r.URL.Query().Get("q")))
}
