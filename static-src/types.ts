// Client-side type definitions. Wire interfaces and enums are generated from the Go api/* structs
// into ./wire/types.gen.ts and re-exported below; edit the Go side and run
// `go run ./cmd/wire-codegen`. Client-only types are declared here.

// --- Wire types (re-exported from generated) ---

export type {
  // Enums
  AlwaysAllowBlock,
  DecisionKind,
  EntryKind,
  ErrorCode,
  ForgeKind,
  InterruptMode,
  ModelSwitchReason,
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
  ToolOffload,
  ToolInteraction,
  TurnThroughput,
  MeteringItem,
  PermissionOption,
  PlanEntry,
  PolicyRule,
  PolicyView,
  PolicyExplainResult,
  QueuedPrompt,
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
  GovernanceLock,
  GovernanceMCPRegistry,
  GovernanceRegistryServer,
  GovernanceStatePayload,
  KnowledgeIndexingPayload,
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
  SystemNoticePayload,
  TabsChangedPayload,
  SpecApprovedPayload,
  SpecChangedPayload,
  InventoryChangedPayload,
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
  InterruptMode,
  OpenEntry,
  QueuedPrompt,
  SessionEffortLevel,
  SteerOrigin,
  SubjectStamp,
  TurnOutcome,
  Usage,
} from "./wire/types.gen.js";

// --- The entry log, client side ---

/** Which payload type each entry kind carries: the wire's `Entry.payload` is `unknown` (Go's `any`
 *  chosen by `Kind`). Keyed by `EntryKind`, so a new wire kind fails here until it names a payload.
 *  `entries.ts` is the one place that narrows through it. */
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

/** One turn's client state: the sealed entries plus whatever is still coalescing. `entries[0]` is
 *  the `turn_open` and `entries[i].seq === i`; any other `seq` is a HOLE, which triggers one range
 *  read. `openEntries` is keyed by LANE (`""` the owning agent, a uuid a delegate's). */
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
  /** Credit cost relative to the cheapest model (`_meta.kiro.rateMultiplier`). OPTIONAL (`omitempty`):
   *  absent renders no readout, not `1x`. Every consumer goes through `rateLabel`. */
  rate_multiplier?: number;
  description?: string;
  /** Whether this model has reasoning-effort levels at all (KAS
   *  `_meta.kiro.hasEffort`). Absent on every model = the catalog does not carry
   *  the capability, which the effort control reads as "show it anyway". */
  has_effort?: boolean;
  /** This model's default tier (`_meta.kiro.defaultEffortLevel`): the pre-session fallback for the
   *  live-level highlight. The tier LIST is Session.effort_levels. */
  default_effort_level?: string;
  /** Whether thinking can be turned off on this model
   *  (`_meta.kiro.thinkingToggleable`); the effort slider then offers an Off stop. */
  thinking_toggleable?: boolean;
  /** Whether this model runs with thinking off when the chat has not chosen
   *  (`_meta.kiro.defaultThinkingEnabled: false`). */
  thinking_default_off?: boolean;
}

/** Connection status flag, surfaced through the status bar. */
export type ConnectionStatus = "connecting" | "connected" | "disconnected";

/** One mid-turn steer the agent has NOT read yet: a row in the bottom dock.
 *
 *  THE CLIENT WRITES INTENT, THE SERVER WRITES FACT. Sending writes a `pending` entry at once
 *  (rendered "Sending", no controls) that `chat.steer`'s rollback un-draws on a failed POST; the
 *  server writes the id (`steer_queued`) and the settling `steer` entry. Reconciled by id
 *  (`steer-<messageID>`), else by exact text against the OLDEST pending entry, always onto the
 *  existing row. A row leaves on its settling `steer` entry or a `removed` frame; an undeliverable
 *  one stays `unsent`. Always the USER's words (agent notices arrive as `agent_notice`). */
export interface PendingSteer {
  /** The row's key, and the id every lifecycle event names it by. For a user row it is
   *  `steer-<messageID>`, derived from this device's POST, and it never changes: a
   *  resubmit moves `kas` instead, so the row keeps its element. */
  id: string;
  text: string;
  /** Whose words these are, resolved SERVER-side (`marotte.SteerOrigin`). Required: the obvious
   *  fallback, "the user's", is wrong for what this names. `recordSteerSent` is the one local writer. */
  origin: SteerOrigin;
  /** Written on SUBMIT and cleared by `steer_queued`: this row is the local
   *  claim that a POST is in flight, and its id is derived rather than
   *  confirmed. Absent on every entry the server has acknowledged. */
  pending?: boolean;
  /** A compaction landed while this row was WAITING, so the agent reads it against a summarized
   *  context. CLIENT-ONLY, set on ARRIVAL ORDER (no timestamps compared), and actionable only while
   *  the steer is unread. Lost on a reconnect, which rebuilds the dock without log order. */
  compacted?: true;
  /** The id KAS holds this row under when it is not `id`: a combined resubmit sends the
   *  kept rows as one steer, and that steer's `steer` entry is what retires them. */
  kas?: string;
  /** The server holds the row and could not deliver it to any turn (`steer_queued`
   *  state `unsent`); it goes with the chat's next prompt unless deleted first. */
  unsent?: true;
}

// --- Local session state (client-only projection of server chat) ---

/** How much of a chat's transcript is resident. Absent means never loaded, treated as not-`loaded`.
 *  - `loaded`: a successful newest-page fetch; the ONLY writer.
 *  - `evicted`: the idle sweep dropped the window; activation refetches.
 *  - `partial`: SSE ingest landed on an evicted chat.
 *  - `load_failed`: the newest-page GET failed; activation refetches. */
export type MessagesResidency = "loaded" | "evicted" | "partial" | "load_failed";

export interface Session {
  id: string;
  name: string;
  model: string;
  acp_session_id: string;
  current_mode_id: string;
  // No available_modes / available_models: they are a WORKSPACE vocabulary (one copy in roles.ts,
  // from /api/config-template). `current_mode_id` is this chat's CHOICE from it.
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
  /** The turns a `turn_revert` took out of this window, client-only: a range-read answer decoded
   *  before the frame would otherwise re-seat them. Not "absent from `turn_order`", which also means
   *  a lost `turn_opened` that the repair must re-read. */
  reverted?: Set<string>;
  has_more: boolean;
  thinking: boolean;
  /** The SERVER'S LAST STATEMENT about whether this chat has its OWN turn open (the single-chat GET's
   *  `live`, from the turn registry). Not a second `thinking`, which is client memory starting false.
   *  OPTIONAL: forgotten, not written false, when the GET omits it, so `turnLive` (store.ts, the ONE
   *  reader, with the writer table) falls back to the log. */
  turn_open?: boolean;
  working_label: string;
  /** Agent-declared activity status from the KAS focus_update channel
   *  (chat_status SSE): "in_progress" | "waiting_on_user" | "completed" |
   *  "idle". Client-only and ephemeral — cleared on the next prompt send
   *  and on a transport gap, never persisted. */
  agent_status?: string;
  /** How the newest FINISHED turn ended, verbatim from `ChatHeader.last_turn_outcome`. The server's
   *  statement, so a header read REPLACES it (absent CLEARS). Read by `tabStatusFor` and the boot
   *  snapshot's provisional row. */
  last_turn_outcome?: TurnOutcome;
  /** `ChatHeader.updated_at`, epoch millis, bumped by every `Mutate`: LAST ACTIVITY, good enough for
   *  the dot tooltip's "finished ~2h ago". Replaced by every header read. */
  updated_at?: number;
  /** Mid-turn steers the agent has NOT read yet: the dock's rows, written on submit (intent) and by
   *  `steer_queued` (fact). A read steer is its transcript entry; there is no second field. */
  steers?: PendingSteer[];
  /** The model picked for the NEXT turn (`ChatHeader.pending_model`): the badge's ONE input, on every
   *  device. No local queue. */
  pending_model?: string;
  supervised_mode?: boolean;
  /** What Send means while a turn runs: a steer into it, or a follow-up held for its
   *  end. From `ChatHeader.interrupt_mode`; absent is steer. */
  interrupt_mode?: InterruptMode;
  /** The server-held follow-ups (`ChatHeader.queued_prompts`), drained one per clean
   *  turn end. A header read replaces it, so an absent list is a clear. */
  queued?: QueuedPrompt[];
  /** Reasoning-effort level ("low".."max", "" = engine default): a per-chat composer setting. */
  effort?: string;
  /** The chat's thinking choice (`ChatHeader.thinking`: "on", "off", "" = the model's
   *  default) and the value the session last reported ("" = the model offers no
   *  thinking option). Named apart from `thinking`, which is the turn-live latch. The
   *  effort slider's Off stop reads both through `effort.ts` `thinkingIsOff`. */
  thinking_choice?: string;
  thinking_active?: string;
  /** The SERVER's copy of the composer draft, from the single-chat GET. A SEED only: composer-state.ts
   *  adopts it once per chat, since a fetch can land mid-typing. Off the header (no per-frame cost),
   *  so an empty chat with a record needs activateChatView's own fetch to get one. */
  draft?: string;
  /** The SERVER's copy of the staged paths, a SEED like `draft`; off the header because both save on
   *  one 600ms debounce. */
  attachments?: string[];
  compaction_watermark?: string;
  /** Client-only transcript residency; see MessagesResidency. Carried across
   *  the header-list rebuild like every other client-only projection. */
  residency?: MessagesResidency;
  /** This row is the boot snapshot's paint-time hint (`boot-snapshot.ts` the only writer). `loadList`
   *  PRESERVES rows the server did not name, so the mark keeps a hint for a deleted chat from
   *  outliving the answer that omitted it. */
  provisional?: true;
}

// GET /api/sessions' row types are GENERATED (`ResumableSession`, `WorkflowRun`, wire/types.gen.ts).
