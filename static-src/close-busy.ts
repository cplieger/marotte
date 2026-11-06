// What closing a tab stops: the server's close cancels the chat's reply, its questions and its
// unread queued messages, and stops its workflow runs, tangents included, with no undo. An idle
// close asks nothing. The server checks the ids the dialog named against what it would stop and
// refuses a close another device made stale (`close_impact_changed`).

import { confirm } from "./confirm.js";
import { pendingDecisionIDs } from "./decision-dock.js";
import { sessionDrain } from "./queue-slots.js";
import { liveRunIDsForChat, runLabelOf } from "./run-store.js";
import { get, turnLive } from "./store.js";
import { inFlightMessageIDs } from "./submit.js";
import type { ChatWork, CloseImpact } from "./wire/types.gen.js";

/** One chat row the close takes with it: its ref and the name its tab shows. */
export interface ClosingChat {
  readonly ref: string;
  readonly name: string;
}

/** A gate's answer: the dialog's verdict (a plain `true` when nothing is stopped) and the impact
 *  the close sends, read in the same pass as the dialog's text. */
interface CloseConfirm {
  readonly verdict: true | Promise<boolean>;
  readonly impact: CloseImpact;
}

/** One chat's work by id, from the signals the tab strip paints from: its open turns and its own
 *  sends still in flight, its asks, its live runs, its unread steers and its follow-ups whose turn
 *  has not opened. */
function chatWork(ref: string): ChatWork {
  const session = get(ref);
  const turns: string[] = [];
  if (session !== undefined) {
    for (const [id, state] of session.turns) {
      if (state.closeAt === undefined) {
        turns.push(id);
      }
    }
  }
  turns.push(...inFlightMessageIDs(ref));
  return {
    ref,
    turns,
    asks: pendingDecisionIDs(ref),
    runs: liveRunIDsForChat(ref),
    steers: (session?.steers ?? []).map((s) => s.id),
    queued:
      session === undefined
        ? []
        : sessionDrain(session)
            .rows.filter((r) => r.wait.kind !== "sent")
            .map((r) => r.row.id),
  };
}

function closeStopsWork(chats: readonly ClosingChat[]): CloseImpact {
  return { chats: chats.map((c) => chatWork(c.ref)) };
}

function union(a: readonly string[], b: readonly string[]): string[] {
  return [...new Set([...a, ...b])];
}

/** Each chat's id lists as the union of what the page sent and what the server answered; a chat
 *  only the server named is added. */
export function mergeImpact(sent: CloseImpact, server: CloseImpact): CloseImpact {
  const byRef = new Map<string, ChatWork>();
  for (const w of [...sent.chats, ...server.chats]) {
    const had = byRef.get(w.ref);
    byRef.set(
      w.ref,
      had === undefined
        ? { ...w }
        : {
            ref: w.ref,
            turns: union(had.turns, w.turns),
            asks: union(had.asks, w.asks),
            runs: union(had.runs, w.runs),
            steers: union(had.steers, w.steers),
            queued: union(had.queued, w.queued),
          },
    );
  }
  return { chats: [...byRef.values()] };
}

/** The chats of `impact` with each kind of work, named as `chats` shows them (the ref for a chat
 *  the page has no row for). */
interface WorkByKind {
  readonly replies: readonly ClosingChat[];
  readonly asks: readonly ClosingChat[];
  /** Each run's name, "" for one whose name has not loaded. */
  readonly runs: readonly string[];
  readonly queued: number;
}

/** A chat replies when the impact names a turn of it, or when its row says a turn runs whose id
 *  this page has not loaded (`turnLive`); the server names that one on a refusal. */
function byKind(impact: CloseImpact, chats: readonly ClosingChat[]): WorkByKind {
  const names = new Map(chats.map((c) => [c.ref, c.name]));
  const named = (w: ChatWork): ClosingChat => ({ ref: w.ref, name: names.get(w.ref) ?? w.ref });
  const replying = (w: ChatWork): boolean => {
    const session = get(w.ref);
    return w.turns.length > 0 || (session !== undefined && turnLive(session));
  };
  return {
    replies: impact.chats.filter(replying).map(named),
    asks: impact.chats.filter((w) => w.asks.length > 0).map(named),
    runs: impact.chats.flatMap((w) => w.runs.map(runLabelOf)),
    queued: impact.chats.reduce((n, w) => n + w.steers.length + w.queued.length, 0),
  };
}

function isIdle(work: WorkByKind): boolean {
  return (
    work.replies.length === 0 &&
    work.asks.length === 0 &&
    work.runs.length === 0 &&
    work.queued === 0
  );
}

function count(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

function listOf(parts: readonly string[]): string {
  if (parts.length <= 2) {
    return parts.join(" and ");
  }
  return `${parts.slice(0, -1).join(", ")}, and ${parts.at(-1) ?? ""}`;
}

/** The runs term, naming each run whose name is known and counting the rest. */
function runsText(runs: readonly string[]): string {
  const named = runs.filter((n) => n !== "");
  const unnamed = runs.length - named.length;
  if (named.length === 0) {
    return `stops ${count(unnamed, "workflow run", "workflow runs")}`;
  }
  const items = unnamed === 0 ? named : [...named, count(unnamed, "other", "others")];
  return `stops the ${runs.length === 1 ? "workflow run" : "workflow runs"} ${listOf(items)}`;
}

/** A per-chat term: `bare` when the closed tab's own chat is the only one, else naming each. */
function perChat(
  chats: readonly ClosingChat[],
  closingRef: string,
  bare: string,
  named: string,
): string {
  if (chats.length === 1 && chats[0]?.ref === closingRef) {
    return bare;
  }
  return `${named} ${listOf(chats.map((c) => c.name))}`;
}

/** What the close stops, as the verb phrases after "It" in the close confirm's body; `ref` is the
 *  closed tab's own chat ("" for none), which goes unnamed. */
function stopsClauses(ref: string, work: WorkByKind): string[] {
  const parts: string[] = [];
  if (work.replies.length > 0) {
    const named = work.replies.length === 1 ? "stops the reply in" : "stops the replies in";
    parts.push(perChat(work.replies, ref, "stops the reply", named));
  }
  if (work.asks.length > 0) {
    parts.push(
      perChat(
        work.asks,
        ref,
        "cancels the question waiting for your answer",
        work.asks.length === 1
          ? "cancels the question waiting for your answer in"
          : "cancels the questions waiting for your answer in",
      ),
    );
  }
  if (work.runs.length > 0) {
    parts.push(runsText(work.runs));
  }
  if (work.queued > 0) {
    parts.push(`drops ${count(work.queued, "queued message", "queued messages")}`);
  }
  return parts;
}

function askToStop(
  name: string,
  ref: string,
  chats: readonly ClosingChat[],
  impact: CloseImpact,
): true | Promise<boolean> {
  const work = byKind(impact, chats);
  if (isIdle(work)) {
    return true;
  }
  const effects = listOf(stopsClauses(ref, work));
  return confirm({
    title: `Close ${name}?`,
    body: `It ${effects}.`,
    confirmLabel: "Close and stop",
    variant: "destructive",
  });
}

/** The gate tabs.ts runs before a person's close of the tab named `name` (chat `ref`, or "")
 *  taking `chats` with it. With `stale` (the impact the refused close sent and the server's
 *  answer) it asks again on their merge, naming a chat only the server knew by `chats`' name for
 *  it. */
export function confirmBusyClose(
  name: string,
  ref: string,
  chats: readonly ClosingChat[],
  stale?: { readonly sent: CloseImpact; readonly server: CloseImpact },
): CloseConfirm {
  const impact =
    stale === undefined ? closeStopsWork(chats) : mergeImpact(stale.sent, stale.server);
  return { verdict: askToStop(name, ref, chats, impact), impact };
}

/** What a History delete of a chat also stops in the chats under its tabs (`chats`, the deleted
 *  chat excluded): the sentence its confirm appends ("" when they are all idle) and the impact the
 *  delete sends, which lists every one of them. With `server` it is the merge of `sent` and the
 *  server's answer. Their workflow runs go on (only a person's close of a chat's own tab stops
 *  them), so neither the sentence nor the impact names a run. */
export function deleteStopsWork(
  chats: readonly ClosingChat[],
  stale?: { readonly sent: CloseImpact; readonly server: CloseImpact },
): { readonly text: string; readonly impact: CloseImpact } {
  const merged =
    stale === undefined ? closeStopsWork(chats) : mergeImpact(stale.sent, stale.server);
  const impact: CloseImpact = { chats: merged.chats.map((w) => ({ ...w, runs: [] })) };
  const work = byKind(impact, chats);
  if (isIdle(work)) {
    return { text: "", impact };
  }
  return { text: `Closing its tabs also ${listOf(stopsClauses("", work))}.`, impact };
}
