// Which hook cards a reader sees: a row, folded into a hooks badge, or hidden. The one owner of
// that decision; every hook visibility question routes here. internal/chat/hook_visibility.go is its server twin, so
// find-in-chat counts no hook this hides. DOM-free at import: block-window.ts reads it under Node, so the badge
// and its popover live in hook-badge.ts.

import type { ToolStatus } from "./types.js";

/** The fields of a settled hook call the rules read. */
interface HookFacts {
  readonly status: ToolStatus;
  readonly output?: string | undefined;
  readonly hook_trigger?: string | undefined;
}

// kiro-cli's default for hooks.showStatus, held until the settings read answers.
let statusShown = true;
const listeners = new Set<() => void>();

/** kiro-cli's hooks.showStatus as last adopted. */
export function hookStatusShown(): boolean {
  return statusShown;
}

/** Adopt hooks.showStatus; a change runs every `onHookVisibility` listener. */
export function setHookStatusShown(shown: boolean): void {
  if (shown === statusShown) {
    return;
  }
  statusShown = shown;
  for (const fn of listeners) {
    fn();
  }
}

/** Run fn whenever hooks.showStatus changes, until the returned function is called. */
export function onHookVisibility(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** A hook that ran, passed and printed nothing. Every other hook keeps its row whatever the switch says. */
export function hookQuiet(call: HookFacts): boolean {
  return call.status === "completed" && (call.output ?? "").trim() === "";
}

/** Whether a hook run is part of what the chat counts: every run while hook status shows, else only those that failed
 *  or printed. A quiet hook that a refused group keeps as a row is not counted: the ledger cannot see the group. */
export function hookCounted(call: HookFacts, showStatus: boolean): boolean {
  return showStatus || !hookQuiet(call);
}

/** A hook that gates one tool call. An unresolved trigger is not one. */
export function isToolTriggerHook(call: HookFacts): boolean {
  return call.hook_trigger === "PreToolUse" || call.hook_trigger === "PostToolUse";
}

/** Whether a hook's entry draws a card. Only a quiet hook that gates no tool call goes, and only with the switch off;
 *  a tool-trigger hook always mounts, since a refused group keeps it as a row. */
export function hookRenders(call: HookFacts, showStatus: boolean): boolean {
  return showStatus || !hookQuiet(call) || isToolTriggerHook(call);
}

/** Stamp the hook facts a group reads off a mounted card; the outcome is `data-outcome`, written by the card. */
export function stampHookCard(card: HTMLElement, call: Partial<HookFacts>): void {
  if (card.dataset["kind"] !== "hook") {
    return;
  }
  // One-way: a later frame carrying no output does not make a hook that printed quiet.
  if ((call.output ?? "").trim() !== "") {
    card.dataset["hookPrinted"] = "1";
  }
  if (call.hook_trigger !== undefined) {
    card.dataset["hookTrigger"] = call.hook_trigger;
    card.toggleAttribute("data-hook-gates", isToolTriggerHook(call as HookFacts));
  }
}

/** A tool card outcome whose hooks stay rows: a hook's refusal shows as its call's own failure or denial, and the
 *  wire does not say which hook refused. */
const REFUSED = new Set(["fail", "denied", "declined"]);

/** What a group shows of its members: the rows, the hooks folded into its badge, and the cards that are no hook. */
export interface HookFold {
  readonly rows: HTMLElement[];
  readonly folded: HTMLElement[];
  readonly work: HTMLElement[];
  /** Rows of a group with no work: quiet hooks that gate no tool call, which the nearest group with work in the
   *  same container may fold (`foldStrayHook`); a row until one does. */
  readonly strays: HTMLElement[];
}

/** Hide or show each member per the hook rules and return what is left as rows and folded hooks. */
export function foldGroupHooks(members: readonly HTMLElement[]): HookFold {
  const work = members.filter((c) => c.dataset["kind"] !== "hook");
  const refused = work.some((c) => REFUSED.has(c.dataset["outcome"] ?? ""));
  const rows: HTMLElement[] = [];
  const folded: HTMLElement[] = [];
  const strays: HTMLElement[] = [];
  for (const c of members) {
    const fate = hookFate(c, refused, work.length > 0);
    const row = fate === "row" || fate === "stray";
    c.hidden = !row;
    c.toggleAttribute("data-hook-folded", fate === "folded");
    if (row) {
      rows.push(c);
    } else if (fate === "folded") {
      folded.push(c);
    }
    if (fate === "stray") {
      strays.push(c);
    }
  }
  return { rows, folded, work, strays };
}

/** Fold a stray into another group's badge, or give it back its row. */
export function foldStrayHook(card: HTMLElement, folded: boolean): void {
  card.hidden = folded;
  card.toggleAttribute("data-hook-folded", folded);
}

/** A quiet hook folds whatever its trigger, but a refused group keeps as a row a hook that could have refused: a
 *  tool-trigger one, or an unresolved one while status shows (with it off, `hookRenders` and the server hide it). */
function hookFate(
  c: HTMLElement,
  refused: boolean,
  hasWork: boolean,
): "row" | "folded" | "hidden" | "stray" {
  const quiet =
    c.dataset["kind"] === "hook" &&
    c.dataset["outcome"] === "ok" &&
    c.dataset["hookPrinted"] === undefined;
  const suspect =
    c.hasAttribute("data-hook-gates") || (statusShown && c.dataset["hookTrigger"] === undefined);
  if (!quiet || (refused && suspect)) {
    return "row";
  }
  if (!statusShown) {
    return "hidden";
  }
  if (hasWork) {
    return "folded";
  }
  return c.hasAttribute("data-hook-gates") ? "row" : "stray";
}
