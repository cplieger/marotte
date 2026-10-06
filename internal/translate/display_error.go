package translate

import (
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/runesafe/v2"
)

// displayErrorBlock is the kind=="display_error" sub-block: the engine's own
// sentence for a faulted execution. errorType is a minified class name for an
// unmapped error, so it is never rendered.
type displayErrorBlock struct {
	Message        string `json:"message"`
	ErrorType      string `json:"errorType"`
	RetryErrorType string `json:"retryErrorType"`
}

// displayErrorMCPConnection is the MCP connect failure KAS broadcasts to every
// session; _kiro/mcp/status already reports it with the server's name.
const displayErrorMCPConnection = "mcp_connection_error"

// maxDisplayErrorBytes bounds the latched message, which becomes a turn reason.
const maxDisplayErrorBytes = 2048

// handleDisplayError latches the engine's account on the chat's own open turn.
// It is not a verdict: a retried attempt or a sub-agent fault on a turn that
// recovers writes one too, so the close reads it only for a broken outcome.
func (t *Translator) handleDisplayError(chatID marotte.ChatID, d *displayErrorBlock) {
	if d.ErrorType == displayErrorMCPConnection {
		slog.Debug("display_error: MCP connect failure, reported by _kiro/mcp/status", "chat_id", chatID)
		return
	}
	msg, _ := runesafe.SanitizeSingleLineCapped(d.Message, maxDisplayErrorBytes, "...")
	if msg == "" {
		return
	}
	turn, ok := t.turns.OwnTurn(chatID)
	if !ok {
		slog.Debug("display_error with no open turn, dropped", "chat_id", chatID)
		return
	}
	turn.SetEngineError(marotte.EngineError{Message: msg, ErrorType: d.ErrorType, RetryErrorType: d.RetryErrorType})
}
