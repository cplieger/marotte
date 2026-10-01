package translate

import "github.com/cplieger/marotte/internal/marotte"

// The KAS door's enum gate. `encoding/json` fills a named string type from ANY
// string, so a kind or status this build does not know would reach the entry log
// verbatim, and the generated decoder validates both against a closed set: one
// unknown value costs the reader the card, or a strict boot window. A closed enum
// on the wire is only honest where marotte derives or normalises the value, so it
// is normalised here, in the one package that unmarshals a KAS frame.

// knownToolKinds is the ToolKind set the wire declares.
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

// knownToolStatuses is the ToolStatus set the wire declares. ToolAborted is
// marotte's own and never arrives on the wire; it is a value the entry log
// carries, so it belongs to the set a frame may not widen past.
var knownToolStatuses = map[marotte.ToolStatus]struct{}{
	marotte.ToolPending:    {},
	marotte.ToolInProgress: {},
	marotte.ToolCompleted:  {},
	marotte.ToolFailed:     {},
	marotte.ToolAborted:    {},
}

// knownPlanStatuses is the PlanStatus set the wire declares.
var knownPlanStatuses = map[marotte.PlanStatus]struct{}{
	marotte.PlanPending:    {},
	marotte.PlanInProgress: {},
	marotte.PlanCompleted:  {},
}

// toolKindFromWire maps an unrecognized kind to ToolKindOther, which is ACP's own
// default for an absent kind and loses nothing: WorkingLabelForKind already reads
// `other` and an unknown kind as the same label.
func toolKindFromWire(k marotte.ToolKind) marotte.ToolKind {
	if _, ok := knownToolKinds[k]; ok {
		return k
	}
	return marotte.ToolKindOther
}

// toolStatusFromWire maps an unrecognized status to ToolInProgress: the call
// exists and no outcome is known, so the turn's close rule settles it as
// `aborted` rather than claiming an outcome the tool never reported. An EMPTY
// status is returned unchanged, because absent means unchanged on an update; the
// create door substitutes ACP's default itself.
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

// gate normalises the enum-typed fields a create frame carries. A create
// describes the whole card, so an absent kind or status takes ACP's own default.
func (w *ACPToolCallWire) gate() {
	w.Kind = toolKindFromWire(w.Kind)
	if w.Status == "" {
		w.Status = marotte.ToolPending
	}
	w.Status = toolStatusFromWire(w.Status)
}

// gate normalises the enum-typed fields an update frame carries. An update names
// only what changed, so an absent field is left absent for the fold's own
// unchanged rule (applyToolCallStatus, applyToolCallTitleAndKind).
func (w *ACPToolCallUpdateWire) gate() {
	if w.Kind != "" {
		w.Kind = toolKindFromWire(w.Kind)
	}
	w.Status = toolStatusFromWire(w.Status)
}

// gate normalises every plan row's status.
func (w *ACPPlanWire) gate() {
	for i := range w.Entries {
		w.Entries[i].Status = planStatusFromWire(w.Entries[i].Status)
	}
}
