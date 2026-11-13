// The run pane's step transcript holds a window of a long step, as a long chat does, and building
// it costs what it mounts: no slide geometry for traces sealed by the build, and one header refresh,
// which folds the group's hooks, per tool group.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { Turn } from "./turns.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import type { RunStepStream } from "./run-chat-steps.js";

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
    document.body.appendChild(d);
  }
}

vi.mock("./tool-group.js", { spy: true });

const { resetBlockRenders, initBlockRenderer } = await import("./messages-blocks.js");
const { clearAllEntrySigs, toolCallSigs } = await import("./store-signals.js");
const { initToolCallbacks } = await import("./messages-tools.js");
const { refreshGroupHeader } = await import("./tool-group.js");
const { setHookStatusShown } = await import("./hook-fold.js");
const { createRunStepStream } = await import("./run-chat-steps.js");
const { createFollowScroller } = await import("./scroll-controller.js");

initToolCallbacks({ refreshGroupHeader });
initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
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

const NODE = "build";
/** The step pane's residency budget: its newest window plus the overscan floor, and its tool-card
 *  share. */
const STEP_WINDOW_ENTRIES = 96 + 24;
const STEP_WINDOW_TOOL_CALLS = 32;

function at(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-${String(seq)}`, turn: turnID, kind, seq, ts: seq + 1, payload } as Entry;
}

function call(turnID: string, seq: number, over: Partial<EntryToolCall> = {}): Entry {
  const payload: EntryToolCall = {
    id: `tool-${turnID}-${String(seq)}`,
    title: `Run Command ${String(seq)}`,
    status: "completed",
    kind: "execute",
    ts: 1,
    ...over,
  };
  return at(turnID, seq, "tool_call", payload);
}

function hook(turnID: string, seq: number): Entry {
  return call(turnID, seq, {
    title: `Hook fired: guard ${String(seq)}`,
    kind: "hook",
    source_path: `.kiro/hooks/guard-${String(seq)}.json`,
    hook_trigger: "PreToolUse",
  } as Partial<EntryToolCall>);
}

/** A step turn: `turn_open`, then `fill` writing from seq 1 on. */
function stepTurn(turnID: string, fill: (seq: () => number) => Entry[]): Turn {
  let next = 0;
  const seq = (): number => ++next;
  const body = [
    at(turnID, 0, "turn_open", { source: "workflow_step", node_path: NODE, n: 1 }),
    ...fill(seq),
  ];
  return {
    id: turnID,
    n: 1,
    trigger: undefined,
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

let scroller: HTMLElement;
let host: HTMLElement;
let stream: RunStepStream;

beforeEach(() => {
  resetBlockRenders();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
  setHookStatusShown(true);
  scroller = document.createElement("div");
  scroller.style.cssText = "height: 600px; overflow-y: auto;";
  host = document.createElement("div");
  host.className = "ev-d-body";
  scroller.append(host);
  document.body.appendChild(scroller);
  stream = createRunStepStream(() => host);
  vi.mocked(refreshGroupHeader).mockClear();
});

afterEach(() => {
  vi.restoreAllMocks();
  stream.dispose();
  scroller.remove();
  resetBlockRenders();
});

const mountedEntries = (): number => host.querySelectorAll("[data-entry-seq]").length;
const mountedCalls = (): number => host.querySelectorAll(".tool-call").length;

/** 2,000 entries: runs of prose and groups of tool calls, 300 calls in all. */
function longStep(turnID: string): Turn {
  return stepTurn(turnID, (seq) => {
    const out: Entry[] = [];
    while (out.length < 1999) {
      for (let i = 0; i < 17 && out.length < 1999; i++) {
        out.push(at(turnID, seq(), "text", { text: `line ${String(out.length)}\n\n` }));
      }
      for (let i = 0; i < 3 && out.length < 1999; i++) {
        out.push(call(turnID, seq()));
      }
    }
    return out;
  });
}

describe("a long step in the run pane", () => {
  it("mounts its newest window, not the whole step", () => {
    stream.apply(new Map([[NODE, [{ turn: longStep("t-long"), live: true }]]]));

    expect(mountedEntries()).toBeGreaterThan(0);
    expect(mountedEntries()).toBeLessThanOrEqual(STEP_WINDOW_ENTRIES);
    expect(mountedCalls()).toBeLessThanOrEqual(STEP_WINDOW_TOOL_CALLS);
  });

  it("seats one older slice, within the same budget, when the reader scrolls to its top", async () => {
    // The run tab's own wiring: the follower over the page body, the step's host inside it.
    stream.dispose();
    const view = document.createElement("div");
    scroller.replaceChildren(view);
    view.append(host);
    const resume = document.createElement("button");
    document.body.append(resume);
    const follower = createFollowScroller(view, scroller, resume);
    follower.attach({ el: view, scrollTop: 0, readingState: "following" });
    stream = createRunStepStream(() => host, follower);
    stream.apply(new Map([[NODE, [{ turn: longStep("t-older"), live: true }]]]));
    const first = mountedEntries();
    expect(first).toBeLessThanOrEqual(STEP_WINDOW_ENTRIES);
    await expect.poll(() => scroller.scrollTop).toBeGreaterThan(0);
    // Following the live edge over content that overflows asks for nothing older.
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => requestAnimationFrame(r));
    }
    expect(mountedEntries()).toBe(first);

    scroller.scrollTop = 0;
    scroller.dispatchEvent(new Event("scroll"));
    await expect.poll(() => mountedEntries()).toBeGreaterThan(first);

    expect(mountedEntries() - first).toBeLessThanOrEqual(STEP_WINDOW_ENTRIES);
    follower.detach();
    resume.remove();
  });
});

describe("a long step streaming under a following reader", () => {
  it("unmounts the rows far above the reader as the step grows", async () => {
    stream.dispose();
    const view = document.createElement("div");
    scroller.replaceChildren(view);
    view.append(host);
    const resume = document.createElement("button");
    document.body.append(resume);
    const follower = createFollowScroller(view, scroller, resume);
    follower.attach({ el: view, scrollTop: 0, readingState: "following" });
    stream = createRunStepStream(() => host, follower);
    const whole = longStep("t-grow");
    // The step as it streams: its first 200 entries, then 100 more per frame.
    for (let n = 200; n <= whole.body.length; n += 100) {
      const turn = { ...whole, body: whole.body.slice(0, n) };
      stream.apply(new Map([[NODE, [{ turn, live: true }]]]));
      await new Promise((r) => requestAnimationFrame(r));
    }

    const newest = whole.body[whole.body.length - 1];
    expect(host.querySelector(`[data-entry-seq="${String(newest?.seq)}"]`)).not.toBeNull();
    expect(mountedEntries()).toBeLessThan(600);
    follower.detach();
    resume.remove();
  });
});

describe("building a step's body", () => {
  it("reads no slide geometry for the thinking blocks the build seals", () => {
    const rects = vi.spyOn(Element.prototype, "getBoundingClientRect");
    const clientRects = vi.spyOn(Element.prototype, "getClientRects");
    const turn = stepTurn("t-think", (seq) =>
      Array.from({ length: 50 }, (_, i) => [
        at("t-think", seq(), "thinking", { text: `weighing option ${String(i)}` }),
        at("t-think", seq(), "text", { text: `chose ${String(i)}` }),
      ]).flat(),
    );

    stream.apply(new Map([[NODE, [{ turn, live: false }]]]));

    const onTraces = (calls: readonly unknown[]): number =>
      calls.filter((c) => c instanceof Element && c.closest(".reasoning-block") !== null).length;
    expect(host.querySelectorAll(".reasoning-block").length).toBeGreaterThan(20);
    expect(onTraces(rects.mock.contexts)).toBe(0);
    expect(onTraces(clientRects.mock.contexts)).toBe(0);
  });

  it("refreshes each tool group's header once", () => {
    const turn = stepTurn("t-groups", (seq) =>
      Array.from({ length: 40 }, () => [
        at("t-groups", seq(), "text", { text: "next\n\n" }),
        ...Array.from({ length: 5 }, () => call("t-groups", seq())),
      ]).flat(),
    );

    stream.apply(new Map([[NODE, [{ turn, live: false }]]]));

    const groups = host.querySelectorAll(".tool-group").length;
    expect(groups).toBeGreaterThan(3);
    expect(mountedCalls()).toBeGreaterThan(groups);
    expect(vi.mocked(refreshGroupHeader).mock.calls.length).toBeLessThanOrEqual(groups);
  });

  it("folds a group's quiet hooks into its badge once, not once per hook card", () => {
    const turn = stepTurn("t-hooks", (seq) =>
      Array.from({ length: 10 }, () => [
        at("t-hooks", seq(), "text", { text: "next\n\n" }),
        call("t-hooks", seq()),
        ...Array.from({ length: 12 }, () => hook("t-hooks", seq())),
      ]).flat(),
    );

    stream.apply(new Map([[NODE, [{ turn, live: false }]]]));

    // A group the window's head cuts through holds no work card, so it carries no badge of its own.
    const withWork = [...host.querySelectorAll<HTMLElement>(".tool-group")].filter(
      (g) => g.querySelector('.tool-call:not([data-kind="hook"])') !== null,
    );
    expect(withWork.length).toBeGreaterThan(1);
    for (const g of withWork) {
      expect(g.querySelector(".tool-group-hooks")?.textContent).toContain("12");
    }
    expect(vi.mocked(refreshGroupHeader).mock.calls.length).toBeLessThanOrEqual(
      host.querySelectorAll(".tool-group").length,
    );
  });
});
