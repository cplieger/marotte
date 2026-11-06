// v3 host-mediated client requests. _kiro/terminal/shell_type still arrives from KAS's workflow
// step reloads and must be answered (KAS fails the load on -32603); _kiro/openExternalUrl
// arrives on the engine lane for MCP browser OAuth.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/translate"
)

const methodKiroShellType = "_kiro/terminal/shell_type"

func (in *inbound) handleKiroClientRequest(ctx context.Context, chatID marotte.ChatID, origin acpResponder, msg *marotte.RPCResponse) bool {
	if msg.Method != methodKiroShellType {
		return false
	}
	in.respondBridge(ctx, chatID, origin, msg, kiroShellTypeResult(), nil)
	return true
}

// consentAnchorer owns the consent URLs KAS opens: the frame names neither a server nor a session.
type consentAnchorer interface {
	// anchorConsentURL keeps url as the anchor a pasted callback with its state is relayed on, and says how to show
	// it; pageConnected is whether any page could.
	anchorConsentURL(ctx context.Context, url string, pageConnected bool) consentShown
}

// openedReply is _kiro/openExternalUrl's success.
func openedReply() map[string]any { return map[string]any{"success": true} }

// respondKiroOpenExternalURL is _kiro/openExternalUrl's one owner, on the engine lane (KAS's frame names no
// session). The registry anchors every URL: the first after a Sign in goes to its row, a state already anchored is
// answered as seen, and any other is shown on every page, refused when none is connected: a success makes KAS wait
// on a callback for up to 300 s, an error ends the attempt at once.
func (in *inbound) respondKiroOpenExternalURL(ctx context.Context, origin acpResponder, msg *marotte.RPCResponse) {
	var p struct {
		URL string `json:"url"`
	}
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	if !translate.IsWebURL(p.URL) {
		slog.Warn("v3 openExternalUrl: rejecting a link that is not a web address")
		in.respondBridge(ctx, "", origin, msg, nil, &marotte.RPCError{
			Code:    -32602,
			Message: "openExternalUrl: only http/https URLs are allowed",
		})
		return
	}
	shown := in.signIns.anchorConsentURL(ctx, p.URL, in.bus.fanout.ClientCount() > 0)
	switch shown.how {
	case consentRefused:
		slog.Warn("v3 openExternalUrl: refusing a sign-in URL, since no page is connected to show it")
		in.respondBridge(ctx, "", origin, msg, nil, &marotte.RPCError{
			Code:    -32000,
			Message: "no client can open this URL",
		})
	case consentOnRow:
		slog.Info("v3 openExternalUrl: showing the consent page on the server signing in", "server", shown.server)
		in.respondBridge(ctx, "", origin, msg, openedReply(), nil)
	case consentSeen:
		slog.Debug("v3 openExternalUrl: answering a consent URL already anchored")
		in.respondBridge(ctx, "", origin, msg, openedReply(), nil)
	case consentBanner:
		slog.Info("v3 openExternalUrl: showing a consent URL no standing Sign in took on every page")
		in.respondBridge(ctx, "", origin, msg, openedReply(), nil)
		in.bus.Broadcast(ctx, marotte.NewEvent(marotte.EventOpenExternalURL, "", marotte.OpenExternalURLPayload{URL: p.URL, ExpiresAt: shown.expires}))
	}
}

func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": marotte.HostShellType}
}
