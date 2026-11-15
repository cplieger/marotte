package server

import (
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
)

const openAPIPath = "/api/openapi.json"

// openAPIHandler serves the OpenAPI document composition passed with the endpoint table.
func openAPIHandler(doc []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		httpreply.WriteRawJSON(w, doc)
	})
}
