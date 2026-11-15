// Package apitable is marotte's endpoint table: every route the server mounts, with its types,
// its access row and the owner packages of its error codes. The server binds every route against
// it at boot, and cmd/wire-codegen generates the TypeScript client and docs/openapi.json from it.
package apitable

import (
	_ "embed"
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// OpenAPI is docs/openapi.json, embedded; TestOpenAPI_EmbeddedEqualsDocs keeps them equal.
//
//go:embed openapi.json
var OpenAPI []byte

// Entries returns every entry, HEAD twins generated from GET entries, in a fixed order.
func Entries() []route.Entry {
	listed := entries()
	out := make([]route.Entry, 0, len(listed)+len(listed)/2)
	for i := range listed {
		e := &listed[i]
		out = append(out, *e)
		if e.Method == http.MethodGet && !e.Any {
			twin := *e
			twin.Method = http.MethodHead
			out = append(out, twin)
		}
	}
	return out
}
