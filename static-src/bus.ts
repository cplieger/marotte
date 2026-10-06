// Event bus. `onSSE`/`dispatch` route SSE events with payload types from SSEPayloads;
// `onBus`/`emitBus` carry cross-module notifications typed by BusPayloads.

import { createBus } from "@cplieger/reactive";

import type {
  ServerEvent,
  ChatHeader,
  SteerQueuedPayload,
  AgentNoticePayload,
  KnowledgeIndexingPayload,
  SystemNoticePayload,
  PendingSnapshotPayload,
  StatusSnapshotPayload,
  TabsChangedPayload,
  SpecApprovedPayload,
  SpecChangedPayload,
  InventoryChangedPayload,
  PermissionNeeded,
  ErrorPayload,
  ConnectedPayload,
  MCPConnectedPayload,
  MCPOAuthPayload,
  MCPFailedPayload,
  MCPDisconnectedPayload,
  ElicitationNeededPayload,
  UserInputNeededPayload,
  DecisionSettledPayload,
  DraftChangedPayload,
  OpenExternalURLPayload,
  TurnOpenedPayload,
  EntryOpenedPayload,
  EntryDeltaPayload,
  EntrySealedPayload,
  EntryAppendedPayload,
  TurnClosedPayload,
  ToolProgressPayload,
  CodeReferencesPayload,
  PermissionsChangedPayload,
  PolicyErrorPayload,
  SafetyStatusPayload,
  SafetyPropertiesPayload,
  GovernanceStatePayload,
  ToolJobChangedPayload,
  ToolJobOutputPayload,
  TerminalCreatedPayload,
  TerminalOutputPayload,
  TerminalExitedPayload,
  RunStartedPayload,
  RunProgressPayload,
  RunFinishedPayload,
  RunInputNeededPayload,
  RunInputSettledPayload,
} from "./types.js";

// --- Typed SSE surface ---

/** Payload shape per SSE event type. Events with no payload use `undefined`;
 *  events with a well-known shape get their own entry. Events not listed
 *  here fall through to `unknown` and can still be subscribed via `on`. */
export interface SSEPayloads {
  readonly connected: ConnectedPayload;
  readonly chat_created: ChatHeader;
  readonly chat_updated: ChatHeader;
  readonly chat_deleted: { readonly id: string };
  readonly chat_status: { readonly status?: string; readonly description?: string };
  // THE ENTRY LOG: each event appends to the log the window GET reads, deltas an open entry,
  // or sets a turn-level value the `turn_close` carries; nothing moves or rewrites an entry,
  // so live and reload agree. `turn_opened` precedes its turn's first `entry_opened`.
  readonly turn_opened: TurnOpenedPayload;
  readonly entry_opened: EntryOpenedPayload;
  readonly entry_delta: EntryDeltaPayload;
  readonly entry_sealed: EntrySealedPayload;
  readonly entry_appended: EntryAppendedPayload;
  readonly turn_closed: TurnClosedPayload;
  readonly tool_progress: ToolProgressPayload;
  readonly code_references: CodeReferencesPayload;
  // One event: the steer reached KAS's buffer. Read or dropped is the `steer` entry's `state`.
  readonly steer_queued: SteerQueuedPayload;
  // A workflow step or subagent reporting into the launching session, as its own event.
  readonly agent_notice: AgentNoticePayload;
  // KAS's `_kiro/system/notify`: a toast at its own level, keyed by the bridge that
  // sent it (a chat, `run:<id>`, or "" for the utility session).
  readonly system_notice: SystemNoticePayload;
  // The two connect-hook aggregates: the WHOLE pending set and the WHOLE waiting-status set,
  // possibly empty. Empty matters: a per-item replay of nothing leaves a resolved row up.
  readonly pending_snapshot: PendingSnapshotPayload;
  readonly status_snapshot: StatusSnapshotPayload;
  // An unpublishable (over-cap) frame names its subject instead; the transport refetches it.
  readonly subject_changed: undefined;
  // ONE aggregate frame per COMMITTED mutation of the open-tab set, so one mutation is one
  // version. Removal is STATED in `removed_ids`, never inferred from absence. `version` is the
  // client's only watermark; see tabs.ts applyTabsChanged.
  readonly tabs_changed: TabsChangedPayload;
  // A spec directory's files changed on disk, one frame per coalescing
  // window; the spec tab whose ref matches refetches. Workspace-global.
  readonly spec_changed: SpecChangedPayload;
  readonly spec_approved: SpecApprovedPayload;
  readonly permission_needed: PermissionNeeded;
  readonly permissions_changed: PermissionsChangedPayload;
  readonly policy_error: PolicyErrorPayload;
  readonly elicitation_needed: ElicitationNeededPayload;
  readonly user_input_needed: UserInputNeededPayload;
  // Retires any of the three asks on every surface that did not answer: first answer wins.
  readonly decision_settled: DecisionSettledPayload;
  readonly draft_changed: DraftChangedPayload;
  readonly error: ErrorPayload;
  readonly settings_updated: undefined;
  readonly mcp_config_changed: undefined;
  readonly mcp_pool_changed: undefined;
  readonly mcp_connected: MCPConnectedPayload;
  readonly mcp_oauth_needed: MCPOAuthPayload;
  readonly mcp_failed: MCPFailedPayload;
  readonly mcp_disconnected: MCPDisconnectedPayload;
  readonly mcp_prewarm: { readonly package: string; readonly state: string };
  readonly mode_changed: { readonly mode_id: string };
  readonly safety_status: SafetyStatusPayload;
  readonly safety_properties: SafetyPropertiesPayload;
  readonly knowledge_indexing: KnowledgeIndexingPayload;
  readonly governance_state: GovernanceStatePayload;
  readonly open_external_url: OpenExternalURLPayload;
  readonly compaction_started: undefined;
  readonly working_label: { readonly label: string };
  // The generated wire types, so no payload shape is declared twice.
  readonly terminal_created: TerminalCreatedPayload;
  readonly terminal_output: TerminalOutputPayload;
  readonly terminal_exited: TerminalExitedPayload;
  readonly forges_changed: undefined;
  readonly forge_inventory: InventoryChangedPayload;
  readonly hooks_changed: undefined;
  readonly recipes_changed: undefined;
  readonly powers_changed: undefined;
  readonly slash_commands_changed: undefined;
  readonly steering_issues_changed: undefined;
  readonly tool_job_changed: ToolJobChangedPayload;
  readonly tool_job_output: ToolJobOutputPayload;
  readonly run_started: RunStartedPayload;
  readonly run_progress: RunProgressPayload;
  readonly run_finished: RunFinishedPayload;
  // A workflow STEP asking a question, carrying its payload: KAS parks the run with a fixed
  // pauseReason and empty pauseDetail, so `inspect` never says what was asked. Keyed to the
  // launching chat, or `run:<workflowId>` when parentless.
  readonly run_input_needed: RunInputNeededPayload;
  // Its twin, separate from decision_settled: a run ask is keyed by string, not int64.
  readonly run_input_settled: RunInputSettledPayload;
}

export type SSEHandler<K extends keyof SSEPayloads> = SSEPayloads[K] extends undefined
  ? (chatID: string) => void
  : (chatID: string, payload: SSEPayloads[K]) => void;

type AnyHandler = (...args: unknown[]) => void;

/**
 * Snapshot-cached handler set, rebuilt only on mutation; a handler unsubscribed during
 * iteration still fires.
 */
interface HandlerSlot {
  set: Set<AnyHandler>;
  snapshot: AnyHandler[];
  dirty: boolean;
}

function getSlot(map: Map<string, HandlerSlot>, key: string): HandlerSlot {
  let slot = map.get(key);
  if (slot === undefined) {
    slot = { set: new Set(), snapshot: [], dirty: false };
    map.set(key, slot);
  }
  return slot;
}

function addHandler(map: Map<string, HandlerSlot>, key: string, fn: AnyHandler): () => void {
  const slot = getSlot(map, key);
  slot.set.add(fn);
  slot.dirty = true;
  return (): void => {
    slot.set.delete(fn);
    slot.dirty = true;
  };
}

function getSnapshot(slot: HandlerSlot): AnyHandler[] {
  if (slot.dirty) {
    slot.snapshot = Array.from(slot.set);
    slot.dirty = false;
  }
  return slot.snapshot;
}

const sseHandlers = new Map<string, HandlerSlot>();

/** Subscribe to an SSE event with a typed payload. Returns an unsubscribe
 *  function. */
export function onSSE<K extends keyof SSEPayloads>(type: K, fn: SSEHandler<K>): () => void {
  return addHandler(sseHandlers, type, fn as AnyHandler);
}

/** Route an incoming SSE event to all onSSE handlers registered for its
 *  type. Called by transport.ts when an event arrives. */
export function dispatch(evt: ServerEvent): void {
  const slot = sseHandlers.get(evt.type);
  if (slot === undefined) {
    return;
  }
  const fns = getSnapshot(slot);
  const chatID = evt.chat_id ?? "";
  for (const fn of fns) {
    try {
      fn(chatID, evt.payload);
    } catch (e) {
      console.error(`[bus] SSE handler error for "${evt.type}":`, e);
    }
  }
}

// --- Generic cross-module bus ---

// --- Typed bus event constants ---

export const BUS_TURN_IDLE = "turn:idle" as const;
/**
 * Every claim this client holds is void and the projection is re-read: a new hub epoch or a
 * `must_refetch` digest. The payload carries the revalidation's signal for every fetch.
 */
export const BUS_RECONCILE = "transport:reconcile" as const;
/**
 * The page resumed from a suspension. Unlike a RECONCILE, only views with no digest subject
 * are undermined, so it nudges the active view.
 */
export const BUS_PAGE_RESUMED = "transport:resumed" as const;
export const BUS_KEYS_ESCAPE = "keys:escape" as const;
export const BUS_ACTIVATE_CHAT = "chat:activate" as const;
/**
 * A workflow run appeared or ended, so run lists are stale. On the bus so the run handler
 * does not import the history page.
 */
export const BUS_RUNS_CHANGED = "runs:changed" as const;
/**
 * The active tab changed, so affordances scoped to the old tab are scoped to nothing. On the
 * bus: the tab store must not know a search box, and invisibility is not teardown.
 */
export const BUS_TAB_CHANGED = "tabs:changed" as const;
/**
 * An editor buffer's first read settled. `FileState.loaded` is a plain field and its writes
 * flush one at a time, so the in-file find re-runs on this event. On the bus so the loader
 * knows no find bar.
 */
export const BUS_EDITOR_FILE_LOADED = "editor:loaded" as const;
/**
 * A command POST failed and the user must see it: the chat (empty for workspace-global) and
 * the server's prose. On the bus: `failure-notice.ts` reaches the tab store, which reaches
 * the transport. The send button stays the caller's retry surface.
 */
export const BUS_COMMAND_FAILED = "command:failed" as const;
/**
 * A structured question was ANSWERED: the chat and the dock's answer text. KAS's spec-mode
 * checkpoint hands some answers to the CLIENT to carry out, so the spec page acts on it. On
 * the bus: a direct call would close a ring, and no listener may exist.
 */
export const BUS_USER_INPUT_ANSWERED = "user-input:answered" as const;

/** Payload shape per bus event. Events with no payload use `undefined`. */
interface BusPayloads {
  readonly [BUS_TURN_IDLE]: string; // chatID
  readonly [BUS_RECONCILE]: { readonly cause: string; readonly signal: AbortSignal };
  readonly [BUS_PAGE_RESUMED]: undefined;
  readonly [BUS_KEYS_ESCAPE]: undefined;
  readonly [BUS_ACTIVATE_CHAT]: { chatID: string; then?: () => void };
  readonly [BUS_RUNS_CHANGED]: undefined;
  readonly [BUS_TAB_CHANGED]: { to: string; kind: string | null };
  readonly [BUS_EDITOR_FILE_LOADED]: { path: string };
  readonly [BUS_COMMAND_FAILED]: {
    readonly chatID: string;
    readonly chatName: string;
    readonly message: string;
  };
  readonly [BUS_USER_INPUT_ANSWERED]: { readonly chatID: string; readonly answer: string };
}

// The generic bus is @cplieger/reactive's createBus; the SSE surface stays bespoke
// (chatID-prepended handlers, a decoder registry).
const bus = createBus<BusPayloads>();

/** Subscribe to a typed bus event. Returns an unsubscribe function. */
export const onBus = bus.on;

/** Emit a typed bus event. */
export const emitBus = bus.emit;

// SSE payload decoder registry: transport.ts runs a registered decoder on each event's
// payload, dropping the event when it throws. Unregistered events pass untyped.

import { type Decoder, asObject, reqStr } from "./validators.js";
import { decodeSubjectStamp } from "./wire/decoders.gen.js";

const sseDecoders = new Map<keyof SSEPayloads, Decoder<unknown>>();

/** Register a runtime decoder for the given SSE event type. The decoder
 *  is invoked on the parsed `payload` field before handlers fire.
 *  Calling twice for the same type replaces the prior registration. */
export function registerSSEDecoder<K extends keyof SSEPayloads>(
  type: K,
  decoder: Decoder<SSEPayloads[K]>,
): void {
  sseDecoders.set(type, decoder);
}

/** Returns the registered decoder for `type`, or undefined if none. */
export function lookupSSEDecoder(type: string): Decoder<unknown> | undefined {
  return sseDecoders.get(type as keyof SSEPayloads);
}

/**
 * Decode one parsed envelope into a `ServerEvent`, or throw (dropping the event). ONE door
 * for live frames and the connect hook's `pending_snapshot` items.
 */
export function decodeEnvelope(raw: unknown): ServerEvent {
  const o = asObject(raw, "$.event");
  const type = reqStr(o, "type", "$.event");
  // The wire's type string is trusted as a member: an unknown type reaches no handler.
  const evt: ServerEvent = { type: type as keyof SSEPayloads };
  const chatID = o["chat_id"];
  if (typeof chatID === "string") {
    evt.chat_id = chatID;
  }
  const decoder = lookupSSEDecoder(type);
  if (o["payload"] !== undefined) {
    evt.payload = decoder === undefined ? o["payload"] : decoder(o["payload"]);
  }
  if (o["subject"] !== undefined && o["subject"] !== null) {
    evt.subject = decodeSubjectStamp(o["subject"]);
  }
  return evt;
}
