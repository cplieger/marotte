package server

import (
	"log/slog"
	"net/http"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/logsafe"
	"github.com/cplieger/pinstall/v3"
	"github.com/cplieger/webhttp/v3"
)

// kiroRescanPath is the loopback kiro-cli repair hook: it makes an install repaired INSIDE
// the container observable without recreating it (after the retries are exhausted).
const kiroRescanPath = "/api/kiro-cli/rescan"

// kiroRescanSurface is what a refused caller is told declined the request.
const kiroRescanSurface = "the kiro-cli repair hook"

// The readiness reasons marotte puts on the wire. A published contract: an operator reads
// them, and static-src/runtime-health.ts prefix-matches "kiro-cli" then keys copy on each
// literal in full. kiroReasonText is the one producer; TestKiroReasonTextIsTheClientContract
// pins them.
const (
	reasonInstalling = "kiro-cli installing"
	reasonRetrying   = "kiro-cli install retrying"
	// reasonUnavailable is also the fallback for a rescan with no verdict and for a reason a
	// future library adds: an unnamed state still blocks chats.
	reasonUnavailable = "kiro-cli unavailable"
	// reasonSettings is pinstall.ReasonAssertion: the required app.disableAutoupdates could not
	// be asserted, so the binary may replace itself and invalidate the verified digest.
	reasonSettings = "kiro-cli required settings not enforced"
)

// kiroReasonText renders the install manager's typed reason as the reason
// /api/health and the repair hook serve. ReasonReady maps to "", which the
// health envelope omits.
func kiroReasonText(why pinstall.Reason) string {
	switch why {
	case pinstall.ReasonReady:
		return ""
	case pinstall.ReasonInstalling:
		return reasonInstalling
	case pinstall.ReasonRetrying:
		return reasonRetrying
	case pinstall.ReasonUnavailable:
		return reasonUnavailable
	case pinstall.ReasonAssertion:
		return reasonSettings
	}
	return reasonUnavailable
}

// handleKiroRescan re-derives the active kiro-cli version from disk (downloading nothing) and
// reports readiness: 200 when a version is active, else 503 with the manager's reason.
func (s *Server) handleKiroRescan(w http.ResponseWriter, r *http.Request) {
	// The method gate sits INSIDE the loopback wrapper, so a remote caller learns nothing; not
	// on the pattern, whose mismatch falls through to the SPA mount (see ListenAndServe).
	if !httpreply.RequireMethod(w, r, http.MethodPost) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ok, err := s.kiroRescan(r.Context())
	if ok {
		webhttp.WriteJSON(w, healthBody{Status: "ok"})
		return
	}
	// Report the VERDICT, not err (which can name a volume path); the manager logged the fault.
	reason := reasonUnavailable
	if s.kiroReady != nil {
		if _, why := s.kiroReady(); why != pinstall.ReasonReady {
			reason = kiroReasonText(why)
		}
	}
	var errText string
	if err != nil {
		errText = logsafe.Field(err.Error())
	}
	slog.Warn("kiro-cli rescan found no usable version", "reason", reason, "error", errText)
	webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, healthBody{
		Status: "unready",
		Reason: reason,
	})
}

// loopbackOnly admits only requests whose socket peer AND Host are loopback and which carry
// no proxy or browser provenance header (webhttp.LoopbackOnly): the gated endpoints spawn
// processes. The provenance deny closes a reverse proxy on the loopback interface, which
// satisfies both other legs. surface names the refusing endpoint, since two mounts share it.
func loopbackOnly(surface string, next http.Handler) http.Handler {
	refuse := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log the refusal: both endpoints gate a process spawn, so it is worth a correlatable record.
		slog.Warn("loopback-only endpoint refused: not a loopback caller",
			"surface", surface, "remote", r.RemoteAddr, "host", logsafe.Field(r.Host))
		webhttp.WriteJSONStatus(w, http.StatusForbidden,
			httpreply.ErrorJSON(surface+" is loopback-only. Call it from inside the container"))
	})
	return webhttp.LoopbackOnly(refuse)(next)
}
