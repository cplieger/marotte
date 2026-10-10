//go:build !marotte_test

package server

import "net/http"

// registerTestHooks mounts nothing: the SSE control surface under /api/test/ exists
// only in a binary built with -tags marotte_test (testhooks_marottetest.go).
func (*Server) registerTestHooks(*http.ServeMux) {}
