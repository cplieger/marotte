package forges

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "check_busy", Status: http.StatusConflict},
	{Code: "client_id_invalid", Status: http.StatusBadRequest},
	{Code: "connection_busy", Status: http.StatusConflict},
	{Code: "kind_invalid", Status: http.StatusBadRequest},
	{Code: "merge_invalid", Status: http.StatusBadRequest},
	{Code: "not_supported", Status: http.StatusNotImplemented},
	{Code: "owners_invalid", Status: http.StatusBadRequest},
	{Code: "owners_too_many", Status: http.StatusBadRequest},
	{Code: "rate_limited", Status: http.StatusTooManyRequests},
	{Code: "reconnect_required", Status: http.StatusConflict},
	{Code: "sign_in_timed_out", Status: http.StatusGatewayTimeout},
	{Code: "sign_out_cleanup_pending", Status: http.StatusServiceUnavailable},
	{Code: "strategy_required", Status: http.StatusBadRequest},
	{Code: "token_required", Status: http.StatusBadRequest},
	{Code: "watch_invalid", Status: http.StatusBadRequest},
	{Code: "web_base_url_invalid", Status: http.StatusBadRequest},
}
