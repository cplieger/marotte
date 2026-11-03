package marotte

import (
	"strings"

	"github.com/cplieger/marotte/internal/ids"
)

// KeepFact is one reason the chat must not be purged, stored under its holder key in Chat.Keep.
type KeepFact struct {
	Boot    string `json:"boot"`              // boot id of the process that wrote it
	Token   string `json:"token,omitempty"`   // the write that took it; a rollback undoes only its own
	Since   int64  `json:"since"`             // Unix ms the fact was written; diagnostics only
	Process bool   `json:"process,omitempty"` // dies with the process that wrote it
}

// The fixed holder keys; a run's keys are built by KeyRun and KeyRunTab.
const (
	KeyTab  = "tab"
	KeyTurn = "turn"
	// KeyStop is a person's close stopping the chat's runs, from the close's commit until every stop has ended.
	KeyStop = "stop"
)

// The kinds KeepKind answers, one per holder key shape.
const (
	KeepKindTab    = "tab"
	KeepKindRun    = "run"
	KeepKindRunTab = "runtab"
	KeepKindTurn   = "turn"
	KeepKindStop   = "stop"
)

const (
	keyRunPrefix    = "run:"
	keyRunTabPrefix = "runtab:"
)

// KeyRun is the key of "a run this chat launched is not terminal"; false for an invalid workflow id.
func KeyRun(workflowID string) (string, bool) {
	if !ids.ValidChatID(workflowID) {
		return "", false
	}
	return keyRunPrefix + workflowID, true
}

// KeyRunTab is the key of "a run tab shows a run this chat launched"; false for an invalid workflow id.
func KeyRunTab(workflowID string) (string, bool) {
	if !ids.ValidChatID(workflowID) {
		return "", false
	}
	return keyRunTabPrefix + workflowID, true
}

// KeepKind names the kind of a holder key and, for a run key, its workflow id. An unrecognised key
// answers "", which every reader treats as a live fact.
func KeepKind(key string) (kind, workflowID string) {
	switch {
	case key == KeyTab:
		return KeepKindTab, ""
	case key == KeyTurn:
		return KeepKindTurn, ""
	case key == KeyStop:
		return KeepKindStop, ""
	case strings.HasPrefix(key, keyRunTabPrefix):
		if wf := strings.TrimPrefix(key, keyRunTabPrefix); ids.ValidChatID(wf) {
			return KeepKindRunTab, wf
		}
	case strings.HasPrefix(key, keyRunPrefix):
		if wf := strings.TrimPrefix(key, keyRunPrefix); ids.ValidChatID(wf) {
			return KeepKindRun, wf
		}
	}
	return "", ""
}
