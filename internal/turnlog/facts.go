package turnlog

import (
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/rpcerr"
)

// Count bounds on the facts a turn accumulates across frames.
const (
	maxRequestIDs = 32
	maxRecoveries = 16
	maxSteering   = 64
	maxAsks       = 256
)

// interactionUserInput is the interaction type of a question, whose answer is
// recorded as the chosen label or the typed text rather than as an option kind.
const interactionUserInput = "user_input"

// AskOption is one option of a pending ask: its id, its kind (allow_once,
// reject_always, ...) and its label.
type AskOption struct {
	ID, Kind, Name string
}

// ask is one tool call's ask: its options from pending_interaction, and how it
// was answered once interaction_resolved arrives.
type ask struct {
	answered *marotte.ToolInteraction
	kind     string
	options  []AskOption
}

// Facts are a turn's footer facts beyond its metering: ask answers, model requests and
// throughput, recoveries, and the steering KAS added. The zero value is ready to use and
// NOT safe for concurrent use: a Turn guards its own.
type Facts struct {
	throughput *marotte.TurnThroughput
	asks       map[string]*ask
	requestIDs []string
	recoveries []string
	steering   []string
}

// NoteTurnCompletion folds one turn_completion's request ids, throughput and
// recoveries in. Tokens and streaming time sum across frames.
func (f *Facts) NoteTurnCompletion(requestIDs, recoveries []string, tp *marotte.TurnThroughput) {
	f.requestIDs = appendDistinct(f.requestIDs, requestIDs, maxRequestIDs)
	f.recoveries = appendDistinct(f.recoveries, recoveries, maxRecoveries)
	if tp == nil {
		return
	}
	if f.throughput == nil {
		f.throughput = &marotte.TurnThroughput{}
	}
	f.throughput.EstimatedTokens += tp.EstimatedTokens
	f.throughput.ActiveStreamingMs += tp.ActiveStreamingMs
}

// NoteSteering records steering documents KAS added to the turn's context.
func (f *Facts) NoteSteering(docs []string) {
	f.steering = appendDistinct(f.steering, docs, maxSteering)
}

// NoteAsk records the ask a tool call raised: its interaction type and options.
func (f *Facts) NoteAsk(callID, interactionType string, options []AskOption) {
	if callID == "" {
		return
	}
	if _, ok := f.asks[callID]; !ok && len(f.asks) >= maxAsks {
		return
	}
	if f.asks == nil {
		f.asks = make(map[string]*ask)
	}
	f.asks[callID] = &ask{kind: interactionType, options: options}
}

// NoteAnswer records how a tool call's ask was answered: an approval as the option's kind,
// a question as its label (text answers stay text). A cancelled ask records nothing.
func (f *Facts) NoteAnswer(callID, outcome, selected string) {
	if callID == "" || outcome == "" || outcome == "cancelled" {
		return
	}
	// KAS announces every ask before resolving it; an unseen ask has no options to read against.
	a := f.asks[callID]
	if a == nil {
		return
	}
	choice := selected
	for _, o := range a.options {
		if o.ID != selected {
			continue
		}
		choice = o.Kind
		if a.kind == interactionUserInput {
			choice = o.Name
		}
		break
	}
	a.answered = &marotte.ToolInteraction{Type: a.kind, Outcome: outcome, Choice: choice}
}

// WithInteraction is res carrying its call's answered ask, a copy when one is
// added; a result that already carries one keeps it.
func (f *Facts) WithInteraction(callID string, res *marotte.EntryToolResult) *marotte.EntryToolResult {
	a := f.asks[callID]
	if a == nil || a.answered == nil || res.Interaction != nil {
		return res
	}
	out := *res
	out.Interaction = a.answered
	return &out
}

// Stamp writes the facts into a turn_close footer.
func (f *Facts) Stamp(footer *marotte.EntryTurnClose) {
	footer.Throughput = f.throughput
	footer.RequestIDs = f.requestIDs
	footer.Recoveries = f.recoveries
	footer.Steering = f.steering
}

// EngineClass is an engine error's class for a broken turn, when it is a class a
// reader can be shown, and empty otherwise.
func EngineClass(errorType string, outcome marotte.TurnOutcome) string {
	if marotte.SeverityOf(outcome) != marotte.TurnSeverityBroken || !rpcerr.KnownClass(errorType) {
		return ""
	}
	return errorType
}

// NoteTurnCompletion folds one turn_completion's facts into the turn.
func (t *Turn) NoteTurnCompletion(requestIDs, recoveries []string, tp *marotte.TurnThroughput) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.facts.NoteTurnCompletion(requestIDs, recoveries, tp)
}

// NoteSteering records steering documents KAS added to the turn's context.
func (t *Turn) NoteSteering(docs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.facts.NoteSteering(docs)
}

// NoteAsk records the ask a tool call raised.
func (t *Turn) NoteAsk(callID, interactionType string, options []AskOption) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.facts.NoteAsk(callID, interactionType, options)
}

// NoteAnswer records how a tool call's ask was answered.
func (t *Turn) NoteAnswer(callID, outcome, selected string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agg.facts.NoteAnswer(callID, outcome, selected)
}

// appendDistinct appends each non-empty value of add not already in dst, up to
// limit values in all.
func appendDistinct(dst, add []string, limit int) []string {
	for _, v := range add {
		if len(dst) >= limit {
			break
		}
		if v != "" && !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}
