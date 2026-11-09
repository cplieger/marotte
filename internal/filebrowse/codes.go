package filebrowse

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "binary", Status: http.StatusUnsupportedMediaType},
	{Code: "changed", Status: http.StatusConflict},
	{Code: "invalid_file_id", Status: http.StatusBadRequest},
	{Code: "not_a_directory", Status: http.StatusBadRequest},
	{Code: "too_large", Status: http.StatusRequestEntityTooLarge},
}
