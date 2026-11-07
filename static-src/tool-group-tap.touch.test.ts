// A real tap on the header of a tool group the transcript rendered already folded: one tap opens it,
// the next closes it, as on a mouse.

import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as Scroll from "./scroll.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

// The import graph reaches the DOM registry, which throws on a missing app root. Out of layout: the
// stylesheet would size the stand-ins over the transcript and take the tap.
const roots = document.createElement("div");
roots.style.display = "none";
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  roots.appendChild(d);
}
document.body.appendChild(roots);

const { buildAssistantBody, initBlockRenderer } = await import("./messages-blocks.js");
const { setActive } = await import("./store.js");
const { pressHold } = await import("./scroll.js");
const { dispatchTouch, frames } = await import("./__test-helpers__/turn-rail-dom.js");

const CHAT_ID = "c-group-tap";
setActive(CHAT_ID);
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

function callEntry(turn: string, seq: number, kind: EntryToolCall["kind"], title: string): Entry {
  const payload: EntryToolCall = {
    id: `tool-${String(seq)}`,
    title,
    status: "completed",
    kind,
    ts: 1,
  };
  return { id: `${turn}-c${String(seq)}`, turn, kind: "tool_call", seq, ts: seq + 1, payload };
}

let host: HTMLElement | null = null;
let style: HTMLStyleElement | null = null;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  // An iPad in portrait.
  await page.viewport(834, 1194);
});

afterAll(async () => {
  style?.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

afterEach(() => {
  host?.remove();
  host = null;
});

/** A finished turn whose mixed run of calls is followed by prose, so the renderer folds the group. */
function foldedGroup(): HTMLElement {
  const turn = "t-tap";
  const body: Entry[] = [
    callEntry(turn, 1, "hook", "hook a"),
    callEntry(turn, 2, "hook", "hook b"),
    callEntry(turn, 3, "execute", "npm test"),
    { id: `${turn}-say#4`, turn, kind: "text", seq: 4, ts: 5, payload: { text: "and so" } },
  ];
  host = document.createElement("div");
  host.className = "turn-body";
  document.body.prepend(host);
  buildAssistantBody(
    host,
    {
      id: turn,
      n: 1,
      trigger: { id: `${turn}-p`, text: "go" },
      body,
      openEntries: new Map(),
      ts: 1,
      outcome: "completed",
      rewindTo: undefined,
      branchFrom: undefined,
    },
    CHAT_ID,
    false,
  );
  const group = host.querySelector<HTMLElement>(".tool-group");
  expect(group, "the renderer built a group").not.toBeNull();
  return group as HTMLElement;
}

async function tap(target: HTMLElement): Promise<void> {
  const r = target.getBoundingClientRect();
  const at = { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  expect(
    document.elementFromPoint(at.x, at.y)?.closest(".tool-group-header"),
    "the finger lands on the header",
  ).toBe(target);
  await dispatchTouch("touchStart", at);
  await dispatchTouch("touchEnd");
  await frames();
}

function shown(group: HTMLElement): { expanded: string | null; inert: boolean } {
  const body = group.querySelector<HTMLElement>(":scope > .tool-group-body");
  return {
    expanded:
      group.querySelector(":scope > .tool-group-header")?.getAttribute("aria-expanded") ?? null,
    inert: body?.inert ?? true,
  };
}

describe("a tap on a folded tool group's header", () => {
  it("opens it on the first tap and closes it on the second", async () => {
    const group = foldedGroup();
    const header = group.querySelector<HTMLElement>(":scope > .tool-group-header");
    const body = group.querySelector<HTMLElement>(":scope > .tool-group-body");
    expect(shown(group), "rendered folded").toEqual({ expanded: "false", inert: true });
    expect(body?.getBoundingClientRect().height).toBe(0);

    await tap(header as HTMLElement);
    expect(shown(group), "one tap opens").toEqual({ expanded: "true", inert: false });
    await vi.waitFor(() => {
      expect(body?.getBoundingClientRect().height).toBeGreaterThan(0);
    });
    expect(vi.mocked(pressHold).mock.calls).toEqual([[]]);

    await tap(header as HTMLElement);
    expect(shown(group), "the next tap closes").toEqual({ expanded: "false", inert: true });
    await vi.waitFor(() => {
      expect(body?.getBoundingClientRect().height).toBe(0);
    });
  });
});
