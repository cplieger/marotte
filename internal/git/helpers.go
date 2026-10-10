package git

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/webhttp/v3"
)

// requirePOST writes a 405 if the method isn't POST and returns false.
// Returns true when the caller should proceed.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return httpreply.RequireMethod(w, r, http.MethodPost)
}

// decodePostBody enforces the JSON body cap (webhttp.MaxJSONBody) and decodes into v; on failure it
// writes a 400 with msg and returns false. Method checks are requirePOST's.
func decodePostBody(w http.ResponseWriter, r *http.Request, v any, decodeErrMsg string) bool {
	return httpreply.DecodeBody(w, r, v, decodeErrMsg)
}

// decodePostBodyOptional enforces the body cap and decodes into v; an absent or malformed body is
// ignored (v stays zero) because push/pull/stash name no required field. An oversize body writes a
// 413 and returns false.
func decodePostBodyOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBodyOptional(w, r, v)
}

// writeCmdResult writes a git-command result: {jsonKeyOutput: clientBlock(out)} on success,
// {"error": clientBlock(errMsg)} on failure, errMsg being the subprocess output when non-empty,
// else err.Error(). The output field is omitted on failure so partial stdout is not mistaken for
// success.
func writeCmdResult(w http.ResponseWriter, out string, err error) {
	if err != nil {
		webhttp.WriteJSON(w, httpreply.ErrorJSON(clientBlock(cmdFailure(out, err))))
		return
	}
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: clientBlock(out)})
}

// cmdFailure names WHY a git subprocess failed, for a caller composing several failures into one
// message: output when present, else the error, so an empty stream never yields a message ending at
// a colon.
func cmdFailure(out string, err error) string {
	if strings.TrimSpace(out) != "" {
		return out
	}
	return err.Error()
}

// Credentials come from the credential helper marotte registers per connected forge in
// ~/.gitconfig, so no per-call env injection is needed.
func gitCmdWithCreds(ctx context.Context, timeout time.Duration, dir, _ string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := gitExec(tctx, dir, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
