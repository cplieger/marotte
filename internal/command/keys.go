package command

import (
	"maps"

	"github.com/cplieger/marotte/internal/httpreply"
	"github.com/cplieger/marotte/internal/marotte"
)

// JSON protocol keys shared by command responses and pending-permission payloads.
const (
	keyError = "error"
	keyName  = httpreply.JSONKeyName
	keyType  = marotte.ContentKeyType
)

// keySessionID references the canonical marotte.KeySessionID constant.
const keySessionID = marotte.KeySessionID

// ellipsis is the truncation suffix for display strings.
const ellipsis = "..."

// responseOK is the shared success response for commands with no return value; never mutated.
var responseOK = map[string]bool{"ok": true}

// responseWith returns a success response ("ok": true) with the command-specific fields merged in.
func responseWith(extra map[string]any) map[string]any {
	m := make(map[string]any, len(extra)+1)
	m["ok"] = true
	maps.Copy(m, extra)
	return m
}
