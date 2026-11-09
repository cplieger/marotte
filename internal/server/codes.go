package server

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "tools_unavailable", Status: http.StatusServiceUnavailable},
}
