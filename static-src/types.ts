// ---------------------------------------------------------------------------
// Client-side type definitions.
//
// Wire-format interfaces (Message, ChatHeader, ToolCall, etc.) and their
// enums are generated from the Go server's api/* structs at build time.
// They live in ./wire/types.gen.ts and are re-exported below as the
// canonical types for the rest of the app. Edit the Go side and re-run
// `go run ./cmd/wire-codegen` to update them.
//
// Client-only types (Session projection, ConnectionStatus, ModelInfo,
// BannerLevel, PendingTrust*) are declared here.
// ---------------------------------------------------------------------------

// --- Wire types (re-exported from generated) ---

export type {
  // Enums
  AlwaysAllowBlock,
  DecisionKind,
  EntryKind,
  ErrorCode,
  ForgeKind,
  PlanStatus,
  ReadState,
  RunNodeStatus,
  RunStatus,
  RunStepTranscriptState,
  SafetyStatus,
  SettledBy,
  SteerOrigin,
  SteerReason,
  SteerState,
  StopReason,
  TabKind,
  ToolKind,
  ToolStatus,
  Transport,
  TurnOpenSourceName,
  TurnOutcome,
  TurnRevertCause,
  // The entry log: one envelope type plus one payload type per kind
  Entry,
  OpenEntry,
  EntryCompaction,
  EntryCompactionFailed,
  EntryModeSwitched,
  EntryModelSwitched,
  EntryPlan,
  EntryPrompt,
  EntryReconciled,
  EntrySafetyBlocked,
  EntrySteer,
  EntrySteerAck,
  EntryText,
  EntryThinking,
  EntryToolCall,
  EntryToolResult,
  EntryTurnBind,
  EntryTurnClose,
  EntryTurnOpen,
  EntryTurnRevert,
  // Domain shapes
  AccountUsage,
  AccountUsageBreakdown,
  ApprovalFile,
  Attachment,
  ChatHeader,
  FileChange,
  CodeReference,
  RefusalInfo,
  ToolDisclosed,
  ToolDenial,
  ToolDenialRule,
  MeteringItem,
  PermissionOption,
  PlanEntry,
  PolicyRule,
  PolicyView,
  PolicyExplainResult,
  SecurityProfile,
  SessionEffortLevel,
  SessionMode,
  SessionModel,
  TabList,
  TabSubject,
  ToolCall,
  ToolDiff,
  ToolLocation,
  Usage,
  // GET /api/sessions: the two row kinds and the reply that carries their verdicts
  ResumableSession,
  WorkflowRun,
  SessionListResponse,
  // SSE payloads
  ChatDeletedPayload,
  CodeReferencesPayload,
  // The entry log's frames: one payload type per SSE event of the log
  TurnOpenedPayload,
  EntryOpenedPayload,
  EntryDeltaPayload,
  EntrySealedPayload,
  EntryAppendedPayload,
  TurnClosedPayload,
  ToolProgressPayload,
  ConnectedPayload,
  DecisionSettledPayload,
  DraftChangedPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  UserInputOption,
  UserInputSubOption,
  ElicitationPropertySchema,
  ElicitationRequestSchema,
  ErrorPayload,
  GovernanceFeatures,
  GovernanceStatePayload,
  MCPConnectedPayload,
  MCPDisconnectedPayload,
  MCPFailedPayload,
  MCPOAuthPayload,
  OpenExternalURLPayload,
  PermissionNeededPayload,
  PermissionsChangedPayload,
  PolicyErrorPayload,
  SafetyProperty,
  SafetyStatusPayload,
  SafetyPropertiesPayload,
  CatalogInfo,
  AptPackage,
  Inventory,
  Job,
  JobResponse,
  JobsResponse,
  RemoveResponse,
  SearchHit,
  SearchResponse,
  ToolInfo,
  ToolJobChangedPayload,
  ToolJobOutputPayload,
  RunStartedPayload,
  RunProgressPayload,
  RunFinishedPayload,
  RunInputNeededPayload,
  RunInputSettledPayload,
  RunAnswerRequest,
  Recipe,
  RecipesResponse,
  RunLaunchRequest,
  RunLaunchedResponse,
  // GET /api/runs/{id}/steps/{path...}: one step's transcript plus its verdict.
  RunStepTranscript,
  SystemTool,
  SteerQueuedPayload,
  AgentNoticePayload,
  TabsChangedPayload,
  SpecApprovedPayload,
  SpecChangedPayload,
  TextSpan,
  TerminalCreatedPayload,
  TerminalOutputPayload,
  TerminalExitedPayload,
  SubjectStamp,
  PendingSnapshotPayload,
  StatusSnapshotPayload,
  StatusRow,
} from "./wire/types.gen.js";

// PermissionNeeded is the legacy alias used at call sites that predate
// the generated naming. The generated type is PermissionNeededPayload.
export type { PermissionNeededPayload as PermissionNeeded } from "./wire/types.gen.js";

import type {
  Entry,
  EntryCompaction,
  EntryCompactionFailed,
  EntryKind,
  EntryModeSwitched,
  EntryModelSwitched,
  EntryPlan,
  EntryReconciled,
  EntrySafetyBlocked,
  EntrySteer,
  EntrySteerAck,
  EntryText,
  EntryThinking,
  EntryToolCall,
  EntryToolResult,
  EntryTurnBind,
  EntryTurnClose,
  EntryTurnOpen,
  EntryTurnRevert,
  OpenEntry,
  SessionEffortLevel,
  SteerOrigin,
  SubjectStamp,
  TurnOutcome,
  Usage,
} from "./wire/types.gen.js";

// --- The entry log, client side ---

/** Which payload type each entry kind carries.
 *
 *  The wire declares `Entry.payload` as `unknown`, because Go's own field is an
 *  `any` chosen by `Kind`. This map is the client's statement of that choice, and
 *  it is an interface keyed by `EntryKind` rather than a `Record<string, …>` so a
 *  kind added to the wire enum fails the type check here until it names a payload.
 *  `entries.ts` is the one place that narrows an `Entry` through it. */
export interface EntryPayload {
  turn_open: EntryTurnOpen;
  turn_bind: EntryTurnBind;
  text: EntryText;
  thinking: EntryThinking;
  tool_call: EntryToolCall;
  tool_result: EntryToolResult;
  steer: EntrySteer;
  steer_ack: EntrySteerAck;
  plan: EntryPlan;
  compaction: EntryCompaction;
  compaction_failed: EntryCompactionFailed;
  safety_blocked: EntrySafetyBlocked;
  model_switched: EntryModelSwitched;
  mode_switched: EntryModeSwitched;
  turn_revert: EntryTurnRevert;
  reconciled: EntryReconciled;
  turn_close: EntryTurnClose;
}

/** One sealed entry whose payload is narrowed to its kind's. */
export type KindedEntry<K extends EntryKind> = Omit<Entry, "kind" | "payload"> & {
  kind: K;
  payload: EntryPayload[K];
};

/** Every entry, as a discriminated union over `kind`, so a dispatcher's switch is
 *  TOTAL by type rather than by a default arm. */
export type AnyEntry = { [K in EntryKind]: KindedEntry<K> }[EntryKind];

/** One turn's client state: the sealed entries plus whatever is still coalescing.
 *
 *  `entries[0]` is always the `turn_open` and the invariant is
 *  `entries[i].seq === i` — any other `seq` is a HOLE, which is what triggers the
 *  one range read of section 6.4 rather than being folded in at the wrong index.
 *  `openEntries` is keyed by LANE (`""` is the agent that owns the log's turn, a
 *  uuid is a delegate's), because a lane coalesces one entry at a time. */
export interface TurnState {
  entries: Entry[];
  openEntries: Map<string, OpenEntry>;
  /** Where the `turn_close` was appended, cached so `closeOf` is not a scan.
   *  Absent while the turn is open, which is what `turnLive` reads. */
  closeAt?: number;
}

// --- Client-only types ---

/** Severity level for banner notifications. Shared by handlers/turn.ts
 *  and banner-stack.ts to avoid duplicate declarations. */
export type BannerLevel = "error" | "warning" | "info";

/** SSE event envelope sent by the server. The payload is decoded by
 *  registered SSE decoders before reaching handlers; see ./bus.ts. `subject` is
 *  the digest stamp of the projection this frame completes, observed by the
 *  transport AFTER the handlers applied it. */
import type { SSEPayloads } from "./bus.js";
export interface ServerEvent {
  type: keyof SSEPayloads;
  chat_id?: string;
  payload?: unknown;
  subject?: SubjectStamp;
}

/** Prelaunch model-picker entry, mapped from GET /api/config-template's
 *  SessionModel list (kiro-cli 2.14 _kiro/config/template). Distinct from
 *  the per-session SessionModel that arrives over the bridge — this shape
 *  exists for the picker cache before any session spawns. */
export interface ModelInfo {
  model_name: string;
  model_id: string;
  /** Credit cost relative to the cheapest model (`_meta.kiro.rateMultiplier`).
   *  OPTIONAL because the wire field is `omitempty`: absent means the catalog
   *  carried no rate, which renders as no readout rather than as `1x`. Every
   *  consumer goes through `rateLabel` — see its own note for the defect. */
  rate_multiplier?: number;
  description?: string;
  /** Whether this model has reasoning-effort levels at all (KAS
   *  `_meta.kiro.hasEffort`). Absent on every model = the catalog does not carry
   *  the capability, which the effort control reads as "show it anyway". */
  has_effort?: boolean;
  /** This model's own default tier (`_meta.kiro.defaultEffortLevel`), which
   *  kiro-cli's own picker labels `[default]`. The pre-session fallback for the
   *  live-level highlight: before a session exists there is no currentValue to
   *  read, and a chat with no pick of its own would otherwise render with nothing
   *  selected. The tier LIST is not a per-model field — see Session.effort_levels. */
  default_effort_level?: string;
}

/** Connection status flag, surfaced through the status bar. */
export type ConnectionStatus = "connecting" | "connected" | "disconnected";

/** One mid-turn steer the agent has NOT read yet: a row in the bottom dock.
 *
 *  THE CLIENT WRITES INTENT, THE SERVER WRITES FACT. That is the whole rule,
 *  and it replaced an earlier one ("every field here is written by an SSE
 *  event ... rather than by the code that sent it") that this shape no longer
 *  obeys. Sending a steer writes a `pending` entry immediately — the user
 *  pressed Send and is owed a row for it — and `chat.steer`'s `rollback`
 *  un-draws that entry if the POST fails, in the same gesture that puts the
 *  text back in the composer. Everything else is still the server's: the
 *  confirmed id (`steer_queued`), and the `steer` ENTRY that arrives as
 *  `entry_appended` carrying `state: read` or `state: dropped`.
 *
 *  A `pending` entry renders as "Sending" and NOTHING else — no Edit, no
 *  Discard, because there is no server-side id to clear yet. Reconciliation is
 *  by id (the client derives `steer-<messageID>`, which is what KAS returns),
 *  and if that convention ever drifts, by exact text against the OLDEST
 *  pending entry. Both branches adopt onto the existing entry rather than
 *  appending, so the optimistic entry and the confirmed one can never both
 *  render as two rows.
 *
 *  Always the USER's own message. An agent's progress notice travels the same
 *  KAS buffer but arrives as `agent_notice`, so nothing here needs a field for
 *  whose words it holds.
 *
 *  A steer LEAVES this array on a `steer` entry that settles it (its own id, its
 *  `kas` batch id, or a `resends` naming it), whatever the state, or on a
 *  `steer_queued` frame with state `removed`. A row the server could not deliver
 *  stays, marked `unsent`, until the next prompt carries it. The transcript's
 *  record IS that entry, at its own `seq`; nothing here outlives it. So the array
 *  is strictly "waiting", which is why there is no `injected` flag.
 *
 *  `compacted` is the one field the SERVER never writes: a compaction landing
 *  while a row is still waiting is a client-side arrival-order fact, marked on
 *  every row then in the dock.
 *
 *  A steer lands in the turn already running, so a row here is never a message
 *  held back for a later one. */
export interface PendingSteer {
  /** The row's key, and the id every lifecycle event names it by. For a user row it is
   *  `steer-<messageID>`, derived from this device's POST, and it never changes: a
   *  resubmit moves `kas` instead, so the row keeps its element. */
  id: string;
  text: string;
  /** Whose words these are, resolved SERVER-side (`marotte.SteerOrigin`).
   *
   *  Required rather than optional: the fallback a reader would reach for is "the
   *  user's", which is the wrong answer for the case this names. The one local
   *  writer is `recordSteerSent`, where it is a fact about this device's POST. */
  origin: SteerOrigin;
  /** Written on SUBMIT and cleared by `steer_queued`: this row is the local
   *  claim that a POST is in flight, and its id is derived rather than
   *  confirmed. Absent on every entry the server has acknowledged. */
  pending?: boolean;
  /** A compaction landed while this row was still WAITING, so the agent will read
   *  it against a summarized context rather than the conversation the reader wrote
   *  it against. CLIENT-ONLY and set on ARRIVAL ORDER rather than on any timestamp
   *  (the `entries.ts` handler marks every row then in the dock when a `compaction`
   *  entry lands), which is the order the server broadcast in. The one moment the
   *  fact is actionable is while the steer is unread, because the reader can still
   *  take it back and rephrase it.
   *
   *  A row that arrives AFTER the compaction is never marked — the compaction is in
   *  its past on both sides. Lost on a reconnect, where the dock is rebuilt from
   *  `waiting` and holds no order against the log; the alternative is comparing two
   *  wall clocks, which this design refuses everywhere else. */
  compacted?: true;
  /** The id KAS holds this row under when it is not `id`: a combined resubmit sends the
   *  kept rows as one steer, and that steer's `steer` entry is what retires them. */
  kas?: string;
  /** The server holds the row and could not deliver it to any turn (`steer_queued`
   *  state `unsent`); it goes with the chat's next prompt unless deleted first. */
  unsent?: true;
}

// --- Local session state (client-only projection of server chat) ---

/** How much of a chat's transcript is resident client-side. Absent means the window
 *  was never loaded, which every consumer treats exactly like not-`loaded`.
 *  - `loaded`: a successful newest-page fetch put the window here; the ONLY writer.
 *  - `evicted`: the idle sweep dropped the window; the row survives, activation refetches.
 *  - `partial`: SSE ingest landed on an evicted chat; some messages resident, the window not.
 *  - `load_failed`: the newest-page GET answered nothing (non-2xx, network, decode); the
 *    window is whatever the last load or live ingest left, activation refetches, and only
 *    a successful newest-page load reclaims `loaded`. */
export type MessagesResidency = "loaded" | "evicted" | "partial" | "load_failed";

export interface Session {
  id: string;
  name: string;
  model: string;
  acp_session_id: string;
  current_mode_id: string;
  // There is no available_modes / available_models. They are a WORKSPACE
  // vocabulary, not a per-session one: 59 modes repeated across 29 chats,
  // identical in all of them, and 98.6% of a 1.25 MiB /api/chats response the
  // boot fetched twice. roles.ts holds the one copy, fed by
  // /api/config-template. `current_mode_id` above stays because it is this
  // chat's CHOICE from that vocabulary.
  /** The reasoning-effort tiers this session offers (the `effortLevel` config
   *  option's own choices). Empty means the current model has no tiers, which is
   *  what kiro-cli's TUI treats as "effort is not available on this model". */
  effort_levels?: SessionEffortLevel[];
  /** The tier the session is RUNNING at (that option's currentValue). Distinct
   *  from `effort`, which is what this chat CHOSE: a chat that never picked has
   *  an empty `effort` and still runs at a level. */
  effort_active?: string;
  usage: Usage;
  /** This chat's resident slice of the entry log, one `TurnState` per turn, keyed
   *  by turn id. The transcript's rendering unit is the turn, and each entry names
   *  its own turn, so grouping is a partition rather than an inference. */
  turns: Map<string, TurnState>;
  /** The resident turn ids in FILE order — the order the appender wrote them, which
   *  is the order the transcript renders. A separate array rather than the Map's own
   *  insertion order because an older page is PREPENDED, and a Map cannot. */
  turn_order: string[];
  /** How many turns the whole session holds, from `ChatHeader`. `has_more` is about
   *  this window's left edge; this is about the session. */
  turn_count: number;
  /** The turns a `turn_revert` took out of this window, client-only. It exists so the
   *  range read has a second refusal beside the abort: an answer already decoded when
   *  the frame landed would otherwise reach `applyTurnRange`, which seats a turn the
   *  store does not hold — putting back exactly what the drop removed. It cannot be
   *  spelled as "absent from `turn_order`", because that is also every turn whose own
   *  `turn_opened` was lost, and re-reading THOSE is what the repair is for. */
  reverted?: Set<string>;
  has_more: boolean;
  thinking: boolean;
  /** The SERVER'S LAST STATEMENT about whether this chat has a turn of its OWN
   *  open, from the single-chat GET's `live` field — the turn registry's own
   *  answer rather than anything in the log, which is why it is named apart from
   *  the `turn_open` entry kind. NOT live state and deliberately not a second
   *  `thinking`: `thinking` is this client's own memory of a stream it has seen,
   *  and it starts false, so on a mid-turn reload the newest turn's absent
   *  carrier would read as a terminal verdict over a running turn. OPTIONAL
   *  because an ANSWER that states nothing must not collapse to false: the field
   *  is forgotten rather than written when the GET omits it, so `turnLive` falls
   *  back to the log instead of answering settled off a silence. The table of
   *  writers is on `turnLive` in store.ts, which is the ONE reader. */
  turn_open?: boolean;
  working_label: string;
  /** Agent-declared activity status from the KAS focus_update channel
   *  (chat_status SSE): "in_progress" | "waiting_on_user" | "completed" |
   *  "idle". Client-only and ephemeral — cleared on the next prompt send
   *  and on a transport gap, never persisted. */
  agent_status?: string;
  /** How this chat's newest FINISHED turn ended, verbatim from
   *  `ChatHeader.last_turn_outcome`. NOT a latch and not client memory: it is the
   *  server's own statement, so a header read REPLACES it — an absent outcome is a
   *  CLEAR, which is the opposite of how `model` and `effort_levels` carry forward.
   *
   *  Read by `tabStatusFor` (the dot's `failed` and `done` arms both grade it) and by the
   *  boot snapshot's provisional row. */
  last_turn_outcome?: TurnOutcome;
  /** `ChatHeader.updated_at`, epoch millis, bumped by every `Mutate`. So it is LAST
   *  ACTIVITY rather than "finished at" — good enough for "finished ~2h ago", which
   *  is what the tab dot's tooltip renders, and not for a clock. Replaced wholesale
   *  by every header read, like `last_turn_outcome`. */
  updated_at?: number;
  /** Mid-turn steers the agent has NOT read yet: the bottom dock's rows.
   *  Written on submit (intent) and by `steer_queued` (fact).
   *
   *  A row LEAVES on a `steer` ENTRY that settles it, whatever that entry's state, or
   *  on a `removed` frame; a row the server could not deliver stays, marked `unsent`.
   *  There is no second field for a read steer: the entry IS the transcript fact,
   *  positioned by seal order in the turn body, so nothing anchors a note against a
   *  block index. */
  steers?: PendingSteer[];
  /** The model the reader picked for the NEXT turn, from `ChatHeader.pending_model`.
   *  The badge's ONE input — the `.pending` class and the "after current turn"
   *  tooltip render while it is non-empty and clear when a header arrives with it
   *  empty, on every device. The switcher keeps no local queue and has no drain. */
  pending_model?: string;
  supervised_mode?: boolean;
  /** Reasoning-effort level ("low".."max", "" = the engine default). The
   *  fourth per-chat composer setting, beside model, mode and supervised; it
   *  used to be one global `model_effort` setting keyed by the last model, so
   *  two chats could not disagree. */
  effort?: string;
  /** The SERVER's copy of the composer draft, from the single-chat GET. A SEED
   *  only: composer-state.ts holds the live working copy, adopts this once per
   *  chat and thereafter ignores it, because a fetch can land after the user has
   *  started typing the next message. It rides that GET rather than the header so
   *  it stays off the list endpoint and off every chat_updated frame; a chat with
   *  a record but no messages therefore needs its own fetch to get one, which is
   *  what activateChatView's empty branch does (`set_mode` and `set_effort` both
   *  auto-create the record before the first prompt, so a persisted empty chat
   *  can genuinely hold a draft). */
  draft?: string;
  /** The SERVER's copy of the paths staged beside that draft, from the same GET
   *  and for the same reasons. A SEED only, and off the header for the reason the
   *  draft is: both save on one 600ms debounce, so a header field would put them
   *  in a chat_updated frame every keystroke's worth of typing. */
  attachments?: string[];
  compaction_watermark?: string;
  /** Client-only transcript residency; see MessagesResidency. Carried across
   *  the header-list rebuild like every other client-only projection. */
  residency?: MessagesResidency;
  /** This row is the boot snapshot's paint-time hint, not the server's answer:
   *  `boot-snapshot.ts` sets it and nothing else ever does.
   *
   *  It exists because `loadList` PRESERVES rows the server did not name — the
   *  rule that keeps a chat SSE created while the request was in flight — and a
   *  hinted row for a chat deleted since the capture satisfies that rule
   *  identically. The mark is what tells the two apart, so the hint does not
   *  outlive the answer that omitted it. */
  provisional?: true;
}

// GET /api/sessions' two row types are GENERATED (`ResumableSession`,
// `WorkflowRun` in wire/types.gen.ts). They were hand-mirrored here, which is a
// second declaration of one wire shape with nothing holding the two together —
// and the response's own type now carries the per-list read verdicts the picker
// reads, which a mirror of the rows alone could not.
