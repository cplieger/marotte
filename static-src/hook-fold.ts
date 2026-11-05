// Which hook cards a reader sees: a row, folded into its tool group's header badge, or hidden. The one owner of
// that decision; every hook visibility question routes here. internal/chat/hook_visibility.go is its server twin, so
// find-in-chat counts no hook this hides.

import { el } from "@cplieger/ui";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { fileIcon, toolIcon } from "./icons.js";
import { kindNoun } from "./tool-kind-noun.js";
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

/** Run fn whenever hooks.showStatus changes. */
export function onHookVisibility(fn: () => void): void {
  listeners.add(fn);
}

/** A hook that ran, passed and printed nothing. Every other hook keeps its row whatever the switch says. */
export function hookQuiet(call: HookFacts): boolean {
  return call.status === "completed" && (call.output ?? "").trim() === "";
}

/** A hook that gates one tool call. An unresolved trigger is not one. */
export function isToolTriggerHook(call: HookFacts): boolean {
  return call.hook_trigger === "PreToolUse" || call.hook_trigger === "PostToolUse";
}

/** Whether a hook's entry draws a card. Only a quiet hook with no tool call to fold into goes, and only with the
 *  switch off; a tool-trigger hook always mounts, and its group decides what the reader sees. */
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
    card.toggleAttribute("data-hook-gates", isToolTriggerHook(call as HookFacts));
  }
}

/** A tool card outcome whose hooks stay rows: a hook's refusal shows as its call's own failure or denial, and the
 *  wire does not say which hook refused. */
const REFUSED = new Set(["fail", "denied", "declined"]);

/** What a group shows of its members: the rows, and the hooks folded into its badge. */
export interface HookFold {
  readonly rows: HTMLElement[];
  readonly folded: HTMLElement[];
}

/** Hide or show each member per the hook rules and return what is left as rows and folded hooks. */
export function foldGroupHooks(members: readonly HTMLElement[]): HookFold {
  const work = members.filter((c) => c.dataset["kind"] !== "hook");
  const refused = work.some((c) => REFUSED.has(c.dataset["outcome"] ?? ""));
  const rows: HTMLElement[] = [];
  const folded: HTMLElement[] = [];
  for (const c of members) {
    const fate = hookFate(c, refused, work.length > 0);
    c.hidden = fate !== "row";
    c.toggleAttribute("data-hook-folded", fate === "folded");
    if (fate === "row") {
      rows.push(c);
    } else if (fate === "folded") {
      folded.push(c);
    }
  }
  return { rows, folded };
}

function hookFate(c: HTMLElement, refused: boolean, hasWork: boolean): "row" | "folded" | "hidden" {
  const quiet =
    c.dataset["kind"] === "hook" &&
    c.dataset["outcome"] === "ok" &&
    c.dataset["hookPrinted"] === undefined;
  if (!quiet) {
    return "row";
  }
  if (!c.hasAttribute("data-hook-gates")) {
    return statusShown ? "row" : "hidden";
  }
  if (refused) {
    return "row";
  }
  if (!statusShown) {
    return "hidden";
  }
  return hasWork ? "folded" : "row";
}

/** Paint the header's fish-hook badge for n folded hooks, absent at zero. */
export function syncHookBadge(header: HTMLElement, n: number): void {
  let badge = header.querySelector<HTMLElement>(":scope > .tool-group-hooks");
  if (n === 0) {
    badge?.remove();
    return;
  }
  if (badge === null) {
    badge = el(
      "span",
      { className: "tool-group-hooks", [CHROME_ATTR]: "" },
      el(
        "span",
        { className: "tool-group-hooks-icon", "aria-hidden": "true" },
        iconEl(toolIcon({ kind: "hook", toolName: "" })),
      ),
      el("span", { className: "tool-group-hooks-n", "aria-hidden": "true" }),
      el("span", { className: "sr-only" }),
    );
    header.appendChild(badge);
  }
  const count = String(n);
  const visible = badge.querySelector(".tool-group-hooks-n");
  if (visible !== null && visible.textContent !== count) {
    visible.textContent = count;
  }
  const spoken = `, ${count} ${kindNoun("hook", n)} ran`;
  const name = badge.querySelector(".sr-only");
  if (name !== null && name.textContent !== spoken) {
    name.textContent = spoken;
  }
}

/** List the folded hooks as the body's last child, each with its hook file's chip; absent with none. Rebuilt only
 *  when the folded set changes, so a focused chip keeps its focus. */
export function syncHookList(body: HTMLElement, folded: readonly HTMLElement[]): void {
  let list = body.querySelector<HTMLElement>(":scope > .tool-hook-list");
  if (folded.length === 0) {
    list?.remove();
    return;
  }
  const keys = folded.map((c) => c.dataset["toolId"] ?? "").join("\n");
  list ??= el("ul", { className: "tool-hook-list", "aria-label": "Hooks that ran" });
  if (list.dataset["keys"] !== keys) {
    list.dataset["keys"] = keys;
    list.replaceChildren(...folded.map(hookItem));
  }
  if (list.parentElement !== body || list.nextElementSibling !== null) {
    body.appendChild(list);
  }
}

function hookItem(card: HTMLElement): HTMLElement {
  const title = card.dataset["title"] ?? "";
  const item = el(
    "li",
    { className: "tool-hook-item", "data-hook-for": card.dataset["toolId"] ?? "" },
    el(
      "span",
      { className: "tool-icon is-ok", "aria-hidden": "true" },
      iconEl(toolIcon({ kind: "hook", toolName: "" })),
    ),
    el("span", { className: "tool-title" }, title),
  );
  const path = card.dataset["filePath"] ?? "";
  const name = card.dataset["filename"] ?? "";
  if (path !== "") {
    const chip = el(
      "button",
      {
        type: "button",
        className: "tool-file-link",
        "data-path": path,
        "data-tooltip": path,
        "aria-label": `Open the hook file ${name}`,
      },
      el("span", { className: "tool-file-icon" }, iconEl(fileIcon(name, false))),
      el("span", { className: "tool-file-name" }, name),
    );
    // The folded card's own chip owns what a hook file opens.
    chip.addEventListener("click", (e: Event) => {
      e.stopPropagation();
      card.querySelector<HTMLElement>(".tool-file-link")?.click();
    });
    item.appendChild(chip);
  }
  return item;
}

/** The list item standing for a folded hook card, where find-in-chat lands a hit on that card. */
export function foldedHookItem(card: HTMLElement): HTMLElement | null {
  if (!card.hasAttribute("data-hook-folded")) {
    return null;
  }
  const id = CSS.escape(card.dataset["toolId"] ?? "");
  return (
    card
      .closest(".tool-group")
      ?.querySelector<HTMLElement>(`.tool-hook-list > li[data-hook-for="${id}"]`) ?? null
  );
}
