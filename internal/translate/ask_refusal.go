package translate

// The three ask handlers' shared convention: an ask marotte declines gets a well-formed
// fail-closed answer, never a bare return.

import (
	"context"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// refuseAsk answers a server-to-client ask whose params did not decode, on the bridge it arrived
// on. KAS's sendRequest has no timeout, so not answering strands the tool batch. The answer is the
// kind's own fail-closed RESULT, never a JSON-RPC error: KAS's turn-approval path fails OPEN on an
// error, applying every unreviewed write. Nothing decoded from the frame is logged (untrusted text).
func refuseAsk(
	ctx context.Context, chatID marotte.ChatID, origin AskOrigin, method string, requestID int64, result any, cause error,
) {
	slog.Warn("ask params did not decode, answering fail-closed",
		"method", method, "request_id", requestID, "chat_id", chatID, "error", cause)
	if err := origin.Respond(ctx, requestID, result, nil); err != nil {
		slog.Warn("fail-closed ask answer not delivered",
			"method", method, "request_id", requestID, "chat_id", chatID, "error", err)
	}
}
