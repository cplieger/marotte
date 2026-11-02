package marotte

import "errors"

// ErrNoSuchTurn is what awaiting a turn the chat has no record of reports. A
// caller holding that turn's completion handle can never receive it: the record is
// retained until the handle is released.
var ErrNoSuchTurn = errors.New("no such turn")

// InterruptCause names why a turn was interrupted, in the words the transcript's
// divider renders. Empty means an ordinary end. Lives on the TURN, first-wins
// and scoped to the turn.
type InterruptCause string

// TurnStopCause is who asked to stop a chat's turn, recorded where the stop is issued, so marotte
// can classify the steering clear that stop's session/cancel produces.
type TurnStopCause uint8

const (
	// TurnStopNone is no stop since the turn opened.
	TurnStopNone TurnStopCause = iota
	// TurnStopReader is the reader's Stop: KAS re-prompts every unread notification the cancel
	// cleared (cancelLocalTurn's rewakeClearedNotifications).
	TurnStopReader
	// TurnStopTeardown is a tab close or a chat delete: KAS's cancel starts re-delivering the
	// notifications it cleared, and the session/close or session/delete that follows aborts that turn.
	TurnStopTeardown
)

// TurnResult is what a finalized turn reports. Immutable once the turn's
// completion handle fires.
type TurnResult struct {
	Interrupt InterruptCause
	Stop      StopReason
	// Turn is the id of the turn this result belongs to: the turn_open entry's id,
	// which is every handle on a turn under the log.
	Turn string
	// Reason is the failure reason the turn_close carries, empty on a clean close.
	Reason string
	// EmittedNothing is whether the turn produced content, measured AFTER the
	// steering filter's withheld text settled back in: a turn whose only text sits
	// in that carry reads as empty to any earlier measurement.
	EmittedNothing bool
	// WireEnded is whether a wire turn_end closed this turn rather than a local
	// closer, which is what makes the empty-turn recovery safe to arm: a local
	// close's end_turn says only that marotte had nothing better to call it.
	WireEnded bool
}

// EngineError is the engine's own account of a failed execution, from a
// display_error frame. Server-side only: it becomes a turn_close reason.
type EngineError struct {
	Message        string
	ErrorType      string
	RetryErrorType string
}

// TurnOpenSource names what opened a turn.
type TurnOpenSource int

const (
	// TurnSourcePrompt is a user prompt marotte is about to send.
	TurnSourcePrompt TurnOpenSource = iota
	// TurnSourceRevert holds a chat's slot while a rewind's revert lands: no
	// session/prompt is behind it, so a prompt waits rather than steering into it.
	// It only reserves: a turn opened under it would read as a revert carrier and be hidden.
	TurnSourceRevert
	// TurnSourceWireTurnStart is a turn marotte did not open: a turn_start with
	// nothing pending to bind, or a fold with no open turn, is the first it hears.
	TurnSourceWireTurnStart
	// TurnSourceEmptyRetry is the empty-turn recovery's second session/prompt: its
	// own turn, so the retry's reply does not extend a closed turn's.
	TurnSourceEmptyRetry
	// turnSourceWorkflowStep names a workflow STEP's turn, which belongs to the
	// RUN's log rather than to any chat: no production path opens a chat turn with
	// it, and runLog.open writes TurnOpenNameWorkflowStep on the run's own turns
	// directly. The member serves Name() and the source predicates; closeRun ends
	// those turns at the run's terminal transition.
	turnSourceWorkflowStep
)

// PromptClass reports whether a turn opened by this source is a user prompt
// marotte dispatched, which steer routing, parked-steer delivery and admission
// (SteerableHolder) key on. A workflow step's turn is deliberately not one: a
// steer aimed into it is read as that step's own input rather than as the
// reader's correction.
func (s TurnOpenSource) PromptClass() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}

// SteerableHolder reports whether a prompt refused by a holder of this source is
// answered Busy, which the client turns into a steer or a queued follow-up. A
// wire turn qualifies: KAS reads a steer at its next node boundary as for any
// turn, and a session/prompt beside it would abort it.
func (s TurnOpenSource) SteerableHolder() bool {
	return s.PromptClass() || s == TurnSourceWireTurnStart
}

// UserAnswered reports whether opening a turn from this source IS the user
// answering a question the agent left standing, which is what discharges the
// retained waiting_on_user status.
// Separate from PromptClass because that predicate answers for steering and
// admission, and widening one for those reasons must not move a cache lifecycle.
func (s TurnOpenSource) UserAnswered() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}

// Acknowledgeable reports whether a wire turn_start may bind to this source. Only
// a source that sent a session/prompt qualifies: a wireTurnStart turn was created
// BY one. A binding is revisable, so nothing irreversible may rest on it.
func (s TurnOpenSource) Acknowledgeable() bool {
	switch s {
	case TurnSourcePrompt, TurnSourceEmptyRetry:
		return true
	default:
		return false
	}
}
