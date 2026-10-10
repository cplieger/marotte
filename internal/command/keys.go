package command

import (
	"maps"

	"github.com/cplieger/marotte/internal/marotte"
)

// JSON protocol keys shared by command responses and pending-permission payloads.
const (
	keyError = "error"
	keyType  = marotte.ContentKeyType
)

const keySessionID = marotte.KeySessionID

const ellipsis = "..."

// responseOK is the shared success response for commands with no return value; never mutated.
var responseOK = map[string]bool{"ok": true}

func responseWith(extra map[string]any) map[string]any {
	m := make(map[string]any, len(extra)+1)
	m["ok"] = true
	maps.Copy(m, extra)
	return m
}
