// The subject vocabulary: what a notification is ABOUT, and the one place a subject becomes a
// destination. DOM-free, so the service worker compiles it too.

import { split } from "@cplieger/keyenc";
import type { Route } from "./route-path.js";

/** marotte.PRSubjectPrefix. The ONLY TypeScript copy. */
const PR_SUBJECT_PREFIX = "pr:";

/** marotte.RunSubjectPrefix. The ONLY TypeScript copy. */
const RUN_SUBJECT_PREFIX = "run:";

/** marotte.runAskSubjectPrefix. The ONLY TypeScript copy. */
const RUN_ASK_SUBJECT_PREFIX = "runask:";

/** The tag prefix every subject's tag carries. */
const TAG_PREFIX = "marotte:";

/** The two subject fields as they travel (marotte.PushSubject, push_types.go:83-86). */
interface PushWire {
  readonly chatId: string;
  readonly subject: string;
}

/** The service worker's message to an open page: the subject (the page owns routes) and whether the
 *  user clicked the notification, it arrived for a focused page to deliver, or the presence tag must
 *  be re-derived. Structured-cloned across the worker boundary, so the page validates it. */
export interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived" | "subscription_changed";
  chatId: string;
  /** The notification's subject when it has no chat behind it: a run or a pull request,
   *  kind-prefixed so the route is keyed on what the subject IS. */
  subject?: string;
  /** The notification's kind on an arrival, "" otherwise. */
  kind?: string;
  /** The notification's title: the name of the tab its click opens, as the push was sent. */
  title?: string;
  body?: string;
}

/** What a notification is ABOUT, after parsing. */
export type PushTarget =
  | { readonly kind: "chat"; readonly chatID: string }
  | { readonly kind: "pr"; readonly identity: string }
  | { readonly kind: "run"; readonly workflowID: string }
  /** One step's wait in a run: `position` is the ask's id without its turn marker
   *  (`run-asks.ts` `runAskPosition`), `workflowID` its first component, "" when it is hashed. */
  | { readonly kind: "run_ask"; readonly workflowID: string; readonly position: string }
  | { readonly kind: "workspace" };

export function parsePushTarget(wire: PushWire): PushTarget {
  if (wire.subject.startsWith(PR_SUBJECT_PREFIX)) {
    const identity = wire.subject.slice(PR_SUBJECT_PREFIX.length);
    return identity === "" ? { kind: "workspace" } : { kind: "pr", identity };
  }
  if (wire.subject.startsWith(RUN_SUBJECT_PREFIX)) {
    return runTarget(wire.subject.slice(RUN_SUBJECT_PREFIX.length));
  }
  if (wire.subject.startsWith(RUN_ASK_SUBJECT_PREFIX)) {
    return runAskTarget(wire.subject.slice(RUN_ASK_SUBJECT_PREFIX.length));
  }
  return chatTarget(wire.chatId);
}

/** The one producer of a destination: both halves of the app spend this, so the worker's URL and
 *  the page's tabs cannot name different places. */
export function pushTargetRoute(target: PushTarget): Route {
  switch (target.kind) {
    case "chat":
      return { kind: "chat", id: target.chatID };
    case "pr":
      return { kind: "git", tab: "prs", pr: target.identity };
    case "run":
      return { kind: "run", id: target.workflowID };
    case "run_ask":
      return target.workflowID === ""
        ? { kind: "chat", id: "" }
        : { kind: "run", id: target.workflowID };
    case "workspace":
      return { kind: "chat", id: "" };
  }
}

/** The OS coalescing tag: one tray slot per SUBJECT, so a permission ask on one chat cannot
 *  silently replace the finished note on another. */
export function pushTargetTag(target: PushTarget): string {
  switch (target.kind) {
    case "chat":
      return `${TAG_PREFIX}${target.chatID}`;
    case "pr":
      return `${TAG_PREFIX}${PR_SUBJECT_PREFIX}${target.identity}`;
    case "run":
      return `${TAG_PREFIX}${RUN_SUBJECT_PREFIX}${target.workflowID}`;
    case "run_ask":
      return `${TAG_PREFIX}${RUN_ASK_SUBJECT_PREFIX}${target.position}`;
    case "workspace":
      return "marotte";
  }
}

/** The step-question target a tag names, or undefined for any other tag. */
export function runAskOfTag(tag: string): (PushTarget & { kind: "run_ask" }) | undefined {
  const prefix = `${TAG_PREFIX}${RUN_ASK_SUBJECT_PREFIX}`;
  return tag.startsWith(prefix) ? runAskTarget(tag.slice(prefix.length)) : undefined;
}

/** Whether a tag names a chat, a run or a step's wait, the targets a pending set speaks for. A pull
 *  request's banner has no ask to be settled by and is never retracted, and the constant tag is
 *  the cue this page showed for no one chat. */
export function settleableTag(tag: string): boolean {
  return tag.startsWith(TAG_PREFIX) && !tag.startsWith(`${TAG_PREFIX}${PR_SUBJECT_PREFIX}`);
}

/** A pull request's identity as both halves of the app spell it: the subject key minus its
 *  prefix. Twin of the composition in marotte.PRSubject, keyed on the repository's canonical
 *  `repo_id` rather than its display path. OPAQUE, and never parsed: `forgeID` is itself
 *  `<kind>:<host>` (forges.MakeID), so the key contains a colon and is not self-delimiting. */
export function prIdentity(forgeID: string, repoID: string, number: number): string {
  return `${forgeID}:${repoID}#${String(number)}`;
}

export function chatTarget(chatID: string): PushTarget {
  // `run:<workflowId>` is a BRIDGE KEY, not a chat: internal/agent/run_host.go registers a
  // parentless run's bridge under it (`runChatPrefix`), so that run's asks are broadcast on it and
  // arrive as the envelope chat id.
  if (chatID.startsWith(RUN_SUBJECT_PREFIX)) {
    return runTarget(chatID.slice(RUN_SUBJECT_PREFIX.length));
  }
  return chatID === "" ? { kind: "workspace" } : { kind: "chat", chatID };
}

export function runTarget(workflowID: string): PushTarget {
  return workflowID === "" ? { kind: "workspace" } : { kind: "run", workflowID };
}

/** A step's wait from its position. A position over keyenc's component budget is hashed and names
 *  no run, so it keeps its own tag and routes nowhere. */
export function runAskTarget(position: string): PushTarget & { kind: "run_ask" } {
  let workflowID = "";
  try {
    workflowID = split(position)[0] ?? "";
  } catch {
    // A hashed or malformed position: the tag still matches exactly.
  }
  return { kind: "run_ask", workflowID, position };
}

/** The target for an ASK, which carries both an envelope chat id and a run attribution. The run
 *  wins whenever it is present: the chat id is where the ask TRAVELS, the run id is what it is
 *  ABOUT. */
export function askTarget(chatID: string, runID: string | undefined): PushTarget {
  return runID !== undefined && runID !== "" ? runTarget(runID) : chatTarget(chatID);
}
