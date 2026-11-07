// Renders a finished turn whose tool group carries the hooks badge, through the production block renderer, inside a
// `.turn` card, for the badge's geometry and its popover. Importers run in a browser project.

import type { Turn } from "../turns.js";
import type { Entry, EntryToolCall } from "../wire/types.gen.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    d.style.display = "none";
    document.body.appendChild(d);
  }
}

const { buildAssistantBody, initBlockRenderer, resetBlockRenders } =
  await import("../messages-blocks.js");
const { initToolCallbacks } = await import("../messages-tools.js");
const { refreshGroupHeader } = await import("../tool-group.js");
const { setActive } = await import("../store.js");
const { hideHookPopover } = await import("../hook-badge.js");

const CHAT_ID = "c-hook-group";
setActive(CHAT_ID);
initToolCallbacks({ refreshGroupHeader });
initBlockRenderer({
  pushEntryEffect: (): void => undefined,
  disposeEntryEffects: (): void => undefined,
  liveAnchorChanged: () => undefined,
  makeRow: () => {
    const row = document.createElement("div");
    row.className = "msg-row";
    return row;
  },
  makeEvent: (e: Entry) => {
    const row = document.createElement("div");
    row.className = `event event-${e.kind}`;
    return row;
  },
});

export interface HookGroupOptions {
  /** Commands in the group; their titles are the header's summary. */
  readonly commands: readonly string[];
  /** Passing hooks after the commands, by name; each folds into the badge. */
  readonly hooks: readonly string[];
  /** How the first command ended when not cleanly: a failure, or a refusal (declined), which the header names. */
  readonly outcome?: "failed" | "declined";
  /** The hooks' trigger, `PreToolUse` when absent. A refused group keeps a tool-trigger hook as a row. */
  readonly trigger?: string;
  /** Prose after the group, which supersedes it so the renderer folds it. */
  readonly folded?: boolean;
  /** The card's inline size, `100%` of the page when absent. */
  readonly width?: string;
}

export interface HookGroup {
  readonly turn: HTMLElement;
  readonly group: HTMLElement;
  readonly header: HTMLElement;
  readonly badge: HTMLButtonElement;
}

let seq = 0;
const mounted: HTMLElement[] = [];

function call(turn: string, n: number, over: Partial<EntryToolCall>): Entry {
  const payload: EntryToolCall = {
    id: `${turn}-tool-${String(n)}`,
    title: "Run Command",
    status: "completed",
    kind: "execute",
    ts: 1,
    ...over,
  } as EntryToolCall;
  return { id: `${turn}-c${String(n)}`, turn, kind: "tool_call", seq: n, ts: n + 1, payload };
}

/** Mount one turn card holding the group `opts` describes, at the top of the page. */
export function mountHookGroup(opts: HookGroupOptions): HookGroup {
  seq++;
  const id = `t-hook-group-${String(seq)}`;
  const entries: Entry[] = [];
  let n = 1;
  for (const [i, title] of opts.commands.entries()) {
    entries.push(
      call(id, n++, {
        title,
        ...(i === 0 && opts.outcome === "failed" ? { status: "failed" } : {}),
        ...(i === 0 && opts.outcome === "declined" ? { declined: true } : {}),
      } as Partial<EntryToolCall>),
    );
  }
  for (const name of opts.hooks) {
    entries.push(
      call(id, n++, {
        title: `Hook fired: ${name}`,
        kind: "hook",
        source_path: `.kiro/hooks/${name.replaceAll(" ", "-")}.json`,
        hook_trigger: opts.trigger ?? "PreToolUse",
      } as Partial<EntryToolCall>),
    );
  }
  if (opts.folded === true) {
    entries.push({
      id: `${id}-say`,
      turn: id,
      kind: "text",
      seq: n,
      ts: n + 1,
      payload: { text: "done" },
    });
  }
  const turn = document.createElement("div");
  turn.className = "turn";
  turn.style.inlineSize = opts.width ?? "100%";
  const body = document.createElement("div");
  body.className = "turn-body";
  turn.append(body);
  document.body.prepend(turn);
  mounted.push(turn);
  const t: Turn = {
    id,
    n: 1,
    trigger: { id: `${id}-p`, text: "go" },
    body: entries,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
  buildAssistantBody(body, t, CHAT_ID, false);
  const group = body.querySelector<HTMLElement>(".tool-group");
  const header = group?.querySelector<HTMLElement>(":scope > .tool-group-header") ?? null;
  const badge = group?.querySelector<HTMLButtonElement>(":scope > .tool-group-hooks") ?? null;
  if (group === null || header === null || badge === null) {
    throw new Error("the renderer built no group with a hooks badge");
  }
  return { turn, group, header, badge };
}

/** Remove every mounted card and any popover panel left in the body. */
export function unmountHookGroups(): void {
  for (const t of mounted.splice(0)) {
    const badge = t.querySelector<HTMLElement>(".tool-group-hooks");
    if (badge !== null) {
      hideHookPopover(badge);
    }
    t.remove();
  }
  for (const panel of document.querySelectorAll(".tool-hook-popover")) {
    panel.remove();
  }
  resetBlockRenders();
}

export interface Point {
  readonly x: number;
  readonly y: number;
}

/** The badge's target: its box grown by its `::after` hit expander. */
function badgeTarget(badge: HTMLElement): DOMRect {
  const b = badge.getBoundingClientRect();
  const after = getComputedStyle(badge, "::after");
  const top = Math.min(0, parseFloat(after.top));
  const left = Math.min(0, parseFloat(after.left));
  return new DOMRect(b.left + left, b.top + top, b.width - 2 * left, b.height - 2 * top);
}

/** Points on the header's row across the group's whole inner width, outside the badge's target: its corners (in from
 *  the group's rounded clip), its middle, the strip past the badge's end edge, and the strips above and below the badge,
 *  on its horizontal centre. */
export function headerPointsOutsideBadge(header: HTMLElement, badge: HTMLElement): Point[] {
  const group = header.parentElement ?? header;
  const g = group.getBoundingClientRect();
  const row = header.getBoundingClientRect();
  const left = g.left + group.clientLeft;
  const h = {
    left,
    right: left + group.clientWidth,
    top: row.top,
    bottom: row.bottom,
    width: group.clientWidth,
    height: row.height,
  };
  const b = badge.getBoundingClientRect();
  const t = badgeTarget(badge);
  const mid = b.left + b.width / 2;
  const r = parseFloat(getComputedStyle(group).borderTopLeftRadius);
  return [
    { x: h.left + r, y: h.top + 1 },
    { x: h.left + r, y: h.bottom - 1 },
    { x: h.left + h.width / 2, y: h.top + h.height / 2 },
    { x: h.right - 2, y: b.top + b.height / 2 },
    { x: mid, y: h.top + 1 },
    { x: mid, y: h.bottom - 1 },
    { x: h.right - r, y: h.top + 1 },
    { x: h.right - r, y: h.bottom - 1 },
  ].filter((p) => p.x < t.left || p.x > t.right || p.y < t.top || p.y > t.bottom);
}
