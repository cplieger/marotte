// Tool groups are made of tool calls alone: a lone call with hooks is a plain tool box carrying the hooks badge on its
// own header line, and a quiet turn hook folds into the badge of the nearest box in its turn. Rendered through the
// production dispatcher with the shipped stylesheets, so what is asserted is what a reader sees.

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { Turn } from "./turns.js";
import { toolResultID } from "./entry-ids.js";
import type { Entry, EntryToolCall, EntryToolResult } from "./wire/types.gen.js";

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

const {
  buildAssistantBody,
  buildDetachedBody,
  resetBlockRenders,
  initBlockRenderer,
  dropHead,
  mountHeadRange,
} = await import("./messages-blocks.js");
const { forgetHeights, spacerHeight } = await import("./block-heights.js");
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
function mount(
  build: (turn: string) => Entry[],
  range?: { from: number; to: number },
): { body: HTMLElement; turn: Turn } {
  seq++;
  const id = `t-box-hooks-${String(seq)}`;
  const card = document.createElement("div");
  card.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  card.append(body);
  document.body.prepend(card);
  hosts.push(card);
  const turn: Turn = {
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
  forgetHeights([id]);
  buildAssistantBody(body, turn, CHAT_ID, false, range);
  return { body, turn };
}

function render(build: (turn: string) => Entry[]): HTMLElement {
  return mount(build).body;
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

  it("keeps a failed hook's row inside the tool box, its badge listing every hook", async () => {
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
    const badge = only(badges(root), "the box's hooks badge");
    expectOnLine(badge, headerLine(box));
    expect(badge.getAttribute("aria-label")).toBe("2 hooks ran");
    expect(await openList(badge)).toEqual(["seat guard", "broken"]);
    await userEvent.keyboard("{Escape}");
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

describe("hook rows with no tool call between them", () => {
  it("share one box with no header in a turn that has a tool box", () => {
    const root = render((t) => [
      call(t, 1),
      say(t, 2, "done"),
      hook(t, 3, "stop a", { hook_trigger: "Stop", status: "failed" }),
      hook(t, 4, "stop b", { hook_trigger: "Stop", status: "failed" }),
    ]);
    expect(groupHeaders(root), "no group header").toHaveLength(0);
    const rows = hookRows(root);
    expect(rows.map((c) => c.dataset["title"])).toEqual([
      "Hook fired: stop a",
      "Hook fired: stop b",
    ]);
    const shell = rows[0]?.closest<HTMLElement>(".tool-group");
    expect(rows[1]?.closest(".tool-group"), "one box").toBe(shell);
    expect(getComputedStyle(shell ?? document.body).borderTopStyle).toBe("solid");
  });

  /** A tool box, then prose, then two failed turn-end hook rows and more prose. */
  const boxThenRows = (t: string): Entry[] => [
    call(t, 1),
    say(t, 2, "done"),
    hook(t, 3, "stop a", { hook_trigger: "Stop", status: "failed" }),
    hook(t, 4, "stop b", { hook_trigger: "Stop", status: "failed" }),
    say(t, 5, "bye"),
  ];

  it("keep no header when the window drops the turn's tool box", () => {
    const { body, turn } = mount(boxThenRows);
    expect(groupHeaders(body), "premise: no header").toHaveLength(0);
    dropHead(turn, { from: 2, to: 6 });
    expect(toolBoxes(body), "premise: the tool box left the window").toHaveLength(0);
    expect(groupHeaders(body), "no header").toHaveLength(0);
    expect(hookRows(body)).toHaveLength(2);
  });

  it("keep no header when a head extension gives the box back", () => {
    const { body, turn } = mount(boxThenRows);
    dropHead(turn, { from: 2, to: 6 });
    mountHeadRange(turn, { from: 0, to: 6 }, false);
    only(toolBoxes(body), "premise: the tool box is back");
    expect(groupHeaders(body), "no header").toHaveLength(0);
    expect(hookRows(body)).toHaveLength(2);
  });

  it("start with no header when only the turn's tail mounts", () => {
    const { body } = mount(boxThenRows, { from: 2, to: 6 });
    expect(toolBoxes(body), "premise: the tool box is not mounted").toHaveLength(0);
    expect(groupHeaders(body), "no header").toHaveLength(0);
    expect(hookRows(body)).toHaveLength(2);
  });

  it("are a group counting them in a turn with no tool box", () => {
    const root = render((t) => [
      hook(t, 1, "stop a", { hook_trigger: "Stop", status: "failed" }),
      hook(t, 2, "stop b", { hook_trigger: "Stop", status: "failed" }),
    ]);
    const count = only(groupHeaders(root), "one group header").querySelector(".tool-group-count");
    expect(count?.textContent).toMatch(/^Ran 2 hooks/);
    expect(hookRows(root)).toHaveLength(2);
    expect(badges(root)).toHaveLength(0);
  });
});

describe("a delegate's page", () => {
  const LANE = "sub-1";
  /** `e` as the live path persists a delegate's own entry: in its lane, a call stamped with the delegate's id. */
  function nested(e: Entry): Entry {
    const call =
      e.kind === "tool_call"
        ? { ...(e.payload as EntryToolCall), agent_subtask_id: LANE }
        : e.payload;
    return { ...e, lane: LANE, payload: call };
  }

  function renderPage(build: (turn: string) => Entry[]): HTMLElement {
    const { body, turn } = mount((t) => [
      call(t, 1, { title: "invokeSubAgent", agent_subtask_id: LANE }),
      ...build(t).map(nested),
    ]);
    const page = document.createElement("div");
    page.className = "turn-body";
    body.closest(".turn")?.append(page);
    buildDetachedBody(page, turn, CHAT_ID, LANE, false);
    return page;
  }

  it("keeps hook rows past the prose headerless beside the delegate's tool box", () => {
    const page = renderPage((t) => [
      call(t, 2),
      say(t, 3, "done"),
      hook(t, 4, "stop a", { hook_trigger: "Stop", status: "failed" }),
      hook(t, 5, "stop b", { hook_trigger: "Stop", status: "failed" }),
    ]);
    only(toolBoxes(page), "premise: the delegate's tool box mounted");
    expect(groupHeaders(page), "no group header").toHaveLength(0);
    expect(hookRows(page)).toHaveLength(2);
  });

  it("groups two adjacent stamped calls as the chat groups unstamped ones", () => {
    const page = renderPage((t) => [call(t, 2), call(t, 3, { title: "Read Inbox" })]);
    const count = only(groupHeaders(page), "one group header").querySelector(".tool-group-count");
    expect(count?.textContent).toBe("Ran 2 calls: Send Message, Read Inbox");
  });
});

describe("the spacer standing for a dropped box", () => {
  /** How far `next` starts below `box`'s top, less the one row gap between them: what a head spacer must hold. */
  function heldAbove(box: HTMLElement, next: HTMLElement, body: HTMLElement): number {
    const gap = Number.parseFloat(getComputedStyle(body).rowGap);
    return next.getBoundingClientRect().top - box.getBoundingClientRect().top - gap;
  }

  function row(body: HTMLElement, at: number): HTMLElement {
    const r = body.querySelector<HTMLElement>(`:scope > [data-entry-seq="${String(at)}"]`);
    if (r === null) {
      throw new Error(`no row at ${String(at)}`);
    }
    return r;
  }

  it("holds a lone tool box with its quiet hooks at its open height", async () => {
    const output = Array.from({ length: 30 }, (_, i) => `line ${String(i)}`).join("\n");
    const { body, turn } = mount((t) => [
      call(t, 1, { output }),
      hook(t, 2, "seat guard"),
      hook(t, 3, "notify", { hook_trigger: "PostToolUse" }),
      say(t, 4, "sent"),
    ]);
    const box = only(toolBoxes(body), "one tool box");
    box.querySelector<HTMLElement>(".tool-disclosure")?.click();
    await vi.waitFor(() => {
      expect(box.getAnimations({ subtree: true })).toHaveLength(0);
    });
    const shell = box.closest<HTMLElement>(".tool-group") ?? box;
    const held = heldAbove(shell, row(body, 4), body);
    expect(held, "premise: the open box is taller than a folded group").toBeGreaterThan(200);
    dropHead(turn, { from: 4, to: 5 });
    expect(spacerHeight(turn, { from: 4, to: 5 }, "head", "")).toBe(held);
  });

  /** The tail spacer for everything past `say(1)`, priced from estimates alone. */
  function unmeasuredTail(build: (turn: string) => Entry[]): number {
    const { turn } = mount(build, { from: 0, to: 2 });
    forgetHeights([turn.id]);
    return spacerHeight(turn, { from: 0, to: 2 }, "tail", "");
  }

  it("prices a never-mounted lone tool box as one card, its quiet hooks adding nothing", () => {
    const bare = unmeasuredTail((t) => [say(t, 1, "go"), call(t, 2)]);
    const hooked = unmeasuredTail((t) => [
      say(t, 1, "go"),
      call(t, 2),
      hook(t, 3, "seat guard"),
      hook(t, 4, "notify", { hook_trigger: "PostToolUse" }),
    ]);
    expect(hooked).toBe(bare);
  });

  it("prices a never-mounted failed lone box's quiet tool-trigger hook as its row", () => {
    const quiet = unmeasuredTail((t) => [
      say(t, 1, "go"),
      call(t, 2, { status: "failed" }),
      hook(t, 3, "seat guard"),
    ]);
    const loud = unmeasuredTail((t) => [
      say(t, 1, "go"),
      call(t, 2, { status: "failed" }),
      hook(t, 3, "seat guard", { status: "failed" }),
    ]);
    expect(quiet).toBe(loud);
  });

  /** The tail priced for a lone box whose result is `result`, its tool-trigger hook quiet or failed. */
  function settledTail(result: EntryToolResult, hookStatus: "completed" | "failed"): number {
    return unmeasuredTail((t) => [
      say(t, 1, "go"),
      call(t, 2),
      { id: toolResultID(`${t}-c2`), turn: t, kind: "tool_result", seq: 3, ts: 4, payload: result },
      hook(t, 4, "seat guard", { status: hookStatus }),
    ]);
  }

  it.each<[string, EntryToolResult]>([
    [
      "denied",
      {
        status: "completed",
        denial: {
          capability: "fs.write",
          resource: "/etc/passwd",
          scope: "path",
          source: "policy",
        },
      },
    ],
    ["declined", { status: "completed", declined: true }],
    [
      "aborted after a rejected ask",
      {
        status: "aborted",
        interaction: { type: "tool_approval", outcome: "selected", choice: "reject_once" },
      },
    ],
  ])(
    "prices a never-mounted lone box %s with its quiet tool-trigger hook as a row",
    (_, result) => {
      expect(settledTail(result, "completed")).toBe(settledTail(result, "failed"));
    },
  );

  it("prices a never-mounted turn hook folded into another box's badge at nothing", () => {
    const { turn } = mount((t) => [
      call(t, 1),
      say(t, 2, "sent it"),
      hook(t, 3, "stop check", { hook_trigger: "Stop" }),
      say(t, 4, "bye"),
    ]);
    forgetHeights([turn.id]);
    const withHook = spacerHeight(turn, { from: 4, to: 5 }, "head", "");
    expect(withHook).toBe(spacerHeight(turn, { from: 3, to: 5 }, "head", ""));
  });

  it("prices a never-mounted turn hook's row in a turn with no tool box", () => {
    const row = unmeasuredTail((t) => [
      say(t, 1, "go"),
      hook(t, 2, "prompt check", { hook_trigger: "UserPromptSubmit" }),
    ]);
    expect(row).toBe(unmeasuredTail((t) => [say(t, 1, "go"), call(t, 2)]));
  });

  it("adds nothing for a turn hook folded into another box's badge", () => {
    const { body, turn } = mount((t) => [
      call(t, 1),
      say(t, 2, "sent it"),
      hook(t, 3, "stop check", { hook_trigger: "Stop" }),
      say(t, 4, "bye"),
    ]);
    expect(hookRows(body), "premise: the stop hook folded").toHaveLength(0);
    const box = only(toolBoxes(body), "one tool box");
    const held = heldAbove(box.closest<HTMLElement>(".tool-group") ?? box, row(body, 4), body);
    dropHead(turn, { from: 4, to: 5 });
    expect(spacerHeight(turn, { from: 4, to: 5 }, "head", "")).toBe(held);
  });
});
