// Transport, client→server half: the command POST and the ids it mints (the SSE half is
// `sse-adapter.ts`). The send button is the single error surface and the next Send is the retry. A
// plain 409 busy becomes a steer; a 409 with `reason: "starting"` is a real failure.

import type { TabKind } from "./types.js";
import { BUS_COMMAND_FAILED, emitBus } from "./bus.js";
import { get } from "./store.js";
import {
  registerCleanup,
  hasErrorString,
  IDEMPOTENCY_HEADER,
  IDEMPOTENCY_COMMAND_FIELD,
} from "./actions/index.js";

type CommandType =
  | "prompt"
  | "cancel"
  | "delete_chat"
  | "switch_model"
  | "set_supervised_mode"
  | "permission_response"
  | "elicitation_response"
  | "set_effort"
  | "set_thinking"
  | "set_draft"
  | "set_mode"
  | "rewind_chat";

interface Command {
  type: CommandType;
  chat_id?: string;
  payload?: Record<string, unknown>;
}

// --- Typed command discriminated union ---
// Provides compile-time payload validation for each command type.
// The wire format is unchanged (JSON.stringify produces the same output).

type TypedCommand =
  | {
      type: "prompt";
      chat_id: string;
      payload: {
        text: string;
        attachments?: readonly unknown[];
        message_id?: string;
        model?: string;
        display_text?: string | undefined;
      };
    }
  // `lead` is the send-now arrow's row, ordered first by the turn-end resend.
  | { type: "cancel"; chat_id: string; payload?: { lead: string } }
  | { type: "delete_chat"; chat_id: string }
  | { type: "merge_tangent"; chat_id: string; payload: { op_id: string } }
  | { type: "switch_model"; chat_id: string; payload: { model: string } }
  | { type: "set_supervised_mode"; chat_id: string; payload: { enabled: boolean } }
  | {
      type: "permission_response";
      chat_id: string;
      // file_decisions answers a TURN APPROVAL on the ordinary permission
      // reply: action id → keep. Omitting an id it offered is a rollback.
      payload: {
        request_id: number;
        option_id: string;
        file_decisions?: Record<string, boolean>;
        rejection_reason?: string;
        always_resource?: string;
      };
    }
  | {
      type: "elicitation_response";
      chat_id: string;
      payload: { request_id: number; action: string; content?: Record<string, unknown> };
    }
  | { type: "set_effort"; chat_id: string; payload: { level: string } }
  // The effort slider's Off stop; a tier pick turns thinking back on.
  | { type: "set_thinking"; chat_id: string; payload: { enabled: boolean } }
  // The composer text typed and not sent. An empty string is a real value: it
  // is how a sent or abandoned draft is cleared.
  | { type: "set_draft"; chat_id: string; payload: { text: string } }
  | { type: "set_mode"; chat_id: string; payload: { mode_id: string } }
  // A message delivered INTO the running turn (`_session/steer`). Typed here
  // for the steer action's custom runner — the wire shape every other sender
  // (the framework's loose TransportCommand) already produces.
  | {
      type: "steer";
      chat_id: string;
      payload: { text: string; message_id: string };
    }
  // A follow-up held for the end of the running turn (Queue mode). Attachments stay
  // paths: the server reads them when the row is sent.
  | {
      type: "queue_prompt";
      chat_id: string;
      payload: {
        text: string;
        message_id: string;
        display_text?: string;
        attachments?: readonly { path: string; name: string }[];
      };
    }
  | { type: "steer_remove"; chat_id: string; payload: { steer_id: string } }
  // Addresses a USER MESSAGE, not a turn ordinal: KAS's revertMultiple takes a
  // messageId and refuses a non-user one.
  | { type: "rewind_chat"; chat_id: string; payload: { message_id: string } }
  // The three CREATING commands: the only members with no `chat_id`, because the
  // server mints the id and returns it. `op_id` correlates every attempt of ONE
  // gesture and is NOT the idempotency token — that one is the header, per
  // dispatch, in a 5-minute cache.
  | { type: "create_chat"; payload: { op_id: string; name?: string; model?: string } }
  | { type: "resume_session"; payload: { op_id: string; session_id: string; name: string } }
  | { type: "fork_chat"; payload: { op_id: string; parent_chat_id: string; title?: string } }
  // The four TAB mutations, with no `chat_id` because the open-tab set is
  // workspace-global; a chat tab names its chat in `ref` instead. `op_id` is echoed back
  // on the frame so a client can tell its own committed mutation from another device's.
  | {
      type: "open_tab";
      payload: { kind: TabKind; op_id: string; ref?: string; parent?: string; owns?: boolean };
    }
  | { type: "close_tab"; payload: { id: string; op_id: string } }
  | { type: "reorder_tabs"; payload: { order: string[]; op_id: string } }
  | { type: "pin_tab"; payload: { id: string; pinned: boolean; op_id: string } }
  | { type: "reparent_tab"; payload: { id: string; parent: string; op_id: string } }
  // A human sign-off on one phase of one spec, with no `chat_id` for the tab
  // mutations' reason: a spec is workspace-global. `hash` is the compare-and-swap
  // precondition rather than a value the server trusts.
  | { type: "approve_spec_phase"; payload: { dir: string; phase: string; hash: string } };

const TRANSPORT_ERROR_CODES = {
  TIMEOUT: "timeout",
  CANCELLED: "cancelled",
  NETWORK: "network",
} as const;

export interface SendResult {
  ok: boolean;
  /** HTTP status. 0 for non-HTTP failures (timeout, network). */
  status: number;
  /** Server error message, if any. */
  error?: string;
  /** Machine-readable failure class from the error envelope's `reason` (internal/command writeErr),
   *  so callers branch on a VALUE. Today only "starting": a 409 whose admission holder cannot take a
   *  steer. */
  reason?: string;
  /** Structured error code for non-HTTP failures. */
  code?: string;
  /** The undecoded success body, when it parsed as JSON: the only place a creating command learns
   *  its server-minted chat id. The wire shape is the action's business. */
  body?: unknown;
}

interface SendOptions {
  /** Caller-supplied signal for cancellation (e.g. on tab close or chat delete). */
  signal?: AbortSignal;
  /** Timeout in ms. Defaults to 15 minutes. */
  timeoutMs?: number;
  /** When true (default), a failure gets a failure-notice.ts toast.
   *  `transportAction` passes false because it owns that surface itself, and both
   *  firing is duplicate feedback for one failure. */
  reportSendState?: boolean;
}

/** It becomes the HEADER, never a body field: the server's envelope has no such member. */
function idempotencyKeyOf(cmd: TypedCommand | Command): string | undefined {
  const v = (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/** The chat a command addresses, or "" (the creating commands): `BUS_COMMAND_FAILED`'s notice
 *  treats "" as workspace-wide. */
function chatIDOf(cmd: TypedCommand | Command): string {
  return "chat_id" in cmd ? (cmd.chat_id ?? "") : "";
}

/** Generate a client-side request id (also used as a message id). */
function newRequestID(): string {
  const arr = new Uint8Array(10);
  crypto.getRandomValues(arr);
  let out = "r-" + Date.now().toString(36) + "-";
  for (const b of arr) {
    out += b.toString(36);
  }
  return out;
}

/** A client-side user-message id: `newRequestID()`'s entropy with the `m-` prefix the server
 *  expects. Use it for every user message about to be sent. */
export function newMessageID(): string {
  return newRequestID().replace("r-", "m-");
}

/** A correlation id for ONE create gesture, surviving a retry minutes later. MINT IT AT THE
 *  DISPATCH SITE, never inside `run()`, which runs per attempt. `op-` keeps it in the identifier
 *  shape the command boundary gates. */
export function newOpID(): string {
  return newRequestID().replace("r-", "op-");
}

/** Default timeout for bridge command channel (long-running agent turns). */
const COMMAND_TIMEOUT_MS = 15 * 60 * 1000;

const inflight = new Set<AbortController>();

/** The global unload cleanup calls it so navigating away does not leak request handles. */
function cancelInflight(): void {
  for (const ctrl of inflight) {
    ctrl.abort();
  }
  inflight.clear();
}

registerCleanup(cancelInflight);

export async function send(cmd: TypedCommand | Command, opts?: SendOptions): Promise<SendResult> {
  // The framework threads the action's key through every retry, so a retry dedupes; a bare send()
  // gets a fresh one.
  const requestID = idempotencyKeyOf(cmd) ?? newRequestID();
  const chatID = chatIDOf(cmd);
  // Read before the round trip: the chat's row can be gone by the time it fails.
  const chatName = chatID === "" ? "" : (get(chatID)?.name ?? "");
  const timeoutMs = opts?.timeoutMs ?? COMMAND_TIMEOUT_MS;
  const ctrl = new AbortController();
  inflight.add(ctrl);

  const signals: AbortSignal[] = [ctrl.signal, AbortSignal.timeout(timeoutMs)];
  if (opts?.signal) {
    signals.push(opts.signal);
  }
  const combined = AbortSignal.any(signals);

  try {
    const r = await fetch("/api/command", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        // A HEADER and never a body field: as a header it reaches the server's
        // one idempotency middleware, which marks in-flight and answers 409.
        [IDEMPOTENCY_HEADER]: requestID,
      },
      signal: combined,
      body: JSON.stringify({
        type: cmd.type,
        chat_id: chatID,
        // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
        payload: "payload" in cmd && cmd.payload != null ? cmd.payload : {},
      }),
    });
    if (r.ok) {
      // A command whose body is not JSON is still a success — the status is what
      // says so, and only the creating commands read a body at all. Parsed here
      // rather than by each caller so there is one place the response is read.
      try {
        return { ok: true, status: r.status, body: await r.json() };
      } catch {
        return { ok: true, status: r.status };
      }
    }

    let errMsg = `HTTP ${String(r.status)}`;
    let reason: string | undefined;
    try {
      const d: unknown = await r.json();
      if (hasErrorString(d)) {
        errMsg = d.error;
      }
      // Lifted beside the error string: the envelope's additive `reason`
      // field is the machine-readable failure class (see SendResult.reason).
      const rawReason = (d as { reason?: unknown }).reason;
      if (typeof rawReason === "string" && rawReason !== "") {
        reason = rawReason;
      }
    } catch {
      /* non-JSON */
    }
    // No failure toast for ANY 409: a plain one is a queue signal the caller
    // converts to a steer, and a "starting" one IS a failure whose surface is
    // the send-error face rather than a toast.
    const reportSendState = opts?.reportSendState ?? true;
    if (r.status !== 409 && reportSendState) {
      emitBus(BUS_COMMAND_FAILED, { chatID, chatName, message: errMsg });
    }
    return {
      ok: false,
      status: r.status,
      error: errMsg,
      ...(reason !== undefined ? { reason } : {}),
    };
  } catch (e: unknown) {
    const err = e instanceof Error ? e : null;
    let msg: string;
    let code: string;
    if (err?.name === "TimeoutError") {
      msg = "Request timed out";
      code = TRANSPORT_ERROR_CODES.TIMEOUT;
    } else if (err?.name === "AbortError") {
      msg = "Request cancelled";
      code = TRANSPORT_ERROR_CODES.CANCELLED;
    } else {
      msg = err?.message ?? "Network error";
      code = TRANSPORT_ERROR_CODES.NETWORK;
    }
    const reportSendState = opts?.reportSendState ?? true;
    if (reportSendState) {
      emitBus(BUS_COMMAND_FAILED, { chatID, chatName, message: msg });
    }
    return { ok: false, status: 0, error: msg, code };
  } finally {
    inflight.delete(ctrl);
  }
}
