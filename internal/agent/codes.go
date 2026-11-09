package agent

import (
	"net/http"

	"github.com/cplieger/marotte/internal/route"
)

// Codes are the envelope codes this package writes, each with the one status it answers it with.
var Codes = route.CodeSet{
	{Code: "alive_invalid", Status: http.StatusBadRequest},
	{Code: "already_paused", Status: http.StatusConflict},
	{Code: "already_running", Status: http.StatusConflict},
	{Code: "ask_branch_running", Status: http.StatusConflict},
	{Code: "ask_gone", Status: http.StatusConflict},
	{Code: "ask_pending", Status: http.StatusConflict},
	{Code: "ask_settling", Status: http.StatusConflict},
	{Code: "ask_stale", Status: http.StatusConflict},
	{Code: "ask_stopping", Status: http.StatusConflict},
	{Code: "ask_unnamed", Status: http.StatusConflict},
	{Code: "ask_unreadable", Status: http.StatusServiceUnavailable},
	{Code: "ask_unsettled", Status: http.StatusConflict},
	{Code: "awaiting_answer", Status: http.StatusConflict},
	{Code: "busy", Status: http.StatusConflict},
	{Code: "engine_slow", Status: http.StatusServiceUnavailable},
	{Code: "finished", Status: http.StatusConflict},
	{Code: "full", Status: http.StatusConflict},
	{Code: "identity_provider_unreachable", Status: http.StatusServiceUnavailable},
	{Code: codeSyncLocked, Status: http.StatusConflict},
	{Code: "model_list_unreadable", Status: http.StatusServiceUnavailable},
	{Code: "not_enabled", Status: http.StatusConflict},
	{Code: "not_started", Status: http.StatusConflict},
	{Code: "not_waiting", Status: http.StatusNotFound},
	{Code: "recipe_list_slow", Status: http.StatusServiceUnavailable},
	{Code: "render_failed", Status: http.StatusBadGateway},
	{Code: "run_answer_pending", Status: http.StatusConflict},
	{Code: "run_paused", Status: http.StatusConflict},
	{Code: "sign_in_required", Status: http.StatusConflict},
	{Code: "step_not_paused", Status: http.StatusConflict},
	{Code: "step_unnamed", Status: http.StatusConflict},
	{Code: "still_applying", Status: http.StatusGatewayTimeout},
	{Code: codeSyncUnavailable, Status: http.StatusConflict},
	{Code: "waits_on_run", Status: http.StatusConflict},
}
