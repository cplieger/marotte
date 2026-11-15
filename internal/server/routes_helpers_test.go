package server

import "net/http"

// mux binds every route of s, with no endpoint table and no seal, for a test serving part of the server.
func (s *Server) mux() *http.ServeMux {
	m, _ := s.routes()
	return m
}
