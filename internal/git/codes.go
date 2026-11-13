package git

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "binary", Status: http.StatusUnsupportedMediaType},
	{Code: "not_utf8", Status: http.StatusUnsupportedMediaType},
	{Code: "too_large", Status: http.StatusRequestEntityTooLarge},
}
