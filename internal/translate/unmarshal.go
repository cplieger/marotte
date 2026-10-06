package translate

import (
	"encoding/json"
	"log/slog"

	"github.com/cplieger/marotte/internal/marotte"
)

// unmarshalParams decodes msg.Params into T, logging a failure at Debug and returning
// the zero value with false.
func unmarshalParams[T any](msg *marotte.RPCResponse, method string) (T, bool) {
	p, err := decodeParams[T](msg)
	if err != nil {
		slog.Debug("translate: unmarshal failed", "method", method, "error", err)
		return p, false
	}
	return p, true
}

// decodeParams decodes msg.Params into T and hands the decode error back, for handlers
// that ANSWER a failed decode: their Warn refusal must carry the reason.
func decodeParams[T any](msg *marotte.RPCResponse) (T, error) {
	var p T
	err := json.Unmarshal(msg.Params, &p)
	return p, err
}
