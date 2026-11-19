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
	// anchorConsentURL decides how to show req's URL and keeps it as an anchor when it carries a state;
	// pageConnected is whether any page could.
	anchorConsentURL(ctx context.Context, req consentRequest, pageConnected bool) consentShown
}

// openedReply is _kiro/openExternalUrl's success.
func openedReply() map[string]any { return map[string]any{"success": true} }

// respondKiroOpenExternalURL is _kiro/openExternalUrl's one owner, on the engine lane (KAS's frame names no
// session). anchorConsentURL owns the showing; consentRefused is the only error, which ends KAS's attempt at once.
func (in *inbound) respondKiroOpenExternalURL(ctx context.Context, origin acpResponder, msg *marotte.RPCResponse) {
	var p consentRequest
	if msg.Params != nil {
		_ = json.Unmarshal(msg.Params, &p)
	}
	p.URL = withGoogleOfflineAccess(p.URL)
	if !translate.IsWebURL(p.URL) {
		slog.Warn("v3 openExternalUrl: rejecting a link that is not a web address")
		in.respondBridge(ctx, "", origin, msg, nil, &marotte.RPCError{
			Code:    -32602,
			Message: "openExternalUrl: only http/https URLs are allowed",
		})
		return
	}
	shown := in.signIns.anchorConsentURL(ctx, p, in.bus.fanout.ClientCount() > 0)
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
		slog.Info("v3 openExternalUrl: showing a consent URL no standing Sign in took on every page", "server", p.Server)
		in.respondBridge(ctx, "", origin, msg, openedReply(), nil)
	}
}

func kiroShellTypeResult() map[string]any {
	return map[string]any{"shellType": marotte.HostShellType}
}
