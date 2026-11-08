// Tool groups are made of tool calls alone: a lone call with hooks is a plain tool box carrying the hooks badge on its
// own header line, and a quiet turn hook folds into the badge of the nearest box in its turn. Rendered through the
// production dispatcher with the shipped stylesheets, so what is asserted is what a reader sees.

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Turn } from "./turns.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

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

const { buildAssistantBody, resetBlockRenders, initBlockRenderer } =
  await import("./messages-blocks.js");
const { clearAllEntrySigs, toolCallSigs } = await import("./store-signals.js");
const { initToolCallbacks } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setActive } = await import("./store.js");
const { setHookStatusShown } = await import("./hook-fold.js");

const CHAT_ID = "c-tool-box-hooks";
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

let style: HTMLStyleElement | undefined;
let seq = 0;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});
afterAll(() => {
  style?.remove();
});
beforeEach(() => {
  resetBlockRenders();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
  setHookStatusShown(true);
});
afterEach(() => {
  resetBlockRenders();
  for (const panel of document.querySelectorAll(".tool-hook-popover")) {
    panel.remove();
  }
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function call(turn: string, n: number, over: Partial<EntryToolCall> = {}): Entry {
  const payload = {
    id: `${turn}-tool-${String(n)}`,
    title: "Send Message",
    status: "completed",
    kind: "other",
    ts: 1,
    ...over,
  } as EntryToolCall;
  return { id: `${turn}-c${String(n)}`, turn, kind: "tool_call", seq: n, ts: n + 1, payload };
}

function hook(turn: string, n: number, name: string, over: Partial<EntryToolCall> = {}): Entry {
  return call(turn, n, {
    title: `Hook fired: ${name}`,
    kind: "hook",
    source_path: `.kiro/hooks/${name.replaceAll(" ", "-")}.json`,
    hook_trigger: "PreToolUse",
    ...over,
  } as Partial<EntryToolCall>);
}

function say(turn: string, n: number, text: string): Entry {
  return {
    id: `${turn}-say#${String(n)}`,
    turn,
    kind: "text",
    seq: n,
    ts: n + 1,
    payload: { text },
  };
}

/** Mount one finished turn card at the top of the page, its body built by the production renderer. */
function render(build: (turn: string) => Entry[]): HTMLElement {
  seq++;
  const id = `t-box-hooks-${String(seq)}`;
  const turn = document.createElement("div");
  turn.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  turn.append(body);
  document.body.prepend(turn);
  hosts.push(turn);
  const t: Turn = {
    id,
    n: 1,
    trigger: { id: `${id}-p`, text: "go" },
    body: build(id),
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
  buildAssistantBody(body, t, CHAT_ID, false);
  return body;
}

const seen = (e: Element): boolean => e.checkVisibility();

/** The tool boxes a reader sees, hook rows left out. */
function toolBoxes(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(".tool-call")].filter(
    (c) => c.dataset["kind"] !== "hook" && seen(c),
  );
}

function hookRows(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(".tool-call[data-kind='hook']")].filter(seen);
}

/** Group headers a reader sees. */
function groupHeaders(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(".tool-group-header")].filter(seen);
}

function badges(root: HTMLElement): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>(".tool-group-hooks")].filter(seen);
}

function only<T>(list: readonly T[], what: string): T {
  expect(list, what).toHaveLength(1);
  return list[0] as T;
}

/** The header line of a tool box: its first row, where the badge sits. */
function headerLine(box: HTMLElement): DOMRect {
  const h = box.querySelector<HTMLElement>(".tool-header");
  if (h === null) {
    throw new Error("tool box without a header");
  }
  return h.getBoundingClientRect();
}

/** The badge lies on `line`'s row at its end: inside its block span, past its middle, and inside its end edge. */
function expectOnLine(badge: HTMLElement, line: DOMRect): void {
  const b = badge.getBoundingClientRect();
  expect(b.top, "badge top inside the line").toBeGreaterThanOrEqual(line.top);
  expect(b.bottom, "badge bottom inside the line").toBeLessThanOrEqual(line.bottom);
  expect(b.right, "badge inside the line's end edge").toBeLessThanOrEqual(line.right);
  expect(b.left, "badge at the line's end").toBeGreaterThan(line.left + line.width / 2);
}

async function openList(badge: HTMLButtonElement): Promise<string[]> {
  await userEvent.click(badge);
  const panel = document.querySelector<HTMLElement>(".tool-hook-popover.is-open");
  expect(panel, "the hooks list opened").not.toBeNull();
  return [...(panel?.querySelectorAll<HTMLElement>(".tool-hook-item .tool-title") ?? [])].map(
    (t) => t.textContent,
  );
}

describe("a lone tool call with hooks", () => {
  it("is one tool box with no group, the badge on its header line listing its 3 hooks", async () => {
    const root = render((t) => [
      call(t, 1, { output: "sent" }),
      hook(t, 2, "seat guard"),
      hook(t, 3, "destructive guard"),
      hook(t, 4, "notify", { hook_trigger: "PostToolUse" }),
    ]);
    expect(groupHeaders(root), "no group header").toHaveLength(0);
    const box = only(toolBoxes(root), "one tool box");
    expect(hookRows(root), "no hook rows").toHaveLength(0);
    const badge = only(badges(root), "one hooks badge");
    expectOnLine(badge, headerLine(box));
    expect(badge.getAttribute("aria-label")).toBe("3 hooks ran");
    const toggle = box.querySelector<HTMLElement>(".tool-disclosure");
    const before = toggle?.getAttribute("aria-expanded");
    expect(await openList(badge)).toEqual(["seat guard", "destructive guard", "notify"]);
    expect(toggle?.getAttribute("aria-expanded"), "the box's own fold is untouched").toBe(before);
    await userEvent.keyboard("{Escape}");
  });

  it("keeps a failed hook's row inside the tool box", () => {
    const root = render((t) => [
      call(t, 1),
      hook(t, 2, "seat guard"),
      hook(t, 3, "broken", { status: "failed" }),
    ]);
    expect(groupHeaders(root), "no group header").toHaveLength(0);
    const box = only(toolBoxes(root), "one tool box");
    const row = only(hookRows(root), "the failed hook's row");
    expect(row.dataset["outcome"]).toBe("fail");
    // The box is the bordered shell the tool's header line opens; the failed row sits inside it, below that line.
    const shell = box.closest<HTMLElement>(".tool-group") ?? box;
    const s = shell.getBoundingClientRect();
    const r = row.getBoundingClientRect();
    expect(r.top).toBeGreaterThanOrEqual(headerLine(box).bottom);
    expect(r.left).toBeGreaterThanOrEqual(s.left);
    expect(r.right).toBeLessThanOrEqual(s.right);
    expect(r.bottom).toBeLessThanOrEqual(s.bottom);
    expect(getComputedStyle(shell).borderTopStyle, "one bordered box").toBe("solid");
    expectOnLine(only(badges(root), "the passing hook's badge"), headerLine(box));
  });
});

describe("a lone steering box with hooks", () => {
  it("is a plain steering card with the badge on its header's first line, clear of its title", async () => {
    const docs = Array.from({ length: 12 }, (_, i) => `a-long-steering-document-${String(i)}.md`);
    const root = render((t) => [
      call(t, 1, {
        title: `Steering loaded: ${docs.join(", ")}`,
        kind: "steering",
        steering_docs: docs,
      } as Partial<EntryToolCall>),
      hook(t, 2, "prompt check", { hook_trigger: "UserPromptSubmit" }),
    ]);
    // A frame, so the card's `content-visibility: auto` contents are laid out for the reader.
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
    expect(groupHeaders(root), "no group header").toHaveLength(0);
    const box = only(toolBoxes(root), "one steering card");
    expect(box.dataset["kind"]).toBe("steering");
    const badge = only(badges(root), "one hooks badge");
    const line = headerLine(box);
    const b = badge.getBoundingClientRect();
    expect(line.height, "premise: the title wraps").toBeGreaterThan(2 * b.height);
    expect(b.top).toBeGreaterThanOrEqual(line.top);
    expect(b.bottom, "on the first line").toBeLessThan(line.top + line.height / 2);
    expect(b.right).toBeLessThanOrEqual(line.right);
    const title = box.querySelector<HTMLElement>(".tool-title")?.getBoundingClientRect();
    expect(title?.right ?? Infinity, "the title stops short of the badge").toBeLessThanOrEqual(
      b.left,
    );
    expect(await openList(badge)).toEqual(["prompt check"]);
    await userEvent.keyboard("{Escape}");
  });
});

describe("two tool calls with hooks", () => {
  it("are one group, the badge on the group header, folding the turn's quiet stop hook too", async () => {
    const root = render((t) => [
      call(t, 1, { title: "Send Message" }),
      hook(t, 2, "seat guard"),
      call(t, 3, { title: "Read Inbox" }),
      hook(t, 4, "destructive guard"),
      say(t, 5, "done"),
      hook(t, 6, "stop check", { hook_trigger: "Stop" }),
    ]);
    const header = only(groupHeaders(root), "one group header");
    expect(toolBoxes(root)).toHaveLength(2);
    expect(hookRows(root), "no hook rows").toHaveLength(0);
    const badge = only(badges(root), "one hooks badge");
    expectOnLine(badge, header.getBoundingClientRect());
    expect(badge.getAttribute("aria-label")).toBe("3 hooks ran");
    expect(await openList(badge)).toEqual(["seat guard", "destructive guard", "stop check"]);
    await userEvent.keyboard("{Escape}");
  });
});

describe("a quiet turn hook", () => {
  it("folds a turn-end hook past the prose into the nearest tool box's badge", async () => {
    const root = render((t) => [
      call(t, 1),
      say(t, 2, "sent it"),
      hook(t, 3, "stop check", { hook_trigger: "Stop" }),
    ]);
    expect(hookRows(root), "no hook rows").toHaveLength(0);
    expect(groupHeaders(root), "no group header").toHaveLength(0);
    const box = only(toolBoxes(root), "one tool box");
    const badge = only(badges(root), "one hooks badge");
    expectOnLine(badge, headerLine(box));
    expect(badge.getAttribute("aria-label")).toBe("1 hook ran");
    expect(await openList(badge)).toEqual(["stop check"]);
    await userEvent.keyboard("{Escape}");
  });

  it("keeps its row in a turn with no tool box", () => {
    const root = render((t) => [
      hook(t, 1, "prompt check", { hook_trigger: "UserPromptSubmit" }),
      say(t, 2, "nothing to run"),
    ]);
    expect(hookRows(root).map((c) => c.dataset["title"])).toEqual(["Hook fired: prompt check"]);
    expect(badges(root)).toHaveLength(0);
  });
});

describe("the group header's count", () => {
  it("counts tool calls only: no header for one call and its hooks, Ran 2 calls for two", () => {
    const one = render((t) => [
      call(t, 1, { title: "Send Message" }),
      hook(t, 2, "seat guard"),
      hook(t, 3, "destructive guard"),
      hook(t, 4, "broken", { status: "failed" }),
    ]);
    expect(groupHeaders(one)).toHaveLength(0);
    const two = render((t) => [
      call(t, 1, { title: "Send Message" }),
      hook(t, 2, "seat guard"),
      call(t, 3, { title: "Read Inbox" }),
      hook(t, 4, "destructive guard"),
      hook(t, 5, "broken", { status: "failed" }),
    ]);
    const count = only(groupHeaders(two), "one group header").querySelector(".tool-group-count");
    expect(count?.textContent).toBe("Ran 2 calls: Send Message, Read Inbox \u00b7 1 failed");
  });
});
