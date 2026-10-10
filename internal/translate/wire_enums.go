package translate

import "github.com/cplieger/marotte/internal/marotte"

// The KAS door's enum gate: `encoding/json` fills a named string type from ANY string,
// and the generated client decoder validates against a closed set, so one unknown value
// costs the reader the card. Normalised here, the one package unmarshalling KAS frames.

var knownToolKinds = map[marotte.ToolKind]struct{}{
	marotte.ToolKindExecute:    {},
	marotte.ToolKindShell:      {},
	marotte.ToolKindRead:       {},
	marotte.ToolKindSearch:     {},
	marotte.ToolKindFetch:      {},
	marotte.ToolKindEdit:       {},
	marotte.ToolKindThink:      {},
	marotte.ToolKindHook:       {},
	marotte.ToolKindWrite:      {},
	marotte.ToolKindDelete:     {},
	marotte.ToolKindMove:       {},
	marotte.ToolKindCommand:    {},
	marotte.ToolKindBrowser:    {},
	marotte.ToolKindSwitchMode: {},
	marotte.ToolKindMCP:        {},
	marotte.ToolKindOther:      {},
}

// knownToolStatuses is the ToolStatus set the wire declares, plus marotte's own
// ToolAborted, which the entry log carries.
var knownToolStatuses = map[marotte.ToolStatus]struct{}{
	marotte.ToolPending:    {},
	marotte.ToolInProgress: {},
	marotte.ToolCompleted:  {},
	marotte.ToolFailed:     {},
	marotte.ToolAborted:    {},
}

var knownPlanStatuses = map[marotte.PlanStatus]struct{}{
	marotte.PlanPending:    {},
	marotte.PlanInProgress: {},
	marotte.PlanCompleted:  {},
}

// toolKindFromWire maps an unrecognized kind to ToolKindOther, ACP's own default;
// WorkingLabelForKind labels both the same.
func toolKindFromWire(k marotte.ToolKind) marotte.ToolKind {
	if _, ok := knownToolKinds[k]; ok {
		return k
	}
	return marotte.ToolKindOther
}

// toolStatusFromWire maps an unrecognized status to ToolInProgress, so the turn's close
// settles it as `aborted` rather than claiming an outcome. An EMPTY status is returned
// unchanged (absent means unchanged on an update).
func toolStatusFromWire(s marotte.ToolStatus) marotte.ToolStatus {
	if s == "" {
		return s
	}
	if _, ok := knownToolStatuses[s]; ok {
		return s
	}
	return marotte.ToolInProgress
}

// planStatusFromWire maps an unrecognized plan status to PlanPending, which
// under-claims progress where the other two members would over-claim it.
func planStatusFromWire(s marotte.PlanStatus) marotte.PlanStatus {
	if _, ok := knownPlanStatuses[s]; ok {
		return s
	}
	return marotte.PlanPending
}

// A create describes the whole card, so an absent kind or status takes ACP's own default.
func (w *acpToolCallWire) gate() {
	w.Kind = toolKindFromWire(w.Kind)
	if w.Status == "" {
		w.Status = marotte.ToolPending
	}
	w.Status = toolStatusFromWire(w.Status)
}

// An absent field stays absent for the fold's unchanged rule.
func (w *acpToolCallUpdateWire) gate() {
	if w.Kind != "" {
		w.Kind = toolKindFromWire(w.Kind)
	}
	w.Status = toolStatusFromWire(w.Status)
}

func (w *acpPlanWire) gate() {
	for i := range w.Entries {
		w.Entries[i].Status = planStatusFromWire(w.Entries[i].Status)
	}
}
