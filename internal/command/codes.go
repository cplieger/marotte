package command

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "agent_rows_waiting", Status: http.StatusConflict},
	{Code: "always_rule_not_saved", Status: http.StatusConflict},
	{Code: "busy", Status: http.StatusConflict},
	{Code: "chat_closed", Status: http.StatusConflict},
	{Code: "chat_gone", Status: http.StatusConflict},
	{Code: "chat_not_found", Status: http.StatusConflict},
	{Code: "consumed", Status: http.StatusConflict},
	{Code: "full", Status: http.StatusConflict},
	{Code: "held", Status: http.StatusConflict},
	{Code: "in_progress", Status: http.StatusConflict},
	{Code: "invalid_hash", Status: http.StatusBadRequest},
	{Code: "invalid_phase", Status: http.StatusBadRequest},
	{Code: "no_turn", Status: http.StatusConflict},
	{Code: "not_user", Status: http.StatusConflict},
	{Code: "not_waiting", Status: http.StatusNotFound},
	{Code: "prompt_only", Status: http.StatusConflict},
	{Code: "sending", Status: http.StatusConflict},
	{Code: "settling", Status: http.StatusConflict},
	{Code: "starting", Status: http.StatusConflict},
	{Code: "still_applying", Status: http.StatusGatewayTimeout},
	{Code: "tabs_full", Status: http.StatusConflict},
	{Code: "too_many_specs", Status: http.StatusConflict},
	{Code: "turn_open", Status: http.StatusConflict},
}
