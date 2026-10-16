package marotte

// Workflow-run domain types: the EVENT surface and nothing more. A run is KAS's
// entity and marotte persists nothing about it, so there is no Run struct, no node
// tree and no plan model here — `_kiro/workflow/inspect` returns all three and GET
// /api/runs/{id} passes them through verbatim. There is deliberately no
// step-NARRATION event either: KAS's nine notification kinds carry neither a
// severity nor a message, so nothing could emit one.

import "encoding/json"

// RunProgressKind is the KAS lifecycle kind behind a run_progress event: seven of
// KAS's nine. The other two are their own events, because one inserts a row and
// the other is terminal and fires a push, while every kind between them carries
// the same instruction — refetch.
type RunProgressKind string

// The seven progress kinds, spelled as KAS's method suffixes so one string greps
// across both codebases.
const (
	RunProgressNodeStart     RunProgressKind = "node_start"
	RunProgressNodeComplete  RunProgressKind = "node_complete"
	RunProgressNodePaused    RunProgressKind = "node_paused"
	RunProgressLoopIteration RunProgressKind = "loop_iteration"
	RunProgressWatchPoll     RunProgressKind = "watch_poll"
	RunProgressPaused        RunProgressKind = "paused"
	RunProgressStepsQueued   RunProgressKind = "steps_queued"
)

// RunStartedPayload is the payload for type="run_started": a run began on this
// chat. Name is its display label, carried because a client that has never
// fetched this run has nothing to label the row with.
//
// Scheduled marks a run the SCHEDULER launched, and it travels because only the
// launch path knows: a manual launch is parentless too, so events cannot separate
// the two. It drives the client's start signal — a manual launch already has the
// user's attention, a scheduled one began with nobody looking.
//
// Initiator and InitiatorReason are KAS's attribution on the `run_start` a resume emits, verbatim;
// a launch carries neither.
type RunStartedPayload struct {
	WorkflowID      string `json:"workflow_id"`
	Name            string `json:"name,omitempty"`
	Initiator       string `json:"initiator,omitempty"`
	InitiatorReason string `json:"initiator_reason,omitempty"`
	Scheduled       bool   `json:"scheduled,omitempty"`
}

// RunProgressPayload is the payload for type="run_progress": an INVALIDATION
// signal, deliberately too thin to reconstruct a run from. `run_start` re-fires on
// every resume and `node_complete` carries neither iteration nor branchId, so an
// accumulating client could not tell two repeat iterations apart. NodeID is absent
// on the run-level `paused` and holds the loop id on `loop_iteration`.
type RunProgressPayload struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id,omitempty"`
	// Status is the node's status after this frame, in KAS's own NodeState
	// vocabulary so it drops straight onto the cached tree.
	Status string `json:"status,omitempty"`
	// StartedAt and EndedAt are RFC 3339, stamped by the SERVER at frame arrival:
	// KAS puts no timestamp on either lifecycle frame. A later refetch overwrites
	// both.
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	// FailureReason is KAS's own explanation for a node that failed. Empty on
	// every other outcome.
	FailureReason string `json:"failure_reason,omitempty"`
	// PauseReason is KAS's sentence for a node that paused; RetryReason is its sentence for a
	// node waiting out a transient failure, which stays running (`node_paused` with kind
	// `retry-wait`). Each is empty on every other frame.
	PauseReason string `json:"pause_reason,omitempty"`
	RetryReason string `json:"retry_reason,omitempty"`
	// Initiator and InitiatorReason are KAS's attribution on a run-level `paused`, verbatim.
	Initiator       string `json:"initiator,omitempty"`
	InitiatorReason string `json:"initiator_reason,omitempty"`
	// SessionID is the step session a `node_start` announces (KAS sends it on
	// the second of a step's two), the execution identity a client shares with
	// the run read. Empty on every other frame.
	SessionID string          `json:"session_id,omitempty"`
	Kind      RunProgressKind `json:"kind"`
	// NodePath addresses ONE execution of a node by its path segments, spelling
	// an iteration container `iter-<n>`. Empty on the run-level and
	// shape-changing kinds, which is what tells the client to refetch instead.
	NodePath []string `json:"node_path,omitempty"`
}

// RunPlanOutcome is where a queued plan revision stands, in KAS's `steps_queued` words. Open on
// the wire: an outcome KAS adds later reaches the run tab as its own word.
type RunPlanOutcome string

// The outcomes marotte branches on: queued until KAS applies ("applied"), rejects or drops
// ("dropped") the revision.
const (
	RunPlanQueued   RunPlanOutcome = "queued"
	RunPlanRejected RunPlanOutcome = "rejected"
)

// RunPlanUpdate is a run's newest queued plan revision, under `plan_update` in GET
// /api/runs/{id}. Pending counts the top-level steps queued and After names the top-level step
// they replace the plan after; Reason is KAS's sentence for a rejection. Held in memory, as the
// IDE holds it: a restart forgets it.
type RunPlanUpdate struct {
	After   string         `json:"after,omitempty"`
	Reason  string         `json:"reason,omitempty"`
	Outcome RunPlanOutcome `json:"outcome"`
	Pending int            `json:"pending"`
}

// RunFinishedPayload is the payload for type="run_finished": terminal. Status is
// KAS's own run-level status, `paused` included — a policy pause at
// `onMaxIterations` reports through here, because KAS emits `run_complete` for it.
//
// There is deliberately no aborted_by_restart flag: a restart PAUSES a run and
// KAS's read-path reconcile has no path to aborted, so nothing would fill it.
// Name is here for RunStartedPayload's reason, and empty when KAS sends no state.
type RunFinishedPayload struct {
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"`
	Name       string `json:"name,omitempty"`
}

// RunStepTranscriptState is the verdict GET /api/runs/{id}/steps/{path} answers
// with. A registered wire enum, so the three values have one definition across both
// languages and a client's branch over them is total. Three rather than a
// 200-with-an-empty-list, because flattening the outcomes makes every client policy
// a guess.
type RunStepTranscriptState string

// The three step-transcript verdicts.
const (
	// RunStepTranscriptReady: the transcript was read. Entries may still be EMPTY,
	// which is its own fact — the step ran and produced no prose.
	RunStepTranscriptReady RunStepTranscriptState = "ready"
	// RunStepTranscriptGone: KAS holds no session for the step, so there is nothing
	// to serve and never will be. A step that never started answers this too.
	RunStepTranscriptGone RunStepTranscriptState = "gone"
	// RunStepTranscriptUnavailable: the read failed. TRANSIENT, and the only verdict
	// a client may retry.
	RunStepTranscriptUnavailable RunStepTranscriptState = "unavailable"
)

// runStepTranscriptSource names where a step transcript was read from. A registered
// wire enum: the two sources answer a LIVE step differently (the log answers `ready`
// with the turn's sealed entries and its open tails; the replay answers
// `unavailable`, because session/load on a live session fails), and the pane reads
// the source to know which it got.
type runStepTranscriptSource string

// The two step-transcript sources.
const (
	// RunStepTranscriptSourceLog: the run's own entry log, the primary read for
	// every run written after the cutover.
	RunStepTranscriptSourceLog runStepTranscriptSource = "log"
	// RunStepTranscriptSourceReplay: KAS's replay of the step's own session,
	// projected into the same shape. The fallback for a path the log holds no turn
	// for: a run predating the cutover, or one launched from the TUI. Lossy (no
	// diffs, no terminal blocks, no model id).
	RunStepTranscriptSourceReplay runStepTranscriptSource = "replay"
)

// RunStepTranscript is GET /api/runs/{id}/steps/{path}'s reply: every turn of the run's log at
// that node path in file order, with open tails, or KAS's replay projected into the same shape when
// the log holds none. No `omitempty` anywhere, so the generated fields are required and a client
// cannot assume a verdict.
type RunStepTranscript struct {
	// WorkflowID and NodePath (the step's workflow.PathKey) echo the request, so a
	// client holding several reads in flight tells the answers apart without correlating.
	WorkflowID string                  `json:"workflow_id"`
	NodePath   string                  `json:"node_path"`
	State      RunStepTranscriptState  `json:"state"`
	Source     runStepTranscriptSource `json:"source"`
	// Entries is the step's transcript. Empty on any state but ready, and
	// legitimately empty on ready.
	Entries []Entry `json:"entries"`
	// OpenEntries holds the in-memory tails of the path's open turns: empty on a
	// settled path and on the replay source.
	OpenEntries []OpenEntry `json:"open_entries"`
	// Subject is one run_turn stamp per open turn of the requested path, read under
	// the run store's lock with the entries it certifies; empty on a settled path and
	// on the replay source, which no subject versions.
	Subject []*SubjectStamp `json:"subject"`
}

// RunAsksPayload is the payload for type="run_asks": every step of one run waiting on a person, as
// marotte last read it, replacing the client's previous snapshot of that run whole. A newer
// (epoch, seq) wins and an older or equal one is dropped. Reachable false means the asks are the
// last ones read, or none when the run was not read this epoch, so a client closes nothing on it.
// The run's final frame has no asks and Reachable true; a run a pending_snapshot no longer names
// has ended.
type RunAsksPayload struct {
	WorkflowID string `json:"workflow_id"`
	// ChatID is the launching chat, else `run:<workflowId>`, as the envelope's.
	ChatID ChatID `json:"chat_id"`
	Epoch  string `json:"epoch"`
	// Asks is never null.
	Asks      []RunAsk `json:"asks"`
	Seq       uint64   `json:"seq"`
	Reachable bool     `json:"reachable"`
}

// RunAsk is one step waiting on a person, in run_asks and under `open_asks` in GET /api/runs/{id}.
// AskID changes with every turn of the step marotte sees start, so an answer typed against an
// older one is refused as stale. Blocked is absent when an answer may go.
type RunAsk struct {
	AskID         string     `json:"ask_id"`
	NodeID        string     `json:"node_id"`
	StepSessionID string     `json:"step_session_id"`
	AgentName     string     `json:"agent_name"`
	Text          string     `json:"text"`
	TextSource    RunAskText `json:"text_source"`
	// Reason is kiro-cli's sentence for why a step that has no completion signal is waiting, such
	// as one whose signal it could not save; absent otherwise.
	Reason   string        `json:"reason,omitempty"`
	Blocked  RunAskBlocker `json:"blocked,omitempty"`
	AskedAt  string        `json:"asked_at,omitempty"`
	NodePath []string      `json:"node_path"`
}

// RunAskText says where an ask's text came from, so a card labels it: only `asked` is the step's
// own question.
type RunAskText string

const (
	// RunAskTextAsked is the warning the step sent in the turn that parked it.
	RunAskTextAsked RunAskText = "asked"
	// RunAskTextLastMessage is the step's last words, which may not be a question.
	RunAskTextLastMessage RunAskText = "last_message"
	// RunAskTextUnseen means marotte did not see what the step said.
	RunAskTextUnseen RunAskText = "unseen"
)

// RunAskBlocker is why an ask cannot be answered now.
type RunAskBlocker string

const (
	// RunAskStopping means a cancel or delete of the run is queued.
	RunAskStopping RunAskBlocker = "stopping"
	// RunAskPending means a prompt marotte sent to a step of this run is still being delivered.
	RunAskPending RunAskBlocker = "pending"
	// RunAskBranchRunning means another leaf node of the run, of any type, is still running.
	RunAskBranchRunning RunAskBlocker = "branch_running"
	// RunAskUnsettled means the run looks live but nothing has moved for a while.
	RunAskUnsettled RunAskBlocker = "unsettled"
	// RunAskSettling means the run looked live and moving at the last read; a read after a quiet
	// spell reads unsettled instead.
	RunAskSettling RunAskBlocker = "settling"
)

// RunAskRefusal is the machine `code` of a refused answer or step message about an ask.
// Only RunAskRefusedSettling and RunAskRefusedUnreadable are worth retrying unchanged.
type RunAskRefusal string

// The ask refusal codes.
const (
	RunAskRefusedStopping      RunAskRefusal = "ask_stopping"
	RunAskRefusedPending       RunAskRefusal = "ask_pending"
	RunAskRefusedStale         RunAskRefusal = "ask_stale"
	RunAskRefusedGone          RunAskRefusal = "ask_gone"
	RunAskRefusedBranchRunning RunAskRefusal = "ask_branch_running"
	RunAskRefusedUnsettled     RunAskRefusal = "ask_unsettled"
	RunAskRefusedSettling      RunAskRefusal = "ask_settling"
	RunAskRefusedUnreadable    RunAskRefusal = "ask_unreadable"
	// RunAskRefusedUnnamed refuses words naming no ask for a path more than one started execution shares.
	RunAskRefusedUnnamed RunAskRefusal = "ask_unnamed"
	// RunAnswerPending refuses a resume, retry or pause while an answer to the run is in flight.
	RunAnswerPending RunAskRefusal = "run_answer_pending"
)

// RunStepEnd is how a step's newest turn in the run's own log ended, under `step_ends` in GET /api/runs/{id}
// keyed by node path, for a step whose turn closed broken. KAS grades a refused step, and one its iteration limit
// stopped, `completed`, so its node status cannot say this.
type RunStepEnd struct {
	Outcome       TurnOutcome `json:"outcome"`
	FailureReason string      `json:"failure_reason,omitempty"`
	FailureKind   FailureKind `json:"failure_kind,omitempty"`
}

// RunStepStart is when a node's current attempt began in the run's own log, under `step_starts` in GET /api/runs/{id}
// keyed by node path. KAS restamps a node's startedAt each time it re-enters it (a resume, a restart), so its tree
// cannot say this. Ended is true once the attempt's newest turn closed for any reason but an interruption: a node KAS
// shows in flight past an ended attempt is on a new one.
type RunStepStart struct {
	// StartedAt is RFC 3339.
	StartedAt string `json:"started_at"`
	Ended     bool   `json:"ended"`
}

// RunAnswerRequest is POST /api/runs/{id}/answer's body: answer one parked step. Empty Text is a 400.
type RunAnswerRequest struct {
	AskID string `json:"ask_id"`
	Text  string `json:"text"`
}

// RunStepMessageRequest is POST /api/runs/{id}/steps/{path}/message's body: the user's words for
// the step at that node path (the wire path, `iter-<n>` included, each segment escaped). MessageID is
// the client's message id; a steer's id derives from it (SteerIDFor).
type RunStepMessageRequest struct {
	Text      string `json:"text"`
	MessageID string `json:"message_id"`
	// AskID, when set, is the ask the words answer: a different ask at the path refuses them as stale.
	AskID string `json:"ask_id,omitempty"`
}

// RunStepSteerRequest is POST /api/runs/{id}/steps/{path}/steer-remove's body: the unread row to
// delete. Its sibling /steer-clear, every unread row of the step, takes no body.
type RunStepSteerRequest struct {
	SteerID string `json:"steer_id"`
}

// RunStepMessageVerb is how the server delivered a step message, picked from the step's live state.
type RunStepMessageVerb string

const (
	// RunStepMessageSteer means the step was running, so the words joined its turn as a steer.
	RunStepMessageSteer RunStepMessageVerb = "steer"
	// RunStepMessageAnswer means the step had asked, so the words answered its question.
	RunStepMessageAnswer RunStepMessageVerb = "answer"
	// RunStepMessagePrompt means the step was paused with no question, so the words resumed it.
	RunStepMessagePrompt RunStepMessageVerb = "prompt"
)

// RunStepMessageResponse is the message route's reply. SteerID is set for the steer verb only:
// the dock row's id.
type RunStepMessageResponse struct {
	Verb    RunStepMessageVerb `json:"verb"`
	SteerID string             `json:"steer_id,omitempty"`
}

// RunStepMessageRefusal is the machine `reason` of a 409 from the message route, for the client to
// word.
type RunStepMessageRefusal string

const (
	// RunStepNotStarted means the step has no session yet.
	RunStepNotStarted RunStepMessageRefusal = "not_started"
	// RunStepFinished means the step, or its whole run, has ended.
	RunStepFinished RunStepMessageRefusal = "finished"
	// RunStepRunPaused means the run is paused while this step is not, so Resume is the way on.
	RunStepRunPaused RunStepMessageRefusal = "run_paused"
	// RunStepBusy means the step is between states (a resume in flight); retry.
	RunStepBusy RunStepMessageRefusal = "busy"
	// RunStepFull means too many unread messages are waiting for the step.
	RunStepFull RunStepMessageRefusal = "full"
)

// RunLaunchRequest is POST /api/runs's body: launch one recipe, PARENTLESS.
//
// Source is `_kiro/workflow/listRecipes`' own value verbatim. The server
// re-validates it against a fresh listRecipes call rather than trusting it, so the
// endpoint cannot be steered at an arbitrary file even though the value looks like
// a path.
type RunLaunchRequest struct {
	Inputs map[string]string `json:"inputs,omitempty"`
	Source string            `json:"source"`
	// Model and Effort are the run's step defaults (RunDefaults); empty sends none.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// RunDefaults are `_kiro/workflow/new`'s `modelId` and `effortLevel`: the model and effort tier
// of every step that pins neither, over the recipe's own root values. The zero value sends
// nothing, so KAS's own order applies.
type RunDefaults struct {
	Model  string
	Effort string
}

// RunLaunchedResponse is POST /api/runs's reply. Name is the recipe's, so the
// client can label the tab it opens without waiting for the first refetch.
type RunLaunchedResponse struct {
	WorkflowID string `json:"workflow_id"`
	Name       string `json:"name"`
}

// Recipe is one launchable workflow definition, projected from
// `_kiro/workflow/listRecipes` for GET /api/recipes.
type Recipe struct {
	// Inputs maps input name → declared type (`string`, `prompt`, `file`).
	Inputs      map[string]string `json:"inputs,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	// ValidationError is KAS's reason the recipe cannot launch (a schema error, a shadowed name,
	// an include that fails to expand), verbatim; empty for a launchable recipe.
	ValidationError string `json:"validation_error,omitempty"`
	// Source is the launch key: `bundled://<name>` or a workspace or home
	// `*.workflow.json` path. Echoed back verbatim in RunLaunchRequest.
	Source string `json:"source"`
	// DocKey is a file-backed recipe's file as the configuration standing keys it (translate.DocKeyFromAbs of Source),
	// so its row joins that item's standing; empty for a bundled recipe.
	DocKey string `json:"doc_key,omitempty"`
	// Plan is KAS's node plan, forwarded VERBATIM: what the recipe will do and on
	// which model, raw for the reason at the top of this file. Last of the
	// pointer-bearing fields on purpose — a slice's len/cap words end the GC scan
	// region where a trailing string would extend it.
	Plan    json.RawMessage `json:"plan,omitempty"`
	BuiltIn bool            `json:"built_in,omitempty"`
}

// RecipesResponse is GET /api/recipes's reply.
type RecipesResponse struct {
	Recipes []Recipe `json:"recipes"`
}

// LiveRun is one row of GET /api/runs/live: a run marotte's own lease registry says
// is in flight, named with the chat whose agent launched it. ChatID is empty for a
// parentless run and for a lease predating the field — both mean "no chat to launch
// a tab under". Executing is a FIELD rather than a filter applied here because
// three consumers ask this row three different questions, and filtering for the
// eviction sweep would take the row away from the tab dot and the tab parent.
type LiveRun struct {
	WorkflowID string `json:"workflow_id"`
	ChatID     string `json:"chat_id"`
	// Executing reports whether THIS PROCESS holds a deadline for the run. NOT
	// `status` and none of KAS's five, because serving one would mean an `inspect` per
	// lease on the client's boot path (runlease.Lease.Deadline owns the answer).
	//
	// It can read false while KAS says running — a lease read back from disk is
	// parked, and `set_step_status` advances a step without re-arming — and both are
	// accepted: the client corrects itself on the run's next progress frame.
	Executing bool `json:"executing"`
}

// LiveRunsResponse is GET /api/runs/live's reply.
type LiveRunsResponse struct {
	// Subject is the `runs` digest stamp with the hub epoch, paired with the set
	// under the lease store's lock.
	Subject *SubjectStamp `json:"subject,omitempty"`
	Runs    []LiveRun     `json:"runs"`
}

// RunControlsResponse is GET /api/runs/{id}/controls's reply: what may be done to
// one run, and why not for the rest.
//
// Its own route rather than a field on GET /api/runs/{id}, a verbatim KAS
// passthrough. Only the server sees all three inputs: a client's SSE-fed cache of
// which chat launched the run is empty after a reload.
type RunControlsResponse struct {
	// Refused maps a verb this run does not offer to the one sentence a reader
	// needs, and carries only a verb whose absence would otherwise be unexplained.
	Refused map[string]string `json:"refused,omitempty"`
	// ParentChatID names the chat whose agent launched the run, empty for a
	// parentless one. Read from the chat store here rather than from an event-fed
	// client cache, which is empty after a reload.
	ParentChatID string `json:"parent_chat_id"`
	// PauseNodeID is the repeat a maxIterations pause stopped at; the extend and
	// finish_loop verbs send it back. Empty for every other run.
	PauseNodeID string `json:"pause_node_id,omitempty"`
	// Verbs are the offered controls, in row order. Strings rather than a
	// registered enum because the client's label table is the narrowing point: an
	// unlabelled verb is dropped, so a future one degrades to a missing button.
	Verbs []string `json:"verbs"`
}

// RunExtendRequest is POST /api/runs/{id}/extend's body: the paused repeat and how
// many more iterations it gets (1..1000).
type RunExtendRequest struct {
	NodeID     string `json:"node_id"`
	Iterations int    `json:"iterations"`
}

// RunFinishLoopRequest is POST /api/runs/{id}/finish-loop's body: the paused repeat
// to end.
type RunFinishLoopRequest struct {
	NodeID string `json:"node_id"`
}

// RunRetriedResponse is POST /api/runs/{id}/retry's reply: KAS's own outcome
// report, forwarded rather than collapsed to `{"ok":true}`. RetriedNodeIDs is why
// the route exists — a retry that resets five nodes and one that resets none are
// otherwise the same HTTP result, and the second is what "nothing happened" is.
type RunRetriedResponse struct {
	// Status is the run's status after the reset, as KAS reports it.
	Status         string   `json:"status"`
	RetriedNodeIDs []string `json:"retried_node_ids"`
}
