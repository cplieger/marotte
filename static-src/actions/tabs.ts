// The four tab mutations; nothing else may send a tab command. A reply is what the server
// committed, reconciled by the pending-op machine against the following `tabs_changed` frame.
// Its version feeds the machine, never the watermark: only an EVENT advances it (tabs-sync.ts),
// or another device's in-flight v+1 would read as stale. `open_tab` and `close_tab` dedupe via a
// key function (the default key includes the unique `op_id`); `pin_tab` and `reorder_tabs` dedupe
// on nothing, and none takes an argument-composite idempotency key, so a repeat is never collapsed.
// `op_id` is a dispatch ARGUMENT: `run()` is re-invoked per retry, so one minted there correlates
// nothing.

import { join as joinKey } from "@cplieger/keyenc";

import { defineAction, retryNetwork, RETRY_STANDARD, IDEMPOTENCY_COMMAND_FIELD } from "./index.js";
import { send as transportSend, type SendResult } from "../transport.js";
import type { TabKind, TabSubject } from "../types.js";
import { decodeTabSubject } from "../wire/decoders.gen.js";

/** What `open_tab` needs about the thing being opened. `ref` is empty for a
 *  singleton, whose identity is its kind. */
interface OpenTabArgs {
  kind: TabKind;
  ref: string;
  /** An already-open tab to nest under. Empty for top level. A parent that
   *  is not open promotes the new tab to top level rather than refusing it. */
  parent: string;
  /** Whether closing this tab tears down what it shows. */
  owns: boolean;
  opID: string;
}

/** What the server committed for an open. `created: false` commits nothing and emits no event, so
 *  the pending-op machine retires such an op on the spot. */
interface OpenTabReply {
  subject: TabSubject;
  created: boolean;
  version: number;
}

export const openTabCommand = defineAction<OpenTabArgs, OpenTabReply | null>({
  name: "tabs.open",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  dedupe: (args) => joinKey("tabs.open", args.kind, args.ref),
  run: async ({ kind, ref, parent, owns, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "open_tab",
        payload: {
          kind,
          op_id: opID,
          ...(ref === "" ? {} : { ref }),
          ...(parent === "" ? {} : { parent }),
          ...(owns ? { owns: true } : {}),
        },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "open that tab");
    }
    const body = asObject(r.body);
    if (body === null || !("subject" in body)) {
      throw sendFailure(r, "open that tab");
    }
    return {
      subject: decodeTabSubject(body["subject"]),
      // Absent treated as not created — the conservative reading.
      created: body["created"] === true,
      version: numberField(body, "version"),
    };
  },
  // At the product limit the refusal has a remedy.
  error: (_args, err) =>
    err.status === 409
      ? "Close a tab first, because this workspace has too many open."
      : "Could not open that tab",
});

interface CloseTabArgs {
  id: string;
  opID: string;
}

/** How long a close may stay unanswered before the pending-op machine VERIFIES by re-listing
 *  (the removal stays applied). */
const CLOSE_CONFIRM_MS = 5000;

/** What the server committed for a close: a list (a parent and children close together); empty
 *  is normal, since two devices can close one tab. */
interface CloseTabReply {
  closed: string[];
  version: number;
}

export const closeTabCommand = defineAction<CloseTabArgs, CloseTabReply | null>({
  name: "tabs.close",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  timeout: CLOSE_CONFIRM_MS,
  dedupe: (args) => joinKey("tabs.close", args.id),
  run: async ({ id, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "close_tab",
        payload: { id, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "close that tab");
    }
    const body = asObject(r.body);
    const closed = body?.["closed"];
    return {
      closed: Array.isArray(closed) ? closed.filter((v): v is string => typeof v === "string") : [],
      version: body === null ? 0 : numberField(body, "version"),
    };
  },
  // No framework toast: a TIMEOUT is inconclusive (the machine verifies), and a definitive refusal
  // is the close gesture's to report with its rollback.
  error: false,
});

interface ReorderTabsArgs {
  order: readonly string[];
  opID: string;
}

/** The exact-set refusal: the set moved under the drag, so re-list, never re-send. */
export const REORDER_STALE = "stale" as const;

/** The version the reorder committed, for the pending-op machine. */
interface ReorderTabsReply {
  version: number;
}

export const reorderTabsCommand = defineAction<
  ReorderTabsArgs,
  ReorderTabsReply | typeof REORDER_STALE | null
>({
  name: "tabs.reorder",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ order, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "reorder_tabs",
        payload: { order: [...order], op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (r.status === 409) {
      // Not an error to the reader: the caller rolls the drop back and re-lists.
      return REORDER_STALE;
    }
    if (!r.ok) {
      throw sendFailure(r, "reorder the tabs");
    }
    const body = asObject(r.body);
    return { version: body === null ? 0 : numberField(body, "version") };
  },
  error: "Could not reorder the tabs",
});

interface PinTabArgs {
  id: string;
  pinned: boolean;
  opID: string;
}

export const pinTabCommand = defineAction<PinTabArgs, boolean>({
  name: "tabs.pin",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ id, pinned, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "pin_tab",
        payload: { id, pinned, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      // 404, not a close's empty answer: a pin names a tab, so an unknown one is a mistake, not a race.
      throw sendFailure(r, "pin that tab");
    }
    return true;
  },
  error: "Could not pin that tab",
});

interface ReparentTabArgs {
  id: string;
  parent: string;
  opID: string;
}

/** Hang an open tab under an open chat tab. No dedupe (two moves must both land); the reply
 *  carries the subject, since an unchanged parent emits no frame to adopt from. */
export const reparentTabCommand = defineAction<ReparentTabArgs, TabSubject>({
  name: "tabs.reparent",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ id, parent, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "reparent_tab",
        payload: { id, parent, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "move that tab");
    }
    const body = asObject(r.body);
    if (body === null || !("subject" in body)) {
      throw sendFailure(r, "move that tab");
    }
    return decodeTabSubject(body["subject"]);
  },
  error: "Could not move that tab",
});

// --- Shared reply handling ---

function asObject(body: unknown): Record<string, unknown> | null {
  return typeof body === "object" && body !== null ? (body as Record<string, unknown>) : null;
}

/** The committed version, 0 when absent or malformed: below every real one, so already covered. */
function numberField(body: Record<string, unknown>, key: string): number {
  const v = body[key];
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

/** A transport failure as the framework's error surface reads it (a thrown Error with `status`). */
function sendFailure(r: SendResult, what: string): Error & { status?: number } {
  const err: Error & { status?: number } = new Error(r.error ?? `Could not ${what}`);
  err.status = r.status;
  return err;
}
