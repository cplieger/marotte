// The tab mutations; nothing else may send a tab command. A reply is what the server
// committed, reconciled by the pending-op machine against the following `tabs_changed` frame.
// Its version feeds the machine, never the watermark: only an EVENT advances it (tabs-sync.ts),
// or another device's in-flight v+1 would read as stale. `open_tab` and `close_tab` dedupe via a
// key function (the default key includes the unique `op_id`); `pin_tab` and `reorder_tabs` dedupe
// on nothing, and none takes an argument-composite idempotency key, so a repeat is never collapsed.
// `op_id` is a dispatch ARGUMENT: `run()` is re-invoked per retry, so one minted there correlates
// nothing.

import { join as joinKey } from "@cplieger/keyenc";

import {
  ActionError,
  defineAction,
  retryNetwork,
  RETRY_STANDARD,
  IDEMPOTENCY_COMMAND_FIELD,
  type ActionErrorLike,
} from "./index.js";
import type { Action, ActionDefinition } from "@cplieger/reactive/actions";
import { errorAbout } from "./subject.js";
import { send as transportSend, type SendResult } from "../transport.js";
import type { TabKind, TabSubject } from "../types.js";
import type { HistoryTab } from "../route-path.js";
import { decodeTabSubject } from "../wire/decoders.gen.js";
import type { CloseImpact } from "../wire/types.gen.js";
import { impactRefusal } from "./close-impact.js";
import { CLOSE_CONFIRM_MS } from "../tabs-sync.js";

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

/** The sentence a refused open reads as. At the product limit the refusal has a remedy. Keyed on the server's
 *  reason, never on 409 alone: a run being purged and an idempotent retry still in progress answer 409 too. */
export function openTabRefusal(err: ActionErrorLike): string {
  return err.code === "tabs_full"
    ? "Close a tab first, because this workspace has too many open."
    : "Could not open that tab";
}

const openTabDefinition: ActionDefinition<OpenTabArgs, OpenTabReply | null> = {
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
  error: (_args, err) => openTabRefusal(err),
};

export const openTabCommand = defineAction(openTabDefinition);

/** `openTabCommand` without the framework's toast, for a caller that handles a 404 itself and
 *  reports any other refusal with `openTabRefusal`. */
export const openTabQuietCommand = defineAction({
  ...openTabDefinition,
  name: "tabs.open.quiet",
  error: false,
});

interface CloseTabArgs {
  id: string;
  opID: string;
  /** What the person's confirm named; the server refuses the close when it stops more. A remote
   *  close or a singleton toggle sends none and is unchecked. */
  impact?: CloseImpact;
}

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
  run: async ({ id, opID, impact }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "close_tab",
        payload: { id, op_id: opID, ...(impact === undefined ? {} : { impact }) },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw impactRefusal(r, sendFailure(r, "close that tab"));
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

interface AckTabArgs {
  id: string;
  seen: number;
  opID: string;
}

/** Acknowledge a chat or run tab up to a read position. Background work with no toast; attention.ts
 *  decides what is resent. Resolves the marker as the server now stores it, or 0 when the tab is
 *  not open. */
export const ackTabCommand = defineAction<AckTabArgs, number>({
  name: "tabs.ack",
  networkMode: "always",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  run: async ({ id, seen, opID }, signal, ctx) => {
    const r = await transportSend(
      {
        type: "ack_tab",
        payload: { id, seen, op_id: opID },
        ...(ctx?.idempotencyKey === undefined
          ? {}
          : { [IDEMPOTENCY_COMMAND_FIELD]: ctx.idempotencyKey }),
      },
      { signal, reportSendState: false },
    );
    if (!r.ok) {
      throw sendFailure(r, "acknowledge that tab");
    }
    const body = asObject(r.body);
    return body === null ? 0 : numberField(body, "seen");
  },
  error: false,
});

// --- The switch (sends nothing: the active tab is this device's alone) ---

export interface SwitchArgs {
  /** The tab to show. */
  readonly to: string;
  /** What the refusal notice is about (`notice-subject.ts`): a chat id, `run:<id>`, or "". */
  readonly subject: string;
}

/** `superseded`: a newer activation replaced this one, or the reader left the tab. */
export type SwitchVerdict = "shown" | "superseded";

/** What `begin` committed: the activation's generation and the last settled tab to return to. */
export interface SwitchOp {
  readonly gen: number;
  readonly from: string;
  /** History's pane when the switch left it, so a failure returns there; else null. */
  readonly history: HistoryTab | null;
  /** The tab History covered when the switch left it, else "". */
  readonly covered: string;
}

/** The switch's halves, injected by tabs.ts, which imports this module. */
export interface SwitchHooks {
  /** The gesture half, inside the dispatch. */
  begin(args: SwitchArgs): SwitchOp;
  /** The whole run, its retries included. MUST observe `signal`: the time bound only aborts it,
   *  so a `settle` that ignores it never settles and never rolls back. */
  settle(args: SwitchArgs, signal: AbortSignal): Promise<SwitchVerdict>;
  /** Undo a definitive failure. Answers true when the revert did not put the reader back on the
   *  previous tab: no other tab is left, or History came back. */
  revert(args: SwitchArgs, op: SwitchOp, err: ActionErrorLike): boolean;
}

/** Whether a failed load may be tried again: `settle`'s own retry loop and the toast's Retry
 *  button read this one predicate. A timeout passes it, so its toast offers Retry, but `settle`
 *  stops at its signal's abort before consulting it. */
export function switchRetryable(err: ActionErrorLike): boolean {
  return err.code !== "show_failed" && err.status !== 404 && err.status !== 400;
}

/** Errors whose revert did not put the reader back on the previous tab (no other tab is left, or
 *  History came back), so their sentence drops "so you are back on the previous tab". The framework
 *  hands `rollback` and the notice the same error object. */
const stayed = new WeakSet<ActionErrorLike>();

/** The sentence a failed switch reads as, after the subject's name. A timeout is matched before
 *  status 0, because the framework's own timeout error carries status 0 too. */
export function switchRefusal(_args: SwitchArgs, err: ActionErrorLike): string {
  const back = stayed.has(err) ? "." : ", so you are back on the previous tab.";
  if (err.code === "timeout") {
    return `This conversation did not load in time${back}`;
  }
  if (err.status === 404) {
    return "This conversation no longer exists.";
  }
  if (err.code === "show_failed") {
    return "Could not show that tab.";
  }
  if (err.status === 400) {
    return "Could not load this conversation.";
  }
  if (err.status === 0) {
    return `Could not reach marotte${back}`;
  }
  return `Could not load this conversation${back}`;
}

/** The user's tab switch: `begin` is the optimistic gesture half, `settle` awaits the view's first
 *  content, and a definitive failure reverts with a toast. `timeoutMs` bounds the whole run.
 *
 *  No `retry` field: the framework sleeps its back-off on the run signal and, when the bound fires
 *  in that sleep, rolls back without calling `run` again, so a switch the reader already left would
 *  revert over the newer tab. `settle` runs the retries and checks supersession after each sleep. */
export function defineSwitchCommand(
  hooks: SwitchHooks,
  timeoutMs: number,
): Action<SwitchArgs, SwitchVerdict> {
  return defineAction<SwitchArgs, SwitchVerdict, SwitchOp>({
    name: "tabs.switch",
    // A resident view needs no network; an offline load fails as `network`.
    networkMode: "always",
    timeout: timeoutMs,
    retryable: switchRetryable,
    optimistic: (args) => hooks.begin(args),
    run: (args, signal) => hooks.settle(args, signal),
    rollback: (args, op, err) => {
      if (err.code !== "cancelled" && op !== undefined && hooks.revert(args, op, err)) {
        stayed.add(err);
      }
    },
    error: errorAbout((a: SwitchArgs) => a.subject, switchRefusal),
  });
}

// --- Shared reply handling ---

function asObject(body: unknown): Record<string, unknown> | null {
  return typeof body === "object" && body !== null ? (body as Record<string, unknown>) : null;
}

/** The committed version, 0 when absent or malformed: below every real one, so already covered. */
function numberField(body: Record<string, unknown>, key: string): number {
  const v = body[key];
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

/** A failed send as the framework's error surface reads it: its status, and as its code either the transport's
 *  failure class (`timeout`, `network`, `cancelled`) or the server's machine reason, so a caller tells a timeout
 *  from a refusal and one refusal from another. */
function sendFailure(r: SendResult, what: string): ActionError {
  const code = r.code ?? r.reason;
  return new ActionError(r.error ?? `Could not ${what}`, {
    status: r.status,
    ...(code !== undefined ? { code } : {}),
  });
}
