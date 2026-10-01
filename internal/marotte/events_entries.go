package marotte

// The SSE payloads of the entry log's live path: one frame per append to the
// log, one per delta into an open entry, and two live-only replaces (tool
// progress, code references) whose durable value the tool_result and the
// turn_close carry. Every one is scoped to a chat by the envelope's chat id, or
// to a RUN by an empty chat id plus WorkflowID in the payload, so the client
// routes a run's frames to the run store without a lookup.

// TurnOpenedPayload is the payload for type="turn_opened": the turn_open entry as
// appended, the first frame of every turn.
type TurnOpenedPayload struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	Entry      Entry  `json:"entry"`
}

// EntryOpenedPayload is the payload for type="entry_opened": a text or thinking
// entry that just opened in its lane, with the text so far and the count of deltas
// folded into it (1 when the first delta opened it).
//
// It carries NO refusal: a refusal-tagged chunk opens no entry, it SEALS the
// lane's open one, so the live carrier is that seal's own frame
// (EntrySealedPayload.Refusal).
type EntryOpenedPayload struct {
	WorkflowID string    `json:"workflow_id,omitempty"`
	Open       OpenEntry `json:"open"`
}

// EntryDeltaPayload is the payload for type="entry_delta": one more piece of an
// open entry's text. N is the running delta count AFTER this delta is applied, so
// the first delta after an entry_opened with n: 1 carries n: 2 and the client
// requires n == held + 1; a hole is the signal to re-read the turn's range. Lane
// is carried so a lane-scoped subscriber routes on the frame without an entry-id
// lookup, and it is REQUIRED on the wire: "" is the lane of the agent that owns
// the turn, so an omitted field and a main-lane frame would be one value at a
// reader that keys its open entries by lane. It carries NO refusal, for
// EntryOpenedPayload's reason: the live carrier is EntrySealedPayload.Refusal.
type EntryDeltaPayload struct {
	Turn       string `json:"turn"`
	EntryID    string `json:"entry_id"`
	Lane       string `json:"lane"`
	Delta      string `json:"delta"`
	WorkflowID string `json:"workflow_id,omitempty"`
	N          uint64 `json:"n"`
}

// EntrySealedPayload is the payload for type="entry_sealed": an open entry froze
// and took its place in the log. Seq and Ts are the log's; N is the total delta
// count the sealed text holds, which the client compares to the n it holds for the
// open entry (a count, so no byte-versus-UTF-16 length question arises). Lane is
// REQUIRED for EntryDeltaPayload's reason: "" is a real lane.
//
// Refusal is the LIVE carrier of a turn's refusal note, and this frame is where it
// belongs because a refusal-tagged chunk opens no entry: it SEALS the lane's open
// one, so the seal is the frame the refusal branch already publishes. Present at
// most once per turn; the durable copy is turn_close.refusal, which the client
// prefers. Two endings carry no seal frame and so defer to turn_close: a lane
// holding only a steer carry (released as entry_appended) and an empty lane (no
// frame at all).
type EntrySealedPayload struct {
	Refusal    *RefusalInfo `json:"refusal,omitempty"`
	Turn       string       `json:"turn"`
	EntryID    string       `json:"entry_id"`
	Lane       string       `json:"lane"`
	WorkflowID string       `json:"workflow_id,omitempty"`
	Seq        uint64       `json:"seq"`
	Ts         int64        `json:"ts"`
	N          uint64       `json:"n"`
}

// EntryAppendedPayload is the payload for type="entry_appended": a born-sealed
// entry (turn_bind, tool_call, tool_result, steer, steer_ack, plan, compaction,
// compaction_failed, safety_blocked, model_switched), or a text entry opened and
// sealed in one step, which never had an entry_opened and so travels as one frame.
type EntryAppendedPayload struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	Entry      Entry  `json:"entry"`
}

// ToolProgressPayload is the payload for type="tool_progress": a DELTA on an
// in-flight tool call, addressed by turn and id, carrying only what this frame
// changed. Every field is omitempty and means "unchanged" when absent;
// OutputDelta's meaning depends on OutputReplace. Live only: the durable value is
// the tool_result entry, whose payload is the settled value of the same fields.
type ToolProgressPayload struct {
	// The three metadata blocks, each sent whole when it changed; none accumulates.
	Checkpoint *ToolCheckpoint `json:"checkpoint,omitempty"`
	Disclosed  *ToolDisclosed  `json:"disclosed,omitempty"`
	Denial     *ToolDenial     `json:"denial,omitempty"`
	Turn       string          `json:"turn"`
	ToolCallID string          `json:"tool_call_id"`
	// Title and Kind: KAS sends them nullish on most updates, so absent is "keep".
	Title  string     `json:"title,omitempty"`
	Kind   ToolKind   `json:"kind,omitempty"`
	Status ToolStatus `json:"status,omitempty"`
	// OutputDelta is normally the text to APPEND; when OutputReplace is set it is the
	// whole output instead. The replace case is load-bearing: at completion a terminal's
	// full stream wins over the ACP fragments already on the card.
	OutputDelta string `json:"output_delta,omitempty"`
	// TerminalID, AgentSubtaskID and WorkflowID are late identity attachments, each
	// adopted once. AgentSubtaskID is informational: the card's lane is its
	// tool_call's, and a disagreeing value is logged rather than adopted. WorkflowID
	// doubles as the run scope on a run's frame.
	TerminalID     string `json:"terminal_id,omitempty"`
	AgentSubtaskID string `json:"agent_subtask_id,omitempty"`
	WorkflowID     string `json:"workflow_id,omitempty"`
	// OutputSpans style the WHOLE output at absolute offsets, so they are sent entire
	// whenever they change. Empty for output carrying no escape sequence.
	OutputSpans []TextSpan `json:"output_spans,omitempty"`
	// DiffsAppended are the diffs this frame added; diffs only ever append, so there is
	// no replace case.
	DiffsAppended []ToolDiff `json:"diffs_appended,omitempty"`
	// Locations are REPLACED wholesale when present.
	Locations []ToolLocation `json:"locations,omitempty"`
	// The scalars last, so the GC scan region stops above them (govet fieldalignment).
	// OutputReplace's meaning is on OutputDelta.
	DurationMs    int  `json:"duration_ms,omitempty"`
	OutputReplace bool `json:"output_replace,omitempty"`
	// Declined is a ONE-WAY latch, so absent means unchanged rather than false: the
	// mark is set on the frame that reports the refusal and no later frame carries a
	// verdict. That is what makes `omitempty` on a bool correct here — the only value
	// it ever sends is true.
	Declined bool `json:"declined,omitempty"`
}

// TurnClosedPayload is the payload for type="turn_closed": the turn_close entry as
// appended, the last frame of every turn. The server's status-cache clear fires on
// it for every turn, because every turn in a chat's log is the chat's.
type TurnClosedPayload struct {
	WorkflowID string `json:"workflow_id,omitempty"`
	Entry      Entry  `json:"entry"`
}
