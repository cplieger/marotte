import {
  apiAction,
  defineAction,
  ActionError,
  retryNetwork,
  RETRY_STANDARD,
  transportAction,
  IDEMPOTENCY_COMMAND_FIELD,
  API_TIMEOUT_MS,
  classifyFetchError,
  withTimeout,
} from "./index.js";
import { exportFilename } from "../export-filename.js";
import { join as joinKey } from "@cplieger/keyenc";
import { errorAbout, successAbout } from "./subject.js";

import type { AttachedFile } from "../attachments.js";
import type {
  ChatHeader,
  InterruptMode,
  PendingSteer,
  Session,
  SessionListResponse,
  TabSubject,
} from "../types.js";
import {
  decodeChatHeader,
  decodeSessionListResponse,
  decodeTabSubject,
} from "../wire/decoders.gen.js";
import {
  get,
  setThinking,
  setSupervisedMode,
  setChatInterruptMode,
  removeChat,
  reinsertSession,
  indexOfSession,
  setModel,
  setCurrentMode,
  turnLive,
  recordSteerSent,
  recordSteerQueued,
  forgetSteer,
  steerIDFor,
  dropConfirmedSteers,
  restoreSteers,
} from "../store.js";
import { send as transportSend, type SendResult } from "../transport.js";

/** The chat an action's arguments address, for its notification's subject. */
const onChat = ({ chatID }: { readonly chatID: string }): string => chatID;

// `opID` is a dispatch argument (never minted in `run()`) so a retry past the Idempotency-Key TTL
// resolves via the server's op_id ledger (command/create_ledger.go). No `dedupe`: two clicks are
// two chats.

export const createChat = defineAction<
  { opID: string; name?: string; model?: string },
  CreatedChat | null
>({
  name: "chat.create",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, name, model }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "create_chat",
        payload: {
          op_id: opID,
          ...(name === undefined || name === "" ? {} : { name }),
          ...(model === undefined || model === "" ? {} : { model }),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "create a chat");
  },
  error: "Could not create a chat",
});

/** What a creating command committed: the chat and the tab opened for it in the same operation,
 *  adopted with no second `open_tab` round trip. `subject` is absent when no tab store is wired. */
export interface CreatedChat {
  chat: ChatHeader;
  subject?: TabSubject;
  version: number;
}

/** The chat a creating command returned; a reply with no chat throws even at HTTP 200. */
function chatFromReply(r: SendResult, signal: AbortSignal, what: string): CreatedChat {
  if (!r.ok) {
    if (signal.aborted || r.code === "cancelled") {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    const errOpts: { status: number; code?: string } = { status: r.status };
    if (r.code !== undefined) {
      errOpts.code = r.code;
    }
    throw new ActionError(r.error ?? `send failed with status ${String(r.status)}`, errOpts);
  }
  const body = r.body;
  if (typeof body !== "object" || body === null || !("chat" in body)) {
    throw new ActionError(`the server did not say which chat it created (${what})`, {
      status: r.status,
      code: "missing_chat",
    });
  }
  const rec = body as Record<string, unknown>;
  const version = rec["version"];
  return {
    chat: decodeChatHeader(rec["chat"]),
    ...(rec["subject"] === undefined || rec["subject"] === null
      ? {}
      : { subject: decodeTabSubject(rec["subject"]) }),
    // 0 when absent or malformed: below every real version, so the machine
    // treats the op as already covered.
    version: typeof version === "number" && Number.isFinite(version) ? version : 0,
  };
}

// No `chat.close` action: `close_tab` runs the same teardown server-side (`closeChatTeardown`).

// The app's ONLY chat delete path. With retention off a close deletes a non-empty chat; with it
// on the server keeps the chat until the purge window expires.

export const deleteChat = transportAction<string, { session: Session; atIndex: number }>({
  name: "chat.delete",
  networkMode: "always",
  scope: (id) => `chat:${id}`,
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  command: (id) => ({ type: "delete_chat", chat_id: id }),
  optimistic: (id) => {
    const session = get(id);
    if (session === undefined) {
      return undefined;
    }
    const atIndex = indexOfSession(id);
    removeChat(id);
    return { session, atIndex };
  },
  // If the server deleted but the response timed out, rollback reinserts a
  // ghost session that a later SSE chat_deleted event removes.
  rollback: (_id, op) => {
    if (op !== undefined) {
      reinsertSession(op.session, op.atIndex);
    }
  },
  error: errorAbout((id: string) => id, "Could not delete chat"),
});

export const setSupervised = transportAction<
  { chatID: string; enabled: boolean },
  { prev: boolean }
>({
  name: "chat.set_supervised",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, enabled }) => ({
    type: "set_supervised_mode",
    chat_id: chatID,
    payload: { enabled },
  }),
  optimistic: ({ chatID, enabled }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev: boolean = session.supervised_mode ?? false;
    setSupervisedMode(chatID, enabled);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setSupervisedMode(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Could not update supervised mode"),
});

/** What Send means while a turn runs on this chat. It touches no turn: a switch
 *  mid-turn leaves sent steers and queued rows where they are. */
export const setInterruptMode = transportAction<
  { chatID: string; mode: InterruptMode },
  { prev: InterruptMode }
>({
  name: "chat.set_interrupt_mode",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, mode }) => ({
    type: "set_interrupt_mode",
    chat_id: chatID,
    payload: { mode },
  }),
  optimistic: ({ chatID, mode }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev: InterruptMode = session.interrupt_mode ?? "steer";
    setChatInterruptMode(chatID, mode);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setChatInterruptMode(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Could not change what Send does mid-turn"),
});

/** The longest name the server accepts, in UTF-16 units (`marotte.MaxUserChatNameUnits`). */
export const MAX_CHAT_NAME_UNITS = 128;

/** A user's own chat name. No optimism: the tab label repaints from the
 *  server's `chat_updated`, and a refused name leaves the old one standing. */
export const renameChat = transportAction<{ chatID: string; name: string }>({
  name: "chat.rename",
  networkMode: "always",
  scope: ({ chatID }) => `chat-name:${chatID}`,
  dedupe: ({ chatID, name }) => joinKey("chat.rename", chatID, name),
  command: ({ chatID, name }) => ({
    type: "rename_chat",
    chat_id: chatID,
    payload: { name },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Couldn't rename the chat"),
});

/** The Kiro session zip can take KAS a while to assemble across a long chain. */
const SESSION_DOWNLOAD_TIMEOUT_MS = 120_000;

/** Download every Kiro session in the chat's chain as one zip
 *  (`GET /api/chats/{id}/kiro-session`). A fetch and a blob rather than an
 *  anchor, so a failure reaches the reader as a toast instead of a broken file. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const downloadKiroSession = defineAction<{ chatID: string; name: string }, void>({
  name: "chat.download_kiro_session",
  dedupe: ({ chatID }) => chatID,
  run: async ({ chatID, name }, signal) => {
    let r: Response;
    try {
      r = await fetch(`/api/chats/${encodeURIComponent(chatID)}/kiro-session`, {
        signal: withTimeout(signal, SESSION_DOWNLOAD_TIMEOUT_MS),
      });
    } catch (e) {
      throw classifyFetchError(e, signal);
    }
    if (!r.ok) {
      throw new ActionError("Download failed", { status: r.status });
    }
    const blob = await r.blob();
    if (signal.aborted) {
      return;
    }
    const url = URL.createObjectURL(blob);
    try {
      const a = document.createElement("a");
      a.href = url;
      a.download = exportFilename(name, chatID, "kiro-session");
      document.body.appendChild(a);
      a.click();
      a.remove();
    } finally {
      URL.revokeObjectURL(url);
    }
  },
  error: errorAbout(onChat, "Couldn't download the Kiro session"),
});

/** Persists the unsent composer text server-side, so it follows the user across devices.
 *  Debounced 600ms, flushed on blur, chat switch and unload; silent and not retryable (the next
 *  keystroke supersedes it). `scope` is per COMPOSER: sharing `chat.send_prompt`'s `chat:<id>`
 *  scope would queue the send's own draft-clear behind the turn it started. Attachments share it,
 *  since `draft_changed` carries both fields. */
export const setDraft = transportAction<{ chatID: string; text: string }>({
  name: "chat.set_draft",
  networkMode: "always",
  scope: ({ chatID }) => `composer:${chatID}`,
  command: ({ chatID, text }) => ({
    type: "set_draft",
    chat_id: chatID,
    payload: { text },
  }),
  success: false,
  error: false,
});

/** Persists the paths staged beside a draft, with `setDraft`'s cadence, scope, silence and no
 *  retry. Paths only (the server reads each file at send time); sends the WHOLE list. */
export const setAttachments = transportAction<{ chatID: string; paths: string[] }>({
  name: "chat.set_attachments",
  networkMode: "always",
  scope: ({ chatID }) => `composer:${chatID}`,
  command: ({ chatID, paths }) => ({
    type: "set_attachments",
    chat_id: chatID,
    payload: { paths },
  }),
  success: false,
  error: false,
});

/** Summarizes through KAS's native `compact` verb (typed `/compact` reaches the model as prose).
 *  `idempotencyKey`: a double compact summarizes a summary. Success claims ACCEPTANCE, not
 *  completion (`internal/command/compact.go`). */
export const compactChat = transportAction<{ chatID: string }>({
  name: "chat.compact",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  command: ({ chatID }) => ({
    type: "compact",
    chat_id: chatID,
  }),
  success: successAbout(onChat, "Compacting the conversation…"),
  error: errorAbout(onChat, "Compact failed"),
});

/** Delivers a message into the running turn (`_session/steer`). Optimistic; a refusal un-draws
 *  the row and restores the composer text. A custom runner: it adopts the 200's authoritative
 *  `steer_id` so the chip confirms without `steer_queued`, and lifts `reason` into the
 *  ActionError code so submit.ts can turn `no_turn` into a prompt. `error: false`: submit.ts
 *  owns the failure surface. */
export const steerChat = defineAction<
  { chatID: string; text: string; messageID: string },
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- run consumes the POST body itself, no result for a caller
  void,
  { chatID: string; steerID: string }
>({
  name: "chat.steer",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  error: false,
  run: async ({ chatID, text, messageID }, signal, ctx) => {
    const cmd: Parameters<typeof transportSend>[0] = {
      type: "steer",
      chat_id: chatID,
      payload: { text, message_id: messageID },
    };
    if (ctx?.idempotencyKey !== undefined) {
      (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD] = ctx.idempotencyKey;
    }
    const r: SendResult = await transportSend(cmd, { signal, reportSendState: false });
    if (!r.ok) {
      const opts: { status: number; code?: string } = { status: r.status };
      const code = r.reason ?? r.code;
      if (code !== undefined) {
        opts.code = code;
      }
      throw new ActionError(r.error ?? `send failed with status ${String(r.status)}`, opts);
    }
    const steerID = steerIDOf(r.body);
    if (steerID !== "") {
      // A fact here, not a guess: this is the reply to THIS device's own POST.
      recordSteerQueued(chatID, { id: steerID, text, origin: "user", state: "queued" });
    }
  },
  optimistic: ({ chatID, text, messageID }) => {
    recordSteerSent(chatID, messageID, text);
    return { chatID, steerID: steerIDFor(messageID) };
  },
  // args re-derives the id rather than reading `op`, which is undefined when
  // the dispatch dies before `optimistic` ran.
  rollback: ({ chatID, messageID }) => {
    forgetSteer(chatID, steerIDFor(messageID));
  },
});

/** The `steer_id` off the steer response body, or "" for an older server
 *  whose reply the SSE frame then covers. */
function steerIDOf(body: unknown): string {
  if (body === null || typeof body !== "object") {
    return "";
  }
  const id = (body as Record<string, unknown>)["steer_id"];
  return typeof id === "string" ? id : "";
}

/** Holds a message for the end of the running turn (Queue mode). Not optimistic: the row comes
 *  from the server's header broadcast, so every device shows one list. Lifts `reason` like
 *  `steerChat`; `error: false`. */
export const queuePrompt = defineAction<
  { chatID: string; text: string; messageID: string; attachments?: readonly AttachedFile[] },
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- run consumes the POST body itself, no result for a caller
  void
>({
  name: "chat.queue_prompt",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  dedupe: (args) => joinKey("chat.queue_prompt", args.chatID, args.messageID),
  idempotencyKey: true,
  error: false,
  run: async ({ chatID, text, messageID, attachments }, signal, ctx) => {
    const cmd: Parameters<typeof transportSend>[0] = {
      type: "queue_prompt",
      chat_id: chatID,
      payload: {
        text,
        message_id: messageID,
        ...(attachments !== undefined && attachments.length > 0
          ? { attachments: attachments.map((a) => ({ path: a.path, name: a.name })) }
          : {}),
      },
    };
    if (ctx?.idempotencyKey !== undefined) {
      (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD] = ctx.idempotencyKey;
    }
    const r: SendResult = await transportSend(cmd, { signal, reportSendState: false });
    if (!r.ok) {
      const opts: { status: number; code?: string } = { status: r.status };
      const code = r.reason ?? r.code;
      if (code !== undefined) {
        opts.code = code;
      }
      throw new ActionError(r.error ?? `send failed (${String(r.status)})`, opts);
    }
  },
});

/** Removes one queued follow-up. An id no row carries is success on the server,
 *  because two devices can discard the same row. */
export const unqueuePrompt = transportAction<{ chatID: string; messageID: string }>({
  name: "chat.unqueue_prompt",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  dedupe: (args) => joinKey("chat.unqueue_prompt", args.chatID, args.messageID),
  command: ({ chatID, messageID }) => ({
    type: "unqueue_prompt",
    chat_id: chatID,
    payload: { message_id: messageID },
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Could not remove the follow-up"),
});

/** Drops every steer KAS still holds (`_session/steer/clear`) without cancelling the turn.
 *  Optimistic. Only CONFIRMED entries go: a `pending` one has no server id to address. */
export const clearSteers = transportAction<
  { chatID: string },
  { chatID: string; removed: readonly PendingSteer[] }
>({
  name: "chat.clear_steers",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID }) => ({ type: "steer_clear", chat_id: chatID }),
  optimistic: ({ chatID }) => ({ chatID, removed: dropConfirmedSteers(chatID) }),
  // The removed entries exist only on the optimistic result; `undefined` means none were taken.
  rollback: (_args, op) => {
    if (op !== undefined) {
      restoreSteers(op.chatID, op.removed);
    }
  },
  error: errorAbout(onChat, "Could not discard"),
});

/** Delete ONE dock row. The wire has no per-steer removal, so the server clears KAS's buffer and
 *  resends the kept rows as one steer; nothing is drawn here. A refusal is an `ActionError` with
 *  the server's sentence, status and `reason`, so Edit can tell a refusal from a lost reply.
 *  Deduped per row. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const removeSteer = defineAction<{ chatID: string; steerID: string }, void>({
  name: "chat.remove_steer",
  networkMode: "always",
  scope: ({ chatID }) => `chat:${chatID}`,
  dedupe: ({ chatID, steerID }) => joinKey("chat.remove_steer", chatID, steerID),
  error: false,
  run: async ({ chatID, steerID }, signal) => {
    const r: SendResult = await transportSend(
      { type: "steer_remove", chat_id: chatID, payload: { steer_id: steerID } },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      const opts: { status: number; code?: string } = { status: r.status };
      const code = r.reason ?? r.code;
      if (code !== undefined) {
        opts.code = code;
      }
      throw new ActionError(r.error ?? `delete failed (${String(r.status)})`, opts);
    }
  },
});

// A live bridge switches in place (session/set_mode); an empty chat applies it at the first prompt.

export const setMode = transportAction<{ chatID: string; modeID: string }, { prev: string }>({
  name: "chat.set_mode",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, modeID }) => ({
    type: "set_mode",
    chat_id: chatID,
    payload: { mode_id: modeID },
  }),
  optimistic: ({ chatID, modeID }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev = session.current_mode_id;
    setCurrentMode(chatID, modeID);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setCurrentMode(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Could not switch mode"),
});

/** The History picker's inventory, through the GENERATED decoder so an absent per-list verdict
 *  cannot read as success. */
export const loadSessions = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SessionListResponse
>({
  name: "chat.load_sessions",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  decode: decodeSessionListResponse,
  error: "Could not load previous sessions",
});

// Adopts a listed KAS session as a NEW chat; the transcript arrives from session/load replay.
// `opID` matters more than for a create: minting per attempt binds two chats to one session.

export const resumeSession = defineAction<
  { opID: string; sessionID: string; name: string },
  CreatedChat | null
>({
  name: "chat.resume_session",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, sessionID, name }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "resume_session",
        payload: { session_id: sessionID, name, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "resume a session");
  },
  error: "Could not resume that session",
});

// The server resumes the parent's bridge and calls `session/fork`; nothing is copied client-side.
// `outcome` is informational; `opID` stops a retry forking twice.

export const forkChat = defineAction<
  { opID: string; parentChatID: string; title?: string },
  CreatedChat | null
>({
  name: "chat.fork",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ opID, parentChatID, title }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "fork_chat",
        payload: {
          parent_chat_id: parentChatID,
          op_id: opID,
          ...(title === undefined ? {} : { title }),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    return chatFromReply(r, signal, "start a tangent");
  },
  error: "Could not start a tangent",
});

// No scope: cancel must not queue behind an in-flight sendPrompt. `lead` is the send-now arrow's
// row, which the turn-end resend orders first.

export const cancelTurn = transportAction<
  { chatID: string; lead?: string },
  { wasThinking: boolean }
>({
  name: "chat.cancel_turn",
  command: ({ chatID, lead }) => ({
    type: "cancel",
    chat_id: chatID,
    ...(lead !== undefined && lead !== "" ? { payload: { lead } } : {}),
  }),
  optimistic: ({ chatID }) => {
    const session = get(chatID);
    const wasThinking = session?.thinking ?? false;
    setThinking(chatID, false);
    return { wasThinking };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setThinking(chatID, op.wasThinking);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: errorAbout(onChat, "Could not cancel turn"),
});

// defineAction: the caller needs a boolean, and setThinking is a loading indicator here rather
// than an optimistic mutation. Rollback restores the previous model.

export const switchModel = defineAction<
  { chatID: string; model: string },
  boolean,
  { prev: string }
>({
  name: "chat.switch_model",
  scope: ({ chatID }) => `chat:${chatID}`,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  optimistic: ({ chatID, model }) => {
    const session = get(chatID);
    // A pick made during a turn becomes `pending_model` and the turn keeps running on
    // the old model, so an optimistic write asserts a switch the server did not make
    // and the header echo flips the pill back. The badge carries the pending pick.
    if (session === undefined || turnLive(session)) {
      return undefined;
    }
    const prev = session.model;
    setModel(chatID, model);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setModel(chatID, op.prev);
    }
  },
  run: async ({ chatID, model }, signal) => {
    // sendPrompt owns thinking; the switcher button's bindLoadingState shows progress.
    const r = await transportSend(
      { type: "switch_model", chat_id: chatID, payload: { model } },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      if (signal.aborted || r.code === "cancelled") {
        throw new ActionError("cancelled", { code: "cancelled" });
      }
      const errOpts: { status: number; code?: string } = { status: r.status };
      if (r.code !== undefined) {
        errOpts.code = r.code;
      }
      throw new ActionError(r.error ?? `send failed with status ${String(r.status)}`, errOpts);
    }
    return true;
  },
  error: errorAbout(onChat, "Could not switch model"),
});

// The server acks at admission, so dispatch runs at the standard timeout; completion is SSE's.
// Returns "sent", "queued" (plain 409), "starting" (409: the admission holder cannot take a
// steer), "gone" (409 chat_not_found) or null. `error: false`: transport.send already toasts.

interface SendPromptArgs {
  chatID: string;
  text: string;
  messageID: string;
  model: string;
  attachments?: readonly unknown[];
}

export const sendPrompt = defineAction<
  SendPromptArgs,
  "sent" | "queued" | "starting" | "gone",
  { chatID: string }
>({
  name: "chat.send_prompt",
  scope: ({ chatID }) => `chat:${chatID}`,
  idempotencyKey: true,
  optimistic: ({ chatID }) => {
    setThinking(chatID, true);
    return { chatID };
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      setThinking(op.chatID, false);
    }
  },
  run: async (args, signal, ctx) => {
    const { chatID, text, messageID, model, attachments } = args;
    const r = await transportSend(
      {
        type: "prompt",
        chat_id: chatID,
        // Top level, where transport.send reads it to build the
        // Idempotency-Key header — not inside `payload`.
        ...(ctx?.idempotencyKey !== undefined
          ? { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }
          : {}),
        payload: {
          text,
          message_id: messageID,
          model,
          attachments:
            attachments !== undefined && attachments.length > 0 ? attachments : undefined,
        },
      },
      { signal, reportSendState: true, timeoutMs: API_TIMEOUT_MS },
    );
    if (r.ok) {
      return "sent";
    }
    if (r.status === 409) {
      if (r.reason === "starting") {
        // Returned as a VALUE, so the framework's rollback never runs: undo here, or the user's retry
        // becomes a steer.
        setThinking(chatID, false);
        return "starting";
      }
      if (r.reason === "chat_not_found") {
        // A tombstoned chat: there is no turn to steer into and no record to prompt, so this is
        // neither "queued" nor a transport failure. Keyed on the reason, never the error text.
        setThinking(chatID, false);
        return "gone";
      }
      // A steerable turn is in flight; caller (submit.ts) converts to a steer.
      return "queued";
    }
    throw new ActionError(r.error ?? "send failed", {
      status: r.status,
      ...(r.code !== undefined ? { code: r.code } : {}),
    });
  },
  error: false, // send-state.ts is the surface
});

// Scope is per request: two pending asks in one chat are independent.

/** The Go sentinel `errAlreadyAnswered` (internal/command/validate.go), served
 *  as 409 `{"error":"already_answered"}`. Matched by value; no generated
 *  constant exists for a command error body. */
const ALREADY_ANSWERED = "already_answered";

/** Answered, `superseded` (another surface answered first; the server takes one answer per id,
 *  so intent was met; silent, as decision-dock.ts announces it), or failed. A custom runner,
 *  because `transportAction`'s `run()` throws on every `!ok`. */
type DecisionAnswer = "answered" | "superseded";

async function answerDecision(
  cmd: { type: string; chat_id: string; payload: Record<string, unknown> },
  signal: AbortSignal,
  idempotencyKey: string | undefined,
): Promise<DecisionAnswer> {
  const withKey =
    idempotencyKey !== undefined ? { ...cmd, [IDEMPOTENCY_COMMAND_FIELD]: idempotencyKey } : cmd;
  const r = await transportSend(withKey as Parameters<typeof transportSend>[0], {
    signal,
    reportSendState: false,
  });
  if (r.ok) {
    return "answered";
  }
  if (r.status === 409 && r.error === ALREADY_ANSWERED) {
    return "superseded";
  }
  throw new ActionError(r.error ?? `send failed with status ${String(r.status)}`, {
    status: r.status,
    ...(r.code !== undefined ? { code: r.code } : {}),
  });
}

export const respondPermission = defineAction<
  {
    chatID: string;
    requestID: number;
    optionID: string;
    /** A turn approval's per-action verdicts: KAS's action id → keep. Absent
     *  on an ordinary tool permission. Every offered action must appear —
     *  an omitted id is a silent rollback, not "no opinion". */
    fileDecisions?: Record<string, boolean>;
    /** The deny note, on a reject_once answer only. */
    rejectionReason?: string;
  },
  DecisionAnswer
>({
  name: "chat.respond_permission",
  scope: ({ chatID, requestID }) => `perm:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, optionID, fileDecisions, rejectionReason }, signal, ctx) =>
    answerDecision(
      {
        type: "permission_response",
        chat_id: chatID,
        payload: {
          request_id: requestID,
          option_id: optionID,
          ...(fileDecisions !== undefined ? { file_decisions: fileDecisions } : {}),
          ...(rejectionReason !== undefined ? { rejection_reason: rejectionReason } : {}),
        },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  // Reached only by a real failure: a superseded answer returns normally.
  error: errorAbout(onChat, "Could not send permission response"),
});

export const respondElicitation = defineAction<
  {
    chatID: string;
    requestID: number;
    action: "accept" | "decline" | "cancel";
    content?: Record<string, unknown>;
  },
  DecisionAnswer
>({
  name: "chat.respond_elicitation",
  scope: ({ chatID, requestID }) => `elicit:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, action, content }, signal, ctx) =>
    answerDecision(
      {
        type: "elicitation_response",
        chat_id: chatID,
        payload:
          action === "accept" && content !== undefined
            ? { request_id: requestID, action, content }
            : { request_id: requestID, action },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  error: errorAbout(onChat, "Could not send elicitation response"),
});

export const respondUserInput = defineAction<
  {
    chatID: string;
    requestID: number;
    action: "answered" | "dismissed";
    answer?: string;
  },
  DecisionAnswer
>({
  name: "chat.respond_user_input",
  scope: ({ chatID, requestID }) => `user-input:${chatID}:${String(requestID)}`,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: ({ chatID, requestID, action, answer }, signal, ctx) =>
    answerDecision(
      {
        type: "user_input_response",
        chat_id: chatID,
        payload:
          action === "answered" && answer !== undefined
            ? { request_id: requestID, action, answer }
            : { request_id: requestID, action },
      },
      signal,
      ctx?.idempotencyKey,
    ),
  error: errorAbout(onChat, "Could not send your answer"),
});
